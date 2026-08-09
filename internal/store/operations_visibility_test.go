package store

import (
	"testing"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
)

func TestVisibleOperationsRespectsCurrentServerReadGrants(t *testing.T) {
	service := newTestMemory(time.Hour)
	defer func() { _ = service.Close() }()
	admin := testActor("admin-1", "GuGu Admin")
	member, err := service.CreateUser(domain.CreateUserInput{
		Email:       "operations-member@example.test",
		DisplayName: "Operations Member",
		Password:    "member secure password",
		Roles:       []string{"server_owner"},
	}, admin)
	if err != nil {
		t.Fatalf("CreateUser returned error: %v", err)
	}
	if _, err := service.PutServerMembership(stoppedServerID, member.ID, []string{"servers.read"}, admin); err != nil {
		t.Fatalf("PutServerMembership returned error: %v", err)
	}

	now := time.Now().UTC()
	service.mu.Lock()
	stoppedServer := service.servers[stoppedServerID]
	runningServer := service.servers[runningServerID]
	allowed := domain.NewQueuedOperation("operation-visible", stoppedServerID, stoppedServer.NodeID, domain.PowerStart, stoppedServer.Generation, "visible-operation-key", now)
	hidden := domain.NewQueuedOperation("operation-hidden", runningServerID, runningServer.NodeID, domain.PowerStop, runningServer.Generation, "hidden-operation-key", now.Add(time.Second))
	allowed.UpdatedAt = now.Add(2 * time.Second)
	service.operations[allowed.ID] = allowed
	service.operations[hidden.ID] = hidden
	service.mu.Unlock()

	adminVisible := service.VisibleOperations(admin.ID)
	if len(adminVisible) != 2 || adminVisible[0].ID != hidden.ID || adminVisible[1].ID != allowed.ID {
		t.Fatalf("admin operation order = %+v, want immutable createdAt descending", adminVisible)
	}

	visible := service.VisibleOperations(member.ID)
	if len(visible) != 1 || visible[0].ID != allowed.ID {
		t.Fatalf("visible operations = %+v, want only %s", visible, allowed.ID)
	}

	if err := service.DeleteServerMembership(stoppedServerID, member.ID, admin); err != nil {
		t.Fatalf("DeleteServerMembership returned error: %v", err)
	}
	if visible := service.VisibleOperations(member.ID); len(visible) != 0 {
		t.Fatalf("visible operations after revocation = %+v, want none", visible)
	}
}
