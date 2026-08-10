package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/gugumanager/gugumanager/internal/store"
)

// wsDial 用已登录客户端的会话 cookie 建立控制台实时流连接。
func wsDial(t *testing.T, serverURL string, client *http.Client, path string) *websocket.Conn {
	t.Helper()
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	var cookieValue string
	for _, cookie := range client.Jar.Cookies(u) {
		if cookie.Name == sessionCookie {
			cookieValue = cookie.Value
			break
		}
	}
	if cookieValue == "" {
		t.Fatal("no session cookie in jar")
	}
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + path
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Cookie": []string{sessionCookie + "=" + cookieValue}})
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

	conn := wsDial(t, testServer.URL, client, "/api/v1/servers/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/console/stream")
	defer conn.Close()

	// 第一帧必须是历史快照。
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var frame consoleStreamFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read history frame: %v", err)
	}
	if frame.Type != "history" {
		t.Fatalf("first frame type = %q, want history", frame.Type)
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
