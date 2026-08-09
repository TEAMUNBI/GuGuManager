package store

import (
	"context"
	"sync"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
)

const (
	consoleBufferLimit  = 500
	metricHistoryPoints = 60
	metricHistoryWindow = 5 * time.Minute
)

// consoleBuffer 是单台服务器的日志环形缓冲（内存态，重启丢失）。
type consoleBuffer struct {
	mu    sync.Mutex
	lines []domain.ConsoleLine
	next  int64
}

// metricState 是单台服务器的指标当前值与历史 ring buffer（内存态）。
type metricState struct {
	mu      sync.Mutex
	current domain.ServerMetrics
	history []domain.ServerMetrics
}

func (s *Postgres) consoleBufferFor(serverID string) *consoleBuffer {
	s.bufMu.Lock()
	defer s.bufMu.Unlock()
	buf := s.consoleBuffers[serverID]
	if buf == nil {
		buf = &consoleBuffer{next: 1}
		s.consoleBuffers[serverID] = buf
	}
	return buf
}

func (s *Postgres) metricStateFor(serverID string) *metricState {
	s.bufMu.Lock()
	defer s.bufMu.Unlock()
	state := s.metricStates[serverID]
	if state == nil {
		state = &metricState{}
		s.metricStates[serverID] = state
	}
	return state
}

// RecordConsoleLines 把 Agent 上报的日志行追加进服务器缓冲，超出上限丢弃最旧行。
func (s *Postgres) RecordConsoleLines(ctx context.Context, serverID string, lines []domain.ConsoleLine) error {
	buf := s.consoleBufferFor(serverID)
	buf.mu.Lock()
	defer buf.mu.Unlock()
	for _, line := range lines {
		if line.Sequence <= 0 {
			line.Sequence = buf.next
		}
		buf.next = line.Sequence + 1
		buf.lines = append(buf.lines, line)
	}
	if len(buf.lines) > consoleBufferLimit {
		buf.lines = append([]domain.ConsoleLine(nil), buf.lines[len(buf.lines)-consoleBufferLimit:]...)
	}
	return nil
}

// ApplyServerMetrics 更新服务器指标当前值并追加历史点（时间窗内、上限 60 点）。
func (s *Postgres) ApplyServerMetrics(ctx context.Context, metrics []domain.ServerMetrics) error {
	for _, m := range metrics {
		if m.ServerID == "" {
			continue
		}
		state := s.metricStateFor(m.ServerID)
		state.mu.Lock()
		if m.ObservedAt.IsZero() {
			m.ObservedAt = time.Now().UTC()
		}
		state.current = m
		cutoff := m.ObservedAt.Add(-metricHistoryWindow)
		kept := state.history[:0]
		for _, point := range state.history {
			if point.ObservedAt.After(cutoff) {
				kept = append(kept, point)
			}
		}
		state.history = append(kept, m)
		if len(state.history) > metricHistoryPoints {
			state.history = state.history[len(state.history)-metricHistoryPoints:]
		}
		state.mu.Unlock()
	}
	return nil
}

// appendMetricsToServer 把内存指标合并进 domain.Server（scanServer 后调用）。
// 无任何上报时保持零值，避免覆盖磁盘限制等 DB 字段。
func (s *Postgres) appendMetricsToServer(server *domain.Server) {
	state := s.metricStateFor(server.ID)
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.current.ObservedAt.IsZero() {
		server.Metrics.CPUPercent = state.current.CPUPercent
		server.Metrics.MemoryBytes = state.current.MemoryBytes
		server.Metrics.MemoryLimit = state.current.MemoryLimitBytes
		server.Metrics.DiskBytes = state.current.DiskBytes
		server.Metrics.DiskLimit = state.current.DiskLimitBytes
		server.Metrics.NetworkRxBytes = state.current.NetworkRxBytes
		server.Metrics.NetworkTxBytes = state.current.NetworkTxBytes
		server.Metrics.PlayersOnline = state.current.PlayersOnline
		server.Metrics.PlayersCapacity = state.current.PlayersMax
		server.ObservedGeneration = state.current.ObservedGeneration
	}
	if len(state.history) > 0 {
		history := make([]domain.MetricPoint, 0, len(state.history))
		for _, point := range state.history {
			history = append(history, domain.MetricPoint{
				Timestamp:   point.ObservedAt,
				CPUPercent:  point.CPUPercent,
				MemoryBytes: point.MemoryBytes,
			})
		}
		server.MetricHistory = history
	}
}
