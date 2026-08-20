BEGIN;

CREATE TABLE bundle_sources (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 128),
    index_url text NOT NULL UNIQUE CHECK (index_url ~ '^https://'),
    official boolean NOT NULL DEFAULT false,
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE bundle_trust_roots (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    key_id text NOT NULL UNIQUE CHECK (key_id ~ '^sha256:[a-f0-9]{64}$'),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 128),
    public_key_pem text NOT NULL,
    source text NOT NULL CHECK (source IN ('official', 'private', 'operator')),
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz
);

ALTER TABLE game_definitions DROP CONSTRAINT game_definitions_review_status_check;
ALTER TABLE game_definitions ADD CONSTRAINT game_definitions_review_status_check
    CHECK (review_status IN ('draft', 'validating', 'pending', 'approved', 'rejected', 'revoked'));

ALTER TABLE game_bundles
    ADD COLUMN document jsonb,
    ADD COLUMN runtime_target jsonb,
    ADD COLUMN artifact_manifest jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN extensions jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN sbom jsonb,
    ADD COLUMN signature jsonb,
    ADD COLUMN signature_key_id text,
    ADD COLUMN signature_verified_at timestamptz,
    ADD COLUMN review_status text NOT NULL DEFAULT 'pending',
    ADD COLUMN revoked_at timestamptz,
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();

ALTER TABLE game_bundles ADD CONSTRAINT game_bundles_review_status_check
    CHECK (review_status IN ('draft', 'validating', 'pending', 'approved', 'rejected', 'revoked'));
ALTER TABLE game_bundles ADD CONSTRAINT game_bundles_signature_key_fk
    FOREIGN KEY (signature_key_id) REFERENCES bundle_trust_roots(key_id) ON DELETE RESTRICT;

CREATE INDEX game_bundles_catalog_idx
    ON game_bundles (game_definition_id, published_at DESC)
    WHERE review_status = 'approved' AND revoked_at IS NULL;

CREATE TABLE bundle_reviews (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    bundle_id uuid NOT NULL REFERENCES game_bundles(id) ON DELETE CASCADE,
    reviewer_id uuid REFERENCES users(id) ON DELETE SET NULL,
    decision text NOT NULL CHECK (decision IN ('submitted', 'approved', 'rejected', 'revoked')),
    reason text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX bundle_reviews_bundle_idx ON bundle_reviews (bundle_id, created_at DESC);

COMMIT;
