package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	gostdruntime "runtime"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/gugumanager/gugumanager/api/proto/gugumanager/agent/v1"
	"github.com/gugumanager/gugumanager/internal/bundletrust"
	"github.com/gugumanager/gugumanager/internal/domain"
	serverfiles "github.com/gugumanager/gugumanager/internal/files"
	"github.com/gugumanager/gugumanager/internal/install"
	"github.com/gugumanager/gugumanager/internal/runtime"
	"github.com/moby/moby/api/types/container"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ExecutionOutcome 是任务执行的最终结果，Agent 回传给 Control Plane。
type ExecutionOutcome struct {
	Succeeded  bool
	ErrorCode  string
	Retryable  bool
	ResultJSON []byte // 可选，如 {"containerId": "..."}
	Observed   *agentv1.ServerObserved
}

// containerRuntime 是 DockerExecutor 依赖的最小容器运行时接口，
// 便于测试注入 fake 实现。*runtime.DockerRuntime 满足该接口。
type containerRuntime interface {
	CreateContainer(ctx context.Context, cfg runtime.ContainerConfig) (string, error)
	StartContainer(ctx context.Context, containerID string) error
	StopContainer(ctx context.Context, containerID string, timeoutSec int) error
	RestartContainer(ctx context.Context, containerID string, timeoutSec int) error
	KillContainer(ctx context.Context, containerID string) error
	RemoveContainer(ctx context.Context, containerID string, force bool) error
	RenameContainer(ctx context.Context, containerID, newName string) error
	InspectContainer(ctx context.Context, containerID string) (runtime.ContainerStatus, error)
	ExecInContainer(ctx context.Context, containerID string, argv []string) (string, error)
	ContainerStats(ctx context.Context, containerID string) (runtime.ContainerStats, error)
	FollowLogs(ctx context.Context, containerID string) (io.ReadCloser, error)
	InspectEnv(ctx context.Context, containerID string) (map[string]string, error)
	ListRunningContainers(ctx context.Context, namePrefix string) ([]string, error)
	CopyArchiveToContainer(ctx context.Context, containerID, hostPath, containerPath string) error
	CopyArchiveFromContainer(ctx context.Context, containerID, containerPath, hostPath string) error
}

const (
	minecraftRCONAdapter = "minecraft-rcon/v1"
	paperMCGameID        = "io.gugumanager.papermc"
	paperMCRuntimeImage  = "itzg/minecraft-server@sha256:da92e9d215c159cd53a0e960d9a9cb67b5455ba1a7fca5b35d92be1e0bde857a"
)

// DockerExecutor 用 Docker 容器执行 Control Plane 下发的任务。
// 运行时惰性初始化（构造时不探测 Docker 守护进程）。
type DockerExecutor struct {
	dataRoot         string
	runnerPath       string
	bundleTrustRoots [][]byte
	rt               containerRuntime
	newRuntime       func() (containerRuntime, error)
}

// NewDockerExecutor 创建 Docker 任务执行器。dataRoot 是服务器数据的
// 根目录（每台服务器的数据卷位于 <dataRoot>/<serverID>）。
// 运行时延迟到首次 ExecuteTask 时创建，因此无 Docker 环境也能构造。
func NewDockerExecutor(dataRoot string) (*DockerExecutor, error) {
	if err := recoverInterruptedRestores(dataRoot); err != nil {
		return nil, fmt.Errorf("recover interrupted restores: %w", err)
	}
	return &DockerExecutor{
		dataRoot: dataRoot, runnerPath: strings.TrimSpace(os.Getenv("GUGU_AGENT_EXTENSION_RUNNER")),
		bundleTrustRoots: loadBundleTrustRoots(strings.TrimSpace(os.Getenv("GUGU_AGENT_BUNDLE_TRUST_ROOTS"))),
		newRuntime: func() (containerRuntime, error) {
			return runtime.NewDockerRuntime()
		},
	}, nil
}

// runtime 返回可用的容器运行时；测试注入的 rt 优先。
func (e *DockerExecutor) runtime() (containerRuntime, error) {
	if e.rt != nil {
		return e.rt, nil
	}
	if e.newRuntime == nil {
		return nil, errors.New("no runtime factory configured")
	}
	return e.newRuntime()
}

// ExecuteTask 按 task.Type 分发任务（provision / power / backup），返回执行结果。
// 返回的 error 仅表示执行器自身故障；任务失败以 ExecutionOutcome 表达。
func (e *DockerExecutor) ExecuteTask(ctx context.Context, task *agentv1.Task) (*ExecutionOutcome, error) {
	switch task.GetType() {
	case "provision":
		return e.executeProvision(ctx, task)
	case "power":
		return e.executePower(ctx, task)
	case "backup":
		return e.executeBackup(ctx, task)
	case "reconcile":
		return e.executeReconcile(ctx, task)
	case "extension":
		return e.executeExtension(ctx, task)
	default:
		return &ExecutionOutcome{
			Succeeded: false,
			ErrorCode: "UNSUPPORTED_TASK",
			Retryable: false,
		}, nil
	}
}

