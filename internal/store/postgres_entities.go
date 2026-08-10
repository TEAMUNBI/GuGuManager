package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/id"
	"github.com/lib/pq"
)

// RegisterNode persists a node and its capabilities, returning the node id.
// The node name is unique; a conflicting name maps to NODE_NAME_CONFLICT.
func (s *Postgres) RegisterNode(ctx context.Context, node domain.Node) (string, error) {
	name := strings.TrimSpace(node.Name)
	if name == "" {
		return "", domain.NewProblem("VALIDATION_FAILED", "节点名称不能为空", false)
	}
	if node.CPUCores <= 0 || node.MemoryBytes <= 0 || node.DiskBytes <= 0 {
		return "", domain.NewProblem("VALIDATION_FAILED", "节点资源规格无效", false)
	}
	condition := strings.TrimSpace(node.Condition)
	if condition == "" {
		condition = "available"
	}
	region := strings.TrimSpace(node.Region)
	if region == "" {
		region = "unknown"
	}
	agentVersion := strings.TrimSpace(node.Version)
	if agentVersion == "" {
		agentVersion = "unknown"
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	var address sql.NullString
	if node.Address != "" {
		address = sql.NullString{String: node.Address, Valid: true}
	}
	var lastHeartbeat sql.NullTime
	if !node.LastHeartbeatAt.IsZero() {
		lastHeartbeat = sql.NullTime{Time: node.LastHeartbeatAt, Valid: true}
	}

	var nodeID string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO nodes (
			name, agent_version, protocol_version, condition, region, address,
			cpu_cores, memory_bytes, disk_bytes, last_heartbeat_at, created_at, updated_at
		)
		VALUES ($1, $2, 'v1', $3, $4, $5, $6, $7, $8, $9, now(), now())
		RETURNING id::text
	`, name, agentVersion, condition, region, address, node.CPUCores, node.MemoryBytes, node.DiskBytes, lastHeartbeat).Scan(&nodeID)
	if err != nil {
		if isUniqueViolation(err) {
			return "", domain.NewProblem("NODE_NAME_CONFLICT", "节点名称已被占用", false)
		}
		return "", domain.NewProblem("INTERNAL_ERROR", "无法注册节点", true)
	}

	for _, capability := range node.Capabilities {
		capability = strings.TrimSpace(capability)
		if capability == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO node_capabilities (node_id, capability_key, capability_version)
			VALUES ($1, $2, '1')
			ON CONFLICT (node_id, capability_key) DO NOTHING
		`, nodeID, capability); err != nil {
			return "", domain.NewProblem("INTERNAL_ERROR", "无法保存节点能力", true)
		}
	}

	if err := tx.Commit(); err != nil {
		return "", domain.NewProblem("INTERNAL_ERROR", "无法提交事务", true)
	}
	return nodeID, nil
}

// NodeByID retrieves a single non-revoked node with its capabilities.
func (s *Postgres) NodeByID(ctx context.Context, nodeID string) (domain.Node, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var node domain.Node
	var address sql.NullString
	var lastHeartbeat sql.NullTime
	var capabilities []string
	err := s.db.QueryRowContext(ctx, `
		SELECT n.id::text, n.name, n.condition, n.agent_version, n.region,
		       host(n.address), n.cpu_cores, n.memory_bytes, n.disk_bytes, n.last_heartbeat_at,
		       COALESCE(array_agg(nc.capability_key) FILTER (WHERE nc.capability_key IS NOT NULL), '{}') AS capabilities
		FROM nodes n
		LEFT JOIN node_capabilities nc ON nc.node_id = n.id
		WHERE n.id = $1 AND n.revoked_at IS NULL
		GROUP BY n.id
	`, nodeID).Scan(&node.ID, &node.Name, &node.Condition, &node.Version, &node.Region,
		&address, &node.CPUCores, &node.MemoryBytes, &node.DiskBytes, &lastHeartbeat,
		pq.Array(&capabilities))
	if err == sql.ErrNoRows {
		return domain.Node{}, domain.NewProblem("NOT_FOUND", "节点不存在", false)
	}
	if err != nil {
		return domain.Node{}, domain.NewProblem("INTERNAL_ERROR", "无法查询节点", true)
	}
	if address.Valid {
		node.Address = address.String
	}
	if lastHeartbeat.Valid {
		node.LastHeartbeatAt = lastHeartbeat.Time
	}
	node.Capabilities = capabilities
	return node, nil
}

