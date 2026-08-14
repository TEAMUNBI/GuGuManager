package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	serverfiles "github.com/gugumanager/gugumanager/internal/files"
	"github.com/gugumanager/gugumanager/internal/id"
	gamedefinition "github.com/gugumanager/gugumanager/spec/game-definition"
	"github.com/lib/pq"
)

// fixedCatalogGame resolves a game definition from the embedded fixed bundle
// catalog. PostgreSQL stores only bundle metadata, so the fixed catalog is the
// authoritative source for the Startup declaration (variables, bindings,
// command) exactly like the in-memory adapter.
var (
	fixedCatalogOnce sync.Once
	fixedCatalog     map[string]domain.GameDefinition
	fixedCatalogErr  error
)

func fixedCatalogGame(gameID string) (domain.GameDefinition, error) {
	fixedCatalogOnce.Do(func() {
		games, err := loadFixedGameCatalog()
		if err != nil {
			fixedCatalogErr = err
			return
		}
		fixedCatalog = make(map[string]domain.GameDefinition, len(games))
		for _, game := range games {
			fixedCatalog[game.ID] = game
		}
	})
	if fixedCatalogErr != nil {
		return domain.GameDefinition{}, fixedCatalogErr
	}
	game, ok := fixedCatalog[gameID]
	if !ok {
		return domain.GameDefinition{}, packageIncompatible("server references an unknown fixed Bundle")
	}
	return game, nil
}

// AuthorizeServer checks whether userID holds permission on serverID.
// Platform administrators bypass membership permissions; other users need an
// active membership carrying the requested permission.
func (s *Postgres) AuthorizeServer(userID string, serverID string, permission string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.authorizeServer(ctx, userID, serverID, permission)
	return err
}

func (s *Postgres) authorizeServer(ctx context.Context, actorID string, serverID string, permission string) (domain.User, error) {
	var user domain.User
	var roles []string
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.display_name, u.status,
		       COALESCE(array_agg(r.role_key) FILTER (WHERE r.role_key IS NOT NULL), '{}') as roles
		FROM users u
		LEFT JOIN user_roles ur ON u.id = ur.user_id
		LEFT JOIN roles r ON ur.role_id = r.id
		WHERE u.id = $1
		GROUP BY u.id, u.display_name, u.status
	`, actorID).Scan(&user.ID, &user.DisplayName, &user.Status, pq.Array(&roles))
	if err != nil || user.Status != "active" {
		return domain.User{}, domain.NewProblem("AUTH_REQUIRED", "请先登录", false)
	}
	user.Roles = roles

	var serverExists bool
	if err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM servers WHERE id = $1 AND deleted_at IS NULL)
	`, serverID).Scan(&serverExists); err != nil {
		return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法查询服务器", true)
	}
	if !serverExists {
		return domain.User{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	if hasRole(user, "platform_admin") {
		return user, nil
	}

	var permissions []string
	err = s.db.QueryRowContext(ctx, `
		SELECT permissions FROM server_members WHERE server_id = $1 AND user_id = $2
	`, serverID, actorID).Scan(pq.Array(&permissions))
	if err == sql.ErrNoRows {
		return domain.User{}, domain.NewProblem("NOT_FOUND", "服务器不存在或未授权", false)
	}
	if err != nil {
		return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法查询服务器成员关系", true)
	}
	if !containsString(permissions, permission) {
		return domain.User{}, domain.NewProblem("FORBIDDEN", "缺少服务器操作权限", false)
	}
	return user, nil
}

// EffectiveServerPermissions returns the permissions userID can use on one
// server. It mirrors the in-memory adapter: platform administrators get the
// full permission set; members need servers.read to be exposed at all.
func (s *Postgres) EffectiveServerPermissions(userID string, serverID string) (domain.ServerPermissions, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM users WHERE id = $1`, userID).Scan(&status)
	if err != nil || status != "active" {
		return domain.ServerPermissions{}, domain.NewProblem("AUTH_REQUIRED", "请先登录", false)
	}
	var serverExists bool
	if err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM servers WHERE id = $1 AND deleted_at IS NULL)
	`, serverID).Scan(&serverExists); err != nil {
		return domain.ServerPermissions{}, domain.NewProblem("INTERNAL_ERROR", "无法查询服务器", true)
	}
	if !serverExists {
		return domain.ServerPermissions{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}

	var roles []string
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(array_agg(r.role_key) FILTER (WHERE r.role_key IS NOT NULL), '{}')
		FROM user_roles ur
		JOIN roles r ON ur.role_id = r.id
		WHERE ur.user_id = $1
	`, userID).Scan(pq.Array(&roles)); err != nil {
		return domain.ServerPermissions{}, domain.NewProblem("INTERNAL_ERROR", "无法查询用户角色", true)
	}
	if containsString(roles, "platform_admin") {
		return domain.ServerPermissions{ServerID: serverID, Permissions: allServerPermissions()}, nil
	}

	var permissions []string
	err = s.db.QueryRowContext(ctx, `
		SELECT permissions FROM server_members WHERE server_id = $1 AND user_id = $2
	`, serverID, userID).Scan(pq.Array(&permissions))
	if err == sql.ErrNoRows || !containsString(permissions, "servers.read") {
		return domain.ServerPermissions{}, domain.NewProblem("NOT_FOUND", "服务器不存在或未授权", false)
	}
	if err != nil {
		return domain.ServerPermissions{}, domain.NewProblem("INTERNAL_ERROR", "无法查询服务器成员关系", true)
	}
	sort.Strings(permissions)
	return domain.ServerPermissions{ServerID: serverID, Permissions: permissions}, nil
}

// VisibleServers returns the servers visible to userID, filtered by query.
// Platform administrators see every non-deleted server.
func (s *Postgres) VisibleServers(userID string, query string) []domain.Server {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM users WHERE id = $1`, userID).Scan(&status)
	if err != nil || status != "active" {
		return []domain.Server{}
	}
	var roles []string
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(array_agg(r.role_key) FILTER (WHERE r.role_key IS NOT NULL), '{}')
		FROM user_roles ur
		JOIN roles r ON ur.role_id = r.id
		WHERE ur.user_id = $1
	`, userID).Scan(pq.Array(&roles)); err != nil {
		return []domain.Server{}
	}
	admin := containsString(roles, "platform_admin")

	query = strings.TrimSpace(query)
	sqlText := serverSelect + ` WHERE s.deleted_at IS NULL`
	var args []any
	if !admin {
		sqlText += ` AND s.id IN (
			SELECT server_id FROM server_members
			WHERE user_id = $1 AND permissions @> ARRAY['servers.read']
		)`
		args = append(args, userID)
	}
	if query != "" {
		args = append(args, query)
		if admin {
			sqlText += ` AND (s.name ILIKE '%' || $1 || '%' OR gd.name ILIKE '%' || $1 || '%' OR n.name ILIKE '%' || $1 || '%')`
		} else {
			sqlText += ` AND (s.name ILIKE '%' || $2 || '%' OR gd.name ILIKE '%' || $2 || '%' OR n.name ILIKE '%' || $2 || '%')`
		}
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
		servers = append(servers, server)
	}
	return servers
}

// Overview aggregates counts across the persisted tables. CPU and live memory
// usage come from the server_metrics snapshots persisted by agent MetricsBatch
// frames; without any reporting those fields stay zero while memory limits come
// from the servers table.
func (s *Postgres) Overview() domain.Overview {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := domain.Overview{Environment: s.environment}
	err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM servers WHERE deleted_at IS NULL),
			(SELECT COUNT(*) FROM servers WHERE deleted_at IS NULL AND observed_power = 'running'),
			(SELECT COUNT(*) FROM nodes WHERE revoked_at IS NULL),
			(SELECT COUNT(*) FROM nodes WHERE revoked_at IS NULL AND condition = 'available'),
			(SELECT COUNT(*) FROM server_tasks WHERE status IN ('queued', 'leased', 'running')),
			(SELECT COALESCE(SUM(memory_limit_bytes), 0) FROM servers WHERE deleted_at IS NULL),
			(SELECT COALESCE(SUM(memory_bytes), 0) FROM server_metrics),
			(SELECT COALESCE(AVG(cpu_percent), 0) FROM server_metrics)
	`).Scan(&result.ServerCount, &result.RunningServerCount, &result.TotalNodeCount,
		&result.OnlineNodeCount, &result.QueuedOperationCount, &result.MemoryTotalBytes,
		&result.MemoryUsedBytes, &result.CPUPercent)
	if err != nil {
		return result
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id::text, COALESCE(u.display_name, 'system'), e.action, e.target_type, e.target_id,
		       e.result, COALESCE(e.operation_id::text, ''), e.created_at
		FROM audit_events e
		LEFT JOIN users u ON u.id = e.actor_id
		ORDER BY e.created_at DESC
		LIMIT 5
	`)
	if err != nil {
		return result
	}
	defer rows.Close()
	for rows.Next() {
		event, err := scanAuditEvent(rows)
		if err != nil {
			continue
		}
		result.RecentActivity = append(result.RecentActivity, event)
	}
	return result
}

func scanAuditEvent(scanner rowScanner) (domain.AuditEvent, error) {
	var event domain.AuditEvent
	err := scanner.Scan(&event.ID, &event.ActorName, &event.Action, &event.TargetType, &event.TargetName,
		&event.Result, &event.OperationID, &event.CreatedAt)
	return event, err
}

// GameDefinitions lists the approved game catalog rows persisted in
// PostgreSQL. Presentation metadata (summary/icon/defaults) is enriched from
// the embedded fixed catalog when the id matches.
func (s *Postgres) GameDefinitions() []domain.GameDefinition {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `
		SELECT gd.id, gd.name, gd.review_status,
		       COALESCE((SELECT gb.digest FROM game_bundles gb
		                 WHERE gb.game_definition_id = gd.id
		                 ORDER BY gb.created_at DESC LIMIT 1), ''),
		       COALESCE((SELECT gb.definition_version FROM game_bundles gb
		                 WHERE gb.game_definition_id = gd.id
		                 ORDER BY gb.created_at DESC LIMIT 1), ''),
		       COALESCE((SELECT gb.game_version FROM game_bundles gb
		                 WHERE gb.game_definition_id = gd.id
		                 ORDER BY gb.created_at DESC LIMIT 1), ''),
		       (SELECT COUNT(*) FROM servers sv
		         JOIN game_bundles svb ON svb.id = sv.game_bundle_id
		         WHERE svb.game_definition_id = gd.id AND sv.deleted_at IS NULL)
		FROM game_definitions gd
		ORDER BY gd.created_at ASC
	`)
	if err != nil {
		return []domain.GameDefinition{}
	}
	defer rows.Close()

	games := []domain.GameDefinition{}
	for rows.Next() {
		var game domain.GameDefinition
		if err := rows.Scan(&game.ID, &game.Name, &game.Status, &game.BundleDigest,
			&game.Version, &game.GameVersion, &game.Servers); err != nil {
			continue
		}
		markCatalogBundleUntrusted(&game, gameSourceDatabaseMetadata)
		if fixed, err := fixedCatalogGame(game.ID); err == nil && fixed.BundleDigest == game.BundleDigest {
			game.Summary = fixed.Summary
			game.Capabilities = append([]string(nil), fixed.Capabilities...)
			game.Platforms = append([]string(nil), fixed.Platforms...)
			game.Icon = fixed.Icon
			game.DefaultMemory = fixed.DefaultMemory
			game.DefaultDisk = fixed.DefaultDisk
			game.BundleDocument = fixed.BundleDocument
			game.Signed = fixed.Signed
			game.Verified = fixed.Verified
			game.Runnable = fixed.Runnable
			game.Supported = fixed.Supported
			game.TrustLevel = fixed.TrustLevel
			game.Source = fixed.Source
			game.SupportReasons = append([]string(nil), fixed.SupportReasons...)
			game.RuntimeTarget = cloneRuntimeTarget(fixed.RuntimeTarget)
		}
		games = append(games, game)
	}
	return games
}

// AuditEvents lists all audit events newest first.
func (s *Postgres) AuditEvents() []domain.AuditEvent {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id::text, COALESCE(u.display_name, 'system'), e.action, e.target_type, e.target_id,
		       e.result, COALESCE(e.operation_id::text, ''), e.created_at
		FROM audit_events e
		LEFT JOIN users u ON u.id = e.actor_id
		ORDER BY e.created_at DESC
	`)
	if err != nil {
		return []domain.AuditEvent{}
	}
	defer rows.Close()

	events := []domain.AuditEvent{}
	for rows.Next() {
		event, err := scanAuditEvent(rows)
		if err != nil {
			continue
		}
		events = append(events, event)
	}
	return events
}