// executeBackup 执行备份创建/恢复/删除：容器内 tar 打包数据卷，归档保存到
// 节点本地 <dataRoot>/backups/，回传 checksum/size/storageLocation。
func (e *DockerExecutor) executeBackup(ctx context.Context, task *agentv1.Task) (*ExecutionOutcome, error) {
	payload := task.GetBackup()
	if payload == nil && len(task.GetPayloadJson()) > 0 {
		payload = &agentv1.BackupTaskPayload{}
		if err := protojson.Unmarshal(task.GetPayloadJson(), payload); err != nil {
			slog.Warn("backup: decode payload", "server_id", task.GetServerId(), "error", err)
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED"}, nil
		}
	}
	if payload == nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED"}, nil
	}
	containerName := fmt.Sprintf("gugu-server-%s", task.GetServerId())
	switch action := payload.GetAction().(type) {
	case *agentv1.BackupTaskPayload_Create:
		create := action.Create
		if create == nil {
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED"}, nil
		}
		rt, err := e.runtime()
		if err != nil {
			slog.Warn("backup: runtime unavailable", "server_id", task.GetServerId(), "error", err)
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "RUNTIME_UNAVAILABLE", Retryable: true}, nil
		}
		return e.createBackup(ctx, rt, task.GetServerId(), containerName, create)
	case *agentv1.BackupTaskPayload_Restore:
		restore := action.Restore
		if restore == nil {
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED"}, nil
		}
		rt, err := e.runtime()
		if err != nil {
			slog.Warn("backup: runtime unavailable", "server_id", task.GetServerId(), "error", err)
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "RUNTIME_UNAVAILABLE", Retryable: true}, nil
		}
		return e.restoreBackup(ctx, rt, containerName, restore)
	case *agentv1.BackupTaskPayload_Delete:
		del := action.Delete
		if del == nil {
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED"}, nil
		}
		return e.deleteBackup(ctx, del)
	default:
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED"}, nil
	}
}

func (e *DockerExecutor) createBackup(ctx context.Context, rt containerRuntime, serverID, containerName string, create *agentv1.CreateBackupPayload) (*ExecutionOutcome, error) {
	backupID := create.GetBackupId()
	objectKey, err := canonicalBackupObjectKey(backupID)
	if err != nil || (create.GetStorageObjectKey() != "" && create.GetStorageObjectKey() != objectKey) {
		slog.Warn("backup: invalid storage identity", "server_id", serverID, "backup_id", backupID)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_INTEGRITY_FAILED", Retryable: false}, nil
	}
	archive, err := resolveBackupArchive(e.dataRoot, backupID, objectKey)
	if err != nil {
		slog.Warn("backup: resolve archive", "server_id", serverID, "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_INTEGRITY_FAILED", Retryable: false}, nil
	}
	if _, err := os.Stat(archive); err == nil {
		metadata, inspectErr := inspectBackupArchive(archive)
		if inspectErr != nil {
			slog.Warn("backup: immutable archive is invalid", "server_id", serverID, "error", inspectErr)
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_INTEGRITY_FAILED", Retryable: false}, nil
		}
		return backupCreateOutcome(backupID, objectKey, metadata)
	} else if !os.IsNotExist(err) {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}

	backupDir := filepath.Dir(archive)
	containerArchiveName := backupID + ".tar.gz"
	containerArchive := "/tmp/" + containerArchiveName
	defer func() {
		if _, cleanupErr := rt.ExecInContainer(context.Background(), containerName, []string{"rm", "-f", containerArchive}); cleanupErr != nil {
			slog.Warn("backup: clean container temporary archive", "server_id", serverID, "error", cleanupErr)
		}
	}()
	if _, err := rt.ExecInContainer(ctx, containerName, []string{"tar", "-czf", containerArchive, "-C", "/data", "."}); err != nil {
		slog.Warn("backup: in-container tar", "server_id", serverID, "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	outer, err := os.CreateTemp(backupDir, "."+backupID+"-*.docker.partial")
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	outerPath := outer.Name()
	if err := outer.Close(); err != nil {
		_ = os.Remove(outerPath)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	defer os.Remove(outerPath)
	if err := rt.CopyArchiveFromContainer(ctx, containerName, containerArchive, outerPath); err != nil {
		slog.Warn("backup: copy archive from container", "server_id", serverID, "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	partial, err := extractDockerBackupPayload(outerPath, backupDir, containerArchiveName, backupID)
	if err != nil {
		slog.Warn("backup: extract docker archive payload", "server_id", serverID, "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_INTEGRITY_FAILED", Retryable: false}, nil
	}
	defer os.Remove(partial)
	metadata, err := inspectBackupArchive(partial)
	if err != nil {
		slog.Warn("backup: validate archive", "server_id", serverID, "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_INTEGRITY_FAILED", Retryable: false}, nil
	}
	if err := publishBackupArchive(partial, archive); err != nil {
		if !os.IsExist(err) {
			slog.Warn("backup: publish archive", "server_id", serverID, "error", err)
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
		}
		existing, inspectErr := inspectBackupArchive(archive)
		if inspectErr != nil || existing != metadata {
			slog.Warn("backup: conflicting immutable archive", "server_id", serverID, "error", inspectErr)
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_INTEGRITY_FAILED", Retryable: false}, nil
		}
	}
	return backupCreateOutcome(backupID, objectKey, metadata)
}

func backupCreateOutcome(backupID, objectKey string, metadata backupMetadata) (*ExecutionOutcome, error) {
	result, err := json.Marshal(map[string]any{
		"backupId":        backupID,
		"checksum":        metadata.Checksum,
		"manifestDigest":  metadata.ManifestDigest,
		"sizeBytes":       metadata.SizeBytes,
		"storageLocation": objectKey,
	})
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: false}, nil
	}
	return &ExecutionOutcome{Succeeded: true, ResultJSON: result}, nil
}

func (e *DockerExecutor) restoreBackup(ctx context.Context, rt containerRuntime, containerName string, restore *agentv1.RestoreBackupPayload) (*ExecutionOutcome, error) {
	status, err := rt.InspectContainer(ctx, containerName)
	if err != nil {
		slog.Warn("backup: inspect container before restore", "container", containerName, "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "RUNTIME_UNAVAILABLE", Retryable: true}, nil
	}
	if status.Running {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "SERVER_MUST_BE_STOPPED", Retryable: false}, nil
	}
	archive, err := resolveBackupArchive(e.dataRoot, restore.GetBackupId(), restore.GetStorageObjectKey())
	if err != nil {
		slog.Warn("backup: invalid restore archive path", "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_INTEGRITY_FAILED", Retryable: false}, nil
	}
	if _, err := os.Stat(archive); err != nil {
		slog.Warn("backup: restore archive missing", "archive", archive, "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED"}, nil
	}
	if expected := restore.GetExpectedContentDigest(); expected != "" {
		sum, _, err := fileChecksum(archive)
		if err != nil || expected != "sha256:"+sum {
			slog.Warn("backup: restore content digest mismatch", "archive", archive)
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_INTEGRITY_FAILED", Retryable: false}, nil
		}
	}
	if expected := restore.GetExpectedManifestDigest(); expected != "" {
		actual, err := backupManifestDigest(archive)
		if err != nil || expected != actual {
			slog.Warn("backup: restore manifest digest mismatch", "archive", archive)
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_INTEGRITY_FAILED", Retryable: false}, nil
		}
	}
	var restoreErr error
	if restorer, ok := rt.(interface {
		RestoreNamedVolume(context.Context, string, string) error
	}); ok {
		restoreErr = restorer.RestoreNamedVolume(ctx, containerName, archive)
	} else {
		restoreErr = replaceServerDataFromBackup(e.dataRoot, containerName, archive, restore.GetExpectedManifestDigest())
	}
	if restoreErr != nil {
		slog.Warn("backup: staged restore", "container", containerName, "error", restoreErr)
		if errors.Is(restoreErr, errBackupIntegrity) {
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_INTEGRITY_FAILED", Retryable: false}, nil
		}
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	return &ExecutionOutcome{Succeeded: true}, nil
}

func (e *DockerExecutor) deleteBackup(ctx context.Context, del *agentv1.DeleteBackupPayload) (*ExecutionOutcome, error) {
	_ = ctx
	archive, err := resolveBackupArchive(e.dataRoot, del.GetBackupId(), del.GetStorageObjectKey())
	if err != nil {
		slog.Warn("backup: invalid delete archive path", "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_INTEGRITY_FAILED", Retryable: false}, nil
	}
	if err := os.Remove(archive); err != nil && !os.IsNotExist(err) {
		slog.Warn("backup: delete archive", "archive", archive, "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	return &ExecutionOutcome{Succeeded: true}, nil
}

// ExecuteConsoleCommand dispatches through an explicitly configured console
// adapter. Docker's current adapter is RCON-only: arbitrary commands are never
// interpreted by a container shell.
func (e *DockerExecutor) ExecuteConsoleCommand(ctx context.Context, serverID string, command string) (*ExecutionOutcome, error) {
	rt, err := e.runtime()
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "RUNTIME_UNAVAILABLE", Retryable: true}, nil
	}
	containerName := fmt.Sprintf("gugu-server-%s", serverID)
	env, err := rt.InspectEnv(ctx, containerName)
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "COMMAND_FAILED", Retryable: true}, nil
	}
	if env["GUGU_CONSOLE_ADAPTER"] != minecraftRCONAdapter {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "CONSOLE_UNSUPPORTED", Retryable: false}, nil
	}
	password := env["RCON_PASSWORD"]
	if password == "" {
		// The trusted marker proves that this runtime declares an RCON adapter.
		// A missing credential is therefore a broken adapter configuration, not
		// an unsupported console capability.
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "COMMAND_FAILED", Retryable: false}, nil
	}
	port := env["RCON_PORT"]
	if port == "" {
		port = "25575"
	}
	output, err := rt.ExecInContainer(ctx, containerName,
		[]string{"rcon-cli", "--host", "127.0.0.1", "--port", port, "--password", password, command})
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "COMMAND_FAILED", Retryable: false}, nil
	}
	result, err := json.Marshal(map[string]string{"output": output})
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "COMMAND_FAILED", Retryable: false}, nil
	}
	return &ExecutionOutcome{Succeeded: true, ResultJSON: result}, nil
}

