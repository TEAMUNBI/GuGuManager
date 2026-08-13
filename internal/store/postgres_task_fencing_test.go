package store

import (
	"context"
	"testing"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
)

// TestPostgresTaskFenceRejectsForgedAndStaleMessages 验证 000009 的栅栏语义：
// 伪造租约凭据、旧 attempt、旧 epoch 的消息只能成为 no-op；精确匹配的
// Ack/Progress/Result 才推进状态；终态不可被迟到结果覆盖；state_version
// 单调递增；任务输入与 checkpoint 分离。
func TestPostgresTaskFenceRejectsForgedAndStaleMessages(t *testing.T) {
	s := testPostgres(t)
	resetTestDatabase(t, s)
	admin := setupAdminForTest(t, s)

	nodeID, err := s.RegisterNode(context.Background(), domain.Node{
		Name: "fence-node", Condition: "available", Version: "agent-v1", Region: "cn",
		Address: "127.0.0.7", CPUCores: 4, MemoryBytes: 1 << 32, DiskBytes: 1 << 34,
	})
	if err != nil {
		t.Fatalf("register node: %v", err)
	}
	insertGameFixture(t, s, "pg-fence-game", testDigestB)
	op, err := s.CreateServer(domain.CreateServerInput{
		Name:             "pg-fence-server",
		GameDefinitionID: "pg-fence-game",
		GameBundleDigest: testDigestB,
		NodeID:           nodeID,
		MemoryMB:         512,
		DiskGB:           5,
	}, "idem-fence-create-01", admin)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	claimed, err := s.ClaimTask(context.Background(), nodeID, 7)
	if err != nil || claimed == nil {
		t.Fatalf("claim task: %v", err)
	}
	if claimed.OperationID != op.ID || claimed.LeaseToken == "" || claimed.ConnectionEpoch != 7 || claimed.Attempt != 1 {
		t.Fatalf("claimed task fence fields wrong: %+v", claimed)
	}

	taskStatus := func() string {
		t.Helper()
		var status string
		if err := s.db.QueryRow(`SELECT status FROM server_tasks WHERE id = $1`, op.ID).Scan(&status); err != nil {
			t.Fatalf("read task status: %v", err)
		}
		return status
	}
	taskProgress := func() int {
		t.Helper()
		var progress int
		if err := s.db.QueryRow(`SELECT progress FROM server_tasks WHERE id = $1`, op.ID).Scan(&progress); err != nil {
			t.Fatalf("read task progress: %v", err)
		}
		return progress
	}
	taskStateVersion := func() int64 {
		t.Helper()
		var version int64
		if err := s.db.QueryRow(`SELECT state_version FROM server_tasks WHERE id = $1`, op.ID).Scan(&version); err != nil {
			t.Fatalf("read task state_version: %v", err)
		}
		return version
	}

	fence := TaskLeaseFence{OperationID: op.ID, NodeID: nodeID, Epoch: 7, Attempt: 1, LeaseToken: claimed.LeaseToken}
	forged := fence
	forged.LeaseToken = "forged-token"
	staleAttempt := fence
	staleAttempt.Attempt = 0
	staleEpoch := fence
	staleEpoch.Epoch = 6

	// 伪造租约凭据的 ack：no-op，任务保持 leased。
	if err := s.AckTask(context.Background(), forged, true, ""); err != nil {
		t.Fatalf("forged ack returned error: %v", err)
	}
	if taskStatus() != "leased" {
		t.Fatalf("forged ack changed status to %q", taskStatus())
	}

	// 旧 attempt 与旧 epoch 的进度：no-op。
	if err := s.ReportTaskProgress(context.Background(), staleAttempt, 50, "half"); err != nil {
		t.Fatalf("stale attempt progress returned error: %v", err)
	}
	if err := s.ReportTaskProgress(context.Background(), staleEpoch, 50, "half"); err != nil {
		t.Fatalf("stale epoch progress returned error: %v", err)
	}
	if taskProgress() != 0 {
		t.Fatalf("stale progress changed progress to %d", taskProgress())
	}

	// 精确匹配的 ack：leased -> running，state_version 单调 +1。
	versionBefore := taskStateVersion()
	if err := s.AckTask(context.Background(), fence, true, ""); err != nil {
		t.Fatalf("matched ack returned error: %v", err)
	}
	if taskStatus() != "running" {
		t.Fatalf("matched ack left status %q, want running", taskStatus())
	}
	if taskStateVersion() <= versionBefore {
		t.Fatal("state_version did not advance on ack")
	}

	// 进度单调推进，checkpoint 写入 checkpoint 列，task_input 保持不变。
	if err := s.ReportTaskProgress(context.Background(), fence, 50, "half"); err != nil {
		t.Fatalf("progress returned error: %v", err)
	}
	if err := s.ReportTaskProgress(context.Background(), fence, 30, "third"); err != nil {
		t.Fatalf("second progress returned error: %v", err)
	}
	if taskProgress() != 50 {
		t.Fatalf("progress regressed to %d", taskProgress())
	}
	var checkpoint, taskInput string
	if err := s.db.QueryRow(`
		SELECT COALESCE(checkpoint #>> '{}', ''), COALESCE(task_input::text, '')
		FROM server_tasks WHERE id = $1`, op.ID).Scan(&checkpoint, &taskInput); err != nil {
		t.Fatalf("read checkpoint/input: %v", err)
	}
	if checkpoint != "third" {
		t.Fatalf("checkpoint = %q, want third", checkpoint)
	}
	if taskInput == "" {
		t.Fatal("task_input lost after progress frames")
	}

	// 精确匹配的结果：running -> succeeded，服务器推进到 ready。
	if err := s.CompleteTask(context.Background(), fence, true, nil, nil); err != nil {
		t.Fatalf("complete task: %v", err)
	}
	if taskStatus() != "succeeded" {
		t.Fatalf("status after result = %q, want succeeded", taskStatus())
	}
	server, err := s.Server(claimed.ServerID)
	if err != nil {
		t.Fatalf("read server: %v", err)
	}
	if server.LifecycleState != "ready" {
		t.Fatalf("server lifecycle after provision = %q, want ready", server.LifecycleState)
	}

	// 终态后的迟到结果（即使栅栏精确匹配）只能成为 no-op。
	if err := s.CompleteTask(context.Background(), fence, false, nil, nil); err != nil {
		t.Fatalf("late result returned error: %v", err)
	}
	if taskStatus() != "succeeded" {
		t.Fatalf("late result overwrote terminal state: %q", taskStatus())
	}
}