// VisibleOperations returns operations for servers the user can read, newest
// first. Platform administrators see everything.
func (s *Postgres) VisibleOperations(userID string) []domain.Operation {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM users WHERE id = $1`, userID).Scan(&status)
	if err != nil || status != "active" {
		return []domain.Operation{}
	}
	var roles []string
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(array_agg(r.role_key) FILTER (WHERE r.role_key IS NOT NULL), '{}')
		FROM user_roles ur
		JOIN roles r ON ur.role_id = r.id
		WHERE ur.user_id = $1
	`, userID).Scan(pq.Array(&roles)); err != nil {
		return []domain.Operation{}
	}
	admin := containsString(roles, "platform_admin")

	sqlText := taskSelect
	var args []any
	if !admin {
		sqlText += ` WHERE st.server_id IN (
			SELECT server_id FROM server_members
			WHERE user_id = $1 AND permissions @> ARRAY['servers.read']
		)`
		args = append(args, userID)
	}
	sqlText += ` ORDER BY st.created_at DESC, st.id ASC`

	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return []domain.Operation{}
	}
	defer rows.Close()

	operations := []domain.Operation{}
	for rows.Next() {
		operation, err := operationFromTask(rows)
		if err != nil {
			continue
		}
		operations = append(operations, operation)
	}
	return operations
}

// Operation looks up one operation by id. Authorization is enforced by the
// HTTP layer (AuthorizeServer), matching the in-memory adapter.
func (s *Postgres) Operation(operationID string) (domain.Operation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	operation, err := operationFromTask(s.db.QueryRowContext(ctx, taskSelect+` WHERE st.id = $1`, operationID))
	if err == sql.ErrNoRows {
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "操作不存在", false)
	}
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法查询操作", true)
	}
	return operation, nil
}

// taskSelect maps one server_tasks row onto a domain.Operation. The id column
// doubles as the operation id. 000009 起 checkpoint 只承载执行期进度（以
// JSON 字符串形式存放），读取时用 #>> '{}' 还原原始字符串；pre-000009 的
// 对象型 checkpoint 同样原样返回 JSON 文本。
const taskSelect = `
	SELECT st.id::text, st.server_id::text, st.node_id::text, st.task_type, st.status,
	       st.generation, st.attempt, st.max_attempts, st.lease_owner,
	       st.lease_expires_at, st.checkpoint #>> '{}', st.progress,
	       st.error_code, st.error_retryable, st.idempotency_key,
	       st.created_at, st.updated_at, st.completed_at
	FROM server_tasks st`

func operationFromTask(scanner rowScanner) (domain.Operation, error) {
	var operation domain.Operation
	var taskType string
	var leaseOwner sql.NullString
	var leaseExpiresAt sql.NullTime
	var checkpoint sql.NullString
	var errorCode sql.NullString
	var errorRetryable sql.NullBool
	var createdAt, updatedAt time.Time
	var completedAt sql.NullTime
	err := scanner.Scan(
		&operation.ID, &operation.ServerID, &operation.NodeID, &taskType, &operation.Status,
		&operation.Generation, &operation.Attempt, &operation.MaxAttempts, &leaseOwner,
		&leaseExpiresAt, &checkpoint, &operation.Progress,
		&errorCode, &errorRetryable, &operation.IdempotencyKey,
		&createdAt, &updatedAt, &completedAt,
	)
	if err != nil {
		return domain.Operation{}, err
	}
	operation.Type = domain.PowerAction(taskType)
	operation.CreatedAt = createdAt
	operation.UpdatedAt = updatedAt
	if leaseOwner.Valid {
		operation.LeaseOwner = &leaseOwner.String
	}
	if leaseExpiresAt.Valid {
		expiresAt := leaseExpiresAt.Time
		operation.LeaseExpiresAt = &expiresAt
	}
	if checkpoint.Valid {
		operation.Checkpoint = checkpoint.String
	}
	if errorCode.Valid {
		operation.Error = &domain.OperationError{Code: errorCode.String, Message: "异步操作未能完成", Retryable: errorRetryable.Bool}
	}
	normalizeOperationMetadata(&operation)
	return operation, nil
}

// serverRow is the locked servers row plus its node/game join for one write
// transaction.
type serverRow struct {
	ID                    string
	Name                  string
	NodeID                string
	NodeCondition         string
	Lifecycle             string
	Desired               string
	Observed              string
	Generation            int64
	GameID                string
	GameBundleDigest      string
	GameDefinitionVersion string
}

// lockServerRow locks the server row FOR UPDATE and returns it with the node
// condition and game bundle identity. Callers map sql.ErrNoRows to NOT_FOUND.
func (s *Postgres) lockServerRow(ctx context.Context, tx *sql.Tx, serverID string) (serverRow, error) {
	var row serverRow
	err := tx.QueryRowContext(ctx, `
		SELECT s.id::text, s.name, s.lifecycle_state, s.desired_power, s.observed_power,
		       s.generation, s.node_id::text,
		       COALESCE(n.condition, 'offline'),
		       COALESCE(gd.id, ''), COALESCE(gb.digest, ''), COALESCE(gb.definition_version, '')
		FROM servers s
		LEFT JOIN nodes n ON n.id = s.node_id
		LEFT JOIN game_bundles gb ON gb.id = s.game_bundle_id
		LEFT JOIN game_definitions gd ON gd.id = gb.game_definition_id
		WHERE s.id = $1 AND s.deleted_at IS NULL
		FOR UPDATE OF s
	`, serverID).Scan(&row.ID, &row.Name, &row.Lifecycle, &row.Desired, &row.Observed,
		&row.Generation, &row.NodeID, &row.NodeCondition,
		&row.GameID, &row.GameBundleDigest, &row.GameDefinitionVersion)
	return row, err
}

// activeTaskInTx returns the active exclusive task for a server, if any. The
// partial unique index on server_tasks guarantees at most one.
func (s *Postgres) activeTaskInTx(ctx context.Context, tx *sql.Tx, serverID string) (domain.Operation, bool, error) {
	operation, err := operationFromTask(tx.QueryRowContext(ctx, taskSelect+`
		WHERE st.server_id = $1 AND st.status IN ('queued', 'leased', 'running')
		ORDER BY st.created_at
		LIMIT 1
	`, serverID))
	if err == sql.ErrNoRows {
		return domain.Operation{}, false, nil
	}
	if err != nil {
		return domain.Operation{}, false, domain.NewProblem("INTERNAL_ERROR", "无法查询进行中的任务", true)
	}
	return operation, true, nil
}

// generationFence validates the If-Match generation against the locked row.
func generationFence(row serverRow, expectedGeneration int64) error {
	if row.Generation == expectedGeneration {
		return nil
	}
	problem := domain.NewProblem("PRECONDITION_FAILED", "服务器 generation 已变化，请刷新后重试", false)
	problem.Details["currentGeneration"] = row.Generation
	problem.Details["providedGeneration"] = expectedGeneration
	return problem
}

