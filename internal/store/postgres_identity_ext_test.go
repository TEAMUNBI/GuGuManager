package store

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/gugumanager/gugumanager/internal/config"
	"github.com/gugumanager/gugumanager/internal/domain"
)

func testPostgres(t *testing.T) *Postgres {
	t.Helper()
	dsn := os.Getenv("GUGU_TEST_DATABASE_URL")
	if dsn == "" || !strings.HasSuffix(dsn, "_test") {
		t.Skip("GUGU_TEST_DATABASE_URL required, must end in _test")
	}
	// lib/pq defaults to sslmode=require while the local test instance has no
	// SSL configured; fall back to plaintext unless the DSN already pins a mode.
	parsed, err := url.Parse(dsn)
	if err == nil {
		query := parsed.Query()
		if query.Get("sslmode") == "" {
			query.Set("sslmode", "disable")
			parsed.RawQuery = query.Encode()
			dsn = parsed.String()
		}
	}
	s, err := NewPostgres(context.Background(), dsn, config.Production, "test-agent-token-1234567890", "")
	if err != nil {
		t.Fatalf("new postgres: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// resetTestDatabase clears persisted rows so the shared gugu_identity_test
// database can be reused across runs. The roles seed rows are preserved.
func resetTestDatabase(t *testing.T, s *Postgres) {
	t.Helper()
	if _, err := s.db.Exec(`
		TRUNCATE server_members, servers, nodes, game_bundles, game_definitions,
		         allocations, server_tasks, backups, startup_values, sessions,
		         password_reset_tokens, audit_events, user_roles, users CASCADE`); err != nil {
		t.Fatalf("reset test database: %v", err)
	}
}

// createServerFixture inserts the smallest valid servers row. server_members
// has a foreign key to servers(id), which in turn requires node, game
// definition and game bundle rows.
func createServerFixture(t *testing.T, s *Postgres, ownerID string) string {
	t.Helper()
	var nodeID string
	if err := s.db.QueryRow(`
		INSERT INTO nodes (name, agent_version, protocol_version, condition, region, cpu_cores, memory_bytes, disk_bytes)
		VALUES ('membership-test-node', 'test-agent', 'v1', 'available', 'test', 1, 1073741824, 1073741824)
		RETURNING id::text`).Scan(&nodeID); err != nil {
		t.Fatalf("insert membership test node: %v", err)
	}
	if _, err := s.db.Exec(`
		INSERT INTO game_definitions (id, name, source_url, review_status)
		VALUES ('membership-test-game', 'Membership Test Game', 'https://example.invalid/game.json', 'approved')`); err != nil {
		t.Fatalf("insert membership test game definition: %v", err)
	}
	var bundleID string
	if err := s.db.QueryRow(`
		INSERT INTO game_bundles (game_definition_id, definition_version, game_version, digest, schema_version, license, compatibility, published_at)
		VALUES ('membership-test-game', '1.0.0', '1.0.0', 'sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc', 'v1', 'test', '{}'::jsonb, now())
		RETURNING id::text`).Scan(&bundleID); err != nil {
		t.Fatalf("insert membership test game bundle: %v", err)
	}
	var serverID string
	if err := s.db.QueryRow(`
		INSERT INTO servers (
			owner_id, node_id, game_bundle_id, game_version, name, lifecycle_state,
			desired_power, observed_power, node_condition, health_condition, memory_limit_bytes, disk_limit_bytes
		)
		VALUES ($1, $2, $3, '1.0.0', 'Membership Test Server', 'ready', 'stopped', 'stopped', 'available', 'healthy', 1073741824, 1073741824)
		RETURNING id::text`, ownerID, nodeID, bundleID).Scan(&serverID); err != nil {
		t.Fatalf("insert membership test server: %v", err)
	}
	return serverID
}

func TestPostgresMembershipAndReset(t *testing.T) {
	s := testPostgres(t)
	resetTestDatabase(t, s)
	s.SetBootstrapToken("bootstrap-token-12345678901234567890123456789012")

	// Setup 必须校验 bootstrap token
	if _, err := s.SetupAdmin(domain.SetupAdminInput{Email: "admin@test.local", DisplayName: "Admin", Password: "correct-horse-battery", BootstrapToken: "wrong-token"}); err == nil {
		t.Fatal("expected wrong bootstrap token to be rejected")
	}
	admin, err := s.SetupAdmin(domain.SetupAdminInput{Email: "admin@test.local", DisplayName: "Admin", Password: "correct-horse-battery", BootstrapToken: "bootstrap-token-12345678901234567890123456789012"})
	if err != nil {
		t.Fatalf("setup admin: %v", err)
	}

	// server_members.server_id has a foreign key to servers(id), so create a real server row.
	serverID := createServerFixture(t, s, admin.ID)

	// membership 写入/读取/撤销
	member, err := s.PutServerMembership(serverID, admin.ID, []string{"servers.read", "servers.power"}, admin)
	if err != nil {
		t.Fatalf("put membership: %v", err)
	}
	if len(member.Permissions) != 2 {
		t.Fatalf("expected 2 permissions, got %v", member.Permissions)
	}
	got, err := s.ServerMembership(serverID, admin.ID)
	if err != nil || len(got.Permissions) != 2 {
		t.Fatalf("read membership: %v %v", got, err)
	}
	if err := s.DeleteServerMembership(serverID, admin.ID, admin); err != nil {
		t.Fatalf("delete membership: %v", err)
	}
	if _, err := s.ServerMembership(serverID, admin.ID); err == nil {
		t.Fatal("expected membership to be gone")
	}

	// reset 令牌签发与消费；旧会话应被撤销
	_, loginToken, err := s.Login("admin@test.local", "correct-horse-battery")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	resetToken, err := s.IssuePasswordResetToken(admin.ID, admin)
	if err != nil {
		t.Fatalf("issue reset token: %v", err)
	}
	if err := s.ResetPassword(resetToken.Token, "new-password-12345"); err != nil {
		t.Fatalf("reset password: %v", err)
	}
	if _, err := s.Session(loginToken); err == nil {
		t.Fatal("expected pre-reset session to be revoked")
	}
	if _, _, err := s.Login("admin@test.local", "new-password-12345"); err != nil {
		t.Fatalf("login with new password: %v", err)
	}
	if !s.ValidateAgentToken("test-agent-token-1234567890") {
		t.Fatal("expected agent token to validate")
	}
	if s.ValidateAgentToken("wrong-agent-token") {
		t.Fatal("expected wrong agent token to fail")
	}
}
