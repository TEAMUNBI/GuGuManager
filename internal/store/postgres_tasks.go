package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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
		if err := s.recordTaskOutboxEvent(ctx, s.db, "task.created", taskEventPayload{
			OperationID: inserted,
			ServerID:    serverID,
			NodeID:      nodeID,
			TaskType:    taskType,
			Generation:  generation,
			Attempt:     0,
			MaxAttempts: 3,
			Status:      "queued",
		}); err != nil {
			return "", domain.NewProblem("INTERNAL_ERROR", "无法记录任务事件", true)
		}
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
		WHERE node_id = $1 AND status = 'queued' AND attempt < max_attempts
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
// and records the outcome as an audit event. resultJSON carries the agent's
// machine-readable result (e.g. backup checksum) for task-type specific writes.
func (s *Postgres) CompleteTask(ctx context.Context, operationID, nodeID string, succeeded bool, errCode *string, resultJSON []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	var serverID, taskType string
	var taskCheckpoint string
	var taskGeneration int64
	err = tx.QueryRowContext(ctx, `
		SELECT server_id::text, task_type, generation, COALESCE(checkpoint::text, '') FROM server_tasks
		WHERE id = $1 AND node_id = $2
		FOR UPDATE
	`, operationID, nodeID).Scan(&serverID, &taskType, &taskGeneration, &taskCheckpoint)
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
	var errorCodeValue any
	if errCode != nil {
		errorCodeValue = *errCode
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE server_tasks
		SET status = $1, error_code = $2, completed_at = $3, updated_at = $3
		WHERE id = $4 AND node_id = $5
	`, status, errorCodeValue, now, operationID, nodeID)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法完成任务", true)
	}

	// 任务终态与 outbox 事件在同一事务内落库：消费方（发布器）只会看到
	// 已提交的 task.completed，不会出现"任务已成功、事件缺失"的中间态。
	var completedErrorCode string
	if errCode != nil {
		completedErrorCode = *errCode
	}
	if err := s.recordTaskOutboxEvent(ctx, tx, "task.completed", taskEventPayload{
		OperationID: operationID,
		ServerID:    serverID,
		NodeID:      nodeID,
		TaskType:    taskType,
		Generation:  taskGeneration,
		Status:      status,
		ErrorCode:   completedErrorCode,
	}); err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法记录任务事件", true)
	}

	// 成功的 provision 把服务器生命周期推进到 ready，之后才能接收电源操作；
	// 失败的 provision 保持 provisioning 供上层重试/处置。
	if succeeded && taskType == "provision" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE servers
			SET lifecycle_state = 'ready', updated_at = now()
			WHERE id = $1 AND deleted_at IS NULL
		`, serverID); err != nil {
			return domain.NewProblem("INTERNAL_ERROR", "无法更新服务器生命周期", true)
		}
	}

	// 成功的备份任务把 backups 元数据推进到终态：创建 → ready（带校验与位置），
	// 恢复 → 回到 ready，删除 → 标记 deleted。backupId 从 checkpoint 解析，
	// checksum/size/storageLocation 从 resultJSON（Agent 执行回传）解析。
	if succeeded {
		switch taskType {
		case "backup":
			// 只有 Agent 回传了结果（checksum/size/location）才算成功创建；
			// 空 resultJSON 表示没有有效归档，备份保持 creating 供重试/处置。
			if len(resultJSON) == 0 {
				break
			}
			var payload struct {
				BackupID string `json:"backupId"`
			}
			_ = json.Unmarshal([]byte(taskCheckpoint), &payload)
			var res struct {
				Checksum        string `json:"checksum"`
				SizeBytes       int64  `json:"sizeBytes"`
				StorageLocation string `json:"storageLocation"`
			}
			_ = json.Unmarshal(resultJSON, &res)
			if payload.BackupID != "" {
				// content_digest 有 CHECK 约束（sha256: + 64 位 hex 或 NULL），
				// 未回传校验值时落 NULL 而非空串。
				var contentDigest any
				if res.Checksum != "" {
					contentDigest = res.Checksum
				}
				var sizeBytes any
				if res.SizeBytes > 0 {
					sizeBytes = res.SizeBytes
				}
				if _, err := tx.ExecContext(ctx, `
					UPDATE backups
					SET status = 'ready', content_digest = $2, size_bytes = $3, storage_location = $4, completed_at = now()
					WHERE id = $1 AND server_id = $5
				`, payload.BackupID, contentDigest, sizeBytes, res.StorageLocation, serverID); err != nil {
					return domain.NewProblem("INTERNAL_ERROR", "无法更新备份元数据", true)
				}
			}
		case "restore":
			var payload struct {
				BackupID string `json:"backupId"`
			}
			_ = json.Unmarshal([]byte(taskCheckpoint), &payload)
			if payload.BackupID != "" {
				if _, err := tx.ExecContext(ctx, `
					UPDATE backups SET status = 'ready'
					WHERE id = $1 AND server_id = $2
				`, payload.BackupID, serverID); err != nil {
					return domain.NewProblem("INTERNAL_ERROR", "无法更新备份状态", true)
				}
			}
		case "backup-delete":
			var payload struct {
				BackupID string `json:"backupId"`
			}
			_ = json.Unmarshal([]byte(taskCheckpoint), &payload)
			if payload.BackupID != "" {
				if _, err := tx.ExecContext(ctx, `
					UPDATE backups SET status = 'deleted'
					WHERE id = $1 AND server_id = $2
				`, payload.BackupID, serverID); err != nil {
					return domain.NewProblem("INTERNAL_ERROR", "无法更新备份状态", true)
				}
			}
		}
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

// ReconcileTaskLeases 回收过期的任务租约，是多副本恢复的核心对账逻辑：
//
//   - status='leased' 且租约已过期、attempt 未达上限的任务重新入队
//     （清空租约与租约主），等待 Agent 下次 claim 重试；
//   - attempt 已耗尽（>= max_attempts）的任务判为终态失败
//     （error_code='MAX_ATTEMPTS'，不可重试）并写一条系统审计。
//
// 与 ReconcileNodeLiveness 同风格：now 显式传入便于测试确定化。多个副本
// 并发运行安全——UPDATE 逐行原子，重复执行无副作用；audit 只对真正被
// 判失败的行写一次（UPDATE ... RETURNING 返回本次实际变更的行）。
func (s *Postgres) ReconcileTaskLeases(now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 未达重试上限：重新入队，保留 attempt 计数供下一次领取累加。
	if _, err := s.db.ExecContext(ctx, `
		UPDATE server_tasks
		SET status = 'queued', lease_owner = NULL, lease_expires_at = NULL, updated_at = now()
		WHERE status = 'leased' AND lease_expires_at < $1 AND attempt < max_attempts
	`, now); err != nil {
		return
	}

	// 已达重试上限：终态失败并审计。事务内先收集被变更的行，再写入审计，
	// 避免在结果集未关闭时复用同一连接执行插入。
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		UPDATE server_tasks
		SET status = 'failed', error_code = 'MAX_ATTEMPTS', error_retryable = false,
		    completed_at = now(), updated_at = now()
		WHERE status = 'leased' AND lease_expires_at < $1 AND attempt >= max_attempts
		RETURNING id::text, server_id::text
	`, now)
	if err != nil {
		return
	}
	type expiredTask struct {
		operationID string
		serverID    string
	}
	var expired []expiredTask
	for rows.Next() {
		var task expiredTask
		if err := rows.Scan(&task.operationID, &task.serverID); err != nil {
			rows.Close()
			return
		}
		expired = append(expired, task)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return
	}

	for _, task := range expired {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO audit_events (actor_type, action, target_type, target_id, result, operation_id, trace_id, created_at)
			VALUES ('system', 'task.expired', 'server', $1, 'failed', $2, $3, now())
		`, task.serverID, task.operationID, id.New()); err != nil {
			return
		}
	}
	if err := tx.Commit(); err != nil {
		return
	}
}
