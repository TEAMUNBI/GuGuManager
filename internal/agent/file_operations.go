package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/gugumanager/gugumanager/api/proto/gugumanager/agent/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	containerDataDir = "/data"
	maxReadBytes     = 10 << 20 // 10 MiB
	maxWriteBytes    = 10 << 20 // 10 MiB
)

// FileEntry is a single entry in a directory listing result.
type FileEntry struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Directory  bool      `json:"directory"`
	SizeBytes  int64     `json:"sizeBytes"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

// FileReadResult holds the content of a read file.
type FileReadResult struct {
	Content    []byte
	Base64     bool
	SizeBytes  int64
	ModifiedAt time.Time
	Filename   string
}

// ExecuteFileOperation dispatches a file operation to the container and returns
// a complete FileOperationResponse. The request_id is preserved by the caller.
//
// Write uses Docker's CopyArchiveToContainer API which works on stopped
// containers, so the game server process is never started just to save a file.
// Exec-based operations (list/read/mkdir/move/remove) briefly start the
// container if it was stopped and stop it again afterward to preserve the
// user's intended power state.
func (e *DockerExecutor) ExecuteFileOperation(ctx context.Context, req *agentv1.FileOperationRequest) *agentv1.FileOperationResponse {
	resp := &agentv1.FileOperationResponse{}
	rt, err := e.Runtime()
	if err != nil {
		resp.Succeeded = false
		resp.ErrorCode = "RUNTIME_UNAVAILABLE"
		return resp
	}
	containerName := fmt.Sprintf("gugu-server-%s", req.GetServerId())

	// Write uses CopyArchiveToContainer which works on stopped containers.
	// DownloadBackup reads the node-local backup archive and never touches the
	// container, so neither existence checks nor power-state round trips apply.
	if _, isWrite := req.GetOperation().(*agentv1.FileOperationRequest_Write); isWrite {
		if err := e.ensureContainerExists(ctx, rt, containerName); err != nil {
			resp.Succeeded = false
			resp.ErrorCode = "FILE_OPERATION_FAILED"
			return resp
		}
	} else if _, isDownloadBackup := req.GetOperation().(*agentv1.FileOperationRequest_DownloadBackup); !isDownloadBackup {
		wasRunning, err := e.ensureContainerRunning(ctx, rt, containerName)
		if err != nil {
			resp.Succeeded = false
			resp.ErrorCode = "FILE_OPERATION_FAILED"
			return resp
		}
		defer e.restoreContainerState(ctx, rt, containerName, wasRunning)
	}

	switch v := req.GetOperation().(type) {
	case *agentv1.FileOperationRequest_List:
		entries, err := listFiles(ctx, rt, containerName, v.List.GetPath())
		if err != nil {
			resp.ErrorCode = fileErrorCode(err)
			return resp
		}
		resp.Succeeded = true
		resp.Result = &agentv1.FileOperationResponse_List{List: &agentv1.FileOperationResponse_ListFilesResult{Entries: entries}}

	case *agentv1.FileOperationRequest_Read:
		result, err := readFile(ctx, rt, containerName, v.Read.GetPath())
		if err != nil {
			resp.ErrorCode = fileErrorCode(err)
			return resp
		}
		resp.Succeeded = true
		resp.Result = &agentv1.FileOperationResponse_Read{Read: &agentv1.FileOperationResponse_ReadFileResult{
			Content:    result.Content,
			Base64:     result.Base64,
			SizeBytes:  uint64(result.SizeBytes),
			ModifiedAt: timestamppb.New(result.ModifiedAt),
		}}

	case *agentv1.FileOperationRequest_Write:
		if err := writeFile(ctx, rt, containerName, v.Write.GetPath(), v.Write.GetContent(), v.Write.GetBase64()); err != nil {
			resp.ErrorCode = fileErrorCode(err)
			return resp
		}
		resp.Succeeded = true
		resp.Result = &agentv1.FileOperationResponse_Write{Write: &agentv1.FileOperationResponse_WriteFileResult{}}

	case *agentv1.FileOperationRequest_Mkdir:
		if err := makeDirectory(ctx, rt, containerName, v.Mkdir.GetPath()); err != nil {
			resp.ErrorCode = fileErrorCode(err)
			return resp
		}
		resp.Succeeded = true
		resp.Result = &agentv1.FileOperationResponse_Mkdir{Mkdir: &agentv1.FileOperationResponse_MakeDirectoryResult{}}

	case *agentv1.FileOperationRequest_Move:
		if err := moveFile(ctx, rt, containerName, v.Move.GetSource(), v.Move.GetDestination(), v.Move.GetReplace()); err != nil {
			resp.ErrorCode = fileErrorCode(err)
			return resp
		}
		resp.Succeeded = true
		resp.Result = &agentv1.FileOperationResponse_Move{Move: &agentv1.FileOperationResponse_MoveFileResult{}}

	case *agentv1.FileOperationRequest_Remove:
		if err := removeFile(ctx, rt, containerName, v.Remove.GetPath(), v.Remove.GetRecursive()); err != nil {
			resp.ErrorCode = fileErrorCode(err)
			return resp
		}
		resp.Succeeded = true
		resp.Result = &agentv1.FileOperationResponse_Remove{Remove: &agentv1.FileOperationResponse_RemoveFileResult{}}

	case *agentv1.FileOperationRequest_DownloadBackup:
		result, err := e.downloadBackup(ctx, v.DownloadBackup.GetBackupId())
		if err != nil {
			resp.ErrorCode = fileErrorCode(err)
			return resp
		}
		resp.Succeeded = true
		resp.Result = &agentv1.FileOperationResponse_DownloadBackup{DownloadBackup: &agentv1.FileOperationResponse_DownloadBackupResult{
			Content:   result.Content,
			Base64:    result.Base64,
			SizeBytes: uint64(result.SizeBytes),
			Filename:  result.Filename,
		}}

	default:
		resp.ErrorCode = "VALIDATION_FAILED"
	}
	return resp
}

// downloadBackup reads a backup archive from the node-local backup directory
// and returns it base64-encoded. Backups may be large, so no maxReadBytes cap
// is applied; the 512 MiB gRPC message limit bounds the response.
func (e *DockerExecutor) downloadBackup(ctx context.Context, backupID string) (*FileReadResult, error) {
	if strings.TrimSpace(backupID) == "" {
		return nil, fmt.Errorf("backup id required: %w", os.ErrInvalid)
	}
	archive := filepath.Join(e.dataRoot, "backups", backupID+".tar.gz")
	content, err := os.ReadFile(archive)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("read backup archive: %w", err)
	}
	return &FileReadResult{
		Content:    []byte(base64.StdEncoding.EncodeToString(content)),
		Base64:     true,
		SizeBytes:  int64(len(content)),
		Filename:   backupID + ".tar.gz",
		ModifiedAt: time.Now().UTC(),
	}, nil
}

// ensureContainerExists verifies the container has been provisioned without
// starting it. CopyArchiveToContainer works on stopped containers.
func (e *DockerExecutor) ensureContainerExists(ctx context.Context, rt containerRuntime, containerID string) error {
	if _, err := rt.InspectContainer(ctx, containerID); err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}
	return nil
}

// ensureContainerRunning checks whether the container is running and starts it
// if necessary. It returns wasRunning=true if the container was already running,
// so callers can decide whether to stop it after their operation completes.
func (e *DockerExecutor) ensureContainerRunning(ctx context.Context, rt containerRuntime, containerID string) (wasRunning bool, err error) {
	status, inspectErr := rt.InspectContainer(ctx, containerID)
	if inspectErr != nil {
		return false, fmt.Errorf("inspect container: %w", inspectErr)
	}
	if status.Running {
		return true, nil
	}
	if startErr := rt.StartContainer(ctx, containerID); startErr != nil {
		return false, fmt.Errorf("start container: %w", startErr)
	}
	return false, nil
}

// restoreContainerState stops the container if it was not previously running.
// Errors are logged but not returned because this runs in a defer after the
// primary operation has already succeeded or failed.
func (e *DockerExecutor) restoreContainerState(ctx context.Context, rt containerRuntime, containerID string, wasRunning bool) {
	if wasRunning {
		return
	}
	if err := rt.StopContainer(ctx, containerID, 10); err != nil {
		slog.Warn("file operation: failed to re-stop container",
			"container", containerID, "error", err)
	}
}

// safeContainerPath resolves a user-supplied relative path against /data and
// blocks traversal outside the data root. The empty string maps to /data.
func safeContainerPath(requested string) (string, error) {
	clean := path.Clean("/" + strings.TrimSpace(requested))
	if clean == "/" {
		return containerDataDir, nil
	}
	clean = strings.TrimPrefix(clean, "/")
	full := path.Join(containerDataDir, clean)
	if !strings.HasPrefix(full, containerDataDir+"/") && full != containerDataDir {
		return "", fmt.Errorf("path escapes container data root: %w", os.ErrPermission)
	}
	return full, nil
}

// listFiles lists directory contents via `ls -la --time-style=full-iso`.
func listFiles(ctx context.Context, rt containerRuntime, container, requested string) ([]*agentv1.FileOperationResponse_FileEntry, error) {
	dirPath, err := safeContainerPath(requested)
	if err != nil {
		return nil, err
	}
	output, err := rt.ExecInContainer(ctx, container, []string{
		"sh", "-c",
		fmt.Sprintf("ls -la --time-style=full-iso %s 2>&1", shellQuote(dirPath)),
	})
	if err != nil {
		return nil, fmt.Errorf("ls: %w", err)
	}
	if strings.Contains(output, "No such file") {
		return nil, os.ErrNotExist
	}
	if strings.Contains(output, "Permission denied") {
		return nil, os.ErrPermission
	}

	var entries []*agentv1.FileOperationResponse_FileEntry
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "total ") {
			continue
		}
		entry, ok := parseLsLine(line, requested)
		if !ok {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// parseLsLine parses a single `ls -la --time-style=full-iso` line:
// drwxr-xr-x 2 root root 4096 2024-01-15 10:30:00.000000000 +0000 name
func parseLsLine(line, requestedDir string) (*agentv1.FileOperationResponse_FileEntry, bool) {
	fields := strings.Fields(line)
	if len(fields) < 9 {
		return nil, false
	}
	perm := fields[0]
	if perm == "total" {
		return nil, false
	}
	sizeStr := fields[4]
	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil {
		size = 0
	}
	dateStr := fields[5] + " " + fields[6] + " " + fields[7]
	mt, err := time.Parse("2006-01-02 15:04:05.000000000 -0700", dateStr)
	if err != nil {
		mt = time.Now().UTC()
	}
	name := strings.Join(fields[8:], " ")
	if name == "." || name == ".." {
		return nil, false
	}
	relPath := path.Join(strings.TrimPrefix(requestedDir, "/"), name)
	if relPath[0] == '/' {
		relPath = relPath[1:]
	}
	return &agentv1.FileOperationResponse_FileEntry{
		Name:       name,
		Path:       "/" + relPath,
		Directory:  perm[0] == 'd',
		SizeBytes:  uint64(size),
		ModifiedAt: timestamppb.New(mt),
	}, true
}

// readFile reads a file from the container. Text files are read via `cat`;
// binary files (detected by null bytes) are base64-encoded.
func readFile(ctx context.Context, rt containerRuntime, container, requested string) (*FileReadResult, error) {
	filePath, err := safeContainerPath(requested)
	if err != nil {
		return nil, err
	}

	output, err := rt.ExecInContainer(ctx, container, []string{
		"sh", "-c",
		fmt.Sprintf("cat %s 2>&1; echo \"__EXIT__$?\"", shellQuote(filePath)),
	})
	if err != nil {
		return nil, fmt.Errorf("cat: %w", err)
	}

	idx := strings.LastIndex(output, "__EXIT__")
	if idx < 0 {
		return nil, fmt.Errorf("unexpected exec output")
	}
	content := output[:idx]
	exitCode := strings.TrimSpace(output[idx+len("__EXIT__"):])
	if exitCode != "0" {
		if strings.Contains(content, "No such file") || strings.Contains(content, "is a directory") {
			if strings.Contains(content, "is a directory") {
				return nil, fmt.Errorf("%w: is a directory", os.ErrInvalid)
			}
			return nil, os.ErrNotExist
		}
		if strings.Contains(content, "Permission denied") {
			return nil, os.ErrPermission
		}
		return nil, fmt.Errorf("read failed (exit %s)", exitCode)
	}

	if int64(len(content)) > maxReadBytes {
		return nil, fmt.Errorf("file too large: %w", os.ErrInvalid)
	}

	isBinary := bytes.ContainsAny([]byte(content), "\x00")
	result := &FileReadResult{
		SizeBytes:  int64(len(content)),
		ModifiedAt: time.Now().UTC(),
	}
	if isBinary {
		result.Content = []byte(base64.StdEncoding.EncodeToString([]byte(content)))
		result.Base64 = true
	} else {
		result.Content = []byte(content)
		result.Base64 = false
	}
	return result, nil
}

// writeFile writes content to a file in the container via a tar stream.
func writeFile(ctx context.Context, rt containerRuntime, container, requested string, data []byte, isBase64 bool) error {
	filePath, err := safeContainerPath(requested)
	if err != nil {
		return err
	}
	content := data
	if isBase64 {
		decoded, err := base64.StdEncoding.DecodeString(string(data))
		if err != nil {
			return fmt.Errorf("invalid base64: %w", os.ErrInvalid)
		}
		content = decoded
	}
	if int64(len(content)) > maxWriteBytes {
		return fmt.Errorf("file too large: %w", os.ErrInvalid)
	}

	tmpDir, err := os.MkdirTemp("", "gugu-write-")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	baseName := path.Base(filePath)
	tarPath := filepath.Join(tmpDir, "upload.tar")
	tarFile, err := os.Create(tarPath)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(tarFile)
	header := &tar.Header{
		Name: baseName,
		Mode: 0o644,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(header); err != nil {
		tarFile.Close()
		return err
	}
	if _, err := tw.Write(content); err != nil {
		tarFile.Close()
		return err
	}
	tw.Close()
	tarFile.Close()

	parentDir := path.Dir(filePath)
	return rt.CopyArchiveToContainer(ctx, container, tarPath, parentDir)
}

// makeDirectory creates a directory in the container.
func makeDirectory(ctx context.Context, rt containerRuntime, container, requested string) error {
	dirPath, err := safeContainerPath(requested)
	if err != nil {
		return err
	}
	output, err := rt.ExecInContainer(ctx, container, []string{
		"sh", "-c",
		fmt.Sprintf("mkdir -p %s 2>&1", shellQuote(dirPath)),
	})
	if err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if strings.Contains(output, "Permission denied") {
		return os.ErrPermission
	}
	return nil
}

// moveFile moves/renames a file or directory in the container.
func moveFile(ctx context.Context, rt containerRuntime, container, source, destination string, replace bool) error {
	srcPath, err := safeContainerPath(source)
	if err != nil {
		return err
	}
	dstPath, err := safeContainerPath(destination)
	if err != nil {
		return err
	}
	flag := "-n"
	if replace {
		flag = "-f"
	}
	output, err := rt.ExecInContainer(ctx, container, []string{
		"sh", "-c",
		fmt.Sprintf("mv %s %s %s 2>&1", flag, shellQuote(srcPath), shellQuote(dstPath)),
	})
	if err != nil {
		return fmt.Errorf("mv: %w", err)
	}
	if strings.Contains(output, "No such file") {
		return os.ErrNotExist
	}
	if strings.Contains(output, "Permission denied") {
		return os.ErrPermission
	}
	return nil
}

// removeFile deletes a file or directory in the container.
func removeFile(ctx context.Context, rt containerRuntime, container, requested string, recursive bool) error {
	filePath, err := safeContainerPath(requested)
	if err != nil {
		return err
	}
	flag := "-f"
	if recursive {
		flag = "-rf"
	}
	output, err := rt.ExecInContainer(ctx, container, []string{
		"sh", "-c",
		fmt.Sprintf("rm %s %s 2>&1", flag, shellQuote(filePath)),
	})
	if err != nil {
		return fmt.Errorf("rm: %w", err)
	}
	if strings.Contains(output, "Permission denied") {
		return os.ErrPermission
	}
	return nil
}

// shellQuote wraps a string in single quotes for safe shell interpolation.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// fileErrorCode maps a file operation error to a stable error code string.
func fileErrorCode(err error) string {
	switch {
	case errorsIs(err, os.ErrNotExist):
		return "NOT_FOUND"
	case errorsIs(err, os.ErrPermission):
		return "FORBIDDEN"
	case errorsIs(err, os.ErrInvalid):
		return "VALIDATION_FAILED"
	default:
		return "FILE_OPERATION_FAILED"
	}
}

// errorsIs is a small wrapper around errors.Is kept local to avoid import churn.
// It falls back to string matching for backward compatibility with wrapped
// errors that predate Go 1.13 error wrapping.
func errorsIs(err, target error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, target) {
		return true
	}
	return strings.Contains(err.Error(), target.Error())
}
