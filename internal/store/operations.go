package store

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/id"
)

func (m *Memory) CreateServer(input domain.CreateServerInput, idempotencyKey string, actor domain.User) (domain.Operation, error) {
	input.Name = strings.TrimSpace(input.Name)
	if len([]rune(input.Name)) < 1 || len([]rune(input.Name)) > 64 {
		return domain.Operation{}, domain.NewProblem("VALIDATION_FAILED", "服务器名称需要在 1 到 64 个字符之间", false)
	}
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return domain.Operation{}, err
	}
	if input.MemoryMB < 512 || input.MemoryMB > 131072 || input.DiskGB < 1 || input.DiskGB > 2048 {
		return domain.Operation{}, domain.NewProblem("VALIDATION_FAILED", "资源配额超出允许范围", false)
	}

	digest := requestDigest(input)
	scope := idempotencyScope("server:create", actor.ID, "", idempotencyKey)
	m.mu.Lock()
	now := time.Now().UTC()
	currentActor, authErr := m.currentActorLocked(actor.ID)
	if authErr != nil || !hasRole(currentActor, "platform_admin") {
		if authErr == nil {
			authErr = domain.NewProblem("FORBIDDEN", "需要平台管理员权限", false)
		}
		m.mu.Unlock()
		return domain.Operation{}, authErr
	}
	m.reconcileNodeLivenessLocked(now)
	if existing, ok, err := m.idempotentOperationLocked(scope, digest); err != nil || ok {
		m.mu.Unlock()
		return existing, err
	}
	node, nodeOK := m.nodes[input.NodeID]
	game, gameOK := m.games[input.GameDefinitionID]
	if !nodeOK || !gameOK {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("VALIDATION_FAILED", "节点或游戏包不存在", false)
	}
	if game.Status != "approved" {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("GAME_DEFINITION_NOT_APPROVED", "游戏包尚未通过审核", false)
	}
	if input.GameBundleDigest != game.BundleDigest {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("PACKAGE_INCOMPATIBLE", "请求的 Bundle 摘要与游戏目录不匹配", false)
	}
	if !game.Runnable {
		m.mu.Unlock()
		return domain.Operation{}, packageRuntimeTargetUnavailable(game)
	}
	if node.Condition != "available" {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("NODE_OFFLINE", "节点当前不可接收新任务", true)
	}
	memoryBytes := int64(input.MemoryMB) * 1024 * 1024
	diskBytes := int64(input.DiskGB) * 1024 * 1024 * 1024
	if memoryBytes > node.MemoryBytes-node.AllocatedMemoryBytes || diskBytes > node.DiskBytes-node.AllocatedDiskBytes {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("INSUFFICIENT_RESOURCE", "节点剩余资源不足", false)
	}

	portRef, protocol, firstPort, containerPort, role := allocationSettingsForGame(game)
	port, portAvailable := m.nextAllocationPortLocked(node.ID, node.Address, protocol, firstPort)
	if !portAvailable {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("PORT_CONFLICT", "节点没有可用的游戏端口", false)
	}

	serverID := id.New()
	operation := domain.NewQueuedOperation(id.New(), serverID, node.ID, domain.PowerAction("provision"), 1, idempotencyKey, now)
	allocation := domain.Allocation{
		ID: id.New(), ServerID: serverID, NodeID: node.ID, BindIP: node.Address,
		Port: port, Protocol: protocol, PortRef: portRef, ContainerPort: containerPort, Role: role,
		Primary: true, CreatedAt: now, UpdatedAt: now,
	}
	server := domain.Server{
		ID: serverID, Name: strings.TrimSpace(input.Name), Description: "新建开发服务器", GameID: game.ID, GameBundleDigest: game.BundleDigest, GameDefinitionVersion: game.Version, GameName: game.Name, GameVersion: game.GameVersion,
		NodeID: node.ID, NodeName: node.Name, LifecycleState: "provisioning", DesiredPower: "stopped", ObservedPower: "unknown", NodeCondition: node.Condition, HealthCondition: "unknown",
		Generation: 1, ObservedGeneration: 0, ObservedAt: now, Allocation: allocationAddress(allocation), OwnerName: currentActor.DisplayName, Metrics: domain.ResourceMetrics{MemoryLimit: memoryBytes, DiskLimit: diskBytes}, MetricHistory: []domain.MetricPoint{}, UpdatedAt: now,
	}
	startup, startupValues, err := startupFromFixedBundle(server, game, nil)
	if err != nil {
		m.mu.Unlock()
		return domain.Operation{}, err
	}
	for _, variable := range startup.Variables {
		if variable.Key != "memory_mb" {
			continue
		}
		if err := applyStartupOverrides(startup.Variables, startupValues, map[string]any{"memory_mb": int64(input.MemoryMB)}); err != nil {
			m.mu.Unlock()
			return domain.Operation{}, err
		}
		break
	}
	if err := m.createServerFileSystemLocked(serverID); err != nil {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法创建服务器数据目录", true)
	}
	m.servers[server.ID] = server
	m.serverOrder = append(m.serverOrder, server.ID)
	m.allocations[allocation.ID] = allocation
	m.allocationOrder[server.ID] = []string{allocation.ID}
	m.startups[server.ID] = startup
	m.startupValues[server.ID] = startupValues
	m.operations[operation.ID] = operation
	m.idempotency[scope] = idempotencyRecord{OperationID: operation.ID, RequestDigest: digest}
	node.AllocatedMemoryBytes += memoryBytes
	node.AllocatedDiskBytes += diskBytes
	node.TotalServers++
	m.nodes[node.ID] = node
	game.Servers++
	m.games[game.ID] = game
	m.mu.Unlock()

	m.recordAudit(currentActor.DisplayName, "server.create", "server", server.Name, "accepted", operation.ID)
	go m.finishProvision(operation.ID, server.ID)
	return operation, nil
}

