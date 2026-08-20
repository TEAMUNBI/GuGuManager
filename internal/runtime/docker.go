package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// containerStateString returns a human-readable string representation of a
// container state. It preserves the status rendering used by the old Docker
// client while accepting the Moby API's container.State value.
func containerStateString(s *container.State) string {
	if s == nil {
		return "unknown"
	}
	if s.Running {
		if s.Paused {
			return "paused"
		}
		if s.Restarting {
			return "restarting"
		}
		return "running"
	}
	if s.Dead {
		return "dead"
	}
	if s.StartedAt != "" {
		return "exited"
	}
	return "created"
}

// DockerRuntime implements container operations using Docker Engine API.
type DockerRuntime struct {
	client *client.Client
}

// NewDockerRuntime creates a new Docker runtime adapter.
// It connects to the Docker daemon using the default socket or DOCKER_HOST environment variable.
func NewDockerRuntime() (*DockerRuntime, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}

	// Verify connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = cli.Ping(ctx, client.PingOptions{})
	if err != nil {
		return nil, fmt.Errorf("ping docker daemon: %w", err)
	}

	return &DockerRuntime{client: cli}, nil
}

// Close closes the Docker client connection.
func (r *DockerRuntime) Close() error {
	return r.client.Close()
}

// ContainerConfig holds the parameters for creating a container.
type ContainerConfig struct {
	Name          string            // Container name
	Image         string            // Immutable Docker image reference
	Entrypoint    []string          // Trusted runtime entrypoint
	Cmd           []string          // Trusted runtime arguments
	WorkingDir    string            // Trusted container working directory
	User          string            // Trusted container user
	Env           map[string]string // Environment variables
	Labels        map[string]string // Immutable GuGuManager ownership/spec labels
	PortBindings  map[int]int       // Container port -> host port mapping
	PortProtocols map[int]string    // Container port -> tcp/udp
	PortBindIPs   map[int]string    // Container port -> host bind address
	BindIP        string            // Host bind address; empty means wildcard
	VolumePath    string            // Legacy host path mount
	VolumeName    string            // Docker named volume mount
	VolumeTarget  string            // Named/bind volume target, default /data
	MemoryMB      int64             // Memory limit in MB
	CPUShares     int64             // CPU shares (relative weight)
	PIDsLimit     int64             // Optional process limit
	HealthCheck   *container.HealthConfig
	StartOnCreate *bool // nil preserves the historical auto-start behavior
}

