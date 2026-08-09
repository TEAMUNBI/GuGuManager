BEGIN;

-- Earlier development schemas allowed an empty permission array. Memberships
-- without servers.read cannot see their server, so bring legacy rows forward
-- before making the persisted contract strict.
UPDATE server_members
SET permissions = array_append(permissions, 'servers.read')
WHERE array_position(permissions, 'servers.read') IS NULL;

CREATE FUNCTION server_members_permissions_are_unique(permission_values text[])
RETURNS boolean
LANGUAGE SQL
IMMUTABLE
PARALLEL SAFE
AS $$
    SELECT cardinality(permission_values) = (
        SELECT count(DISTINCT permission)
        FROM unnest(permission_values) AS item(permission)
    );
$$;

ALTER TABLE server_members
    ADD CONSTRAINT server_members_permissions_not_empty_check
        CHECK (cardinality(permissions) > 0),
    ADD CONSTRAINT server_members_permissions_read_check
        CHECK (array_position(permissions, 'servers.read') IS NOT NULL),
    ADD CONSTRAINT server_members_permissions_known_check
        CHECK (
            array_position(permissions, NULL) IS NULL
            AND permissions <@ ARRAY[
                'servers.read',
                'servers.power',
                'servers.console',
                'servers.files.read',
                'servers.files.write',
                'servers.backups.read',
                'servers.backups.create',
                'servers.backups.restore',
                'servers.backups.delete',
                'servers.network.read',
                'servers.network.write',
                'servers.startup.read',
                'servers.startup.write'
            ]::text[]
        ),
    ADD CONSTRAINT server_members_permissions_unique_check
        CHECK (server_members_permissions_are_unique(permissions));

COMMIT;
