package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
)

// countOutboxEvents 返回 outbox_events 中给定 event_type 且（可选）
// published 状态的事件数。
func countOutboxEvents(t *testing.T, s *Postgres, eventType string, published bool) int {
	t.Helper()
	var count int
	query := `
		SELECT COUNT(*) FROM outbox_events
		WHERE event_type = $1 AND published_at IS NULL`
	if published {
		query = `
			SELECT COUNT(*) FROM outbox_events
			WHERE event_type = $1 AND published_at IS NOT NULL`
	}
	if err := s.db.QueryRow(query, eventType).Scan(&count); err != nil {
		t.Fatalf("count outbox events: %v", err)
	}
	return count
}

// TestPostgresOutboxTaskLifecycle 验证任务入队写 task.created、完成任务写
// task.completed（与业务同事务），发布器消费后标记 published_at 且幂等。
func TestPostgresOutboxTaskLifecycle(t *testing.T) {
	s := testPostgres(t)
	resetTestDatabase(t, s)
	admin := setupAdminForTest(t, s)

	nodeID, err := s.RegisterNode(context.Background(), domain.Node{
		Name: "outbox-node", Condition: "available", Version: "agent-v1", Region: "cn",
		Address: "127.0.0.3", CPUCores: 4, MemoryBytes: 1 << 32, DiskBytes: 1 << 34,
	})
	if err != nil {
		t.Fatalf("register node: %v", err)
	}
	insertGameFixture(t, s, "pg-outbox-game", testDigestB)
	op, err := s.CreateServer(domain.CreateServerInput{
		Name:             "pg-outbox-server",
		GameDefinitionID: "pg-outbox-game",
		GameBundleDigest: testDigestB,
		NodeID:           nodeID,
		MemoryMB:         512,
		DiskGB:           5,
	}, "idem-outbox-create-01", admin)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	// 入队即产生一条未发布的 task.created，aggregate 归到服务器。
	if got := countOutboxEvents(t, s, "task.created", false); got != 1 {
		t.Fatalf("task.created unpublished = %d, want 1", got)
	}

	// 发布器消费：标记 published 并返回本次发布数量；再次调用无新事件。
	published, err := s.PublishOutboxEvents(context.Background(), 50)
	if err != nil {
		t.Fatalf("publish outbox: %v", err)
	}
	if published != 1 {
		t.Fatalf("published = %d, want 1", published)
	}
	if got := countOutboxEvents(t, s, "task.created", true); got != 1 {
		t.Fatalf("task.created published = %d, want 1", got)
	}
	if again, err := s.PublishOutboxEvents(context.Background(), 50); err != nil || again != 0 {
		t.Fatalf("second publish = %d, %v; want 0, nil", again, err)
	}

	// 完成任务产生一条 task.completed（终态与事件同事务）。
	claimed, err := s.ClaimTask(context.Background(), nodeID, 1)
	if err != nil || claimed == nil {
		t.Fatalf("claim task: %v", err)
	}
	failureCode := "TEST_ERROR"
	fence := TaskLeaseFence{
		OperationID: claimed.OperationID, NodeID: nodeID,
		Epoch: claimed.ConnectionEpoch, Attempt: claimed.Attempt,
		LeaseToken: claimed.LeaseToken,
	}
	if err := s.CompleteTask(context.Background(), fence, false, &failureCode, nil); err != nil {
		t.Fatalf("complete task: %v", err)
	}
	if got := countOutboxEvents(t, s, "task.completed", false); got != 1 {
		t.Fatalf("task.completed unpublished = %d, want 1", got)
	}
	if got := countOutboxEvents(t, s, "task.completed", true); got != 0 {
		t.Fatalf("task.completed published = %d, want 0", got)
	}

	// 事件负载可解析，且携带终态与错误码。
	var payloadBytes []byte
	if err := s.db.QueryRow(`
		SELECT payload FROM outbox_events WHERE event_type = 'task.completed'
	`).Scan(&payloadBytes); err != nil {
		t.Fatalf("read task.completed payload: %v", err)
	}
	var payload taskEventPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatalf("decode task.completed payload: %v", err)
	}
	if payload.OperationID != op.ID || payload.Status != "failed" || payload.ErrorCode != "TEST_ERROR" {
		t.Fatalf("task.completed payload mismatch: %+v", payload)
	}
}

