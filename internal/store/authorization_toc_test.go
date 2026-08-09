package store

import (
	"maps"
	"reflect"
	"testing"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
)

func currentAdmin(t *testing.T, service *Memory) domain.User {
	t.Helper()
	for _, user := range service.Users() {
		if hasRole(user, "platform_admin") && user.Status == "active" {
			return user
		}
	}
	t.Fatal("active platform administrator not found")
	return domain.User{}
}

func makeMember(t *testing.T, service *Memory, serverID string, permissions []string) (admin domain.User, member domain.User) {
	t.Helper()
	admin = currentAdmin(t, service)
	created, err := service.CreateUser(domain.CreateUserInput{
		Email:       "toc-tou-member@example.test",
		DisplayName: "TOCTOU Member",
		Password:    "member secure password",
		Roles:       []string{"server_owner"},
	}, admin)
	if err != nil {
		t.Fatalf("CreateUser member failed: %v", err)
	}
	if _, err := service.PutServerMembership(serverID, created.ID, permissions, admin); err != nil {
		t.Fatalf("PutServerMembership failed: %v", err)
	}
	return admin, created
}

func assertNoStoreMutation(t *testing.T, service *Memory, beforeOperations int, beforeGeneration int64, beforeAudit int, serverID string) {
	t.Helper()
	service.mu.RLock()
	defer service.mu.RUnlock()
	if got := len(service.operations); got != beforeOperations {
		t.Fatalf("operations changed after stale actor rejection: before=%d after=%d", beforeOperations, got)
	}
	if serverID != "" {
		if got := service.servers[serverID].Generation; got != beforeGeneration {
			t.Fatalf("server generation changed after stale actor rejection: before=%d after=%d", beforeGeneration, got)
		}
	}
	if got := len(service.audit); got != beforeAudit {
		t.Fatalf("audit changed after stale actor rejection: before=%d after=%d", beforeAudit, got)
	}
}

func TestCreateServerRejectsDemotedPlatformAdminBeforeIdempotencyReplay(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	defer func() { _ = service.Close() }()
	admin := currentAdmin(t, service)
	second, err := service.CreateUser(domain.CreateUserInput{
		Email:       "toc-tou-second-admin@example.test",
		DisplayName: "Second Admin",
		Password:    "second admin secure password",
		Roles:       []string{"platform_admin"},
	}, admin)
	if err != nil {
		t.Fatalf("CreateUser second admin failed: %v", err)
	}
	input := validCreateServerInput()
	key := "toc-tou-create-replay-01"
	created, err := service.CreateServer(input, key, admin)
	if err != nil {
		t.Fatalf("initial CreateServer failed: %v", err)
	}
	service.mu.RLock()
	operationsBefore := len(service.operations)
	auditBefore := len(service.audit)
	service.mu.RUnlock()
	roles := []string{"server_owner"}
	if _, err := service.UpdateUser(admin.ID, domain.UpdateUserInput{Roles: &roles}, second); err != nil {
		t.Fatalf("demoting original admin failed: %v", err)
	}
	if _, err := service.CreateServer(input, key, admin); err == nil {
		t.Fatal("demoted platform administrator replay unexpectedly succeeded")
	} else {
		requireProblemCode(t, err, "FORBIDDEN")
	}
	service.mu.RLock()
	defer service.mu.RUnlock()
	if len(service.operations) != operationsBefore {
		t.Fatalf("idempotent replay created a new operation: before=%d after=%d", operationsBefore, len(service.operations))
	}
	if len(service.audit) != auditBefore+1 {
		t.Fatalf("demotion audit count = %d, want %d", len(service.audit), auditBefore+1)
	}
	if _, ok := service.operations[created.ID]; !ok {
		t.Fatalf("initial operation %q disappeared", created.ID)
	}
}

