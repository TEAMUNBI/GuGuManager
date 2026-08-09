package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/identity"
	"github.com/gugumanager/gugumanager/internal/store"
)

func TestProtectedAPIRequiresSession(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestDecodeJSONRequiresApplicationJSONContentType(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		wantError   bool
	}{
		{name: "missing header", wantError: true},
		{name: "plain text", contentType: "text/plain", wantError: true},
		{name: "json with charset", contentType: "application/json; charset=utf-8"},
		{name: "mixed case JSON", contentType: "Application/JSON"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"email":"admin@example.test"}`))
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			var payload struct {
				Email string `json:"email"`
			}
			err := decodeJSON(request, &payload)
			if test.wantError && err == nil {
				t.Fatalf("decodeJSON(%q) accepted an unsupported media type", test.contentType)
			}
			if !test.wantError && err != nil {
				t.Fatalf("decodeJSON(%q) error = %v", test.contentType, err)
			}
		})
	}
}

func TestLoginReturnsUnsupportedMediaTypeForNonJSONBody(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"admin@gugu.local","password":"gugu-dev-2026"}`))
	request.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnsupportedMediaType)
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if payload.Error.Code != "UNSUPPORTED_MEDIA_TYPE" {
		t.Fatalf("error code = %q, want UNSUPPORTED_MEDIA_TYPE", payload.Error.Code)
	}
}

func TestLoginRejectsBodyLargerThanAnonymousJSONLimit(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	body := `{"email":"admin@gugu.local","password":"gugu-dev-2026"}` + strings.Repeat(" ", 64<<10)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
}

func TestSetupHTTPContractConsumesBootstrapTokenOnce(t *testing.T) {
	service := store.NewMemoryForSetup("development", "bootstrap-token-with-enough-entropy", time.Now().Add(10*time.Minute), "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(handler)
	defer testServer.Close()
	client := testServer.Client()

	statusResponse := doJSON(t, client, http.MethodGet, testServer.URL+"/api/v1/setup/status", "", nil)
	if statusResponse.StatusCode != http.StatusOK {
		t.Fatalf("setup status = %d, want 200", statusResponse.StatusCode)
	}
	var status struct {
		Data domain.SetupStatus `json:"data"`
	}
	decodeResponse(t, statusResponse, &status)
	if !status.Data.Required || status.Data.BootstrapExpiresAt == nil {
		t.Fatalf("setup status payload = %+v", status.Data)
	}

	wrong := doJSON(t, client, http.MethodPost, testServer.URL+"/api/v1/setup/admin", `{"bootstrapToken":"wrong-token-value","email":"owner@example.test","displayName":"Owner","password":"correct horse battery staple"}`, nil)
	wrong.Body.Close()
	if wrong.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong bootstrap token status = %d, want 401", wrong.StatusCode)
	}

	created := doJSON(t, client, http.MethodPost, testServer.URL+"/api/v1/setup/admin", `{"bootstrapToken":"bootstrap-token-with-enough-entropy","email":"owner@example.test","displayName":"Owner","password":"correct horse battery staple"}`, nil)
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("setup admin status = %d, want 201", created.StatusCode)
	}
	var user struct {
		Data domain.User `json:"data"`
	}
	decodeResponse(t, created, &user)
	if user.Data.Email != "owner@example.test" || user.Data.Status != "active" {
		t.Fatalf("created setup user = %+v", user.Data)
	}

	replay := doJSON(t, client, http.MethodPost, testServer.URL+"/api/v1/setup/admin", `{"bootstrapToken":"bootstrap-token-with-enough-entropy","email":"second@example.test","displayName":"Second","password":"another secure password"}`, nil)
	defer replay.Body.Close()
	if replay.StatusCode != http.StatusConflict {
		t.Fatalf("bootstrap replay status = %d, want 409", replay.StatusCode)
	}
}

