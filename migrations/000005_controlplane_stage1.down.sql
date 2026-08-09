BEGIN;

DROP TABLE IF EXISTS startup_values;

DROP INDEX IF EXISTS allocations_primary_idx;

ALTER TABLE allocations DROP COLUMN IF EXISTS is_primary;

COMMIT;
