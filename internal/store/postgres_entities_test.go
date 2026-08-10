package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gugumanager/gugumanager/internal/domain"
)

// setupAdminForTest bootstraps the first platform administrator on a reset
// database. testPostgres/resetTestDatabase are defined in
// postgres_identity_ext_test.go and reused here.
func setupAdminForTest(t *testing.T, s *Postgres) domain.User {
	t.Helper()
	s.SetBootstrapToken("bootstrap-token-12345678901234567890123456789012")
	admin, err := s.SetupAdmin(domain.SetupAdminInput{
		Email:          "admin-pg@test.local",
		DisplayName:    "Admin",
		Password:       "correct-horse-battery",
		BootstrapToken: "bootstrap-token-12345678901234567890123456789012",
	})
	if err != nil {
		t.Fatalf("setup admin: %v", err)
	}
	return admin
}

// insertGameFixture inserts the smallest approved game definition + bundle
// rows. The bundle digest must match the sha256: constraint on
// game_bundles.digest; callers pass a unique digest per test.
func insertGameFixture(t *testing.T, s *Postgres, gameID string, digest string) {
	t.Helper()
	if _, err := s.db.Exec(`
		INSERT INTO game_definitions (id, name, source_url, review_status)
		VALUES ($1, 'PG Test Game', 'https://example.invalid/pg.json', 'approved')
	`, gameID); err != nil {
		t.Fatalf("insert game definition: %v", err)
	}
	if _, err := s.db.Exec(`
		INSERT INTO game_bundles (game_definition_id, definition_version, game_version, digest, schema_version, license, compatibility, published_at)
		VALUES ($1, '1.0.0', '1.0.0', $2, 'v1', 'test', '{}'::jsonb, now())
	`, gameID, digest); err != nil {
		t.Fatalf("insert game bundle: %v", err)
	}
}

const (
	testDigestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testDigestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// TestPostgresNodeServerRegistration covers RegisterNode -> NodeByID round
// trip (including capabilities and unique-name conflict) and the
// ControlPlane Servers()/Server() read path.
func TestPostgresNodeServerRegistration(t *testing.T) {
	s := testPostgres(t)
	resetTestDatabase(t, s)
	admin := setupAdminForTest(t, s)

	nodeID, err := s.RegisterNode(context.Background(), domain.Node{
		Name:         "node-a",
		Condition:    "available",
		Version:      "agent-v1.2.3",
		Region:       "cn-north",
		Address:      "127.0.0.1",
		CPUCores:     8,
		MemoryBytes:  16 << 30,
		DiskBytes:    1 << 40,
		Capabilities: []string{"docker", "files"},
	})
	if err != nil {
		t.Fatalf("register node: %v", err)
	}

	got, err := s.NodeByID(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("node by id: %v", err)
	}
	if got.ID != nodeID || got.Name != "node-a" || got.Condition != "available" ||
		got.Version != "agent-v1.2.3" || got.Region != "cn-north" || got.Address != "127.0.0.1" ||
		got.CPUCores != 8 || got.MemoryBytes != 16<<30 || got.DiskBytes != 1<<40 {
		t.Fatalf("node round-trip mismatch: %+v", got)
	}
	if len(got.Capabilities) != 2 {
		t.Fatalf("expected 2 capabilities, got %v", got.Capabilities)
	}

	// Duplicate node name must map to NODE_NAME_CONFLICT.
	_, err = s.RegisterNode(context.Background(), domain.Node{
		Name: "node-a", Condition: "available", Version: "x", Region: "r",
		CPUCores: 1, MemoryBytes: 1 << 30, DiskBytes: 1 << 30,
	})
	if err == nil {
		t.Fatal("expected duplicate node name to fail")
	}
	problem, ok := err.(*domain.Problem)
	if !ok || problem.Code != "NODE_NAME_CONFLICT" {
		t.Fatalf("expected NODE_NAME_CONFLICT, got %v", err)
	}

	// Nodes() lists the registered node.
	nodes := s.Nodes()
	if len(nodes) != 1 || nodes[0].ID != nodeID || nodes[0].Name != "node-a" {
		t.Fatalf("Nodes() mismatch: %+v", nodes)
	}

	// CreateServer on an approved bundle -> queued operation -> visible in Servers().
	insertGameFixture(t, s, "pg-game", testDigestA)
	op, err := s.CreateServer(domain.CreateServerInput{
		Name:             "pg-server-1",
		GameDefinitionID: "pg-game",
		GameBundleDigest: testDigestA,
		NodeID:           nodeID,
		MemoryMB:         1024,
		DiskGB:           10,
	}, "idem-key-create-0001", admin)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	if op.Status != "queued" || op.Generation != 1 {
		t.Fatalf("expected queued provision operation, got %+v", op)
	}

	servers := s.Servers("")
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %+v", servers)
	}
	server := servers[0]
	if server.ID != op.ServerID || server.Name != "pg-server-1" || server.NodeID != nodeID ||
		server.NodeName != "node-a" || server.LifecycleState != "provisioning" ||
		server.DesiredPower != "stopped" || server.ObservedPower != "unknown" ||
		server.Generation != 1 || server.GameID != "pg-game" || server.GameName != "PG Test Game" ||
		server.OwnerName != admin.DisplayName || server.Metrics.MemoryLimit != 1024*1024*1024 {
		t.Fatalf("server read mismatch: %+v", server)
	}

	single, err := s.Server(server.ID)
	if err != nil {
		t.Fatalf("server by id: %v", err)
	}
	if single.Name != "pg-server-1" {
		t.Fatalf("server by id mismatch: %+v", single)
	}
}

