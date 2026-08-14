-- 000011_network_port_roles
ALTER TABLE allocations
    ADD COLUMN port_ref text,
    ADD COLUMN container_port integer,
    ADD COLUMN role text;

UPDATE allocations
SET container_port = port,
    role = CASE WHEN is_primary THEN 'primary' ELSE 'additional' END
WHERE container_port IS NULL OR role IS NULL;

ALTER TABLE allocations
    ALTER COLUMN container_port SET NOT NULL,
    ALTER COLUMN container_port SET DEFAULT 0,
    ALTER COLUMN role SET NOT NULL,
    ALTER COLUMN role SET DEFAULT 'additional',
    ADD CONSTRAINT allocations_container_port_check CHECK (container_port BETWEEN 1 AND 65535),
    ADD CONSTRAINT allocations_role_check CHECK (role IN ('primary', 'query', 'rcon', 'additional'));
