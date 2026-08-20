BEGIN;

ALTER TABLE nodes
    ADD COLUMN architecture text NOT NULL DEFAULT 'linux/amd64',
    ADD COLUMN drain_mode boolean NOT NULL DEFAULT false,
    ADD COLUMN drain_reason text NOT NULL DEFAULT '',
    ADD COLUMN drained_at timestamptz;

CREATE TABLE schedules (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id uuid REFERENCES servers(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 128),
    action text NOT NULL CHECK (action IN ('backup', 'start', 'stop', 'restart', 'retention-cleanup')),
    cron_expression text NOT NULL,
    timezone text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    missed_run_policy text NOT NULL DEFAULT 'skip' CHECK (missed_run_policy IN ('skip', 'run-once', 'catch-up')),
    concurrency_policy text NOT NULL DEFAULT 'forbid' CHECK (concurrency_policy IN ('forbid', 'allow', 'replace')),
    next_run_at timestamptz,
    last_scheduled_at timestamptz,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (server_id IS NOT NULL OR action = 'retention-cleanup')
);

CREATE INDEX schedules_due_idx ON schedules (next_run_at) WHERE enabled = true;

CREATE TABLE schedule_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    schedule_id uuid NOT NULL REFERENCES schedules(id) ON DELETE CASCADE,
    scheduled_for timestamptz NOT NULL,
    status text NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'skipped')),
    operation_id uuid,
    failure_code text,
    started_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (schedule_id, scheduled_for)
);

CREATE TABLE webhook_endpoints (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 128),
    url text NOT NULL CHECK (url ~ '^https://'),
    secret_ciphertext text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE notifications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    severity text NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
    category text NOT NULL,
    title text NOT NULL,
    message text NOT NULL,
    target_type text NOT NULL,
    target_id text NOT NULL,
    dedupe_key text NOT NULL UNIQUE,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    acknowledged_at timestamptz,
    acknowledged_by uuid REFERENCES users(id) ON DELETE SET NULL
);

CREATE TABLE notification_deliveries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    notification_id uuid NOT NULL REFERENCES notifications(id) ON DELETE CASCADE,
    webhook_endpoint_id uuid REFERENCES webhook_endpoints(id) ON DELETE CASCADE,
    channel text NOT NULL CHECK (channel IN ('in-app', 'webhook')),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'leased', 'delivered', 'failed', 'dead-letter')),
    attempt integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_owner uuid,
    lease_until timestamptz,
    response_code integer,
    last_error text,
    delivered_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (notification_id, webhook_endpoint_id, channel)
);

CREATE INDEX notification_delivery_pending_idx ON notification_deliveries (next_attempt_at)
    WHERE status IN ('pending', 'failed');

CREATE TABLE workspace_quotas (
    id text PRIMARY KEY DEFAULT 'default' CHECK (id = 'default'),
    max_nodes integer NOT NULL DEFAULT 10 CHECK (max_nodes > 0),
    max_servers integer NOT NULL DEFAULT 100 CHECK (max_servers > 0),
    max_memory_bytes bigint NOT NULL DEFAULT 1099511627776 CHECK (max_memory_bytes > 0),
    max_disk_bytes bigint NOT NULL DEFAULT 10995116277760 CHECK (max_disk_bytes > 0),
    max_running_servers integer NOT NULL DEFAULT 25 CHECK (max_running_servers > 0),
    max_concurrent_creates integer NOT NULL DEFAULT 4 CHECK (max_concurrent_creates > 0),
    max_concurrent_backups integer NOT NULL DEFAULT 4 CHECK (max_concurrent_backups > 0),
    max_concurrent_uploads integer NOT NULL DEFAULT 2 CHECK (max_concurrent_uploads > 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO workspace_quotas (id) VALUES ('default');

CREATE TABLE capacity_reservations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    server_id uuid REFERENCES servers(id) ON DELETE CASCADE,
    operation_id uuid,
    memory_bytes bigint NOT NULL CHECK (memory_bytes >= 0),
    disk_bytes bigint NOT NULL CHECK (disk_bytes >= 0),
    port_count integer NOT NULL DEFAULT 0 CHECK (port_count >= 0),
    status text NOT NULL CHECK (status IN ('held', 'consumed', 'released', 'expired')),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX capacity_reservations_active_idx ON capacity_reservations (node_id, expires_at)
    WHERE status = 'held';

CREATE TABLE server_migrations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    source_node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    target_node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    backup_id uuid REFERENCES backups(id) ON DELETE SET NULL,
    operation_id uuid,
    status text NOT NULL CHECK (status IN ('queued', 'backing-up', 'placing', 'restoring', 'health-checking', 'switching', 'succeeded', 'rolled-back', 'failed')),
    generation bigint NOT NULL,
    failure_code text,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    CHECK (source_node_id <> target_node_id)
);

CREATE UNIQUE INDEX server_migrations_active_idx ON server_migrations (server_id)
    WHERE status NOT IN ('succeeded', 'rolled-back', 'failed');

COMMIT;
