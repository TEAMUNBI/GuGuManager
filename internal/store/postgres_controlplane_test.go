package store

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gugumanager/gugumanager/internal/domain"
	serverfiles "github.com/gugumanager/gugumanager/internal/files"
	"github.com/gugumanager/gugumanager/internal/httpapi"
)

// localFileDispatcher 是测试用 FileDispatcher，在本地文件系统上执行操作，
// 模拟 Agent 在容器 /data 目录内的行为。仅供集成测试使用。
type localFileDispatcher struct {
	fileRoot string
}

func (d *localFileDispatcher) fsFor(serverID string) (*serverfiles.ServerFS, error) {
	root := filepath.Join(d.fileRoot, serverID)
	// 模拟 Agent 在容器 /data 目录内的行为：目录始终存在，首次访问时创建。
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return serverfiles.NewServerFS(root, serverfiles.Limits{
		MaxReadBytes:  10 << 20,
		MaxWriteBytes: 10 << 20,
	})
}

func localFileErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case strings.Contains(err.Error(), "escape") || strings.Contains(err.Error(), "unsafe"):
		return "PATH_ESCAPE"
	case strings.Contains(err.Error(), "size limit"):
		return "SIZE_LIMIT"
	case strings.Contains(err.Error(), "not exist"):
		return "NOT_FOUND"
	case strings.Contains(err.Error(), "permission"):
		return "FORBIDDEN"
	default:
		return "VALIDATION_FAILED"
	}
}

func (d *localFileDispatcher) ListFiles(_ context.Context, _, serverID, path string) ([]domain.FileEntry, error) {
	filesys, err := d.fsFor(serverID)
	if err != nil {
		return nil, &AgentFileError{Code: "INTERNAL_ERROR"}
	}
	entries, err := filesys.List(path)
	if err != nil {
		return nil, &AgentFileError{Code: localFileErrorCode(err)}
	}
	result := make([]domain.FileEntry, 0, len(entries))
	for _, e := range entries {
		kind := "file"
		if e.Directory {
			kind = "directory"
		}
		result = append(result, domain.FileEntry{
			Name: e.Name, Path: e.Path, Kind: kind,
			SizeBytes: e.SizeBytes, ModifiedAt: e.ModifiedAt,
		})
	}
	return result, nil
}

func (d *localFileDispatcher) ReadFile(_ context.Context, _, serverID, path string) (domain.FileContent, error) {
	filesys, err := d.fsFor(serverID)
	if err != nil {
		return domain.FileContent{}, &AgentFileError{Code: "INTERNAL_ERROR"}
	}
	content, err := filesys.ReadFile(path)
	if err != nil {
		return domain.FileContent{}, &AgentFileError{Code: localFileErrorCode(err)}
	}
	entry, err := filesys.Stat(path)
	if err != nil {
		return domain.FileContent{}, &AgentFileError{Code: localFileErrorCode(err)}
	}
	encoded := string(content)
	encoding := "utf-8"
	if !utf8.Valid(content) {
		encoded = base64.RawStdEncoding.EncodeToString(content)
		encoding = "base64"
	}
	return domain.FileContent{
		Path: entry.Path, Content: encoded, Encoding: encoding,
		SizeBytes: entry.SizeBytes, ModifiedAt: entry.ModifiedAt,
	}, nil
}

func (d *localFileDispatcher) WriteFile(_ context.Context, _, serverID, path string, content []byte, _ bool) error {
	filesys, err := d.fsFor(serverID)
	if err != nil {
		return &AgentFileError{Code: "INTERNAL_ERROR"}
	}
	if err := filesys.WriteFile(path, content); err != nil {
		return &AgentFileError{Code: localFileErrorCode(err)}
	}
	return nil
}

func (d *localFileDispatcher) MakeDirectory(_ context.Context, _, serverID, path string) error {
	filesys, err := d.fsFor(serverID)
	if err != nil {
		return &AgentFileError{Code: "INTERNAL_ERROR"}
	}
	if err := filesys.Mkdir(path); err != nil {
		return &AgentFileError{Code: localFileErrorCode(err)}
	}
	return nil
}