// requireServerReconcileCapabilityTx verifies the target node's live state and
// the exact versioned capability declaration while the caller holds the
// server write lock. PostgreSQL persists capabilities as a split name/version
// pair, so the canonical domain declaration must never be queried as a key.
func (s *Postgres) requireServerReconcileCapabilityTx(ctx context.Context, tx *sql.Tx, nodeID string) error {
	requiredCapability, requiredVersion, ok := domain.SplitNodeCapability(domain.NodeCapabilityServerReconcile)
	if !ok {
		return domain.NewProblem("INTERNAL_ERROR", "无法解析服务端所需的节点能力", false)
	}

	var condition string
	var nodeVersion string
	err := tx.QueryRowContext(ctx, `
		SELECT n.condition, n.agent_version
		FROM nodes n
		WHERE n.id = $1 AND n.revoked_at IS NULL
		FOR SHARE
	`, nodeID).Scan(&condition, &nodeVersion)
	if err == sql.ErrNoRows {
		return domain.NewProblem("NODE_OFFLINE", "节点当前离线，无法接收重新对账任务", true)
	}
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法核验目标节点能力", true)
	}
	if condition != "available" {
		return domain.NewProblem("NODE_OFFLINE", "节点当前离线，无法接收重新对账任务", true)
	}
	var capabilityEvidence int
	err = tx.QueryRowContext(ctx, `
		SELECT 1
		FROM node_capabilities
		WHERE node_id = $1 AND capability_key = $2 AND capability_version = $3
		FOR SHARE
	`, nodeID, requiredCapability, requiredVersion).Scan(&capabilityEvidence)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return domain.NewProblem("INTERNAL_ERROR", "无法核验目标节点能力", true)
	}

	problem := domain.NewProblem("CAPABILITY_UNSUPPORTED", "目标节点未声明执行该操作所需的能力", false)
	problem.Details["requiredCapability"] = requiredCapability
	problem.Details["requiredVersion"] = requiredVersion
	problem.Details["nodeId"] = nodeID
	problem.Details["nodeVersion"] = nodeVersion
	return problem
}

// enqueueTaskTx inserts a queued server_tasks row inside tx. taskInput is the
// immutable task input materialized for the agent (provision/backup payload
// JSON); empty means the task needs no input. It returns ("", nil) when the
// idempotency pair already exists so the caller can roll back and replay via
// lookupIdempotentTask.
func (s *Postgres) enqueueTaskTx(ctx context.Context, tx *sql.Tx, taskID, serverID, nodeID, taskType string, generation int64, actorID, idemKey string, requestDigest []byte, taskInput string) (string, error) {
	var actorIDValue any
	if actorID != "" {
		actorIDValue = actorID
	}
	var taskInputValue any
	if taskInput != "" {
		taskInputValue = taskInput
	}
	var inserted string
	err := tx.QueryRowContext(ctx, `
		INSERT INTO server_tasks (
			id, server_id, node_id, task_type, status, generation, actor_id,
			idempotency_scope, idempotency_key, request_digest, task_input, attempt, max_attempts, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, 'queued', $5, $6, $7, $8, $9, $10, 0, 3, now(), now())
		ON CONFLICT (idempotency_scope, idempotency_key) DO NOTHING
		RETURNING id::text
	`, taskID, serverID, nodeID, taskType, generation, actorIDValue,
		taskIdempotencyScope(taskType, actorID, idemKey), idemKey, requestDigest, taskInputValue).Scan(&inserted)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	// 任务入队与 outbox 事件同事务提交：业务状态、任务、事件三者原子，
	// 满足架构约定的"在一个数据库事务中写业务状态、任务和 Outbox"。
	if err := s.recordTaskOutboxEvent(ctx, tx, "task.created", taskEventPayload{
		OperationID: inserted,
		ServerID:    serverID,
		NodeID:      nodeID,
		TaskType:    taskType,
		Generation:  generation,
		Attempt:     0,
		MaxAttempts: 3,
		Status:      "queued",
	}); err != nil {
		return "", err
	}
	return inserted, nil
}

// commitWriteTask enqueues the task (optionally with a materialized task
// input), records the audit row, commits, and returns the accepted operation.
// A task conflict rolls back and replays the recorded idempotent operation.
func (s *Postgres) commitWriteTask(
	ctx context.Context,
	tx *sql.Tx,
	scope string,
	idemKey string,
	digest [32]byte,
	operationID, serverID, nodeID, taskType string,
	generation int64,
	actorID string,
	actor domain.User,
	auditAction string,
	now time.Time,
	taskInput string,
) (domain.Operation, error) {
	inserted, err := s.enqueueTaskTx(ctx, tx, operationID, serverID, nodeID, taskType, generation, actorID, idemKey, digest[:], taskInput)
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法创建任务", true)
	}
	if inserted == "" {
		_ = tx.Rollback()
		existing, ok, lookupErr := s.lookupIdempotentTask(ctx, scope, idemKey, digest)
		if lookupErr != nil || ok {
			return existing, lookupErr
		}
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "幂等记录缺失", true)
	}
	if err := s.recordAuditTx(ctx, tx, actor, auditAction, "server", serverID, "accepted", operationID); err != nil {
		return domain.Operation{}, err
	}
	if err := tx.Commit(); err != nil {
		if isUniqueViolation(err) {
			existing, ok, lookupErr := s.lookupIdempotentTask(ctx, scope, idemKey, digest)
			if lookupErr != nil || ok {
				return existing, lookupErr
			}
		}
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法提交事务", true)
	}
	return domain.NewQueuedOperation(operationID, serverID, nodeID, domain.PowerAction(taskType), generation, idemKey, now), nil
}

// recordAuditTx inserts one audit event inside tx.
func (s *Postgres) recordAuditTx(ctx context.Context, tx *sql.Tx, actor domain.User, action string, targetType string, targetID string, result string, operationID string) error {
	var actorID any
	if actor.ID != "" {
		actorID = actor.ID
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO audit_events (actor_id, actor_type, action, target_type, target_id, result, operation_id, trace_id, created_at)
		VALUES ($1, 'user', $2, $3, $4, $5, $6, $7, now())
	`, actorID, action, targetType, targetID, result, operationID, id.New())
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法记录审计日志", true)
	}
	return nil
}

// RequestPower validates the actor, advances the server generation, and
// enqueues a start/stop/restart/kill task.
func (s *Postgres) RequestPower(serverID string, action domain.PowerAction, idempotencyKey string, actor domain.User) (domain.Operation, error) {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return domain.Operation{}, err
	}
	if action != domain.PowerStart && action != domain.PowerStop && action != domain.PowerRestart && action != domain.PowerKill {
		return domain.Operation{}, domain.NewProblem("VALIDATION_FAILED", "不支持的电源操作", false)
	}
	digest := requestDigest(struct {
		Action domain.PowerAction `json:"action"`
	}{Action: action})
	taskType := string(action)
	scope := taskIdempotencyScope(taskType, actor.ID, idempotencyKey)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	currentActor, err := s.authorizeServer(ctx, actor.ID, serverID, "servers.power")
	if err != nil {
		return domain.Operation{}, err
	}
	if existing, ok, err := s.lookupIdempotentTask(ctx, scope, idempotencyKey, digest); err != nil || ok {
		return existing, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	row, err := s.lockServerRow(ctx, tx, serverID)
	if err == sql.ErrNoRows {
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法查询服务器", true)
	}
	if row.NodeCondition != "available" {
		return domain.Operation{}, domain.NewProblem("NODE_OFFLINE", "节点当前离线，无法接收电源操作", true)
	}
	if active, ok, err := s.activeTaskInTx(ctx, tx, serverID); err != nil {
		return domain.Operation{}, err
	} else if ok {
		if active.Type == action {
			return active, nil
		}
		return domain.Operation{}, operationInProgress(active)
	}
	if row.Lifecycle != "ready" {
		return domain.Operation{}, domain.NewProblem("OPERATION_IN_PROGRESS", "服务器仍在完成生命周期操作", true)
	}
	if action == domain.PowerStart || action == domain.PowerRestart {
		if err := s.validateRequiredStartupVariables(ctx, tx, row); err != nil {
			return domain.Operation{}, err
		}
	}

	now := time.Now().UTC()
	nextGeneration := row.Generation + 1
	desired := row.Desired
	observed := row.Observed
	switch action {
	case domain.PowerStart:
		desired = "running"
		observed = "starting"
	case domain.PowerStop, domain.PowerKill:
		desired = "stopped"
		observed = "stopping"
	default:
		observed = "stopping"
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE servers
		SET generation = generation + 1, desired_power = $2, observed_power = $3,
		    node_condition = $4, updated_at = now()
		WHERE id = $1
	`, serverID, desired, observed, row.NodeCondition); err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法更新服务器电源状态", true)
	}

	operationID := id.New()
	operation, err := s.commitWriteTask(ctx, tx, scope, idempotencyKey, digest, operationID, serverID, row.NodeID, taskType,
		nextGeneration, actor.ID, currentActor, "server.power."+taskType, now, "")
	if err != nil {
		return domain.Operation{}, err
	}
	return operation, nil
}

// validateRequiredStartupVariables ensures every required Startup variable is
// configured before a start/restart task is accepted.
func (s *Postgres) validateRequiredStartupVariables(ctx context.Context, tx *sql.Tx, row serverRow) error {
	server := domain.Server{
		ID: row.ID, GameID: row.GameID, GameBundleDigest: row.GameBundleDigest,
		GameDefinitionVersion: row.GameDefinitionVersion, NodeID: row.NodeID, Generation: row.Generation,
	}
	game, err := fixedCatalogGame(row.GameID)
	if err != nil {
		return err
	}
	startup, _, err := startupFromFixedBundle(server, game, nil)
	if err != nil {
		return err
	}
	values, err := s.startupValuesTx(ctx, tx, row.ID)
	if err != nil {
		return err
	}
	if err := s.decryptSecretValues(startup.Variables, values); err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法解密启动变量", true)
	}
	missing := make([]string, 0)
	for _, variable := range startup.Variables {
		value, configured := values[variable.Key]
		if variable.Required && (!configured || value == nil) {
			missing = append(missing, variable.Key)
		}
	}
	if len(missing) > 0 {
		return validationProblem("missing required Startup variables: " + strings.Join(missing, ", "))
	}
	return nil
}

