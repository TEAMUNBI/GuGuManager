package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
)

func TestEmbeddedCatalogOnlyMarksPinnedPaperMCRuntimeRunnable(t *testing.T) {
	service := NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()

	games := service.GameDefinitions()
	if len(games) != 3 {
		t.Fatalf("catalog size = %d, want 3", len(games))
	}
	for _, game := range games {
		if game.Signed || game.Verified || game.Supported {
			t.Errorf("%s makes an unsupported signature/support claim: %+v", game.ID, game)
		}
		if game.TrustLevel != gameTrustLevelLocal || game.Source != gameSourceEmbeddedV1Alpha1 {
			t.Errorf("%s provenance = %s/%s, want %s/%s", game.ID, game.TrustLevel, game.Source, gameTrustLevelLocal, gameSourceEmbeddedV1Alpha1)
		}
		if game.RuntimeTarget == nil || game.RuntimeTarget.Digest == "" {
			t.Errorf("%s has no immutable runtime target", game.ID)
			continue
		}
		if game.ID == paperMCGameID {
			if !game.Runnable || game.RuntimeTarget.Image != paperMCRuntimeImage || !reflect.DeepEqual(game.SupportReasons, []string{gameReasonSignatureUnverified}) {
				t.Errorf("PaperMC runtime evidence = %+v", game)
			}
		} else if game.Runnable || !reflect.DeepEqual(game.SupportReasons, []string{gameReasonSignatureUnverified, gameReasonRuntimeTargetUnavailable}) {
			t.Errorf("%s unexpectedly became runnable: %+v", game.ID, game)
		}
	}

	games[0].SupportReasons[0] = "MUTATED_BY_CALLER"
	games[0].RuntimeTarget.Environment["TYPE"] = "MUTATED_BY_CALLER"
	if got := service.GameDefinitions()[0].SupportReasons[0]; got != gameReasonSignatureUnverified {
		t.Fatalf("catalog response aliases stored support reasons: %q", got)
	}
	if got := service.GameDefinitions()[0].RuntimeTarget.Environment["TYPE"]; got == "MUTATED_BY_CALLER" {
		t.Fatal("catalog response aliases stored runtime target")
	}
}

func TestMemoryCreateServerRejectsUnavailableRuntimeTargetWithoutSideEffects(t *testing.T) {
	service := NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()
	actor := testActor("admin-1", "GuGu Admin")
	game := service.games["io.gugumanager.factorio"]

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
	games, err := loadFixedGameCatalog()
	if err != nil {
		t.Fatalf("load fixed catalog: %v", err)
	}
	var game domain.GameDefinition
	for _, candidate := range games {
		if candidate.ID == "io.gugumanager.factorio" {
			game = candidate
			break
		}
	}
	if game.ID == "" {
		t.Fatal("fixed catalog does not contain Factorio")
	}
	if _, err := s.db.Exec(`
		INSERT INTO game_definitions (id, name, source_url, review_status)
		VALUES ($1, $2, 'https://example.invalid/factorio.json', 'approved')
	`, game.ID, game.Name); err != nil {
		t.Fatalf("insert game definition: %v", err)
	}
	if _, err := s.db.Exec(`
		INSERT INTO game_bundles (game_definition_id, definition_version, game_version, digest, schema_version, license, compatibility, published_at)
		VALUES ($1, $2, $3, $4, 'v1', 'MIT', '{}'::jsonb, now())
	`, game.ID, game.Version, game.GameVersion, game.BundleDigest); err != nil {
		t.Fatalf("insert game bundle: %v", err)
	}

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