// Nodes returns all non-revoked nodes with liveness and capacity aggregates.
// It satisfies the ControlPlane interface and therefore has no ctx.
func (s *Postgres) Nodes() []domain.Node {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `
		SELECT n.id::text, n.name, n.condition, n.agent_version, n.region,
		       host(n.address), n.cpu_cores, n.memory_bytes, n.disk_bytes, n.last_heartbeat_at,
		       COALESCE(SUM(CASE WHEN sv.observed_power = 'running' THEN 1 ELSE 0 END), 0) AS running_servers,
		       COUNT(sv.id) AS total_servers,
		       COALESCE(SUM(sv.memory_limit_bytes), 0) AS allocated_memory,
		       COALESCE(SUM(sv.disk_limit_bytes), 0) AS allocated_disk,
		       COALESCE(array_agg(nc.capability_key) FILTER (WHERE nc.capability_key IS NOT NULL), '{}') AS capabilities
		FROM nodes n
		LEFT JOIN servers sv ON sv.node_id = n.id AND sv.deleted_at IS NULL
		LEFT JOIN node_capabilities nc ON nc.node_id = n.id
		WHERE n.revoked_at IS NULL
		GROUP BY n.id
		ORDER BY n.created_at ASC
	`)
	if err != nil {
		return []domain.Node{}
	}
	defer rows.Close()

	nodes := []domain.Node{}
	for rows.Next() {
		var node domain.Node
		var address sql.NullString
		var lastHeartbeat sql.NullTime
		var capabilities []string
		if err := rows.Scan(&node.ID, &node.Name, &node.Condition, &node.Version, &node.Region,
			&address, &node.CPUCores, &node.MemoryBytes, &node.DiskBytes, &lastHeartbeat,
			&node.RunningServers, &node.TotalServers, &node.AllocatedMemoryBytes, &node.AllocatedDiskBytes,
			pq.Array(&capabilities)); err != nil {
			continue
		}
		if address.Valid {
			node.Address = address.String
		}
		if lastHeartbeat.Valid {
			node.LastHeartbeatAt = lastHeartbeat.Time
		}
		node.Capabilities = capabilities
		nodes = append(nodes, node)
	}
	return nodes
}

// serverSelect joins servers with their node, game definition, bundle, owner
// and primary allocation. The allocation LATERAL picks the active primary
// allocation (falling back to the oldest one when none is marked primary).
const serverSelect = `
	SELECT s.id::text, s.name, s.description, s.game_version,
	       gd.id, gd.name, gb.digest, gb.definition_version,
	       s.node_id::text, n.name, s.lifecycle_state, s.desired_power, s.observed_power,
	       n.condition, s.health_condition, s.generation, s.observed_generation, s.observed_at,
	       a.bind_ip, a.port, u.display_name,
	       s.memory_limit_bytes, s.disk_limit_bytes, s.updated_at
	FROM servers s
	JOIN nodes n ON n.id = s.node_id
	JOIN game_bundles gb ON gb.id = s.game_bundle_id
	JOIN game_definitions gd ON gd.id = gb.game_definition_id
	JOIN users u ON u.id = s.owner_id
	LEFT JOIN LATERAL (
		SELECT host(bind_ip) AS bind_ip, port
		FROM allocations
		WHERE server_id = s.id AND released_at IS NULL
		ORDER BY is_primary DESC, created_at ASC
		LIMIT 1
	) a ON true`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanServer(scanner rowScanner) (domain.Server, error) {
	var server domain.Server
	var bindIP sql.NullString
	var port sql.NullInt64
	err := scanner.Scan(
		&server.ID, &server.Name, &server.Description, &server.GameVersion,
		&server.GameID, &server.GameName, &server.GameBundleDigest, &server.GameDefinitionVersion,
		&server.NodeID, &server.NodeName, &server.LifecycleState, &server.DesiredPower, &server.ObservedPower,
		&server.NodeCondition, &server.HealthCondition, &server.Generation, &server.ObservedGeneration, &server.ObservedAt,
		&bindIP, &port, &server.OwnerName,
		&server.Metrics.MemoryLimit, &server.Metrics.DiskLimit, &server.UpdatedAt,
	)
	if err != nil {
		return domain.Server{}, err
	}
	if bindIP.Valid && port.Valid {
		server.Allocation = net.JoinHostPort(bindIP.String, strconv.Itoa(int(port.Int64)))
	}
	return server, nil
}

