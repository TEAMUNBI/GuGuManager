BEGIN;

ALTER TABLE server_members
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();

CREATE TABLE password_reset_tokens (
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

CREATE INDEX password_reset_tokens_active_idx
    ON password_reset_tokens (user_id, expires_at)
    WHERE consumed_at IS NULL;

COMMIT;
