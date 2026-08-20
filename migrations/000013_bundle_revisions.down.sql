BEGIN;

DROP TABLE IF EXISTS bundle_reviews;
DROP INDEX IF EXISTS game_bundles_catalog_idx;
ALTER TABLE game_bundles DROP CONSTRAINT IF EXISTS game_bundles_signature_key_fk;
ALTER TABLE game_bundles DROP CONSTRAINT IF EXISTS game_bundles_review_status_check;
ALTER TABLE game_bundles
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS revoked_at,
    DROP COLUMN IF EXISTS review_status,
    DROP COLUMN IF EXISTS signature_verified_at,
    DROP COLUMN IF EXISTS signature_key_id,
    DROP COLUMN IF EXISTS signature,
    DROP COLUMN IF EXISTS sbom,
    DROP COLUMN IF EXISTS extensions,
    DROP COLUMN IF EXISTS artifact_manifest,
    DROP COLUMN IF EXISTS runtime_target,
    DROP COLUMN IF EXISTS document;

ALTER TABLE game_definitions DROP CONSTRAINT IF EXISTS game_definitions_review_status_check;
ALTER TABLE game_definitions ADD CONSTRAINT game_definitions_review_status_check
    CHECK (review_status IN ('pending', 'approved', 'rejected'));

DROP TABLE IF EXISTS bundle_trust_roots;
DROP TABLE IF EXISTS bundle_sources;

COMMIT;
