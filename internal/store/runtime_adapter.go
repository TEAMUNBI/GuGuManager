package store

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/runtime"
)

// RuntimeAdapter handles the bridge between Store operations and actual container runtime.
// It replaces the simulated sleep-based operations with real Docker API calls.
type RuntimeAdapter struct {
	docker   *runtime.DockerRuntime
	dataRoot string // Base directory for server data volumes
}

// NewRuntimeAdapter creates a new runtime adapter with Docker backend.
func NewRuntimeAdapter(dataRoot string) (*RuntimeAdapter, error) {
	docker, err := runtime.NewDockerRuntime()
	if err != nil {
		return nil, fmt.Errorf("initialize docker runtime: %w", err)
	}

	return &RuntimeAdapter{
		docker:   docker,
		dataRoot: dataRoot,
	}, nil
}

// Close closes the runtime adapter and its underlying connections.
func (r *RuntimeAdapter) Close() error {
	return r.docker.Close()
}

// ProvisionServer creates a new container for a game server.
func (r *RuntimeAdapter) ProvisionServer(ctx context.Context, server domain.Server, startup domain.Startup, values map[string]any) (containerID string, err error) {
	// Build environment variables from startup configuration
	env := make(map[string]string)
	for key, value := range values {
		env[key] = fmt.Sprintf("%v", value)
	}

	// Add common environment variables
	env["SERVER_NAME"] = server.Name
	env["EULA"] = "TRUE" // Accept EULA for game servers

	// Parse port from allocation
	var mainPort int
	if _, err := fmt.Sscanf(server.Allocation, "%*[^:]:%d", &mainPort); err == nil && mainPort > 0 {
		// Port successfully extracted
	} else {
		mainPort = 25565 // Default Minecraft port
	}

	// Determine Docker image from game definition
	image := getDockerImageForGame(server.GameID, server.GameVersion)

	// Create data directory for this server
	volumePath := filepath.Join(r.dataRoot, server.ID)

	cfg := runtime.ContainerConfig{
		Name:  fmt.Sprintf("gugu-server-%s", server.ID),
		Image: image,
		Env:   env,
		PortBindings: map[int]int{
			25565: mainPort, // Map container port to allocated host port
		},
		VolumePath: volumePath,
		MemoryMB:   server.Metrics.MemoryLimit / (1024 * 1024),
		CPUShares:  1024, // Default CPU shares
	}

	containerID, err = r.docker.CreateContainer(ctx, cfg)
	if err != nil {
		return "", fmt.Errorf("create container for server %s: %w", server.ID, err)
	}

	return containerID, nil
}

// StartServer starts a stopped container.
func (r *RuntimeAdapter) StartServer(ctx context.Context, containerID string) error {
	return r.docker.StartContainer(ctx, containerID)
}

// StopServer stops a running container gracefully.
func (r *RuntimeAdapter) StopServer(ctx context.Context, containerID string) error {
	return r.docker.StopContainer(ctx, containerID, 30) // 30 second timeout
}

// RestartServer restarts a container.
func (r *RuntimeAdapter) RestartServer(ctx context.Context, containerID string) error {
	return r.docker.RestartContainer(ctx, containerID, 30)
}

// DeleteServer removes a container and its resources.
func (r *RuntimeAdapter) DeleteServer(ctx context.Context, containerID string) error {
	return r.docker.RemoveContainer(ctx, containerID, true) // Force removal
}

// GetServerStatus retrieves the current status of a container.
func (r *RuntimeAdapter) GetServerStatus(ctx context.Context, containerID string) (power string, health string, err error) {
	status, err := r.docker.InspectContainer(ctx, containerID)
	if err != nil {
		return "unknown", "unknown", err
	}

	// Map Docker state to GuGuManager power states
	switch status.State {
	case "running":
		power = "running"
	case "exited", "dead":
		power = "stopped"
	case "created":
		power = "stopped"
	case "restarting":
		power = "starting"
	default:
		power = "unknown"
	}

	// Map health status
	if status.Healthy {
		health = "healthy"
	} else if power == "running" {
		health = "unhealthy"
	} else {
		health = "unknown"
	}

	return power, health, nil
}

// getDockerImageForGame returns the appropriate Docker image for a game.
// This is a simplified implementation; production should read from GameDefinition.
func getDockerImageForGame(gameID string, gameVersion string) string {
	switch gameID {
	case "papermc":
		return "itzg/minecraft-server:latest"
	case "factorio":
		return "factoriotools/factorio:latest"
	case "vintagestory":
		return "copygirl/vintagestory:latest"
	default:
		return "itzg/minecraft-server:latest"
	}
}

