package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	agentv1 "github.com/gugumanager/gugumanager/api/proto/gugumanager/agent/v1"
	"github.com/gugumanager/gugumanager/internal/runtime"
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
	RemoveContainer(ctx context.Context, containerID string, force bool) error
	InspectContainer(ctx context.Context, containerID string) (runtime.ContainerStatus, error)
	ExecInContainer(ctx context.Context, containerID string, argv []string) (string, error)
	ContainerStats(ctx context.Context, containerID string) (runtime.ContainerStats, error)
	FollowLogs(ctx context.Context, containerID string) (io.ReadCloser, error)
	InspectEnv(ctx context.Context, containerID string) (map[string]string, error)
	ListRunningContainers(ctx context.Context, namePrefix string) ([]string, error)
	CopyArchiveToContainer(ctx context.Context, containerID, hostPath, containerPath string) error
	CopyArchiveFromContainer(ctx context.Context, containerID, containerPath, hostPath string) error
}

// gameImageMap 把 Control Plane 的 GameDefinitionId 映射到本机 Docker 镜像。
// 未知的游戏定义由 Control Plane 先行校验，这里只做保守回退。
var gameImageMap = map[string]string{
	"io.gugumanager.papermc":  "itzg/minecraft-server:latest",
	"io.gugumanager.vanilla":  "itzg/minecraft-server:latest",
	"io.gugumanager.spigot":   "itzg/minecraft-server:latest",
	"io.gugumanager.forge":    "itzg/minecraft-server:latest",
	"io.gugumanager.fabric":   "itzg/minecraft-server:latest",
	"io.gugumanager.velocity": "itzg/velocity-proxy:latest",
}

// DockerExecutor 用 Docker 容器执行 Control Plane 下发的任务。
// 运行时惰性初始化（构造时不探测 Docker 守护进程）。
type DockerExecutor struct {
	dataRoot   string
	rt         containerRuntime
	newRuntime func() (containerRuntime, error)
}