func (m *Memory) RequestPower(serverID string, action domain.PowerAction, idempotencyKey string, actor domain.User) (domain.Operation, error) {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return domain.Operation{}, err
	}
	if action != domain.PowerStart && action != domain.PowerStop && action != domain.PowerRestart && action != domain.PowerKill {
		return domain.Operation{}, domain.NewProblem("VALIDATION_FAILED", "不支持的电源操作", false)
	}
	digest := requestDigest(struct {
		Action domain.PowerAction `json:"action"`
	}{Action: action})
	scope := idempotencyScope("server:power", actor.ID, serverID, idempotencyKey)
	m.mu.Lock()
	now := time.Now().UTC()
	currentActor, authErr := m.authorizeServerLocked(actor.ID, serverID, "servers.power")
	if authErr != nil {
		m.mu.Unlock()
		return domain.Operation{}, authErr
	}
	m.reconcileNodeLivenessLocked(now)
	if existing, ok, err := m.idempotentOperationLocked(scope, digest); err != nil || ok {
		m.mu.Unlock()
		return existing, err
	}
	server, ok := m.servers[serverID]
	if !ok {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	node := m.nodes[server.NodeID]
	if node.Condition != "available" {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("NODE_OFFLINE", "节点当前离线，无法接收电源操作", true)
	}
	if active, ok := m.activeExclusiveOperationLocked(serverID); ok {
		if active.Type == action {
			m.idempotency[scope] = idempotencyRecord{OperationID: active.ID, RequestDigest: digest}
			m.mu.Unlock()
			return active, nil
		}
		m.mu.Unlock()
		return domain.Operation{}, operationInProgress(active)
	}
	if server.LifecycleState != "ready" {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("OPERATION_IN_PROGRESS", "服务器仍在完成生命周期操作", true)
	}
	if action == domain.PowerStart || action == domain.PowerRestart {
		startup, err := m.startupTemplateForServerLocked(server)
		if err != nil {
			m.mu.Unlock()
			return domain.Operation{}, err
		}
		values := m.startupValues[serverID]
		missing := make([]string, 0)
		for _, variable := range startup.Variables {
			value, configured := values[variable.Key]
			if variable.Required && (!configured || value == nil) {
				missing = append(missing, variable.Key)
			}
		}
		if len(missing) > 0 {
			m.mu.Unlock()
			return domain.Operation{}, validationProblem("missing required Startup variables: " + strings.Join(missing, ", "))
		}
	}
	server.Generation++
	operation := domain.NewQueuedOperation(id.New(), serverID, server.NodeID, action, server.Generation, idempotencyKey, now)
	m.operations[operation.ID] = operation
	m.idempotency[scope] = idempotencyRecord{OperationID: operation.ID, RequestDigest: digest}
	if action == domain.PowerStart {
		server.DesiredPower = "running"
		server.ObservedPower = "starting"
	} else if action == domain.PowerStop || action == domain.PowerKill {
		server.DesiredPower = "stopped"
		server.ObservedPower = "stopping"
	} else {
		server.ObservedPower = "stopping"
	}
	server.NodeCondition = node.Condition
	server.UpdatedAt = now
	m.servers[serverID] = server
	m.mu.Unlock()

	m.recordAudit(currentActor.DisplayName, "server.power."+string(action), "server", server.Name, "accepted", operation.ID)
	go m.finishPower(operation.ID, serverID, action)
	return operation, nil
}

