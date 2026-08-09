BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email text NOT NULL,
    normalized_email text NOT NULL UNIQUE,
    display_name text NOT NULL,
    password_hash text NOT NULL,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE roles (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    role_key text NOT NULL UNIQUE,
    description text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE user_roles (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id uuid NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, role_id)
);

CREATE TABLE sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_digest bytea NOT NULL UNIQUE,
    csrf_digest bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (octet_length(token_digest) = 32),
    CHECK (octet_length(csrf_digest) = 32)
);

CREATE INDEX sessions_user_active_idx ON sessions (user_id, expires_at) WHERE revoked_at IS NULL;

CREATE TABLE nodes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE,
    certificate_subject text UNIQUE,
    agent_version text NOT NULL,
    protocol_version text NOT NULL,
    condition text NOT NULL CHECK (condition IN ('available', 'offline', 'maintenance')),
    region text NOT NULL,
    address inet,
    cpu_cores integer NOT NULL CHECK (cpu_cores > 0),
    memory_bytes bigint NOT NULL CHECK (memory_bytes > 0),
    disk_bytes bigint NOT NULL CHECK (disk_bytes > 0),
    last_heartbeat_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE node_capabilities (
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    capability_key text NOT NULL,
    capability_version text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (node_id, capability_key)
);

CREATE TABLE game_definitions (
    id text PRIMARY KEY,
    name text NOT NULL,
    source_url text NOT NULL,
    review_status text NOT NULL CHECK (review_status IN ('pending', 'approved', 'rejected')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE game_bundles (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    game_definition_id text NOT NULL REFERENCES game_definitions(id) ON DELETE RESTRICT,
    definition_version text NOT NULL,
    game_version text NOT NULL,
    digest text NOT NULL UNIQUE CHECK (digest ~ '^sha256:[a-f0-9]{64}$'),
    schema_version text NOT NULL,
    signature_identity text,
    license text NOT NULL,
    compatibility jsonb NOT NULL,
    published_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (game_definition_id, definition_version)
);

CREATE TABLE servers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    game_bundle_id uuid NOT NULL REFERENCES game_bundles(id) ON DELETE RESTRICT,
    game_version text NOT NULL,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
    description text NOT NULL DEFAULT '',
    lifecycle_state text NOT NULL CHECK (lifecycle_state IN ('provisioning', 'ready', 'deleting', 'deleted')),
    desired_power text NOT NULL CHECK (desired_power IN ('running', 'stopped')),
    observed_power text NOT NULL CHECK (observed_power IN ('unknown', 'stopped', 'starting', 'running', 'stopping')),
    node_condition text NOT NULL CHECK (node_condition IN ('available', 'offline', 'maintenance')),
    health_condition text NOT NULL CHECK (health_condition IN ('unknown', 'healthy', 'unhealthy')),
    generation bigint NOT NULL DEFAULT 1 CHECK (generation > 0),
    observed_generation bigint NOT NULL DEFAULT 0 CHECK (observed_generation >= 0 AND observed_generation <= generation),
    observed_at timestamptz NOT NULL DEFAULT now(),
    memory_limit_bytes bigint NOT NULL CHECK (memory_limit_bytes > 0),
    disk_limit_bytes bigint NOT NULL CHECK (disk_limit_bytes > 0),
    deleted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX servers_owner_idx ON servers (owner_id) WHERE deleted_at IS NULL;
CREATE INDEX servers_node_idx ON servers (node_id) WHERE deleted_at IS NULL;

CREATE TABLE server_members (
    server_id uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    permissions text[] NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (server_id, user_id)
);

CREATE TABLE allocations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    bind_ip inet NOT NULL,
    port integer NOT NULL CHECK (port BETWEEN 1 AND 65535),
    protocol text NOT NULL CHECK (protocol IN ('tcp', 'udp')),
    server_id uuid REFERENCES servers(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    released_at timestamptz
);

CREATE UNIQUE INDEX allocations_active_endpoint_idx ON allocations (node_id, bind_ip, port, protocol) WHERE released_at IS NULL;

CREATE TABLE server_tasks (
    id uuid PRIMARY KEY,
    server_id uuid NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    task_type text NOT NULL CHECK (task_type IN ('provision', 'start', 'stop', 'restart', 'kill', 'backup', 'restore', 'backup-delete', 'delete', 'reconcile')),
    status text NOT NULL CHECK (status IN ('queued', 'leased', 'dispatched', 'running', 'succeeded', 'failed', 'canceled')),
    generation bigint NOT NULL CHECK (generation > 0),
    actor_id uuid REFERENCES users(id) ON DELETE SET NULL,
    idempotency_scope text NOT NULL,
    idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 16 AND 128),
    request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
    attempt integer NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    max_attempts integer NOT NULL DEFAULT 3 CHECK (max_attempts > 0),
    lease_owner text,
    lease_expires_at timestamptz,
    checkpoint jsonb,
    progress integer NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
    error_code text,
    error_retryable boolean,
    error_details jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE (idempotency_scope, idempotency_key)
);

CREATE INDEX server_tasks_claim_idx ON server_tasks (status, created_at) WHERE status IN ('queued', 'leased');
CREATE INDEX server_tasks_server_idx ON server_tasks (server_id, created_at DESC);
CREATE UNIQUE INDEX server_tasks_exclusive_idx ON server_tasks (server_id) WHERE status IN ('queued', 'leased', 'dispatched', 'running');

CREATE TABLE outbox_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregate_type text NOT NULL,
    aggregate_id text NOT NULL,
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);

CREATE INDEX outbox_unpublished_idx ON outbox_events (created_at) WHERE published_at IS NULL;

CREATE TABLE backups (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id uuid NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
    creator_id uuid REFERENCES users(id) ON DELETE SET NULL,
    name text NOT NULL,
    status text NOT NULL CHECK (status IN ('creating', 'ready', 'failed', 'restoring', 'deleting', 'deleted')),
    format_version text NOT NULL,
    game_bundle_digest text NOT NULL,
    content_digest text CHECK (content_digest IS NULL OR content_digest ~ '^sha256:[a-f0-9]{64}$'),
    size_bytes bigint CHECK (size_bytes IS NULL OR size_bytes >= 0),
    storage_location text,
    retention_until timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);

CREATE INDEX backups_server_idx ON backups (server_id, created_at DESC) WHERE status <> 'deleted';
CREATE UNIQUE INDEX backups_restore_lock_idx ON backups (server_id) WHERE status = 'restoring';

CREATE TABLE audit_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_id uuid REFERENCES users(id) ON DELETE SET NULL,
    actor_type text NOT NULL CHECK (actor_type IN ('user', 'agent', 'system')),
    action text NOT NULL,
    target_type text NOT NULL,
    target_id text NOT NULL,
    result text NOT NULL CHECK (result IN ('accepted', 'success', 'failure')),
    operation_id uuid,
    trace_id uuid NOT NULL,
    client_ip inet,
    metadata jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_events_created_idx ON audit_events (created_at DESC);
CREATE INDEX audit_events_target_idx ON audit_events (target_type, target_id, created_at DESC);

INSERT INTO roles (role_key, description) VALUES
    ('platform_admin', 'Full control over the single GuGuManager management domain'),
    ('server_owner', 'Server-scoped power, console, file and backup access');

COMMIT;