func TestServerWritesRejectRevokedMembershipBeforeAnyMutation(t *testing.T) {
	tests := []struct {
		name       string
		permission string
		invoke     func(*Memory, domain.User, int64) error
		serverID   string
	}{
		{
			name:       "power",
			permission: "servers.power",
			serverID:   stoppedServerID,
			invoke: func(service *Memory, actor domain.User, _ int64) error {
				_, err := service.RequestPower(stoppedServerID, domain.PowerStart, "toc-tou-power-00001", actor)
				return err
			},
		},
		{
			name:       "console",
			permission: "servers.console",
			serverID:   "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			invoke: func(service *Memory, actor domain.User, _ int64) error {
				return service.SendConsoleCommand("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "list", actor)
			},
		},
		{
			name:       "allocation-create",
			permission: "servers.network.write",
			serverID:   stoppedServerID,
			invoke: func(service *Memory, actor domain.User, generation int64) error {
				_, err := service.CreateAllocation(stoppedServerID, domain.CreateAllocationInput{BindIP: "10.0.20.14", Port: 35001, Protocol: "udp"}, generation, "toc-tou-allocation-create-1", actor)
				return err
			},
		},
		{
			name:       "allocation-primary",
			permission: "servers.network.write",
			serverID:   stoppedServerID,
			invoke: func(service *Memory, actor domain.User, generation int64) error {
				returnOperation, err := service.SetPrimaryAllocation(stoppedServerID, "a2222222-2222-4222-8222-222222222222", generation, "toc-tou-allocation-primary-1", actor)
				_ = returnOperation
				return err
			},
		},
		{
			name:       "allocation-delete",
			permission: "servers.network.write",
			serverID:   stoppedServerID,
			invoke: func(service *Memory, actor domain.User, generation int64) error {
				_, err := service.DeleteAllocation(stoppedServerID, "a2222222-2222-4222-8222-222222222222", generation, "toc-tou-allocation-delete-1", actor)
				return err
			},
		},
		{
			name:       "startup",
			permission: "servers.startup.write",
			serverID:   stoppedServerID,
			invoke: func(service *Memory, actor domain.User, generation int64) error {
				_, err := service.UpdateStartup(stoppedServerID, map[string]any{"server_name": "TOCTOU"}, generation, "toc-tou-startup-000001", actor)
				return err
			},
		},
		{
			name:       "backup-create",
			permission: "servers.backups.create",
			serverID:   stoppedServerID,
			invoke: func(service *Memory, actor domain.User, _ int64) error {
				_, err := service.CreateBackup(stoppedServerID, "toc-tou-backup-create-01", actor)
				return err
			},
		},
		{
			name:       "backup-restore",
			permission: "servers.backups.restore",
			serverID:   stoppedServerID,
			invoke: func(service *Memory, actor domain.User, _ int64) error {
				_, err := service.RestoreBackup(stoppedServerID, "d3333333-3333-4333-8333-333333333333", "toc-tou-backup-restore-01", actor)
				return err
			},
		},
		{
			name:       "backup-delete",
			permission: "servers.backups.delete",
			serverID:   stoppedServerID,
			invoke: func(service *Memory, actor domain.User, _ int64) error {
				_, err := service.DeleteBackup(stoppedServerID, "d3333333-3333-4333-8333-333333333333", "toc-tou-backup-delete-01", actor)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newTestMemory(time.Millisecond)
			defer func() { _ = service.Close() }()
			admin, member := makeMember(t, service, test.serverID, []string{"servers.read", test.permission})
			server, err := service.Server(test.serverID)
			if err != nil {
				t.Fatal(err)
			}
			service.mu.RLock()
			operationsBefore := len(service.operations)
			auditBefore := len(service.audit)
			allocationsBefore := maps.Clone(service.allocations)
			startupsBefore := maps.Clone(service.startupValues)
			consoleBefore := append([]domain.ConsoleLine(nil), service.console[test.serverID]...)
			service.mu.RUnlock()
			if err := service.DeleteServerMembership(test.serverID, member.ID, admin); err != nil {
				t.Fatalf("revoke membership failed: %v", err)
			}
			if err := test.invoke(service, member, server.Generation); err == nil {
				t.Fatal("revoked member write unexpectedly succeeded")
			} else {
				requireProblemCode(t, err, "NOT_FOUND")
			}
			assertNoStoreMutation(t, service, operationsBefore, server.Generation, auditBefore+1, test.serverID)
			service.mu.RLock()
			defer service.mu.RUnlock()
			if !maps.Equal(service.allocations, allocationsBefore) {
				t.Fatalf("allocations changed after revoked %s write", test.name)
			}
			if !reflect.DeepEqual(service.startupValues, startupsBefore) {
				t.Fatalf("startup values changed after revoked %s write", test.name)
			}
			if !equalConsole(service.console[test.serverID], consoleBefore) {
				t.Fatalf("console changed after revoked %s write", test.name)
			}
		})
	}
}

