BEGIN;

DROP TABLE IF EXISTS password_reset_tokens;

ALTER TABLE server_members
    DROP COLUMN IF EXISTS updated_at;

COMMIT;