// Server retrieves one non-deleted server by id.
func (s *Postgres) Server(serverID string) (domain.Server, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	server, err := scanServer(s.db.QueryRowContext(ctx, serverSelect+` WHERE s.id = $1 AND s.deleted_at IS NULL`, serverID))
	if err == sql.ErrNoRows {
		return domain.Server{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	if err != nil {
		return domain.Server{}, domain.NewProblem("INTERNAL_ERROR", "无法查询服务器", true)
	}
	s.appendMetricsToServer(&server)
	return server, nil
}

// Servers returns all non-deleted servers, optionally filtered by name.
// It satisfies the ControlPlane interface and therefore has no ctx.
func (s *Postgres) Servers(query string) []domain.Server {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	query = strings.TrimSpace(query)
	sqlText := serverSelect + ` WHERE s.deleted_at IS NULL`
	var args []any
	if query != "" {
		sqlText += ` AND s.name ILIKE '%' || $1 || '%'`
		args = append(args, query)
	}
	sqlText += ` ORDER BY s.created_at ASC`

	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return []domain.Server{}
	}
	defer rows.Close()

	servers := []domain.Server{}
	for rows.Next() {
		server, err := scanServer(rows)
		if err != nil {
			continue
		}
		s.appendMetricsToServer(&server)
		servers = append(servers, server)
	}
	return servers
}

// CreateServer validates the node and approved game bundle, provisions a
// servers row with its primary allocation, and enqueues a 'provision' task.
// The returned operation is queued; the node agent picks it up via ClaimTask.
// It satisfies the ControlPlane interface and therefore has no ctx.
func (s *Postgres) CreateServer(input domain.CreateServerInput, idempotencyKey string, actor domain.User) (domain.Operation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

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
	scope := taskIdempotencyScope("provision", actor.ID, idempotencyKey)

	// Idempotency replay: reuse the recorded task when the request digest
	// matches; reject the key when it was already used for a different body.
	if existing, ok, err := s.lookupIdempotentTask(ctx, scope, idempotencyKey, digest); err != nil || ok {
		return existing, err
	}

	// The node must exist, not be revoked, and be able to accept work.
	var nodeCondition, nodeAddress string
	var nodeMemory, nodeDisk int64
	err := s.db.QueryRowContext(ctx, `
		SELECT condition, COALESCE(host(address), ''), memory_bytes, disk_bytes
		FROM nodes WHERE id = $1 AND revoked_at IS NULL
	`, input.NodeID).Scan(&nodeCondition, &nodeAddress, &nodeMemory, &nodeDisk)
	if err == sql.ErrNoRows {
		return domain.Operation{}, domain.NewProblem("VALIDATION_FAILED", "节点或游戏包不存在", false)
	}
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法查询节点", true)
	}
	if nodeCondition != "available" {
		return domain.Operation{}, domain.NewProblem("NODE_OFFLINE", "节点当前不可接收新任务", true)
	}

	// The game definition must be approved and the requested bundle digest
	// must resolve to a published bundle of that definition.
	var gameName, reviewStatus, bundleID, bundleGameVersion, bundleDefinitionVersion string
	err = s.db.QueryRowContext(ctx, `
		SELECT gd.name, gd.review_status, gb.id::text, gb.game_version, gb.definition_version
		FROM game_definitions gd
		JOIN game_bundles gb ON gb.game_definition_id = gd.id
		WHERE gd.id = $1 AND gb.digest = $2
	`, input.GameDefinitionID, input.GameBundleDigest).Scan(&gameName, &reviewStatus, &bundleID, &bundleGameVersion, &bundleDefinitionVersion)
	if err == sql.ErrNoRows {
		return domain.Operation{}, domain.NewProblem("VALIDATION_FAILED", "节点或游戏包不存在", false)
	}
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法查询游戏包", true)
	}
	if reviewStatus != "approved" {
		return domain.Operation{}, domain.NewProblem("GAME_DEFINITION_NOT_APPROVED", "游戏包尚未通过审核", false)
	}

	memoryBytes := int64(input.MemoryMB) * 1024 * 1024
	diskBytes := int64(input.DiskGB) * 1024 * 1024 * 1024

	// Respect the node's remaining capacity.
	var allocatedMemory, allocatedDisk int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(memory_limit_bytes), 0), COALESCE(SUM(disk_limit_bytes), 0)
		FROM servers WHERE node_id = $1 AND deleted_at IS NULL
	`, input.NodeID).Scan(&allocatedMemory, &allocatedDisk); err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法统计节点资源占用", true)
	}
	if memoryBytes > nodeMemory-allocatedMemory || diskBytes > nodeDisk-allocatedDisk {
		return domain.Operation{}, domain.NewProblem("INSUFFICIENT_RESOURCE", "节点剩余资源不足", false)
	}

	// Reserve a free primary port on the node.
	protocol, firstPort := defaultAllocationSettings(input.GameDefinitionID)
	port, ok := s.nextAvailablePort(ctx, input.NodeID, protocol, firstPort)
	if !ok {
		return domain.Operation{}, domain.NewProblem("PORT_CONFLICT", "节点没有可用的游戏端口", false)
	}
	bindIP := nodeAddress
	if bindIP == "" {
		bindIP = "0.0.0.0"
	}

	now := time.Now().UTC()
	serverID := id.New()
	operationID := id.New()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO servers (
			id, owner_id, node_id, game_bundle_id, game_version, name, description,
			lifecycle_state, desired_power, observed_power, node_condition, health_condition,
			generation, observed_generation, memory_limit_bytes, disk_limit_bytes, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, '', 'provisioning', 'stopped', 'unknown', $7, 'unknown', 1, 0, $8, $9, $10, $10)
	`, serverID, actor.ID, input.NodeID, bundleID, bundleGameVersion, input.Name, nodeCondition, memoryBytes, diskBytes, now)
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法创建服务器", true)
	}

	var allocationID string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO allocations (id, node_id, bind_ip, port, protocol, server_id, is_primary, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, true, $7)
		RETURNING id::text
	`, id.New(), input.NodeID, bindIP, port, protocol, serverID, now).Scan(&allocationID)
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法分配游戏端口", true)
	}

	// Materialize the Startup declaration defaults so required variables are
	// configured before the first start/restart, matching the in-memory
	// adapter. Bundles unknown to the fixed catalog keep the legacy behavior:
	// no startup_values row is written and Startup() reports them as
	// PACKAGE_INCOMPATIBLE.
	var startupValues map[string]any
	var startupDefinitions []domain.StartupVariable
	if game, catalogErr := fixedCatalogGame(input.GameDefinitionID); catalogErr == nil {
		serverForStartup := domain.Server{
			ID: serverID, GameID: input.GameDefinitionID, GameBundleDigest: input.GameBundleDigest,
			GameDefinitionVersion: bundleDefinitionVersion, NodeID: input.NodeID, Generation: 1,
		}
		startup, values, startupErr := startupFromFixedBundle(serverForStartup, game, nil)
		if startupErr != nil {
			return domain.Operation{}, startupErr
		}
		for _, variable := range startup.Variables {
			if variable.Key != "memory_mb" {
				continue
			}
			if err := applyStartupOverrides(startup.Variables, values, map[string]any{"memory_mb": int64(input.MemoryMB)}); err != nil {
				return domain.Operation{}, err
			}
			break
		}
		startupValues = values
		startupDefinitions = startup.Variables
		storedValues, storageErr := s.startupValuesForStorage(startup.Variables, values)
		if storageErr != nil {
			return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法加密启动变量", true)
		}
		encoded, marshalErr := json.Marshal(storedValues)
		if marshalErr != nil {
			return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法保存启动变量", true)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO startup_values (server_id, values, updated_at)
			VALUES ($1, $2::jsonb, now())
			ON CONFLICT (server_id) DO UPDATE
			SET values = EXCLUDED.values, updated_at = now()
		`, serverID, string(encoded)); err != nil {
			return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法保存启动变量", true)
		}
	}

	// The agent claims queued tasks via checkpoint::text as its payload, so
	// the provision payload must be materialized here. Previously it was left
	// empty and every provision failed on the agent with PROVISION_FAILED.
	payloadJSON, marshalErr := json.Marshal(provisionTaskPayload{
		GameDefinitionID:    input.GameDefinitionID,
		ResourceLimits:      provisionResourceLimits{MemoryBytes: uint64(memoryBytes), DiskBytes: uint64(diskBytes)},
		Allocations:         []provisionTaskAllocation{{AllocationID: allocationID, BindIP: bindIP, HostPort: uint32(port), ContainerPort: uint32(port), Protocol: provisionProtocolName(protocol)}},
		Variables:           stringifiedNonSecretStartupValues(startupDefinitions, startupValues),
		SecretKeys:          secretStartupKeys(startupDefinitions),
		StartAfterProvision: false,
	})
	if marshalErr != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法生成任务负载", true)
	}

	// The task id doubles as the operation id returned to the control plane.
	// The UNIQUE (idempotency_scope, idempotency_key) constraint backs the
	// replay path for concurrent duplicates.
	var taskID string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO server_tasks (
			id, server_id, node_id, task_type, status, generation, actor_id,
			idempotency_scope, idempotency_key, request_digest, checkpoint, attempt, max_attempts, created_at, updated_at
		)
		VALUES ($1, $2, $3, 'provision', 'queued', $4, $5, $6, $7, $8, $9, 0, 3, $10, $10)
		ON CONFLICT (idempotency_scope, idempotency_key) DO NOTHING
		RETURNING id::text
	`, operationID, serverID, input.NodeID, 1, actor.ID, scope, idempotencyKey, digest[:], string(payloadJSON), now).Scan(&taskID)
	if err == sql.ErrNoRows {
		// A concurrent request already recorded this key: roll back our
		// writes and return the recorded operation.
		_ = tx.Rollback()
		existing, ok, lookupErr := s.lookupIdempotentTask(ctx, scope, idempotencyKey, digest)
		if lookupErr != nil || ok {
			return existing, lookupErr
		}
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "幂等记录缺失", true)
	}
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法创建服务器任务", true)
	}

	// 服务器创建（provision 入队）与 outbox 事件同事务提交。
	if err := s.recordTaskOutboxEvent(ctx, tx, "task.created", taskEventPayload{
		OperationID: taskID,
		ServerID:    serverID,
		NodeID:      input.NodeID,
		TaskType:    "provision",
		Generation:  1,
		Attempt:     0,
		MaxAttempts: 3,
		Status:      "queued",
	}); err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法记录任务事件", true)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO audit_events (actor_id, actor_type, action, target_type, target_id, result, operation_id, trace_id, created_at)
		VALUES ($1, 'user', 'server.create', 'server', $2, 'accepted', $3, $4, $5)
	`, actor.ID, serverID, operationID, id.New(), now)
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法记录审计日志", true)
	}

	if err := tx.Commit(); err != nil {
		if isUniqueViolation(err) {
			existing, ok, lookupErr := s.lookupIdempotentTask(ctx, scope, idempotencyKey, digest)
			if lookupErr != nil || ok {
				return existing, lookupErr
			}
			return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "幂等记录缺失", true)
		}
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法提交事务", true)
	}

	operation := domain.NewQueuedOperation(operationID, serverID, input.NodeID, domain.PowerAction("provision"), 1, idempotencyKey, now)
	operation.MaxAttempts = 3
	return operation, nil
}

