BEGIN;

ALTER TABLE backups
    DROP COLUMN remote_last_error,
    DROP COLUMN remote_lease_until,
    DROP COLUMN remote_lease_owner,
    DROP COLUMN remote_attempt,
    DROP COLUMN remote_status,
    DROP COLUMN object_manifest_id,
    DROP COLUMN protected;

DROP TABLE object_tombstones;
DROP TABLE backup_policies;
DROP TABLE object_manifests;

COMMIT;
