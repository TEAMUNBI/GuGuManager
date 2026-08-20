package store

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/id"
	"github.com/lib/pq"
	"github.com/robfig/cron/v3"
)

func normalizeScheduleInput(input domain.ScheduleInput, now time.Time) (domain.ScheduleInput, time.Time, error) {
	input.Name, input.Action = strings.TrimSpace(input.Name), strings.TrimSpace(input.Action)
	input.CronExpression, input.Timezone = strings.TrimSpace(input.CronExpression), strings.TrimSpace(input.Timezone)
	if len([]rune(input.Name)) < 1 || len([]rune(input.Name)) > 128 {
		return input, time.Time{}, domain.NewProblem("VALIDATION_FAILED", "Schedule 名称需要在 1 到 128 个字符之间", false)
	}
	allowedAction := map[string]bool{"backup": true, "start": true, "stop": true, "restart": true, "retention-cleanup": true}
	if !allowedAction[input.Action] || (input.ServerID == "" && input.Action != "retention-cleanup") {
		return input, time.Time{}, domain.NewProblem("VALIDATION_FAILED", "Schedule action 或 serverId 无效", false)
	}
	if input.Timezone == "" {
		input.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(input.Timezone); err != nil {
		return input, time.Time{}, domain.NewProblem("VALIDATION_FAILED", "Schedule timezone 必须是有效的 IANA 时区", false)
	}
	parsed, err := cron.ParseStandard("CRON_TZ=" + input.Timezone + " " + input.CronExpression)
	if err != nil {
		return input, time.Time{}, domain.NewProblem("VALIDATION_FAILED", "Cron 表达式无效", false)
	}
	if input.MissedRunPolicy == "" {
		input.MissedRunPolicy = "skip"
	}
	if input.ConcurrencyPolicy == "" {
		input.ConcurrencyPolicy = "forbid"
	}
	if !map[string]bool{"skip": true, "run-once": true, "catch-up": true}[input.MissedRunPolicy] ||
		!map[string]bool{"forbid": true, "allow": true, "replace": true}[input.ConcurrencyPolicy] {
		return input, time.Time{}, domain.NewProblem("VALIDATION_FAILED", "Schedule 执行策略无效", false)
	}
	return input, parsed.Next(now), nil
}

func (s *Postgres) Schedules() ([]domain.Schedule, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, COALESCE(server_id::text, ''), name, action, cron_expression, timezone,
		       enabled, missed_run_policy, concurrency_policy, next_run_at, last_scheduled_at, created_at, updated_at
		FROM schedules ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, domain.NewProblem("INTERNAL_ERROR", "无法读取调度计划", true)
	}
	defer rows.Close()
	result := []domain.Schedule{}
	for rows.Next() {
		var item domain.Schedule
		var next, last sql.NullTime
		if err := rows.Scan(&item.ID, &item.ServerID, &item.Name, &item.Action, &item.CronExpression, &item.Timezone,
			&item.Enabled, &item.MissedRunPolicy, &item.ConcurrencyPolicy, &next, &last, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if next.Valid {
			item.NextRunAt = &next.Time
		}
		if last.Valid {
			item.LastScheduledAt = &last.Time
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Postgres) CreateSchedule(input domain.ScheduleInput, actor domain.User) (domain.Schedule, error) {
	if !hasRole(actor, "platform_admin") {
		return domain.Schedule{}, domain.NewProblem("FORBIDDEN", "需要平台管理员权限", false)
	}
	now := time.Now().UTC()
	normalized, next, err := normalizeScheduleInput(input, now)
	if err != nil {
		return domain.Schedule{}, err
	}
	enabled := true
	if normalized.Enabled != nil {
		enabled = *normalized.Enabled
	}
	var nextValue any
	if enabled {
		nextValue = next
	}
	item := domain.Schedule{ID: id.New(), ServerID: normalized.ServerID, Name: normalized.Name, Action: normalized.Action,
		CronExpression: normalized.CronExpression, Timezone: normalized.Timezone, Enabled: enabled,
		MissedRunPolicy: normalized.MissedRunPolicy, ConcurrencyPolicy: normalized.ConcurrencyPolicy, CreatedAt: now, UpdatedAt: now}
	if enabled {
		item.NextRunAt = &next
	}
	_, err = s.db.Exec(`
		INSERT INTO schedules (id, server_id, name, action, cron_expression, timezone, enabled,
		                       missed_run_policy, concurrency_policy, next_run_at, created_by, created_at, updated_at)
		VALUES ($1, NULLIF($2, '')::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12)
	`, item.ID, item.ServerID, item.Name, item.Action, item.CronExpression, item.Timezone, item.Enabled,
		item.MissedRunPolicy, item.ConcurrencyPolicy, nextValue, actor.ID, now)
	if err != nil {
		if foreignKeyViolation(err) {
			return domain.Schedule{}, domain.NewProblem("NOT_FOUND", "Schedule 引用的服务器不存在", false)
		}
		return domain.Schedule{}, domain.NewProblem("INTERNAL_ERROR", "无法创建调度计划", true)
	}
	return item, nil
}

func (s *Postgres) DeleteSchedule(scheduleID string, actor domain.User) error {
	if !hasRole(actor, "platform_admin") {
		return domain.NewProblem("FORBIDDEN", "需要平台管理员权限", false)
	}
	result, err := s.db.Exec(`DELETE FROM schedules WHERE id = $1`, scheduleID)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法删除调度计划", true)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return domain.NewProblem("NOT_FOUND", "Schedule 不存在", false)
	}
	return nil
}

func (s *Postgres) ScheduleRuns(scheduleID string) ([]domain.ScheduleRun, error) {
	rows, err := s.db.Query(`
		SELECT id::text, schedule_id::text, scheduled_for, status, COALESCE(operation_id::text, ''),
		       COALESCE(failure_code, ''), started_at, completed_at
		FROM schedule_runs WHERE schedule_id = $1 ORDER BY scheduled_for DESC LIMIT 100
	`, scheduleID)
	if err != nil {
		return nil, domain.NewProblem("INTERNAL_ERROR", "无法读取调度运行记录", true)
	}
	defer rows.Close()
	result := []domain.ScheduleRun{}
	for rows.Next() {
		var run domain.ScheduleRun
		var started, completed sql.NullTime
		if err := rows.Scan(&run.ID, &run.ScheduleID, &run.ScheduledFor, &run.Status, &run.OperationID, &run.FailureCode, &started, &completed); err != nil {
			return nil, err
		}
		if started.Valid {
			run.StartedAt = &started.Time
		}
		if completed.Valid {
			run.CompletedAt = &completed.Time
		}
		result = append(result, run)
	}
	return result, rows.Err()
}

type dueSchedule struct {
	id, serverID, action, expression, timezone, missed, concurrency, actorID string
	scheduledFor                                                             time.Time
}

func (s *Postgres) RunDueSchedules(ctx context.Context, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		limit = 16
	}
	processed := 0
	for processed < limit && ctx.Err() == nil {
		due, runID, skipped, ok, err := s.claimDueSchedule(ctx, now)
		if err != nil {
			return processed, err
		}
		if !ok {
			break
		}
		processed++
		if skipped {
			continue
		}
		actor, err := s.UserByID(due.actorID)
		if err != nil {
			s.completeScheduleRun(runID, "failed", "ACTOR_REVOKED", "")
			continue
		}
		key := fmt.Sprintf("schedule-%s-%d", due.id, due.scheduledFor.Unix())
		var operation domain.Operation
		switch due.action {
		case "backup":
			operation, err = s.CreateBackup(due.serverID, key, actor)
		case "start", "stop", "restart":
			operation, err = s.RequestPower(due.serverID, domain.PowerAction(due.action), key, actor)
		case "retention-cleanup":
			_, err = s.ApplyBackupRetention(ctx, actor, 100)
		}
		if err != nil {
			code := "SCHEDULE_ACTION_FAILED"
			var problem *domain.Problem
			if errors.As(err, &problem) {
				code = problem.Code
			}
			s.completeScheduleRun(runID, "failed", code, "")
			continue
		}
		if operation.ID == "" {
			s.completeScheduleRun(runID, "succeeded", "", "")
		} else {
			_, _ = s.db.Exec(`UPDATE schedule_runs SET status = 'running', operation_id = $2, started_at = now() WHERE id = $1`, runID, operation.ID)
		}
	}
	_ = s.ReconcileScheduleRuns(ctx)
	return processed, ctx.Err()
}

func (s *Postgres) claimDueSchedule(ctx context.Context, now time.Time) (dueSchedule, string, bool, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dueSchedule{}, "", false, false, err
	}
	defer tx.Rollback()
	var leader bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(717171717171::bigint)`).Scan(&leader); err != nil || !leader {
		return dueSchedule{}, "", false, false, err
	}
	var due dueSchedule
	err = tx.QueryRowContext(ctx, `
		SELECT id::text, COALESCE(server_id::text, ''), action, cron_expression, timezone,
		       missed_run_policy, concurrency_policy, COALESCE(created_by::text, ''), next_run_at
		FROM schedules WHERE enabled = true AND next_run_at <= $1
		ORDER BY next_run_at FOR UPDATE SKIP LOCKED LIMIT 1
	`, now).Scan(&due.id, &due.serverID, &due.action, &due.expression, &due.timezone,
		&due.missed, &due.concurrency, &due.actorID, &due.scheduledFor)
	if err == sql.ErrNoRows {
		return dueSchedule{}, "", false, false, tx.Commit()
	}
	if err != nil {
		return dueSchedule{}, "", false, false, err
	}
	parsed, err := cron.ParseStandard("CRON_TZ=" + due.timezone + " " + due.expression)
	if err != nil {
		return dueSchedule{}, "", false, false, err
	}
	base := due.scheduledFor
	if due.missed != "catch-up" && now.Sub(due.scheduledFor) > time.Minute {
		base = now
	}
	next := parsed.Next(base)
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM schedule_runs WHERE schedule_id = $1 AND status IN ('queued', 'running')`, due.id).Scan(&active); err != nil {
		return dueSchedule{}, "", false, false, err
	}
	skipped := active > 0 && due.concurrency != "allow"
	status := "queued"
	if skipped {
		status = "skipped"
	}
	runID := id.New()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO schedule_runs (id, schedule_id, scheduled_for, status, completed_at)
		VALUES ($1, $2, $3, $4, CASE WHEN $4 = 'skipped' THEN now() END)
		ON CONFLICT (schedule_id, scheduled_for) DO NOTHING
	`, runID, due.id, due.scheduledFor, status)
	if err != nil {
		return dueSchedule{}, "", false, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE schedules SET last_scheduled_at = $2, next_run_at = $3, updated_at = now() WHERE id = $1`, due.id, due.scheduledFor, next); err != nil {
		return dueSchedule{}, "", false, false, err
	}
	if err := tx.Commit(); err != nil {
		return dueSchedule{}, "", false, false, err
	}
	return due, runID, skipped, true, nil
}