func TestIdempotentServerMutationReplayRechecksRevokedMembership(t *testing.T) {
	tests := []struct {
		name       string
		permission string
		invoke     func(*Memory, domain.User, int64) (domain.Operation, error)
	}{
		{
			name:       "power",
			permission: "servers.power",
			invoke: func(service *Memory, actor domain.User, _ int64) (domain.Operation, error) {
				return service.RequestPower(stoppedServerID, domain.PowerStart, "toc-tou-power-replay-001", actor)
			},
		},
		{
			name:       "startup",
			permission: "servers.startup.write",
			invoke: func(service *Memory, actor domain.User, generation int64) (domain.Operation, error) {
				return service.UpdateStartup(stoppedServerID, map[string]any{"server_name": "Replay check"}, generation, "toc-tou-startup-replay-1", actor)
			},
		},
		{
			name:       "backup-create",
			permission: "servers.backups.create",
			invoke: func(service *Memory, actor domain.User, _ int64) (domain.Operation, error) {
				return service.CreateBackup(stoppedServerID, "toc-tou-backup-replay-01", actor)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newTestMemory(time.Second)
			defer func() { _ = service.Close() }()
			admin, member := makeMember(t, service, stoppedServerID, []string{"servers.read", test.permission})
			server, err := service.Server(stoppedServerID)
			if err != nil {
				t.Fatal(err)
			}
			first, err := test.invoke(service, member, server.Generation)
			if err != nil {
				t.Fatalf("initial mutation failed: %v", err)
			}
			service.mu.RLock()
			operationsBefore := len(service.operations)
			service.mu.RUnlock()
			if err := service.DeleteServerMembership(stoppedServerID, member.ID, admin); err != nil {
				t.Fatalf("revoke membership failed: %v", err)
			}
			replayed, err := test.invoke(service, member, server.Generation)
			requireProblemCode(t, err, "NOT_FOUND")
			if replayed.ID != "" {
				t.Fatalf("revoked replay returned operation %+v instead of rejecting", replayed)
			}
			service.mu.RLock()
			defer service.mu.RUnlock()
			if len(service.operations) != operationsBefore {
				t.Fatalf("revoked replay changed operation count: before=%d after=%d", operationsBefore, len(service.operations))
			}
			if _, found := service.operations[first.ID]; !found {
				t.Fatalf("initial operation %q disappeared", first.ID)
			}
		})
	}
}

func equalConsole(left, right []domain.ConsoleLine) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestDisablingAndReenablingUserRevokesOutstandingPasswordResetToken(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	defer func() { _ = service.Close() }()
	admin := currentAdmin(t, service)
	user, err := service.CreateUser(domain.CreateUserInput{
		Email:       "toc-tou-reset@example.test",
		DisplayName: "Reset Target",
		Password:    "initial secure password",
		Roles:       []string{"server_owner"},
	}, admin)
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	issued, err := service.IssuePasswordResetToken(user.ID, admin)
	if err != nil {
		t.Fatalf("IssuePasswordResetToken failed: %v", err)
	}
	status := "disabled"
	if _, err := service.UpdateUser(user.ID, domain.UpdateUserInput{Status: &status}, admin); err != nil {
		t.Fatalf("disable user failed: %v", err)
	}
	status = "active"
	if _, err := service.UpdateUser(user.ID, domain.UpdateUserInput{Status: &status}, admin); err != nil {
		t.Fatalf("re-enable user failed: %v", err)
	}
	if err := service.ResetPassword(issued.Token, "new secure password"); err == nil {
		t.Fatal("reset token issued before disable remained usable after re-enable")
	} else {
		requireProblemCode(t, err, "AUTH_INVALID_RESET_TOKEN")
	}
}

