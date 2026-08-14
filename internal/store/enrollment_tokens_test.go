package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
)

const enrollmentTestAdminID = "00000000-0000-4000-8000-000000000001"

func TestMemoryEnrollmentTokenIsConsumableOnlyOnce(t *testing.T) {
	service := NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer service.Close()

	actor := domain.User{ID: enrollmentTestAdminID, Roles: []string{"platform_admin"}}
	token, expiresAt, err := service.IssueAgentEnrollmentToken(" node-a ", 2*time.Minute, actor)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if len(token) != 64 || expiresAt.Before(time.Now().UTC()) {
		t.Fatalf("issued token metadata = %q / %v", token, expiresAt)
	}
	if err := service.ConsumeEnrollmentToken(context.Background(), token); err != nil {
		t.Fatalf("consume issued token: %v", err)
	}
	if err := service.ConsumeEnrollmentToken(context.Background(), token); err == nil {
		t.Fatal("replayed token was accepted")
	}
}

func TestMemoryEnrollmentTokenValidation(t *testing.T) {
	service := NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer service.Close()
	actor := domain.User{ID: enrollmentTestAdminID, Roles: []string{"platform_admin"}}

	tests := []struct {
		name string
		hint string
		ttl  time.Duration
	}{
		{name: "negative ttl", ttl: -time.Second},
		{name: "long hint", hint: strings.Repeat("n", enrollmentTokenMaxHintRunes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := service.IssueAgentEnrollmentToken(test.hint, test.ttl, actor); err == nil {
				t.Fatal("invalid enrollment token request was accepted")
			} else if problem, ok := err.(*domain.Problem); !ok || problem.Code != "VALIDATION_FAILED" {
				t.Fatalf("error = %T %v, want VALIDATION_FAILED", err, err)
			}
		})
	}
}

func TestMemoryRevokedNodeIsHiddenAndHeartbeatRejected(t *testing.T) {
	service := NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer service.Close()
	actor := domain.User{ID: enrollmentTestAdminID, Roles: []string{"platform_admin"}}

	service.mu.Lock()
	node := service.nodes[availableNodeID]
	service.mu.Unlock()
	if node.ID == "" {
		t.Fatal("test fixture node is missing")
	}
	if err := service.RevokeNode(node.ID, actor); err != nil {
		t.Fatalf("revoke node: %v", err)
	}
	for _, visible := range service.Nodes() {
		if visible.ID == node.ID {
			t.Fatalf("revoked node remained visible: %+v", visible)
		}
	}
	if err := service.Heartbeat(node.Name, "test-agent"); err == nil {
		t.Fatal("revoked node heartbeat was accepted")
	}
}