// Allocations lists the active (non-released) allocations of a server.
func (s *Postgres) Allocations(serverID string) ([]domain.Allocation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var serverExists bool
	if err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM servers WHERE id = $1 AND deleted_at IS NULL)
	`, serverID).Scan(&serverExists); err != nil {
		return nil, domain.NewProblem("INTERNAL_ERROR", "无法查询服务器", true)
	}
	if !serverExists {
		return nil, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, server_id::text, node_id::text, host(bind_ip), port, protocol, port_ref, container_port, role, is_primary, created_at
		FROM allocations
		WHERE server_id = $1 AND released_at IS NULL
		ORDER BY created_at ASC
	`, serverID)
	if err != nil {
		return nil, domain.NewProblem("INTERNAL_ERROR", "无法查询网络分配", true)
	}
	defer rows.Close()

	allocations := []domain.Allocation{}
	for rows.Next() {
		var allocation domain.Allocation
		if err := rows.Scan(&allocation.ID, &allocation.ServerID, &allocation.NodeID, &allocation.BindIP,
			&allocation.Port, &allocation.Protocol, &allocation.PortRef, &allocation.ContainerPort, &allocation.Role, &allocation.Primary, &allocation.CreatedAt); err != nil {
			continue
		}
		allocation.UpdatedAt = allocation.CreatedAt
		allocations = append(allocations, allocation)
	}
	return allocations, nil
}

// CreateAllocation validates the endpoint, advances the generation, and
// enqueues a reconcile task.
func (s *Postgres) CreateAllocation(
	serverID string,
	input domain.CreateAllocationInput,
	expectedGeneration int64,
	idempotencyKey string,
	actor domain.User,
) (domain.Operation, error) {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return domain.Operation{}, err
	}
	normalized, err := normalizeAllocationInput(input)
	if err != nil {
		return domain.Operation{}, err
	}
	digest := requestDigest(struct {
		Generation int64                        `json:"generation"`
		Input      domain.CreateAllocationInput `json:"input"`
	}{Generation: expectedGeneration, Input: normalized})
	taskType := "reconcile"
	scope := taskIdempotencyScope(taskType, actor.ID, idempotencyKey)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	currentActor, err := s.authorizeServer(ctx, actor.ID, serverID, "servers.network.write")
	if err != nil {
		return domain.Operation{}, err
	}
	if existing, ok, err := s.lookupIdempotentTask(ctx, scope, idempotencyKey, digest); err != nil || ok {
		return existing, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	row, err := s.lockServerRow(ctx, tx, serverID)
	if err == sql.ErrNoRows {
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法查询服务器", true)
	}
	if err := s.requireServerReconcileCapabilityTx(ctx, tx, row.NodeID); err != nil {
		return domain.Operation{}, err
	}
	if err := generationFence(row, expectedGeneration); err != nil {
		return domain.Operation{}, err
	}
	if active, ok, err := s.activeTaskInTx(ctx, tx, serverID); err != nil {
		return domain.Operation{}, err
	} else if ok {
		return domain.Operation{}, operationInProgress(active)
	}
	if row.Lifecycle != "ready" {
		return domain.Operation{}, domain.NewProblem("OPERATION_IN_PROGRESS", "服务器仍在完成生命周期操作", true)
	}

	now := time.Now().UTC()
	var conflict bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM allocations
			WHERE node_id = $1 AND host(bind_ip) = $2 AND port = $3 AND protocol = $4 AND released_at IS NULL
		)
	`, row.NodeID, normalized.BindIP, normalized.Port, normalized.Protocol).Scan(&conflict); err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法检查端口占用", true)
	}
	if conflict {
		return domain.Operation{}, domain.NewProblem("PORT_CONFLICT", "节点上的监听地址已被占用", false)
	}

	makePrimary := normalized.Primary
	if !makePrimary {
		var count int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM allocations WHERE server_id = $1 AND released_at IS NULL
		`, serverID).Scan(&count); err != nil {
			return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法统计网络分配", true)
		}
		makePrimary = count == 0
	}
	allocationID := id.New()
	if makePrimary {
		if _, err := tx.ExecContext(ctx, `
			UPDATE allocations SET is_primary = false WHERE server_id = $1 AND released_at IS NULL
		`, serverID); err != nil {
			return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法切换主分配", true)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO allocations (id, node_id, bind_ip, port, protocol, port_ref, container_port, role, server_id, is_primary, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, allocationID, row.NodeID, normalized.BindIP, normalized.Port, normalized.Protocol, normalized.PortRef, normalized.ContainerPort, normalized.Role, serverID, makePrimary, now); err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法创建网络分配", true)
	}

	nextGeneration := row.Generation + 1
	if _, err := tx.ExecContext(ctx, `
		UPDATE servers SET generation = generation + 1, updated_at = now() WHERE id = $1
	`, serverID); err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法推进服务器 generation", true)
	}

	operationID := id.New()
	return s.commitWriteTask(ctx, tx, scope, idempotencyKey, digest, operationID, serverID, row.NodeID, taskType,
		nextGeneration, actor.ID, currentActor, "network.allocation.create", now, "")
}

// SetPrimaryAllocation switches which active allocation is primary and
// enqueues a reconcile task.
func (s *Postgres) SetPrimaryAllocation(
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
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return domain.Operation{}, err
	}
	digest := requestDigest(struct {
		Generation   int64  `json:"generation"`
		AllocationID string `json:"allocationId"`
	}{Generation: expectedGeneration, AllocationID: allocationID})
	taskType := "reconcile"
	scope := taskIdempotencyScope(taskType, actor.ID, idempotencyKey)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	currentActor, err := s.authorizeServer(ctx, actor.ID, serverID, "servers.network.write")
	if err != nil {
		return domain.Operation{}, err
	}
	if existing, ok, err := s.lookupIdempotentTask(ctx, scope, idempotencyKey, digest); err != nil || ok {
		return existing, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	row, err := s.lockServerRow(ctx, tx, serverID)
	if err == sql.ErrNoRows {
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法查询服务器", true)
	}
	if err := s.requireServerReconcileCapabilityTx(ctx, tx, row.NodeID); err != nil {
		return domain.Operation{}, err
	}
	if err := generationFence(row, expectedGeneration); err != nil {
		return domain.Operation{}, err
	}
	if active, ok, err := s.activeTaskInTx(ctx, tx, serverID); err != nil {
		return domain.Operation{}, err
	} else if ok {
		return domain.Operation{}, operationInProgress(active)
	}
	if row.Lifecycle != "ready" {
		return domain.Operation{}, domain.NewProblem("OPERATION_IN_PROGRESS", "服务器仍在完成生命周期操作", true)
	}

	now := time.Now().UTC()
	var exists bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM allocations WHERE id = $1 AND server_id = $2 AND released_at IS NULL
		)
	`, allocationID, serverID).Scan(&exists); err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法查询网络分配", true)
	}
	if !exists {
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "网络分配不存在", false)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE allocations
		SET is_primary = (id = $1)
		WHERE server_id = $2 AND released_at IS NULL
	`, allocationID, serverID); err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法切换主分配", true)
	}

	nextGeneration := row.Generation + 1
	if _, err := tx.ExecContext(ctx, `
		UPDATE servers SET generation = generation + 1, updated_at = now() WHERE id = $1
	`, serverID); err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法推进服务器 generation", true)
	}

	operationID := id.New()
	return s.commitWriteTask(ctx, tx, scope, idempotencyKey, digest, operationID, serverID, row.NodeID, taskType,
		nextGeneration, actor.ID, currentActor, "network.allocation.primary", now, "")
}

// DeleteAllocation releases an allocation (soft delete) and enqueues a
// reconcile task. The last allocation and the primary allocation cannot be
// deleted.
func (s *Postgres) DeleteAllocation(
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
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return domain.Operation{}, err
	}
	digest := requestDigest(struct {
		Generation   int64  `json:"generation"`
		AllocationID string `json:"allocationId"`
	}{Generation: expectedGeneration, AllocationID: allocationID})
	taskType := "reconcile"
	scope := taskIdempotencyScope(taskType, actor.ID, idempotencyKey)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	currentActor, err := s.authorizeServer(ctx, actor.ID, serverID, "servers.network.write")
	if err != nil {
		return domain.Operation{}, err
	}
	if existing, ok, err := s.lookupIdempotentTask(ctx, scope, idempotencyKey, digest); err != nil || ok {
		return existing, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	row, err := s.lockServerRow(ctx, tx, serverID)
	if err == sql.ErrNoRows {
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法查询服务器", true)
	}
	if err := s.requireServerReconcileCapabilityTx(ctx, tx, row.NodeID); err != nil {
		return domain.Operation{}, err
	}
	if err := generationFence(row, expectedGeneration); err != nil {
		return domain.Operation{}, err
	}
	if active, ok, err := s.activeTaskInTx(ctx, tx, serverID); err != nil {
		return domain.Operation{}, err
	} else if ok {
		return domain.Operation{}, operationInProgress(active)
	}
	if row.Lifecycle != "ready" {
		return domain.Operation{}, domain.NewProblem("OPERATION_IN_PROGRESS", "服务器仍在完成生命周期操作", true)
	}

	now := time.Now().UTC()
	var allocation domain.Allocation
	err = tx.QueryRowContext(ctx, `
		SELECT id::text, server_id::text, is_primary FROM allocations
		WHERE id = $1 AND server_id = $2 AND released_at IS NULL
		FOR UPDATE
	`, allocationID, serverID).Scan(&allocation.ID, &allocation.ServerID, &allocation.Primary)
	if err == sql.ErrNoRows {
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "网络分配不存在", false)
	}
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法查询网络分配", true)
	}
	var activeCount int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM allocations WHERE server_id = $1 AND released_at IS NULL
	`, serverID).Scan(&activeCount); err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法统计网络分配", true)
	}
	if activeCount <= 1 {
		return domain.Operation{}, domain.NewProblem("OPERATION_CONFLICT", "不能删除服务器的最后一个网络分配", false)
	}
	if allocation.Primary {
		return domain.Operation{}, domain.NewProblem("OPERATION_CONFLICT", "不能删除主网络分配，请先切换主分配", false)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE allocations SET released_at = $1 WHERE id = $2
	`, now, allocationID); err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法释放网络分配", true)
	}

	nextGeneration := row.Generation + 1
	if _, err := tx.ExecContext(ctx, `
		UPDATE servers SET generation = generation + 1, updated_at = now() WHERE id = $1
	`, serverID); err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法推进服务器 generation", true)
	}

	operationID := id.New()
	return s.commitWriteTask(ctx, tx, scope, idempotencyKey, digest, operationID, serverID, row.NodeID, taskType,
		nextGeneration, actor.ID, currentActor, "network.allocation.delete", now, "")
}

