BEGIN;

-- 服务器最新一次指标快照（每服务器一行，Agent MetricsBatch 帧 upsert）。
-- 供概览页聚合 CPU/内存用量，并在控制面重启后恢复实时指标。
CREATE TABLE IF NOT EXISTS server_metrics (
    server_id uuid PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    observed_generation bigint NOT NULL DEFAULT 0,
    cpu_percent double precision NOT NULL DEFAULT 0,
    memory_bytes bigint NOT NULL DEFAULT 0,
    memory_limit_bytes bigint NOT NULL DEFAULT 0,
    disk_bytes bigint NOT NULL DEFAULT 0,
    disk_limit_bytes bigint NOT NULL DEFAULT 0,
    network_rx_bytes bigint NOT NULL DEFAULT 0,
    network_tx_bytes bigint NOT NULL DEFAULT 0,
    players_online integer NOT NULL DEFAULT 0,
    players_max integer NOT NULL DEFAULT 0,
    observed_at timestamptz NOT NULL
);

-- 指标历史点（每服务器最多保留 metricHistoryPoints 个，图表与重启恢复用）。
CREATE TABLE IF NOT EXISTS server_metric_history (
    server_id uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    observed_at timestamptz NOT NULL,
    cpu_percent double precision NOT NULL DEFAULT 0,
    memory_bytes bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (server_id, observed_at)
);

CREATE INDEX IF NOT EXISTS server_metric_history_lookup_idx
    ON server_metric_history (server_id, observed_at DESC);

-- 控制台日志（Agent LogBatch 帧写入，重启后恢复最近 consoleBufferLimit 行）。
CREATE TABLE IF NOT EXISTS console_logs (
    server_id uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    sequence bigint NOT NULL,
    stream text NOT NULL DEFAULT 'stdout',
    message text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (server_id, sequence)
);

CREATE INDEX IF NOT EXISTS console_logs_lookup_idx
    ON console_logs (server_id, sequence DESC);

COMMIT;