func TestUserMembershipHTTPAuthorizationAndImmediateRevocation(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(handler)
	defer testServer.Close()
	adminClient, adminSession := authenticatedClient(t, testServer.URL)
	adminHeaders := map[string]string{"X-CSRF-Token": adminSession.CSRFToken}

	missingCSRF := doJSON(t, adminClient, http.MethodPost, testServer.URL+"/api/v1/users", `{"email":"member@example.test","displayName":"Member","password":"member secure password","roles":["server_owner"]}`, nil)
	missingCSRF.Body.Close()
	if missingCSRF.StatusCode != http.StatusForbidden {
		t.Fatalf("user create without CSRF status = %d, want 403", missingCSRF.StatusCode)
	}

	created := doJSON(t, adminClient, http.MethodPost, testServer.URL+"/api/v1/users", `{"email":"member@example.test","displayName":"Member","password":"member secure password","roles":["server_owner"]}`, adminHeaders)
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("user create status = %d, want 201", created.StatusCode)
	}
	var createdPayload struct {
		Data domain.User `json:"data"`
	}
	decodeResponse(t, created, &createdPayload)
	memberID := createdPayload.Data.ID

	membershipURL := testServer.URL + "/api/v1/servers/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/members/" + memberID
	granted := doJSON(t, adminClient, http.MethodPut, membershipURL, `{"permissions":["servers.read","servers.files.read"]}`, adminHeaders)
	granted.Body.Close()
	if granted.StatusCode != http.StatusOK {
		t.Fatalf("membership put status = %d, want 200", granted.StatusCode)
	}

	memberJar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	memberClient := &http.Client{Jar: memberJar}
	login := doJSON(t, memberClient, http.MethodPost, testServer.URL+"/api/v1/auth/login", `{"email":"member@example.test","password":"member secure password"}`, nil)
	if login.StatusCode != http.StatusOK {
		t.Fatalf("member login status = %d", login.StatusCode)
	}
	var memberSession struct {
		Data domain.SessionView `json:"data"`
	}
	decodeResponse(t, login, &memberSession)

	assigned := doJSON(t, memberClient, http.MethodGet, testServer.URL+"/api/v1/servers/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "", nil)
	assigned.Body.Close()
	if assigned.StatusCode != http.StatusOK {
		t.Fatalf("assigned server status = %d, want 200", assigned.StatusCode)
	}
	unassigned := doJSON(t, memberClient, http.MethodGet, testServer.URL+"/api/v1/servers/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "", nil)
	unassigned.Body.Close()
	if unassigned.StatusCode != http.StatusNotFound {
		t.Fatalf("unassigned server status = %d, want 404", unassigned.StatusCode)
	}
	deniedPower := doJSON(t, memberClient, http.MethodPost, testServer.URL+"/api/v1/servers/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/power", `{"action":"start"}`, map[string]string{
		"X-CSRF-Token": memberSession.Data.CSRFToken, "Idempotency-Key": "member-power-key-0001",
	})
	deniedPower.Body.Close()
	if deniedPower.StatusCode != http.StatusForbidden {
		t.Fatalf("ungranted power status = %d, want 403", deniedPower.StatusCode)
	}

	revoked := doJSON(t, adminClient, http.MethodDelete, membershipURL, "", adminHeaders)
	revoked.Body.Close()
	if revoked.StatusCode != http.StatusNoContent {
		t.Fatalf("membership delete status = %d, want 204", revoked.StatusCode)
	}
	afterRevoke := doJSON(t, memberClient, http.MethodGet, testServer.URL+"/api/v1/servers/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "", nil)
	defer afterRevoke.Body.Close()
	if afterRevoke.StatusCode != http.StatusNotFound {
		t.Fatalf("revoked membership server status = %d, want 404", afterRevoke.StatusCode)
	}
}

func TestEffectiveServerPermissionsHTTPScopesMemberAndAdmin(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(handler)
	defer testServer.Close()
	const serverID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

	unauthenticated := doJSON(t, testServer.Client(), http.MethodGet, testServer.URL+"/api/v1/servers/"+serverID+"/permissions", "", nil)
	unauthenticated.Body.Close()
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated permission status = %d, want 401", unauthenticated.StatusCode)
	}

	adminClient, adminSession := authenticatedClient(t, testServer.URL)
	adminHeaders := map[string]string{"X-CSRF-Token": adminSession.CSRFToken}
	created := doJSON(t, adminClient, http.MethodPost, testServer.URL+"/api/v1/users", `{"email":"effective-http-member@example.test","displayName":"Effective HTTP Member","password":"member secure password","roles":["server_owner"]}`, adminHeaders)
	if created.StatusCode != http.StatusCreated {
		created.Body.Close()
		t.Fatalf("user create status = %d, want 201", created.StatusCode)
	}
	var createdPayload struct {
		Data domain.User `json:"data"`
	}
	decodeResponse(t, created, &createdPayload)
	membershipURL := testServer.URL + "/api/v1/servers/" + serverID + "/members/" + createdPayload.Data.ID
	grant := doJSON(t, adminClient, http.MethodPut, membershipURL, `{"permissions":["servers.read","servers.files.read"]}`, adminHeaders)
	if grant.StatusCode != http.StatusOK {
		grant.Body.Close()
		t.Fatalf("membership grant status = %d, want 200", grant.StatusCode)
	}
	grant.Body.Close()

	memberJar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	memberClient := &http.Client{Jar: memberJar}
	login := doJSON(t, memberClient, http.MethodPost, testServer.URL+"/api/v1/auth/login", `{"email":"effective-http-member@example.test","password":"member secure password"}`, nil)
	if login.StatusCode != http.StatusOK {
		login.Body.Close()
		t.Fatalf("member login status = %d, want 200", login.StatusCode)
	}
	login.Body.Close()

	memberResponse := doJSON(t, memberClient, http.MethodGet, testServer.URL+"/api/v1/servers/"+serverID+"/permissions", "", nil)
	if memberResponse.StatusCode != http.StatusOK {
		memberResponse.Body.Close()
		t.Fatalf("member permission status = %d, want 200", memberResponse.StatusCode)
	}
	var memberPayload struct {
		Data struct {
			ServerID    string   `json:"serverId"`
			Permissions []string `json:"permissions"`
		} `json:"data"`
	}
	decodeResponse(t, memberResponse, &memberPayload)
	if memberPayload.Data.ServerID != serverID || !reflect.DeepEqual(memberPayload.Data.Permissions, []string{"servers.files.read", "servers.read"}) {
		t.Fatalf("member permission payload = %+v", memberPayload.Data)
	}

	adminResponse := doJSON(t, adminClient, http.MethodGet, testServer.URL+"/api/v1/servers/"+serverID+"/permissions", "", nil)
	if adminResponse.StatusCode != http.StatusOK {
		adminResponse.Body.Close()
		t.Fatalf("admin permission status = %d, want 200", adminResponse.StatusCode)
	}
	var adminPayload struct {
		Data struct {
			ServerID    string   `json:"serverId"`
			Permissions []string `json:"permissions"`
		} `json:"data"`
	}
	decodeResponse(t, adminResponse, &adminPayload)
	wantAdmin := []string{
		"servers.backups.create", "servers.backups.delete", "servers.backups.read", "servers.backups.restore",
		"servers.console", "servers.files.read", "servers.files.write", "servers.network.read", "servers.network.write",
		"servers.power", "servers.read", "servers.startup.read", "servers.startup.write",
	}
	if adminPayload.Data.ServerID != serverID || !reflect.DeepEqual(adminPayload.Data.Permissions, wantAdmin) {
		t.Fatalf("admin permission payload = %+v, want %#v", adminPayload.Data, wantAdmin)
	}

	unknown := doJSON(t, memberClient, http.MethodGet, testServer.URL+"/api/v1/servers/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/permissions", "", nil)
	defer unknown.Body.Close()
	if unknown.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown/unassigned permission status = %d, want 404", unknown.StatusCode)
	}
}