// Startup returns the Startup declaration resolved from the fixed bundle plus
// the persisted variable values.
func (s *Postgres) Startup(serverID string) (domain.Startup, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	server, err := s.Server(serverID)
	if err != nil {
		return domain.Startup{}, err
	}
	game, err := fixedCatalogGame(server.GameID)
	if err != nil {
		return domain.Startup{}, err
	}
	startup, _, err := startupFromFixedBundle(server, game, nil)
	if err != nil {
		return domain.Startup{}, err
	}
	values, err := s.startupValues(ctx, serverID)
	if err != nil {
		return domain.Startup{}, err
	}
	// Secret 变量值静态加密存储，读取时先解密，供生成启动命令与展示。
	if err := s.decryptSecretValues(startup.Variables, values); err != nil {
		return domain.Startup{}, domain.NewProblem("INTERNAL_ERROR", "无法解密启动变量", true)
	}
	result := domain.Startup{
		ServerID: serverID, Generation: server.Generation,
		Command:   resolveStartupCommand(startup, values),
		Variables: make([]domain.StartupVariable, 0, len(startup.Variables)),
	}
	for _, definition := range startup.Variables {
		value, configured := values[definition.Key]
		result.Variables = append(result.Variables, startupVariablePublicView(definition, value, configured))
	}
	return result, nil
}

func (s *Postgres) startupValues(ctx context.Context, serverID string) (map[string]any, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT values FROM startup_values WHERE server_id = $1`, serverID).Scan(&raw)
	if err == sql.ErrNoRows {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, domain.NewProblem("INTERNAL_ERROR", "无法读取启动变量", true)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var values map[string]any
	if err := decoder.Decode(&values); err != nil {
		return nil, domain.NewProblem("INTERNAL_ERROR", "无法解析启动变量", true)
	}
	return normalizeJSONNumbers(values).(map[string]any), nil
}

func (s *Postgres) startupValuesTx(ctx context.Context, tx *sql.Tx, serverID string) (map[string]any, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT values FROM startup_values WHERE server_id = $1`, serverID).Scan(&raw)
	if err == sql.ErrNoRows {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, domain.NewProblem("INTERNAL_ERROR", "无法读取启动变量", true)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var values map[string]any
	if err := decoder.Decode(&values); err != nil {
		return nil, domain.NewProblem("INTERNAL_ERROR", "无法解析启动变量", true)
	}
	return normalizeJSONNumbers(values).(map[string]any), nil
}

// normalizeJSONNumbers converts decoded JSON numbers back to int64 when they
// represent JavaScript-safe integers, matching the in-memory adapter's values.
func normalizeJSONNumbers(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if integer, exact := gamedefinition.ParseStartupInteger(typed); exact {
			return integer
		}
		return typed.String()
	case map[string]any:
		for key, child := range typed {
			typed[key] = normalizeJSONNumbers(child)
		}
		return typed
	case []any:
		for index, child := range typed {
			typed[index] = normalizeJSONNumbers(child)
		}
		return typed
	default:
		return value
	}
}

// UpdateStartup validates the variable updates against the fixed bundle
// declaration, persists them, and enqueues a reconcile task.
func (s *Postgres) UpdateStartup(
	serverID string,
	updates map[string]any,
	expectedGeneration int64,
	idempotencyKey string,
	actor domain.User,
) (domain.Operation, error) {
	if len(updates) == 0 {
		return domain.Operation{}, domain.NewProblem("VALIDATION_FAILED", "variables 至少需要包含一个启动变量", false)
	}
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return domain.Operation{}, err
	}
	digest := requestDigest(struct {
		Generation int64          `json:"generation"`
		Variables  map[string]any `json:"variables"`
	}{Generation: expectedGeneration, Variables: canonicalStartupDigestVariables(updates)})
	taskType := "reconcile"
	scope := taskIdempotencyScope(taskType, actor.ID, idempotencyKey)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	currentActor, err := s.authorizeServer(ctx, actor.ID, serverID, "servers.startup.write")
	if err != nil {
		return domain.Operation{}, err
	}
	if existing, ok, err := s.lookupIdempotentTask(ctx, scope, idempotencyKey, digest); err != nil || ok {
		return existing, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	row, err := s.lockServerRow(ctx, tx, serverID)
	if err == sql.ErrNoRows {
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法查询服务器", true)
	}
	if err := s.requireServerReconcileCapabilityTx(ctx, tx, row.NodeID); err != nil {
		return domain.Operation{}, err
	}
	if err := generationFence(row, expectedGeneration); err != nil {
		return domain.Operation{}, err
	}
	if active, ok, err := s.activeTaskInTx(ctx, tx, serverID); err != nil {
		return domain.Operation{}, err
	} else if ok {
		return domain.Operation{}, operationInProgress(active)
	}
	if row.Lifecycle != "ready" {
		return domain.Operation{}, domain.NewProblem("OPERATION_IN_PROGRESS", "服务器仍在完成生命周期操作", true)
	}

	server := domain.Server{
		ID: row.ID, GameID: row.GameID, GameBundleDigest: row.GameBundleDigest,
		GameDefinitionVersion: row.GameDefinitionVersion, NodeID: row.NodeID, Generation: row.Generation,
	}
	game, err := fixedCatalogGame(row.GameID)
	if err != nil {
		return domain.Operation{}, err
	}
	startup, _, err := startupFromFixedBundle(server, game, nil)
	if err != nil {
		return domain.Operation{}, err
	}
	definitions := make(map[string]domain.StartupVariable, len(startup.Variables))
	for _, variable := range startup.Variables {
		definitions[variable.Key] = variable
	}
	normalized, cleared, err := normalizeStartupUpdates(definitions, updates)
	if err != nil {
		return domain.Operation{}, err
	}

	values, err := s.startupValuesTx(ctx, tx, serverID)
	if err != nil {
		return domain.Operation{}, err
	}
	if err := s.decryptSecretValues(startup.Variables, values); err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法解密启动变量", true)
	}
	for key := range cleared {
		delete(values, key)
	}
	for key, value := range normalized {
		values[key] = value
	}
	// Secret 变量值静态加密后落库；已加密的值保持不变，明文旧数据就地升级。
	if err := s.encryptSecretValues(startup.Variables, values); err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法加密启动变量", true)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
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

	now := time.Now().UTC()
	nextGeneration := row.Generation + 1
	if _, err := tx.ExecContext(ctx, `
		UPDATE servers SET generation = generation + 1, updated_at = now() WHERE id = $1
	`, serverID); err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法推进服务器 generation", true)
	}

	operationID := id.New()
	return s.commitWriteTask(ctx, tx, scope, idempotencyKey, digest, operationID, serverID, row.NodeID, taskType,
		nextGeneration, actor.ID, currentActor, "server.startup.update", now, "")
}

// normalizeStartupUpdates validates the update map against the declared
// variables, mirroring the in-memory adapter's closed-object subset rules.
func normalizeStartupUpdates(definitions map[string]domain.StartupVariable, updates map[string]any) (map[string]any, map[string]bool, error) {
	normalized := make(map[string]any, len(updates))
	cleared := make(map[string]bool, len(updates))
	for key, value := range updates {
		definition, declared := definitions[key]
		if !declared {
			return nil, nil, validationProblem("未声明的启动变量: " + key)
		}
		if value == nil {
			if definition.Required {
				return nil, nil, validationProblem("必填启动变量不能清除: " + key)
			}
			cleared[key] = true
			continue
		}
		normalizedValue, err := normalizeStartupValue(definition, value)
		if err != nil {
			return nil, nil, err
		}
		normalized[key] = normalizedValue
	}
	return normalized, cleared, nil
}

// Console returns the buffered console lines of a server. 生产链路中日志由
// Agent 的 LogBatch 帧经 RecordConsoleLines 写入内存缓冲；无上报时为空。
func (s *Postgres) Console(serverID string) ([]domain.ConsoleLine, error) {
	if _, err := s.Server(serverID); err != nil {
		return nil, err
	}
	buf := s.consoleBufferFor(serverID)
	buf.mu.Lock()
	defer buf.mu.Unlock()
	return append([]domain.ConsoleLine(nil), buf.lines...), nil
}

// SubscribeConsoleLines 订阅服务器实时控制台日志（WebSocket 推送用）。
// 返回接收 channel 与取消函数；取消后 channel 关闭，不能再投递。
func (s *Postgres) SubscribeConsoleLines(serverID string) (<-chan domain.ConsoleLine, func()) {
	return s.consoleHub.Subscribe(serverID)
}

