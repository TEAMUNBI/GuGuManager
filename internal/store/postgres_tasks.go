package store

import (
	"bytes"
	"context"
	"database/sql"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/id"
)

// ClaimedTask is the unit of work handed to a node agent. OperationID is the
// server_tasks.id and doubles as the operation id shown to the control plane.
type ClaimedTask struct {
	OperationID string
	ServerID    string
	NodeID      string
	Generation  int64
	TaskType    string
	Attempt     int
	PayloadJSON []byte
}

const taskLeaseDuration = 30 * time.Second

// taskIdempotencyScope builds the idempotency scope stored in server_tasks.
// PostgreSQL text values cannot contain NUL bytes, so the unit separator
// (\x1f) is used instead of the in-memory adapter's \x00 join.
func taskIdempotencyScope(taskType string, actorID string, idemKey string) string {
	return "server:" + taskType + "\x1f" + actorID + "\x1f" + idemKey
}

// EnqueueTask records a queued server task. The (idempotency_scope,
// idempotency_key) pair is unique: replaying the same key with the same
// request digest returns the original task id, while a mismatched digest maps
// to IDEMPOTENCY_KEY_REUSED.
func (s *Postgres) EnqueueTask(ctx context.Context, serverID, nodeID, taskType string, generation int64, actorID string, idemKey string, requestDigest []byte) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	scope := taskIdempotencyScope(taskType, actorID, idemKey)
	taskID := id.New()
	now := time.Now().UTC()

	var actorIDValue any
	if actorID != "" {
		actorIDValue = actorID
	}

	var inserted string
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO server_tasks (
			id, server_id, node_id, task_type, status, generation, actor_id,
			idempotency_scope, idempotency_key, request_digest, attempt, max_attempts, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, 'queued', $5, $6, $7, $8, $9, 0, 3, $10, $10)
		ON CONFLICT (idempotency_scope, idempotency_key) DO NOTHING
		RETURNING id::text
	`, taskID, serverID, nodeID, taskType, generation, actorIDValue, scope, idemKey, requestDigest, now).Scan(&inserted)
	if err == nil {
		return inserted, nil
	}
	if err != sql.ErrNoRows {
		return "", domain.NewProblem("INTERNAL_ERROR", "无法创建任务", true)
	}

	// Idempotency replay: a task with the same scope and key already exists.
	var existingID string
	var storedDigest []byte
	err = s.db.QueryRowContext(ctx, `
		SELECT id::text, request_digest FROM server_tasks
		WHERE idempotency_scope = $1 AND idempotency_key = $2
	`, scope, idemKey).Scan(&existingID, &storedDigest)
	if err == sql.ErrNoRows {
		return "", domain.NewProblem("INTERNAL_ERROR", "幂等记录缺失", true)
	}
	if err != nil {
		return "", domain.NewProblem("INTERNAL_ERROR", "无法查询幂等记录", true)
	}
	if !bytes.Equal(storedDigest, requestDigest) {
		return "", domain.NewProblem("IDEMPOTENCY_KEY_REUSED", "幂等键已用于不同的请求内容", false)
	}
	return existingID, nil
}

// ClaimTask hands the oldest queued task for a node to that node, marking it
// leased with a 30-second lease. It returns (nil, nil) when nothing is
// queued. FOR UPDATE SKIP LOCKED keeps concurrent claims from racing.
func (s *Postgres) ClaimTask(ctx context.Context, nodeID string) (*ClaimedTask, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	var task ClaimedTask
	var attempt int
	var payload sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT id::text, server_id::text, node_id::text, task_type, generation, attempt, checkpoint::text
		FROM server_tasks
		WHERE node_id = $1 AND status = 'queued'
		ORDER BY created_at
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`, nodeID).Scan(&task.OperationID, &task.ServerID, &task.NodeID, &task.TaskType, &task.Generation, &attempt, &payload)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, domain.NewProblem("INTERNAL_ERROR", "无法领取任务", true)
	}

	leaseExpiresAt := time.Now().UTC().Add(taskLeaseDuration)
	_, err = tx.ExecContext(ctx, `
		UPDATE server_tasks
		SET status = 'leased', lease_owner = $1, lease_expires_at = $2,
		    attempt = attempt + 1, updated_at = now()
		WHERE id = $3
	`, nodeID, leaseExpiresAt, task.OperationID)
	if err != nil {
		return nil, domain.NewProblem("INTERNAL_ERROR", "无法领取任务", true)
	}
	if err := tx.Commit(); err != nil {
		return nil, domain.NewProblem("INTERNAL_ERROR", "无法提交事务", true)
	}

	task.Attempt = attempt + 1
	if payload.Valid {
		task.PayloadJSON = []byte(payload.String)
	}
	return &task, nil
}

