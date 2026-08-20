BEGIN;

CREATE TABLE object_manifests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    object_key text NOT NULL UNIQUE,
    format_version text NOT NULL CHECK (format_version = 'gugu-envelope/v1'),
    plaintext_digest text NOT NULL UNIQUE CHECK (plaintext_digest ~ '^sha256:[a-f0-9]{64}$'),
    ciphertext_digest text NOT NULL CHECK (ciphertext_digest ~ '^sha256:[a-f0-9]{64}$'),
    plaintext_size bigint NOT NULL CHECK (plaintext_size >= 0),
    ciphertext_size bigint NOT NULL CHECK (ciphertext_size > 0),
    key_id text NOT NULL,
    wrapped_data_key text NOT NULL,
    manifest jsonb NOT NULL,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'deleting', 'deleted', 'orphaned')),
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz
);

CREATE TABLE backup_policies (
    server_id uuid PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    retention_days integer NOT NULL DEFAULT 30 CHECK (retention_days BETWEEN 1 AND 3650),
    max_count integer NOT NULL DEFAULT 10 CHECK (max_count BETWEEN 1 AND 1000),
    protect_manual boolean NOT NULL DEFAULT true,
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE object_tombstones (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    object_manifest_id uuid NOT NULL UNIQUE REFERENCES object_manifests(id) ON DELETE CASCADE,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'leased', 'failed', 'completed')),
    attempt integer NOT NULL DEFAULT 0,
    lease_owner uuid,
    lease_until timestamptz,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);

ALTER TABLE backups
    ADD COLUMN protected boolean NOT NULL DEFAULT false,
    ADD COLUMN object_manifest_id uuid REFERENCES object_manifests(id) ON DELETE RESTRICT,
    ADD COLUMN remote_status text NOT NULL DEFAULT 'pending'
        CHECK (remote_status IN ('pending', 'uploading', 'ready', 'failed', 'delete_pending', 'deleted', 'local_only')),
    ADD COLUMN remote_attempt integer NOT NULL DEFAULT 0,
    ADD COLUMN remote_lease_owner uuid,
    ADD COLUMN remote_lease_until timestamptz,
    ADD COLUMN remote_last_error text;

CREATE INDEX backups_remote_pending_idx ON backups (created_at)
    WHERE status = 'ready' AND remote_status IN ('pending', 'failed');
CREATE INDEX backups_retention_idx ON backups (retention_until, created_at)
    WHERE status = 'ready' AND protected = false;

COMMIT;
