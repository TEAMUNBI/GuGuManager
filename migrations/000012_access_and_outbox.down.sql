BEGIN;

DROP INDEX IF EXISTS outbox_publish_ready_idx;
ALTER TABLE outbox_events
    DROP COLUMN IF EXISTS dead_lettered_at,
    DROP COLUMN IF EXISTS next_attempt_at,
    DROP COLUMN IF EXISTS last_error,
    DROP COLUMN IF EXISTS publish_attempts,
    DROP COLUMN IF EXISTS business_at,
    DROP COLUMN IF EXISTS event_version;

DROP TABLE IF EXISTS console_connection_tokens;
DROP TABLE IF EXISTS api_tokens;

COMMIT;