// UpdateServerObserved applies an agent-reported server state snapshot.
// Runtime metadata (RuntimeID/BundleDigest/Detail) has no dedicated columns
// in the current schema and is intentionally not persisted.
func (s *Postgres) UpdateServerObserved(ctx context.Context, obs domain.ServerObserved) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	observedAt := obs.ObservedAt
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE servers
		SET observed_power = $2, health_condition = $3, observed_generation = $4,
		    observed_at = $5, updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL
	`, obs.ServerID, obs.ObservedPower, obs.HealthCondition, obs.ObservedGeneration, observedAt)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法更新服务器观测状态", true)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	return nil
}

// ApplyServerObserved 是 Agent 上报路径使用的别名，复用 UpdateServerObserved。
func (s *Postgres) ApplyServerObserved(ctx context.Context, obs domain.ServerObserved) error {
	return s.UpdateServerObserved(ctx, obs)
}

// RecordAgentHeartbeat 记录节点心跳：刷新 last_heartbeat_at 并用上报值覆盖
// 资源与版本字段（占位值在首次 Enroll 时写入，真实值由此落地），offline 节点
// 收到心跳后恢复 available。维护模式不受影响。
func (s *Postgres) RecordAgentHeartbeat(ctx context.Context, nodeID string, hb domain.Heartbeat) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	observedAt := hb.ObservedAt
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE nodes
		SET last_heartbeat_at = $2,
		    memory_bytes = CASE WHEN $3::bigint > 0 THEN $3::bigint ELSE memory_bytes END,
		    disk_bytes = CASE WHEN $4::bigint > 0 THEN $4::bigint ELSE disk_bytes END,
		    agent_version = CASE WHEN $5 <> '' THEN $5 ELSE agent_version END,
		    condition = CASE WHEN condition = 'offline' THEN 'available' ELSE condition END,
		    updated_at = now()
		WHERE id = $1 AND revoked_at IS NULL
	`, nodeID, observedAt, hb.MemoryTotalBytes, hb.DiskTotalBytes, hb.AgentVersion)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法记录节点心跳", true)
	}
	return nil
}

