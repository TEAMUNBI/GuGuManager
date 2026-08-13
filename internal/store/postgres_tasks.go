package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"path"
	"strings"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/id"
)

// ClaimedTask is the unit of work handed to a node agent. OperationID is the
// server_tasks.id and doubles as the operation id shown to the control plane.
//
// Since migration 000009 the immutable task input lives in server_tasks.task_input
// and the checkpoint column only carries agent progress. Pre-000009 rows still
// carry their input inside checkpoint and are surfaced as PayloadJSON for
// compatibility with the previous stable agent.
type ClaimedTask struct {
	OperationID     string
	ServerID        string
	NodeID          string
	Generation      int64
	TaskType        string
	Attempt         int
	LeaseToken      string
	ConnectionEpoch int64
	StateVersion    int64
	PayloadJSON     []byte // legacy rows: input materialized in checkpoint
	TaskInputJSON   []byte // current rows: input separated from checkpoint
}

// InputJSON returns the task input for dispatch, preferring the separated
// task_input column and falling back to the legacy checkpoint payload.
func (t *ClaimedTask) InputJSON() []byte {
	if len(t.TaskInputJSON) > 0 {
		return t.TaskInputJSON
	}
	return t.PayloadJSON
}

// SetInputJSON writes the materialized input back into the field it came from.
func (t *ClaimedTask) SetInputJSON(encoded []byte) {
	if len(t.TaskInputJSON) > 0 {
		t.TaskInputJSON = encoded
		return
	}
	t.PayloadJSON = encoded
}

