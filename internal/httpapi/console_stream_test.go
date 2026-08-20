package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/gugumanager/gugumanager/internal/store"
)

func wsDial(t *testing.T, serverURL string, path string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + path
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatalf("dial ws: %v", err)
	}
	return conn
}

func TestConsoleStreamPushesHistoryThenLiveLines(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(handler)
	defer testServer.Close()
	client, session := authenticatedClient(t, testServer.URL)
	issued := doJSON(t, client, http.MethodPost, testServer.URL+"/api/v1/servers/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/console-tokens", "", map[string]string{"X-CSRF-Token": session.CSRFToken})
	if issued.StatusCode != http.StatusCreated {
		issued.Body.Close()
		t.Fatalf("issue console token status = %d, want 201", issued.StatusCode)
	}
	var credential struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	decodeResponse(t, issued, &credential)

	conn := wsDial(t, testServer.URL, "/api/v1/servers/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/console/stream?token="+credential.Data.Token)
	defer conn.Close()
	wsURL := "ws" + strings.TrimPrefix(testServer.URL, "http") + "/api/v1/servers/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/console/stream?token=" + credential.Data.Token
	if replay, response, err := websocket.DefaultDialer.Dial(wsURL, nil); err == nil {
		replay.Close()
		t.Fatal("expected consumed console token replay to fail")
	} else if response != nil {
		response.Body.Close()
	}

	// 第一帧必须是历史快照。
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var frame consoleStreamFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read history frame: %v", err)
	}
	if frame.Type != "snapshot" || frame.Version != 1 {
		t.Fatalf("first frame = %+v, want v1 snapshot", frame)
	}

	// 发送命令产生两行日志，应经 WS 实时推送（替代轮询）。
	response := doJSON(t, client, http.MethodPost, testServer.URL+"/api/v1/servers/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/console/commands", `{"command":"list"}`, map[string]string{"X-CSRF-Token": session.CSRFToken})
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("console command status = %d, want 202", response.StatusCode)
	}

	gotLive := false
	for i := 0; i < 4; i++ {
		if err := conn.ReadJSON(&frame); err != nil {
			break
		}
		if frame.Type == "line" {
			gotLive = true
			break
		}
	}
	if !gotLive {
		t.Fatal("expected a live line frame after sending a console command")
	}
}

func TestConsoleStreamRequiresAuthorization(t *testing.T) {
	// 未登录的 WS 握手应被拒绝（h.auth 在升级前拦截）。
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(handler)
	defer testServer.Close()

	wsURL := "ws" + strings.TrimPrefix(testServer.URL, "http") + "/api/v1/servers/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/console/stream"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		conn.Close()
		t.Fatal("expected unauthenticated ws handshake to fail")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated ws status = %d, want 401", resp.StatusCode)
	}
}

func TestConsoleStreamRejectsCrossOriginBeforeConsumingToken(t *testing.T) {
	service := store.NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()
	handler := New(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer := httptest.NewServer(handler)
	defer testServer.Close()
	client, session := authenticatedClient(t, testServer.URL)
	issued := doJSON(t, client, http.MethodPost, testServer.URL+"/api/v1/servers/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/console-tokens", "", map[string]string{"X-CSRF-Token": session.CSRFToken})
	var credential struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	decodeResponse(t, issued, &credential)
	path := "/api/v1/servers/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/console/stream?token=" + credential.Data.Token
	wsURL := "ws" + strings.TrimPrefix(testServer.URL, "http") + path
	header := http.Header{"Origin": []string{"https://attacker.invalid"}}
	if conn, response, err := websocket.DefaultDialer.Dial(wsURL, header); err == nil {
		conn.Close()
		t.Fatal("cross-origin console connection succeeded")
	} else if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin response = %+v, err=%v", response, err)
	}
	// The rejected handshake must not burn the one-time token.
	conn := wsDial(t, testServer.URL, path)
	conn.Close()
}
