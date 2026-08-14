-- 000011_network_port_roles
ALTER TABLE allocations
    DROP CONSTRAINT IF EXISTS allocations_role_check,
    DROP CONSTRAINT IF EXISTS allocations_container_port_check,
    DROP COLUMN IF EXISTS role,
    DROP COLUMN IF EXISTS container_port,
    DROP COLUMN IF EXISTS port_ref;