// TestPostgresReconcileTaskLeasesRequeues 验证过期租约回收：Agent 领取后
// 未回报、租约过期的任务重新入队，可再次被领取并累加 attempt。
func TestPostgresReconcileTaskLeasesRequeues(t *testing.T) {
	s := testPostgres(t)
	resetTestDatabase(t, s)
	admin := setupAdminForTest(t, s)

	nodeID, err := s.RegisterNode(context.Background(), domain.Node{
		Name: "lease-node", Condition: "available", Version: "agent-v1", Region: "cn",
		Address: "127.0.0.4", CPUCores: 4, MemoryBytes: 1 << 32, DiskBytes: 1 << 34,
	})
	if err != nil {
		t.Fatalf("register node: %v", err)
	}
	insertGameFixture(t, s, "pg-lease-game", testDigestB)
	op, err := s.CreateServer(domain.CreateServerInput{
		Name:             "pg-lease-server",
		GameDefinitionID: "pg-lease-game",
		GameBundleDigest: testDigestB,
		NodeID:           nodeID,
		MemoryMB:         512,
		DiskGB:           5,
	}, "idem-lease-create-01", admin)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	// 领取后任务进入 leased；模拟 Agent 失联（租约已过期）。
	claimed, err := s.ClaimTask(context.Background(), nodeID, 1)
	if err != nil || claimed == nil {
		t.Fatalf("claim task: %v", err)
	}
	expireLeases(t, s, time.Now().UTC().Add(-time.Second))

	// 回收器把过期任务重新入队。
	s.ReconcileTaskLeases(time.Now().UTC())

	var status string
	if err := s.db.QueryRow(`SELECT status FROM server_tasks WHERE id = $1`, op.ID).Scan(&status); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	if status != "queued" {
		t.Fatalf("task status after reconcile = %q, want queued", status)
	}

	// 重新入队后仍可领取，attempt 累加到 2。
	again, err := s.ClaimTask(context.Background(), nodeID, 1)
	if err != nil || again == nil {
		t.Fatalf("claim task after reconcile: %v", err)
	}
	if again.Attempt != 2 {
		t.Fatalf("claimed attempt = %d, want 2", again.Attempt)
	}
}

