package runtime

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// containerStateString returns a human-readable string representation of a
// container state. It mirrors the String method that was removed from
// github.com/docker/docker/api/types.ContainerState.
func containerStateString(s *types.ContainerState) string {
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
	_, err = cli.Ping(ctx)
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
	Name         string            // Container name
	Image        string            // Docker image (e.g., "itzg/minecraft-server:latest")
	Env          map[string]string // Environment variables
	PortBindings map[int]int       // Container port -> host port mapping
	VolumePath   string            // Host path to mount as /data
	MemoryMB     int64             // Memory limit in MB
	CPUShares    int64             // CPU shares (relative weight)
}

// CreateContainer creates and starts a new container.
func (r *DockerRuntime) CreateContainer(ctx context.Context, cfg ContainerConfig) (containerID string, err error) {
	// Pull image if not present
	_, _, err = r.client.ImageInspectWithRaw(ctx, cfg.Image)
	if err != nil {
		pullReader, pullErr := r.client.ImagePull(ctx, cfg.Image, image.PullOptions{})
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
	exposedPorts := nat.PortSet{}
	portBindings := nat.PortMap{}
	for containerPort, hostPort := range cfg.PortBindings {
		port := nat.Port(fmt.Sprintf("%d/tcp", containerPort))
		exposedPorts[port] = struct{}{}
		portBindings[port] = []nat.PortBinding{
			{
				HostIP:   "0.0.0.0",
				HostPort: fmt.Sprintf("%d", hostPort),
			},
		}
	}

	// Container configuration
	containerCfg := &container.Config{
		Image:        cfg.Image,
		Env:          env,
		ExposedPorts: exposedPorts,
	}

	// Host configuration (resources and bindings)
	hostCfg := &container.HostConfig{
		PortBindings: portBindings,
		Resources: container.Resources{
			Memory:    cfg.MemoryMB * 1024 * 1024, // Convert MB to bytes
			CPUShares: cfg.CPUShares,
		},
		RestartPolicy: container.RestartPolicy{
			Name: "unless-stopped",
		},
	}

	// Mount data volume if specified
	if cfg.VolumePath != "" {
		hostCfg.Mounts = []mount.Mount{
			{
				Type:   mount.TypeBind,
				Source: cfg.VolumePath,
				Target: "/data",
			},
		}
	}

	// Create container
	resp, err := r.client.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, cfg.Name)
	if err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}

	// Start container
	if err := r.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		// Cleanup on failure
		_ = r.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return "", fmt.Errorf("start container: %w", err)
	}

	return resp.ID, nil
}

// StopContainer stops a running container gracefully.
func (r *DockerRuntime) StopContainer(ctx context.Context, containerID string, timeoutSec int) error {
	timeout := timeoutSec
	return r.client.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
}

// StartContainer starts a stopped container.
func (r *DockerRuntime) StartContainer(ctx context.Context, containerID string) error {
	return r.client.ContainerStart(ctx, containerID, container.StartOptions{})
}

// RestartContainer restarts a container.
func (r *DockerRuntime) RestartContainer(ctx context.Context, containerID string, timeoutSec int) error {
	timeout := timeoutSec
	return r.client.ContainerRestart(ctx, containerID, container.StopOptions{Timeout: &timeout})
}

// RemoveContainer removes a container (must be stopped first unless force=true).
func (r *DockerRuntime) RemoveContainer(ctx context.Context, containerID string, force bool) error {
	return r.client.ContainerRemove(ctx, containerID, container.RemoveOptions{
		Force:         force,
		RemoveVolumes: true,
	})
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
	inspect, err := r.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return ContainerStatus{}, fmt.Errorf("inspect container: %w", err)
	}

	status := ContainerStatus{
		ID:      inspect.ID,
		State:   inspect.State.Status,
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
	return r.client.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       fmt.Sprintf("%d", tail),
		Timestamps: true,
	})
}

// AttachToContainer attaches to a container's stdin/stdout for console interaction.
func (r *DockerRuntime) AttachToContainer(ctx context.Context, containerID string) (io.ReadWriteCloser, error) {
	resp, err := r.client.ContainerAttach(ctx, containerID, container.AttachOptions{
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