func (m *Memory) SendConsoleCommand(serverID string, command string, actor domain.User) error {
	command = strings.TrimSpace(command)
	if command == "" || len([]rune(command)) > 512 {
		return domain.NewProblem("VALIDATION_FAILED", "命令不能为空且不能超过 512 个字符", false)
	}
	m.mu.Lock()
	currentActor, authErr := m.authorizeServerLocked(actor.ID, serverID, "servers.console")
	if authErr != nil {
		m.mu.Unlock()
		return authErr
	}
	server, ok := m.servers[serverID]
	if !ok {
		m.mu.Unlock()
		return domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	if server.ObservedPower != "running" {
		m.mu.Unlock()
		return domain.NewProblem("OPERATION_CONFLICT", "服务器未运行，无法发送控制台命令", false)
	}
	lines := m.console[serverID]
	sequence := int64(1)
	if len(lines) > 0 {
		sequence = lines[len(lines)-1].Sequence + 1
	}
	now := time.Now().UTC()
	commandLines := []domain.ConsoleLine{
		{Sequence: sequence, Timestamp: now, Stream: "command", Message: "> " + command},
		{Sequence: sequence + 1, Timestamp: now.Add(20 * time.Millisecond), Stream: "stdout", Message: "[panel] command accepted by development adapter"},
	}
	m.console[serverID] = append(m.console[serverID], commandLines...)
	m.mu.Unlock()
	// 实时广播给 WebSocket 订阅者（发布在锁外，避免占用 Store 写锁）。
	m.consoleHub.Publish(serverID, commandLines)
	m.recordAudit(currentActor.DisplayName, "console.command", "server", server.Name, "accepted", id.New())
	return nil
}

// RecordConsoleCommandResult records the Agent-confirmed terminal result. The
// request identifier correlates this event with the dispatched command without
// storing command contents in the audit log.
func (m *Memory) RecordConsoleCommandResult(serverID string, actor domain.User, result domain.ConsoleCommandResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	currentActor, err := m.authorizeServerLocked(actor.ID, serverID, "servers.console")
	if err != nil {
		return err
	}
	server, ok := m.servers[serverID]
	if !ok {
		return domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	auditResult := "failure"
	if result.Succeeded {
		auditResult = "success"
	}
	m.recordAuditLocked(currentActor.DisplayName, "console.command.result", "server", server.Name, auditResult, result.RequestID)
	return nil
}

func (m *Memory) Heartbeat(nodeName string, agentVersion string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for nodeID, node := range m.nodes {
		if node.Name != nodeName {
			continue
		}
		if m.revokedNodes[nodeID] {
			return domain.NewProblem("NOT_FOUND", "节点不存在", false)
		}
		node.Condition = "available"
		node.Version = agentVersion
		node.LastHeartbeatAt = time.Now().UTC()
		m.nodes[nodeID] = node
		for serverID, server := range m.servers {
			if server.NodeID == nodeID {
				server.NodeCondition = "available"
				m.servers[serverID] = server
			}
		}
		return nil
	}
	return domain.NewProblem("NOT_FOUND", "节点不存在", false)
}

func validateIdempotencyKey(key string) error {
	if len(key) < 16 || len(key) > 128 {
		return domain.NewProblem("VALIDATION_FAILED", "Idempotency-Key 长度需要在 16 到 128 个字符之间", false)
	}
	for index := 0; index < len(key); index++ {
		if key[index] < 0x20 || key[index] > 0x7e {
			return domain.NewProblem("VALIDATION_FAILED", "Idempotency-Key 只能包含可打印 ASCII 字符", false)
		}
	}
	return nil
}

func requestDigest(value any) [32]byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("encode idempotent request: %v", err))
	}
	return sha256.Sum256(encoded)
}