// TestPostgresReconcileTaskLeasesFailsAfterMaxAttempts 验证重试耗尽：任务
// 达到 max_attempts 后回收器判终态失败，并写一条 system 审计。
func TestPostgresReconcileTaskLeasesFailsAfterMaxAttempts(t *testing.T) {
	s := testPostgres(t)
	resetTestDatabase(t, s)
	admin := setupAdminForTest(t, s)

	nodeID, err := s.RegisterNode(context.Background(), domain.Node{
		Name: "lease-fail-node", Condition: "available", Version: "agent-v1", Region: "cn",
		Address: "127.0.0.5", CPUCores: 4, MemoryBytes: 1 << 32, DiskBytes: 1 << 34,
	})
	if err != nil {
		t.Fatalf("register node: %v", err)
	}
	insertGameFixture(t, s, "pg-lease-fail-game", testDigestB)
	op, err := s.CreateServer(domain.CreateServerInput{
		Name:             "pg-lease-fail-server",
		GameDefinitionID: "pg-lease-fail-game",
		GameBundleDigest: testDigestB,
		NodeID:           nodeID,
		MemoryMB:         512,
		DiskGB:           5,
	}, "idem-lease-fail-01", admin)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	// 反复领取并让租约过期，直到 attempt 达到 max_attempts（3）。
	for attempt := 1; attempt <= 3; attempt++ {
		claimed, err := s.ClaimTask(context.Background(), nodeID, 1)
		if err != nil || claimed == nil {
			t.Fatalf("claim %d: %v", attempt, err)
		}
		if claimed.Attempt != attempt {
			t.Fatalf("claim %d attempt = %d", attempt, claimed.Attempt)
		}
		expireLeases(t, s, time.Now().UTC().Add(-time.Second))
		s.ReconcileTaskLeases(time.Now().UTC())
	}

	// 重试耗尽后任务为终态失败（MAX_ATTEMPTS 不可重试）。
	var status, errorCode string
	var retryable *bool
	if err := s.db.QueryRow(`
		SELECT status, COALESCE(error_code, ''), error_retryable FROM server_tasks WHERE id = $1
	`, op.ID).Scan(&status, &errorCode, &retryable); err != nil {
		t.Fatalf("read task state: %v", err)
	}
	if status != "failed" || errorCode != "MAX_ATTEMPTS" {
		t.Fatalf("task state after attempts exhausted = %q/%q, want failed/MAX_ATTEMPTS", status, errorCode)
	}
	if retryable == nil || *retryable {
		t.Fatalf("MAX_ATTEMPTS must be non-retryable, got %v", retryable)
	}

	// 判失败伴随 system 审计。
	var auditCount int
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM audit_events WHERE action = 'task.expired' AND operation_id = $1
	`, op.ID).Scan(&auditCount); err != nil {
		t.Fatalf("count task.expired audit: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("task.expired audit count = %d, want 1", auditCount)
	}

	// 判失败后不会再被领取。
	if again, err := s.ClaimTask(context.Background(), nodeID, 1); err != nil || again != nil {
		t.Fatalf("expected no claimable task, got %+v (%v)", again, err)
	}
}

// TestPostgresClaimTaskRespectsMaxAttempts 验证 ClaimTask 不会领取已耗尽
// 重试次数的任务（上限保护，与回收器互为双保险）。
func TestPostgresClaimTaskRespectsMaxAttempts(t *testing.T) {
	s := testPostgres(t)
	resetTestDatabase(t, s)
	admin := setupAdminForTest(t, s)

	nodeID, err := s.RegisterNode(context.Background(), domain.Node{
		Name: "claim-cap-node", Condition: "available", Version: "agent-v1", Region: "cn",
		Address: "127.0.0.6", CPUCores: 4, MemoryBytes: 1 << 32, DiskBytes: 1 << 34,
	})
	if err != nil {
		t.Fatalf("register node: %v", err)
	}
	insertGameFixture(t, s, "pg-claim-cap-game", testDigestB)
	op, err := s.CreateServer(domain.CreateServerInput{
		Name:             "pg-claim-cap-server",
		GameDefinitionID: "pg-claim-cap-game",
		GameBundleDigest: testDigestB,
		NodeID:           nodeID,
		MemoryMB:         512,
		DiskGB:           5,
	}, "idem-claim-cap-01", admin)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	// 把任务直接推到重试上限（模拟重复失联）。
	if _, err := s.db.Exec(`
		UPDATE server_tasks SET attempt = max_attempts WHERE id = $1
	`, op.ID); err != nil {
		t.Fatalf("bump attempt: %v", err)
	}

	if again, err := s.ClaimTask(context.Background(), nodeID, 1); err != nil || again != nil {
		t.Fatalf("expected no claimable task at attempt cap, got %+v (%v)", again, err)
	}
}

// expireLeases 把全部 leased 任务的租约提前到给定时刻，模拟 Agent 失联。
func expireLeases(t *testing.T, s *Postgres, at time.Time) {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE server_tasks SET lease_expires_at = $1 WHERE status = 'leased'`, at); err != nil {
		t.Fatalf("expire leases: %v", err)
	}
}
