package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
)

// taskEventPayload 是写入 outbox_events 的任务生命周期事件负载。
// 业务状态、任务与 Outbox 在同一事务内提交，保证事件不丢、不前置。
type taskEventPayload struct {
	OperationID string `json:"operationId"`
	ServerID    string `json:"serverId"`
	NodeID      string `json:"nodeId"`
	TaskType    string `json:"taskType"`
	Generation  int64  `json:"generation"`
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"maxAttempts"`
	Status      string `json:"status"`
	ErrorCode   string `json:"errorCode,omitempty"`
}

// sqlExecer 抽象 *sql.DB 与 *sql.Tx 共有的执行能力：事务内调用传 *sql.Tx
// 保证与业务写入原子提交；无事务的便捷路径（如 gRPC EnqueueTask）传 *sql.DB。
type sqlExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// recordTaskOutboxEventTx 写入一条 server_task 生命周期事件
// （task.created / task.completed）。事务内调用时与业务写入原子提交，
// 事务回滚则事件一并回滚，从根源杜绝"业务已写、事件缺失"的半成功状态。
func (s *Postgres) recordTaskOutboxEvent(ctx context.Context, exec sqlExecer, eventType string, payload taskEventPayload) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = exec.ExecContext(ctx, `
		INSERT INTO outbox_events (aggregate_type, aggregate_id, event_type, payload)
		VALUES ('server_task', $1, $2, $3::jsonb)
	`, payload.ServerID, eventType, string(encoded))
	return err
}

// PublishOutboxEvents 消费一批未发布事件：把它们标记为已发布
// （published_at），返回本次发布的事件数。
//
// 多副本语义：多个控制面副本可以各自运行发布器，SELECT ... FOR UPDATE
// SKIP LOCKED 保证每条事件恰好被一个发布者处理；标记动作幂等，重复执行
// 无副作用。当前 Agent 以 pull 模式经 ClaimTask 领取任务，事件发布器负责
// 确认事件已送达并回收未发布积压，为后续 WebSocket / webhook 等实时推送
// 保留统一扩展点。
func (s *Postgres) PublishOutboxEvents(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 50
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM outbox_events
		WHERE published_at IS NULL
		ORDER BY created_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, limit)
	if err != nil {
		return 0, domain.NewProblem("INTERNAL_ERROR", "无法查询待发布事件", true)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var eventID string
		if err := rows.Scan(&eventID); err != nil {
			return 0, domain.NewProblem("INTERNAL_ERROR", "无法读取待发布事件", true)
		}
		ids = append(ids, eventID)
	}
	if err := rows.Err(); err != nil {
		return 0, domain.NewProblem("INTERNAL_ERROR", "无法读取待发布事件", true)
	}
	if len(ids) == 0 {
		return 0, nil
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE outbox_events
		SET published_at = now()
		WHERE id = ANY($1::uuid[])
	`, ids); err != nil {
		return 0, domain.NewProblem("INTERNAL_ERROR", "无法标记事件已发布", true)
	}
	if err := tx.Commit(); err != nil {
		return 0, domain.NewProblem("INTERNAL_ERROR", "无法提交事务", true)
	}
	return len(ids), nil
}