// TestPostgresTaskClaimComplete covers CreateServer -> EnqueueTask ->
// ClaimTask lease -> CompleteTask -> no more queued task.
func TestPostgresTaskClaimComplete(t *testing.T) {
	s := testPostgres(t)
	resetTestDatabase(t, s)
	admin := setupAdminForTest(t, s)

	nodeID, err := s.RegisterNode(context.Background(), domain.Node{
		Name: "node-b", Condition: "available", Version: "agent-v1", Region: "cn",
		Address: "127.0.0.2", CPUCores: 4, MemoryBytes: 1 << 32, DiskBytes: 1 << 34,
	})
	if err != nil {
		t.Fatalf("register node: %v", err)
	}

	insertGameFixture(t, s, "pg-task-game", testDigestB)
	op, err := s.CreateServer(domain.CreateServerInput{
		Name:             "pg-task-server",
		GameDefinitionID: "pg-task-game",
		GameBundleDigest: testDigestB,
		NodeID:           nodeID,
		MemoryMB:         512,
		DiskGB:           5,
	}, "idem-key-create-0002", admin)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	claimed, err := s.ClaimTask(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	if claimed == nil {
		t.Fatal("expected a queued task, got nil")
	}
	if claimed.OperationID != op.ID || claimed.NodeID != nodeID || claimed.TaskType != "provision" ||
		claimed.Generation != 1 || claimed.Attempt < 1 {
		t.Fatalf("claimed task mismatch: %+v (operation %s)", claimed, op.ID)
	}
	// The provision payload must be materialized at CreateServer time and
	// carried to the agent via PayloadJSON; an empty payload fails every
	// provision on the agent with PROVISION_FAILED.
	if len(claimed.PayloadJSON) == 0 {
		t.Fatal("claimed provision task carries no payload")
	}
	var payload struct {
		GameDefinitionID string `json:"gameDefinitionId"`
		Allocations      []struct {
			AllocationID  string `json:"allocationId"`
			HostPort      uint32 `json:"hostPort"`
			ContainerPort uint32 `json:"containerPort"`
		} `json:"allocations"`
	}
	if err := json.Unmarshal(claimed.PayloadJSON, &payload); err != nil {
		t.Fatalf("decode provision payload: %v", err)
	}
	if payload.GameDefinitionID != "pg-task-game" || len(payload.Allocations) != 1 ||
		payload.Allocations[0].HostPort == 0 || payload.Allocations[0].ContainerPort == 0 {
		t.Fatalf("provision payload mismatch: %s", claimed.PayloadJSON)
	}

	if err := s.CompleteTask(context.Background(), claimed.OperationID, nodeID, true, nil, nil); err != nil {
		t.Fatalf("complete task: %v", err)
	}

	// 成功的 provision 必须把服务器推进到 ready，否则无法接收电源操作。
	after, err := s.Server(claimed.ServerID)
	if err != nil {
		t.Fatalf("server after provision: %v", err)
	}
	if after.LifecycleState != "ready" {
		t.Fatalf("server lifecycle after provision = %q, want ready", after.LifecycleState)
	}

	again, err := s.ClaimTask(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("claim task after completion: %v", err)
	}
	if again != nil {
		t.Fatalf("expected no queued task after completion, got %+v", again)
	}
}
