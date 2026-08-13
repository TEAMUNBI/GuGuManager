package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
)

func TestEmbeddedCatalogDoesNotClaimUnprovenTrustOrRuntimeSupport(t *testing.T) {
	service := NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()

	wantReasons := []string{gameReasonSignatureUnverified, gameReasonRuntimeTargetUnavailable}
	games := service.GameDefinitions()
	if len(games) != 3 {
		t.Fatalf("catalog size = %d, want 3", len(games))
	}
	for _, game := range games {
		if game.Signed || game.Verified || game.Runnable || game.Supported {
			t.Errorf("%s makes an unsupported trust/runtime claim: %+v", game.ID, game)
		}
		if game.TrustLevel != gameTrustLevelLocal || game.Source != gameSourceEmbeddedV1Alpha1 {
			t.Errorf("%s provenance = %s/%s, want %s/%s", game.ID, game.TrustLevel, game.Source, gameTrustLevelLocal, gameSourceEmbeddedV1Alpha1)
		}
		if !reflect.DeepEqual(game.SupportReasons, wantReasons) {
			t.Errorf("%s support reasons = %v, want %v", game.ID, game.SupportReasons, wantReasons)
		}
	}

	games[0].SupportReasons[0] = "MUTATED_BY_CALLER"
	if got := service.GameDefinitions()[0].SupportReasons[0]; got != gameReasonSignatureUnverified {
		t.Fatalf("catalog response aliases stored support reasons: %q", got)
	}
}

func TestMemoryCreateServerRejectsUnavailableRuntimeTargetWithoutSideEffects(t *testing.T) {
	service := NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()
	actor := testActor("admin-1", "GuGu Admin")
	game := service.games["io.gugumanager.papermc"]

	service.mu.RLock()
	beforeServers := len(service.servers)
	beforeAllocations := len(service.allocations)
	beforeOperations := len(service.operations)
	beforeIdempotency := len(service.idempotency)
	beforeAudit := len(service.audit)
	beforeNode := service.nodes[availableNodeID]
	beforeGame := service.games[game.ID]
	service.mu.RUnlock()

	_, err := service.CreateServer(domain.CreateServerInput{
		Name: "must-not-exist", GameDefinitionID: game.ID, GameBundleDigest: game.BundleDigest,
		NodeID: availableNodeID, MemoryMB: 2048, DiskGB: 10,
	}, "catalog-fail-closed-memory", actor)
	problem := requireProblemCode(t, err, "PACKAGE_INCOMPATIBLE")
	if problem.Details["gameDefinitionId"] != game.ID || !reflect.DeepEqual(problem.Details["supportReasons"], beforeGame.SupportReasons) {
		t.Fatalf("structured package rejection = %+v", problem.Details)
	}

	service.mu.RLock()
	defer service.mu.RUnlock()
	if len(service.servers) != beforeServers || len(service.allocations) != beforeAllocations || len(service.operations) != beforeOperations || len(service.idempotency) != beforeIdempotency || len(service.audit) != beforeAudit {
		t.Fatalf("rejected create mutated collections: servers=%d allocations=%d operations=%d idempotency=%d audit=%d", len(service.servers), len(service.allocations), len(service.operations), len(service.idempotency), len(service.audit))
	}
	if !reflect.DeepEqual(service.nodes[availableNodeID], beforeNode) || !reflect.DeepEqual(service.games[game.ID], beforeGame) {
		t.Fatal("rejected create mutated node or catalog accounting")
	}
}

func TestPostgresCatalogAndCreateServerFailClosedWithoutWrites(t *testing.T) {
	s := testPostgres(t)
	resetTestDatabase(t, s)
	admin := setupAdminForTest(t, s)
	nodeID, err := s.RegisterNode(context.Background(), domain.Node{
		Name: "catalog-truth-node", Condition: "available", Version: "agent-v1", Region: "test",
		Address: "127.0.0.9", CPUCores: 4, MemoryBytes: 8 << 30, DiskBytes: 100 << 30,
	})
	if err != nil {
		t.Fatalf("register node: %v", err)
	}
	game := insertCatalogGameFixture(t, s)

	listed := s.GameDefinitions()
	if len(listed) != 1 {
		t.Fatalf("postgres catalog = %+v, want one entry", listed)
	}
	got := listed[0]
	if got.Signed || got.Verified || got.Runnable || got.Supported || got.TrustLevel != gameTrustLevelLocal || got.Source != gameSourceEmbeddedV1Alpha1 {
		t.Fatalf("postgres catalog makes an unsupported claim: %+v", got)
	}
	if !reflect.DeepEqual(got.SupportReasons, []string{gameReasonSignatureUnverified, gameReasonRuntimeTargetUnavailable}) {
		t.Fatalf("postgres support reasons = %v", got.SupportReasons)
	}

	counts := func() [6]int {
		var result [6]int
		for index, table := range []string{"servers", "allocations", "server_tasks", "startup_values", "outbox_events", "audit_events"} {
			if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&result[index]); err != nil {
				t.Fatalf("count %s: %v", table, err)
			}
		}
		return result
	}
	before := counts()
	_, err = s.CreateServer(domain.CreateServerInput{
		Name: "must-not-exist", GameDefinitionID: game.ID, GameBundleDigest: game.BundleDigest,
		NodeID: nodeID, MemoryMB: 2048, DiskGB: 10,
	}, "catalog-fail-closed-postgres", admin)
	problem := requireProblemCode(t, err, "PACKAGE_INCOMPATIBLE")
	if problem.Details["gameDefinitionId"] != game.ID || problem.Details["source"] != gameSourceEmbeddedV1Alpha1 {
		t.Fatalf("structured postgres package rejection = %+v", problem.Details)
	}
	if after := counts(); after != before {
		t.Fatalf("rejected postgres create wrote state: before=%v after=%v", before, after)
	}
}