// NewDockerExecutor 创建 Docker 任务执行器。dataRoot 是服务器数据的
// 根目录（每台服务器的数据卷位于 <dataRoot>/<serverID>）。
// 运行时延迟到首次 ExecuteTask 时创建，因此无 Docker 环境也能构造。
func NewDockerExecutor(dataRoot string) (*DockerExecutor, error) {
	return &DockerExecutor{
		dataRoot: dataRoot,
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
	rt, err := e.runtime()
	if err != nil {
		slog.Warn("backup: runtime unavailable", "server_id", task.GetServerId(), "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "RUNTIME_UNAVAILABLE", Retryable: true}, nil
	}
	containerName := fmt.Sprintf("gugu-server-%s", task.GetServerId())
	switch action := payload.GetAction().(type) {
	case *agentv1.BackupTaskPayload_Create:
		create := action.Create
		if create == nil {
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED"}, nil
		}
		return e.createBackup(ctx, rt, task.GetServerId(), containerName, create)
	case *agentv1.BackupTaskPayload_Restore:
		restore := action.Restore
		if restore == nil {
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED"}, nil
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
	backupDir := filepath.Join(e.dataRoot, "backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		slog.Warn("backup: create backup dir", "server_id", serverID, "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	archive := filepath.Join(backupDir, create.GetBackupId()+".tar.gz")
	// 容器内打包到 /tmp，再拷出到节点备份目录。
	if _, err := rt.ExecInContainer(ctx, containerName, []string{"sh", "-c", fmt.Sprintf("tar -czf /tmp/%s.tar.gz -C /data .", create.GetBackupId())}); err != nil {
		slog.Warn("backup: in-container tar", "server_id", serverID, "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	if err := rt.CopyArchiveFromContainer(ctx, containerName, fmt.Sprintf("/tmp/%s.tar.gz", create.GetBackupId()), archive); err != nil {
		slog.Warn("backup: copy archive from container", "server_id", serverID, "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	sum, size, err := fileChecksum(archive)
	if err != nil {
		slog.Warn("backup: checksum archive", "server_id", serverID, "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	result, err := json.Marshal(map[string]any{
		"checksum":        "sha256:" + sum,
		"sizeBytes":       size,
		"storageLocation": "backups/" + create.GetBackupId() + ".tar.gz",
	})
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: false}, nil
	}
	return &ExecutionOutcome{Succeeded: true, ResultJSON: result}, nil
}

func (e *DockerExecutor) restoreBackup(ctx context.Context, rt containerRuntime, containerName string, restore *agentv1.RestoreBackupPayload) (*ExecutionOutcome, error) {
	archive := filepath.Join(e.dataRoot, restore.GetStorageObjectKey())
	if _, err := os.Stat(archive); err != nil {
		slog.Warn("backup: restore archive missing", "archive", archive, "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED"}, nil
	}
	// 服务器 stop 后容器为 stopped，docker exec 无法在容器内执行；
	// 恢复前先确保容器 running（对已 running 容器 docker start 幂等）。
	if err := rt.StartContainer(ctx, containerName); err != nil {
		slog.Warn("backup: start container before restore", "container", containerName, "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	if _, err := rt.ExecInContainer(ctx, containerName, []string{"sh", "-c", "rm -rf /data/* /data/.[!.]* 2>/dev/null || true"}); err != nil {
		slog.Warn("backup: clear data dir", "container", containerName, "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	if err := rt.CopyArchiveToContainer(ctx, containerName, archive, "/tmp"); err != nil {
		slog.Warn("backup: copy archive into container", "container", containerName, "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	if _, err := rt.ExecInContainer(ctx, containerName, []string{"sh", "-c", "tar -xzf /tmp/restore.tar.gz -C /data 2>/dev/null || tar -xzf $(ls /tmp/*.tar.gz | head -1) -C /data"}); err != nil {
		slog.Warn("backup: in-container extract", "container", containerName, "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	return &ExecutionOutcome{Succeeded: true}, nil
}

func (e *DockerExecutor) deleteBackup(ctx context.Context, del *agentv1.DeleteBackupPayload) (*ExecutionOutcome, error) {
	archive := filepath.Join(e.dataRoot, del.GetStorageObjectKey())
	if err := os.Remove(archive); err != nil && !os.IsNotExist(err) {
		slog.Warn("backup: delete archive", "archive", archive, "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	return &ExecutionOutcome{Succeeded: true}, nil
}

// ExecuteConsoleCommand 在服务器容器内执行控制台命令，输出回传控制面。
func (e *DockerExecutor) ExecuteConsoleCommand(ctx context.Context, serverID string, command string) (*ExecutionOutcome, error) {
	rt, err := e.runtime()
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "RUNTIME_UNAVAILABLE", Retryable: true}, nil
	}
	containerName := fmt.Sprintf("gugu-server-%s", serverID)
	// 游戏命令（list/say/stop 等）由游戏服务器进程解释，不能作为 shell 命令执行。
	// 优先走容器内 RCON（密码由 provision 注入）；无 RCON 时回退容器 shell。
	if env, envErr := rt.InspectEnv(ctx, containerName); envErr == nil {
		if password := env["RCON_PASSWORD"]; password != "" {
			output, execErr := rt.ExecInContainer(ctx, containerName,
				[]string{"rcon-cli", "--host", "127.0.0.1", "--port", "25575", "--password", password, command})
			if execErr == nil {
				result, _ := json.Marshal(map[string]string{"output": output})
				return &ExecutionOutcome{Succeeded: true, ResultJSON: result}, nil
			}
			// RCON 执行失败不立即返回：fallthrough 到 shell 兜底。
		}
	}
	output, err := rt.ExecInContainer(ctx, containerName, []string{"sh", "-c", command})
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

	image, ok := gameImageMap[payload.GetGameDefinitionId()]
	if !ok {
		return &ExecutionOutcome{
			Succeeded: false,
			ErrorCode: "PROVISION_FAILED",
			Retryable: false,
		}, nil
	}

	cfg := runtime.ContainerConfig{
		Name:         fmt.Sprintf("gugu-server-%s", task.GetServerId()),
		Image:        image,
		Env:          map[string]string{"EULA": "TRUE"},
		PortBindings: map[int]int{},
		VolumePath:   filepath.Join(e.dataRoot, task.GetServerId()),
		MemoryMB:     1024,
		CPUShares:    1024,
	}
	// 开启 RCON，供指标采样器查询在线玩家；密码随机，仅容器内可见。
	cfg.Env["ENABLE_RCON"] = "TRUE"
	cfg.Env["RCON_PORT"] = "25575"
	cfg.Env["RCON_PASSWORD"] = randomHex(16)
	for k, v := range payload.GetVariables() {
		cfg.Env[k] = v
	}
	for _, alloc := range payload.GetAllocations() {
		if alloc.GetContainerPort() != 0 {
			cfg.PortBindings[int(alloc.GetContainerPort())] = int(alloc.GetHostPort())
		}
	}
	if rl := payload.GetResourceLimits(); rl != nil {
		if mb := rl.GetMemoryBytes() / (1024 * 1024); mb > 0 {
			cfg.MemoryMB = int64(mb)
		}
		if cpu := rl.GetCpuMillicores(); cpu > 0 {
			cfg.CPUShares = int64(cpu)
		}
	}

	rt, err := e.runtime()
	if err != nil {
		slog.Warn("provision: runtime unavailable", "server_id", task.GetServerId(), "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "RUNTIME_UNAVAILABLE", Retryable: true}, nil
	}
	// Docker 不再为不存在的 bind 源目录自动建目录：先创建数据卷根目录，
	// 否则 CreateContainer 以 "bind source path does not exist" 失败。
	if err := os.MkdirAll(cfg.VolumePath, 0o755); err != nil {
		slog.Warn("provision: create data directory", "server_id", task.GetServerId(), "volume", cfg.VolumePath, "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "PROVISION_FAILED", Retryable: true}, nil
	}
	containerID, err := rt.CreateContainer(ctx, cfg)
	if err != nil {
		slog.Warn("provision: create container failed", "server_id", task.GetServerId(), "image", cfg.Image, "name", cfg.Name, "volume", cfg.VolumePath, "error", err)
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "PROVISION_FAILED", Retryable: true}, nil
	}
	result, err := json.Marshal(map[string]string{"containerId": containerID})
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "PROVISION_FAILED", Retryable: false}, nil
	}
	return &ExecutionOutcome{Succeeded: true, ResultJSON: result}, nil
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
		actionErr = rt.RemoveContainer(ctx, containerID, true)
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
