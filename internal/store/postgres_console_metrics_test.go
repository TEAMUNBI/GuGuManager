package store

import (
	"context"
	"testing"
	"time"

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

func TestPostgresApplyServerMetricsAndMerge(t *testing.T) {
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
	server, err := s.Server(serverID)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	if server.Metrics.CPUPercent != 42.5 {
		t.Fatalf("metrics cpu = %v, want 42.5", server.Metrics.CPUPercent)
	}
	if server.Metrics.PlayersOnline != 3 || server.Metrics.PlayersCapacity != 20 {
		t.Fatalf("metrics players = %d/%d, want 3/20", server.Metrics.PlayersOnline, server.Metrics.PlayersCapacity)
	}
	if len(server.MetricHistory) < 1 {
		t.Fatalf("metric history empty, want >=1")
	}
}