func TestOperationListUsesCurrentServerReadGrants(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Second)
	defer func() { _ = service.Close() }()
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(handler)
	defer testServer.Close()

	unauthorized := doJSON(t, testServer.Client(), http.MethodGet, testServer.URL+"/api/v1/operations", "", nil)
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized operation list status = %d, want 401", unauthorized.StatusCode)
	}

	adminClient, adminSession := authenticatedClient(t, testServer.URL)
	adminHeaders := map[string]string{"X-CSRF-Token": adminSession.CSRFToken}
	created := doJSON(t, adminClient, http.MethodPost, testServer.URL+"/api/v1/users", `{"email":"operations-member@example.test","displayName":"Operations Member","password":"member secure password","roles":["server_owner"]}`, adminHeaders)
	if created.StatusCode != http.StatusCreated {
		created.Body.Close()
		t.Fatalf("user create status = %d, want 201", created.StatusCode)
	}
	var createdPayload struct {
		Data domain.User `json:"data"`
	}
	decodeResponse(t, created, &createdPayload)

	const allowedServerID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	const hiddenServerID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	membershipURL := testServer.URL + "/api/v1/servers/" + allowedServerID + "/members/" + createdPayload.Data.ID
	granted := doJSON(t, adminClient, http.MethodPut, membershipURL, `{"permissions":["servers.read"]}`, adminHeaders)
	granted.Body.Close()
	if granted.StatusCode != http.StatusOK {
		t.Fatalf("membership put status = %d, want 200", granted.StatusCode)
	}

	for _, request := range []struct {
		serverID string
		action   string
		key      string
	}{
		{serverID: allowedServerID, action: "start", key: "operation-list-allowed"},
		{serverID: hiddenServerID, action: "stop", key: "operation-list-hidden"},
	} {
		response := doJSON(t, adminClient, http.MethodPost, testServer.URL+"/api/v1/servers/"+request.serverID+"/power", `{"action":"`+request.action+`"}`, map[string]string{
			"X-CSRF-Token":    adminSession.CSRFToken,
			"Idempotency-Key": request.key,
		})
		response.Body.Close()
		if response.StatusCode != http.StatusAccepted {
			t.Fatalf("power request for %s status = %d, want 202", request.serverID, response.StatusCode)
		}
	}

	adminList := doJSON(t, adminClient, http.MethodGet, testServer.URL+"/api/v1/operations?limit=1", "", nil)
	if adminList.StatusCode != http.StatusOK {
		adminList.Body.Close()
		t.Fatalf("admin operation list status = %d, want 200", adminList.StatusCode)
	}
	var adminPayload struct {
		Data []domain.Operation `json:"data"`
		Page struct {
			NextCursor *string `json:"nextCursor"`
		} `json:"page"`
	}
	decodeResponse(t, adminList, &adminPayload)
	if len(adminPayload.Data) != 1 || adminPayload.Page.NextCursor == nil {
		t.Fatalf("admin first operation page = %+v, want one item and a cursor", adminPayload)
	}
	adminNext := doJSON(t, adminClient, http.MethodGet, testServer.URL+"/api/v1/operations?limit=1&cursor="+url.QueryEscape(*adminPayload.Page.NextCursor), "", nil)
	if adminNext.StatusCode != http.StatusOK {
		adminNext.Body.Close()
		t.Fatalf("admin next operation page status = %d, want 200", adminNext.StatusCode)
	}
	var adminNextPayload struct {
		Data []domain.Operation `json:"data"`
		Page struct {
			NextCursor *string `json:"nextCursor"`
		} `json:"page"`
	}
	decodeResponse(t, adminNext, &adminNextPayload)
	if len(adminNextPayload.Data) != 1 || adminNextPayload.Page.NextCursor != nil || adminNextPayload.Data[0].ID == adminPayload.Data[0].ID {
		t.Fatalf("admin second operation page = %+v, want the remaining item", adminNextPayload)
	}

	memberJar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	memberClient := &http.Client{Jar: memberJar}
	login := doJSON(t, memberClient, http.MethodPost, testServer.URL+"/api/v1/auth/login", `{"email":"operations-member@example.test","password":"member secure password"}`, nil)
	login.Body.Close()
	if login.StatusCode != http.StatusOK {
		t.Fatalf("member login status = %d, want 200", login.StatusCode)
	}

	memberList := doJSON(t, memberClient, http.MethodGet, testServer.URL+"/api/v1/operations", "", nil)
	if memberList.StatusCode != http.StatusOK {
		memberList.Body.Close()
		t.Fatalf("member operation list status = %d, want 200", memberList.StatusCode)
	}
	var memberPayload struct {
		Data []domain.Operation `json:"data"`
	}
	decodeResponse(t, memberList, &memberPayload)
	if len(memberPayload.Data) != 1 || memberPayload.Data[0].ServerID != allowedServerID {
		t.Fatalf("member operations = %+v, want only server %s", memberPayload.Data, allowedServerID)
	}

	revoked := doJSON(t, adminClient, http.MethodDelete, membershipURL, "", adminHeaders)
	revoked.Body.Close()
	if revoked.StatusCode != http.StatusNoContent {
		t.Fatalf("membership delete status = %d, want 204", revoked.StatusCode)
	}
	afterRevoke := doJSON(t, memberClient, http.MethodGet, testServer.URL+"/api/v1/operations", "", nil)
	if afterRevoke.StatusCode != http.StatusOK {
		afterRevoke.Body.Close()
		t.Fatalf("revoked operation list status = %d, want 200", afterRevoke.StatusCode)
	}
	var revokedPayload struct {
		Data []domain.Operation `json:"data"`
	}
	decodeResponse(t, afterRevoke, &revokedPayload)
	if len(revokedPayload.Data) != 0 {
		t.Fatalf("revoked operations = %+v, want none", revokedPayload.Data)
	}
}