func (s *Postgres) completeScheduleRun(runID, status, failureCode, operationID string) {
	_, _ = s.db.Exec(`UPDATE schedule_runs SET status = $2, failure_code = NULLIF($3, ''), operation_id = COALESCE(NULLIF($4, '')::uuid, operation_id), completed_at = now() WHERE id = $1`, runID, status, failureCode, operationID)
}

func (s *Postgres) ReconcileScheduleRuns(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE schedule_runs sr SET
		  status = CASE st.status WHEN 'succeeded' THEN 'succeeded' ELSE 'failed' END,
		  failure_code = CASE WHEN st.status = 'failed' THEN COALESCE(st.error_code, 'OPERATION_FAILED') ELSE NULL END,
		  completed_at = now()
		FROM server_tasks st
		WHERE sr.operation_id = st.id AND sr.status = 'running' AND st.status IN ('succeeded', 'failed')
	`)
	return err
}

func (s *Postgres) BackupPolicy(serverID string) (domain.BackupPolicy, error) {
	var policy domain.BackupPolicy
	err := s.db.QueryRow(`
		SELECT $1, COALESCE(p.retention_days, 30), COALESCE(p.max_count, 10),
		       COALESCE(p.protect_manual, true), COALESCE(p.enabled, true), COALESCE(p.updated_at, now())
		FROM servers s LEFT JOIN backup_policies p ON p.server_id = s.id
		WHERE s.id = $1 AND s.deleted_at IS NULL
	`, serverID).Scan(&policy.ServerID, &policy.RetentionDays, &policy.MaxCount, &policy.ProtectManual, &policy.Enabled, &policy.UpdatedAt)
	if err == sql.ErrNoRows {
		return policy, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	return policy, err
}

func (s *Postgres) PutBackupPolicy(serverID string, input domain.BackupPolicyInput, actor domain.User) (domain.BackupPolicy, error) {
	if err := s.AuthorizeServer(actor.ID, serverID, "servers.backups.delete"); err != nil {
		return domain.BackupPolicy{}, err
	}
	if input.RetentionDays < 1 || input.RetentionDays > 3650 || input.MaxCount < 1 || input.MaxCount > 1000 {
		return domain.BackupPolicy{}, domain.NewProblem("VALIDATION_FAILED", "备份保留天数或数量无效", false)
	}
	_, err := s.db.Exec(`
		INSERT INTO backup_policies (server_id, retention_days, max_count, protect_manual, enabled)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (server_id) DO UPDATE SET retention_days = EXCLUDED.retention_days,
		max_count = EXCLUDED.max_count, protect_manual = EXCLUDED.protect_manual, enabled = EXCLUDED.enabled, updated_at = now()
	`, serverID, input.RetentionDays, input.MaxCount, input.ProtectManual, input.Enabled)
	if err != nil {
		return domain.BackupPolicy{}, domain.NewProblem("INTERNAL_ERROR", "无法保存备份策略", true)
	}
	return s.BackupPolicy(serverID)
}

func (s *Postgres) ApplyBackupRetention(ctx context.Context, actor domain.User, limit int) (int, error) {
	if !hasRole(actor, "platform_admin") {
		return 0, domain.NewProblem("FORBIDDEN", "需要平台管理员权限", false)
	}
	rows, err := s.db.QueryContext(ctx, `
		WITH ranked AS (
		  SELECT b.id::text, b.server_id::text, b.created_at, p.retention_days, p.max_count,
		         row_number() OVER (PARTITION BY b.server_id ORDER BY b.created_at DESC) AS position
		  FROM backups b JOIN backup_policies p ON p.server_id = b.server_id AND p.enabled
		  WHERE b.status IN ('ready', 'failed') AND b.protected = false
		)
		SELECT id, server_id FROM ranked
		WHERE created_at < now() - make_interval(days => retention_days) OR position > max_count
		ORDER BY created_at LIMIT $1
	`, limit)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type candidate struct{ id, serverID string }
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.serverID); err != nil {
			return 0, err
		}
		candidates = append(candidates, item)
	}
	deleted := 0
	for _, item := range candidates {
		key := fmt.Sprintf("retention-%s-%d", item.id, time.Now().UTC().Unix()/60)
		if _, err := s.DeleteBackup(item.serverID, item.id, key, actor); err == nil {
			deleted++
		}
	}
	return deleted, nil
}

func (s *Postgres) Notifications() ([]domain.Notification, error) {
	rows, err := s.db.Query(`SELECT id::text, severity, category, title, message, target_type, target_id, metadata, created_at, acknowledged_at FROM notifications ORDER BY created_at DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.Notification{}
	for rows.Next() {
		var item domain.Notification
		var raw []byte
		var acknowledged sql.NullTime
		if err := rows.Scan(&item.ID, &item.Severity, &item.Category, &item.Title, &item.Message, &item.TargetType, &item.TargetID, &raw, &item.CreatedAt, &acknowledged); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &item.Metadata)
		if acknowledged.Valid {
			item.AcknowledgedAt = &acknowledged.Time
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Postgres) AcknowledgeNotification(notificationID string, actor domain.User) error {
	result, err := s.db.Exec(`UPDATE notifications SET acknowledged_at = COALESCE(acknowledged_at, now()), acknowledged_by = COALESCE(acknowledged_by, $2) WHERE id = $1`, notificationID, actor.ID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return domain.NewProblem("NOT_FOUND", "通知不存在", false)
	}
	return nil
}

func (s *Postgres) Webhooks() ([]domain.WebhookEndpoint, error) {
	rows, err := s.db.Query(`SELECT id::text, name, url, enabled, created_at, updated_at FROM webhook_endpoints ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.WebhookEndpoint{}
	for rows.Next() {
		var item domain.WebhookEndpoint
		if err := rows.Scan(&item.ID, &item.Name, &item.URL, &item.Enabled, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Postgres) CreateWebhook(input domain.WebhookInput, actor domain.User) (domain.WebhookCredential, error) {
	if !hasRole(actor, "platform_admin") {
		return domain.WebhookCredential{}, domain.NewProblem("FORBIDDEN", "需要平台管理员权限", false)
	}
	input.Name, input.URL = strings.TrimSpace(input.Name), strings.TrimSpace(input.URL)
	parsed, err := url.Parse(input.URL)
	if len([]rune(input.Name)) < 1 || len([]rune(input.Name)) > 128 || err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return domain.WebhookCredential{}, domain.NewProblem("VALIDATION_FAILED", "Webhook 名称或 HTTPS URL 无效", false)
	}
	secretRaw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, secretRaw); err != nil {
		return domain.WebhookCredential{}, err
	}
	secret := hex.EncodeToString(secretRaw)
	s.mu.RLock()
	keyring := s.secretKeyring
	legacy := s.secretCipher
	s.mu.RUnlock()
	var encrypted string
	if keyring != nil {
		encrypted, err = keyring.EncryptValue(secret)
	} else if legacy != nil {
		encrypted, err = legacy.EncryptValue(secret)
	} else {
		err = errors.New("secret keyring is unavailable")
	}
	if err != nil {
		return domain.WebhookCredential{}, domain.NewProblem("INTERNAL_ERROR", "无法加密 Webhook Secret", true)
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	now := time.Now().UTC()
	endpoint := domain.WebhookEndpoint{ID: id.New(), Name: input.Name, URL: input.URL, Enabled: enabled, CreatedAt: now, UpdatedAt: now}
	_, err = s.db.Exec(`INSERT INTO webhook_endpoints (id, name, url, secret_ciphertext, enabled, created_by, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$7)`,
		endpoint.ID, endpoint.Name, endpoint.URL, encrypted, endpoint.Enabled, actor.ID, now)
	if err != nil {
		return domain.WebhookCredential{}, domain.NewProblem("INTERNAL_ERROR", "无法创建 Webhook", true)
	}
	return domain.WebhookCredential{WebhookEndpoint: endpoint, Secret: secret}, nil
}

func (s *Postgres) EmitNotification(ctx context.Context, severity, category, title, message, targetType, targetID, dedupeKey string, metadata map[string]any) (string, error) {
	encoded, _ := json.Marshal(metadata)
	notificationID := id.New()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `
		INSERT INTO notifications (id, severity, category, title, message, target_type, target_id, dedupe_key, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb)
		ON CONFLICT (dedupe_key) DO UPDATE SET dedupe_key = EXCLUDED.dedupe_key
		RETURNING id::text
	`, notificationID, severity, category, title, message, targetType, targetID, dedupeKey, string(encoded)).Scan(&notificationID)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO notification_deliveries (notification_id, channel, status, delivered_at) VALUES ($1, 'in-app', 'delivered', now()) ON CONFLICT DO NOTHING`, notificationID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO notification_deliveries (notification_id, webhook_endpoint_id, channel) SELECT $1, id, 'webhook' FROM webhook_endpoints WHERE enabled ON CONFLICT DO NOTHING`, notificationID); err != nil {
		return "", err
	}
	return notificationID, tx.Commit()
}

func (s *Postgres) TestWebhook(ctx context.Context, webhookID string, actor domain.User) (string, error) {
	if !hasRole(actor, "platform_admin") {
		return "", domain.NewProblem("FORBIDDEN", "需要平台管理员权限", false)
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM webhook_endpoints WHERE id=$1 AND enabled)`, webhookID).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		return "", domain.NewProblem("NOT_FOUND", "Webhook 不存在或已停用", false)
	}
	notificationID := id.New()
	deliveryID := id.New()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO notifications (id,severity,category,title,message,target_type,target_id,dedupe_key)
		VALUES ($1,'info','webhook-test','GuGuManager webhook test','Signed webhook delivery is working','webhook',$2,$3)
	`, notificationID, webhookID, "webhook-test-"+deliveryID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO notification_deliveries (id,notification_id,webhook_endpoint_id,channel)
		VALUES ($1,$2,$3,'webhook')
	`, deliveryID, notificationID, webhookID); err != nil {
		return "", err
	}
	return deliveryID, tx.Commit()
}

