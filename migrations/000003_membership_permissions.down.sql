BEGIN;

ALTER TABLE server_members
    DROP CONSTRAINT IF EXISTS server_members_permissions_unique_check,
    DROP CONSTRAINT IF EXISTS server_members_permissions_known_check,
    DROP CONSTRAINT IF EXISTS server_members_permissions_read_check,
    DROP CONSTRAINT IF EXISTS server_members_permissions_not_empty_check;

DROP FUNCTION IF EXISTS server_members_permissions_are_unique(text[]);

COMMIT;