func TestSlowPowerRequestIsRejectedWhenMembershipIsRevokedAfterHandlerAuthorization(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()
	admin := domain.User{ID: "00000000-0000-4000-8000-000000000001", DisplayName: "GuGu Admin", Roles: []string{"platform_admin"}}
	member, err := service.CreateUser(domain.CreateUserInput{
		Email: "slow-power-member@example.test", DisplayName: "Slow Power Member", Password: "member secure password", Roles: []string{"server_owner"},
	}, admin)
	if err != nil {
		t.Fatalf("CreateUser member failed: %v", err)
	}
	if _, err := service.PutServerMembership("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", member.ID, []string{"servers.read", "servers.power"}, admin); err != nil {
		t.Fatalf("PutServerMembership failed: %v", err)
	}
	barrier := &authorizeBarrierService{ControlPlane: service, authorized: make(chan struct{})}
	handler := New(barrier, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(handler)
	defer testServer.Close()

	memberJar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	memberClient := &http.Client{Jar: memberJar}
	login := doJSON(t, memberClient, http.MethodPost, testServer.URL+"/api/v1/auth/login", `{"email":"slow-power-member@example.test","password":"member secure password"}`, nil)
	if login.StatusCode != http.StatusOK {
		login.Body.Close()
		t.Fatalf("member login status = %d, want 200", login.StatusCode)
	}
	var session struct {
		Data domain.SessionView `json:"data"`
	}
	decodeResponse(t, login, &session)

	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })
	request, err := http.NewRequest(http.MethodPost, testServer.URL+"/api/v1/servers/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/power", reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", session.Data.CSRFToken)
	request.Header.Set("Idempotency-Key", "slow-power-revocation-01")
	responseResult := make(chan struct {
		response *http.Response
		err      error
	}, 1)
	go func() {
		response, requestErr := memberClient.Do(request)
		responseResult <- struct {
			response *http.Response
			err      error
		}{response: response, err: requestErr}
	}()
	select {
	case <-barrier.authorized:
	case <-time.After(time.Second):
		t.Fatal("handler did not complete initial membership authorization")
	}
	if err := service.DeleteServerMembership("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", member.ID, admin); err != nil {
		t.Fatalf("DeleteServerMembership failed: %v", err)
	}
	serverBefore, err := service.Server("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}
	auditBefore := len(service.AuditEvents())
	if _, err := io.WriteString(writer, `{"action":"start"}`); err != nil {
		t.Fatalf("write delayed request body: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close delayed request body: %v", err)
	}
	select {
	case result := <-responseResult:
		if result.err != nil {
			t.Fatalf("slow request failed: %v", result.err)
		}
		defer result.response.Body.Close()
		if result.response.StatusCode != http.StatusNotFound {
			t.Fatalf("slow revoked power request status = %d, want 404", result.response.StatusCode)
		}
	case <-time.After(time.Second):
		t.Fatal("slow request did not complete after body release")
	}
	serverAfter, err := service.Server("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}
	if serverAfter.Generation != serverBefore.Generation || serverAfter.DesiredPower != serverBefore.DesiredPower || serverAfter.ObservedPower != serverBefore.ObservedPower {
		t.Fatalf("revoked slow request mutated server: before=%+v after=%+v", serverBefore, serverAfter)
	}
	if auditAfter := len(service.AuditEvents()); auditAfter != auditBefore {
		t.Fatalf("revoked slow request emitted an audit event: before=%d after=%d", auditBefore, auditAfter)
	}
}

func TestPasswordResetHTTPRevokesOldSessionAndRejectsReplay(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()
	admin := domain.User{ID: "00000000-0000-4000-8000-000000000001", DisplayName: "GuGu Admin", Roles: []string{"platform_admin"}}
	user, err := service.CreateUser(domain.CreateUserInput{Email: "reset-http@example.test", DisplayName: "Reset HTTP", Password: "old secure password", Roles: []string{"server_owner"}}, admin)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(handler)
	defer testServer.Close()
	adminClient, adminSession := authenticatedClient(t, testServer.URL)

	userJar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	userClient := &http.Client{Jar: userJar}
	login := doJSON(t, userClient, http.MethodPost, testServer.URL+"/api/v1/auth/login", `{"email":"reset-http@example.test","password":"old secure password"}`, nil)
	login.Body.Close()
	if login.StatusCode != http.StatusOK {
		t.Fatalf("reset target login status = %d", login.StatusCode)
	}

	issue := doJSON(t, adminClient, http.MethodPost, testServer.URL+"/api/v1/users/"+user.ID+"/password-reset-tokens", "", map[string]string{"X-CSRF-Token": adminSession.CSRFToken})
	if issue.StatusCode != http.StatusCreated {
		t.Fatalf("reset token issue status = %d, want 201", issue.StatusCode)
	}
	var issued struct {
		Data domain.PasswordResetToken `json:"data"`
	}
	decodeResponse(t, issue, &issued)
	if issued.Data.Token == "" {
		t.Fatal("reset token response omitted one-time plaintext token")
	}

	reset := doJSON(t, testServer.Client(), http.MethodPost, testServer.URL+"/api/v1/auth/password-reset", `{"token":"`+issued.Data.Token+`","password":"new secure password"}`, nil)
	reset.Body.Close()
	if reset.StatusCode != http.StatusNoContent {
		t.Fatalf("password reset status = %d, want 204", reset.StatusCode)
	}
	oldSession := doJSON(t, userClient, http.MethodGet, testServer.URL+"/api/v1/auth/session", "", nil)
	oldSession.Body.Close()
	if oldSession.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old session after reset status = %d, want 401", oldSession.StatusCode)
	}
	replay := doJSON(t, testServer.Client(), http.MethodPost, testServer.URL+"/api/v1/auth/password-reset", `{"token":"`+issued.Data.Token+`","password":"another secure password"}`, nil)
	defer replay.Body.Close()
	if replay.StatusCode != http.StatusUnauthorized {
		t.Fatalf("password reset replay status = %d, want 401", replay.StatusCode)
	}
}

func TestPasswordResetTokenHTTPRejectsMissingAndDisabledUsers(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()
	admin := domain.User{ID: "00000000-0000-4000-8000-000000000001", DisplayName: "GuGu Admin", Roles: []string{"platform_admin"}}
	disabledUser, err := service.CreateUser(domain.CreateUserInput{
		Email: "disabled-reset-http@example.test", DisplayName: "Disabled Reset HTTP", Password: "initial secure password", Roles: []string{"server_owner"},
	}, admin)
	if err != nil {
		t.Fatal(err)
	}
	disabled := "disabled"
	if _, err := service.UpdateUser(disabledUser.ID, domain.UpdateUserInput{Status: &disabled}, admin); err != nil {
		t.Fatal(err)
	}
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(handler)
	defer testServer.Close()
	adminClient, adminSession := authenticatedClient(t, testServer.URL)
	headers := map[string]string{"X-CSRF-Token": adminSession.CSRFToken}

	tests := []struct {
		name       string
		userID     string
		wantStatus int
	}{
		{name: "missing user", userID: "ffffffff-ffff-4fff-8fff-ffffffffffff", wantStatus: http.StatusNotFound},
		{name: "disabled user", userID: disabledUser.ID, wantStatus: http.StatusConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := doJSON(t, adminClient, http.MethodPost, testServer.URL+"/api/v1/users/"+test.userID+"/password-reset-tokens", "", headers)
			defer response.Body.Close()
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.wantStatus)
			}
		})
	}
}

func TestLoginValidatesOpenAPIFieldsBeforeAuthentication(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	tests := []struct {
		name string
		body string
	}{
		{name: "missing fields", body: `{}`},
		{name: "invalid email", body: `{"email":"not-an-email","password":"gugu-dev-2026"}`},
		{name: "short password", body: `{"email":"admin@gugu.local","password":"short"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
			}
		})
	}
}

func TestJSONDecoderRejectsBodiesBeyondTheDocumentLimit(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	body := `{"email":"admin@gugu.local","password":"gugu-dev-2026"}` + strings.Repeat(" ", 12<<20)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("oversized JSON status = %d, want 422", response.Code)
	}
}

func TestLoginRateLimitAppliesBeforePasswordVerification(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(handler)
	defer testServer.Close()
	client := testServer.Client()

	for attempt := 1; attempt <= 5; attempt++ {
		response := doJSON(t, client, http.MethodPost, testServer.URL+"/api/v1/auth/login", `{"email":"admin@gugu.local","password":"wrong-password"}`, nil)
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("failed login %d status = %d, want 401", attempt, response.StatusCode)
		}
	}
	blocked := doJSON(t, client, http.MethodPost, testServer.URL+"/api/v1/auth/login", `{"email":"admin@gugu.local","password":"gugu-dev-2026"}`, nil)
	defer blocked.Body.Close()
	if blocked.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("blocked login status = %d, want 429", blocked.StatusCode)
	}
	if blocked.Header.Get("Retry-After") == "" {
		t.Fatal("rate-limited response omitted Retry-After")
	}
}

func TestLoginRateLimitReservesInFlightAttemptBeforeCallingService(t *testing.T) {
	base := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = base.Close() }()
	service := &blockingLoginService{
		Memory:  base,
		started: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	h := &Handler{
		service: service,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		loginLimiter: identity.NewAttemptLimiter(identity.AttemptLimit{
			Maximum: 1, Window: time.Minute, BlockFor: time.Minute,
		}, time.Now),
		sensitiveLimiter: identity.NewAttemptLimiter(identity.AttemptLimit{
			Maximum: 1, Window: time.Minute, BlockFor: time.Minute,
		}, time.Now),
	}

	newRequest := func() *http.Request {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"admin@gugu.local","password":"gugu-dev-2026"}`))
		request.Header.Set("Content-Type", "application/json")
		request.RemoteAddr = "192.0.2.10:1234"
		return request
	}
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		h.login(response, newRequest())
		firstDone <- response
	}()
	select {
	case <-service.started:
	case <-time.After(time.Second):
		t.Fatal("first login did not reach the service")
	}

	secondDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		h.login(response, newRequest())
		secondDone <- response
	}()

	secondEnteredService := false
	var secondResponse *httptest.ResponseRecorder
	select {
	case <-service.started:
		secondEnteredService = true
	case secondResponse = <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second login neither rate-limited nor completed")
	}
	close(service.release)
	firstResponse := <-firstDone
	if secondResponse == nil {
		secondResponse = <-secondDone
	}
	if secondEnteredService {
		t.Fatal("second login entered the service while the first attempt was in flight")
	}
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first login status = %d, want 200", firstResponse.Code)
	}
	if secondResponse.Code != http.StatusTooManyRequests {
		t.Fatalf("second login status = %d, want 429", secondResponse.Code)
	}
}

