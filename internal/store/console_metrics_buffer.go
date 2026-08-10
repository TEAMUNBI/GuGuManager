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

// RecordConsoleLines 把 Agent 上报的日志行追加进服务器缓冲（超出上限丢弃最旧行），
// 并批量持久化到 console_logs 表，使控制面重启后仍能恢复最近日志。
func (s *Postgres) RecordConsoleLines(ctx context.Context, serverID string, lines []domain.ConsoleLine) error {
	buf := s.consoleBufferFor(serverID)
	buf.mu.Lock()
	rows := make([][]any, 0, len(lines))
	for _, line := range lines {
		if line.Sequence <= 0 {
			line.Sequence = buf.next
		}
		buf.next = line.Sequence + 1
		buf.lines = append(buf.lines, line)
		rows = append(rows, []any{serverID, line.Sequence, line.Stream, line.Message, line.Timestamp})
	}
	if len(buf.lines) > consoleBufferLimit {
		buf.lines = append([]domain.ConsoleLine(nil), buf.lines[len(buf.lines)-consoleBufferLimit:]...)
	}
	buf.mu.Unlock()

	if len(rows) == 0 {
		return nil
	}
	if err := s.persistConsoleLines(ctx, rows); err != nil {
		return err
	}
	// 裁剪：每服务器仅保留最近 consoleBufferLimit 行，防止日志表无限膨胀。
	_, _ = s.db.ExecContext(ctx, `
		DELETE FROM console_logs
		WHERE server_id = $1 AND sequence < (
			SELECT MIN(sequence) FROM (
				SELECT sequence FROM console_logs WHERE server_id = $1
				ORDER BY sequence DESC LIMIT $2
			) keep
		)
	`, serverID, consoleBufferLimit)
	return nil
}

// persistConsoleLines 批量写入日志行（幂等，重复 sequence 忽略）。
func (s *Postgres) persistConsoleLines(ctx context.Context, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	const batch = 200
	for start := 0; start < len(rows); start += batch {
		end := min(start+batch, len(rows))
		stmt := `INSERT INTO console_logs (server_id, sequence, stream, message, created_at)
			VALUES ($1, $2, $3, $4, $5) ON CONFLICT (server_id, sequence) DO NOTHING`
		for _, row := range rows[start:end] {
			if _, err := s.db.ExecContext(ctx, stmt, row...); err != nil {
				return err
			}
		}
	}
	return nil
}

// ApplyServerMetrics 更新服务器指标当前值并追加历史点（时间窗内、上限 60 点），
// 同时把最新快照 upsert 到 server_metrics、历史点写入 server_metric_history，
// 使控制面重启后指标不丢失。
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

		if err := s.persistMetrics(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// persistMetrics 把单条指标写入 server_metrics（upsert）与 server_metric_history，
// 并裁剪每服务器历史点数量，避免表无限膨胀。
func (s *Postgres) persistMetrics(ctx context.Context, m domain.ServerMetrics) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO server_metrics (
			server_id, observed_generation, cpu_percent, memory_bytes, memory_limit_bytes,
			disk_bytes, disk_limit_bytes, network_rx_bytes, network_tx_bytes,
			players_online, players_max, observed_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (server_id) DO UPDATE SET
			observed_generation = EXCLUDED.observed_generation,
			cpu_percent = EXCLUDED.cpu_percent,
			memory_bytes = EXCLUDED.memory_bytes,
			memory_limit_bytes = EXCLUDED.memory_limit_bytes,
			disk_bytes = EXCLUDED.disk_bytes,
			disk_limit_bytes = EXCLUDED.disk_limit_bytes,
			network_rx_bytes = EXCLUDED.network_rx_bytes,
			network_tx_bytes = EXCLUDED.network_tx_bytes,
			players_online = EXCLUDED.players_online,
			players_max = EXCLUDED.players_max,
			observed_at = EXCLUDED.observed_at
	`, m.ServerID, m.ObservedGeneration, m.CPUPercent, m.MemoryBytes, m.MemoryLimitBytes,
		m.DiskBytes, m.DiskLimitBytes, m.NetworkRxBytes, m.NetworkTxBytes,
		m.PlayersOnline, m.PlayersMax, m.ObservedAt); err != nil {
		return err
	}

	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO server_metric_history (server_id, observed_at, cpu_percent, memory_bytes)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (server_id, observed_at) DO UPDATE SET
			cpu_percent = EXCLUDED.cpu_percent, memory_bytes = EXCLUDED.memory_bytes
	`, m.ServerID, m.ObservedAt, m.CPUPercent, m.MemoryBytes); err != nil {
		return err
	}

	// 裁剪历史：每服务器仅保留最近 metricHistoryPoints 个点。
	_, _ = s.db.ExecContext(ctx, `
		DELETE FROM server_metric_history
		WHERE server_id = $1 AND observed_at < (
			SELECT MIN(observed_at) FROM (
				SELECT observed_at FROM server_metric_history WHERE server_id = $1
				ORDER BY observed_at DESC LIMIT $2
			) keep
		)
	`, m.ServerID, metricHistoryPoints)
	return nil
}

