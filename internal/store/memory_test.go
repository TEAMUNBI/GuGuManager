package store

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
)

func TestEmptyCollectionsRemainNonNilForJSONArrayResponses(t *testing.T) {
	service := newTestMemory(time.Millisecond)

	t.Run("console", func(t *testing.T) {
		lines, err := service.Console(stoppedServerID)
		if err != nil {
			t.Fatal(err)
		}
		if lines == nil {
			t.Fatal("empty console snapshot is nil, want an empty JSON array")
		}
	})

	t.Run("backups", func(t *testing.T) {
		backups, err := service.Backups("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
		if err != nil {
			t.Fatal(err)
		}
		if backups == nil {
			t.Fatal("empty backup list is nil, want an empty JSON array")
		}
	})
}

func TestDevelopmentIdentityUsesArgon2idAndStoresOnlySessionTokenDigest(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	if !strings.HasPrefix(service.adminPasswordPHC, "$argon2id$") {
		t.Fatalf("administrator password = %q, want Argon2id PHC", service.adminPasswordPHC)
	}

	session, token, err := service.Login("admin@gugu.local", "gugu-dev-2026")
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || session.CSRFToken == "" {
		t.Fatal("login did not return session and CSRF tokens")
	}
	if _, found := service.sessions[sessionTokenDigest(token)]; !found {
		t.Fatal("session was not stored under its token digest")
	}
	if _, found := service.sessions[tokenDigest(token)]; found {
		t.Fatal("session was stored under the legacy, non-domain-separated digest")
	}
}

func TestMemorySessionAuthenticationAndSingleUseRecovery(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	defer func() { _ = service.Close() }()

	login, oldToken, err := service.Login("admin@gugu.local", "gugu-dev-2026")
	if err != nil {
		t.Fatal(err)
	}
	authenticated, err := service.AuthenticateSession(oldToken)
	if err != nil {
		t.Fatal(err)
	}
	if authenticated.CSRFToken != "" {
		t.Fatal("ordinary authentication exposed a CSRF plaintext")
	}
	if !service.ValidateCSRF(oldToken, login.CSRFToken) {
		t.Fatal("ordinary authentication invalidated the current CSRF token")
	}

	recovered, newToken, err := service.RecoverSession(oldToken)
	if err != nil {
		t.Fatal(err)
	}
	if newToken == "" || newToken == oldToken || recovered.CSRFToken == "" || recovered.CSRFToken == login.CSRFToken {
		t.Fatalf("recovery did not rotate both opaque values: old=%q new=%q csrfChanged=%v", oldToken, newToken, recovered.CSRFToken != login.CSRFToken)
	}
	if _, err := service.AuthenticateSession(oldToken); err == nil {
		t.Fatal("recovery left the old session active")
	}
	if service.ValidateCSRF(oldToken, login.CSRFToken) {
		t.Fatal("old session and CSRF remained valid after recovery")
	}
	if !service.ValidateCSRF(newToken, recovered.CSRFToken) {
		t.Fatal("recovery response did not contain a usable CSRF token")
	}
}

func TestMemoryLegacySessionDigestIsInvalidated(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	defer func() { _ = service.Close() }()
	login, token, err := service.Login("admin@gugu.local", "gugu-dev-2026")
	if err != nil {
		t.Fatal(err)
	}
	currentDigest := sessionTokenDigest(token)
	legacyDigest := tokenDigest(token)
	service.mu.Lock()
	service.sessions[legacyDigest] = service.sessions[currentDigest]
	delete(service.sessions, currentDigest)
	service.mu.Unlock()

	if _, err := service.AuthenticateSession(token); err == nil {
		t.Fatal("legacy session digest remained valid after the v2 switch")
	}
	if service.ValidateCSRF(token, login.CSRFToken) {
		t.Fatal("legacy session digest passed CSRF validation")
	}
}

func TestMemoryConcurrentSessionRecoveryHasOneUsableWinner(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	defer func() { _ = service.Close() }()
	_, token, err := service.Login("admin@gugu.local", "gugu-dev-2026")
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
			view, nextToken, recoverErr := service.RecoverSession(token)
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
		if !service.ValidateCSRF(current.token, current.view.CSRFToken) {
			t.Fatal("winning recovery response was not usable")
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent recoveries = %d, want exactly 1", successes)
	}
}

func TestExpiredSessionCannotPassCSRFValidation(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	session, token, err := service.Login("admin@gugu.local", "gugu-dev-2026")
	if err != nil {
		t.Fatal(err)
	}
	digest := sessionTokenDigest(token)
	stored := service.sessions[digest]
	stored.ExpiresAt = time.Now().Add(-time.Second)
	service.sessions[digest] = stored

	if service.ValidateCSRF(token, session.CSRFToken) {
		t.Fatal("expired session passed CSRF validation")
	}
	if _, err := service.AuthenticateSession(token); err == nil {
		t.Fatal("expired session was accepted")
	}
	if _, found := service.sessions[digest]; found {
		t.Fatal("expired session was not revoked on lookup")
	}
}

func TestServerFileOperationsUseTheSafeFilesystemAndWriteAuditEvents(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	defer func() { _ = service.Close() }()
	actor := testActor("admin-1", "GuGu Admin")

	if err := service.CreateDirectory(stoppedServerID, "panel", actor); err != nil {
		t.Fatalf("CreateDirectory returned error: %v", err)
	}
	if err := service.WriteFile(stoppedServerID, "panel/notes.txt", []byte("save before upgrade"), actor); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	content, err := service.ReadFile(stoppedServerID, "panel/notes.txt")
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if content.Content != "save before upgrade" || content.Encoding != "utf-8" {
		t.Fatalf("file content = %+v", content)
	}
	if err := service.MoveFile(stoppedServerID, "panel/notes.txt", "panel/upgrade-notes.txt", false, actor); err != nil {
		t.Fatalf("MoveFile returned error: %v", err)
	}
	if err := service.DeleteFile(stoppedServerID, "panel", true, actor); err != nil {
		t.Fatalf("DeleteFile returned error: %v", err)
	}
	if _, err := service.ReadFile(stoppedServerID, "panel/upgrade-notes.txt"); err == nil {
		t.Fatal("deleted file remained readable")
	} else {
		requireProblemCode(t, err, "NOT_FOUND")
	}

	events := service.AuditEvents()
	for _, action := range []string{"file.mkdir", "file.write", "file.move", "file.delete"} {
		found := false
		for _, event := range events {
			if event.Action == action && event.Result == "success" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing success audit event %q in %+v", action, events)
		}
	}
}