func keyedRequestDigest(key [32]byte, value any) [32]byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("encode idempotent request: %v", err))
	}
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write(encoded)
	var digest [32]byte
	copy(digest[:], mac.Sum(nil))
	return digest
}

func idempotencyScope(operation string, actor string, target string, key string) string {
	return strings.Join([]string{operation, actor, target, key}, "\x00")
}

func (m *Memory) idempotentOperationLocked(scope string, digest [32]byte) (domain.Operation, bool, error) {
	record, ok := m.idempotency[scope]
	if !ok {
		return domain.Operation{}, false, nil
	}
	if record.RequestDigest != digest {
		return domain.Operation{}, false, domain.NewProblem("IDEMPOTENCY_KEY_REUSED", "幂等键已用于不同的请求内容", false)
	}
	operation, ok := m.operations[record.OperationID]
	if !ok {
		return domain.Operation{}, false, domain.NewProblem("INTERNAL_ERROR", "幂等记录指向不存在的操作", true)
	}
	return operation, true, nil
}

func (m *Memory) activeExclusiveOperationLocked(serverID string) (domain.Operation, bool) {
	for _, operation := range m.operations {
		if operation.ServerID != serverID || isTerminalOperation(operation.Status) || !isExclusiveOperation(operation.Type) {
			continue
		}
		return operation, true
	}
	return domain.Operation{}, false
}

func (m *Memory) VisibleOperations(userID string) []domain.Operation {
	m.mu.Lock()
	defer m.mu.Unlock()

	user, ok := m.users[userID]
	if !ok || user.User.Status != "active" {
		return []domain.Operation{}
	}

	admin := hasRole(user.User, "platform_admin")
	result := make([]domain.Operation, 0, len(m.operations))
	for operationID, operation := range m.operations {
		if !admin {
			membership, member := m.memberships[operation.ServerID][userID]
			if !member || !containsString(membership.Permissions, "servers.read") {
				continue
			}
		}
		normalizeOperationMetadata(&operation)
		m.operations[operationID] = operation
		result = append(result, operation)
	}

	sort.Slice(result, func(left int, right int) bool {
		if result[left].CreatedAt.Equal(result[right].CreatedAt) {
			return result[left].ID < result[right].ID
		}
		return result[left].CreatedAt.After(result[right].CreatedAt)
	})
	return result
}

func isTerminalOperation(status string) bool {
	return status == "succeeded" || status == "failed"
}

func isExclusiveOperation(operationType domain.PowerAction) bool {
	switch operationType {
	case "provision", "start", "stop", "restart", "kill", "backup", "restore", "backup-delete", "delete", "reconcile":
		return true
	default:
		return false
	}
}

const (
	memoryOperationLeaseOwner    = "development-memory-worker"
	memoryOperationLeaseDuration = 30 * time.Second
)