// Now integrate this into Memory store by replacing finishProvision and finishPower

// finishProvisionWithRuntime executes real container provisioning.
func (m *Memory) finishProvisionWithRuntime(operationID string, serverID string, adapter *RuntimeAdapter) {
	if !m.beginOperation(operationID) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	m.mu.Lock()
	operation, operationOK := m.operations[operationID]
	server, serverOK := m.servers[serverID]
	startup, startupOK := m.startups[serverID]
	values := m.startupValues[serverID]
	m.mu.Unlock()

	if !operationOK || !serverOK || !startupOK {
		m.mu.Lock()
		failOperationLocked(&operation, domain.OperationError{
			Code:      "INTERNAL_ERROR",
			Message:   "服务器或配置不存在",
			Retryable: false,
		}, time.Now().UTC())
		m.operations[operationID] = operation
		m.mu.Unlock()
		return
	}

	// Actually create the container
	containerID, err := adapter.ProvisionServer(ctx, server, startup, values)
	if err != nil {
		m.mu.Lock()
		failOperationLocked(&operation, domain.OperationError{
			Code:      "PROVISION_FAILED",
			Message:   fmt.Sprintf("创建容器失败: %v", err),
			Retryable: true,
		}, time.Now().UTC())
		m.operations[operationID] = operation
		m.mu.Unlock()
		return
	}

	// Update operation and server state
	now := time.Now().UTC()
	m.mu.Lock()
	operation, operationOK = m.operations[operationID]
	server, serverOK = m.servers[serverID]
	finished := operationOK && completeOperationForGenerationLocked(&operation, server, serverOK, now)
	if finished {
		m.operations[operationID] = operation
	}
	if finished && operation.Status == "succeeded" {
		server.LifecycleState = "ready"
		server.ObservedPower = "stopped"
		server.ObservedGeneration = server.Generation
		server.ObservedAt = now
		server.UpdatedAt = now
		// Store container ID for future operations
		if server.Metadata == nil {
			server.Metadata = make(map[string]string)
		}
		server.Metadata["containerID"] = containerID
		m.servers[serverID] = server
	}
	m.mu.Unlock()
}

// finishPowerWithRuntime executes real container power operations.
func (m *Memory) finishPowerWithRuntime(operationID string, serverID string, action domain.PowerAction, adapter *RuntimeAdapter) {
	if !m.beginOperation(operationID) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	m.mu.Lock()
	operation, operationOK := m.operations[operationID]
	server, serverOK := m.servers[serverID]
	containerID := ""
	if serverOK && server.Metadata != nil {
		containerID = server.Metadata["containerID"]
	}
	m.mu.Unlock()

	if !operationOK || !serverOK || containerID == "" {
		m.mu.Lock()
		failOperationLocked(&operation, domain.OperationError{
			Code:      "INTERNAL_ERROR",
			Message:   "服务器或容器 ID 不存在",
			Retryable: false,
		}, time.Now().UTC())
		m.operations[operationID] = operation
		m.mu.Unlock()
		return
	}

	// Execute the actual Docker operation
	var err error
	switch action {
	case domain.PowerStart:
		err = adapter.StartServer(ctx, containerID)
	case domain.PowerStop:
		err = adapter.StopServer(ctx, containerID)
	case domain.PowerRestart:
		err = adapter.RestartServer(ctx, containerID)
	default:
		err = fmt.Errorf("unsupported power action: %s", action)
	}

	if err != nil {
		m.mu.Lock()
		failOperationLocked(&operation, domain.OperationError{
			Code:      "POWER_OPERATION_FAILED",
			Message:   fmt.Sprintf("电源操作失败: %v", err),
			Retryable: true,
		}, time.Now().UTC())
		m.operations[operationID] = operation
		m.mu.Unlock()
		return
	}

	// Poll for actual status
	time.Sleep(2 * time.Second)
	power, health, err := adapter.GetServerStatus(ctx, containerID)
	if err != nil {
		power = "unknown"
		health = "unknown"
	}

	// Update operation and server state
	now := time.Now().UTC()
	m.mu.Lock()
	operation, operationOK = m.operations[operationID]
	server, serverOK = m.servers[serverID]
	finished := operationOK && completeOperationForGenerationLocked(&operation, server, serverOK, now)
	if finished {
		m.operations[operationID] = operation
	}
	if finished && operation.Status == "succeeded" {
		server.ObservedPower = power
		server.HealthCondition = health
		server.ObservedGeneration = server.Generation
		server.ObservedAt = now
		server.UpdatedAt = now
		m.servers[serverID] = server
	}
	m.mu.Unlock()
}