// CreateContainer creates and starts a new container.
func (r *DockerRuntime) CreateContainer(ctx context.Context, cfg ContainerConfig) (containerID string, err error) {
	// Pull image if not present
	_, err = r.client.ImageInspect(ctx, cfg.Image)
	if err != nil {
		pullReader, pullErr := r.client.ImagePull(ctx, cfg.Image, client.ImagePullOptions{})
		if pullErr != nil {
			return "", fmt.Errorf("pull image %s: %w", cfg.Image, pullErr)
		}
		defer pullReader.Close()
		// Consume the pull output to ensure it completes
		_, _ = io.Copy(io.Discard, pullReader)
	}

	// Convert environment variables
	env := []string{}
	for key, value := range cfg.Env {
		env = append(env, fmt.Sprintf("%s=%s", key, value))
	}

	// Convert port bindings
	exposedPorts := network.PortSet{}
	portBindings := network.PortMap{}
	for containerPort, hostPort := range cfg.PortBindings {
		protocol := network.TCP
		if strings.EqualFold(cfg.PortProtocols[containerPort], "udp") {
			protocol = network.UDP
		}
		port, ok := network.PortFrom(uint16(containerPort), protocol)
		if !ok {
			return "", fmt.Errorf("invalid container port %d", containerPort)
		}
		bindIP := netip.IPv4Unspecified()
		configuredBindIP := cfg.PortBindIPs[containerPort]
		if configuredBindIP == "" {
			configuredBindIP = cfg.BindIP
		}
		if configuredBindIP != "" {
			parsed, parseErr := netip.ParseAddr(configuredBindIP)
			if parseErr != nil {
				return "", fmt.Errorf("invalid bind IP %q: %w", configuredBindIP, parseErr)
			}
			bindIP = parsed
		}
		exposedPorts[port] = struct{}{}
		portBindings[port] = []network.PortBinding{
			{
				HostIP:   bindIP,
				HostPort: fmt.Sprintf("%d", hostPort),
			},
		}
	}

	// Container configuration
	containerCfg := &container.Config{
		Image:        cfg.Image,
		Env:          env,
		ExposedPorts: exposedPorts,
		Entrypoint:   append([]string(nil), cfg.Entrypoint...),
		Cmd:          append([]string(nil), cfg.Cmd...),
		WorkingDir:   cfg.WorkingDir,
		User:         cfg.User,
		Healthcheck:  cfg.HealthCheck,
		Labels:       cfg.Labels,
	}

	// Host configuration (resources and bindings)
	hostCfg := &container.HostConfig{
		PortBindings: portBindings,
		Resources: container.Resources{
			Memory:    cfg.MemoryMB * 1024 * 1024, // Convert MB to bytes
			CPUShares: cfg.CPUShares,
			PidsLimit: func() *int64 {
				if cfg.PIDsLimit > 0 {
					return &cfg.PIDsLimit
				}
				return nil
			}(),
		},
		RestartPolicy: container.RestartPolicy{
			Name: "unless-stopped",
		},
	}

	// Mount data volume if specified
	volumeTarget := cfg.VolumeTarget
	if volumeTarget == "" {
		volumeTarget = "/data"
	}
	if cfg.VolumeName != "" {
		if _, err := r.client.VolumeCreate(ctx, client.VolumeCreateOptions{Name: cfg.VolumeName}); err != nil {
			return "", fmt.Errorf("create named volume %s: %w", cfg.VolumeName, err)
		}
		hostCfg.Mounts = []mount.Mount{{Type: mount.TypeVolume, Source: cfg.VolumeName, Target: volumeTarget}}
	} else if cfg.VolumePath != "" {
		hostCfg.Mounts = []mount.Mount{
			{
				Type:   mount.TypeBind,
				Source: cfg.VolumePath,
				Target: volumeTarget,
			},
		}
	}

	// Create container
	resp, err := r.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     containerCfg,
		HostConfig: hostCfg,
		Name:       cfg.Name,
	})
	if err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}

	// Start container unless the caller explicitly wants a provisioned/stopped container.
	shouldStart := true
	if cfg.StartOnCreate != nil {
		shouldStart = *cfg.StartOnCreate
	}
	if shouldStart {
		if _, err := r.client.ContainerStart(ctx, resp.ID, client.ContainerStartOptions{}); err != nil {
			// Cleanup on failure
			_, _ = r.client.ContainerRemove(ctx, resp.ID, client.ContainerRemoveOptions{Force: true})
			return "", fmt.Errorf("start container: %w", err)
		}
	}

	return resp.ID, nil
}

// StopContainer stops a running container gracefully.
func (r *DockerRuntime) StopContainer(ctx context.Context, containerID string, timeoutSec int) error {
	timeout := timeoutSec
	_, err := r.client.ContainerStop(ctx, containerID, client.ContainerStopOptions{Timeout: &timeout})
	return err
}

// StartContainer starts a stopped container.
func (r *DockerRuntime) StartContainer(ctx context.Context, containerID string) error {
	_, err := r.client.ContainerStart(ctx, containerID, client.ContainerStartOptions{})
	return err
}

// RestartContainer restarts a container.
func (r *DockerRuntime) RestartContainer(ctx context.Context, containerID string, timeoutSec int) error {
	timeout := timeoutSec
	_, err := r.client.ContainerRestart(ctx, containerID, client.ContainerRestartOptions{Timeout: &timeout})
	return err
}