func normalizeOperationMetadata(operation *domain.Operation) {
	if operation.Attempt < 1 {
		operation.Attempt = 1
	}
	if operation.MaxAttempts < operation.Attempt {
		operation.MaxAttempts = operation.Attempt
	}
	if operation.Checkpoint == "" {
		switch operation.Status {
		case "succeeded":
			operation.Checkpoint = "completed"
		case "failed":
			operation.Checkpoint = "failed"
		case "running":
			operation.Checkpoint = runningCheckpoint(operation.Type)
		default:
			operation.Checkpoint = operation.Status
		}
	}
	if isTerminalOperation(operation.Status) {
		operation.LeaseOwner = nil
		operation.LeaseExpiresAt = nil
	}
	if operation.Status == "failed" && operation.Error == nil {
		operation.Error = &domain.OperationError{
			Code:      "OPERATION_FAILED",
			Message:   "异步操作未能完成",
			Retryable: false,
		}
	}
}

func runningCheckpoint(operationType domain.PowerAction) string {
	switch operationType {
	case "provision":
		return "provisioning"
	case "start", "stop", "restart", "kill":
		return "applying-power-state"
	case "backup":
		return "creating-backup"
	case "restore":
		return "restoring-backup"
	case "backup-delete":
		return "deleting-backup"
	case "reconcile":
		return "reconciling"
	default:
		return "running"
	}
}

func (m *Memory) beginOperation(operationID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	operation, ok := m.operations[operationID]
	if !ok || isTerminalOperation(operation.Status) {
		return false
	}
	now := time.Now().UTC()
	normalizeOperationMetadata(&operation)
	operation.Status = "running"
	if operation.Progress < 5 {
		operation.Progress = 5
	}
	operation.Checkpoint = runningCheckpoint(operation.Type)
	owner := memoryOperationLeaseOwner
	expiresAt := now.Add(memoryOperationLeaseDuration)
	operation.LeaseOwner = &owner
	operation.LeaseExpiresAt = &expiresAt
	operation.Error = nil
	operation.UpdatedAt = now
	m.operations[operationID] = operation
	return true
}

func completeOperationLocked(operation *domain.Operation, now time.Time) bool {
	normalizeOperationMetadata(operation)
	if isTerminalOperation(operation.Status) {
		return false
	}
	operation.Status = "succeeded"
	operation.Progress = 100
	operation.Checkpoint = "completed"
	operation.LeaseOwner = nil
	operation.LeaseExpiresAt = nil
	operation.Error = nil
	operation.UpdatedAt = now
	return true
}

func completeOperationForGenerationLocked(operation *domain.Operation, server domain.Server, serverOK bool, now time.Time) bool {
	if !operationTargetsCurrentServer(*operation, server, serverOK) {
		return failOperationLocked(operation, domain.OperationError{
			Code:      "OPERATION_STALE",
			Message:   "操作对应的服务器状态已变化",
			Retryable: false,
		}, now)
	}
	return completeOperationLocked(operation, now)
}

func operationTargetsCurrentServer(operation domain.Operation, server domain.Server, serverOK bool) bool {
	return serverOK && server.Generation == operation.Generation && server.NodeID == operation.NodeID
}

func failOperationLocked(operation *domain.Operation, failure domain.OperationError, now time.Time) bool {
	normalizeOperationMetadata(operation)
	if isTerminalOperation(operation.Status) {
		return false
	}
	operation.Status = "failed"
	operation.Progress = 100
	operation.Checkpoint = "failed"
	operation.LeaseOwner = nil
	operation.LeaseExpiresAt = nil
	operation.Error = &failure
	operation.UpdatedAt = now
	return true
}

func operationInProgress(operation domain.Operation) *domain.Problem {
	problem := domain.NewProblem("OPERATION_IN_PROGRESS", "服务器已有互斥操作正在执行", true)
	problem.Details["operationId"] = operation.ID
	problem.Details["operationType"] = operation.Type
	return problem
}