type blockingLoginService struct {
	*store.Memory
	started chan struct{}
	release chan struct{}
}

func (s *blockingLoginService) Login(email string, password string) (domain.SessionView, string, error) {
	s.started <- struct{}{}
	<-s.release
	return s.Memory.Login(email, password)
}

func TestProductionLogoutClearsTheSecureSessionCookie(t *testing.T) {
	service := store.NewMemory("production", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	session, token, err := service.Login("admin@gugu.local", "gugu-dev-2026")
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	request.Header.Set("X-CSRF-Token", session.CSRFToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, want %d", response.Code, http.StatusNoContent)
	}
	result := response.Result()
	defer result.Body.Close()
	cookies := result.Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookie {
		t.Fatalf("logout cookies = %+v, want one %s cookie", cookies, sessionCookie)
	}
	if !cookies[0].Secure {
		t.Fatal("production logout cookie is missing Secure")
	}
	if cookies[0].MaxAge >= 0 {
		t.Fatalf("logout cookie MaxAge = %d, want a deletion cookie", cookies[0].MaxAge)
	}
}

func TestLoginPowerAndIdempotency(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(handler)
	defer testServer.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}

	loginBody := `{"email":"admin@gugu.local","password":"gugu-dev-2026"}`
	loginResponse := doJSON(t, client, http.MethodPost, testServer.URL+"/api/v1/auth/login", loginBody, nil)
	if loginResponse.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", loginResponse.StatusCode)
	}
	var session struct {
		Data domain.SessionView `json:"data"`
	}
	decodeResponse(t, loginResponse, &session)
	if session.Data.CSRFToken == "" {
		t.Fatal("login did not return a CSRF token")
	}

	first := doJSON(t, client, http.MethodPost, testServer.URL+"/api/v1/servers/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/power", `{"action":"stop"}`, map[string]string{"X-CSRF-Token": session.Data.CSRFToken, "Idempotency-Key": "power-test-key-0001"})
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("first power status = %d", first.StatusCode)
	}
	var firstPayload struct {
		Data json.RawMessage `json:"data"`
	}
	decodeResponse(t, first, &firstPayload)
	assertOperationMetadataJSON(t, firstPayload.Data)
	var firstOperation domain.Operation
	if err := json.Unmarshal(firstPayload.Data, &firstOperation); err != nil {
		t.Fatal(err)
	}
	second := doJSON(t, client, http.MethodPost, testServer.URL+"/api/v1/servers/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/power", `{"action":"stop"}`, map[string]string{"X-CSRF-Token": session.Data.CSRFToken, "Idempotency-Key": "power-test-key-0001"})
	var secondOperation struct {
		Data domain.Operation `json:"data"`
	}
	decodeResponse(t, second, &secondOperation)
	if firstOperation.ID != secondOperation.Data.ID {
		t.Fatalf("duplicate operation IDs = %q and %q", firstOperation.ID, secondOperation.Data.ID)
	}

	missingCSRF := doJSON(t, client, http.MethodPost, testServer.URL+"/api/v1/servers/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/power", `{"action":"start"}`, map[string]string{"Idempotency-Key": "power-test-key-0002"})
	defer missingCSRF.Body.Close()
	if missingCSRF.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d, want %d", missingCSRF.StatusCode, http.StatusForbidden)
	}
}

func assertOperationMetadataJSON(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"nodeId", "attempt", "maxAttempts", "leaseOwner", "leaseExpiresAt", "checkpoint", "error"} {
		if _, ok := fields[field]; !ok {
			t.Fatalf("operation JSON is missing required %q: %s", field, raw)
		}
	}
	var nodeID string
	if err := json.Unmarshal(fields["nodeId"], &nodeID); err != nil {
		t.Fatalf("decode operation nodeId: %v", err)
	}
	if nodeID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("queued operation nodeId = %q, want the server's target node", nodeID)
	}
	for _, field := range []string{"leaseOwner", "leaseExpiresAt", "error"} {
		if string(fields[field]) != "null" {
			t.Fatalf("queued operation %s = %s, want explicit null", field, fields[field])
		}
	}
}

func TestServerListUsesStableCursorPagination(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(handler)
	defer testServer.Close()
	client, _ := authenticatedClient(t, testServer.URL)

	first := doJSON(t, client, http.MethodGet, testServer.URL+"/api/v1/servers?limit=1", "", nil)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first page status = %d", first.StatusCode)
	}
	var firstPage struct {
		Data []domain.Server `json:"data"`
		Page struct {
			NextCursor *string `json:"nextCursor"`
		} `json:"page"`
	}
	decodeResponse(t, first, &firstPage)
	if len(firstPage.Data) != 1 || firstPage.Page.NextCursor == nil {
		t.Fatalf("first page = %+v, want one item and next cursor", firstPage)
	}

	secondURL := testServer.URL + "/api/v1/servers?limit=1&cursor=" + url.QueryEscape(*firstPage.Page.NextCursor)
	second := doJSON(t, client, http.MethodGet, secondURL, "", nil)
	var secondPage struct {
		Data []domain.Server `json:"data"`
	}
	decodeResponse(t, second, &secondPage)
	if len(secondPage.Data) != 1 || secondPage.Data[0].ID == firstPage.Data[0].ID {
		t.Fatalf("second page did not advance: first=%+v second=%+v", firstPage.Data, secondPage.Data)
	}
}

func TestFileAPIRejectsAbsolutePath(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(handler)
	defer testServer.Close()
	client, _ := authenticatedClient(t, testServer.URL)

	response := doJSON(t, client, http.MethodGet, testServer.URL+"/api/v1/servers/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/files?path=%2Fconfig", "", nil)
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("absolute path status = %d, want %d", response.StatusCode, http.StatusUnprocessableEntity)
	}
}