// TestPostgresTaskLeaseExpiryRevokesToken 验证租约过期路径：回收器把过期
// 任务重新入队并吊销租约凭据，旧凭据的迟到消息无法影响任务；下一次领取
// 生成新凭据并绑定新 epoch。
func TestPostgresTaskLeaseExpiryRevokesToken(t *testing.T) {
	s := testPostgres(t)
	resetTestDatabase(t, s)
	admin := setupAdminForTest(t, s)

	nodeID, err := s.RegisterNode(context.Background(), domain.Node{
		Name: "fence-expiry-node", Condition: "available", Version: "agent-v1", Region: "cn",
		Address: "127.0.0.8", CPUCores: 4, MemoryBytes: 1 << 32, DiskBytes: 1 << 34,
	})
	if err != nil {
		t.Fatalf("register node: %v", err)
	}
	insertGameFixture(t, s, "pg-fence-expiry-game", testDigestB)
	op, err := s.CreateServer(domain.CreateServerInput{
		Name:             "pg-fence-expiry-server",
		GameDefinitionID: "pg-fence-expiry-game",
		GameBundleDigest: testDigestB,
		NodeID:           nodeID,
		MemoryMB:         512,
		DiskGB:           5,
	}, "idem-fence-expiry-01", admin)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	first, err := s.ClaimTask(context.Background(), nodeID, 7)
	if err != nil || first == nil {
		t.Fatalf("first claim: %v", err)
	}
	expireLeases(t, s, time.Now().UTC().Add(-time.Second))
	s.ReconcileTaskLeases(time.Now().UTC())

	var status string
	var leaseToken *string
	if err := s.db.QueryRow(`SELECT status, lease_token::text FROM server_tasks WHERE id = $1`, op.ID).Scan(&status, &leaseToken); err != nil {
		t.Fatalf("read requeued task: %v", err)
	}
	if status != "queued" {
		t.Fatalf("status after reconcile = %q, want queued", status)
	}
	if leaseToken != nil && *leaseToken != "" {
		t.Fatalf("requeued task retained lease token %q", *leaseToken)
	}

	// 旧凭据的 ack/result 在重新入队后都是 no-op。
	stale := TaskLeaseFence{OperationID: op.ID, NodeID: nodeID, Epoch: 7, Attempt: 1, LeaseToken: first.LeaseToken}
	if err := s.AckTask(context.Background(), stale, true, ""); err != nil {
		t.Fatalf("stale ack after requeue: %v", err)
	}
	if err := s.CompleteTask(context.Background(), stale, true, nil, nil); err != nil {
		t.Fatalf("stale result after requeue: %v", err)
	}
	var after string
	if err := s.db.QueryRow(`SELECT status FROM server_tasks WHERE id = $1`, op.ID).Scan(&after); err != nil {
		t.Fatalf("read status after stale messages: %v", err)
	}
	if after != "queued" {
		t.Fatalf("stale message moved requeued task to %q", after)
	}

	// 新连接（epoch 8）重新领取：新凭据、attempt 累加，旧凭据无法推进。
	second, err := s.ClaimTask(context.Background(), nodeID, 8)
	if err != nil || second == nil {
		t.Fatalf("second claim: %v", err)
	}
	if second.Attempt != 2 || second.ConnectionEpoch != 8 || second.LeaseToken == first.LeaseToken {
		t.Fatalf("second claim fence wrong: %+v", second)
	}
	if err := s.AckTask(context.Background(), stale, true, ""); err != nil {
		t.Fatalf("old-token ack after second claim: %v", err)
	}
	if err := s.db.QueryRow(`SELECT status FROM server_tasks WHERE id = $1`, op.ID).Scan(&after); err != nil {
		t.Fatalf("read status after old-token ack: %v", err)
	}
	if after != "leased" {
		t.Fatalf("old-token ack advanced second claim to %q", after)
	}
	current := TaskLeaseFence{OperationID: op.ID, NodeID: nodeID, Epoch: 8, Attempt: 2, LeaseToken: second.LeaseToken}
	if err := s.AckTask(context.Background(), current, true, ""); err != nil {
		t.Fatalf("current ack: %v", err)
	}
	if err := s.db.QueryRow(`SELECT status FROM server_tasks WHERE id = $1`, op.ID).Scan(&after); err != nil {
		t.Fatalf("read status after current ack: %v", err)
	}
	if after != "running" {
		t.Fatalf("current ack left status %q, want running", after)
	}
}
