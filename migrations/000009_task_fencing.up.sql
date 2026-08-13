-- 000009_task_fencing
-- 有栅栏的任务状态机：
--   1. 状态固定为 queued -> leased -> running -> succeeded/failed；
--      dispatched（已下发未确认）与 canceled 退出状态域。
--   2. 新增 lease_token（租约凭据）、connection_epoch（领取该任务的连接
--      epoch）、state_version（每次状态转换单调 +1）与 lease_renewed_at。
--   3. 任务输入与 checkpoint 分离：新任务输入写入 task_input（jsonb），
--      checkpoint 只承载 Agent 执行期进度检查点。

ALTER TABLE server_tasks
    ADD COLUMN task_input jsonb,
    ADD COLUMN lease_token uuid,
    ADD COLUMN connection_epoch bigint NOT NULL DEFAULT 0,
    ADD COLUMN state_version bigint NOT NULL DEFAULT 0,
    ADD COLUMN lease_renewed_at timestamptz;

ALTER TABLE server_tasks
    ADD CONSTRAINT server_tasks_connection_epoch_chk CHECK (connection_epoch >= 0),
    ADD CONSTRAINT server_tasks_state_version_chk CHECK (state_version >= 0);

-- dispatched：已被领取但从未确认投递的任务重新入队（delivery timeout）。
UPDATE server_tasks
   SET status = 'queued',
       lease_owner = NULL,
       lease_expires_at = NULL,
       state_version = state_version + 1,
       updated_at = now()
 WHERE status = 'dispatched';

-- canceled：历史上不可重试的中断等价于结构化失败终态。
-- 本迁移是单向状态压缩，down 迁移只恢复 schema，不还原 dispatched/canceled 行。
UPDATE server_tasks
   SET status = 'failed',
       error_code = COALESCE(error_code, 'CANCELED'),
       error_retryable = false,
       completed_at = COALESCE(completed_at, updated_at),
       state_version = state_version + 1,
       updated_at = now()
 WHERE status = 'canceled';

ALTER TABLE server_tasks DROP CONSTRAINT server_tasks_status_check;
ALTER TABLE server_tasks ADD CONSTRAINT server_tasks_status_check
    CHECK (status IN ('queued', 'leased', 'running', 'succeeded', 'failed'));

-- 每服务器互斥索引收缩到新活动状态域。
DROP INDEX server_tasks_exclusive_idx;
CREATE UNIQUE INDEX server_tasks_exclusive_idx ON server_tasks (server_id)
    WHERE status IN ('queued', 'leased', 'running');

DROP INDEX server_tasks_claim_idx;
CREATE INDEX server_tasks_claim_idx ON server_tasks (created_at)
    WHERE status = 'queued';

CREATE INDEX server_tasks_lease_expiry_idx ON server_tasks (lease_expires_at)
    WHERE status = 'leased';