// SendConsoleCommand validates the command and records it as an audit event.
// The agent console stream is out of scope for this stage.
func (s *Postgres) SendConsoleCommand(serverID string, command string, actor domain.User) error {
	command = strings.TrimSpace(command)
	if command == "" || len([]rune(command)) > 512 {
		return domain.NewProblem("VALIDATION_FAILED", "命令不能为空且不能超过 512 个字符", false)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	currentActor, err := s.authorizeServer(ctx, actor.ID, serverID, "servers.console")
	if err != nil {
		return err
	}
	var name, observedPower string
	err = s.db.QueryRowContext(ctx, `
		SELECT name, observed_power FROM servers WHERE id = $1 AND deleted_at IS NULL
	`, serverID).Scan(&name, &observedPower)
	if err == sql.ErrNoRows {
		return domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法查询服务器", true)
	}
	if observedPower != "running" {
		return domain.NewProblem("OPERATION_CONFLICT", "服务器未运行，无法发送控制台命令", false)
	}
	if err := s.recordAudit(ctx, s.db, currentActor, "console.command", "server", serverID, "accepted", id.New()); err != nil {
		return err
	}
	return nil
}

// RecordConsoleCommandResult records only the terminal result confirmed by the
// Agent. Command text and runtime output are intentionally excluded.
func (s *Postgres) RecordConsoleCommandResult(serverID string, actor domain.User, result domain.ConsoleCommandResult) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	currentActor, err := s.authorizeServer(ctx, actor.ID, serverID, "servers.console")
	if err != nil {
		return err
	}
	auditResult := "failure"
	if result.Succeeded {
		auditResult = "success"
	}
	return s.recordAudit(ctx, s.db, currentActor, "console.command.result", "server", serverID, auditResult, result.RequestID)
}

// recordAudit records an audit event at connection level.
func (s *Postgres) recordAudit(ctx context.Context, db *sql.DB, actor domain.User, action string, targetType string, targetID string, result string, operationID string) error {
	var actorID any
	if actor.ID != "" {
		actorID = actor.ID
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO audit_events (actor_id, actor_type, action, target_type, target_id, result, operation_id, trace_id, created_at)
		VALUES ($1, 'user', $2, $3, $4, $5, $6, $7, now())
	`, actorID, action, targetType, targetID, result, operationID, id.New())
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法记录审计日志", true)
	}
	return nil
}

// serverFileSystem lazily initializes the per-server restricted filesystem
// below fileRoot. The in-memory adapter pre-creates these at startup; here the
// directory appears on first access.
func (s *Postgres) serverFileSystem(serverID string) (*serverfiles.ServerFS, error) {
	if strings.TrimSpace(s.fileRoot) == "" {
		return nil, domain.NewProblem("INTERNAL_ERROR", "服务器数据目录未配置", true)
	}
	s.mu.RLock()
	cached := s.fileSystems[serverID]
	s.mu.RUnlock()
	if cached != nil {
		return cached, nil
	}
	root := filepath.Join(s.fileRoot, serverID)
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, domain.NewProblem("INTERNAL_ERROR", "无法创建服务器数据目录", true)
	}
	filesystem, err := serverfiles.NewServerFS(root, serverfiles.Limits{MaxReadBytes: developmentMaxReadBytes, MaxWriteBytes: developmentMaxWriteBytes})
	if err != nil {
		return nil, domain.NewProblem("INTERNAL_ERROR", "无法初始化服务器数据目录", true)
	}
	s.mu.Lock()
	s.fileSystems[serverID] = filesystem
	s.mu.Unlock()
	return filesystem, nil
}

// getFileDispatcher 返回当前注入的远程文件调度器（线程安全）。
func (s *Postgres) getFileDispatcher() FileDispatcher {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fileDispatcher
}

// fileOpTimeout 是单次远程文件操作的总超时，包含网络往返与容器内执行。
const fileOpTimeout = 30 * time.Second

// downloadBackupTimeout 覆盖备份下载的完整传输窗口；备份归档可能接近 gRPC
// 512MiB 消息上限，需要比常规文件操作更宽裕的超时。
const downloadBackupTimeout = 10 * time.Minute

// mapAgentFileError 将 Agent 返回的稳定错误码映射为 domain.Problem。
func mapAgentFileError(errorCode string) error {
	switch errorCode {
	case "NOT_FOUND":
		return domain.NewProblem("NOT_FOUND", "文件或目录不存在", false)
	case "FORBIDDEN":
		return domain.NewProblem("FORBIDDEN", "文件操作被拒绝", false)
	case "VALIDATION_FAILED":
		return domain.NewProblem("VALIDATION_FAILED", "文件路径或参数无效", false)
	case "PATH_ESCAPE":
		return domain.NewProblem("PATH_ESCAPE_BLOCKED", "文件路径不安全", false)
	case "SIZE_LIMIT":
		return domain.NewProblem("VALIDATION_FAILED", "文件大小超过操作限制", false)
	case "RUNTIME_UNAVAILABLE":
		return domain.NewProblem("NODE_OFFLINE", "目标节点未连接或运行时不可用", true)
	case "TIMEOUT":
		return domain.NewProblem("GATEWAY_TIMEOUT", "文件操作超时", true)
	default:
		return domain.NewProblem("INTERNAL_ERROR", "文件操作失败", true)
	}
}

// errorCodeFromDispatcher 从 FileDispatcher 返回的错误中提取 Agent 错误码。
// 上下文超时/取消映射为 TIMEOUT/CANCELLED；AgentFileError 携带其 Code；
// 其余错误归为 INTERNAL_ERROR。
func errorCodeFromDispatcher(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "TIMEOUT"
	}
	if errors.Is(err, context.Canceled) {
		return "CANCELLED"
	}
	var afe *AgentFileError
	if errors.As(err, &afe) {
		return afe.Code
	}
	return "INTERNAL_ERROR"
}

// beginFileMutation holds the per-server mutation gate while the actor is
// authorized and the remote file operation is dispatched. It mirrors the
// in-memory adapter's restore/file mutation exclusion.
func (s *Postgres) beginFileMutation(serverID string, actorID string) (domain.Server, domain.User, FileDispatcher, func(), error) {
	gate, _ := s.fileMutationGates.LoadOrStore(serverID, &sync.RWMutex{})
	gate.(*sync.RWMutex).Lock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	actor, err := s.authorizeServer(ctx, actorID, serverID, "servers.files.write")
	if err != nil {
		gate.(*sync.RWMutex).Unlock()
		return domain.Server{}, domain.User{}, nil, nil, err
	}
	server, err := s.Server(serverID)
	if err != nil {
		gate.(*sync.RWMutex).Unlock()
		return domain.Server{}, domain.User{}, nil, nil, err
	}
	var restoring bool
	if err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM server_tasks
			WHERE server_id = $1 AND task_type = 'restore'
			  AND status IN ('queued', 'leased', 'running')
		)
	`, serverID).Scan(&restoring); err != nil {
		gate.(*sync.RWMutex).Unlock()
		return domain.Server{}, domain.User{}, nil, nil, domain.NewProblem("INTERNAL_ERROR", "无法查询恢复任务", true)
	}
	if restoring {
		gate.(*sync.RWMutex).Unlock()
		active, _ := s.activeRestoreOperation(ctx, serverID)
		return domain.Server{}, domain.User{}, nil, nil, operationInProgress(active)
	}
	fd := s.getFileDispatcher()
	if fd == nil {
		gate.(*sync.RWMutex).Unlock()
		return domain.Server{}, domain.User{}, nil, nil, domain.NewProblem("INTERNAL_ERROR", "文件调度器未配置", true)
	}
	if server.NodeID == "" {
		gate.(*sync.RWMutex).Unlock()
		return domain.Server{}, domain.User{}, nil, nil, domain.NewProblem("VALIDATION_FAILED", "服务器尚未分配到节点", false)
	}
	release := func() {
		gate.(*sync.RWMutex).Unlock()
	}
	return server, actor, fd, release, nil
}

func (s *Postgres) activeRestoreOperation(ctx context.Context, serverID string) (domain.Operation, bool) {
	operation, err := operationFromTask(s.db.QueryRowContext(ctx, taskSelect+`
		WHERE st.server_id = $1 AND st.task_type = 'restore'
		  AND st.status IN ('queued', 'leased', 'running')
		ORDER BY st.created_at
		LIMIT 1
	`, serverID))
	if err != nil {
		return domain.Operation{}, false
	}
	return operation, true
}

// Files lists the immediate children of requestedPath via the Agent.
func (s *Postgres) Files(serverID string, requestedPath string) ([]domain.FileEntry, error) {
	server, err := s.Server(serverID)
	if err != nil {
		return nil, err
	}
	fd := s.getFileDispatcher()
	if fd == nil {
		return nil, domain.NewProblem("INTERNAL_ERROR", "文件调度器未配置", true)
	}
	if server.NodeID == "" {
		return nil, domain.NewProblem("VALIDATION_FAILED", "服务器尚未分配到节点", false)
	}
	ctx, cancel := context.WithTimeout(context.Background(), fileOpTimeout)
	defer cancel()
	entries, err := fd.ListFiles(ctx, server.NodeID, serverID, requestedPath)
	if err != nil {
		return nil, mapAgentFileError(errorCodeFromDispatcher(err))
	}
	return entries, nil
}

// ReadFile reads one regular file inside the server's container /data directory.
func (s *Postgres) ReadFile(serverID string, requestedPath string) (domain.FileContent, error) {
	server, err := s.Server(serverID)
	if err != nil {
		return domain.FileContent{}, err
	}
	fd := s.getFileDispatcher()
	if fd == nil {
		return domain.FileContent{}, domain.NewProblem("INTERNAL_ERROR", "文件调度器未配置", true)
	}
	if server.NodeID == "" {
		return domain.FileContent{}, domain.NewProblem("VALIDATION_FAILED", "服务器尚未分配到节点", false)
	}
	ctx, cancel := context.WithTimeout(context.Background(), fileOpTimeout)
	defer cancel()
	content, err := fd.ReadFile(ctx, server.NodeID, serverID, requestedPath)
	if err != nil {
		return domain.FileContent{}, mapAgentFileError(errorCodeFromDispatcher(err))
	}
	return content, nil
}

