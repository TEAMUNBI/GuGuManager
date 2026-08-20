BEGIN;

CREATE TABLE api_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
    token_digest bytea NOT NULL UNIQUE CHECK (octet_length(token_digest) = 32),
    scopes text[] NOT NULL CHECK (cardinality(scopes) > 0),
    expires_at timestamptz,
    last_used_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (expires_at IS NULL OR expires_at > created_at)
);

CREATE INDEX api_tokens_user_active_idx ON api_tokens (user_id, created_at DESC)
    WHERE revoked_at IS NULL;

CREATE TABLE console_connection_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    server_id uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    token_digest bytea NOT NULL UNIQUE CHECK (octet_length(token_digest) = 32),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (expires_at > created_at)
);

CREATE INDEX console_connection_tokens_active_idx
    ON console_connection_tokens (server_id, user_id, expires_at)
    WHERE consumed_at IS NULL;

ALTER TABLE outbox_events
    ADD COLUMN event_version integer NOT NULL DEFAULT 1 CHECK (event_version > 0),
    ADD COLUMN business_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN publish_attempts integer NOT NULL DEFAULT 0 CHECK (publish_attempts >= 0),
    ADD COLUMN last_error text,
    ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN dead_lettered_at timestamptz;

CREATE INDEX outbox_publish_ready_idx
    ON outbox_events (next_attempt_at, created_at)
    WHERE published_at IS NULL AND dead_lettered_at IS NULL;

COMMIT;