type deliveryClaim struct {
	id, notificationID, endpointID, endpointURL, encryptedSecret string
	attempt                                                      int
}

func (s *Postgres) DeliverNotifications(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		limit = 16
	}
	delivered := 0
	for delivered < limit && ctx.Err() == nil {
		claim, lease, ok, err := s.claimDelivery(ctx)
		if err != nil {
			return delivered, err
		}
		if !ok {
			break
		}
		var notification domain.Notification
		var metadata []byte
		err = s.db.QueryRowContext(ctx, `SELECT id::text, severity, category, title, message, target_type, target_id, metadata, created_at FROM notifications WHERE id = $1`, claim.notificationID).
			Scan(&notification.ID, &notification.Severity, &notification.Category, &notification.Title, &notification.Message, &notification.TargetType, &notification.TargetID, &metadata, &notification.CreatedAt)
		if err == nil {
			err = s.sendWebhook(ctx, claim, notification, metadata)
		}
		if err == nil {
			_, _ = s.db.Exec(`UPDATE notification_deliveries SET status = 'delivered', delivered_at = now(), lease_owner = NULL, lease_until = NULL, last_error = NULL, updated_at = now() WHERE id = $1 AND lease_owner = $2`, claim.id, lease)
			delivered++
		} else {
			status := "failed"
			if claim.attempt >= 8 {
				status = "dead-letter"
			}
			delay := time.Duration(1<<min(claim.attempt, 10)) * time.Second
			_, _ = s.db.Exec(`UPDATE notification_deliveries SET status = $3, next_attempt_at = now() + $4::interval, lease_owner = NULL, lease_until = NULL, last_error = $5, updated_at = now() WHERE id = $1 AND lease_owner = $2`, claim.id, lease, status, delay.String(), truncateError(err))
		}
	}
	return delivered, ctx.Err()
}