// RestoreTelemetry 从 DB 恢复控制台日志与指标到内存缓冲（启动时调用一次），
// 使控制面重启后实时监控数据不丢失。无任何上报记录时静默返回。
func (s *Postgres) RestoreTelemetry(ctx context.Context) {
	s.restoreConsoleLines(ctx)
	s.restoreMetrics(ctx)
}

func (s *Postgres) restoreConsoleLines(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT server_id, sequence, stream, message, created_at
		FROM console_logs
		ORDER BY server_id, sequence DESC
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	type record struct {
		serverID string
		line     domain.ConsoleLine
	}
	var records []record
	for rows.Next() {
		var serverID, stream, message string
		var sequence int64
		var createdAt time.Time
		if err := rows.Scan(&serverID, &sequence, &stream, &message, &createdAt); err != nil {
			continue
		}
		records = append(records, record{serverID: serverID, line: domain.ConsoleLine{
			Sequence: sequence, Timestamp: createdAt, Stream: stream, Message: message,
		}})
	}
	// 倒序恢复：同一服务器内 sequence 升序，next 从最大 sequence+1 继续。
	for i := len(records) - 1; i >= 0; i-- {
		rec := records[i]
		buf := s.consoleBufferFor(rec.serverID)
		buf.mu.Lock()
		buf.lines = append(buf.lines, rec.line)
		if rec.line.Sequence >= buf.next {
			buf.next = rec.line.Sequence + 1
		}
		if len(buf.lines) > consoleBufferLimit {
			buf.lines = append([]domain.ConsoleLine(nil), buf.lines[len(buf.lines)-consoleBufferLimit:]...)
		}
		buf.mu.Unlock()
	}
}

func (s *Postgres) restoreMetrics(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT server_id, observed_generation, cpu_percent, memory_bytes, memory_limit_bytes,
		       disk_bytes, disk_limit_bytes, network_rx_bytes, network_tx_bytes,
		       players_online, players_max, observed_at
		FROM server_metrics
	`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var m domain.ServerMetrics
		var observedAt time.Time
		if err := rows.Scan(&m.ServerID, &m.ObservedGeneration, &m.CPUPercent, &m.MemoryBytes,
			&m.MemoryLimitBytes, &m.DiskBytes, &m.DiskLimitBytes, &m.NetworkRxBytes,
			&m.NetworkTxBytes, &m.PlayersOnline, &m.PlayersMax, &observedAt); err != nil {
			continue
		}
		m.ObservedAt = observedAt
		s.restoreMetricHistory(m)
	}
}

// restoreMetricHistory 恢复单台服务器的当前值与历史点。
func (s *Postgres) restoreMetricHistory(current domain.ServerMetrics) {
	state := s.metricStateFor(current.ServerID)
	state.mu.Lock()
	defer state.mu.Unlock()
	state.current = current
	state.history = state.history[:0]

	rows, err := s.db.QueryContext(context.Background(), `
		SELECT observed_at, cpu_percent, memory_bytes
		FROM server_metric_history
		WHERE server_id = $1
		ORDER BY observed_at DESC
		LIMIT $2
	`, current.ServerID, metricHistoryPoints)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var point domain.ServerMetrics
		var observedAt time.Time
		if err := rows.Scan(&observedAt, &point.CPUPercent, &point.MemoryBytes); err != nil {
			continue
		}
		point.ObservedAt = observedAt
		state.history = append(state.history, point)
	}
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
