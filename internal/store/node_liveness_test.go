package store

import (
	"testing"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
)

const runningServerID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

func TestReconcileNodeLivenessUsesThirtySecondThresholdAndPropagatesToServers(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	now := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)

	service.mu.Lock()
	stale := service.nodes[availableNodeID]
	stale.LastHeartbeatAt = now.Add(-30 * time.Second)
	stale.Condition = "available"
	service.nodes[availableNodeID] = stale

	fresh := service.nodes["22222222-2222-4222-8222-222222222222"]
	fresh.LastHeartbeatAt = now.Add(-29 * time.Second)
	fresh.Condition = "available"
	service.nodes[fresh.ID] = fresh

	assigned := service.servers[runningServerID]
	assigned.NodeCondition = "available"
	observedPower := assigned.ObservedPower
	observedAt := assigned.ObservedAt
	service.servers[runningServerID] = assigned
	service.mu.Unlock()

	service.ReconcileNodeLiveness(now)

	service.mu.RLock()
	defer service.mu.RUnlock()
	if got := service.nodes[availableNodeID].Condition; got != "offline" {
		t.Fatalf("node at 30-second heartbeat age has condition %q, want offline", got)
	}
	if got := service.nodes[fresh.ID].Condition; got != "available" {
		t.Fatalf("node at 29-second heartbeat age has condition %q, want available", got)
	}
	if got := service.servers[runningServerID].NodeCondition; got != "offline" {
		t.Fatalf("assigned server nodeCondition = %q, want offline", got)
	}
	if got := service.servers[runningServerID].ObservedPower; got != observedPower {
		t.Fatalf("offline reconciliation changed observedPower from %q to %q", observedPower, got)
	}
	if got := service.servers[runningServerID].ObservedAt; !got.Equal(observedAt) {
		t.Fatalf("offline reconciliation changed observedAt from %s to %s", observedAt, got)
	}
}

func TestStaleNodesRejectNewProvisionAndPowerOperations(t *testing.T) {
	service := newTestMemory(time.Second)
	now := time.Now().UTC()

	service.mu.Lock()
	node := service.nodes[availableNodeID]
	node.LastHeartbeatAt = now.Add(-31 * time.Second)
	service.nodes[availableNodeID] = node
	service.mu.Unlock()

	_, err := service.CreateServer(validCreateServerInput(), "stale-create-key-0001", testActor("admin-1", "GuGu Admin"))
	requireProblemCode(t, err, "NODE_OFFLINE")

	_, err = service.RequestPower(runningServerID, domain.PowerStop, "stale-power-key-00001", testActor("admin-1", "GuGu Admin"))
	requireProblemCode(t, err, "NODE_OFFLINE")
}

func TestReadModelsReconcileStaleNodeState(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	service.mu.Lock()
	node := service.nodes[availableNodeID]
	node.LastHeartbeatAt = time.Now().Add(-time.Minute)
	service.nodes[availableNodeID] = node
	service.mu.Unlock()

	if got := service.Overview().OnlineNodeCount; got != 1 {
		t.Fatalf("online node count = %d, want only the fresh second node", got)
	}
	server, err := service.Server(runningServerID)
	if err != nil {
		t.Fatal(err)
	}
	if server.NodeCondition != "offline" {
		t.Fatalf("server node condition = %q, want offline", server.NodeCondition)
	}
}
