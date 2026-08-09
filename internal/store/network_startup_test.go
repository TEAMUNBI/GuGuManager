package store

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	gamedefinition "github.com/gugumanager/gugumanager/spec/game-definition"
)

func TestAllocationLifecycleMaintainsGenerationUniquenessAndPrimary(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	actor := testActor("admin-1", "GuGu Admin")

	server, err := service.Server(stoppedServerID)
	if err != nil {
		t.Fatal(err)
	}
	seeded, err := service.Allocations(stoppedServerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded) != 1 || !seeded[0].Primary {
		t.Fatalf("seeded allocations = %+v, want one primary", seeded)
	}

	created, err := service.CreateAllocation(stoppedServerID, domain.CreateAllocationInput{
		BindIP: "10.0.20.14", Port: 34198, Protocol: "udp", Primary: false,
	}, server.Generation, "allocation-create-0001", actor)
	if err != nil {
		t.Fatalf("CreateAllocation failed: %v", err)
	}
	if created.Type != domain.PowerAction("reconcile") || created.Generation != server.Generation+1 {
		t.Fatalf("create operation = %+v", created)
	}
	if completed := waitForStoredOperation(t, service, created.ID); completed.Status != "succeeded" {
		t.Fatalf("create operation status = %q", completed.Status)
	}

	allocations, err := service.Allocations(stoppedServerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(allocations) != 2 || countPrimaryAllocations(allocations) != 1 {
		t.Fatalf("allocations after create = %+v", allocations)
	}
	var secondary domain.Allocation
	for _, allocation := range allocations {
		if !allocation.Primary {
			secondary = allocation
		}
	}
	if secondary.ID == "" {
		t.Fatal("created secondary allocation not found")
	}

	_, err = service.CreateAllocation(stoppedServerID, domain.CreateAllocationInput{
		BindIP: "10.0.20.14", Port: 34199, Protocol: "udp", Primary: false,
	}, server.Generation, "allocation-stale-0001", actor)
	requireProblemCode(t, err, "PRECONDITION_FAILED")

	_, err = service.CreateAllocation(stoppedServerID, domain.CreateAllocationInput{
		BindIP: "10.0.20.14", Port: 34198, Protocol: "udp", Primary: false,
	}, created.Generation, "allocation-duplicate-01", actor)
	requireProblemCode(t, err, "PORT_CONFLICT")

	promoted, err := service.SetPrimaryAllocation(stoppedServerID, secondary.ID, created.Generation, "allocation-primary-0001", actor)
	if err != nil {
		t.Fatalf("SetPrimaryAllocation failed: %v", err)
	}
	waitForStoredOperation(t, service, promoted.ID)
	allocations, _ = service.Allocations(stoppedServerID)
	if countPrimaryAllocations(allocations) != 1 || !allocationByID(allocations, secondary.ID).Primary {
		t.Fatalf("allocations after primary switch = %+v", allocations)
	}

	_, err = service.DeleteAllocation(stoppedServerID, secondary.ID, promoted.Generation, "allocation-delete-main", actor)
	requireProblemCode(t, err, "OPERATION_CONFLICT")

	oldPrimaryID := seeded[0].ID
	deleted, err := service.DeleteAllocation(stoppedServerID, oldPrimaryID, promoted.Generation, "allocation-delete-0001", actor)
	if err != nil {
		t.Fatalf("DeleteAllocation failed: %v", err)
	}
	waitForStoredOperation(t, service, deleted.ID)
	allocations, _ = service.Allocations(stoppedServerID)
	if len(allocations) != 1 || allocations[0].ID != secondary.ID || !allocations[0].Primary {
		t.Fatalf("allocations after delete = %+v", allocations)
	}

	_, err = service.DeleteAllocation(stoppedServerID, secondary.ID, deleted.Generation, "allocation-delete-last", actor)
	requireProblemCode(t, err, "OPERATION_CONFLICT")
}

func TestAllocationValidationIdempotencyExclusiveGateAndOfflineNode(t *testing.T) {
	actor := testActor("admin-1", "GuGu Admin")
	tests := []domain.CreateAllocationInput{
		{BindIP: "not-an-ip", Port: 25565, Protocol: "tcp"},
		{BindIP: "0.0.0.0", Port: 25565, Protocol: "tcp"},
		{BindIP: "::", Port: 25565, Protocol: "tcp"},
		{BindIP: "127.0.0.1", Port: 0, Protocol: "tcp"},
		{BindIP: "127.0.0.1", Port: 65536, Protocol: "tcp"},
		{BindIP: "127.0.0.1", Port: 25565, Protocol: "sctp"},
	}
	for index, input := range tests {
		service := newTestMemory(time.Second)
		server, _ := service.Server(stoppedServerID)
		_, err := service.CreateAllocation(stoppedServerID, input, server.Generation, "allocation-invalid-000"+string(rune('1'+index)), actor)
		requireProblemCode(t, err, "VALIDATION_FAILED")
	}

	service := newTestMemory(time.Second)
	server, _ := service.Server(stoppedServerID)
	input := domain.CreateAllocationInput{BindIP: "10.0.20.14", Port: 34200, Protocol: "udp"}
	first, err := service.CreateAllocation(stoppedServerID, input, server.Generation, "allocation-idempotent", actor)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := service.CreateAllocation(stoppedServerID, input, server.Generation, "allocation-idempotent", actor)
	if err != nil || duplicate.ID != first.ID {
		t.Fatalf("idempotent allocation = %+v, err=%v, want %s", duplicate, err, first.ID)
	}
	_, err = service.UpdateStartup(stoppedServerID, map[string]any{"server_name": "blocked"}, first.Generation, "startup-blocked-0001", actor)
	problem := requireProblemCode(t, err, "OPERATION_IN_PROGRESS")
	if problem.Details["operationId"] != first.ID {
		t.Fatalf("blocking operation = %+v", problem.Details)
	}

	offline, _ := service.Server("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	_, err = service.CreateAllocation(offline.ID, domain.CreateAllocationInput{BindIP: "10.0.30.18", Port: 42421, Protocol: "tcp"}, offline.Generation, "allocation-offline-001", actor)
	requireProblemCode(t, err, "NODE_OFFLINE")
}

func TestStartupUpdateValidatesDeclarationsTypesConstraintsAndSecrets(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	actor := testActor("admin-1", "GuGu Admin")
	server, _ := service.Server(stoppedServerID)

	startup, err := service.Startup(stoppedServerID)
	if err != nil {
		t.Fatal(err)
	}
	if startup.Command.Executable == "" || len(startup.Command.Args) == 0 {
		t.Fatalf("resolved command = %+v", startup.Command)
	}
	secret := startupVariableByKey(startup.Variables, "server_token")
	if !secret.Secret || !secret.HasValue || secret.Value != nil {
		t.Fatalf("secret view leaked or lost state: %+v", secret)
	}
	difficulty := startupVariableByKey(startup.Variables, "difficulty")
	if len(difficulty.EnumValues) != 3 || difficulty.EnumValues[1] != "normal" {
		t.Fatalf("difficulty enum metadata = %+v", difficulty.EnumValues)
	}

	operation, err := service.UpdateStartup(stoppedServerID, map[string]any{
		"server_name":       "Friday Factory 2",
		"autosave_interval": 15,
		"public_listing":    true,
		"server_token":      nil,
	}, server.Generation, "startup-update-00001", actor)
	if err != nil {
		t.Fatalf("UpdateStartup failed: %v", err)
	}
	if operation.Type != domain.PowerAction("reconcile") || operation.Generation != server.Generation+1 {
		t.Fatalf("startup operation = %+v", operation)
	}
	duplicate, err := service.UpdateStartup(stoppedServerID, map[string]any{
		"server_name":       "Friday Factory 2",
		"autosave_interval": 15,
		"public_listing":    true,
		"server_token":      nil,
	}, server.Generation, "startup-update-00001", actor)
	if err != nil || duplicate.ID != operation.ID {
		t.Fatalf("startup idempotency = %+v, err=%v", duplicate, err)
	}
	waitForStoredOperation(t, service, operation.ID)

	updated, err := service.Startup(stoppedServerID)
	if err != nil {
		t.Fatal(err)
	}
	if got := startupVariableByKey(updated.Variables, "server_name").Value; got != "Friday Factory 2" {
		t.Fatalf("server_name = %#v", got)
	}
	if got := startupVariableByKey(updated.Variables, "autosave_interval").Value; got != int64(15) {
		t.Fatalf("autosave_interval = %#v, want int64(15)", got)
	}
	if got := startupVariableByKey(updated.Variables, "public_listing").Value; got != true {
		t.Fatalf("public_listing = %#v", got)
	}
	if got := startupVariableByKey(updated.Variables, "server_token"); got.HasValue || got.Value != nil {
		t.Fatalf("cleared secret = %+v", got)
	}
	audits := service.AuditEvents()
	accepted, succeeded := false, false
	for _, event := range audits {
		if event.OperationID != operation.ID || event.Action != "server.startup.update" {
			continue
		}
		accepted = accepted || event.Result == "accepted"
		succeeded = succeeded || event.Result == "success"
	}
	if !accepted || !succeeded {
		t.Fatalf("startup audits accepted=%v success=%v: %+v", accepted, succeeded, audits)
	}

	invalidUpdates := []map[string]any{
		{"undeclared": "value"},
		{"server_name": ""},
		{"autosave_interval": 0},
		{"autosave_interval": 1.5},
		{"public_listing": "true"},
	}
	for index, updates := range invalidUpdates {
		fresh := newTestMemory(time.Millisecond)
		current, _ := fresh.Server(stoppedServerID)
		_, err := fresh.UpdateStartup(stoppedServerID, updates, current.Generation, "startup-invalid-000"+string(rune('1'+index)), actor)
		requireProblemCode(t, err, "VALIDATION_FAILED")
	}

	paper := newTestMemory(time.Millisecond)
	paperServer, _ := paper.Server("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	_, err = paper.UpdateStartup(paperServer.ID, map[string]any{"accept_eula": nil}, paperServer.Generation, "startup-required-null", actor)
	requireProblemCode(t, err, "VALIDATION_FAILED")
	_, err = paper.UpdateStartup(paperServer.ID, map[string]any{"accept_eula": false}, paperServer.Generation, "startup-const-false1", actor)
	requireProblemCode(t, err, "VALIDATION_FAILED")

	_, err = service.UpdateStartup(stoppedServerID, map[string]any{"server_name": "stale"}, server.Generation, "startup-stale-000001", actor)
	requireProblemCode(t, err, "PRECONDITION_FAILED")
}

func TestStartupVariablePublicViewRedactsSecretMaterial(t *testing.T) {
	definition := domain.StartupVariable{
		Key: "server_token", Type: "string", Secret: true, Required: true,
		Default: "default-secret", Value: "template-secret",
		EnumValues: []string{"enum-secret-a", "enum-secret-b"}, ConstValue: "const-secret",
	}

	public := startupVariablePublicView(definition, "configured-secret", true)
	if !public.Secret || !public.Required || !public.HasValue || public.Key != definition.Key || public.Type != definition.Type {
		t.Fatalf("secret declaration state was not preserved: %+v", public)
	}
	if public.Value != nil || public.Default != nil || public.ConstValue != nil || len(public.EnumValues) != 0 {
		t.Fatalf("secret public view leaked value metadata: %+v", public)
	}

	nonSecret := domain.StartupVariable{
		Key: "difficulty", Type: "string", Default: "normal",
		EnumValues: []string{"normal", "hard"}, ConstValue: "normal",
	}
	nonSecretPublic := startupVariablePublicView(nonSecret, "normal", true)
	nonSecretPublic.EnumValues[0] = "mutated"
	if nonSecret.EnumValues[0] != "normal" {
		t.Fatalf("public enum aliases the trusted template: source=%+v public=%+v", nonSecret.EnumValues, nonSecretPublic.EnumValues)
	}
	if nonSecretPublic.Value != "normal" || nonSecretPublic.Default != "normal" || nonSecretPublic.ConstValue != "normal" {
		t.Fatalf("non-secret public view lost supported metadata: %+v", nonSecretPublic)
	}
}

func TestNormalizeStartupIntegerEnforcesJavaScriptSafeRange(t *testing.T) {
	definition := domain.StartupVariable{Key: "player_slots", Type: "integer"}
	for _, value := range []int64{-gamedefinition.MaxSafeStartupInteger, gamedefinition.MaxSafeStartupInteger} {
		normalized, err := normalizeStartupValue(definition, value)
		if err != nil {
			t.Fatalf("safe boundary %d was rejected: %v", value, err)
		}
		if normalized != value {
			t.Fatalf("safe boundary normalized to %#v, want %d", normalized, value)
		}
	}

	for _, value := range []int64{-gamedefinition.MaxSafeStartupInteger - 1, gamedefinition.MaxSafeStartupInteger + 1} {
		_, err := normalizeStartupValue(definition, value)
		problem := requireProblemCode(t, err, "VALIDATION_FAILED")
		if strings.Contains(problem.Message, strconv.FormatInt(value, 10)) {
			t.Fatalf("problem message %q leaked rejected integer %d", problem.Message, value)
		}
	}
	for _, value := range []json.Number{
		"15.0000000000000000000000000000000001",
		"9007199254740990.5",
	} {
		_, err := normalizeStartupValue(definition, value)
		problem := requireProblemCode(t, err, "VALIDATION_FAILED")
		if strings.Contains(problem.Message, value.String()) {
			t.Fatalf("problem message %q leaked rejected number %q", problem.Message, value)
		}
	}
}

func TestStartupIdempotencyCanonicalizesEquivalentIntegerLexemes(t *testing.T) {
	service := newTestMemory(time.Second)
	actor := testActor("admin-1", "GuGu Admin")
	server, err := service.Server(stoppedServerID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.UpdateStartup(
		stoppedServerID,
		map[string]any{"autosave_interval": json.Number("15")},
		server.Generation,
		"startup-integer-lexeme",
		actor,
	)
	if err != nil {
		t.Fatalf("first UpdateStartup failed: %v", err)
	}
	for _, equivalent := range []json.Number{"15.0", "1.5e1"} {
		replayed, err := service.UpdateStartup(
			stoppedServerID,
			map[string]any{"autosave_interval": equivalent},
			server.Generation,
			"startup-integer-lexeme",
			actor,
		)
		if err != nil {
			t.Fatalf("equivalent integer %q did not replay: %v", equivalent, err)
		}
		if replayed.ID != first.ID {
			t.Fatalf("equivalent integer %q returned operation %q, want %q", equivalent, replayed.ID, first.ID)
		}
	}
}

func TestStartupSecretIdempotencyDigestIsKeyed(t *testing.T) {
	service := newTestMemory(time.Second)
	actor := testActor("admin-1", "GuGu Admin")
	server, _ := service.Server(stoppedServerID)
	updates := map[string]any{"server_token": "replacement-secret"}
	key := "startup-secret-digest"

	if _, err := service.UpdateStartup(stoppedServerID, updates, server.Generation, key, actor); err != nil {
		t.Fatalf("UpdateStartup failed: %v", err)
	}

	scope := idempotencyScope("server:startup:update", actor.ID, stoppedServerID, key)
	service.mu.RLock()
	record := service.idempotency[scope]
	service.mu.RUnlock()
	plainDigest := requestDigest(struct {
		Generation int64          `json:"generation"`
		Variables  map[string]any `json:"variables"`
	}{Generation: server.Generation, Variables: updates})
	if record.RequestDigest == plainDigest {
		t.Fatal("secret-bearing idempotency digest must not be an unkeyed SHA-256 fingerprint")
	}
}

func TestStartupUsesFixedBundleSecretMetadata(t *testing.T) {
	service := newTestMemory(time.Millisecond)

	paper, err := service.Startup("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	rconPassword := startupVariableByKey(paper.Variables, "rcon_password")
	if !rconPassword.Secret || !rconPassword.HasValue || rconPassword.Value != nil {
		t.Fatalf("Paper rcon_password = %+v, want a configured non-echoed fixed-bundle secret", rconPassword)
	}
	if legacy := startupVariableByKey(paper.Variables, "server_token"); legacy.Key != "" {
		t.Fatalf("Paper exposed hand-written variable not present in its bundle: %+v", legacy)
	}
	if len(paper.Command.Args) == 0 || paper.Command.Args[0] != "-Xmx2048M" {
		t.Fatalf("Paper command = %+v, want memory binding resolved from the Bundle default", paper.Command)
	}

	factorio, err := service.Startup(stoppedServerID)
	if err != nil {
		t.Fatal(err)
	}
	serverToken := startupVariableByKey(factorio.Variables, "server_token")
	if !serverToken.Secret || !serverToken.HasValue || serverToken.Value != nil {
		t.Fatalf("Factorio server_token = %+v, want a configured non-echoed fixed-bundle secret", serverToken)
	}
}

func TestStartupRejectsBundleIdentityMismatchAndTamperedDeclarations(t *testing.T) {
	t.Run("server digest does not match catalog bundle", func(t *testing.T) {
		service := newTestMemory(time.Millisecond)
		service.mu.Lock()
		server := service.servers[stoppedServerID]
		server.GameBundleDigest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
		service.servers[stoppedServerID] = server
		service.mu.Unlock()

		_, err := service.Startup(stoppedServerID)
		requireProblemCode(t, err, "PACKAGE_INCOMPATIBLE")
	})

	t.Run("server version does not match catalog bundle", func(t *testing.T) {
		service := newTestMemory(time.Millisecond)
		service.mu.Lock()
		server := service.servers[stoppedServerID]
		server.GameDefinitionVersion = "9.9.9"
		service.servers[stoppedServerID] = server
		service.mu.Unlock()

		_, err := service.Startup(stoppedServerID)
		requireProblemCode(t, err, "PACKAGE_INCOMPATIBLE")
	})

	t.Run("catalog document digest does not match its identity", func(t *testing.T) {
		service := newTestMemory(time.Millisecond)
		service.mu.Lock()
		game := service.games["io.gugumanager.factorio"]
		game.BundleDocument += "\n"
		service.games[game.ID] = game
		service.mu.Unlock()

		_, err := service.Startup(stoppedServerID)
		requireProblemCode(t, err, "PACKAGE_INCOMPATIBLE")
	})

	t.Run("hand-written cache variable is rejected", func(t *testing.T) {
		service := newTestMemory(time.Millisecond)
		service.mu.Lock()
		startup := service.startups[stoppedServerID]
		startup.Variables = append(startup.Variables, domain.StartupVariable{Key: "forged_secret", Type: "string", Secret: true})
		service.startups[stoppedServerID] = startup
		service.mu.Unlock()

		_, err := service.Startup(stoppedServerID)
		requireProblemCode(t, err, "PACKAGE_INCOMPATIBLE")
	})
}

func TestDynamicAndSeedServersUseTheSameBundleStartupTemplate(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	seeded, err := service.Startup(stoppedServerID)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := service.CreateServer(domain.CreateServerInput{
		Name:             "Bundle-derived Factorio",
		GameDefinitionID: "io.gugumanager.factorio",
		GameBundleDigest: service.games["io.gugumanager.factorio"].BundleDigest,
		NodeID:           availableNodeID,
		MemoryMB:         4096,
		DiskGB:           20,
	}, "bundle-derived-create", testActor("admin-1", "GuGu Admin"))
	if err != nil {
		t.Fatalf("CreateServer failed: %v", err)
	}
	dynamic, err := service.Startup(operation.ServerID)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(startupTemplateView(dynamic), startupTemplateView(seeded)) {
		t.Fatalf("dynamic startup template = %+v, seeded = %+v", startupTemplateView(dynamic), startupTemplateView(seeded))
	}

	paperOperation, err := service.CreateServer(domain.CreateServerInput{
		Name:             "Bundle-derived Paper",
		GameDefinitionID: "io.gugumanager.papermc",
		GameBundleDigest: service.games["io.gugumanager.papermc"].BundleDigest,
		NodeID:           availableNodeID,
		MemoryMB:         8192,
		DiskGB:           20,
	}, "bundle-derived-paper", testActor("admin-1", "GuGu Admin"))
	if err != nil {
		t.Fatalf("Paper CreateServer failed: %v", err)
	}
	paper, err := service.Startup(paperOperation.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(paper.Command.Args) == 0 || paper.Command.Args[0] != "-Xmx8192M" {
		t.Fatalf("dynamic Paper command = %+v, want generic memory binding", paper.Command)
	}
}

func countPrimaryAllocations(allocations []domain.Allocation) int {
	count := 0
	for _, allocation := range allocations {
		if allocation.Primary {
			count++
		}
	}
	return count
}

func allocationByID(allocations []domain.Allocation, allocationID string) domain.Allocation {
	for _, allocation := range allocations {
		if allocation.ID == allocationID {
			return allocation
		}
	}
	return domain.Allocation{}
}

func startupVariableByKey(variables []domain.StartupVariable, key string) domain.StartupVariable {
	for _, variable := range variables {
		if variable.Key == key {
			return variable
		}
	}
	return domain.StartupVariable{}
}

func startupTemplateView(startup domain.Startup) domain.Startup {
	startup.ServerID = ""
	startup.Generation = 0
	for index := range startup.Variables {
		startup.Variables[index].Value = nil
		startup.Variables[index].HasValue = false
	}
	return startup
}