// TaskLeaseFence is the exact lease identity a message must present to touch a
// task. Ack/Progress/Renew/Result are no-ops unless every field matches the
// row; stale attempts, dead connections, and late results can never overwrite
// terminal state.
type TaskLeaseFence struct {
	OperationID string
	NodeID      string
	Epoch       int64
	Attempt     int
	LeaseToken  string
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
// leased with a 30-second lease and a fresh random lease token bound to the
// claiming connection epoch. It returns (nil, nil) when nothing is queued.
// FOR UPDATE SKIP LOCKED keeps concurrent claims from racing. The attempt and
// state_version counters advance monotonically on every claim.
func (s *Postgres) ClaimTask(ctx context.Context, nodeID string, connectionEpoch int64) (*ClaimedTask, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	var task ClaimedTask
	var attempt int
	var taskInput sql.NullString
	var checkpoint sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT id::text, server_id::text, node_id::text, task_type, generation, attempt,
		       task_input::text, checkpoint::text
		FROM server_tasks
		WHERE node_id = $1 AND status = 'queued' AND attempt < max_attempts
		ORDER BY created_at
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`, nodeID).Scan(&task.OperationID, &task.ServerID, &task.NodeID, &task.TaskType, &task.Generation, &attempt, &taskInput, &checkpoint)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, domain.NewProblem("INTERNAL_ERROR", "无法领取任务", true)
	}

	if taskInput.Valid {
		task.TaskInputJSON = []byte(taskInput.String)
	} else if checkpoint.Valid {
		task.PayloadJSON = []byte(checkpoint.String)
	}
	if err := s.materializeSecretHandlesTx(ctx, tx, &task); err != nil {
		return nil, domain.NewProblem("SECRET_HANDLE_FAILED", "unable to prepare one-time Secret handles", true)
	}

	task.ConnectionEpoch = connectionEpoch
	leaseExpiresAt := time.Now().UTC().Add(taskLeaseDuration)
	err = tx.QueryRowContext(ctx, `
		UPDATE server_tasks
		SET status = 'leased', lease_owner = $1, lease_token = gen_random_uuid(),
		    connection_epoch = $2, lease_expires_at = $3, lease_renewed_at = now(),
		    attempt = attempt + 1, state_version = state_version + 1, updated_at = now()
		WHERE id = $4 AND status = 'queued'
		RETURNING attempt, state_version, lease_token::text
	`, nodeID, connectionEpoch, leaseExpiresAt, task.OperationID).Scan(&task.Attempt, &task.StateVersion, &task.LeaseToken)
	if err != nil {
		return nil, domain.NewProblem("INTERNAL_ERROR", "无法领取任务", true)
	}
	if err := tx.Commit(); err != nil {
		return nil, domain.NewProblem("INTERNAL_ERROR", "无法提交事务", true)
	}

	return &task, nil
}

// fenceTaskRow locks the task row and verifies the fence. It returns the row's
// current fields for callers that need to apply type-specific transitions.
// The task must be non-terminal and the fence must match exactly; a legacy
// message without a lease token is validated against node + attempt only so
// the previous stable agent keeps working.
func (s *Postgres) fenceTaskRow(ctx context.Context, tx *sql.Tx, fence TaskLeaseFence) (serverID, taskType, taskStatus string, generation, stateVersion, currentAttempt, currentEpoch int64, currentLeaseToken string, taskCheckpoint string, taskInput string, err error) {
	err = tx.QueryRowContext(ctx, `
		SELECT server_id::text, task_type, status, generation, state_version,
		       attempt, connection_epoch, COALESCE(lease_token::text, ''),
		       COALESCE(checkpoint::text, ''), COALESCE(task_input::text, '')
		FROM server_tasks
		WHERE id = $1 AND node_id = $2
		FOR UPDATE
	`, fence.OperationID, fence.NodeID).Scan(&serverID, &taskType, &taskStatus, &generation, &stateVersion, &currentAttempt, &currentEpoch, &currentLeaseToken, &taskCheckpoint, &taskInput)
	if err != nil {
		return
	}
	if !taskFenceMatches(fence, currentAttempt, currentEpoch, currentLeaseToken) {
		// Fence mismatch: the message belongs to a stale attempt, a dead
		// connection epoch, or a revoked lease. Callers map sql.ErrNoRows to
		// a silent no-op; it can never overwrite current or terminal state.
		err = sql.ErrNoRows
		return
	}
	return
}

// taskFenceMatches compares a message fence with the row. Legacy messages
// (empty lease token) only need the attempt to match; fenced messages must
// match epoch, attempt, and lease token exactly.
func taskFenceMatches(fence TaskLeaseFence, attempt int64, epoch int64, leaseToken string) bool {
	if fence.LeaseToken == "" {
		return fence.Attempt == int(attempt)
	}
	return fence.Attempt == int(attempt) && fence.Epoch == epoch && fence.LeaseToken == leaseToken
}

// staleFenceOrNotFound maps a fenced transition result to either a silent
// no-op (the row exists but the fence is stale, or it is already terminal)
// or a real error.
func staleFenceOrNotFound(err error) error {
	if err == nil || err == sql.ErrNoRows {
		return nil
	}
	return domain.NewProblem("INTERNAL_ERROR", "无法更新任务状态", true)
}

// AckTask applies the agent's TaskAck. An accepted ack moves leased -> running
// and renews the lease; a rejected ack fails the task terminally. Stale or
// mismatched fences are no-ops.
func (s *Postgres) AckTask(ctx context.Context, fence TaskLeaseFence, accepted bool, errCode string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	if _, _, _, _, _, _, _, _, _, _, err := s.fenceTaskRow(ctx, tx, fence); err != nil {
		return staleFenceOrNotFound(err)
	}

	var result sql.Result
	if accepted {
		result, err = tx.ExecContext(ctx, `
			UPDATE server_tasks
			SET status = 'running',
			    lease_expires_at = now() + $1::interval,
			    lease_renewed_at = now(),
			    state_version = state_version + 1,
			    updated_at = now()
			WHERE id = $2 AND status = 'leased'
		`, taskLeaseDuration.String(), fence.OperationID)
	} else {
		code := strings.TrimSpace(errCode)
		if code == "" {
			code = "ACK_REJECTED"
		}
		result, err = tx.ExecContext(ctx, `
			UPDATE server_tasks
			SET status = 'failed', error_code = $1, error_retryable = false,
			    completed_at = now(), lease_owner = NULL, lease_expires_at = NULL,
			    lease_renewed_at = NULL, lease_token = NULL,
			    state_version = state_version + 1, updated_at = now()
			WHERE id = $2 AND status IN ('leased', 'running')
		`, code, fence.OperationID)
	}
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法确认任务", true)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		// 已被其他状态转换抢占（例如对账把租约重新入队）：ack 成为 no-op。
		return staleFenceOrNotFound(sql.ErrNoRows)
	}
	if err := tx.Commit(); err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法提交事务", true)
	}
	return nil
}

// ReportTaskProgress applies agent progress: percent is monotonic, a progress
// frame also confirms execution (leased -> running), and each frame renews the
// lease. Checkpoint now carries only execution progress, never task input.
func (s *Postgres) ReportTaskProgress(ctx context.Context, fence TaskLeaseFence, percent int, checkpoint string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if percent < 0 || percent > 100 {
		return domain.NewProblem("VALIDATION_FAILED", "任务进度必须在 0 到 100 之间", false)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	if _, _, _, _, _, _, _, _, _, _, err := s.fenceTaskRow(ctx, tx, fence); err != nil {
		return staleFenceOrNotFound(err)
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE server_tasks
		SET progress = GREATEST(progress, $1),
		    checkpoint = COALESCE(to_jsonb(NULLIF($2, '')), checkpoint),
		    status = CASE WHEN status = 'leased' THEN 'running' ELSE status END,
		    lease_expires_at = now() + $3::interval,
		    lease_renewed_at = now(),
		    state_version = state_version + 1,
		    updated_at = now()
		WHERE id = $4 AND status IN ('leased', 'running')
	`, percent, checkpoint, taskLeaseDuration.String(), fence.OperationID)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法更新任务进度", true)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return staleFenceOrNotFound(sql.ErrNoRows)
	}
	if err := tx.Commit(); err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法提交事务", true)
	}
	return nil
}