// KillContainer sends Docker's default SIGKILL to the container's init
// process. It deliberately leaves the stopped container in place so a force
// stop cannot destroy the runtime that start/reconcile still owns.
func (r *DockerRuntime) KillContainer(ctx context.Context, containerID string) error {
	_, err := r.client.ContainerKill(ctx, containerID, client.ContainerKillOptions{})
	return err
}

// RemoveContainer removes a container (must be stopped first unless force=true).
func (r *DockerRuntime) RemoveContainer(ctx context.Context, containerID string, force bool) error {
	_, err := r.client.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{
		Force:         force,
		RemoveVolumes: true,
	})
	return err
}

// RenameContainer atomically changes only the Docker name. Reconcile uses it
// after the candidate runtime has passed health checks so the canonical
// gugu-server-<id> name always identifies the active generation.
func (r *DockerRuntime) RenameContainer(ctx context.Context, containerID, newName string) error {
	_, err := r.client.ContainerRename(ctx, containerID, client.ContainerRenameOptions{NewName: newName})
	return err
}

// ContainerStatus represents the current state of a container.
type ContainerStatus struct {
	ID      string
	State   string // "created", "running", "paused", "restarting", "removing", "exited", "dead"
	Status  string // Human-readable status
	Running bool
	Healthy bool // Based on health check if configured
}

// InspectContainer gets the current status of a container.
func (r *DockerRuntime) InspectContainer(ctx context.Context, containerID string) (ContainerStatus, error) {
	inspectResult, err := r.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return ContainerStatus{}, fmt.Errorf("inspect container: %w", err)
	}
	inspect := inspectResult.Container
	if inspect.State == nil {
		return ContainerStatus{}, fmt.Errorf("inspect container: missing state")
	}

	status := ContainerStatus{
		ID:      inspect.ID,
		State:   string(inspect.State.Status),
		Status:  containerStateString(inspect.State),
		Running: inspect.State.Running,
	}

	// Check health if available
	if inspect.State.Health != nil {
		status.Healthy = inspect.State.Health.Status == "healthy"
	} else {
		// No health check configured, assume healthy if running
		status.Healthy = inspect.State.Running
	}

	return status, nil
}

// ContainerLogs retrieves logs from a container.
func (r *DockerRuntime) ContainerLogs(ctx context.Context, containerID string, tail int) (io.ReadCloser, error) {
	return r.client.ContainerLogs(ctx, containerID, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       fmt.Sprintf("%d", tail),
		Timestamps: true,
	})
}

// AttachToContainer attaches to a container's stdin/stdout for console interaction.
func (r *DockerRuntime) AttachToContainer(ctx context.Context, containerID string) (io.ReadWriteCloser, error) {
	resp, err := r.client.ContainerAttach(ctx, containerID, client.ContainerAttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return nil, fmt.Errorf("attach container: %w", err)
	}
	return resp.Conn, nil
}

// ContainerStats 是一次 docker stats 单容器采样的结果。
type ContainerStats struct {
	CPUPercent       float64
	MemoryBytes      uint64
	MemoryLimitBytes uint64
	NetworkRxBytes   uint64
	NetworkTxBytes   uint64
}

// dockerStatsSnapshot 是 docker stats 响应中我们需要的字段子集。
type dockerStatsSnapshot struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs  uint32 `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemUsage uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64 `json:"usage"`
		Limit uint64 `json:"limit"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
}

