package store

import (
	"context"
	"sync"
	"testing"

	"github.com/gugumanager/gugumanager/internal/domain"
)

func TestPostgresSessionAuthenticationRecoveryAndLegacyInvalidation(t *testing.T) {
	s := testPostgres(t)
	resetTestDatabase(t, s)
	s.SetBootstrapToken("bootstrap-token-12345678901234567890123456789012")
	_, err := s.SetupAdmin(domain.SetupAdminInput{
		BootstrapToken: "bootstrap-token-12345678901234567890123456789012",
		Email:          "session-admin@test.local", DisplayName: "Session Admin", Password: "correct-horse-battery",
	})
	if err != nil {
		t.Fatalf("setup admin: %v", err)
	}
	login, oldToken, err := s.Login("session-admin@test.local", "correct-horse-battery")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	authenticated, err := s.AuthenticateSession(oldToken)
	if err != nil {
		t.Fatalf("authenticate session: %v", err)
	}
	if authenticated.CSRFToken != "" {
		t.Fatal("ordinary PostgreSQL authentication exposed CSRF plaintext")
	}
	if !s.ValidateCSRF(oldToken, login.CSRFToken) {
		t.Fatal("ordinary PostgreSQL authentication invalidated CSRF")
	}

	recovered, newToken, err := s.RecoverSession(oldToken)
	if err != nil {
		t.Fatalf("recover session: %v", err)
	}
	if newToken == "" || newToken == oldToken || recovered.CSRFToken == "" || recovered.CSRFToken == login.CSRFToken {
		t.Fatal("PostgreSQL recovery did not rotate session and CSRF")
	}
	if _, err := s.AuthenticateSession(oldToken); err == nil {
		t.Fatal("old PostgreSQL session remained valid after recovery")
	}
	if !s.ValidateCSRF(newToken, recovered.CSRFToken) {
		t.Fatal("PostgreSQL recovery response was not usable")
	}

	legacyToken := "legacy-session-token-with-adequate-entropy"
	legacyDigest := tokenDigest(legacyToken)
	csrfDigest := tokenDigest("legacy-csrf")
	var userID string
	if err := s.db.QueryRow(`SELECT id::text FROM users WHERE normalized_email = 'session-admin@test.local'`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`
		INSERT INTO sessions (user_id, token_digest, csrf_digest, expires_at)
		VALUES ($1, $2, $3, now() + interval '1 hour')
	`, userID, legacyDigest[:], csrfDigest[:]); err != nil {
		t.Fatalf("insert legacy session: %v", err)
	}
	if _, err := s.AuthenticateSession(legacyToken); err == nil {
		t.Fatal("legacy non-domain-separated session digest was accepted")
	}
}

func TestPostgresConcurrentSessionRecoveryHasOneUsableWinner(t *testing.T) {
	s := testPostgres(t)
	resetTestDatabase(t, s)
	s.SetBootstrapToken("bootstrap-token-12345678901234567890123456789012")
	_, err := s.SetupAdmin(domain.SetupAdminInput{
		BootstrapToken: "bootstrap-token-12345678901234567890123456789012",
		Email:          "concurrent-session@test.local", DisplayName: "Concurrent Session", Password: "correct-horse-battery",
	})
	if err != nil {
		t.Fatalf("setup admin: %v", err)
	}
	_, token, err := s.Login("concurrent-session@test.local", "correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		view  domain.SessionView
		token string
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			view, nextToken, recoverErr := s.RecoverSession(token)
			results <- result{view: view, token: nextToken, err: recoverErr}
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	successes := 0
	for current := range results {
		if current.err != nil {
			continue
		}
		successes++
		if !s.ValidateCSRF(current.token, current.view.CSRFToken) {
			t.Fatal("winning PostgreSQL recovery response was not usable")
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent PostgreSQL recoveries = %d, want 1", successes)
	}
}

func TestPostgresReadinessChecksDatabaseMigrationsAndEncryptionKey(t *testing.T) {
	s := testPostgres(t)
	canonical := []string{"000001", "000002", "000003", "000004", "000005", "000006", "000007", "000008"}
	s.SetRequiredMigrationVersions(canonical)

	if err := s.Readiness(context.Background()); err == nil {
		t.Fatal("readiness accepted a missing startup secret encryption key")
	}
	if err := s.SetSecretCipher([]byte("readiness-test-encryption-key")); err != nil {
		t.Fatal(err)
	}
	s.SetRequiredMigrationVersions(append(canonical, "999999"))
	if err := s.Readiness(context.Background()); err == nil {
		t.Fatal("readiness accepted a missing canonical migration")
	}
	s.SetRequiredMigrationVersions(canonical)
	if err := s.Readiness(context.Background()); err != nil {
		t.Fatalf("ready PostgreSQL rejected: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Readiness(context.Background()); err == nil {
		t.Fatal("readiness accepted a closed PostgreSQL pool")
	}
}
