package store

import (
	"context"
	"time"
)

// ReconcileNodeLiveness 对 Postgres 适配器应用心跳超时：超过 nodeOfflineAfter
// 未收到心跳的节点（维护模式除外）标记为 offline，并把 offline 传播到其上
// 分配的所有服务器（node_condition = 'offline'），禁止再向这些节点下发任务。
// 与 Memory 版语义一致，now 由调用方显式传入以便测试确定化。
func (s *Postgres) ReconcileNodeLiveness(now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cutoff := now.Add(-nodeOfflineAfter)
	if _, err := s.db.ExecContext(ctx, `
		UPDATE nodes
		SET condition = 'offline', updated_at = now()
		WHERE revoked_at IS NULL
		  AND condition <> 'maintenance'
		  AND condition <> 'offline'
		  AND (last_heartbeat_at IS NULL OR last_heartbeat_at < $1)
	`, cutoff); err != nil {
		return
	}

	// offline 传播到服务器：节点下所有未删除服务器跟随节点条件，
	// 保持 observed_power 不变（与 Memory 版一致）。
	if _, err := s.db.ExecContext(ctx, `
		UPDATE servers
		SET node_condition = 'offline', updated_at = now()
		WHERE deleted_at IS NULL
		  AND node_condition <> 'offline'
		  AND node_id IN (SELECT id FROM nodes WHERE condition = 'offline')
	`); err != nil {
		return
	}
}