func (s *Postgres) claimDelivery(ctx context.Context) (deliveryClaim, string, bool, error) {
	lease := id.New()
	var claim deliveryClaim
	err := s.db.QueryRowContext(ctx, `
		WITH candidate AS (
		 SELECT id FROM notification_deliveries
		 WHERE channel = 'webhook' AND status IN ('pending','failed') AND next_attempt_at <= now()
		 ORDER BY next_attempt_at FOR UPDATE SKIP LOCKED LIMIT 1
		)
		UPDATE notification_deliveries d SET status='leased', attempt=attempt+1, lease_owner=$1,
		lease_until=now()+interval '2 minutes', updated_at=now()
		FROM candidate c, webhook_endpoints w
		WHERE d.id=c.id AND w.id=d.webhook_endpoint_id AND w.enabled
		RETURNING d.id::text, d.notification_id::text, w.id::text, w.url, w.secret_ciphertext, d.attempt
	`, lease).Scan(&claim.id, &claim.notificationID, &claim.endpointID, &claim.endpointURL, &claim.encryptedSecret, &claim.attempt)
	if err == sql.ErrNoRows {
		return claim, lease, false, nil
	}
	return claim, lease, err == nil, err
}

func (s *Postgres) sendWebhook(ctx context.Context, claim deliveryClaim, notification domain.Notification, metadata []byte) error {
	secretValue, err := s.decryptStoredSecret(claim.encryptedSecret)
	if err != nil {
		return err
	}
	secret, ok := secretValue.(string)
	if !ok || secret == "" {
		return errors.New("webhook secret is invalid")
	}
	payload := struct {
		ID, Severity, Category, Title, Message, TargetType, TargetID string
		CreatedAt                                                    time.Time
		Metadata                                                     json.RawMessage
	}{notification.ID, notification.Severity, notification.Category, notification.Title, notification.Message, notification.TargetType, notification.TargetID, notification.CreatedAt, metadata}
	body, _ := json.Marshal(payload)
	timestamp := fmt.Sprint(time.Now().UTC().Unix())
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, claim.endpointURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-GuGu-Timestamp", timestamp)
	request.Header.Set("X-GuGu-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	client, err := guardedWebhookClient(ctx, claim.endpointURL)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 16<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", response.StatusCode)
	}
	return nil
}

