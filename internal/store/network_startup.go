package store

import (
	"encoding/json"
	"math"
	"net"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/id"
	gamedefinition "github.com/gugumanager/gugumanager/spec/game-definition"
)

type reconcileMutation func(server *domain.Server, now time.Time) error

func (m *Memory) Allocations(serverID string) ([]domain.Allocation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reconcileNodeLivenessLocked(time.Now().UTC())
	if _, ok := m.servers[serverID]; !ok {
		return nil, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}

	result := make([]domain.Allocation, 0, len(m.allocationOrder[serverID]))
	for _, allocationID := range m.allocationOrder[serverID] {
		allocation, ok := m.allocations[allocationID]
		if ok {
			result = append(result, allocation)
		}
	}
	return result, nil
}

func (m *Memory) CreateAllocation(
	serverID string,
	input domain.CreateAllocationInput,
	expectedGeneration int64,
	idempotencyKey string,
	actor domain.User,
) (domain.Operation, error) {
	normalized, err := normalizeAllocationInput(input)
	if err != nil {
		return domain.Operation{}, err
	}
	digest := requestDigest(struct {
		Generation int64                        `json:"generation"`
		Input      domain.CreateAllocationInput `json:"input"`
	}{Generation: expectedGeneration, Input: normalized})
	scope := idempotencyScope("server:allocation:create", actor.ID, serverID, idempotencyKey)

	return m.requestReconcile(serverID, expectedGeneration, idempotencyKey, scope, digest, actor, "servers.network.write", "network.allocation.create",
		func(server *domain.Server, now time.Time) error {
			for _, allocation := range m.allocations {
				if allocation.NodeID == server.NodeID &&
					allocation.BindIP == normalized.BindIP &&
					allocation.Port == normalized.Port &&
					allocation.Protocol == normalized.Protocol {
					return domain.NewProblem("PORT_CONFLICT", "节点上的监听地址已被占用", false)
				}
			}

			order := m.allocationOrder[serverID]
			makePrimary := normalized.Primary || len(order) == 0
			if makePrimary {
				for _, allocationID := range order {
					allocation := m.allocations[allocationID]
					allocation.Primary = false
					allocation.UpdatedAt = now
					m.allocations[allocationID] = allocation
				}
			}
			allocation := domain.Allocation{
				ID: id.New(), ServerID: serverID, NodeID: server.NodeID,
				BindIP: normalized.BindIP, Port: normalized.Port, Protocol: normalized.Protocol,
				PortRef: normalized.PortRef, ContainerPort: normalized.ContainerPort, Role: normalized.Role,
				Primary: makePrimary, CreatedAt: now, UpdatedAt: now,
			}
			m.allocations[allocation.ID] = allocation
			m.allocationOrder[serverID] = append(order, allocation.ID)
			if allocation.Primary {
				server.Allocation = allocationAddress(allocation)
			}
			return nil
		})
}

func (m *Memory) SetPrimaryAllocation(
	serverID string,
	allocationID string,
	expectedGeneration int64,
	idempotencyKey string,
	actor domain.User,
) (domain.Operation, error) {
	allocationID = strings.TrimSpace(allocationID)
	if allocationID == "" {
		return domain.Operation{}, domain.NewProblem("VALIDATION_FAILED", "allocationId 不能为空", false)
	}
	digest := requestDigest(struct {
		Generation   int64  `json:"generation"`
		AllocationID string `json:"allocationId"`
	}{Generation: expectedGeneration, AllocationID: allocationID})
	scope := idempotencyScope("server:allocation:primary", actor.ID, serverID+"\x00"+allocationID, idempotencyKey)

	return m.requestReconcile(serverID, expectedGeneration, idempotencyKey, scope, digest, actor, "servers.network.write", "network.allocation.primary",
		func(server *domain.Server, now time.Time) error {
			target, ok := m.allocations[allocationID]
			if !ok || target.ServerID != serverID {
				return domain.NewProblem("NOT_FOUND", "网络分配不存在", false)
			}
			for _, currentID := range m.allocationOrder[serverID] {
				allocation := m.allocations[currentID]
				allocation.Primary = currentID == allocationID
				allocation.UpdatedAt = now
				m.allocations[currentID] = allocation
			}
			target.Primary = true
			target.UpdatedAt = now
			m.allocations[allocationID] = target
			server.Allocation = allocationAddress(target)
			return nil
		})
}