// Runtime 返回当前容器运行时，供日志 tailer 与指标采样器使用。
func (e *DockerExecutor) Runtime() (containerRuntime, error) {
	return e.runtime()
}

// ListRunningServers 返回所有 gugu-server-* 运行中容器的 serverID。
func (e *DockerExecutor) ListRunningServers(ctx context.Context) ([]string, error) {
	rt, err := e.runtime()
	if err != nil {
		return nil, err
	}
	return rt.ListRunningContainers(ctx, "gugu-server-")
}

func (e *DockerExecutor) executeProvision(ctx context.Context, task *agentv1.Task) (*ExecutionOutcome, error) {
	payload := task.GetProvision()
	if payload == nil && len(task.GetPayloadJson()) > 0 {
		payload = &agentv1.ProvisionTaskPayload{}
		if err := protojson.Unmarshal(task.GetPayloadJson(), payload); err != nil {
			slog.Warn("provision: decode payload", "server_id", task.GetServerId(), "error", err, "payload", string(task.GetPayloadJson()))
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "PROVISION_FAILED"}, nil
		}
	}
	if payload == nil {
		slog.Warn("provision: empty payload", "server_id", task.GetServerId())
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "PROVISION_FAILED"}, nil
	}
	if task.GetBundleDigest() != "" && task.GetBundleDigest() != payload.GetBundleDigest() {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "RUNTIME_TARGET_UNTRUSTED", Retryable: false}, nil
	}

	target, err := e.trustedRuntimeTarget(payload)
	if err != nil {
		slog.Warn("provision: reject runtime target", "server_id", task.GetServerId(), "error", err)
		return &ExecutionOutcome{
			Succeeded: false,
			ErrorCode: "RUNTIME_TARGET_UNTRUSTED",
			Retryable: false,
		}, nil
	}
	installPlan, err := e.trustedInstallPlan(payload)
	if err != nil {
		slog.Warn("provision: reject install plan", "server_id", task.GetServerId(), "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BUNDLE_INSTALL_UNTRUSTED", Retryable: false}, nil
	}
	environment, err := renderRuntimeEnvironment(target.Environment, payload.GetVariables())
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "RUNTIME_TARGET_INVALID", Retryable: false}, nil
	}
	startOnCreate := payload.GetStartAfterProvision()
	if len(installPlan.Artifacts) > 0 {
		// The signed artifacts are committed to the data volume while the
		// container is stopped. No unverified byte is ever executed.
		startOnCreate = false
	}
	volumeTarget := "/data"
	if len(target.DataMounts) > 0 {
		volumeTarget = target.DataMounts[0].Target
	}

	cfg := runtime.ContainerConfig{
		Name:          fmt.Sprintf("gugu-server-%s", task.GetServerId()),
		Image:         target.Image,
		Entrypoint:    []string{target.Command.Executable},
		Cmd:           append([]string(nil), target.Command.Args...),
		WorkingDir:    target.WorkingDir,
		User:          target.User,
		Env:           environment,
		PortBindings:  map[int]int{},
		PortProtocols: map[int]string{},
		PortBindIPs:   map[int]string{},
		VolumeName:    fmt.Sprintf("gugu-server-%s-data", task.GetServerId()),
		VolumeTarget:  volumeTarget,
		MemoryMB:      1024,
		CPUShares:     1024,
		StartOnCreate: &startOnCreate,
		HealthCheck:   runtimeHealthCheck(target),
		Labels:        runtimeOwnershipLabels(task.GetServerId(), payload.GetBundleDigest(), payload.GetDesiredDigest(), task.GetGeneration()),
	}
	cfg.Env["GUGU_DESIRED_DIGEST"] = payload.GetDesiredDigest()
	cfg.Env["GUGU_GENERATION"] = strconv.FormatUint(task.GetGeneration(), 10)
	cfg.Env["GUGU_BUNDLE_DIGEST"] = payload.GetBundleDigest()
	if target.Console != nil && target.Console.Adapter == minecraftRCONAdapter {
		cfg.Env["GUGU_CONSOLE_ADAPTER"] = target.Console.Adapter
		cfg.Env["ENABLE_RCON"] = "TRUE"
		cfg.Env["RCON_PORT"] = fmt.Sprintf("%d", target.Console.Port)
		cfg.Env["RCON_PASSWORD"] = randomHex(16)
	}
	for _, alloc := range payload.GetAllocations() {
		if alloc.GetContainerPort() != 0 {
			cfg.PortBindings[int(alloc.GetContainerPort())] = int(alloc.GetHostPort())
			cfg.PortProtocols[int(alloc.GetContainerPort())] = provisionNetworkProtocol(alloc.GetProtocol())
			cfg.PortBindIPs[int(alloc.GetContainerPort())] = alloc.GetBindIp()
		}
	}
	if rl := payload.GetResourceLimits(); rl != nil {
		if mb := rl.GetMemoryBytes() / (1024 * 1024); mb > 0 {
			cfg.MemoryMB = int64(mb)
		}
		if cpu := rl.GetCpuMillicores(); cpu > 0 {
			cfg.CPUShares = int64(cpu)
		}
		if pids := rl.GetPids(); pids > 0 {
			cfg.PIDsLimit = int64(pids)
		}
	}

	rt, err := e.runtime()
	if err != nil {
		slog.Warn("provision: runtime unavailable", "server_id", task.GetServerId(), "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "RUNTIME_UNAVAILABLE", Retryable: true}, nil
	}
	containerID, err := rt.CreateContainer(ctx, cfg)
	if err != nil {
		slog.Warn("provision: create container failed", "server_id", task.GetServerId(), "image", cfg.Image, "name", cfg.Name, "volume", cfg.VolumeName, "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "PROVISION_FAILED", Retryable: true}, nil
	}
	if len(installPlan.Artifacts) > 0 {
		if err := e.installBundleArtifacts(ctx, rt, containerID, volumeTarget, installPlan); err != nil {
			_ = rt.RemoveContainer(context.Background(), containerID, true)
			slog.Warn("provision: install signed artifacts", "server_id", task.GetServerId(), "error", err)
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "BUNDLE_INSTALL_FAILED", Retryable: isRetryableInstallError(err)}, nil
		}
		if payload.GetStartAfterProvision() {
			if err := rt.StartContainer(ctx, containerID); err != nil {
				_ = rt.RemoveContainer(context.Background(), containerID, true)
				return &ExecutionOutcome{Succeeded: false, ErrorCode: "PROVISION_FAILED", Retryable: true}, nil
			}
		}
	}
	result, err := json.Marshal(map[string]string{"containerId": containerID})
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "PROVISION_FAILED", Retryable: false}, nil
	}
	return &ExecutionOutcome{Succeeded: true, ResultJSON: result}, nil
}

