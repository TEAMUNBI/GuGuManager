DROP INDEX IF EXISTS backups_deleted_idx;
ALTER TABLE backups DROP CONSTRAINT IF EXISTS backups_deleted_timestamp_chk;
ALTER TABLE backups DROP CONSTRAINT IF EXISTS backups_failed_metadata_chk;
ALTER TABLE backups DROP CONSTRAINT IF EXISTS backups_failure_metadata_pair_chk;
ALTER TABLE backups
    DROP COLUMN IF EXISTS deleted_at,
    DROP COLUMN IF EXISTS failure_message,
    DROP COLUMN IF EXISTS failure_code,
    DROP COLUMN IF EXISTS manifest_digest;