// RenewTaskLease extends the lease of a running (or leased) task. Agents send
// RunningTaskHeartbeat frames every 10 seconds; without renewal a long task
// would be re-queued mid-execution.
func (s *Postgres) RenewTaskLease(ctx context.Context, fence TaskLeaseFence) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	if _, _, _, _, _, _, _, _, _, _, err := s.fenceTaskRow(ctx, tx, fence); err != nil {
		return staleFenceOrNotFound(err)
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE server_tasks
		SET lease_expires_at = $1 + $2::interval,
		    lease_renewed_at = $1,
		    state_version = state_version + 1,
		    updated_at = now()
		WHERE id = $3 AND status IN ('leased', 'running')
	`, now, taskLeaseDuration.String(), fence.OperationID)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法续租任务", true)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return staleFenceOrNotFound(sql.ErrNoRows)
	}
	if err := tx.Commit(); err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法提交事务", true)
	}
	return nil
}

// CompleteTask moves a leased/running task to its terminal state
// (succeeded/failed) and records the outcome as an audit event. The fence must
// match the row: stale attempts, dead connections, and late results are silent
// no-ops that can never overwrite terminal state. resultJSON carries the
// agent's machine-readable result (e.g. backup checksum) for task-type
// specific writes.
func (s *Postgres) CompleteTask(ctx context.Context, fence TaskLeaseFence, succeeded bool, errCode *string, resultJSON []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	serverID, taskType, taskStatus, taskGeneration, _, _, _, _, taskCheckpoint, taskInput, err := s.fenceTaskRow(ctx, tx, fence)
	if err != nil {
		if err == sql.ErrNoRows {
			// 行不存在、栅栏过期或已被其他转换抢占：stale/late result 是 no-op。
			_ = tx.Commit()
			return nil
		}
		return domain.NewProblem("INTERNAL_ERROR", "无法查询任务", true)
	}
	if taskStatus == "succeeded" || taskStatus == "failed" {
		_ = tx.Commit()
		return nil
	}

	// 000009 起备份任务的输入位于 task_input；pre-000009 行回退 checkpoint。
	taskPayload := taskInput
	if taskPayload == "" {
		taskPayload = taskCheckpoint
	}

	now := time.Now().UTC()
	effectiveSucceeded := succeeded
	effectiveErrCode := errCode
	if succeeded && taskType == "backup" && !validBackupTaskResult(taskPayload, resultJSON) {
		effectiveSucceeded = false
		code := "BACKUP_INTEGRITY_FAILED"
		effectiveErrCode = &code
	}
	status := "failed"
	result := "failure"
	if effectiveSucceeded {
		status = "succeeded"
		result = "success"
	}
	var errorCodeValue any
	if effectiveErrCode != nil {
		errorCodeValue = *effectiveErrCode
	}
	resultOf := tx.QueryRowContext(ctx, `
		UPDATE server_tasks
		SET status = $1, error_code = $2, completed_at = $3, updated_at = $3,
		    lease_owner = NULL, lease_expires_at = NULL, lease_renewed_at = NULL,
		    lease_token = NULL, state_version = state_version + 1
		WHERE id = $4 AND node_id = $5 AND status IN ('leased', 'running')
		RETURNING id::text
	`, status, errorCodeValue, now, fence.OperationID, fence.NodeID)
	var completedID string
	if err := resultOf.Scan(&completedID); err != nil {
		if err == sql.ErrNoRows {
			// 终态转换被并发对账抢先：结果只能成为 no-op。
			_ = tx.Commit()
			return nil
		}
		return domain.NewProblem("INTERNAL_ERROR", "无法完成任务", true)
	}

	// 任务终态与 outbox 事件在同一事务内落库：消费方（发布器）只会看到
	// 已提交的 task.completed，不会出现"任务已成功、事件缺失"的中间态。
	var completedErrorCode string
	if effectiveErrCode != nil {
		completedErrorCode = *effectiveErrCode
	}
	if err := s.recordTaskOutboxEvent(ctx, tx, "task.completed", taskEventPayload{
		OperationID: fence.OperationID,
		ServerID:    serverID,
		NodeID:      fence.NodeID,
		TaskType:    taskType,
		Generation:  taskGeneration,
		Status:      status,
		ErrorCode:   completedErrorCode,
	}); err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法记录任务事件", true)
	}

	// 成功的 provision 把服务器生命周期推进到 ready，之后才能接收电源操作；
	// 失败的 provision 保持 provisioning 供上层重试/处置。
	if effectiveSucceeded && taskType == "provision" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE servers
			SET lifecycle_state = 'ready', updated_at = now()
			WHERE id = $1 AND deleted_at IS NULL
		`, serverID); err != nil {
			return domain.NewProblem("INTERNAL_ERROR", "无法更新服务器生命周期", true)
		}
	}

	// 成功的备份任务把 backups 元数据推进到终态：创建 → ready（带校验与位置），
	// 恢复 → 回到 ready，删除 → 标记 deleted。backupId 从任务输入解析，
	// checksum/size/storageLocation 从 resultJSON（Agent 执行回传）解析。
	if effectiveSucceeded {
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
			_ = json.Unmarshal([]byte(taskPayload), &payload)
			var res struct {
				Checksum        string `json:"checksum"`
				ManifestDigest  string `json:"manifestDigest"`
				SizeBytes       *int64 `json:"sizeBytes"`
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
				if res.SizeBytes != nil {
					sizeBytes = *res.SizeBytes
				}
				transition, err := tx.ExecContext(ctx, `
					UPDATE backups
					SET status = 'ready', content_digest = $2, size_bytes = $3, storage_location = $4,
					    manifest_digest = $6,
					    completed_at = now(), failure_code = NULL, failure_message = NULL
					WHERE id = $1 AND server_id = $5 AND status = 'creating'
				`, payload.BackupID, contentDigest, sizeBytes, res.StorageLocation, serverID, res.ManifestDigest)
				if err != nil {
					return domain.NewProblem("INTERNAL_ERROR", "无法更新备份元数据", true)
				}
				if err := requireBackupTransition(transition, "creating", "ready"); err != nil {
					return err
				}
			}
		case "restore":
			var payload struct {
				BackupID string `json:"backupId"`
			}
			_ = json.Unmarshal([]byte(taskPayload), &payload)
			if payload.BackupID != "" {
				transition, err := tx.ExecContext(ctx, `
					UPDATE backups SET status = 'ready', failure_code = NULL, failure_message = NULL
					WHERE id = $1 AND server_id = $2 AND status = 'restoring'
				`, payload.BackupID, serverID)
				if err != nil {
					return domain.NewProblem("INTERNAL_ERROR", "无法更新备份状态", true)
				}
				if err := requireBackupTransition(transition, "restoring", "ready"); err != nil {
					return err
				}
				serverResult, err := tx.ExecContext(ctx, `
					UPDATE servers
					SET desired_power = 'stopped', observed_power = 'stopped', health_condition = 'unknown',
					    observed_generation = $2, observed_at = $3, updated_at = $3
					WHERE id = $1 AND deleted_at IS NULL AND generation = $2
				`, serverID, taskGeneration, now)
				if err != nil {
					return domain.NewProblem("INTERNAL_ERROR", "无法收敛恢复后的服务器观测状态", true)
				}
				if err := requireSingleRow(serverResult, "SERVER_STATE_CONFLICT", "服务器 generation 已变化，拒绝覆盖恢复结果"); err != nil {
					return err
				}
			}
		case "backup-delete":
			var payload struct {
				BackupID string `json:"backupId"`
			}
			_ = json.Unmarshal([]byte(taskPayload), &payload)
			if payload.BackupID != "" {
				transition, err := tx.ExecContext(ctx, `
					UPDATE backups SET status = 'deleted', deleted_at = now(), failure_code = NULL, failure_message = NULL
					WHERE id = $1 AND server_id = $2 AND status = 'deleting'
				`, payload.BackupID, serverID)
				if err != nil {
					return domain.NewProblem("INTERNAL_ERROR", "无法更新备份状态", true)
				}
				if err := requireBackupTransition(transition, "deleting", "deleted"); err != nil {
					return err
				}
			}
		}
	} else if taskType == "backup" || taskType == "restore" || taskType == "backup-delete" {
		failureCode := "BACKUP_FAILED"
		if effectiveErrCode != nil && *effectiveErrCode != "" {
			failureCode = *effectiveErrCode
		}
		if err := compensateBackupTaskTx(ctx, tx, taskType, taskPayload, serverID, failureCode); err != nil {
			return err
		}
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO audit_events (actor_type, action, target_type, target_id, result, operation_id, trace_id, created_at)
		VALUES ('agent', 'task.complete', 'server', $1, $2, $3, $4, $5)
	`, serverID, result, fence.OperationID, id.New(), now)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法记录审计日志", true)
	}

	if err := tx.Commit(); err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法提交事务", true)
	}
	return nil
}

func validBackupTaskResult(checkpoint string, resultJSON []byte) bool {
	var payload backupTaskPayload
	var result struct {
		BackupID        string `json:"backupId"`
		Checksum        string `json:"checksum"`
		ManifestDigest  string `json:"manifestDigest"`
		SizeBytes       *int64 `json:"sizeBytes"`
		StorageLocation string `json:"storageLocation"`
	}
	if json.Unmarshal([]byte(checkpoint), &payload) != nil || payload.BackupID == "" {
		return false
	}
	if json.Unmarshal(resultJSON, &result) != nil || result.BackupID != payload.BackupID || result.SizeBytes == nil || *result.SizeBytes < 0 || result.StorageLocation == "" {
		return false
	}
	if !strings.HasPrefix(result.Checksum, "sha256:") || len(result.Checksum) != len("sha256:")+sha256.Size*2 {
		return false
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(result.Checksum, "sha256:")); err != nil {
		return false
	}
	if !strings.HasPrefix(result.ManifestDigest, "sha256:") || len(result.ManifestDigest) != len("sha256:")+sha256.Size*2 {
		return false
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(result.ManifestDigest, "sha256:")); err != nil {
		return false
	}
	clean := path.Clean(result.StorageLocation)
	if clean != result.StorageLocation || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return false
	}
	if payload.StorageObjectKey != "" && clean != path.Clean(payload.StorageObjectKey) {
		return false
	}
	return true
}

func requireSingleRow(result sql.Result, code, message string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法确认数据库状态转换", true)
	}
	if affected == 1 {
		return nil
	}
	problem := domain.NewProblem(code, message, false)
	problem.Details["rowsAffected"] = affected
	return problem
}

func requireBackupTransition(result sql.Result, expected, next string) error {
	if !domain.BackupStatusTransitionAllowed(expected, next) {
		return domain.NewProblem("INTERNAL_ERROR", "备份状态转换未在状态机中声明", false)
	}
	problem := requireSingleRow(result, "BACKUP_STATE_CONFLICT", "备份状态已变化，拒绝覆盖任务结果")
	if problem == nil {
		return nil
	}
	if typed, ok := problem.(*domain.Problem); ok {
		typed.Details["expectedStatus"] = expected
		typed.Details["nextStatus"] = next
	}
	return problem
}

func compensateBackupTaskTx(ctx context.Context, tx *sql.Tx, taskType, checkpoint, serverID, failureCode string) error {
	if taskType != "backup" && taskType != "restore" && taskType != "backup-delete" {
		return nil
	}
	var payload backupTaskPayload
	if err := json.Unmarshal([]byte(checkpoint), &payload); err != nil || payload.BackupID == "" {
		return nil
	}
	expectedStatus := "creating"
	status := "failed"
	if taskType == "restore" {
		expectedStatus = "restoring"
		status = "ready"
	} else if taskType == "backup-delete" {
		expectedStatus = "deleting"
		var previousFailure sql.NullString
		if err := tx.QueryRowContext(ctx, `
			SELECT failure_code
			FROM backups
			WHERE id = $1 AND server_id = $2 AND status = 'deleting'
			FOR UPDATE
		`, payload.BackupID, serverID).Scan(&previousFailure); err != nil {
			if err == sql.ErrNoRows {
				return domain.NewProblem("BACKUP_STATE_CONFLICT", "备份状态已变化，无法补偿删除任务", false)
			}
			return domain.NewProblem("INTERNAL_ERROR", "无法读取删除前的备份状态", true)
		}
		status = "ready"
		if previousFailure.Valid {
			status = "failed"
		}
	}
	normalizedCode, failureMessage := backupFailureDetails(failureCode)
	transition, err := tx.ExecContext(ctx, `
		UPDATE backups
		SET status = $1,
		    completed_at = CASE WHEN $1 = 'failed' THEN NULL ELSE completed_at END,
		    failure_code = $4,
		    failure_message = $5
		WHERE id = $2 AND server_id = $3 AND status = $6
	`, status, payload.BackupID, serverID, normalizedCode, failureMessage, expectedStatus)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法补偿备份终态", true)
	}
	return requireBackupTransition(transition, expectedStatus, status)
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
//     （清空租约、租约主与租约凭据），等待 Agent 下次 claim 重试；
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
	// 旧租约凭据立即作废：迟到的心跳/结果与下一次 claim 的 fence 不匹配。
	if _, err := s.db.ExecContext(ctx, `
		UPDATE server_tasks
		SET status = 'queued', lease_owner = NULL, lease_expires_at = NULL,
		    lease_renewed_at = NULL, lease_token = NULL,
		    state_version = state_version + 1, updated_at = now()
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
		    completed_at = now(), updated_at = now(),
		    lease_owner = NULL, lease_expires_at = NULL, lease_renewed_at = NULL,
		    lease_token = NULL, state_version = state_version + 1
		WHERE status = 'leased' AND lease_expires_at < $1 AND attempt >= max_attempts
		RETURNING id::text, server_id::text, task_type,
		          COALESCE(task_input::text, ''), COALESCE(checkpoint::text, '')
	`, now)
	if err != nil {
		return
	}
	type expiredTask struct {
		operationID string
		serverID    string
		taskType    string
		taskInput   string
		checkpoint  string
	}
	var expired []expiredTask
	for rows.Next() {
		var task expiredTask
		if err := rows.Scan(&task.operationID, &task.serverID, &task.taskType, &task.taskInput, &task.checkpoint); err != nil {
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
		payload := task.taskInput
		if payload == "" {
			payload = task.checkpoint
		}
		if err := compensateBackupTaskTx(ctx, tx, task.taskType, payload, task.serverID, "MAX_ATTEMPTS"); err != nil {
			return
		}
		if err := s.recordTaskOutboxEvent(ctx, tx, "task.completed", taskEventPayload{
			OperationID: task.operationID,
			ServerID:    task.serverID,
			TaskType:    task.taskType,
			Status:      "failed",
			ErrorCode:   "MAX_ATTEMPTS",
		}); err != nil {
			return
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO audit_events (actor_type, action, target_type, target_id, result, operation_id, trace_id, created_at)
			VALUES ('system', 'task.expired', 'server', $1, 'failure', $2, $3, now())
		`, task.serverID, task.operationID, id.New()); err != nil {
			return
		}
	}
	if err := tx.Commit(); err != nil {
		return
	}
}