func (d *localFileDispatcher) MoveFile(_ context.Context, _, serverID, source, destination string, replace bool) error {
	filesys, err := d.fsFor(serverID)
	if err != nil {
		return &AgentFileError{Code: "INTERNAL_ERROR"}
	}
	if err := filesys.Move(source, destination, replace); err != nil {
		return &AgentFileError{Code: localFileErrorCode(err)}
	}
	return nil
}

func (d *localFileDispatcher) RemoveFile(_ context.Context, _, serverID, path string, recursive bool) error {
	filesys, err := d.fsFor(serverID)
	if err != nil {
		return &AgentFileError{Code: "INTERNAL_ERROR"}
	}
	if err := filesys.Delete(path, recursive); err != nil {
		return &AgentFileError{Code: localFileErrorCode(err)}
	}
	return nil
}

// DownloadBackup 读取节点本地备份目录中的归档，模拟 Agent 的回传行为。
func (d *localFileDispatcher) DownloadBackup(_ context.Context, _, _, backupID string) (domain.BackupContent, error) {
	archive := filepath.Join(d.fileRoot, "backups", backupID+".tar.gz")
	content, err := os.ReadFile(archive)
	if err != nil {
		if os.IsNotExist(err) {
			return domain.BackupContent{}, &AgentFileError{Code: "NOT_FOUND"}
		}
		return domain.BackupContent{}, &AgentFileError{Code: "INTERNAL_ERROR"}
	}
	return domain.BackupContent{
		Content:   []byte(base64.StdEncoding.EncodeToString(content)),
		Base64:    true,
		SizeBytes: int64(len(content)),
		Filename:  backupID + ".tar.gz",
	}, nil
}

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
		Capabilities: []string{domain.NodeCapabilityServerReconcile},
	})
	if err != nil {
		t.Fatalf("register node: %v", err)
	}

	game := insertCatalogGameFixture(t, s)
	runnableGame := game
	runnableGame.Runnable = true
	enableTestRuntimeTarget(t, runnableGame)
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
	if err := completeTaskWithCurrentFence(t, s, op.ID, nodeID, true, nil, nil); err != nil {
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
	if err := completeTaskWithCurrentFence(t, s, op.ID, nodeID, true, nil, nil); err != nil {
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
	if err := completeTaskWithCurrentFence(t, s, setOp.ID, nodeID, true, nil, nil); err != nil {
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
	if err := completeTaskWithCurrentFence(t, s, delOp.ID, nodeID, true, nil, nil); err != nil {
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
	if err := completeTaskWithCurrentFence(t, s, updOp.ID, nodeID, true, nil, nil); err != nil {
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

type postgresReconcileSnapshot struct {
	serverState     string
	allocations     string
	startupValues   string
	serverTaskCount int
	outboxCount     int
	auditCount      int
}

func capturePostgresReconcileSnapshot(t *testing.T, s *Postgres, serverID string) postgresReconcileSnapshot {
	t.Helper()
	var snapshot postgresReconcileSnapshot
	err := s.db.QueryRow(`
		SELECT to_jsonb(s)::text,
		       COALESCE((
		           SELECT jsonb_agg(
		               jsonb_build_object(
		                   'id', a.id::text,
		                   'bindIp', host(a.bind_ip),
		                   'port', a.port,
		                   'protocol', a.protocol,
		                   'primary', a.is_primary,
		                   'releasedAt', a.released_at
		               ) ORDER BY a.id
		           )::text
		           FROM allocations a WHERE a.server_id = s.id
		       ), '[]'),
		       COALESCE((SELECT values::text FROM startup_values WHERE server_id = s.id), 'null'),
		       (SELECT COUNT(*) FROM server_tasks),
		       (SELECT COUNT(*) FROM outbox_events),
		       (SELECT COUNT(*) FROM audit_events)
		FROM servers s
		WHERE s.id = $1
	`, serverID).Scan(
		&snapshot.serverState,
		&snapshot.allocations,
		&snapshot.startupValues,
		&snapshot.serverTaskCount,
		&snapshot.outboxCount,
		&snapshot.auditCount,
	)
	if err != nil {
		t.Fatalf("capture reconcile snapshot: %v", err)
	}
	return snapshot
}

func TestPostgresReconcileMutationsRejectMissingVersionedCapabilityWithoutSideEffects(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(*Postgres, domain.User, string, string, string, int64) error
	}{
		{
			name: "create allocation",
			mutate: func(s *Postgres, actor domain.User, serverID, _, _ string, generation int64) error {
				_, err := s.CreateAllocation(serverID, domain.CreateAllocationInput{
					BindIP: "127.0.0.2", Port: 30111, Protocol: "udp",
				}, generation, "idem-cap-create-0001", actor)
				return err
			},
		},
		{
			name: "set primary allocation",
			mutate: func(s *Postgres, actor domain.User, serverID, _, secondaryID string, generation int64) error {
				_, err := s.SetPrimaryAllocation(serverID, secondaryID, generation, "idem-cap-primary-0001", actor)
				return err
			},
		},
		{
			name: "delete allocation",
			mutate: func(s *Postgres, actor domain.User, serverID, _, secondaryID string, generation int64) error {
				_, err := s.DeleteAllocation(serverID, secondaryID, generation, "idem-cap-delete-0001", actor)
				return err
			},
		},
		{
			name: "update startup",
			mutate: func(s *Postgres, actor domain.User, serverID, _, _ string, generation int64) error {
				_, err := s.UpdateStartup(serverID, map[string]any{"memory_mb": 3072}, generation, "idem-cap-startup-0001", actor)
				return err
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			s := testPostgres(t)
			admin, nodeID, serverID := controlPlaneFixture(t, s)

			var primaryID string
			if err := s.db.QueryRow(`
				SELECT id::text FROM allocations
				WHERE server_id = $1 AND released_at IS NULL AND is_primary
			`, serverID).Scan(&primaryID); err != nil {
				t.Fatalf("query primary allocation: %v", err)
			}
			var secondaryID string
			if err := s.db.QueryRow(`
				INSERT INTO allocations (node_id, bind_ip, port, protocol, server_id, is_primary)
				VALUES ($1, '127.0.0.9', 30999, 'tcp', $2, false)
				RETURNING id::text
			`, nodeID, serverID).Scan(&secondaryID); err != nil {
				t.Fatalf("insert secondary allocation: %v", err)
			}

			// Neither a canonical declaration stored as the key nor a mismatched
			// version satisfies the split server.reconcile + version 1 contract.
			if _, err := s.db.Exec(`DELETE FROM node_capabilities WHERE node_id = $1 AND capability_key = 'server.reconcile'`, nodeID); err != nil {
				t.Fatalf("delete valid reconcile capability: %v", err)
			}
			if _, err := s.db.Exec(`
				INSERT INTO node_capabilities (node_id, capability_key, capability_version)
				VALUES ($1, 'server.reconcile', '2'), ($1, 'server.reconcile/v1', '1')
			`, nodeID); err != nil {
				t.Fatalf("insert invalid reconcile declarations: %v", err)
			}

			var generation int64
			if err := s.db.QueryRow(`SELECT generation FROM servers WHERE id = $1`, serverID).Scan(&generation); err != nil {
				t.Fatalf("query server generation: %v", err)
			}
			before := capturePostgresReconcileSnapshot(t, s, serverID)
			err := testCase.mutate(s, admin, serverID, primaryID, secondaryID, generation)
			problem := requireProblemCode(t, err, "CAPABILITY_UNSUPPORTED")
			if problem.Retryable || problem.Details["requiredCapability"] != "server.reconcile" ||
				problem.Details["requiredVersion"] != "1" || problem.Details["nodeId"] != nodeID ||
				problem.Details["nodeVersion"] != "agent-v1" {
				t.Fatalf("capability problem = %+v", problem)
			}
			after := capturePostgresReconcileSnapshot(t, s, serverID)
			if after != before {
				t.Fatalf("capability rejection mutated PostgreSQL state:\nbefore=%+v\nafter=%+v", before, after)
			}
		})
	}
}

func TestPostgresControlPlaneFiles(t *testing.T) {
	s := testPostgres(t)
	s.fileRoot = t.TempDir()
	s.SetFileDispatcher(&localFileDispatcher{fileRoot: s.fileRoot})
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
	// A backup that is still being created cannot be restored or deleted.
	if _, err := s.RestoreBackup(serverID, backupID, "idem-cp-restore-0001", admin); err == nil {
		t.Fatal("expected restoring a non-ready backup to fail")
	}
	if _, err := s.DeleteBackup(serverID, backupID, "idem-cp-del-backup-0001", admin); err == nil {
		t.Fatal("expected deleting a non-ready backup to fail")
	}

	// Simulate a valid Agent result so the state machine advances creating -> ready.
	if err := completeTaskWithCurrentFence(t, s, op.ID, nodeID, true, nil, postgresBackupResult(t, backupID, []byte("control-plane-backup"))); err != nil {
		t.Fatalf("complete backup task: %v", err)
	}
	backups, _ = s.Backups(serverID)
	if len(backups) != 1 || backups[0].Status != "ready" {
		t.Fatalf("backups after completion = %+v, want one ready", backups)
	}

	// Restore the ready recovery point from a stopped server.
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

func TestCreateBackupWritesTaskInputPayload(t *testing.T) {
	s := testPostgres(t)
	admin, _, serverID := controlPlaneFixture(t, s)

	op, err := s.CreateBackup(serverID, "idem-cp-checkpoint-0001", admin)
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	// 000009 起备份任务输入写入 task_input，checkpoint 在 claim 前必须为空。
	var taskInput, checkpoint string
	if err := s.db.QueryRow(`
		SELECT COALESCE(task_input::text, ''), COALESCE(checkpoint::text, '')
		FROM server_tasks WHERE id = $1`, op.ID).Scan(&taskInput, &checkpoint); err != nil {
		t.Fatalf("query task input: %v", err)
	}
	if taskInput == "" {
		t.Fatal("expected non-empty task_input for backup task")
	}
	if checkpoint != "" {
		t.Fatalf("backup task must not carry payload in checkpoint: %q", checkpoint)
	}
	var payload struct {
		BackupID         string `json:"backupId"`
		StorageObjectKey string `json:"storageObjectKey"`
	}
	if err := json.Unmarshal([]byte(taskInput), &payload); err != nil {
		t.Fatalf("decode task input: %v", err)
	}
	if payload.BackupID == "" {
		t.Error("expected backupId in task input")
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
	result := []byte(`{"backupId":"` + backupID + `","checksum":"sha256:` + strings.Repeat("ab", 32) + `","manifestDigest":"sha256:` + strings.Repeat("cd", 32) + `","sizeBytes":42,"storageLocation":"backups/` + backupID + `.tar.gz"}`)
	if err := completeTaskWithCurrentFence(t, s, op.ID, nodeID, true, nil, result); err != nil {
		t.Fatalf("complete backup task: %v", err)
	}

	var status, checksum, manifestDigest, storageLocation string
	var sizeBytes int64
	if err := s.db.QueryRow(`
		SELECT status, COALESCE(content_digest, ''), COALESCE(manifest_digest, ''), COALESCE(storage_location, ''), COALESCE(size_bytes, 0)
		FROM backups WHERE id = $1`, backupID).Scan(&status, &checksum, &manifestDigest, &storageLocation, &sizeBytes); err != nil {
		t.Fatalf("query backup state: %v", err)
	}
	if status != "ready" {
		t.Errorf("backup status = %q, want ready", status)
	}
	if checksum != "sha256:"+strings.Repeat("ab", 32) {
		t.Errorf("backup checksum = %q", checksum)
	}
	if manifestDigest != "sha256:"+strings.Repeat("cd", 32) {
		t.Errorf("backup manifest digest = %q", manifestDigest)
	}
	if sizeBytes != 42 {
		t.Errorf("backup size = %d, want 42", sizeBytes)
	}
	if want := "backups/" + backupID + ".tar.gz"; storageLocation != want {
		t.Errorf("storage location = %q, want %q", storageLocation, want)
	}
}

func TestInvalidBackupResultPersistsIntegrityFailureMetadata(t *testing.T) {
	s := testPostgres(t)
	admin, nodeID, serverID := controlPlaneFixture(t, s)

	op, err := s.CreateBackup(serverID, "idem-cp-invalid-result-0001", admin)
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	backups, err := s.Backups(serverID)
	if err != nil || len(backups) != 1 {
		t.Fatalf("creating backup metadata = %+v, err=%v", backups, err)
	}
	backupID := backups[0].ID
	malformed := []byte(`{"backupId":"` + backupID + `","checksum":"sha256:` + strings.Repeat("ab", 32) + `","sizeBytes":42,"storageLocation":"backups/` + backupID + `.tar.gz"}`)
	if err := completeTaskWithCurrentFence(t, s, op.ID, nodeID, true, nil, malformed); err != nil {
		t.Fatalf("complete malformed backup task: %v", err)
	}
	backups, err = s.Backups(serverID)
	if err != nil || len(backups) != 1 {
		t.Fatalf("failed backup metadata = %+v, err=%v", backups, err)
	}
	backup := backups[0]
	if backup.Status != "failed" || backup.FailureCode == nil || *backup.FailureCode != "BACKUP_INTEGRITY_FAILED" || backup.FailureMessage == nil {
		t.Fatalf("integrity failure metadata = %+v", backup)
	}
}

func TestCompleteBackupTaskRejectsUnexpectedBackupState(t *testing.T) {
	s := testPostgres(t)
	admin, nodeID, serverID := controlPlaneFixture(t, s)

	op, err := s.CreateBackup(serverID, "idem-cp-state-conflict-0001", admin)
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	backups, err := s.Backups(serverID)
	if err != nil || len(backups) != 1 {
		t.Fatalf("creating backup = %+v, err=%v", backups, err)
	}
	backupID := backups[0].ID
	if _, err := s.db.Exec(`
		UPDATE backups
		SET status = 'failed', failure_code = 'BACKUP_FAILED', failure_message = 'injected state conflict'
		WHERE id = $1
	`, backupID); err != nil {
		t.Fatalf("inject backup state conflict: %v", err)
	}

	// 新状态机先 claim 再完成：状态冲突返回结构化错误并整体回滚，
	// 任务保持 claim 后的 leased（租约过期后由对账器重新入队）。
	claimed, err := s.ClaimTask(context.Background(), nodeID, 1)
	if err != nil || claimed == nil {
		t.Fatalf("claim backup task: %v", err)
	}
	fence := TaskLeaseFence{
		OperationID: claimed.OperationID, NodeID: nodeID,
		Epoch: claimed.ConnectionEpoch, Attempt: claimed.Attempt,
		LeaseToken: claimed.LeaseToken,
	}
	err = s.CompleteTask(context.Background(), fence, true, nil, postgresBackupResult(t, backupID, []byte("archive")))
	requireProblemCode(t, err, "BACKUP_STATE_CONFLICT")
	var backupStatus, taskStatus string
	if err := s.db.QueryRow(`SELECT status FROM backups WHERE id = $1`, backupID).Scan(&backupStatus); err != nil {
		t.Fatalf("query backup status: %v", err)
	}
	if err := s.db.QueryRow(`SELECT status FROM server_tasks WHERE id = $1`, op.ID).Scan(&taskStatus); err != nil {
		t.Fatalf("query task status: %v", err)
	}
	if backupStatus != "failed" || taskStatus != "leased" {
		t.Fatalf("state conflict committed partial writes: backup=%q task=%q", backupStatus, taskStatus)
	}
}

func TestPostgresRestoreCompletionConvergesObservedState(t *testing.T) {
	s := testPostgres(t)
	admin, nodeID, serverID := controlPlaneFixture(t, s)
	backupID := completePostgresBackup(t, s, admin, nodeID, serverID, "idem-cp-converge-backup-0001", []byte("restore-point"))

	if _, err := s.db.Exec(`
		UPDATE servers
		SET observed_power = 'stopped', health_condition = 'healthy', observed_generation = 0
		WHERE id = $1
	`, serverID); err != nil {
		t.Fatalf("prepare stopped server: %v", err)
	}
	restore, err := s.RestoreBackup(serverID, backupID, "idem-cp-converge-restore-0001", admin)
	if err != nil {
		t.Fatalf("restore backup: %v", err)
	}
	if err := completeTaskWithCurrentFence(t, s, restore.ID, nodeID, true, nil, nil); err != nil {
		t.Fatalf("complete restore: %v", err)
	}

	var generation, observedGeneration int64
	var desiredPower, observedPower, health string
	if err := s.db.QueryRow(`
		SELECT generation, observed_generation, desired_power, observed_power, health_condition
		FROM servers WHERE id = $1
	`, serverID).Scan(&generation, &observedGeneration, &desiredPower, &observedPower, &health); err != nil {
		t.Fatalf("query converged server: %v", err)
	}
	if observedGeneration != generation || desiredPower != "stopped" || observedPower != "stopped" || health != "unknown" {
		t.Fatalf("restore convergence = generation %d/%d power %s/%s health %s", observedGeneration, generation, desiredPower, observedPower, health)
	}
}

func TestPostgresFailedBackupCanBeDeleted(t *testing.T) {
	s := testPostgres(t)
	admin, nodeID, serverID := controlPlaneFixture(t, s)
	op, err := s.CreateBackup(serverID, "idem-cp-failed-delete-0001", admin)
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	backups, _ := s.Backups(serverID)
	backupID := backups[0].ID
	failureCode := "BACKUP_FAILED"
	if err := completeTaskWithCurrentFence(t, s, op.ID, nodeID, false, &failureCode, nil); err != nil {
		t.Fatalf("fail backup: %v", err)
	}

	deleteOperation, err := s.DeleteBackup(serverID, backupID, "idem-cp-failed-delete-0002", admin)
	if err != nil {
		t.Fatalf("delete failed backup: %v", err)
	}
	if err := completeTaskWithCurrentFence(t, s, deleteOperation.ID, nodeID, true, nil, nil); err != nil {
		t.Fatalf("complete failed-backup deletion: %v", err)
	}
	backups, err = s.Backups(serverID)
	if err != nil || len(backups) != 0 {
		t.Fatalf("backups after deletion = %+v, err=%v", backups, err)
	}
}

func TestPostgresFailedBackupDeleteCompensationReturnsToFailed(t *testing.T) {
	s := testPostgres(t)
	admin, nodeID, serverID := controlPlaneFixture(t, s)
	op, err := s.CreateBackup(serverID, "idem-cp-failed-delete-comp-0001", admin)
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	backups, _ := s.Backups(serverID)
	backupID := backups[0].ID
	failureCode := "BACKUP_FAILED"
	if err := completeTaskWithCurrentFence(t, s, op.ID, nodeID, false, &failureCode, nil); err != nil {
		t.Fatalf("fail backup: %v", err)
	}
	deleteOperation, err := s.DeleteBackup(serverID, backupID, "idem-cp-failed-delete-comp-0002", admin)
	if err != nil {
		t.Fatalf("delete failed backup: %v", err)
	}
	deleteFailure := "RUNTIME_UNAVAILABLE"
	if err := completeTaskWithCurrentFence(t, s, deleteOperation.ID, nodeID, false, &deleteFailure, nil); err != nil {
		t.Fatalf("compensate failed deletion: %v", err)
	}
	backups, err = s.Backups(serverID)
	if err != nil || len(backups) != 1 || backups[0].Status != "failed" {
		t.Fatalf("compensated backup = %+v, err=%v", backups, err)
	}
	if backups[0].FailureCode == nil || *backups[0].FailureCode != "RUNTIME_UNAVAILABLE" {
		t.Fatalf("compensated failure metadata = %+v", backups[0])
	}
}

func TestPostgresDownloadBackupVerifiesChecksumAndSize(t *testing.T) {
	s := testPostgres(t)
	admin, nodeID, serverID := controlPlaneFixture(t, s)
	raw := []byte("verified-backup")
	backupID := completePostgresBackup(t, s, admin, nodeID, serverID, "idem-cp-download-verify-0001", raw)
	root := t.TempDir()
	s.SetFileDispatcher(&localFileDispatcher{fileRoot: root})
	archiveDir := filepath.Join(root, "backups")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		t.Fatalf("create backup directory: %v", err)
	}
	archive := filepath.Join(archiveDir, backupID+".tar.gz")
	if err := os.WriteFile(archive, raw, 0o600); err != nil {
		t.Fatalf("write verified archive: %v", err)
	}
	content, err := s.DownloadBackup(serverID, backupID, admin)
	if err != nil || content.SizeBytes != int64(len(raw)) {
		t.Fatalf("download verified backup = %+v, err=%v", content, err)
	}

	if err := os.WriteFile(archive, []byte("tampered-backup"), 0o600); err != nil {
		t.Fatalf("tamper archive: %v", err)
	}
	_, err = s.DownloadBackup(serverID, backupID, admin)
	requireProblemCode(t, err, "BACKUP_INTEGRITY_FAILED")

	larger := []byte("larger-tampered-backup")
	if err := os.WriteFile(archive, larger, 0o600); err != nil {
		t.Fatalf("write size-mismatched archive: %v", err)
	}
	digest := sha256.Sum256(larger)
	if _, err := s.db.Exec(`UPDATE backups SET content_digest = $2 WHERE id = $1`, backupID, "sha256:"+hex.EncodeToString(digest[:])); err != nil {
		t.Fatalf("prepare size mismatch: %v", err)
	}
	_, err = s.DownloadBackup(serverID, backupID, admin)
	requireProblemCode(t, err, "BACKUP_INTEGRITY_FAILED")
}

func completePostgresBackup(t *testing.T, s *Postgres, admin domain.User, nodeID, serverID, idempotencyKey string, content []byte) string {
	t.Helper()
	op, err := s.CreateBackup(serverID, idempotencyKey, admin)
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	backups, err := s.Backups(serverID)
	if err != nil || len(backups) == 0 {
		t.Fatalf("creating backup = %+v, err=%v", backups, err)
	}
	backupID := backups[0].ID
	if err := completeTaskWithCurrentFence(t, s, op.ID, nodeID, true, nil, postgresBackupResult(t, backupID, content)); err != nil {
		t.Fatalf("complete backup: %v", err)
	}
	return backupID
}

func postgresBackupResult(t *testing.T, backupID string, content []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(content)
	result, err := json.Marshal(map[string]any{
		"backupId":        backupID,
		"checksum":        "sha256:" + hex.EncodeToString(digest[:]),
		"manifestDigest":  "sha256:" + strings.Repeat("cd", 32),
		"sizeBytes":       len(content),
		"storageLocation": "backups/" + backupID + ".tar.gz",
	})
	if err != nil {
		t.Fatalf("marshal backup result: %v", err)
	}
	return result
}
