package store

import (
	"testing"
	"time"
)

// TestPostgresReconcileNodeLiveness 验证 30 秒心跳超时：过期节点标记 offline
// 并传播到其服务器，活跃节点与维护模式节点不受影响。
func TestPostgresReconcileNodeLiveness(t *testing.T) {
	s := testPostgres(t)
	resetTestDatabase(t, s)

	now := time.Now().UTC()

	var staleNodeID string
	if err := s.db.QueryRow(`
		INSERT INTO nodes (name, agent_version, protocol_version, condition, region, cpu_cores, memory_bytes, disk_bytes, last_heartbeat_at)
		VALUES ('liveness-stale', 'test-agent', 'v1', 'available', 'test', 1, 1073741824, 1073741824, $1)
		RETURNING id::text`, now.Add(-40*time.Second)).Scan(&staleNodeID); err != nil {
		t.Fatalf("insert stale node: %v", err)
	}

	var liveNodeID string
	if err := s.db.QueryRow(`
		INSERT INTO nodes (name, agent_version, protocol_version, condition, region, cpu_cores, memory_bytes, disk_bytes, last_heartbeat_at)
		VALUES ('liveness-live', 'test-agent', 'v1', 'available', 'test', 1, 1073741824, 1073741824, $1)
		RETURNING id::text`, now.Add(-5*time.Second)).Scan(&liveNodeID); err != nil {
		t.Fatalf("insert live node: %v", err)
	}

	var maintenanceNodeID string
	if err := s.db.QueryRow(`
		INSERT INTO nodes (name, agent_version, protocol_version, condition, region, cpu_cores, memory_bytes, disk_bytes, last_heartbeat_at)
		VALUES ('liveness-maintenance', 'test-agent', 'v1', 'maintenance', 'test', 1, 1073741824, 1073741824, $1)
		RETURNING id::text`, now.Add(-40*time.Second)).Scan(&maintenanceNodeID); err != nil {
		t.Fatalf("insert maintenance node: %v", err)
	}

	// 服务器挂到过期节点，验证 offline 传播。
	admin := setupAdminForTest(t, s)
	insertGameFixture(t, s, "liveness-test-game", "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
	var bundleID string
	if err := s.db.QueryRow(`
		SELECT id::text FROM game_bundles WHERE game_definition_id = 'liveness-test-game' LIMIT 1
	`).Scan(&bundleID); err != nil {
		t.Fatalf("look up liveness test bundle: %v", err)
	}
	if _, err := s.db.Exec(`
		INSERT INTO servers (
			owner_id, node_id, game_bundle_id, game_version, name, lifecycle_state,
			desired_power, observed_power, node_condition, health_condition, memory_limit_bytes, disk_limit_bytes
		)
		VALUES ($1, $2, $3, '1.0.0', 'Liveness Test Server', 'ready', 'running', 'running', 'available', 'healthy', 1073741824, 1073741824)
	`, admin.ID, staleNodeID, bundleID); err != nil {
		t.Fatalf("insert liveness test server: %v", err)
	}

	s.ReconcileNodeLiveness(now)

	var staleCondition, liveCondition, maintenanceCondition string
	_ = s.db.QueryRow(`SELECT condition FROM nodes WHERE id = $1`, staleNodeID).Scan(&staleCondition)
	_ = s.db.QueryRow(`SELECT condition FROM nodes WHERE id = $1`, liveNodeID).Scan(&liveCondition)
	_ = s.db.QueryRow(`SELECT condition FROM nodes WHERE id = $1`, maintenanceNodeID).Scan(&maintenanceCondition)
	if staleCondition != "offline" {
		t.Errorf("stale node condition = %q, want offline", staleCondition)
	}
	if liveCondition != "available" {
		t.Errorf("live node condition = %q, want available", liveCondition)
	}
	if maintenanceCondition != "maintenance" {
		t.Errorf("maintenance node condition = %q, want maintenance", maintenanceCondition)
	}

	var serverNodeCondition, observedPower string
	_ = s.db.QueryRow(`SELECT node_condition, observed_power FROM servers WHERE node_id = $1`, staleNodeID).Scan(&serverNodeCondition, &observedPower)
	if serverNodeCondition != "offline" {
		t.Errorf("stale node server node_condition = %q, want offline", serverNodeCondition)
	}
	if observedPower != "running" {
		t.Errorf("offline propagation changed observed_power = %q, want running preserved", observedPower)
	}
}
