package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/store"
)

func TestGameDefinitionsAPIExposesFailClosedTrustAndRuntimeState(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()
	server := httptest.NewServer(New(service, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()
	client, _ := authenticatedClient(t, server.URL)

	response := doJSON(t, client, http.MethodGet, server.URL+"/api/v1/game-definitions", "", nil)
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("catalog status = %d, want 200", response.StatusCode)
	}
	var payload struct {
		Data []domain.GameDefinition `json:"data"`
	}
	decodeResponse(t, response, &payload)
	if len(payload.Data) != 3 {
		t.Fatalf("catalog entries = %d, want 3", len(payload.Data))
	}
	for _, game := range payload.Data {
		if game.Signed || game.Verified || game.Supported {
			t.Errorf("API game %s makes an unsupported claim: %+v", game.ID, game)
		}
		if game.TrustLevel != "L0_LOCAL" || game.Source != "embedded-v1alpha1" {
			t.Errorf("API game %s truth metadata = %+v", game.ID, game)
		}
		if game.ID == "io.gugumanager.papermc" {
			if !game.Runnable || game.RuntimeTarget == nil || !reflect.DeepEqual(game.SupportReasons, []string{"BUNDLE_SIGNATURE_UNVERIFIED"}) {
				t.Errorf("API PaperMC runtime metadata = %+v", game)
			}
			continue
		}
		if game.Runnable || game.RuntimeTarget == nil || !reflect.DeepEqual(game.SupportReasons, []string{"BUNDLE_SIGNATURE_UNVERIFIED", "RUNTIME_TARGET_UNAVAILABLE"}) {
			t.Errorf("API unverified runtime metadata for %s = %+v", game.ID, game)
		}
	}
}
