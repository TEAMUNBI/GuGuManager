package store

import (
	"strings"
	"testing"
	"time"
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
	if _, found := service.sessions[tokenDigest(token)]; !found {
		t.Fatal("session was not stored under its token digest")
	}
}

func TestExpiredSessionCannotPassCSRFValidation(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	session, token, err := service.Login("admin@gugu.local", "gugu-dev-2026")
	if err != nil {
		t.Fatal(err)
	}
	digest := tokenDigest(token)
	stored := service.sessions[digest]
	stored.ExpiresAt = time.Now().Add(-time.Second)
	service.sessions[digest] = stored

	if service.ValidateCSRF(token, session.CSRFToken) {
		t.Fatal("expired session passed CSRF validation")
	}
	if _, err := service.Session(token); err == nil {
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
