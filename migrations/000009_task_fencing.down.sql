-- 000009_task_fencing.down
-- 恢复 v1 状态域与索引。注意：up 迁移对 dispatched/canceled 行的转换是
-- 单向状态压缩，本文件不尝试还原历史行。

ALTER TABLE server_tasks DROP CONSTRAINT server_tasks_status_check;
ALTER TABLE server_tasks ADD CONSTRAINT server_tasks_status_check
    CHECK (status IN ('queued', 'leased', 'dispatched', 'running', 'succeeded', 'failed', 'canceled'));

DROP INDEX server_tasks_lease_expiry_idx;

DROP INDEX server_tasks_claim_idx;
CREATE INDEX server_tasks_claim_idx ON server_tasks (status, created_at)
    WHERE status IN ('queued', 'leased');

DROP INDEX server_tasks_exclusive_idx;
CREATE UNIQUE INDEX server_tasks_exclusive_idx ON server_tasks (server_id)
    WHERE status IN ('queued', 'leased', 'dispatched', 'running');

ALTER TABLE server_tasks
    DROP COLUMN lease_renewed_at,
    DROP COLUMN state_version,
    DROP COLUMN connection_epoch,
    DROP COLUMN lease_token,
    DROP COLUMN task_input;