// CompleteTask moves a leased task to its terminal state (succeeded/failed)
// and records the outcome as an audit event.
func (s *Postgres) CompleteTask(ctx context.Context, operationID, nodeID string, succeeded bool, errCode *string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	var serverID string
	err = tx.QueryRowContext(ctx, `
		SELECT server_id::text FROM server_tasks
		WHERE id = $1 AND node_id = $2
		FOR UPDATE
	`, operationID, nodeID).Scan(&serverID)
	if err == sql.ErrNoRows {
		return domain.NewProblem("NOT_FOUND", "任务不存在", false)
	}
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法查询任务", true)
	}

	now := time.Now().UTC()
	status := "failed"
	result := "failure"
	if succeeded {
		status = "succeeded"
		result = "success"
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE server_tasks
		SET status = $1, error_code = $2, completed_at = $3, updated_at = $3
		WHERE id = $4 AND node_id = $5
	`, status, errCode, now, operationID, nodeID)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法完成任务", true)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO audit_events (actor_type, action, target_type, target_id, result, operation_id, trace_id, created_at)
		VALUES ('agent', 'task.complete', 'server', $1, $2, $3, $4, $5)
	`, serverID, result, operationID, id.New(), now)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法记录审计日志", true)
	}

	if err := tx.Commit(); err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法提交事务", true)
	}
	return nil
}

// lookupIdempotentTask resolves a previously recorded server_tasks row by its
// idempotency scope and key, reconstructing the domain.Operation it accepted.
// A digest mismatch means the key was reused with a different request body.
func (s *Postgres) lookupIdempotentTask(ctx context.Context, scope string, key string, digest [32]byte) (domain.Operation, bool, error) {
	var taskID, serverID, nodeID, taskType, status string
	var generation int64
	var attempt int
	var storedDigest []byte
	var createdAt time.Time
	err := s.db.QueryRowContext(ctx, `
		SELECT id::text, server_id::text, node_id::text, task_type, status, generation, attempt, request_digest, created_at
		FROM server_tasks
		WHERE idempotency_scope = $1 AND idempotency_key = $2
	`, scope, key).Scan(&taskID, &serverID, &nodeID, &taskType, &status, &generation, &attempt, &storedDigest, &createdAt)
	if err == sql.ErrNoRows {
		return domain.Operation{}, false, nil
	}
	if err != nil {
		return domain.Operation{}, false, domain.NewProblem("INTERNAL_ERROR", "无法查询幂等记录", true)
	}
	if !bytes.Equal(storedDigest, digest[:]) {
		return domain.Operation{}, false, domain.NewProblem("IDEMPOTENCY_KEY_REUSED", "幂等键已用于不同的请求内容", false)
	}
	operation := domain.NewQueuedOperation(taskID, serverID, nodeID, domain.PowerAction(taskType), generation, key, createdAt)
	operation.Status = status
	if attempt < 1 {
		attempt = 1
	}
	operation.Attempt = attempt
	operation.MaxAttempts = 3
	return operation, true, nil
}
