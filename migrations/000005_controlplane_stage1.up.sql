BEGIN;

-- Startup variable values persisted per server. Values are the normalized
-- variable map produced by UpdateStartup; secrets stay in the same JSONB
-- document (the control plane never returns them).
CREATE TABLE IF NOT EXISTS startup_values (
    server_id uuid PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    values jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Allocations gain an explicit primary marker so SetPrimaryAllocation can be
-- persisted across restarts; the partial unique index keeps at most one active
-- primary allocation per server.
ALTER TABLE allocations ADD COLUMN IF NOT EXISTS is_primary boolean NOT NULL DEFAULT false;

CREATE UNIQUE INDEX IF NOT EXISTS allocations_primary_idx
    ON allocations (server_id) WHERE is_primary AND released_at IS NULL;

COMMIT;