// RecordAudit persists an audit event emitted by the agent control plane.
func (s *Postgres) RecordAudit(ctx context.Context, event domain.AuditEvent) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	createdAt := event.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	var operationID any
	if event.OperationID != "" {
		operationID = event.OperationID
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_events (actor_type, action, target_type, target_id, result, operation_id, trace_id, created_at)
		VALUES ('agent', $1, $2, $3, $4, $5, $6, $7)
	`, event.Action, event.TargetType, event.TargetName, event.Result, operationID, id.New(), createdAt)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法记录审计日志", true)
	}
	return nil
}

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23505"
}

// nextAvailablePort finds the lowest free port at or above firstPort on a node.
func (s *Postgres) nextAvailablePort(ctx context.Context, nodeID string, protocol string, firstPort int) (int, bool) {
	used := map[int]struct{}{}
	rows, err := s.db.QueryContext(ctx, `
		SELECT port FROM allocations
		WHERE node_id = $1 AND protocol = $2 AND released_at IS NULL
	`, nodeID, protocol)
	if err != nil {
		return 0, false
	}
	defer rows.Close()
	for rows.Next() {
		var port int
		if err := rows.Scan(&port); err != nil {
			return 0, false
		}
		used[port] = struct{}{}
	}
	for port := firstPort; port <= 65535; port++ {
		if _, taken := used[port]; !taken {
			return port, true
		}
	}
	return 0, false
}

// provisionTaskPayload 是下发给 Agent 的 provision 任务负载（JSON 形状与
// agentv1.ProvisionTaskPayload 的 protojson 字段名对齐，Agent 端 protojson
// 反序列化后执行 Docker provision）。该负载在 CreateServer 时落库到
// server_tasks.checkpoint，ClaimTask 将其作为 PayloadJSON 下发给 Agent。
type provisionTaskPayload struct {
	GameDefinitionID    string                    `json:"gameDefinitionId"`
	ResourceLimits      provisionResourceLimits   `json:"resourceLimits,omitempty"`
	Allocations         []provisionTaskAllocation `json:"allocations,omitempty"`
	Variables           map[string]string         `json:"variables,omitempty"`
	SecretKeys          []string                  `json:"secretKeys,omitempty"`
	StartAfterProvision bool                      `json:"startAfterProvision"`
}

type provisionResourceLimits struct {
	MemoryBytes   uint64 `json:"memoryBytes,omitempty"`
	DiskBytes     uint64 `json:"diskBytes,omitempty"`
	CPUMillicores uint32 `json:"cpuMillicores,omitempty"`
	PIDs          uint32 `json:"pids,omitempty"`
}

type provisionTaskAllocation struct {
	AllocationID  string `json:"allocationId"`
	BindIP        string `json:"bindIp"`
	HostPort      uint32 `json:"hostPort"`
	ContainerPort uint32 `json:"containerPort"`
	Protocol      string `json:"protocol"`
}

// stringifiedStartupValues 把启动变量值转为 proto map<string,string> 所需的
// 字符串值（bool 与数字统一字符串化）。
func stringifiedStartupValues(values map[string]any) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		switch typed := value.(type) {
		case string:
			result[key] = typed
		case bool:
			result[key] = strconv.FormatBool(typed)
		case int64:
			result[key] = strconv.FormatInt(typed, 10)
		case int:
			result[key] = strconv.Itoa(typed)
		default:
			result[key] = fmt.Sprint(value)
		}
	}
	return result
}

func stringifiedNonSecretStartupValues(definitions []domain.StartupVariable, values map[string]any) map[string]string {
	secret := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if definition.Secret {
			secret[definition.Key] = struct{}{}
		}
	}
	filtered := make(map[string]any, len(values))
	for key, value := range values {
		if _, isSecret := secret[key]; !isSecret {
			filtered[key] = value
		}
	}
	return stringifiedStartupValues(filtered)
}

func secretStartupKeys(definitions []domain.StartupVariable) []string {
	keys := make([]string, 0)
	for _, definition := range definitions {
		if definition.Secret {
			keys = append(keys, definition.Key)
		}
	}
	return keys
}

// provisionProtocolName 把 store 内部的 tcp/udp 协议名映射为 proto 枚举名。
func provisionProtocolName(protocol string) string {
	if protocol == "udp" {
		return "NETWORK_PROTOCOL_UDP"
	}
	return "NETWORK_PROTOCOL_TCP"
}

// backupTaskPayload 是下发给 Agent 的备份任务负载（JSON 形状与
// agentv1.BackupTaskPayload 的 protojson 字段名对齐）。CreateBackup/
// RestoreBackup/DeleteBackup 落库到 server_tasks.checkpoint，ClaimTask
// 将其作为 PayloadJSON 下发给 Agent 执行 docker exec tar。
type backupTaskPayload struct {
	BackupID               string `json:"backupId"`
	FormatVersion          string `json:"formatVersion,omitempty"`
	StorageObjectKey       string `json:"storageObjectKey,omitempty"`
	ExpectedManifestDigest string `json:"expectedManifestDigest,omitempty"`
	ExpectedContentDigest  string `json:"expectedContentDigest,omitempty"`
	DeleteRemoteObject     bool   `json:"deleteRemoteObject,omitempty"`
}