func TestFileAPIWritesReadsMovesAndDeletesWithinServerRoot(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(handler)
	defer testServer.Close()
	client, session := authenticatedClient(t, testServer.URL)
	headers := map[string]string{"X-CSRF-Token": session.CSRFToken}

	mkdir := doJSON(t, client, http.MethodPost, testServer.URL+"/api/v1/servers/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/files/directories", `{"path":"panel"}`, headers)
	mkdir.Body.Close()
	if mkdir.StatusCode != http.StatusCreated {
		t.Fatalf("mkdir status = %d, want %d", mkdir.StatusCode, http.StatusCreated)
	}
	write := doJSON(t, client, http.MethodPut, testServer.URL+"/api/v1/servers/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/files/content", `{"path":"panel/notes.txt","content":"save before upgrade","encoding":"utf-8"}`, headers)
	write.Body.Close()
	if write.StatusCode != http.StatusNoContent {
		t.Fatalf("write status = %d, want %d", write.StatusCode, http.StatusNoContent)
	}
	read := doJSON(t, client, http.MethodGet, testServer.URL+"/api/v1/servers/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/files/content?path=panel%2Fnotes.txt", "", nil)
	if read.StatusCode != http.StatusOK {
		t.Fatalf("read status = %d", read.StatusCode)
	}
	var content struct {
		Data domain.FileContent `json:"data"`
	}
	decodeResponse(t, read, &content)
	if content.Data.Content != "save before upgrade" || content.Data.Encoding != "utf-8" {
		t.Fatalf("content = %+v", content.Data)
	}
	move := doJSON(t, client, http.MethodPost, testServer.URL+"/api/v1/servers/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/files/moves", `{"source":"panel/notes.txt","destination":"panel/upgrade.txt","replace":false}`, headers)
	move.Body.Close()
	if move.StatusCode != http.StatusNoContent {
		t.Fatalf("move status = %d", move.StatusCode)
	}
	deleteResponse := doJSON(t, client, http.MethodDelete, testServer.URL+"/api/v1/servers/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/files?path=panel&recursive=true", "", headers)
	deleteResponse.Body.Close()
	if deleteResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", deleteResponse.StatusCode)
	}
}

func TestFileWriteRequiresContentMember(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(handler)
	defer testServer.Close()
	client, session := authenticatedClient(t, testServer.URL)

	response := doJSON(t, client, http.MethodPut, testServer.URL+"/api/v1/servers/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/files/content", `{"path":"missing-content.txt","encoding":"utf-8"}`, map[string]string{"X-CSRF-Token": session.CSRFToken})
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("missing content status = %d, want %d", response.StatusCode, http.StatusUnprocessableEntity)
	}
}

func TestFileMoveRequiresReplaceMember(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(handler)
	defer testServer.Close()
	client, session := authenticatedClient(t, testServer.URL)
	headers := map[string]string{"X-CSRF-Token": session.CSRFToken}

	write := doJSON(t, client, http.MethodPut, testServer.URL+"/api/v1/servers/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/files/content", `{"path":"move-source.txt","content":"source","encoding":"utf-8"}`, headers)
	write.Body.Close()
	if write.StatusCode != http.StatusNoContent {
		t.Fatalf("setup write status = %d, want %d", write.StatusCode, http.StatusNoContent)
	}
	move := doJSON(t, client, http.MethodPost, testServer.URL+"/api/v1/servers/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/files/moves", `{"source":"move-source.txt","destination":"move-target.txt"}`, headers)
	defer move.Body.Close()
	if move.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("missing replace status = %d, want %d", move.StatusCode, http.StatusUnprocessableEntity)
	}
}

func TestFileMutationRequiresCSRFAndRejectsTraversal(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(handler)
	defer testServer.Close()
	client, session := authenticatedClient(t, testServer.URL)

	missingCSRF := doJSON(t, client, http.MethodPut, testServer.URL+"/api/v1/servers/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/files/content", `{"path":"notes.txt","content":"x","encoding":"utf-8"}`, nil)
	missingCSRF.Body.Close()
	if missingCSRF.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d", missingCSRF.StatusCode)
	}
	traversal := doJSON(t, client, http.MethodPut, testServer.URL+"/api/v1/servers/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/files/content", `{"path":"../outside.txt","content":"x","encoding":"utf-8"}`, map[string]string{"X-CSRF-Token": session.CSRFToken})
	defer traversal.Body.Close()
	if traversal.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("traversal status = %d, want %d", traversal.StatusCode, http.StatusUnprocessableEntity)
	}
}