func (m *Memory) DeleteAllocation(
	serverID string,
	allocationID string,
	expectedGeneration int64,
	idempotencyKey string,
	actor domain.User,
) (domain.Operation, error) {
	allocationID = strings.TrimSpace(allocationID)
	if allocationID == "" {
		return domain.Operation{}, domain.NewProblem("VALIDATION_FAILED", "allocationId 不能为空", false)
	}
	digest := requestDigest(struct {
		Generation   int64  `json:"generation"`
		AllocationID string `json:"allocationId"`
	}{Generation: expectedGeneration, AllocationID: allocationID})
	scope := idempotencyScope("server:allocation:delete", actor.ID, serverID+"\x00"+allocationID, idempotencyKey)

	return m.requestReconcile(serverID, expectedGeneration, idempotencyKey, scope, digest, actor, "servers.network.write", "network.allocation.delete",
		func(_ *domain.Server, _ time.Time) error {
			allocation, ok := m.allocations[allocationID]
			if !ok || allocation.ServerID != serverID {
				return domain.NewProblem("NOT_FOUND", "网络分配不存在", false)
			}
			order := m.allocationOrder[serverID]
			if len(order) <= 1 {
				return domain.NewProblem("OPERATION_CONFLICT", "不能删除服务器的最后一个网络分配", false)
			}
			if allocation.Primary {
				return domain.NewProblem("OPERATION_CONFLICT", "不能删除主网络分配，请先切换主分配", false)
			}
			filtered := make([]string, 0, len(order)-1)
			for _, currentID := range order {
				if currentID != allocationID {
					filtered = append(filtered, currentID)
				}
			}
			delete(m.allocations, allocationID)
			m.allocationOrder[serverID] = filtered
			return nil
		})
}

