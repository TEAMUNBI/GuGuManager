BEGIN;

CREATE TABLE server_bundle_history (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    from_bundle_id uuid NOT NULL REFERENCES game_bundles(id) ON DELETE RESTRICT,
    to_bundle_id uuid NOT NULL REFERENCES game_bundles(id) ON DELETE RESTRICT,
    operation_id uuid NOT NULL UNIQUE REFERENCES server_tasks(id) ON DELETE RESTRICT,
    generation bigint NOT NULL,
    status text NOT NULL CHECK (status IN ('reconciling', 'applied', 'rolled-back', 'failed')),
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    CHECK (from_bundle_id <> to_bundle_id)
);

CREATE INDEX server_bundle_history_server_idx ON server_bundle_history (server_id, created_at DESC);

COMMIT;
