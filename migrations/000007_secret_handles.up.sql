BEGIN;

-- One-time, node-scoped references to encrypted startup values. The raw
-- handle is returned only in the leased task response; PostgreSQL stores its
-- SHA-256 digest and never stores the plaintext Secret value.
CREATE TABLE IF NOT EXISTS secret_handles (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token_digest bytea NOT NULL UNIQUE CHECK (octet_length(token_digest) = 32),
    operation_id uuid NOT NULL REFERENCES server_tasks(id) ON DELETE CASCADE,
    server_id uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    variable_key text NOT NULL CHECK (char_length(variable_key) BETWEEN 1 AND 128),
    encrypted_value text NOT NULL CHECK (
        encrypted_value LIKE 'enc:v1:%' OR encrypted_value LIKE 'enc:v2:%'
    ),
    attempt integer NOT NULL CHECK (attempt > 0),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS secret_handles_lookup_idx
    ON secret_handles (operation_id, server_id, node_id, expires_at)
    WHERE consumed_at IS NULL;

COMMIT;