func (m *Memory) Startup(serverID string) (domain.Startup, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reconcileNodeLivenessLocked(time.Now().UTC())
	server, ok := m.servers[serverID]
	if !ok {
		return domain.Startup{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	startup, err := m.startupTemplateForServerLocked(server)
	if err != nil {
		return domain.Startup{}, err
	}
	values := m.startupValues[serverID]
	result := domain.Startup{
		ServerID: serverID, Generation: server.Generation,
		Command:   resolveStartupCommand(startup, values),
		Variables: make([]domain.StartupVariable, 0, len(startup.Variables)),
		Bindings:  append([]domain.StartupBinding(nil), startup.Bindings...),
	}
	for _, definition := range startup.Variables {
		value, configured := values[definition.Key]
		result.Variables = append(result.Variables, startupVariablePublicView(definition, value, configured))
	}
	return result, nil
}

func startupVariablePublicView(definition domain.StartupVariable, value any, configured bool) domain.StartupVariable {
	variable := definition
	variable.HasValue = configured
	variable.EnumValues = append([]string(nil), definition.EnumValues...)
	if definition.Secret {
		variable.Value = nil
		variable.Default = nil
		variable.ConstValue = nil
		variable.EnumValues = nil
	} else if configured {
		variable.Value = value
	} else {
		variable.Value = nil
	}
	return variable
}

func (m *Memory) UpdateStartup(
	serverID string,
	updates map[string]any,
	expectedGeneration int64,
	idempotencyKey string,
	actor domain.User,
) (domain.Operation, error) {
	if len(updates) == 0 {
		return domain.Operation{}, domain.NewProblem("VALIDATION_FAILED", "variables 至少需要包含一个启动变量", false)
	}
	digest := keyedRequestDigest(m.requestDigestKey, struct {
		Generation int64          `json:"generation"`
		Variables  map[string]any `json:"variables"`
	}{Generation: expectedGeneration, Variables: canonicalStartupDigestVariables(updates)})
	scope := idempotencyScope("server:startup:update", actor.ID, serverID, idempotencyKey)

	return m.requestReconcile(serverID, expectedGeneration, idempotencyKey, scope, digest, actor, "servers.startup.write", "server.startup.update",
		func(server *domain.Server, _ time.Time) error {
			startup, err := m.startupTemplateForServerLocked(*server)
			if err != nil {
				return err
			}
			definitions := make(map[string]domain.StartupVariable, len(startup.Variables))
			for _, variable := range startup.Variables {
				definitions[variable.Key] = variable
			}

			normalized := make(map[string]any, len(updates))
			cleared := make(map[string]bool, len(updates))
			for key, value := range updates {
				definition, declared := definitions[key]
				if !declared {
					return validationProblem("未声明的启动变量: " + key)
				}
				if value == nil {
					if definition.Required {
						return validationProblem("必填启动变量不能清除: " + key)
					}
					cleared[key] = true
					continue
				}
				normalizedValue, err := normalizeStartupValue(definition, value)
				if err != nil {
					return err
				}
				normalized[key] = normalizedValue
			}

			values := m.startupValues[serverID]
			if values == nil {
				values = map[string]any{}
			}
			for key := range cleared {
				delete(values, key)
			}
			for key, value := range normalized {
				values[key] = value
			}
			m.startupValues[serverID] = values
			return nil
		})
}

func (m *Memory) requestReconcile(
	serverID string,
	expectedGeneration int64,
	idempotencyKey string,
	scope string,
	digest [32]byte,
	actor domain.User,
	permission string,
	auditAction string,
	mutate reconcileMutation,
) (domain.Operation, error) {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return domain.Operation{}, err
	}

	m.mu.Lock()
	now := time.Now().UTC()
	currentActor, authErr := m.authorizeServerLocked(actor.ID, serverID, permission)
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
	node, ok := m.nodes[server.NodeID]
	if !ok || node.Condition != "available" {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("NODE_OFFLINE", "节点当前离线，无法接收重新对账任务", true)
	}
	if !containsString(node.Capabilities, domain.NodeCapabilityServerReconcile) {
		m.mu.Unlock()
		return domain.Operation{}, capabilityUnsupported(node, domain.NodeCapabilityServerReconcile)
	}
	if server.Generation != expectedGeneration {
		problem := domain.NewProblem("PRECONDITION_FAILED", "服务器 generation 已变化，请刷新后重试", false)
		problem.Details["currentGeneration"] = server.Generation
		problem.Details["providedGeneration"] = expectedGeneration
		m.mu.Unlock()
		return domain.Operation{}, problem
	}
	if active, ok := m.activeExclusiveOperationLocked(serverID); ok {
		m.mu.Unlock()
		return domain.Operation{}, operationInProgress(active)
	}
	if server.LifecycleState != "ready" {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("OPERATION_IN_PROGRESS", "服务器仍在完成生命周期操作", true)
	}
	if err := mutate(&server, now); err != nil {
		m.mu.Unlock()
		return domain.Operation{}, err
	}

	server.Generation++
	server.NodeCondition = node.Condition
	server.UpdatedAt = now
	operation := domain.NewQueuedOperation(id.New(), serverID, node.ID, domain.PowerAction("reconcile"), server.Generation, idempotencyKey, now)
	m.servers[serverID] = server
	m.operations[operation.ID] = operation
	m.idempotency[scope] = idempotencyRecord{OperationID: operation.ID, RequestDigest: digest}
	m.mu.Unlock()

	m.recordAudit(currentActor.DisplayName, auditAction, "server", server.Name, "accepted", operation.ID)
	go m.finishReconcile(operation.ID, serverID, currentActor.DisplayName, auditAction)
	return operation, nil
}

func capabilityUnsupported(node domain.Node, required string) *domain.Problem {
	problem := domain.NewProblem("CAPABILITY_UNSUPPORTED", "目标节点未声明执行该操作所需的能力", false)
	problem.Details["nodeId"] = node.ID
	problem.Details["nodeVersion"] = node.Version
	requiredCapability, requiredVersion, ok := domain.SplitNodeCapability(required)
	if ok {
		problem.Details["requiredCapability"] = requiredCapability
		problem.Details["requiredVersion"] = requiredVersion
	} else {
		problem.Details["requiredCapability"] = required
	}
	problem.Details["declaredCapabilities"] = append([]string(nil), node.Capabilities...)
	return problem
}

func (m *Memory) finishReconcile(operationID string, serverID string, actorName string, auditAction string) {
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
	succeeded := finished && operation.Status == "succeeded"
	if succeeded {
		server.ObservedGeneration = operation.Generation
		server.ObservedAt = now
		server.UpdatedAt = now
		m.servers[serverID] = server
	}
	if finished {
		result := "failure"
		if succeeded {
			result = "success"
		}
		targetName := serverID
		if serverOK {
			targetName = server.Name
		}
		m.recordAuditLocked(actorName, auditAction, "server", targetName, result, operationID)
	}
	m.mu.Unlock()
}

func normalizeAllocationInput(input domain.CreateAllocationInput) (domain.CreateAllocationInput, error) {
	parsed := net.ParseIP(strings.TrimSpace(input.BindIP))
	if parsed == nil {
		return domain.CreateAllocationInput{}, validationProblem("bindIp 必须是有效的 IPv4 或 IPv6 地址")
	}
	if parsed.IsUnspecified() {
		return domain.CreateAllocationInput{}, validationProblem("bindIp 不能使用通配地址")
	}
	if ipv4 := parsed.To4(); ipv4 != nil {
		input.BindIP = ipv4.String()
	} else {
		input.BindIP = parsed.String()
	}
	if input.Port < 1 || input.Port > 65535 {
		return domain.CreateAllocationInput{}, validationProblem("port 必须在 1 到 65535 之间")
	}
	input.Protocol = strings.ToLower(strings.TrimSpace(input.Protocol))
	if input.Protocol != "tcp" && input.Protocol != "udp" {
		return domain.CreateAllocationInput{}, validationProblem("protocol 必须是 tcp 或 udp")
	}
	if input.ContainerPort == 0 {
		input.ContainerPort = input.Port
	}
	if input.ContainerPort < 1 || input.ContainerPort > 65535 {
		return domain.CreateAllocationInput{}, validationProblem("containerPort 必须在 1 到 65535 之间")
	}
	input.PortRef = strings.TrimSpace(input.PortRef)
	input.Role = strings.ToLower(strings.TrimSpace(input.Role))
	if input.Role == "" {
		if input.Primary {
			input.Role = "primary"
		} else {
			input.Role = "additional"
		}
	}
	switch input.Role {
	case "primary", "query", "rcon", "additional":
	default:
		return domain.CreateAllocationInput{}, validationProblem("role 必须是 primary、query、rcon 或 additional")
	}
	return input, nil
}

func allocationAddress(allocation domain.Allocation) string {
	return net.JoinHostPort(allocation.BindIP, strconv.Itoa(allocation.Port))
}

func validationProblem(message string) *domain.Problem {
	return domain.NewProblem("VALIDATION_FAILED", message, false)
}

func normalizeStartupValue(definition domain.StartupVariable, value any) (any, error) {
	var normalized any
	switch definition.Type {
	case "string":
		text, ok := value.(string)
		if !ok {
			return nil, validationProblem(definition.Key + " 必须是字符串")
		}
		length := len([]rune(text))
		if definition.MinLength != nil && length < *definition.MinLength {
			return nil, validationProblem(definition.Key + " 长度过短")
		}
		if definition.MaxLength != nil && length > *definition.MaxLength {
			return nil, validationProblem(definition.Key + " 长度过长")
		}
		if len(definition.EnumValues) > 0 && !containsString(definition.EnumValues, text) {
			return nil, validationProblem(definition.Key + " 不在允许的枚举值中")
		}
		normalized = text
	case "integer":
		integer, ok := startupInteger(value)
		if !ok {
			return nil, validationProblem(definition.Key + " 必须是整数")
		}
		if integer < -gamedefinition.MaxSafeStartupInteger || integer > gamedefinition.MaxSafeStartupInteger {
			return nil, validationProblem(definition.Key + " must be a JavaScript-safe integer")
		}
		if definition.Minimum != nil && integer < *definition.Minimum {
			return nil, validationProblem(definition.Key + " 小于允许的最小值")
		}
		if definition.Maximum != nil && integer > *definition.Maximum {
			return nil, validationProblem(definition.Key + " 大于允许的最大值")
		}
		normalized = integer
	case "boolean":
		boolean, ok := value.(bool)
		if !ok {
			return nil, validationProblem(definition.Key + " 必须是布尔值")
		}
		normalized = boolean
	default:
		return nil, validationProblem("不支持的启动变量类型: " + definition.Type)
	}
	if definition.ConstValue != nil && !reflect.DeepEqual(normalized, definition.ConstValue) {
		return nil, validationProblem(definition.Key + " 必须等于固定值")
	}
	return normalized, nil
}

func startupInteger(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int8:
		return int64(typed), true
	case int16:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return 0, false
		}
		return int64(typed), true
	case uint8:
		return int64(typed), true
	case uint16:
		return int64(typed), true
	case uint32:
		return int64(typed), true
	case uint64:
		if typed > math.MaxInt64 {
			return 0, false
		}
		return int64(typed), true
	case float32:
		return startupFloatInteger(float64(typed))
	case float64:
		return startupFloatInteger(typed)
	case json.Number:
		return gamedefinition.ParseStartupInteger(typed)
	default:
		return 0, false
	}
}