func runtimeOwnershipLabels(serverID, bundleDigest, desiredDigest string, generation uint64) map[string]string {
	return map[string]string{
		"io.gugumanager.managed":        "true",
		"io.gugumanager.server-id":      serverID,
		"io.gugumanager.bundle-digest":  bundleDigest,
		"io.gugumanager.desired-digest": desiredDigest,
		"io.gugumanager.generation":     strconv.FormatUint(generation, 10),
	}
}

// executeReconcile converges one immutable DesiredRuntimeSpec. It first checks
// the digest stored on the canonical container, then prepares a stopped
// candidate sharing the named data volume. A running server is stopped only
// after preparation; the candidate must become healthy before names switch.
// Any failed start/health/rename restores the old generation.
func (e *DockerExecutor) executeReconcile(ctx context.Context, task *agentv1.Task) (*ExecutionOutcome, error) {
	payload := task.GetReconcile()
	if payload == nil && len(task.GetPayloadJson()) > 0 {
		payload = &agentv1.ReconcileTaskPayload{}
		if err := protojson.Unmarshal(task.GetPayloadJson(), payload); err != nil {
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "RUNTIME_TARGET_INVALID", Retryable: false}, nil
		}
	}
	desired := payload.GetDesired()
	if desired == nil || desired.GetGeneration() != task.GetGeneration() || !validSHA256Digest(desired.GetDigest()) ||
		!validSHA256Digest(desired.GetBundleDigest()) || (task.GetBundleDigest() != "" && task.GetBundleDigest() != desired.GetBundleDigest()) {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "RUNTIME_TARGET_INVALID", Retryable: false}, nil
	}

	provisionShape := &agentv1.ProvisionTaskPayload{
		GameDefinitionId: desired.GetGameDefinitionId(), BundleDigest: desired.GetBundleDigest(),
		RuntimeTargetJson: desired.GetRuntimeTargetJson(), Variables: desired.GetVariables(),
	}
	provisionShape.BundleRevisionJson = desired.GetBundleRevisionJson()
	provisionShape.TrustRootPem = desired.GetTrustRootPem()
	target, err := e.trustedRuntimeTarget(provisionShape)
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "RUNTIME_TARGET_UNTRUSTED", Retryable: false}, nil
	}
	installPlan, err := e.trustedInstallPlan(provisionShape)
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BUNDLE_INSTALL_UNTRUSTED", Retryable: false}, nil
	}
	environment, err := renderRuntimeEnvironment(target.Environment, desired.GetVariables())
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "RUNTIME_TARGET_INVALID", Retryable: false}, nil
	}
	environment["GUGU_DESIRED_DIGEST"] = desired.GetDigest()
	environment["GUGU_GENERATION"] = strconv.FormatUint(desired.GetGeneration(), 10)
	environment["GUGU_BUNDLE_DIGEST"] = desired.GetBundleDigest()
	if target.Console != nil && target.Console.Adapter == minecraftRCONAdapter {
		environment["GUGU_CONSOLE_ADAPTER"] = target.Console.Adapter
		environment["ENABLE_RCON"] = "TRUE"
		environment["RCON_PORT"] = fmt.Sprintf("%d", target.Console.Port)
		environment["RCON_PASSWORD"] = randomHex(16)
	}

	rt, err := e.runtime()
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "RUNTIME_UNAVAILABLE", Retryable: true}, nil
	}
	canonicalName := fmt.Sprintf("gugu-server-%s", task.GetServerId())
	oldStatus, oldInspectErr := rt.InspectContainer(ctx, canonicalName)
	oldEnv, oldEnvErr := rt.InspectEnv(ctx, canonicalName)
	if oldInspectErr == nil && oldEnvErr == nil && oldEnv["GUGU_DESIRED_DIGEST"] == desired.GetDigest() {
		return reconcileSuccess(task, oldStatus.ID, desired, oldStatus, true)
	}

	startCandidate := false
	volumeTarget := "/data"
	if len(target.DataMounts) > 0 {
		volumeTarget = target.DataMounts[0].Target
	}
	volumeName := fmt.Sprintf("gugu-server-%s-data-g%d", task.GetServerId(), desired.GetGeneration())
	if oldInspectErr != nil {
		// With no previous instance there is nothing to preserve. Keep the
		// stable first-generation name for compatibility with backup helpers.
		volumeName = fmt.Sprintf("gugu-server-%s-data", task.GetServerId())
	}
	cfg := runtime.ContainerConfig{
		Name:  fmt.Sprintf("%s-candidate-g%d", canonicalName, desired.GetGeneration()),
		Image: target.Image, Entrypoint: []string{target.Command.Executable}, Cmd: append([]string(nil), target.Command.Args...),
		WorkingDir: target.WorkingDir, User: target.User, Env: environment,
		Labels:       runtimeOwnershipLabels(task.GetServerId(), desired.GetBundleDigest(), desired.GetDigest(), desired.GetGeneration()),
		PortBindings: map[int]int{}, PortProtocols: map[int]string{}, PortBindIPs: map[int]string{},
		VolumeName: volumeName, VolumeTarget: volumeTarget,
		MemoryMB: 1024, CPUShares: 1024, StartOnCreate: &startCandidate, HealthCheck: runtimeHealthCheck(target),
	}
	for _, allocation := range desired.GetAllocations() {
		if allocation.GetContainerPort() == 0 {
			continue
		}
		port := int(allocation.GetContainerPort())
		cfg.PortBindings[port] = int(allocation.GetHostPort())
		cfg.PortProtocols[port] = provisionNetworkProtocol(allocation.GetProtocol())
		cfg.PortBindIPs[port] = allocation.GetBindIp()
	}
	if limits := desired.GetResourceLimits(); limits != nil {
		if mb := limits.GetMemoryBytes() / (1024 * 1024); mb > 0 {
			cfg.MemoryMB = int64(mb)
		}
		if cpu := limits.GetCpuMillicores(); cpu > 0 {
			cfg.CPUShares = int64(cpu)
		}
		if pids := limits.GetPids(); pids > 0 {
			cfg.PIDsLimit = int64(pids)
		}
	}

	candidateID, err := rt.CreateContainer(ctx, cfg)
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "RECONCILE_PREPARE_FAILED", Retryable: true}, nil
	}
	oldExists := oldInspectErr == nil
	oldID := oldStatus.ID
	if oldID == "" {
		oldID = canonicalName
	}
	oldWasRunning := oldExists && oldStatus.Running
	rollback := func() {
		_ = rt.StopContainer(context.Background(), candidateID, 10)
		_ = rt.RemoveContainer(context.Background(), candidateID, true)
		if oldWasRunning {
			_ = rt.StartContainer(context.Background(), oldID)
		}
	}
	// Freeze the source before copying its volume. Candidate image pulling and
	// container creation already completed, so this is the smallest consistent
	// cut-over window while still allowing a byte-for-byte rollback.
	if oldWasRunning {
		if err := rt.StopContainer(ctx, oldID, 30); err != nil {
			_ = rt.RemoveContainer(context.Background(), candidateID, true)
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "RECONCILE_SWITCH_FAILED", Retryable: true}, nil
		}
	}
	if oldExists {
		if err := cloneContainerData(ctx, rt, oldID, candidateID, volumeTarget, e.dataRoot); err != nil {
			rollback()
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "RECONCILE_PREPARE_FAILED", Retryable: true}, nil
		}
	}
	if len(installPlan.Artifacts) > 0 {
		if err := e.installBundleArtifacts(ctx, rt, candidateID, volumeTarget, installPlan); err != nil {
			rollback()
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "BUNDLE_INSTALL_FAILED", Retryable: isRetryableInstallError(err)}, nil
		}
	}
	if desired.GetDesiredRunning() {
		if err := rt.StartContainer(ctx, candidateID); err != nil {
			rollback()
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "RECONCILE_ROLLED_BACK", Retryable: true}, nil
		}
		if _, err := waitForHealthyRuntime(ctx, rt, candidateID, target); err != nil {
			rollback()
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "RECONCILE_ROLLED_BACK", Retryable: true}, nil
		}
	}

	rollbackName := fmt.Sprintf("%s-rollback-g%d", canonicalName, desired.GetGeneration())
	if oldExists {
		if err := rt.RenameContainer(ctx, oldID, rollbackName); err != nil {
			rollback()
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "RECONCILE_SWITCH_FAILED", Retryable: true}, nil
		}
	}
	if err := rt.RenameContainer(ctx, candidateID, canonicalName); err != nil {
		if oldExists {
			_ = rt.RenameContainer(context.Background(), oldID, canonicalName)
		}
		rollback()
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "RECONCILE_ROLLED_BACK", Retryable: true}, nil
	}
	if oldExists {
		_ = rt.RemoveContainer(context.Background(), oldID, true)
	}
	status, err := rt.InspectContainer(ctx, candidateID)
	if err != nil {
		status = runtime.ContainerStatus{ID: candidateID, Running: desired.GetDesiredRunning(), Healthy: desired.GetDesiredRunning()}
	}
	return reconcileSuccess(task, candidateID, desired, status, false)
}

