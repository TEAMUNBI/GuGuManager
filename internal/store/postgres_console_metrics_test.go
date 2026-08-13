package store

import (
	"context"
	"testing"
	"time"

	"github.com/gugumanager/gugumanager/internal/config"
	"github.com/gugumanager/gugumanager/internal/domain"
)

func TestPostgresConsoleBufferAppendAndLimit(t *testing.T) {
	s := testPostgres(t)
	resetTestDatabase(t, s)
	admin := setupAdminForTest(t, s)
	serverID := createServerFixture(t, s, admin.ID)

	lines := make([]domain.ConsoleLine, 0, 1200)
	for i := 0; i < 1200; i++ {
		lines = append(lines, domain.ConsoleLine{Sequence: int64(i + 1), Timestamp: time.Now().UTC(), Stream: "stdout", Message: "line"})
	}
	if err := s.RecordConsoleLines(context.Background(), serverID, lines); err != nil {
		t.Fatalf("record console lines: %v", err)
	}
	got, err := s.Console(serverID)
	if err != nil {
		t.Fatalf("console: %v", err)
	}
	if len(got) != 500 {
		t.Fatalf("buffer length = %d, want 500", len(got))
	}
	if got[0].Sequence != 701 {
		t.Fatalf("first buffered sequence = %d, want 701", got[0].Sequence)
	}
}