func TestFileMutationHoldsStoreLockAgainstConcurrentUserRevocation(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	defer func() { _ = service.Close() }()
	admin, member := makeMember(t, service, stoppedServerID, []string{"servers.read", "servers.files.write"})
	started := make(chan struct{})
	release := make(chan struct{})
	service.fileMutationHook = func() {
		close(started)
		<-release
	}
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- service.WriteFile(stoppedServerID, "lock-serialization.txt", []byte("must finish before revoke"), member)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("file mutation did not reach the physical-write barrier")
	}
	status := "disabled"
	revokeDone := make(chan error, 1)
	go func() {
		_, err := service.UpdateUser(member.ID, domain.UpdateUserInput{Status: &status}, admin)
		revokeDone <- err
	}()
	select {
	case err := <-revokeDone:
		t.Fatalf("user revocation completed while physical file mutation was held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-writeDone; err != nil {
		t.Fatalf("file mutation failed: %v", err)
	}
	if err := <-revokeDone; err != nil {
		t.Fatalf("user revocation failed after file mutation: %v", err)
	}
	service.fileMutationHook = nil
	content, err := service.ReadFile(stoppedServerID, "lock-serialization.txt")
	if err != nil {
		t.Fatalf("serialized file write disappeared: %v", err)
	}
	if content.Content != "must finish before revoke" {
		t.Fatalf("serialized file content = %q", content.Content)
	}
}

func TestFileWritesRejectRevokedMembershipWithoutFilesystemSideEffects(t *testing.T) {
	tests := []struct {
		name   string
		invoke func(*Memory, domain.User) error
		verify func(*testing.T, *Memory)
	}{
		{
			name: "write file",
			invoke: func(service *Memory, actor domain.User) error {
				return service.WriteFile(stoppedServerID, "revoked-write.txt", []byte("must not be written"), actor)
			},
			verify: func(t *testing.T, service *Memory) {
				t.Helper()
				_, err := service.ReadFile(stoppedServerID, "revoked-write.txt")
				requireProblemCode(t, err, "NOT_FOUND")
			},
		},
		{
			name: "create directory",
			invoke: func(service *Memory, actor domain.User) error {
				return service.CreateDirectory(stoppedServerID, "revoked-directory", actor)
			},
			verify: func(t *testing.T, service *Memory) {
				t.Helper()
				_, err := service.ReadFile(stoppedServerID, "revoked-directory")
				requireProblemCode(t, err, "NOT_FOUND")
			},
		},
		{
			name: "move file",
			invoke: func(service *Memory, actor domain.User) error {
				return service.MoveFile(stoppedServerID, "server-settings.json", "revoked-settings.json", false, actor)
			},
			verify: func(t *testing.T, service *Memory) {
				t.Helper()
				if _, err := service.ReadFile(stoppedServerID, "server-settings.json"); err != nil {
					t.Fatalf("source file moved after authorization was revoked: %v", err)
				}
				_, err := service.ReadFile(stoppedServerID, "revoked-settings.json")
				requireProblemCode(t, err, "NOT_FOUND")
			},
		},
		{
			name: "delete file",
			invoke: func(service *Memory, actor domain.User) error {
				return service.DeleteFile(stoppedServerID, "server-settings.json", false, actor)
			},
			verify: func(t *testing.T, service *Memory) {
				t.Helper()
				if _, err := service.ReadFile(stoppedServerID, "server-settings.json"); err != nil {
					t.Fatalf("source file was deleted after authorization was revoked: %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newTestMemory(time.Millisecond)
			defer func() { _ = service.Close() }()
			admin, member := makeMember(t, service, stoppedServerID, []string{"servers.read", "servers.files.write"})
			server, err := service.Server(stoppedServerID)
			if err != nil {
				t.Fatal(err)
			}
			service.mu.RLock()
			operationsBefore := len(service.operations)
			auditBefore := len(service.audit)
			service.mu.RUnlock()

			if err := service.DeleteServerMembership(stoppedServerID, member.ID, admin); err != nil {
				t.Fatalf("revoke membership failed: %v", err)
			}
			if err := test.invoke(service, member); err == nil {
				t.Fatal("revoked member file mutation unexpectedly succeeded")
			} else {
				requireProblemCode(t, err, "NOT_FOUND")
			}
			assertNoStoreMutation(t, service, operationsBefore, server.Generation, auditBefore+1, stoppedServerID)
			test.verify(t, service)
		})
	}
}