func waitForHealthyRuntime(ctx context.Context, rt containerRuntime, containerID string, target domain.GameRuntimeTarget) (runtime.ContainerStatus, error) {
	wait := 30 * time.Second
	if target.Health.IntervalSeconds > 0 && target.Health.FailureThreshold > 0 {
		wait = time.Duration(target.Health.IntervalSeconds*(target.Health.FailureThreshold+2)) * time.Second
		if wait < 30*time.Second {
			wait = 30 * time.Second
		}
	}
	if wait > 2*time.Minute {
		wait = 2 * time.Minute
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		status, err := rt.InspectContainer(ctx, containerID)
		if err == nil && status.Running && status.Healthy {
			return status, nil
		}
		if err == nil && !status.Running && status.State != "created" {
			return status, errors.New("candidate exited before becoming healthy")
		}
		select {
		case <-ctx.Done():
			return runtime.ContainerStatus{}, ctx.Err()
		case <-deadline.C:
			return runtime.ContainerStatus{}, errors.New("candidate health check timed out")
		case <-ticker.C:
		}
	}
}

func reconcileSuccess(task *agentv1.Task, containerID string, desired *agentv1.DesiredRuntimeSpec, status runtime.ContainerStatus, noOp bool) (*ExecutionOutcome, error) {
	result, err := json.Marshal(map[string]any{
		"containerId": containerID, "desiredDigest": desired.GetDigest(), "generation": desired.GetGeneration(), "noOp": noOp,
	})
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "RECONCILE_FAILED", Retryable: false}, nil
	}
	power := agentv1.ObservedPower_OBSERVED_POWER_STOPPED
	health := agentv1.HealthCondition_HEALTH_CONDITION_UNKNOWN
	if status.Running {
		power = agentv1.ObservedPower_OBSERVED_POWER_RUNNING
		if status.Healthy {
			health = agentv1.HealthCondition_HEALTH_CONDITION_HEALTHY
		} else {
			health = agentv1.HealthCondition_HEALTH_CONDITION_UNHEALTHY
		}
	}
	return &ExecutionOutcome{Succeeded: true, ResultJSON: result, Observed: &agentv1.ServerObserved{
		ServerId: task.GetServerId(), ObservedGeneration: desired.GetGeneration(), ObservedPower: power,
		HealthCondition: health, ObservedAt: timestamppb.Now(), RuntimeId: containerID, BundleDigest: desired.GetBundleDigest(),
	}}, nil
}

