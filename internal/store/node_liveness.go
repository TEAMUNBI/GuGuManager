package store

import "time"

const nodeOfflineAfter = 30 * time.Second

// ReconcileNodeLiveness applies the heartbeat timeout using an explicit time so
// callers and tests can make the transition deterministic.
func (m *Memory) ReconcileNodeLiveness(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reconcileNodeLivenessLocked(now)
}

func (m *Memory) reconcileNodeLivenessLocked(now time.Time) {
	for nodeID, node := range m.nodes {
		if node.Condition == "maintenance" {
			continue
		}
		if !node.LastHeartbeatAt.IsZero() && now.Before(node.LastHeartbeatAt.Add(nodeOfflineAfter)) {
			continue
		}

		node.Condition = "offline"
		m.nodes[nodeID] = node
		for serverID, server := range m.servers {
			if server.NodeID != nodeID {
				continue
			}
			server.NodeCondition = "offline"
			m.servers[serverID] = server
		}
	}
}