// containerStatsFromJSON 解析 docker stats 单容器响应为 ContainerStats。
// CPU% = (Δtotal / Δsystem) * onlineCPUs * 100；首次采样（precpu 为零）返回 0。
func containerStatsFromJSON(data []byte) (ContainerStats, error) {
	var stats dockerStatsSnapshot
	if len(data) == 0 {
		return ContainerStats{}, fmt.Errorf("empty stats payload")
	}
	if err := json.Unmarshal(data, &stats); err != nil {
		return ContainerStats{}, fmt.Errorf("decode container stats: %w", err)
	}
	var out ContainerStats
	out.MemoryBytes = stats.MemoryStats.Usage
	out.MemoryLimitBytes = stats.MemoryStats.Limit
	if stats.PreCPUStats.CPUUsage.TotalUsage > 0 &&
		stats.CPUStats.SystemUsage > stats.PreCPUStats.SystemUsage &&
		stats.CPUStats.OnlineCPUs > 0 {
		cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
		systemDelta := float64(stats.CPUStats.SystemUsage - stats.PreCPUStats.SystemUsage)
		if systemDelta > 0 {
			out.CPUPercent = cpuDelta / systemDelta * float64(stats.CPUStats.OnlineCPUs) * 100
		}
	}
	for _, netStats := range stats.Networks {
		out.NetworkRxBytes += netStats.RxBytes
		out.NetworkTxBytes += netStats.TxBytes
	}
	return out, nil
}

// ContainerStats 采集单个容器的实时资源使用（docker stats 单容器）。
func (r *DockerRuntime) ContainerStats(ctx context.Context, containerID string) (ContainerStats, error) {
	resp, err := r.client.ContainerStats(ctx, containerID, client.ContainerStatsOptions{
		IncludePreviousSample: true,
	})
	if err != nil {
		return ContainerStats{}, fmt.Errorf("container stats: %w", err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, resp.Body); err != nil {
		return ContainerStats{}, fmt.Errorf("read container stats: %w", err)
	}
	return containerStatsFromJSON(buf.Bytes())
}

// ExecInContainer 在容器内执行命令并返回合并的 stdout/stderr 输出。
func (r *DockerRuntime) ExecInContainer(ctx context.Context, containerID string, argv []string) (string, error) {
	execConfig := client.ExecCreateOptions{
		Cmd:          argv,
		AttachStdout: true,
		AttachStderr: true,
	}
	execID, err := r.client.ExecCreate(ctx, containerID, execConfig)
	if err != nil {
		return "", fmt.Errorf("exec create: %w", err)
	}
	attach, err := r.client.ExecAttach(ctx, execID.ID, client.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("exec attach: %w", err)
	}
	defer attach.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, attach.Reader); err != nil {
		return "", fmt.Errorf("exec read: %w", err)
	}
	return buf.String(), nil
}

// FollowLogs 从容器实时输出流式读取日志（docker logs -f --timestamps）。
// 返回的 ReadCloser 需要调用方在结束后 Close；ctx 取消会中断读取。
func (r *DockerRuntime) FollowLogs(ctx context.Context, containerID string) (io.ReadCloser, error) {
	return r.client.ContainerLogs(ctx, containerID, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: true,
	})
}

// InspectEnv 返回容器当前环境变量（docker inspect .Config.Env）。
func (r *DockerRuntime) InspectEnv(ctx context.Context, containerID string) (map[string]string, error) {
	inspectResult, err := r.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("inspect container env: %w", err)
	}
	if inspectResult.Container.Config == nil {
		return nil, fmt.Errorf("inspect container env: missing config")
	}
	env := make(map[string]string, len(inspectResult.Container.Config.Env))
	for _, pair := range inspectResult.Container.Config.Env {
		if idx := strings.IndexByte(pair, '='); idx >= 0 {
			env[pair[:idx]] = pair[idx+1:]
		}
	}
	return env, nil
}

// CopyArchiveToContainer 把宿主机上的归档复制进容器目标路径（docker cp 语义）。
func (r *DockerRuntime) CopyArchiveToContainer(ctx context.Context, containerID, hostPath, containerPath string) error {
	file, err := os.Open(hostPath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer file.Close()
	if _, err := r.client.CopyToContainer(ctx, containerID, client.CopyToContainerOptions{
		DestinationPath: containerPath,
		Content:         file,
	}); err != nil {
		return fmt.Errorf("copy to container: %w", err)
	}
	return nil
}

// CopyArchiveFromContainer 把容器内路径复制为宿主机归档文件（docker cp 语义）。
func (r *DockerRuntime) CopyArchiveFromContainer(ctx context.Context, containerID, containerPath, hostPath string) error {
	result, err := r.client.CopyFromContainer(ctx, containerID, client.CopyFromContainerOptions{
		SourcePath: containerPath,
	})
	if err != nil {
		return fmt.Errorf("copy from container: %w", err)
	}
	reader := result.Content
	defer reader.Close()
	file, err := os.Create(hostPath)
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	defer file.Close()
	if _, err := io.Copy(file, reader); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}
	return nil
}