func (e *DockerExecutor) trustedRuntimeTarget(payload *agentv1.ProvisionTaskPayload) (domain.GameRuntimeTarget, error) {
	if len(payload.GetBundleRevisionJson()) > 0 || len(payload.GetTrustRootPem()) > 0 {
		verified, err := e.verifyTrustedBundle(payload)
		if err != nil {
			return domain.GameRuntimeTarget{}, err
		}
		var declared domain.GameRuntimeTarget
		decoder := json.NewDecoder(strings.NewReader(payload.GetRuntimeTargetJson()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&declared); err != nil {
			return domain.GameRuntimeTarget{}, fmt.Errorf("decode declared runtime target: %w", err)
		}
		if declared.Digest != verified.RuntimeTarget.Digest {
			return domain.GameRuntimeTarget{}, fmt.Errorf("signed runtime target digest mismatch")
		}
		return verified.RuntimeTarget, nil
	}
	return trustedLegacyRuntimeTarget(payload)
}

func (e *DockerExecutor) verifyTrustedBundle(payload *agentv1.ProvisionTaskPayload) (bundletrust.Verified, error) {
	if len(payload.GetBundleRevisionJson()) == 0 || len(payload.GetTrustRootPem()) == 0 {
		return bundletrust.Verified{}, fmt.Errorf("incomplete Bundle trust evidence")
	}
	trusted := false
	for _, root := range e.bundleTrustRoots {
		if bytes.Equal(bytes.TrimSpace(root), bytes.TrimSpace(payload.GetTrustRootPem())) {
			trusted = true
			break
		}
	}
	if !trusted {
		return bundletrust.Verified{}, fmt.Errorf("Bundle signing root is not configured on this Agent")
	}
	verified, err := bundletrust.Verify(payload.GetBundleRevisionJson(), payload.GetTrustRootPem())
	if err != nil {
		return bundletrust.Verified{}, err
	}
	if verified.Document.GameDefinitionID != payload.GetGameDefinitionId() || verified.Document.Digest != payload.GetBundleDigest() {
		return bundletrust.Verified{}, fmt.Errorf("signed Bundle identity does not match task")
	}
	if !bundletrust.SupportsPlatform(verified.Document.Compatibility, gostdruntime.GOOS+"/"+gostdruntime.GOARCH) {
		return bundletrust.Verified{}, fmt.Errorf("signed Bundle does not support this Agent platform")
	}
	return verified, nil
}

func (e *DockerExecutor) trustedInstallPlan(payload *agentv1.ProvisionTaskPayload) (bundletrust.Installation, error) {
	if len(payload.GetBundleRevisionJson()) == 0 && len(payload.GetTrustRootPem()) == 0 {
		return bundletrust.Installation{}, nil
	}
	verified, err := e.verifyTrustedBundle(payload)
	if err != nil {
		return bundletrust.Installation{}, err
	}
	return bundletrust.InstallPlan(verified.Document)
}

func (e *DockerExecutor) installBundleArtifacts(
	ctx context.Context,
	rt containerRuntime,
	containerID string,
	volumeTarget string,
	plan bundletrust.Installation,
) error {
	if len(plan.Artifacts) == 0 {
		return nil
	}
	if err := os.MkdirAll(e.dataRoot, 0o750); err != nil {
		return fmt.Errorf("create Agent data root: %w", err)
	}
	stage, err := os.MkdirTemp(e.dataRoot, ".bundle-install-*")
	if err != nil {
		return fmt.Errorf("create Bundle install staging directory: %w", err)
	}
	defer os.RemoveAll(stage)
	stagingFS, err := serverfiles.NewServerFS(stage, serverfiles.Limits{
		MaxReadBytes: install.DefaultMaxArtifactBytes, MaxWriteBytes: install.DefaultMaxArtifactBytes,
	})
	if err != nil {
		return err
	}
	if _, err := install.Install(ctx, stagingFS, plan.Artifacts, install.Options{
		Allowlist: plan.Allowlist, MaxArtifactBytes: install.DefaultMaxArtifactBytes,
	}); err != nil {
		return err
	}
	archive, err := archiveRegularTree(e.dataRoot, stage)
	if err != nil {
		return err
	}
	defer os.Remove(archive)
	if err := rt.CopyArchiveToContainer(ctx, containerID, archive, volumeTarget); err != nil {
		return fmt.Errorf("commit signed Bundle artifacts: %w", err)
	}
	return nil
}

func archiveRegularTree(parent, root string) (archivePath string, err error) {
	file, err := os.CreateTemp(parent, ".bundle-artifacts-*.tar")
	if err != nil {
		return "", fmt.Errorf("create artifact archive: %w", err)
	}
	archivePath = file.Name()
	writer := tar.NewWriter(file)
	defer func() {
		if closeErr := writer.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(archivePath)
		}
	}()
	err = filepath.Walk(root, func(current string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == root {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
			return fmt.Errorf("artifact staging contains unsupported entry %q", current)
		}
		relative, relErr := filepath.Rel(root, current)
		if relErr != nil {
			return relErr
		}
		header, headerErr := tar.FileInfoHeader(info, "")
		if headerErr != nil {
			return headerErr
		}
		header.Name = filepath.ToSlash(relative)
		if info.IsDir() {
			header.Name += "/"
		}
		if headerErr = writer.WriteHeader(header); headerErr != nil || info.IsDir() {
			return headerErr
		}
		input, openErr := os.Open(current)
		if openErr != nil {
			return openErr
		}
		_, copyErr := io.Copy(writer, input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	return archivePath, err
}

func cloneContainerData(ctx context.Context, rt containerRuntime, sourceID, targetID, volumeTarget, dataRoot string) error {
	if err := os.MkdirAll(dataRoot, 0o750); err != nil {
		return err
	}
	archive, err := os.CreateTemp(dataRoot, ".reconcile-volume-*.tar")
	if err != nil {
		return err
	}
	archivePath := archive.Name()
	if err := archive.Close(); err != nil {
		_ = os.Remove(archivePath)
		return err
	}
	defer os.Remove(archivePath)
	if err := rt.CopyArchiveFromContainer(ctx, sourceID, strings.TrimSuffix(volumeTarget, "/")+"/.", archivePath); err != nil {
		return fmt.Errorf("snapshot previous data volume: %w", err)
	}
	if err := rt.CopyArchiveToContainer(ctx, targetID, archivePath, volumeTarget); err != nil {
		return fmt.Errorf("prepare candidate data volume: %w", err)
	}
	return nil
}

func isRetryableInstallError(err error) bool {
	for _, permanent := range []error{
		install.ErrNotHTTPS, install.ErrHostNotAllowed, install.ErrPrivateAddress,
		install.ErrDigestMismatch, install.ErrSizeMismatch, install.ErrInvalidDigest,
		install.ErrDuplicateTarget, serverfiles.ErrUnsafePath, serverfiles.ErrSizeLimit,
	} {
		if errors.Is(err, permanent) {
			return false
		}
	}
	return true
}

func trustedLegacyRuntimeTarget(payload *agentv1.ProvisionTaskPayload) (domain.GameRuntimeTarget, error) {
	if payload.GetGameDefinitionId() != paperMCGameID {
		return domain.GameRuntimeTarget{}, fmt.Errorf("game definition %q has no trusted runtime target", payload.GetGameDefinitionId())
	}
	if !validSHA256Digest(payload.GetBundleDigest()) {
		return domain.GameRuntimeTarget{}, fmt.Errorf("invalid bundle digest")
	}
	if strings.TrimSpace(payload.GetRuntimeTargetJson()) == "" {
		return domain.GameRuntimeTarget{}, fmt.Errorf("runtime target is missing")
	}
	var target domain.GameRuntimeTarget
	decoder := json.NewDecoder(strings.NewReader(payload.GetRuntimeTargetJson()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&target); err != nil {
		return domain.GameRuntimeTarget{}, fmt.Errorf("decode runtime target: %w", err)
	}
	if target.Adapter != "container/v1" || target.Image != paperMCRuntimeImage {
		return domain.GameRuntimeTarget{}, fmt.Errorf("runtime target is not the pinned PaperMC target")
	}
	digest := target.Digest
	computed, err := runtimeTargetDigest(target)
	if err != nil {
		return domain.GameRuntimeTarget{}, err
	}
	if digest != computed {
		return domain.GameRuntimeTarget{}, fmt.Errorf("runtime target digest mismatch")
	}
	target.Digest = digest
	if target.Command.Executable == "" || target.WorkingDir == "" || len(target.DataMounts) == 0 {
		return domain.GameRuntimeTarget{}, fmt.Errorf("runtime target is incomplete")
	}
	return target, nil
}

func loadBundleTrustRoots(configured string) [][]byte {
	if configured == "" {
		return nil
	}
	var roots [][]byte
	for _, filename := range strings.FieldsFunc(configured, func(r rune) bool { return r == ';' || r == ',' }) {
		content, err := os.ReadFile(strings.TrimSpace(filename))
		if err == nil && len(bytes.TrimSpace(content)) > 0 {
			roots = append(roots, content)
		}
	}
	return roots
}

func runtimeTargetDigest(target domain.GameRuntimeTarget) (string, error) {
	target.Digest = ""
	canonical, err := json.Marshal(target)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validSHA256Digest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, r := range value[len("sha256:"):] {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func renderRuntimeEnvironment(template map[string]string, variables map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(template))
	for key, value := range template {
		rendered := value
		for variable, replacement := range variables {
			rendered = strings.ReplaceAll(rendered, "{{ "+variable+" }}", replacement)
		}
		if strings.Contains(rendered, "{{") || strings.Contains(rendered, "}}") {
			return nil, fmt.Errorf("runtime environment %s contains unresolved variable", key)
		}
		result[key] = rendered
	}
	return result, nil
}

func runtimeHealthCheck(target domain.GameRuntimeTarget) *container.HealthConfig {
	if target.Health.Type != "tcp" || target.Health.PortRef == "" {
		return nil
	}
	for _, port := range target.Ports {
		if port.Name != target.Health.PortRef {
			continue
		}
		return &container.HealthConfig{
			Test:     []string{"CMD-SHELL", fmt.Sprintf("mc-health --host 127.0.0.1 --port %d", port.ContainerPort)},
			Interval: time.Duration(target.Health.IntervalSeconds) * time.Second,
			Timeout:  time.Duration(target.Health.TimeoutSeconds) * time.Second,
			Retries:  target.Health.FailureThreshold,
		}
	}
	return nil
}

func provisionNetworkProtocol(protocol agentv1.NetworkProtocol) string {
	if protocol == agentv1.NetworkProtocol_NETWORK_PROTOCOL_UDP {
		return "udp"
	}
	return "tcp"
}

func (e *DockerExecutor) executePower(ctx context.Context, task *agentv1.Task) (*ExecutionOutcome, error) {
	power := task.GetPower()
	if power == nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "POWER_OPERATION_FAILED", Retryable: false}, nil
	}
	rt, err := e.runtime()
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "RUNTIME_UNAVAILABLE", Retryable: true}, nil
	}

	containerID := fmt.Sprintf("gugu-server-%s", task.GetServerId())
	var actionErr error
	switch power.GetAction() {
	case agentv1.PowerAction_POWER_ACTION_START:
		actionErr = rt.StartContainer(ctx, containerID)
	case agentv1.PowerAction_POWER_ACTION_STOP:
		actionErr = rt.StopContainer(ctx, containerID, int(power.GetGracefulTimeoutSeconds()))
	case agentv1.PowerAction_POWER_ACTION_RESTART:
		actionErr = rt.RestartContainer(ctx, containerID, int(power.GetGracefulTimeoutSeconds()))
	case agentv1.PowerAction_POWER_ACTION_KILL:
		actionErr = rt.KillContainer(ctx, containerID)
	default:
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "POWER_OPERATION_FAILED", Retryable: false}, nil
	}
	if actionErr != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "POWER_OPERATION_FAILED", Retryable: true}, nil
	}

	// 动作成功后观察容器实际状态并回传。
	observed := &agentv1.ServerObserved{
		ServerId:        task.GetServerId(),
		ObservedPower:   agentv1.ObservedPower_OBSERVED_POWER_UNKNOWN,
		HealthCondition: agentv1.HealthCondition_HEALTH_CONDITION_UNKNOWN,
		ObservedAt:      timestamppb.Now(),
	}
	status, err := rt.InspectContainer(ctx, containerID)
	if err == nil {
		if status.Running {
			observed.ObservedPower = agentv1.ObservedPower_OBSERVED_POWER_RUNNING
		} else {
			observed.ObservedPower = agentv1.ObservedPower_OBSERVED_POWER_STOPPED
		}
		if status.Healthy {
			observed.HealthCondition = agentv1.HealthCondition_HEALTH_CONDITION_HEALTHY
		}
	}
	return &ExecutionOutcome{Succeeded: true, Observed: observed}, nil
}
