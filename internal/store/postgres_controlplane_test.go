package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/httpapi"
)

// Compile-time assertion: *Postgres must satisfy the full ControlPlane
// interface once postgres_controlplane.go is implemented.
var _ httpapi.ControlPlane = (*Postgres)(nil)

// controlPlaneFixture resets the shared test database and returns an admin
// user, a registered node, and a ready server backed by the embedded fixed
// PaperMC bundle (so Startup/UpdateStartup templates resolve).
func controlPlaneFixture(t *testing.T, s *Postgres) (domain.User, string, string) {
	t.Helper()
	resetTestDatabase(t, s)
	admin := setupAdminForTest(t, s)

	nodeID, err := s.RegisterNode(context.Background(), domain.Node{
		Name: "cp-node", Condition: "available", Version: "agent-v1", Region: "cn",
		Address: "127.0.0.1", CPUCores: 8, MemoryBytes: 16 << 30, DiskBytes: 1 << 40,
	})
	if err != nil {
		t.Fatalf("register node: %v", err)
	}

	game := insertCatalogGameFixture(t, s)
	op, err := s.CreateServer(domain.CreateServerInput{
		Name:             "cp-server-1",
		GameDefinitionID: game.ID,
		GameBundleDigest: game.BundleDigest,
		NodeID:           nodeID,
		MemoryMB:         2048,
		DiskGB:           20,
	}, "idem-cp-create-0001", admin)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	// Simulate the agent completing the provision task so later operations are
	// not blocked by the exclusive provision task (mirrors Memory semantics).
	if err := s.CompleteTask(context.Background(), op.ID, nodeID, true, nil, nil); err != nil {
		t.Fatalf("complete provision: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE servers SET lifecycle_state = 'ready' WHERE id = $1`, op.ServerID); err != nil {
		t.Fatalf("mark server ready: %v", err)
	}
	return admin, nodeID, op.ServerID
}

// insertCatalogGameFixture seeds game_definitions/game_bundles rows for the
// embedded fixed PaperMC bundle so its digest matches the fixed catalog.
func insertCatalogGameFixture(t *testing.T, s *Postgres) domain.GameDefinition {
	t.Helper()
	games, err := loadFixedGameCatalog()
	if err != nil {
		t.Fatalf("load fixed game catalog: %v", err)
	}
	var game domain.GameDefinition
	for _, candidate := range games {
		if candidate.ID == "io.gugumanager.papermc" {
			game = candidate
			break
		}
	}
	if game.ID == "" {
		t.Fatal("fixed catalog does not contain io.gugumanager.papermc")
	}
	if _, err := s.db.Exec(`
		INSERT INTO game_definitions (id, name, source_url, review_status)
		VALUES ('io.gugumanager.papermc', 'PaperMC', 'https://example.invalid/papermc.json', 'approved')`); err != nil {
		t.Fatalf("insert game definition: %v", err)
	}
	if _, err := s.db.Exec(`
		INSERT INTO game_bundles (game_definition_id, definition_version, game_version, digest, schema_version, license, compatibility, published_at)
		VALUES ('io.gugumanager.papermc', '1.0.0', '1.21.8', $1, 'v1', 'Apache-2.0', '{}'::jsonb, now())
	`, game.BundleDigest); err != nil {
		t.Fatalf("insert game bundle: %v", err)
	}
	return game
}

func TestPostgresControlPlaneOverviewAndReads(t *testing.T) {
	s := testPostgres(t)
	admin, _, serverID := controlPlaneFixture(t, s)

	overview := s.Overview()
	if overview.ServerCount != 1 || overview.TotalNodeCount != 1 {
		t.Fatalf("overview counts = %+v, want 1 server and 1 node", overview)
	}
	if overview.RecentActivity == nil {
		t.Fatal("overview RecentActivity must not be nil")
	}

	visible := s.VisibleServers(admin.ID, "")
	if len(visible) != 1 || visible[0].ID != serverID {
		t.Fatalf("VisibleServers(admin) = %+v, want only %s", visible, serverID)
	}
	if filtered := s.VisibleServers(admin.ID, "no-such-name"); len(filtered) != 0 {
		t.Fatalf("filtered VisibleServers = %+v, want none", filtered)
	}

	perms, err := s.EffectiveServerPermissions(admin.ID, serverID)
	if err != nil {
		t.Fatalf("effective permissions: %v", err)
	}
	if !containsString(perms.Permissions, "servers.power") {
		t.Fatalf("admin permissions = %v, want full set", perms.Permissions)
	}
	if err := s.AuthorizeServer(admin.ID, serverID, "servers.power"); err != nil {
		t.Fatalf("authorize admin: %v", err)
	}
	if _, err := s.EffectiveServerPermissions(admin.ID, "00000000-0000-4000-8000-000000000099"); err == nil {
		t.Fatal("expected effective permissions for missing server to fail")
	}

	if games := s.GameDefinitions(); len(games) == 0 {
		t.Fatal("GameDefinitions() must not be empty")
	}
	s.AuditEvents() // must not panic
}

func TestPostgresControlPlanePowerAndOperations(t *testing.T) {
	s := testPostgres(t)
	admin, _, serverID := controlPlaneFixture(t, s)

	op, err := s.RequestPower(serverID, domain.PowerStart, "idem-cp-power-0001", admin)
	if err != nil {
		t.Fatalf("request power: %v", err)
	}
	if op.Status != "queued" || op.Type != domain.PowerStart || op.Generation != 2 {
		t.Fatalf("queued power operation = %+v, want start/generation 2", op)
	}

	got, err := s.Operation(op.ID)
	if err != nil {
		t.Fatalf("operation by id: %v", err)
	}
	if got.ID != op.ID || got.Status != "queued" {
		t.Fatalf("operation lookup = %+v, want %+v", got, op)
	}
	if _, err := s.Operation("00000000-0000-4000-8000-000000000099"); err == nil {
		t.Fatal("expected operation lookup for missing id to fail")
	}

	// Same key + same body replays the recorded operation.
	replayed, err := s.RequestPower(serverID, domain.PowerStart, "idem-cp-power-0001", admin)
	if err != nil {
		t.Fatalf("replay power: %v", err)
	}
	if replayed.ID != op.ID {
		t.Fatalf("idempotent replay = %s, want %s", replayed.ID, op.ID)
	}

	// An unsupported action maps to VALIDATION_FAILED.
	if _, err := s.RequestPower(serverID, domain.PowerAction("wipe"), "idem-cp-power-0002", admin); err == nil {
		t.Fatal("expected unsupported power action to fail")
	}

	ops := s.VisibleOperations(admin.ID)
	if len(ops) < 1 {
		t.Fatalf("VisibleOperations(admin) = %+v, want at least the power operation", ops)
	}
}

func TestPostgresControlPlaneAllocationsAndStartup(t *testing.T) {
	s := testPostgres(t)
	admin, nodeID, serverID := controlPlaneFixture(t, s)

	allocs, err := s.Allocations(serverID)
	if err != nil {
		t.Fatalf("allocations: %v", err)
	}
	if len(allocs) != 1 || !allocs[0].Primary {
		t.Fatalf("initial allocations = %+v, want one primary", allocs)
	}
	generation := int64(1)

	op, err := s.CreateAllocation(serverID, domain.CreateAllocationInput{
		BindIP: "127.0.0.2", Port: 30001, Protocol: "tcp",
	}, generation, "idem-cp-alloc-0001", admin)
	if err != nil {
		t.Fatalf("create allocation: %v", err)
	}
	if op.Type != "reconcile" || op.Generation != generation+1 {
		t.Fatalf("create allocation operation = %+v, want reconcile/generation %d", op, generation+1)
	}
	generation = op.Generation
	if err := s.CompleteTask(context.Background(), op.ID, nodeID, true, nil, nil); err != nil {
		t.Fatalf("complete reconcile task: %v", err)
	}

	// A stale generation fence must fail with PRECONDITION_FAILED.
	_, err = s.CreateAllocation(serverID, domain.CreateAllocationInput{
		BindIP: "127.0.0.3", Port: 30002, Protocol: "tcp",
	}, 1, "idem-cp-alloc-0002", admin)
	if err == nil {
		t.Fatal("expected stale generation to fail")
	}
	if problem, ok := err.(*domain.Problem); !ok || problem.Code != "PRECONDITION_FAILED" {
		t.Fatalf("stale generation error = %v, want PRECONDITION_FAILED", err)
	}

	allocs, err = s.Allocations(serverID)
	if err != nil {
		t.Fatalf("allocations after create: %v", err)
	}
	if len(allocs) != 2 {
		t.Fatalf("allocations after create = %+v, want 2", allocs)
	}
	var secondID string
	for _, allocation := range allocs {
		if !allocation.Primary {
			secondID = allocation.ID
		}
	}
	if secondID == "" {
		t.Fatalf("expected exactly one non-primary allocation in %+v", allocs)
	}

	setOp, err := s.SetPrimaryAllocation(serverID, secondID, generation, "idem-cp-primary-0001", admin)
	if err != nil {
		t.Fatalf("set primary: %v", err)
	}
	if setOp.Generation != generation+1 {
		t.Fatalf("set primary generation = %d, want %d", setOp.Generation, generation+1)
	}
	generation = setOp.Generation
	if err := s.CompleteTask(context.Background(), setOp.ID, nodeID, true, nil, nil); err != nil {
		t.Fatalf("complete primary reconcile task: %v", err)
	}
	allocs, _ = s.Allocations(serverID)
	primaryCount := 0
	var primaryID string
	for _, allocation := range allocs {
		if allocation.Primary {
			primaryCount++
			primaryID = allocation.ID
		}
	}
	if primaryCount != 1 || primaryID != secondID {
		t.Fatalf("primary switch mismatch: primary=%s (want %s), all=%+v", primaryID, secondID, allocs)
	}

	delOp, err := s.DeleteAllocation(serverID, allocs[0].ID, generation, "idem-cp-del-0001", admin)
	if err != nil {
		t.Fatalf("delete non-primary allocation: %v", err)
	}
	if delOp.Generation != generation+1 {
		t.Fatalf("delete allocation generation = %d, want %d", delOp.Generation, generation+1)
	}
	generation = delOp.Generation
	if err := s.CompleteTask(context.Background(), delOp.ID, nodeID, true, nil, nil); err != nil {
		t.Fatalf("complete delete reconcile task: %v", err)
	}
	allocs, _ = s.Allocations(serverID)
	if len(allocs) != 1 {
		t.Fatalf("allocations after delete = %+v, want 1", allocs)
	}

	// Startup round-trips through UpdateStartup with the generation fence.
	startup, err := s.Startup(serverID)
	if err != nil {
		t.Fatalf("startup: %v", err)
	}
	if startup.ServerID != serverID || startup.Generation != generation {
		t.Fatalf("startup = %+v, want server %s generation %d", startup, serverID, generation)
	}
	foundMemory := false
	for _, variable := range startup.Variables {
		if variable.Key == "memory_mb" {
			foundMemory = true
		}
		if variable.Secret && (variable.Value != nil || variable.Default != nil) {
			t.Fatalf("startup must not expose secret values: %+v", variable)
		}
	}
	if !foundMemory {
		t.Fatalf("startup variables missing memory_mb: %+v", startup.Variables)
	}

	updOp, err := s.UpdateStartup(serverID, map[string]any{"memory_mb": 4096}, generation, "idem-cp-startup-0001", admin)
	if err != nil {
		t.Fatalf("update startup: %v", err)
	}
	if updOp.Generation != generation+1 {
		t.Fatalf("update startup generation = %d, want %d", updOp.Generation, generation+1)
	}
	generation = updOp.Generation
	if err := s.CompleteTask(context.Background(), updOp.ID, nodeID, true, nil, nil); err != nil {
		t.Fatalf("complete startup reconcile task: %v", err)
	}
	startup, err = s.Startup(serverID)
	if err != nil {
		t.Fatalf("startup after update: %v", err)
	}
	for _, variable := range startup.Variables {
		if variable.Key == "memory_mb" && variable.Value != int64(4096) {
			t.Fatalf("memory_mb after update = %v, want 4096", variable.Value)
		}
	}

	// Undeclared variables must fail validation.
	if _, err := s.UpdateStartup(serverID, map[string]any{"not_a_variable": 1}, generation, "idem-cp-startup-0002", admin); err == nil {
		t.Fatal("expected undeclared startup variable to fail")
	}
}

func TestPostgresControlPlaneFiles(t *testing.T) {
	s := testPostgres(t)
	s.fileRoot = t.TempDir()
	admin, _, serverID := controlPlaneFixture(t, s)

	entries, err := s.Files(serverID, "")
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("files on fresh server = %+v, want empty", entries)
	}

	if err := s.CreateDirectory(serverID, "config", admin); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	if err := s.WriteFile(serverID, "config/server.properties", []byte("motd=hello\n"), admin); err != nil {
		t.Fatalf("write file: %v", err)
	}
	content, err := s.ReadFile(serverID, "config/server.properties")
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if content.Content != "motd=hello\n" || content.Encoding != "utf-8" {
		t.Fatalf("read content = %+v, want utf-8 motd", content)
	}
	if err := s.CreateDirectory(serverID, "logs", admin); err != nil {
		t.Fatalf("create directory: %v", err)
	}
	if err := s.MoveFile(serverID, "config/server.properties", "config/moved.properties", false, admin); err != nil {
		t.Fatalf("move file: %v", err)
	}
	if _, err := s.ReadFile(serverID, "config/server.properties"); err == nil {
		t.Fatal("expected moved source to be gone")
	}
	if err := s.DeleteFile(serverID, "config/moved.properties", false, admin); err != nil {
		t.Fatalf("delete file: %v", err)
	}

	entries, err = s.Files(serverID, "config")
	if err != nil {
		t.Fatalf("files after mutations: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("config entries after cleanup = %+v, want empty", entries)
	}

	// Escaping the server root must be blocked.
	if _, err := s.ReadFile(serverID, "../../etc/passwd"); err == nil {
		t.Fatal("expected path escape to fail")
	}
}

func TestPostgresControlPlaneBackupsConsoleHeartbeat(t *testing.T) {
	s := testPostgres(t)
	admin, nodeID, serverID := controlPlaneFixture(t, s)

	op, err := s.CreateBackup(serverID, "idem-cp-backup-0001", admin)
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	if op.Type != "backup" {
		t.Fatalf("backup operation type = %s, want backup", op.Type)
	}
	backups, err := s.Backups(serverID)
	if err != nil {
		t.Fatalf("backups: %v", err)
	}
	if len(backups) != 1 || backups[0].Status != "creating" {
		t.Fatalf("backups = %+v, want one creating", backups)
	}
	backupID := backups[0].ID
	// Simulate the agent finishing the backup task so the restore path is not
	// blocked by the exclusive backup task.
	if err := s.CompleteTask(context.Background(), op.ID, nodeID, true, nil, nil); err != nil {
		t.Fatalf("complete backup task: %v", err)
	}

	// A backup that is not ready cannot be restored or deleted.
	if _, err := s.RestoreBackup(serverID, backupID, "idem-cp-restore-0001", admin); err == nil {
		t.Fatal("expected restoring a non-ready backup to fail")
	}
	if _, err := s.DeleteBackup(serverID, backupID, "idem-cp-del-backup-0001", admin); err == nil {
		t.Fatal("expected deleting a non-ready backup to fail")
	}

	// Mark the backup ready via SQL and restore it from a stopped server.
	if _, err := s.db.Exec(`UPDATE backups SET status = 'ready', content_digest = $1 WHERE id = $2`,
		"sha256:"+strings.Repeat("ab", 32), backupID); err != nil {
		t.Fatalf("mark backup ready: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE servers SET observed_power = 'stopped' WHERE id = $1`, serverID); err != nil {
		t.Fatalf("stop server: %v", err)
	}
	restoreOp, err := s.RestoreBackup(serverID, backupID, "idem-cp-restore-0001", admin)
	if err != nil {
		t.Fatalf("restore backup: %v", err)
	}
	if restoreOp.Type != "restore" {
		t.Fatalf("restore operation type = %s, want restore", restoreOp.Type)
	}
	backups, _ = s.Backups(serverID)
	if len(backups) != 1 || backups[0].Status != "restoring" {
		t.Fatalf("backups after restore = %+v, want one restoring", backups)
	}

	// Console is metadata-level this stage: no panic, and a command on a
	// stopped server is rejected.
	if lines, err := s.Console(serverID); err != nil || len(lines) != 0 {
		t.Fatalf("console = %+v, %v; want empty list", lines, err)
	}
	if err := s.SendConsoleCommand(serverID, "say hi", admin); err == nil {
		t.Fatal("expected console command on a stopped server to fail")
	}
	if _, err := s.db.Exec(`UPDATE servers SET observed_power = 'running' WHERE id = $1`, serverID); err != nil {
		t.Fatalf("run server: %v", err)
	}
	if err := s.SendConsoleCommand(serverID, "say hi", admin); err != nil {
		t.Fatalf("send console command: %v", err)
	}

	if err := s.Heartbeat(nodeID+"-missing", "agent-v2"); err == nil {
		t.Fatal("expected heartbeat for unknown node to fail")
	}
	if err := s.Heartbeat("cp-node", "agent-v2"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
}

func TestCreateBackupWritesCheckpointPayload(t *testing.T) {
	s := testPostgres(t)
	admin, _, serverID := controlPlaneFixture(t, s)

	op, err := s.CreateBackup(serverID, "idem-cp-checkpoint-0001", admin)
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	var checkpoint string
	if err := s.db.QueryRow(`SELECT COALESCE(checkpoint::text, '') FROM server_tasks WHERE id = $1`, op.ID).Scan(&checkpoint); err != nil {
		t.Fatalf("query checkpoint: %v", err)
	}
	if checkpoint == "" {
		t.Fatal("expected non-empty checkpoint for backup task")
	}
	var payload struct {
		BackupID         string `json:"backupId"`
		StorageObjectKey string `json:"storageObjectKey"`
	}
	if err := json.Unmarshal([]byte(checkpoint), &payload); err != nil {
		t.Fatalf("decode checkpoint: %v", err)
	}
	if payload.BackupID == "" {
		t.Error("expected backupId in checkpoint")
	}
	if want := "backups/" + payload.BackupID + ".tar.gz"; payload.StorageObjectKey != want {
		t.Errorf("storageObjectKey = %q, want %q", payload.StorageObjectKey, want)
	}
}

func TestCompleteBackupTaskMarksBackupReady(t *testing.T) {
	s := testPostgres(t)
	admin, nodeID, serverID := controlPlaneFixture(t, s)

	op, err := s.CreateBackup(serverID, "idem-cp-ready-0001", admin)
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	backups, err := s.Backups(serverID)
	if err != nil {
		t.Fatalf("backups: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("backups = %+v, want 1", backups)
	}
	backupID := backups[0].ID
	result := []byte(`{"checksum":"sha256:` + strings.Repeat("ab", 32) + `","sizeBytes":42,"storageLocation":"backups/` + backupID + `.tar.gz"}`)
	if err := s.CompleteTask(context.Background(), op.ID, nodeID, true, nil, result); err != nil {
		t.Fatalf("complete backup task: %v", err)
	}

	var status, checksum, storageLocation string
	var sizeBytes int64
	if err := s.db.QueryRow(`
		SELECT status, COALESCE(content_digest, ''), COALESCE(storage_location, ''), COALESCE(size_bytes, 0)
		FROM backups WHERE id = $1`, backupID).Scan(&status, &checksum, &storageLocation, &sizeBytes); err != nil {
		t.Fatalf("query backup state: %v", err)
	}
	if status != "ready" {
		t.Errorf("backup status = %q, want ready", status)
	}
	if checksum != "sha256:"+strings.Repeat("ab", 32) {
		t.Errorf("backup checksum = %q", checksum)
	}
	if sizeBytes != 42 {
		t.Errorf("backup size = %d, want 42", sizeBytes)
	}
	if want := "backups/" + backupID + ".tar.gz"; storageLocation != want {
		t.Errorf("storage location = %q, want %q", storageLocation, want)
	}
}