func (s *Postgres) decryptStoredSecret(value string) (any, error) {
	s.mu.RLock()
	keyring, legacy := s.secretKeyring, s.secretCipher
	s.mu.RUnlock()
	if keyring != nil {
		return keyring.DecryptValue(value)
	}
	if legacy != nil {
		return legacy.DecryptValue(value)
	}
	return nil, errors.New("secret keyring is unavailable")
}

func guardedWebhookClient(ctx context.Context, rawURL string) (*http.Client, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return nil, errors.New("webhook URL must use HTTPS")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, parsed.Hostname())
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("webhook host cannot be resolved")
	}
	for _, address := range addresses {
		if !publicWebhookIP(address.IP) {
			return nil, errors.New("webhook host resolves to a private address")
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		var last error
		for _, resolved := range addresses {
			connection, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
			if err == nil {
				return connection, nil
			}
			last = err
		}
		return nil, last
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func publicWebhookIP(ip net.IP) bool {
	return ip != nil && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsUnspecified() && !ip.IsMulticast() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast()
}

func truncateError(err error) string {
	value := err.Error()
	if len(value) > 1000 {
		value = value[:1000]
	}
	return value
}

func (s *Postgres) Quota() (domain.Quota, error) {
	var quota domain.Quota
	err := s.db.QueryRow(`SELECT max_nodes, max_servers, max_memory_bytes, max_disk_bytes, max_running_servers, max_concurrent_creates, max_concurrent_backups, max_concurrent_uploads, updated_at FROM workspace_quotas WHERE id='default'`).
		Scan(&quota.MaxNodes, &quota.MaxServers, &quota.MaxMemoryBytes, &quota.MaxDiskBytes, &quota.MaxRunningServers, &quota.MaxConcurrentCreates, &quota.MaxConcurrentBackups, &quota.MaxConcurrentUploads, &quota.UpdatedAt)
	return quota, err
}

func (s *Postgres) PutQuota(input domain.Quota, actor domain.User) (domain.Quota, error) {
	if !hasRole(actor, "platform_admin") {
		return domain.Quota{}, domain.NewProblem("FORBIDDEN", "需要平台管理员权限", false)
	}
	if input.MaxNodes < 1 || input.MaxServers < 1 || input.MaxMemoryBytes < 1 || input.MaxDiskBytes < 1 || input.MaxRunningServers < 1 || input.MaxConcurrentCreates < 1 || input.MaxConcurrentBackups < 1 || input.MaxConcurrentUploads < 1 {
		return domain.Quota{}, domain.NewProblem("VALIDATION_FAILED", "配额必须为正数", false)
	}
	_, err := s.db.Exec(`UPDATE workspace_quotas SET max_nodes=$1,max_servers=$2,max_memory_bytes=$3,max_disk_bytes=$4,max_running_servers=$5,max_concurrent_creates=$6,max_concurrent_backups=$7,max_concurrent_uploads=$8,updated_at=now() WHERE id='default'`,
		input.MaxNodes, input.MaxServers, input.MaxMemoryBytes, input.MaxDiskBytes, input.MaxRunningServers, input.MaxConcurrentCreates, input.MaxConcurrentBackups, input.MaxConcurrentUploads)
	if err != nil {
		return domain.Quota{}, err
	}
	return s.Quota()
}

func (s *Postgres) Capacity() (domain.Capacity, error) {
	var result domain.Capacity
	if err := s.db.QueryRow(`SELECT count(*), COALESCE(sum(memory_bytes),0), COALESCE(sum(disk_bytes),0) FROM nodes WHERE revoked_at IS NULL`).Scan(&result.NodeCount, &result.TotalMemoryBytes, &result.TotalDiskBytes); err != nil {
		return result, err
	}
	if err := s.db.QueryRow(`SELECT count(*), count(*) FILTER (WHERE observed_power='running'), COALESCE(sum(memory_limit_bytes),0), COALESCE(sum(disk_limit_bytes),0) FROM servers WHERE deleted_at IS NULL`).Scan(&result.ServerCount, &result.RunningServerCount, &result.AllocatedMemoryBytes, &result.AllocatedDiskBytes); err != nil {
		return result, err
	}
	quota, err := s.Quota()
	result.Quota = quota
	return result, err
}

func (s *Postgres) SetNodeDrain(nodeID string, draining bool, reason string, actor domain.User) (domain.Node, error) {
	if !hasRole(actor, "platform_admin") {
		return domain.Node{}, domain.NewProblem("FORBIDDEN", "需要平台管理员权限", false)
	}
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) > 500 {
		return domain.Node{}, domain.NewProblem("VALIDATION_FAILED", "排空原因过长", false)
	}
	result, err := s.db.Exec(`
		UPDATE nodes SET drain_mode=$2, drain_reason=CASE WHEN $2 THEN $3 ELSE '' END,
		       drained_at=CASE WHEN $2 THEN COALESCE(drained_at, now()) ELSE NULL END, updated_at=now()
		WHERE id=$1 AND revoked_at IS NULL
	`, nodeID, draining, reason)
	if err != nil {
		return domain.Node{}, domain.NewProblem("INTERNAL_ERROR", "无法更新节点排空状态", true)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return domain.Node{}, domain.NewProblem("NOT_FOUND", "节点不存在", false)
	}
	return s.NodeByID(context.Background(), nodeID)
}

func foreignKeyViolation(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23503"
}
