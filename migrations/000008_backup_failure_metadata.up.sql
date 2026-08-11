ALTER TABLE backups
    ADD COLUMN manifest_digest text CHECK (manifest_digest IS NULL OR manifest_digest ~ '^sha256:[a-f0-9]{64}$'),
    ADD COLUMN failure_code text,
    ADD COLUMN failure_message text,
    ADD COLUMN deleted_at timestamptz;

ALTER TABLE backups
    ADD CONSTRAINT backups_failure_metadata_pair_chk
    CHECK ((failure_code IS NULL) = (failure_message IS NULL));

UPDATE backups
SET failure_code = 'BACKUP_FAILED',
    failure_message = 'Backup task failed before detailed failure metadata was available'
WHERE status = 'failed'
  AND failure_code IS NULL
  AND failure_message IS NULL;

UPDATE backups
SET deleted_at = COALESCE(completed_at, created_at)
WHERE status = 'deleted' AND deleted_at IS NULL;

ALTER TABLE backups
    ADD CONSTRAINT backups_failed_metadata_chk
    CHECK (status <> 'failed' OR failure_code IS NOT NULL),
    ADD CONSTRAINT backups_deleted_timestamp_chk
    CHECK ((status = 'deleted') = (deleted_at IS NOT NULL));

CREATE INDEX backups_deleted_idx ON backups (deleted_at) WHERE deleted_at IS NOT NULL;