// WriteFile writes one file inside the server's container /data directory.
func (s *Postgres) WriteFile(serverID string, requestedPath string, content []byte, actor domain.User) error {
	server, currentActor, fd, release, err := s.beginFileMutation(serverID, actor.ID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), fileOpTimeout)
	defer cancel()
	if err := fd.WriteFile(ctx, server.NodeID, serverID, requestedPath, content, false); err != nil {
		release()
		return mapAgentFileError(errorCodeFromDispatcher(err))
	}
	release()
	if err := s.recordAudit(context.Background(), s.db, currentActor, "file.write", "server", server.Name, "success", id.New()); err != nil {
		return err
	}
	return nil
}

// CreateDirectory creates one directory inside the server's container /data.
func (s *Postgres) CreateDirectory(serverID string, requestedPath string, actor domain.User) error {
	server, currentActor, fd, release, err := s.beginFileMutation(serverID, actor.ID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), fileOpTimeout)
	defer cancel()
	if err := fd.MakeDirectory(ctx, server.NodeID, serverID, requestedPath); err != nil {
		release()
		return mapAgentFileError(errorCodeFromDispatcher(err))
	}
	release()
	if err := s.recordAudit(context.Background(), s.db, currentActor, "file.mkdir", "server", server.Name, "success", id.New()); err != nil {
		return err
	}
	return nil
}

// MoveFile moves or renames a file inside the server's container /data.
func (s *Postgres) MoveFile(serverID string, source string, destination string, replace bool, actor domain.User) error {
	server, currentActor, fd, release, err := s.beginFileMutation(serverID, actor.ID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), fileOpTimeout)
	defer cancel()
	if err := fd.MoveFile(ctx, server.NodeID, serverID, source, destination, replace); err != nil {
		release()
		return mapAgentFileError(errorCodeFromDispatcher(err))
	}
	release()
	if err := s.recordAudit(context.Background(), s.db, currentActor, "file.move", "server", server.Name, "success", id.New()); err != nil {
		return err
	}
	return nil
}

// DeleteFile removes a file (or directory tree when recursive) in the container.
func (s *Postgres) DeleteFile(serverID string, requestedPath string, recursive bool, actor domain.User) error {
	server, currentActor, fd, release, err := s.beginFileMutation(serverID, actor.ID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), fileOpTimeout)
	defer cancel()
	if err := fd.RemoveFile(ctx, server.NodeID, serverID, requestedPath, recursive); err != nil {
		release()
		return mapAgentFileError(errorCodeFromDispatcher(err))
	}
	release()
	if err := s.recordAudit(context.Background(), s.db, currentActor, "file.delete", "server", server.Name, "success", id.New()); err != nil {
		return err
	}
	return nil
}

// Backups lists the non-deleted backups of a server, newest first.
func (s *Postgres) Backups(serverID string) ([]domain.Backup, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var serverExists bool
	if err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM servers WHERE id = $1 AND deleted_at IS NULL)
	`, serverID).Scan(&serverExists); err != nil {
		return nil, domain.NewProblem("INTERNAL_ERROR", "无法查询服务器", true)
	}
	if !serverExists {
		return nil, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, name, status, size_bytes, content_digest, manifest_digest, storage_location, retention_until, created_at, completed_at,
		       failure_code, failure_message, deleted_at
		FROM backups
		WHERE server_id = $1 AND status <> 'deleted'
		ORDER BY created_at DESC
	`, serverID)
	if err != nil {
		return nil, domain.NewProblem("INTERNAL_ERROR", "无法查询备份", true)
	}
	defer rows.Close()

	backups := []domain.Backup{}
	for rows.Next() {
		var backup domain.Backup
		var sizeBytes sql.NullInt64
		var checksum, manifestDigest, storageLocation sql.NullString
		var retentionUntil, completedAt, deletedAt sql.NullTime
		var failureCode, failureMessage sql.NullString
		if err := rows.Scan(&backup.ID, &backup.Name, &backup.Status, &sizeBytes,
			&checksum, &manifestDigest, &storageLocation, &retentionUntil, &backup.CreatedAt, &completedAt,
			&failureCode, &failureMessage, &deletedAt); err != nil {
			continue
		}
		if sizeBytes.Valid {
			backup.SizeBytes = valuePointer(sizeBytes.Int64)
		}
		if checksum.Valid {
			backup.Checksum = valuePointer(checksum.String)
		}
		if manifestDigest.Valid {
			backup.ManifestDigest = valuePointer(manifestDigest.String)
		}
		if storageLocation.Valid {
			backup.StorageLocation = valuePointer(storageLocation.String)
		}
		if retentionUntil.Valid {
			backup.RetentionUntil = valuePointer(retentionUntil.Time)
		}
		if completedAt.Valid {
			backup.CompletedAt = valuePointer(completedAt.Time)
		}
		if failureCode.Valid {
			backup.FailureCode = valuePointer(failureCode.String)
		}
		if failureMessage.Valid {
			backup.FailureMessage = valuePointer(failureMessage.String)
		}
		if deletedAt.Valid {
			backup.DeletedAt = valuePointer(deletedAt.Time)
		}
		backups = append(backups, backup)
	}
	return backups, nil
}

