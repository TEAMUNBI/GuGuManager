package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
)

const outboxMaxPublishAttempts = 10

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

type OutboxEvent struct {
	ID            string
	AggregateType string
	AggregateID   string
	EventType     string
	EventVersion  int
	BusinessAt    time.Time
	Payload       json.RawMessage
}

type OutboxPublisher func(context.Context, OutboxEvent) error

type sqlExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s *Postgres) recordTaskOutboxEvent(ctx context.Context, exec sqlExecer, eventType string, payload taskEventPayload) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = exec.ExecContext(ctx, `
		INSERT INTO outbox_events (aggregate_type, aggregate_id, event_type, event_version, business_at, payload)
		VALUES ('server_task', $1, $2, 1, now(), $3::jsonb)
	`, payload.ServerID, eventType, string(encoded))
	return err
}

// PublishOutboxEvents keeps the existing administrative/test drain contract.
// Production uses PublishOutboxEventsTo so broker acknowledgement gates the
// published timestamp.
func (s *Postgres) PublishOutboxEvents(ctx context.Context, limit int) (int, error) {
	return s.PublishOutboxEventsTo(ctx, limit, func(context.Context, OutboxEvent) error { return nil })
}

// PublishOutboxEventsTo provides at-least-once delivery. A crash after broker
// acknowledgement but before commit intentionally causes a duplicate; every
// consumer uses the immutable event ID as its idempotency key.
func (s *Postgres) PublishOutboxEventsTo(ctx context.Context, limit int, publish OutboxPublisher) (int, error) {
	if publish == nil {
		return 0, domain.NewProblem("INTERNAL_ERROR", "Outbox publisher 未配置", false)
	}
	if limit <= 0 {
		limit = 50
	}
	published := 0
	for published < limit {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return published, domain.NewProblem("INTERNAL_ERROR", "无法开始 Outbox 事务", true)
		}
		var event OutboxEvent
		err = tx.QueryRowContext(ctx, `
			SELECT id::text, aggregate_type, aggregate_id, event_type, event_version, business_at, payload
			FROM outbox_events
			WHERE published_at IS NULL AND dead_lettered_at IS NULL AND next_attempt_at <= now()
			ORDER BY created_at
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		`).Scan(&event.ID, &event.AggregateType, &event.AggregateID, &event.EventType, &event.EventVersion, &event.BusinessAt, &event.Payload)
		if errors.Is(err, sql.ErrNoRows) {
			_ = tx.Rollback()
			break
		}
		if err != nil {
			_ = tx.Rollback()
			return published, domain.NewProblem("INTERNAL_ERROR", "无法查询待发布事件", true)
		}

		if err = publish(ctx, event); err != nil {
			message := strings.TrimSpace(err.Error())
			if len(message) > 1024 {
				message = message[:1024]
			}
			_, updateErr := tx.ExecContext(ctx, `
				UPDATE outbox_events
				SET publish_attempts = publish_attempts + 1,
				    last_error = $2,
				    next_attempt_at = now() + make_interval(secs => LEAST(300, (1::bigint << LEAST(8, publish_attempts + 1)))::integer),
				    dead_lettered_at = CASE WHEN publish_attempts + 1 >= $3 THEN now() ELSE NULL END
				WHERE id = $1
			`, event.ID, message, outboxMaxPublishAttempts)
			if updateErr != nil || tx.Commit() != nil {
				return published, domain.NewProblem("INTERNAL_ERROR", "无法记录 Outbox 发布失败", true)
			}
			return published, err
		}

		if _, err = tx.ExecContext(ctx, `
			UPDATE outbox_events
			SET published_at = now(), publish_attempts = publish_attempts + 1,
			    last_error = NULL, next_attempt_at = now()
			WHERE id = $1
		`, event.ID); err != nil {
			_ = tx.Rollback()
			return published, domain.NewProblem("INTERNAL_ERROR", "无法标记事件已发布", true)
		}
		if err = tx.Commit(); err != nil {
			return published, domain.NewProblem("INTERNAL_ERROR", "无法提交 Outbox 事务", true)
		}
		published++
	}
	return published, nil
}

func (s *Postgres) ReplayDeadLetter(ctx context.Context, eventID string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE outbox_events
		SET dead_lettered_at=NULL, publish_attempts=0, last_error=NULL, next_attempt_at=now()
		WHERE id=$1 AND published_at IS NULL AND dead_lettered_at IS NOT NULL
	`, eventID)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法重放 Outbox 事件", true)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return domain.NewProblem("NOT_FOUND", "死信事件不存在", false)
	}
	return nil
}

func (s *Postgres) OutboxDeadLetters(ctx context.Context) ([]domain.OutboxDeadLetter, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, aggregate_type, aggregate_id, event_type, event_version,
		       payload, publish_attempts, COALESCE(last_error, ''), business_at, dead_lettered_at
		FROM outbox_events WHERE published_at IS NULL AND dead_lettered_at IS NOT NULL
		ORDER BY dead_lettered_at DESC, id LIMIT 200
	`)
	if err != nil {
		return nil, domain.NewProblem("INTERNAL_ERROR", "无法读取 Outbox 死信", true)
	}
	defer rows.Close()
	result := []domain.OutboxDeadLetter{}
	for rows.Next() {
		var item domain.OutboxDeadLetter
		var payload []byte
		if err := rows.Scan(&item.ID, &item.AggregateType, &item.AggregateID, &item.EventType,
			&item.EventVersion, &payload, &item.PublishAttempts, &item.LastError, &item.BusinessAt, &item.DeadLetteredAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &item.Payload); err != nil {
			item.Payload = map[string]any{"invalid": true}
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