func TestPostgresConsoleInsertFailureDoesNotMutateBufferOrBroadcast(t *testing.T) {
	s := testPostgres(t)
	resetTestDatabase(t, s)
	serverID := "ffffffff-ffff-4fff-8fff-ffffffffffff"
	stream, cancel := s.SubscribeConsoleLines(serverID)
	defer cancel()

	// The missing server forces an FK insert failure after sequence assignment
	// but before any process-local visibility is allowed.
	err := s.RecordConsoleLines(context.Background(), serverID, []domain.ConsoleLine{{
		Timestamp: time.Now().UTC(), Stream: "stdout", Message: "must stay private",
	}})
	if err == nil {
		t.Fatal("foreign-key-invalid console line unexpectedly committed")
	}
	buf := s.consoleBufferFor(serverID)
	buf.mu.Lock()
	if len(buf.lines) != 0 || buf.next != 1 {
		t.Fatalf("buffer after rollback = lines:%+v next:%d, want empty/1", buf.lines, buf.next)
	}
	buf.mu.Unlock()
	select {
	case line := <-stream:
		t.Fatalf("subscriber received uncommitted line: %+v", line)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestPostgresConsoleDuplicateSequenceIsNotBufferedOrBroadcast(t *testing.T) {
	s := testPostgres(t)
	resetTestDatabase(t, s)
	admin := setupAdminForTest(t, s)
	serverID := createServerFixture(t, s, admin.ID)
	now := time.Now().UTC()
	if err := s.RecordConsoleLines(context.Background(), serverID, []domain.ConsoleLine{{
		Sequence: 7, Timestamp: now, Stream: "stdout", Message: "first",
	}}); err != nil {
		t.Fatalf("record first line: %v", err)
	}
	stream, cancel := s.SubscribeConsoleLines(serverID)
	defer cancel()
	if err := s.RecordConsoleLines(context.Background(), serverID, []domain.ConsoleLine{{
		Sequence: 7, Timestamp: now.Add(time.Second), Stream: "stdout", Message: "duplicate",
	}}); err != nil {
		t.Fatalf("record duplicate line: %v", err)
	}
	select {
	case line := <-stream:
		t.Fatalf("duplicate DB row was broadcast: %+v", line)
	case <-time.After(50 * time.Millisecond):
	}
	got, err := s.Console(serverID)
	if err != nil {
		t.Fatalf("console: %v", err)
	}
	if len(got) != 1 || got[0].Message != "first" {
		t.Fatalf("buffer after duplicate = %+v, want only first row", got)
	}
}

func TestPostgresConsoleBufferAutoSequence(t *testing.T) {
	s := testPostgres(t)
	resetTestDatabase(t, s)
	admin := setupAdminForTest(t, s)
	serverID := createServerFixture(t, s, admin.ID)

	// 无 sequence 的行由缓冲自增分配。
	if err := s.RecordConsoleLines(context.Background(), serverID, []domain.ConsoleLine{
		{Timestamp: time.Now().UTC(), Stream: "stdout", Message: "a"},
		{Timestamp: time.Now().UTC(), Stream: "stdout", Message: "b"},
	}); err != nil {
		t.Fatalf("record console lines: %v", err)
	}
	got, err := s.Console(serverID)
	if err != nil {
		t.Fatalf("console: %v", err)
	}
	if got[0].Sequence != 1 || got[1].Sequence != 2 {
		t.Fatalf("auto sequences = %d/%d, want 1/2", got[0].Sequence, got[1].Sequence)
	}
}

func TestPostgresMetricsAndConsolePersistAcrossRestart(t *testing.T) {
	s := testPostgres(t)
	resetTestDatabase(t, s)
	admin := setupAdminForTest(t, s)
	serverID := createServerFixture(t, s, admin.ID)

	now := time.Now().UTC()
	if err := s.ApplyServerMetrics(context.Background(), []domain.ServerMetrics{{
		ServerID: serverID, ObservedGeneration: 1, CPUPercent: 42.5, MemoryBytes: 512 << 20,
		MemoryLimitBytes: 1024 << 20, PlayersOnline: 3, PlayersMax: 20, ObservedAt: now,
	}}); err != nil {
		t.Fatalf("apply metrics: %v", err)
	}
	if err := s.RecordConsoleLines(context.Background(), serverID, []domain.ConsoleLine{
		{Timestamp: now, Stream: "stdout", Message: "line-a"},
		{Timestamp: now, Stream: "stdout", Message: "line-b"},
	}); err != nil {
		t.Fatalf("record console lines: %v", err)
	}

	// 模拟控制面重启：用同一数据库新建 store，调用 RestoreTelemetry 恢复。
	dsn := testDatabaseDSN()
	restarted, err := NewPostgres(context.Background(), dsn, config.Production, "test-agent-token-1234567890", "")
	if err != nil {
		t.Fatalf("new postgres after restart: %v", err)
	}
	t.Cleanup(func() { restarted.Close() })
	restarted.RestoreTelemetry(context.Background())

	server, err := restarted.Server(serverID)
	if err != nil {
		t.Fatalf("server after restart: %v", err)
	}
	if server.Metrics.CPUPercent != 42.5 {
		t.Fatalf("restored cpu = %v, want 42.5", server.Metrics.CPUPercent)
	}
	if server.Metrics.PlayersOnline != 3 {
		t.Fatalf("restored players online = %d, want 3", server.Metrics.PlayersOnline)
	}
	if len(server.MetricHistory) < 1 {
		t.Fatalf("restored metric history empty, want >=1")
	}

	lines, err := restarted.Console(serverID)
	if err != nil {
		t.Fatalf("console after restart: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("restored console lines = %d, want 2", len(lines))
	}
	if lines[0].Message != "line-a" || lines[1].Message != "line-b" {
		t.Fatalf("restored messages = %q/%q, want line-a/line-b", lines[0].Message, lines[1].Message)
	}

	// 恢复后继续追加日志，sequence 应从 DB 最大值之后继续。
	if err := restarted.RecordConsoleLines(context.Background(), serverID, []domain.ConsoleLine{
		{Timestamp: time.Now().UTC(), Stream: "stdout", Message: "line-c"},
	}); err != nil {
		t.Fatalf("record after restart: %v", err)
	}
	lines, err = restarted.Console(serverID)
	if err != nil {
		t.Fatalf("console after append: %v", err)
	}
	if len(lines) != 3 || lines[2].Message != "line-c" {
		t.Fatalf("appended lines = %d (last %q), want 3 with line-c", len(lines), lines[len(lines)-1].Message)
	}
	if lines[0].Sequence != 1 || lines[2].Sequence != 3 {
		t.Fatalf("sequences after restart = %d..%d, want 1..3", lines[0].Sequence, lines[2].Sequence)
	}
}
