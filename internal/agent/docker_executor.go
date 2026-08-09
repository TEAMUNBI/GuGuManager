package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// ExecuteTask 按 task.Type 分发任务（provision / power），返回执行结果。
// 返回的 error 仅表示执行器自身故障；任务失败以 ExecutionOutcome 表达。
func (e *DockerExecutor) ExecuteTask(ctx context.Context, task *agentv1.Task) (*ExecutionOutcome, error) {
	switch task.GetType() {
	case "provision":
		return e.executeProvision(ctx, task)
	case "power":
		return e.executePower(ctx, task)
	default:
		return &ExecutionOutcome{
			Succeeded: false,
			ErrorCode: "UNSUPPORTED_TASK",
			Retryable: false,
		}, nil
	}
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
