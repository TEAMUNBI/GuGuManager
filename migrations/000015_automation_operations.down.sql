BEGIN;

DROP TABLE server_migrations;
DROP TABLE capacity_reservations;
DROP TABLE workspace_quotas;
DROP TABLE notification_deliveries;
DROP TABLE notifications;
DROP TABLE webhook_endpoints;
DROP TABLE schedule_runs;
DROP TABLE schedules;

ALTER TABLE nodes
    DROP COLUMN drained_at,
    DROP COLUMN drain_reason,
    DROP COLUMN drain_mode,
    DROP COLUMN architecture;

COMMIT;
