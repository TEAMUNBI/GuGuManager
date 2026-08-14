-- 000010_agent_enrollment
-- 短期、单次、仅存摘要的 Agent 注册令牌：
--   颁发时只返回一次明文令牌，数据库只保存 SHA-256 摘要；
--   消费是原子的单次操作（consumed_at），过期即失效。
CREATE TABLE enrollment_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token_digest bytea NOT NULL UNIQUE CHECK (octet_length(token_digest) = 32),
    node_name_hint text,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    CHECK (expires_at > created_at),
    CHECK (consumed_at IS NULL OR consumed_at > created_at)
);

CREATE INDEX enrollment_tokens_expiry_idx ON enrollment_tokens (expires_at) WHERE consumed_at IS NULL;