func canonicalStartupDigestVariables(updates map[string]any) map[string]any {
	canonical := make(map[string]any, len(updates))
	for key, value := range updates {
		number, ok := value.(json.Number)
		if ok {
			integer, exact := gamedefinition.ParseStartupInteger(number)
			if exact && integer >= -gamedefinition.MaxSafeStartupInteger && integer <= gamedefinition.MaxSafeStartupInteger {
				canonical[key] = integer
				continue
			}
		}
		canonical[key] = value
	}
	return canonical
}

func startupFloatInteger(value float64) (int64, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value {
		return 0, false
	}
	const integerLimit = float64(1 << 63)
	if value < -integerLimit || value >= integerLimit {
		return 0, false
	}
	return int64(value), true
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (m *Memory) startupTemplateForServerLocked(server domain.Server) (domain.Startup, error) {
	game, ok := m.games[server.GameID]
	if !ok {
		return domain.Startup{}, packageIncompatible("server references an unknown fixed Bundle")
	}
	fixed, _, err := startupFromFixedBundle(server, game, nil)
	if err != nil {
		return domain.Startup{}, err
	}
	cached, ok := m.startups[server.ID]
	if !ok || !startupTemplatesEqual(cached, fixed) {
		return domain.Startup{}, packageIncompatible("materialized Startup declaration differs from its fixed Bundle")
	}
	return fixed, nil
}

func resolveStartupCommand(startup domain.Startup, values map[string]any) domain.StartupCommand {
	result := domain.StartupCommand{Executable: startup.Command.Executable, Args: append([]string(nil), startup.Command.Args...)}
	definitions := make(map[string]domain.StartupVariable, len(startup.Variables))
	for _, variable := range startup.Variables {
		definitions[variable.Key] = variable
	}
	for _, binding := range startup.Bindings {
		definition, declared := definitions[binding.Variable]
		if !declared || definition.Secret || binding.Target != "argument" {
			continue
		}
		value, configured := values[binding.Variable]
		renderedValue, supported := startupTemplateValue(value)
		if !configured || !supported {
			continue
		}
		placeholder := startupArgumentPlaceholder(binding.Variable)
		for index, argument := range result.Args {
			if argument == placeholder {
				result.Args[index] = strings.ReplaceAll(binding.Template, "{{ value }}", renderedValue)
			}
		}
	}
	return result
}

func startupTemplateValue(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case int64:
		return strconv.FormatInt(typed, 10), true
	case bool:
		return strconv.FormatBool(typed), true
	default:
		return "", false
	}
}
