package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	agentv1 "github.com/gugumanager/gugumanager/api/proto/gugumanager/agent/v1"
	runnerv1 "github.com/gugumanager/gugumanager/api/proto/gugumanager/extension/runner/v1"
	"google.golang.org/protobuf/proto"
)

const (
	extensionInvokeTypeURL = "type.gugumanager.io/gugumanager.extension.runner.v1.Invoke"
	runnerMaxFrameBytes    = 16 << 20
)

func (e *DockerExecutor) executeExtension(ctx context.Context, task *agentv1.Task) (*ExecutionOutcome, error) {
	payload := task.GetExtension()
	if payload == nil || payload.GetTypeUrl() != extensionInvokeTypeURL || strings.TrimSpace(e.runnerPath) == "" {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "EXTENSION_UNSUPPORTED", Retryable: false}, nil
	}
	invoke := &runnerv1.Invoke{}
	if err := proto.Unmarshal(payload.GetValue(), invoke); err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "EXTENSION_INVALID", Retryable: false}, nil
	}
	if invoke.GetOperationId() != task.GetOperationId() || invoke.GetServerId() != task.GetServerId() ||
		invoke.GetBundleDigest() != task.GetBundleDigest() {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "EXTENSION_IDENTITY_MISMATCH", Retryable: false}, nil
	}
	// The control plane cannot choose an arbitrary host path. The Runner opens
	// only this Agent-owned server directory as a capability directory.
	invoke.ServerRoot = filepath.Join(e.dataRoot, task.GetServerId())

	response, stderr, err := invokeRunnerProcess(ctx, e.runnerPath, task.GetOperationId(), invoke)
	if err != nil {
		result, _ := json.Marshal(map[string]string{"detail": truncateRunnerText(stderr, 4096)})
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "EXTENSION_RUNNER_FAILED", Retryable: true, ResultJSON: result}, nil
	}
	result := response.GetResult()
	if result == nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "EXTENSION_PROTOCOL_ERROR", Retryable: false}, nil
	}
	encoded, _ := json.Marshal(map[string]any{
		"outputBase64": base64.StdEncoding.EncodeToString(result.GetOutput()),
		"detail":       truncateRunnerText(result.GetDetail(), 4096),
	})
	if !result.GetSucceeded() {
		code := result.GetErrorCode()
		if code == "" {
			code = "EXTENSION_FAILED"
		}
		return &ExecutionOutcome{Succeeded: false, ErrorCode: code, Retryable: false, ResultJSON: encoded}, nil
	}
	return &ExecutionOutcome{Succeeded: true, ResultJSON: encoded}, nil
}

func invokeRunnerProcess(ctx context.Context, runnerPath, requestID string, invoke *runnerv1.Invoke) (*runnerv1.Response, string, error) {
	command := exec.CommandContext(ctx, runnerPath)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, "", err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, "", err
	}
	stderrPipe, err := command.StderrPipe()
	if err != nil {
		return nil, "", err
	}
	var stderr cappedBuffer
	var drain sync.WaitGroup
	drain.Add(1)
	go func() {
		defer drain.Done()
		_, _ = io.Copy(&stderr, stderrPipe)
	}()
	if err := command.Start(); err != nil {
		return nil, "", err
	}
	fail := func(err error) (*runnerv1.Response, string, error) {
		_ = stdin.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
		drain.Wait()
		return nil, stderr.String(), err
	}

	hello := &runnerv1.Request{RequestId: requestID, Payload: &runnerv1.Request_Hello{Hello: &runnerv1.Hello{ProtocolVersions: []uint32{1}}}}
	if err := writeRunnerFrame(stdin, hello); err != nil {
		return fail(err)
	}
	negotiated := &runnerv1.Response{}
	if err := readRunnerFrame(stdout, negotiated); err != nil {
		return fail(err)
	}
	if negotiated.GetRequestId() != requestID || negotiated.GetHello().GetProtocolVersion() != 1 {
		return fail(errors.New("runner protocol negotiation failed"))
	}
	request := &runnerv1.Request{RequestId: requestID, Payload: &runnerv1.Request_Invoke{Invoke: invoke}}
	if err := writeRunnerFrame(stdin, request); err != nil {
		return fail(err)
	}
	_ = stdin.Close()
	for {
		response := &runnerv1.Response{}
		if err := readRunnerFrame(stdout, response); err != nil {
			return fail(err)
		}
		if response.GetRequestId() != requestID {
			return fail(errors.New("runner response request_id mismatch"))
		}
		if response.GetResult() == nil {
			continue
		}
		if err := command.Wait(); err != nil {
			drain.Wait()
			return nil, stderr.String(), err
		}
		drain.Wait()
		return response, stderr.String(), nil
	}
}

func writeRunnerFrame(writer io.Writer, message proto.Message) error {
	payload, err := proto.Marshal(message)
	if err != nil {
		return err
	}
	if len(payload) == 0 || len(payload) > runnerMaxFrameBytes {
		return errors.New("runner frame exceeds limit")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := writer.Write(header[:]); err != nil {
		return err
	}
	_, err = writer.Write(payload)
	return err
}

func readRunnerFrame(reader io.Reader, message proto.Message) error {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return err
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 || length > runnerMaxFrameBytes {
		return errors.New("runner frame length is invalid")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return err
	}
	if err := proto.Unmarshal(payload, message); err != nil {
		return fmt.Errorf("decode runner frame: %w", err)
	}
	return nil
}

type cappedBuffer struct {
	buffer bytes.Buffer
}

func (b *cappedBuffer) Write(payload []byte) (int, error) {
	const limit = 64 << 10
	remaining := limit - b.buffer.Len()
	if remaining > 0 {
		_, _ = b.buffer.Write(payload[:min(remaining, len(payload))])
	}
	return len(payload), nil
}

func (b *cappedBuffer) String() string { return b.buffer.String() }

func truncateRunnerText(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
