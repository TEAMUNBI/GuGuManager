BEGIN;

-- password_reset_tokens was introduced by 000002_identity.up.sql. This
-- migration re-declares it idempotently so databases that only ran earlier
-- migrations still converge on the same table shape. The schema is kept
-- identical to 000002 so all environments end up with one definition.
CREATE TABLE IF NOT EXISTS password_reset_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    issuer_id uuid REFERENCES users(id) ON DELETE SET NULL,
    token_digest bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (octet_length(token_digest) = 32),
    CHECK (expires_at > created_at),
    CHECK (consumed_at IS NULL OR consumed_at >= created_at)
);

CREATE INDEX IF NOT EXISTS password_reset_tokens_active_idx
    ON password_reset_tokens (user_id, expires_at)
    WHERE consumed_at IS NULL;

COMMIT;