func TestBackupRestoreAndDeleteEndpointsReturnOperations(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Second)
	defer func() { _ = service.Close() }()
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(handler)
	defer testServer.Close()
	client, session := authenticatedClient(t, testServer.URL)
	headers := map[string]string{"X-CSRF-Token": session.CSRFToken, "Idempotency-Key": "restore-http-key-0001"}

	restore := doJSON(t, client, http.MethodPost, testServer.URL+"/api/v1/servers/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/backups/d3333333-3333-4333-8333-333333333333/restore", "", headers)
	if restore.StatusCode != http.StatusAccepted {
		t.Fatalf("restore status = %d", restore.StatusCode)
	}
	var operation struct {
		Data domain.Operation `json:"data"`
	}
	decodeResponse(t, restore, &operation)
	if operation.Data.Type != domain.PowerAction("restore") {
		t.Fatalf("restore operation = %+v", operation.Data)
	}

	fileHeaders := map[string]string{"X-CSRF-Token": session.CSRFToken}
	fileMutations := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "write", method: http.MethodPut, path: "/files/content", body: `{"path":"blocked-write.txt","content":"blocked","encoding":"utf-8"}`},
		{name: "create directory", method: http.MethodPost, path: "/files/directories", body: `{"path":"blocked-directory"}`},
		{name: "move", method: http.MethodPost, path: "/files/moves", body: `{"source":"server-settings.json","destination":"blocked-server-settings.json","replace":false}`},
		{name: "delete", method: http.MethodDelete, path: "/files?path=server-settings.json"},
	}
	for _, mutation := range fileMutations {
		t.Run("restore blocks file "+mutation.name, func(t *testing.T) {
			response := doJSON(t, client, mutation.method, testServer.URL+"/api/v1/servers/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"+mutation.path, mutation.body, fileHeaders)
			if response.StatusCode != http.StatusConflict {
				defer response.Body.Close()
				body, _ := io.ReadAll(response.Body)
				t.Fatalf("status = %d, want %d; body=%s", response.StatusCode, http.StatusConflict, body)
			}
			var errorPayload struct {
				Error struct {
					Code    string         `json:"code"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			decodeResponse(t, response, &errorPayload)
			if errorPayload.Error.Code != "OPERATION_IN_PROGRESS" {
				t.Fatalf("error code = %q, want OPERATION_IN_PROGRESS", errorPayload.Error.Code)
			}
			if errorPayload.Error.Details["operationId"] != operation.Data.ID {
				t.Fatalf("lock points to %v, want %s", errorPayload.Error.Details["operationId"], operation.Data.ID)
			}
		})
	}

	service2 := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Second)
	defer func() { _ = service2.Close() }()
	handler2 := New(service2, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer2 := httptest.NewServer(handler2)
	defer testServer2.Close()
	client2, session2 := authenticatedClient(t, testServer2.URL)
	deleteHeaders := map[string]string{"X-CSRF-Token": session2.CSRFToken, "Idempotency-Key": "delete-http-key-00001"}
	deleted := doJSON(t, client2, http.MethodDelete, testServer2.URL+"/api/v1/servers/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/backups/d3333333-3333-4333-8333-333333333333", "", deleteHeaders)
	defer deleted.Body.Close()
	if deleted.StatusCode != http.StatusAccepted {
		t.Fatalf("delete backup status = %d", deleted.StatusCode)
	}
}

func TestAllocationHTTPContractRequiresWriteGuardsAndReturnsReconcileOperation(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Second)
	defer func() { _ = service.Close() }()
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(handler)
	defer testServer.Close()
	client, session := authenticatedClient(t, testServer.URL)
	endpoint := testServer.URL + "/api/v1/servers/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/allocations"

	listed := doJSON(t, client, http.MethodGet, endpoint, "", nil)
	if listed.StatusCode != http.StatusOK {
		t.Fatalf("allocation list status = %d, want 200", listed.StatusCode)
	}
	var listPayload struct {
		Data []domain.Allocation `json:"data"`
	}
	decodeResponse(t, listed, &listPayload)
	if len(listPayload.Data) != 1 || !listPayload.Data[0].Primary {
		t.Fatalf("seeded allocation payload = %+v", listPayload.Data)
	}

	body := `{"bindIp":"10.0.20.14","port":34210,"protocol":"udp","primary":false}`
	missingCSRF := doJSON(t, client, http.MethodPost, endpoint, body, map[string]string{
		"Idempotency-Key": "allocation-http-0001", "If-Match": "7",
	})
	missingCSRF.Body.Close()
	if missingCSRF.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d, want 403", missingCSRF.StatusCode)
	}

	baseHeaders := map[string]string{
		"X-CSRF-Token": session.CSRFToken, "Idempotency-Key": "allocation-http-0001",
	}
	missingGeneration := doJSON(t, client, http.MethodPost, endpoint, body, baseHeaders)
	missingGeneration.Body.Close()
	if missingGeneration.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("missing If-Match status = %d, want 422", missingGeneration.StatusCode)
	}
	malformedHeaders := map[string]string{
		"X-CSRF-Token": session.CSRFToken, "Idempotency-Key": "allocation-http-0001", "If-Match": "generation-7",
	}
	malformed := doJSON(t, client, http.MethodPost, endpoint, body, malformedHeaders)
	malformed.Body.Close()
	if malformed.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("malformed If-Match status = %d, want 422", malformed.StatusCode)
	}

	patchHeaders := map[string]string{
		"X-CSRF-Token": session.CSRFToken, "Idempotency-Key": "allocation-patch-0001", "If-Match": "7",
	}
	invalidPatch := doJSON(t, client, http.MethodPatch, endpoint+"/"+listPayload.Data[0].ID, `{"primary":false}`, patchHeaders)
	invalidPatch.Body.Close()
	if invalidPatch.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("non-promoting PATCH status = %d, want 422", invalidPatch.StatusCode)
	}

	acceptedHeaders := map[string]string{
		"X-CSRF-Token": session.CSRFToken, "Idempotency-Key": "allocation-http-0001", "If-Match": "7",
	}
	accepted := doJSON(t, client, http.MethodPost, endpoint, body, acceptedHeaders)
	if accepted.StatusCode != http.StatusAccepted {
		t.Fatalf("allocation create status = %d, want 202", accepted.StatusCode)
	}
	var operationPayload struct {
		Data domain.Operation `json:"data"`
	}
	decodeResponse(t, accepted, &operationPayload)
	if operationPayload.Data.Type != domain.PowerAction("reconcile") || operationPayload.Data.Generation != 8 {
		t.Fatalf("allocation operation = %+v", operationPayload.Data)
	}

	staleHeaders := map[string]string{
		"X-CSRF-Token": session.CSRFToken, "Idempotency-Key": "allocation-http-stale", "If-Match": "7",
	}
	stale := doJSON(t, client, http.MethodPost, endpoint, `{"bindIp":"10.0.20.14","port":34211,"protocol":"udp","primary":false}`, staleHeaders)
	defer stale.Body.Close()
	if stale.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("stale allocation status = %d, want 412", stale.StatusCode)
	}
}

func TestStartupHTTPContractOmitsSecretValuesAndValidatesUpdates(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Second)
	defer func() { _ = service.Close() }()
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(handler)
	defer testServer.Close()
	client, session := authenticatedClient(t, testServer.URL)
	endpoint := testServer.URL + "/api/v1/servers/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/startup"

	response := doJSON(t, client, http.MethodGet, endpoint, "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("startup status = %d, want 200", response.StatusCode)
	}
	var startupPayload struct {
		Data struct {
			Generation int64                        `json:"generation"`
			Command    domain.StartupCommand        `json:"command"`
			Variables  []map[string]json.RawMessage `json:"variables"`
		} `json:"data"`
	}
	decodeResponse(t, response, &startupPayload)
	if startupPayload.Data.Generation != 7 || startupPayload.Data.Command.Executable == "" {
		t.Fatalf("startup payload = %+v", startupPayload.Data)
	}
	foundSecret := false
	for _, variable := range startupPayload.Data.Variables {
		var key string
		if err := json.Unmarshal(variable["key"], &key); err != nil {
			t.Fatal(err)
		}
		if key != "server_token" {
			continue
		}
		foundSecret = true
		if _, exposed := variable["value"]; exposed {
			t.Fatalf("secret variable exposed value member: %+v", variable)
		}
		if _, exposed := variable["default"]; exposed {
			t.Fatalf("secret variable exposed default member: %+v", variable)
		}
		var hasValue bool
		if err := json.Unmarshal(variable["hasValue"], &hasValue); err != nil || !hasValue {
			t.Fatalf("secret hasValue = %v, err=%v", hasValue, err)
		}
	}
	if !foundSecret {
		t.Fatal("startup response omitted declared secret")
	}

	unknownHeaders := map[string]string{
		"X-CSRF-Token": session.CSRFToken, "Idempotency-Key": "startup-http-unknown", "If-Match": "7",
	}
	unknown := doJSON(t, client, http.MethodPut, endpoint, `{"variables":{"undeclared":"value"}}`, unknownHeaders)
	unknown.Body.Close()
	if unknown.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unknown startup variable status = %d, want 422", unknown.StatusCode)
	}

	missingGenerationHeaders := map[string]string{
		"X-CSRF-Token": session.CSRFToken, "Idempotency-Key": "startup-http-00001",
	}
	missingGeneration := doJSON(t, client, http.MethodPut, endpoint, `{"variables":{"server_name":"Friday Factory 2"}}`, missingGenerationHeaders)
	missingGeneration.Body.Close()
	if missingGeneration.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("missing startup If-Match status = %d, want 422", missingGeneration.StatusCode)
	}

	preciseIntegerHeaders := map[string]string{
		"X-CSRF-Token": session.CSRFToken, "Idempotency-Key": "startup-http-precise", "If-Match": "7",
	}
	preciseInteger := doJSON(t, client, http.MethodPut, endpoint, `{"variables":{"autosave_interval":15.0000000000000000000000000000000001}}`, preciseIntegerHeaders)
	preciseInteger.Body.Close()
	if preciseInteger.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("high-precision fractional startup integer status = %d, want 422", preciseInteger.StatusCode)
	}

	updateHeaders := map[string]string{
		"X-CSRF-Token": session.CSRFToken, "Idempotency-Key": "startup-http-00001", "If-Match": "7",
	}
	updated := doJSON(t, client, http.MethodPut, endpoint, `{"variables":{"server_name":"Friday Factory 2","autosave_interval":15,"public_listing":true,"server_token":null}}`, updateHeaders)
	if updated.StatusCode != http.StatusAccepted {
		t.Fatalf("startup update status = %d, want 202", updated.StatusCode)
	}
	var operationPayload struct {
		Data domain.Operation `json:"data"`
	}
	decodeResponse(t, updated, &operationPayload)
	if operationPayload.Data.Type != domain.PowerAction("reconcile") || operationPayload.Data.Generation != 8 {
		t.Fatalf("startup operation = %+v", operationPayload.Data)
	}

	staleHeaders := map[string]string{
		"X-CSRF-Token": session.CSRFToken, "Idempotency-Key": "startup-http-stale1", "If-Match": "7",
	}
	stale := doJSON(t, client, http.MethodPut, endpoint, `{"variables":{"server_name":"stale"}}`, staleHeaders)
	defer stale.Body.Close()
	if stale.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("stale startup status = %d, want 412", stale.StatusCode)
	}
}

func TestStartupUpdateRejectsCaseVariantVariablesMemberBeforeServiceCall(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "title case", body: `{"Variables":{"server_name":"Friday Factory 2"}}`},
		{name: "upper case", body: `{"VARIABLES":{"server_name":"Friday Factory 2"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Second)
			defer func() { _ = service.Close() }()
			spy := &startupUpdateSpy{ControlPlane: service}
			handler := New(spy, slog.New(slog.NewTextHandler(io.Discard, nil)))
			testServer := httptest.NewServer(handler)
			defer testServer.Close()
			client, session := authenticatedClient(t, testServer.URL)
			const serverID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
			before, err := service.Startup(serverID)
			if err != nil {
				t.Fatal(err)
			}

			response := doJSON(t, client, http.MethodPut, testServer.URL+"/api/v1/servers/"+serverID+"/startup", test.body, map[string]string{
				"X-CSRF-Token":    session.CSRFToken,
				"Idempotency-Key": "startup-http-exact-member",
				"If-Match":        "7",
			})
			if response.StatusCode != http.StatusUnprocessableEntity {
				response.Body.Close()
				t.Fatalf("case-variant variables status = %d, want 422", response.StatusCode)
			}
			var problem struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			decodeResponse(t, response, &problem)
			if problem.Error.Code != "VALIDATION_FAILED" {
				t.Fatalf("case-variant variables error code = %q, want VALIDATION_FAILED", problem.Error.Code)
			}
			if spy.updateCalls != 0 {
				t.Fatalf("UpdateStartup calls = %d, want 0", spy.updateCalls)
			}
			after, err := service.Startup(serverID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("case-variant request mutated startup state: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestStartupHTTPBoundaryRedactsSecretMetadataWithoutMutatingService(t *testing.T) {
	backingService := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Second)
	defer func() { _ = backingService.Close() }()

	leakyService := &startupResponseService{
		ControlPlane: backingService,
		startup: domain.Startup{
			ServerID:   "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
			Generation: 7,
			Command: domain.StartupCommand{
				Executable: "/opt/game/server",
				Args:       []string{"--token", "{{server_token}}"},
			},
			Variables: []domain.StartupVariable{
				{
					Key:        "server_token",
					Type:       "string",
					Secret:     true,
					Required:   true,
					Default:    "default-secret",
					Value:      "configured-secret",
					HasValue:   true,
					EnumValues: []string{"configured-secret", "alternate-secret"},
					ConstValue: "constant-secret",
				},
			},
		},
	}
	handler := New(leakyService, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(handler)
	defer testServer.Close()
	client, _ := authenticatedClient(t, testServer.URL)

	response := doJSON(t, client, http.MethodGet, testServer.URL+"/api/v1/servers/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/startup", "", nil)
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("startup status = %d, want 200", response.StatusCode)
	}
	var payload struct {
		Data struct {
			Variables []map[string]json.RawMessage `json:"variables"`
		} `json:"data"`
	}
	decodeResponse(t, response, &payload)
	if len(payload.Data.Variables) != 1 {
		t.Fatalf("startup variables = %d, want 1", len(payload.Data.Variables))
	}
	secret := payload.Data.Variables[0]
	for _, field := range []string{"value", "default", "constValue", "enumValues"} {
		if _, exposed := secret[field]; exposed {
			t.Errorf("secret variable exposed %q member: %+v", field, secret)
		}
	}
	for _, field := range []string{"key", "type", "secret", "required", "hasValue"} {
		if _, declared := secret[field]; !declared {
			t.Errorf("secret variable omitted declaration member %q: %+v", field, secret)
		}
	}

	original := leakyService.startup.Variables[0]
	if original.Default != "default-secret" || original.Value != "configured-secret" || original.ConstValue != "constant-secret" || len(original.EnumValues) != 2 {
		t.Fatalf("HTTP redaction mutated service startup value: %+v", original)
	}
}

type startupResponseService struct {
	ControlPlane
	startup domain.Startup
}

func (s *startupResponseService) Startup(string) (domain.Startup, error) {
	return s.startup, nil
}

type startupUpdateSpy struct {
	ControlPlane
	updateCalls int
}

func (s *startupUpdateSpy) UpdateStartup(serverID string, updates map[string]any, expectedGeneration int64, idempotencyKey string, actor domain.User) (domain.Operation, error) {
	s.updateCalls++
	return s.ControlPlane.UpdateStartup(serverID, updates, expectedGeneration, idempotencyKey, actor)
}

type authorizeBarrierService struct {
	ControlPlane
	authorized chan struct{}
	once       sync.Once
}

func (s *authorizeBarrierService) AuthorizeServer(userID string, serverID string, permission string) error {
	err := s.ControlPlane.AuthorizeServer(userID, serverID, permission)
	if err == nil {
		s.once.Do(func() { close(s.authorized) })
	}
	return err
}

func TestPortConflictMapsToHTTPConflict(t *testing.T) {
	if got := statusFor("PORT_CONFLICT"); got != http.StatusConflict {
		t.Fatalf("PORT_CONFLICT status = %d, want %d", got, http.StatusConflict)
	}
}

func authenticatedClient(t *testing.T, serverURL string) (*http.Client, domain.SessionView) {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	response := doJSON(t, client, http.MethodPost, serverURL+"/api/v1/auth/login", `{"email":"admin@gugu.local","password":"gugu-dev-2026"}`, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", response.StatusCode)
	}
	var payload struct {
		Data domain.SessionView `json:"data"`
	}
	decodeResponse(t, response, &payload)
	return client, payload.Data
}

func doJSON(t *testing.T, client *http.Client, method string, url string, body string, headers map[string]string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeResponse(t *testing.T, response *http.Response, target any) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}