func (m *Memory) nextAllocationPortLocked(nodeID string, bindIP string, protocol string, firstPort int) (int, bool) {
	for port := firstPort; port <= 65535; port++ {
		available := true
		for _, allocation := range m.allocations {
			if allocation.NodeID == nodeID && allocation.BindIP == bindIP && allocation.Port == port && allocation.Protocol == protocol {
				available = false
				break
			}
		}
		if available {
			return port, true
		}
	}
	return 0, false
}

func allocationSettingsForGame(game domain.GameDefinition) (portRef string, protocol string, firstPort int, containerPort int, role string) {
	if game.RuntimeTarget != nil {
		for _, port := range game.RuntimeTarget.Ports {
			if port.Role == "primary" {
				protocol = strings.ToLower(port.Protocol)
				if protocol != "tcp" && protocol != "udp" {
					break
				}
				return port.Name, protocol, port.ContainerPort, port.ContainerPort, "primary"
			}
		}
	}
	protocol, firstPort = defaultAllocationSettings(game.ID)
	return "", protocol, firstPort, firstPort, "primary"
}

func defaultAllocationSettings(gameID string) (string, int) {
	switch gameID {
	case "io.gugumanager.factorio":
		return "udp", 34197
	case "io.gugumanager.vintagestory":
		return "tcp", 42420
	default:
		return "tcp", 25565
	}
}

func (m *Memory) finishProvision(operationID string, serverID string) {
	// Check if real runtime is enabled
	m.mu.RLock()
	adapter := m.runtimeAdapter
	m.mu.RUnlock()

	if adapter != nil {
		// Use real Docker runtime
		m.finishProvisionWithRuntime(operationID, serverID, adapter)
		return
	}

	// Fall back to simulated operation
	if !m.beginOperation(operationID) {
		return
	}
	time.Sleep(m.operationLatency)
	now := time.Now().UTC()
	m.mu.Lock()
	operation, operationOK := m.operations[operationID]
	server, serverOK := m.servers[serverID]
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
		m.servers[serverID] = server
	}
	m.mu.Unlock()
}

func (m *Memory) finishPower(operationID string, serverID string, action domain.PowerAction) {
	// Check if real runtime is enabled
	m.mu.RLock()
	adapter := m.runtimeAdapter
	m.mu.RUnlock()

	if adapter != nil {
		// Use real Docker runtime
		m.finishPowerWithRuntime(operationID, serverID, action, adapter)
		return
	}

	// Fall back to simulated operation
	if !m.beginOperation(operationID) {
		return
	}
	if m.operationLatency > 200*time.Millisecond {
		time.Sleep(m.operationLatency / 2)
	}
	m.mu.Lock()
	operation, operationOK := m.operations[operationID]
	if operationOK && !isTerminalOperation(operation.Status) {
		normalizeOperationMetadata(&operation)
		operation.Status = "running"
		operation.Progress = 55
		operation.Checkpoint = runningCheckpoint(operation.Type)
		operation.UpdatedAt = time.Now().UTC()
		m.operations[operationID] = operation
	}
	m.mu.Unlock()
	time.Sleep(m.operationLatency / 2)
	now := time.Now().UTC()
	m.mu.Lock()
	operation, operationOK = m.operations[operationID]
	server, serverOK := m.servers[serverID]
	finished := operationOK && completeOperationForGenerationLocked(&operation, server, serverOK, now)
	if finished {
		m.operations[operationID] = operation
	}
	if finished && operation.Status == "succeeded" {
		if action == domain.PowerStart || (action == domain.PowerRestart && server.DesiredPower == "running") {
			server.ObservedPower = "running"
			server.HealthCondition = "healthy"
		} else {
			server.ObservedPower = "stopped"
			server.HealthCondition = "unknown"
		}
		server.ObservedGeneration = server.Generation
		server.ObservedAt = now
		server.UpdatedAt = now
		m.servers[serverID] = server
	}
	m.mu.Unlock()
}