// CreateBackup records backup metadata (creating) and enqueues a backup task.
func (s *Postgres) CreateBackup(serverID string, idempotencyKey string, actor domain.User) (domain.Operation, error) {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return domain.Operation{}, err
	}
	digest := requestDigest(struct{}{})
	taskType := "backup"
	scope := taskIdempotencyScope(taskType, actor.ID, idempotencyKey)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	currentActor, err := s.authorizeServer(ctx, actor.ID, serverID, "servers.backups.create")
	if err != nil {
		return domain.Operation{}, err
	}
	if existing, ok, err := s.lookupIdempotentTask(ctx, scope, idempotencyKey, digest); err != nil || ok {
		return existing, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	row, err := s.lockServerRow(ctx, tx, serverID)
	if err == sql.ErrNoRows {
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法查询服务器", true)
	}
	if row.NodeCondition != "available" {
		return domain.Operation{}, domain.NewProblem("NODE_OFFLINE", "节点当前离线，无法创建备份", true)
	}
	if row.Lifecycle != "ready" {
		return domain.Operation{}, domain.NewProblem("RESTORE_LOCKED", "服务器当前不可创建备份", false)
	}
	if active, ok, err := s.activeTaskInTx(ctx, tx, serverID); err != nil {
		return domain.Operation{}, err
	} else if ok {
		if active.Type == domain.PowerAction(taskType) {
			return active, nil
		}
		return domain.Operation{}, operationInProgress(active)
	}

	now := time.Now().UTC()
	backupID := id.New()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO backups (id, server_id, creator_id, name, status, format_version, game_bundle_digest, created_at)
		VALUES ($1, $2, $3, $4, 'creating', 'v1', $5, $6)
	`, backupID, serverID, actor.ID, "manual-"+now.Format("20060102-150405"), row.GameBundleDigest, now); err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法记录备份元数据", true)
	}

	// 把备份任务输入写入 task_input（000009 起与 checkpoint 分离），
	// Agent claim 后以其执行 docker exec tar。
	taskInput, marshalErr := json.Marshal(backupTaskPayload{
		BackupID:         backupID,
		FormatVersion:    "v1",
		StorageObjectKey: "backups/" + backupID + ".tar.gz",
	})
	if marshalErr != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法生成任务负载", true)
	}

	operationID := id.New()
	// Backups do not advance the server generation; they snapshot current state.
	return s.commitWriteTask(ctx, tx, scope, idempotencyKey, digest, operationID, serverID, row.NodeID, taskType,
		row.Generation, actor.ID, currentActor, "backup.create", now, string(taskInput))
}

// RestoreBackup validates the backup integrity and server state, advances the
// generation, marks the backup restoring, and enqueues a restore task.
func (s *Postgres) RestoreBackup(serverID string, backupID string, idempotencyKey string, actor domain.User) (domain.Operation, error) {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return domain.Operation{}, err
	}
	digest := requestDigest(struct {
		BackupID string `json:"backupId"`
	}{BackupID: backupID})
	taskType := "restore"
	scope := taskIdempotencyScope(taskType, actor.ID, idempotencyKey)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	currentActor, err := s.authorizeServer(ctx, actor.ID, serverID, "servers.backups.restore")
	if err != nil {
		return domain.Operation{}, err
	}
	if existing, ok, err := s.lookupIdempotentTask(ctx, scope, idempotencyKey, digest); err != nil || ok {
		return existing, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	row, err := s.lockServerRow(ctx, tx, serverID)
	if err == sql.ErrNoRows {
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法查询服务器", true)
	}
	if row.NodeCondition != "available" {
		return domain.Operation{}, domain.NewProblem("NODE_OFFLINE", "节点当前离线，无法恢复备份", true)
	}

	var backupStatus, contentDigest, manifestDigest, storageLocation string
	err = tx.QueryRowContext(ctx, `
		SELECT status, COALESCE(content_digest, ''), COALESCE(manifest_digest, ''), COALESCE(storage_location, '')
		FROM backups WHERE id = $1 AND server_id = $2
		FOR UPDATE
	`, backupID, serverID).Scan(&backupStatus, &contentDigest, &manifestDigest, &storageLocation)
	if err == sql.ErrNoRows || backupStatus == "deleted" {
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "备份不存在", false)
	}
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法查询备份", true)
	}
	if row.Lifecycle != "ready" || row.Observed != "stopped" {
		return domain.Operation{}, domain.NewProblem("RESTORE_LOCKED", "恢复备份前必须停止服务器", false)
	}
	if backupStatus != "ready" {
		return domain.Operation{}, domain.NewProblem("RESTORE_LOCKED", "备份当前不可恢复", false)
	}
	if !backupChecksumValid(contentDigest) {
		return domain.Operation{}, domain.NewProblem("BACKUP_INTEGRITY_FAILED", "备份摘要校验失败", false)
	}
	if active, ok, err := s.activeTaskInTx(ctx, tx, serverID); err != nil {
		return domain.Operation{}, err
	} else if ok {
		return domain.Operation{}, operationInProgress(active)
	}

	now := time.Now().UTC()
	nextGeneration := row.Generation + 1
	if _, err := tx.ExecContext(ctx, `
		UPDATE servers
		SET generation = generation + 1, desired_power = 'stopped', updated_at = now()
		WHERE id = $1
	`, serverID); err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法推进服务器 generation", true)
	}
	transition, err := tx.ExecContext(ctx, `
		UPDATE backups SET status = 'restoring'
		WHERE id = $1 AND server_id = $2 AND status = 'ready'
	`, backupID, serverID)
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法更新备份状态", true)
	}
	if err := requireBackupTransition(transition, "ready", "restoring"); err != nil {
		return domain.Operation{}, err
	}

	operationID := id.New()
	// 恢复任务输入：storageObjectKey 指向备份归档在节点上的相对路径。
	taskInput, marshalErr := json.Marshal(backupTaskPayload{
		BackupID:               backupID,
		StorageObjectKey:       backupObjectKey(backupID, storageLocation),
		ExpectedManifestDigest: manifestDigest,
		ExpectedContentDigest:  contentDigest,
	})
	if marshalErr != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法生成任务负载", true)
	}
	return s.commitWriteTask(ctx, tx, scope, idempotencyKey, digest, operationID, serverID, row.NodeID, taskType,
		nextGeneration, actor.ID, currentActor, "backup.restore", now, string(taskInput))
}

// backupObjectKey 返回备份归档在节点备份目录下的相对路径；storageLocation
// 已写入时沿用，否则按 <backupID>.tar.gz 推导。
func backupObjectKey(backupID, storageLocation string) string {
	if storageLocation != "" {
		return storageLocation
	}
	return "backups/" + backupID + ".tar.gz"
}

// DeleteBackup marks a ready backup deleting and enqueues a backup-delete task.
func (s *Postgres) DeleteBackup(serverID string, backupID string, idempotencyKey string, actor domain.User) (domain.Operation, error) {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return domain.Operation{}, err
	}
	digest := requestDigest(struct {
		BackupID string `json:"backupId"`
	}{BackupID: backupID})
	taskType := "backup-delete"
	scope := taskIdempotencyScope(taskType, actor.ID, idempotencyKey)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	currentActor, err := s.authorizeServer(ctx, actor.ID, serverID, "servers.backups.delete")
	if err != nil {
		return domain.Operation{}, err
	}
	if existing, ok, err := s.lookupIdempotentTask(ctx, scope, idempotencyKey, digest); err != nil || ok {
		return existing, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	row, err := s.lockServerRow(ctx, tx, serverID)
	if err == sql.ErrNoRows {
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法查询服务器", true)
	}

	var backupStatus, storageLocation string
	err = tx.QueryRowContext(ctx, `
		SELECT status, COALESCE(storage_location, '') FROM backups WHERE id = $1 AND server_id = $2
		FOR UPDATE
	`, backupID, serverID).Scan(&backupStatus, &storageLocation)
	if err == sql.ErrNoRows || backupStatus == "deleted" {
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "备份不存在", false)
	}
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法查询备份", true)
	}
	if backupStatus != "ready" && backupStatus != "failed" {
		return domain.Operation{}, domain.NewProblem("RESTORE_LOCKED", "备份当前不可删除", false)
	}
	if active, ok, err := s.activeTaskInTx(ctx, tx, serverID); err != nil {
		return domain.Operation{}, err
	} else if ok {
		return domain.Operation{}, operationInProgress(active)
	}

	now := time.Now().UTC()
	transition, err := tx.ExecContext(ctx, `
		UPDATE backups
		SET status = 'deleting',
		    failure_code = CASE WHEN $3 = 'failed' THEN failure_code ELSE NULL END,
		    failure_message = CASE WHEN $3 = 'failed' THEN failure_message ELSE NULL END
		WHERE id = $1 AND server_id = $2 AND status = $3
	`, backupID, serverID, backupStatus)
	if err != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法更新备份状态", true)
	}
	if err := requireBackupTransition(transition, backupStatus, "deleting"); err != nil {
		return domain.Operation{}, err
	}

	operationID := id.New()
	// 删除任务输入：告知 Agent 归档相对路径并请求删除远端对象。
	taskInput, marshalErr := json.Marshal(backupTaskPayload{
		BackupID:           backupID,
		StorageObjectKey:   backupObjectKey(backupID, storageLocation),
		DeleteRemoteObject: true,
	})
	if marshalErr != nil {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法生成任务负载", true)
	}
	return s.commitWriteTask(ctx, tx, scope, idempotencyKey, digest, operationID, serverID, row.NodeID, taskType,
		row.Generation, actor.ID, currentActor, "backup.delete", now, string(taskInput))
}

// DownloadBackup 校验备份就绪且节点在线后，从目标节点读取备份归档内容。
// 下载是只读操作，不推进 generation、不占用互斥操作槽位。
func (s *Postgres) DownloadBackup(serverID string, backupID string, actor domain.User) (domain.BackupContent, error) {
	ctx, cancel := context.WithTimeout(context.Background(), downloadBackupTimeout)
	defer cancel()

	if _, err := s.authorizeServer(ctx, actor.ID, serverID, "servers.backups.read"); err != nil {
		return domain.BackupContent{}, err
	}

	var nodeID, nodeCondition string
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(s.node_id::text, ''), COALESCE(n.condition, 'offline')
		FROM servers s
		LEFT JOIN nodes n ON n.id = s.node_id
		WHERE s.id = $1 AND s.deleted_at IS NULL
	`, serverID).Scan(&nodeID, &nodeCondition); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.BackupContent{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
		}
		return domain.BackupContent{}, domain.NewProblem("INTERNAL_ERROR", "无法查询服务器", true)
	}
	if nodeCondition != "available" {
		return domain.BackupContent{}, domain.NewProblem("NODE_OFFLINE", "节点当前离线，无法下载备份", true)
	}
	if nodeID == "" {
		return domain.BackupContent{}, domain.NewProblem("VALIDATION_FAILED", "服务器尚未分配到节点", false)
	}

	var backupStatus, expectedChecksum string
	var expectedSize sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `
		SELECT status, COALESCE(content_digest, ''), size_bytes
		FROM backups WHERE id = $1 AND server_id = $2
	`, backupID, serverID).Scan(&backupStatus, &expectedChecksum, &expectedSize); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.BackupContent{}, domain.NewProblem("NOT_FOUND", "备份不存在", false)
		}
		return domain.BackupContent{}, domain.NewProblem("INTERNAL_ERROR", "无法查询备份", true)
	}
	if backupStatus == "deleted" {
		return domain.BackupContent{}, domain.NewProblem("NOT_FOUND", "备份不存在", false)
	}
	if backupStatus != "ready" {
		return domain.BackupContent{}, domain.NewProblem("RESTORE_LOCKED", "备份当前不可下载", false)
	}
	if !backupChecksumValid(expectedChecksum) || !expectedSize.Valid || expectedSize.Int64 < 0 {
		return domain.BackupContent{}, domain.NewProblem("BACKUP_INTEGRITY_FAILED", "备份完整性元数据无效", false)
	}

	fd := s.getFileDispatcher()
	if fd == nil {
		return domain.BackupContent{}, domain.NewProblem("INTERNAL_ERROR", "文件调度器未配置", true)
	}
	content, err := fd.DownloadBackup(ctx, nodeID, serverID, backupID)
	if err != nil {
		return domain.BackupContent{}, mapAgentFileError(errorCodeFromDispatcher(err))
	}
	payload := content.Content
	if content.Base64 {
		decoded, decodeErr := base64.StdEncoding.DecodeString(string(payload))
		if decodeErr != nil {
			return domain.BackupContent{}, domain.NewProblem("BACKUP_INTEGRITY_FAILED", "备份归档编码无效", false)
		}
		payload = decoded
	}
	if int64(len(payload)) != expectedSize.Int64 || content.SizeBytes != expectedSize.Int64 {
		return domain.BackupContent{}, domain.NewProblem("BACKUP_INTEGRITY_FAILED", "备份归档大小与记录不一致", false)
	}
	digest := sha256.Sum256(payload)
	actualChecksum := "sha256:" + hex.EncodeToString(digest[:])
	if actualChecksum != expectedChecksum {
		return domain.BackupContent{}, domain.NewProblem("BACKUP_INTEGRITY_FAILED", "备份归档摘要与记录不一致", false)
	}
	return content, nil
}

// backupChecksumValid mirrors the in-memory adapter: a checksum is only valid
// when it is a well-formed sha256: digest.
func backupChecksumValid(checksum string) bool {
	if checksum == "" || !strings.HasPrefix(checksum, "sha256:") {
		return false
	}
	digest := strings.TrimPrefix(checksum, "sha256:")
	if len(digest) != 64 {
		return false
	}
	for _, char := range digest {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

// Heartbeat refreshes a node's heartbeat metadata by name. Missing or revoked
// nodes map to NOT_FOUND, mirroring the in-memory adapter.
func (s *Postgres) Heartbeat(nodeName string, agentVersion string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := s.db.ExecContext(ctx, `
		UPDATE nodes
		SET last_heartbeat_at = now(),
		    agent_version = CASE WHEN $2 <> '' THEN $2 ELSE agent_version END,
		    condition = CASE WHEN condition = 'offline' THEN 'available' ELSE condition END,
		    updated_at = now()
		WHERE name = $1 AND revoked_at IS NULL
	`, nodeName, agentVersion)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法记录节点心跳", true)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return domain.NewProblem("NOT_FOUND", "节点不存在", false)
	}
	return nil
}
