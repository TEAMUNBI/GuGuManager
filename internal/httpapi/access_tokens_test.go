package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/store"
)

func TestAPITokenLifecycleAndScopeEnforcement(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer service.Close()
	testServer := httptest.NewServer(New(service, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer testServer.Close()
	client, session := authenticatedClient(t, testServer.URL)

	missingCSRF := doJSON(t, client, http.MethodPost, testServer.URL+"/api/v1/api-tokens", `{"name":"automation","scopes":["servers.read"]}`, nil)
	missingCSRF.Body.Close()
	if missingCSRF.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d, want 403", missingCSRF.StatusCode)
	}

	created := doJSON(t, client, http.MethodPost, testServer.URL+"/api/v1/api-tokens", `{"name":"automation","scopes":["servers.read"]}`, map[string]string{"X-CSRF-Token": session.CSRFToken})
	if created.StatusCode != http.StatusCreated {
		created.Body.Close()
		t.Fatalf("create token status = %d, want 201", created.StatusCode)
	}
	var credential struct {
		Data domain.APITokenCredential `json:"data"`
	}
	decodeResponse(t, created, &credential)
	if credential.Data.Token == "" || credential.Data.Name != "automation" {
		t.Fatalf("credential = %+v", credential.Data)
	}

	listed := doJSON(t, client, http.MethodGet, testServer.URL+"/api/v1/api-tokens", "", nil)
	var tokens struct {
		Data []domain.APIToken `json:"data"`
	}
	decodeResponse(t, listed, &tokens)
	if len(tokens.Data) != 1 || tokens.Data[0].ID != credential.Data.ID {
		t.Fatalf("tokens = %+v", tokens.Data)
	}

	headers := map[string]string{"Authorization": "Bearer " + credential.Data.Token}
	server := doJSON(t, testServer.Client(), http.MethodGet, testServer.URL+"/api/v1/servers/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "", headers)
	server.Body.Close()
	if server.StatusCode != http.StatusOK {
		t.Fatalf("server read with bearer status = %d, want 200", server.StatusCode)
	}
	audit := doJSON(t, testServer.Client(), http.MethodGet, testServer.URL+"/api/v1/audit-events", "", headers)
	audit.Body.Close()
	if audit.StatusCode != http.StatusForbidden {
		t.Fatalf("admin API without platform.admin status = %d, want 403", audit.StatusCode)
	}
	power := doJSON(t, testServer.Client(), http.MethodPost, testServer.URL+"/api/v1/servers/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/power", `{"action":"stop"}`, headers)
	power.Body.Close()
	if power.StatusCode != http.StatusForbidden {
		t.Fatalf("power without scope status = %d, want 403", power.StatusCode)
	}

	revoked := doJSON(t, client, http.MethodDelete, testServer.URL+"/api/v1/api-tokens/"+credential.Data.ID, "", map[string]string{"X-CSRF-Token": session.CSRFToken})
	revoked.Body.Close()
	if revoked.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke status = %d, want 204", revoked.StatusCode)
	}
	after := doJSON(t, testServer.Client(), http.MethodGet, testServer.URL+"/api/v1/servers/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "", headers)
	after.Body.Close()
	if after.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked bearer status = %d, want 401", after.StatusCode)
	}
}