// RestoreNamedVolume restores a validated gzip tar archive into the named
// volume mounted at /data by using a short-lived helper container. This keeps
// Docker Desktop volume contents inside the Linux VM instead of relying on a
// Windows host bind path.
func (r *DockerRuntime) RestoreNamedVolume(ctx context.Context, containerID, archivePath string) error {
	inspectResult, err := r.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("inspect restore target: %w", err)
	}
	inspect := inspectResult.Container
	if inspect.Config == nil || inspect.Config.Image == "" {
		return fmt.Errorf("restore target has no image")
	}
	volumeName := ""
	volumeTarget := "/data"
	for _, mounted := range inspect.Mounts {
		if mounted.Type == mount.TypeVolume && mounted.Destination == "/data" {
			volumeName = mounted.Name
			break
		}
	}
	if volumeName == "" {
		return fmt.Errorf("restore target has no named /data volume")
	}

	helper, err := r.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:      inspect.Config.Image,
			Entrypoint: []string{"sh", "-c"},
			Cmd:        []string{"sleep 300"},
		},
		HostConfig: &container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: volumeName, Target: volumeTarget}}},
		Name:       fmt.Sprintf("gugu-restore-%d", time.Now().UTC().UnixNano()),
	})
	if err != nil {
		return fmt.Errorf("create restore helper: %w", err)
	}
	defer func() {
		_, _ = r.client.ContainerRemove(context.Background(), helper.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if _, err := r.client.ContainerStart(ctx, helper.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("start restore helper: %w", err)
	}

	archive, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open restore archive: %w", err)
	}
	defer archive.Close()
	info, err := archive.Stat()
	if err != nil {
		return fmt.Errorf("stat restore archive: %w", err)
	}
	var wrapper bytes.Buffer
	tarWriter := tar.NewWriter(&wrapper)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "restore.tar.gz", Mode: 0o600, Size: info.Size(), ModTime: time.Unix(0, 0)}); err != nil {
		return fmt.Errorf("write restore wrapper header: %w", err)
	}
	if _, err := io.Copy(tarWriter, archive); err != nil {
		return fmt.Errorf("write restore wrapper: %w", err)
	}
	if err := tarWriter.Close(); err != nil {
		return fmt.Errorf("close restore wrapper: %w", err)
	}
	if _, err := r.client.CopyToContainer(ctx, helper.ID, client.CopyToContainerOptions{DestinationPath: "/tmp", Content: bytes.NewReader(wrapper.Bytes())}); err != nil {
		return fmt.Errorf("copy restore archive: %w", err)
	}
	_, err = r.ExecInContainer(ctx, helper.ID, []string{"sh", "-c", "find /data -mindepth 1 -maxdepth 1 -exec rm -rf -- {} + && tar -xzf /tmp/restore.tar.gz -C /data"})
	if err != nil {
		return fmt.Errorf("activate restored volume: %w", err)
	}
	return nil
}

// ListRunningContainers 返回名称以 namePrefix 开头的运行中容器，去掉前缀。
func (r *DockerRuntime) ListRunningContainers(ctx context.Context, namePrefix string) ([]string, error) {
	result, err := r.client.ContainerList(ctx, client.ContainerListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	var out []string
	for _, c := range result.Items {
		for _, name := range c.Names {
			trimmed := strings.TrimPrefix(name, "/")
			if strings.HasPrefix(trimmed, namePrefix) {
				out = append(out, trimmed[len(namePrefix):])
				break
			}
		}
	}
	return out, nil
}
