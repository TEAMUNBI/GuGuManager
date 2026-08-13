package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/lib/pq"

	"github.com/gugumanager/gugumanager/internal/agentrpc"
	"github.com/gugumanager/gugumanager/internal/config"
	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/store"
)

func TestNewLoggerHonorsConfiguredLevelAndFormat(t *testing.T) {
	t.Run("debug json", func(t *testing.T) {
		var output bytes.Buffer
		logger := newLogger(&output, "debug", "json")
		logger.Debug("diagnostic", "component", "control-plane")

		var event map[string]any
		if err := json.Unmarshal(output.Bytes(), &event); err != nil {
			t.Fatalf("logger output is not JSON: %v; output=%q", err, output.String())
		}
		if event["level"] != "DEBUG" || event["msg"] != "diagnostic" || event["component"] != "control-plane" {
			t.Fatalf("JSON log event = %+v", event)
		}
	})

	t.Run("warn text", func(t *testing.T) {
		var output bytes.Buffer
		logger := newLogger(&output, "warn", "text")
		logger.Info("filtered")
		logger.Warn("visible", "component", "control-plane")

		line := output.String()
		if strings.Contains(line, "filtered") {
			t.Fatalf("info event bypassed warn filter: %q", line)
		}
		if !strings.Contains(line, "level=WARN") || !strings.Contains(line, "msg=visible") || !strings.Contains(line, "component=control-plane") {
			t.Fatalf("text log event = %q", line)
		}
	})
}

// TestBuildServiceProduction verifies that the production branch connects to
// PostgreSQL, runs migrations, and returns a *store.Postgres with the
// bootstrap token wired so that SetupAdmin rejects unknown tokens.
// It requires a real test database and skips when none is available.
func TestBuildServiceProduction(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("GUGU_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("GUGU_TEST_DATABASE_URL required, must end in _test")
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse GUGU_TEST_DATABASE_URL: %v", err)
	}
	databaseName := strings.Trim(parsed.Path, "/")
	if !strings.HasSuffix(databaseName, "_test") {
		t.Skipf("refusing test database %q: name must end in _test", databaseName)
	}
	// lib/pq defaults to sslmode=require while the local test instance has no
	// SSL configured; fall back to plaintext unless the DSN already pins a mode.
	query := parsed.Query()
	if query.Get("sslmode") == "" {
		query.Set("sslmode", "disable")
		parsed.RawQuery = query.Encode()
		dsn = parsed.String()
	}
	probe, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open probe database: %v", err)
	}
	if err := probe.Ping(); err != nil {
		probe.Close()
		t.Skipf("database unavailable: %v", err)
	}
	probe.Close()

	tokenFile := filepath.Join(t.TempDir(), "bootstrap.token")
	if err := os.WriteFile(tokenFile, []byte("production-bootstrap-token-000000000000000000000000"), 0o600); err != nil {
		t.Fatalf("write bootstrap token file: %v", err)
	}
	encryptionKeyFile := filepath.Join(t.TempDir(), "encryption.key")
	if err := os.WriteFile(encryptionKeyFile, []byte("test-only-production-encryption-key"), 0o600); err != nil {
		t.Fatalf("write encryption key file: %v", err)
	}
	t.Setenv("GUGU_AGENT_TOKEN", "production-agent-token-000000000000000000000000")

	cfg := config.Config{
		Environment:        config.Production,
		HTTPAddr:           "127.0.0.1:8080",
		WebRoot:            "web/dist",
		PublicURL:          "https://panel.example.com",
		DatabaseURL:        dsn,
		BootstrapTokenFile: tokenFile,
		EncryptionKeyFile:  encryptionKeyFile,
		TLSTerminated:      true,
		LogLevel:           "info",
		LogFormat:          "json",
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service, err := buildService(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("buildService(production): %v", err)
	}
	pg, ok := service.(*store.Postgres)
	if !ok {
		t.Fatalf("buildService(production) returned %T, want *store.Postgres", service)
	}
	t.Cleanup(func() { _ = pg.Close() })

	// The bootstrap token must be active: SetupAdmin with a wrong token is
	// rejected before any write happens.
	if _, err := pg.SetupAdmin(domain.SetupAdminInput{
		BootstrapToken: "definitely-not-the-bootstrap-token",
		Email:          "admin@example.com",
		DisplayName:    "Admin",
		Password:       "password-123456",
	}); err == nil || !strings.Contains(err.Error(), "SETUP_TOKEN_INVALID") {
		t.Fatalf("SetupAdmin with wrong bootstrap token err = %v, want SETUP_TOKEN_INVALID", err)
	}
}

// TestBuildServiceDevelopment verifies the development branch still returns
// the in-memory adapter.
func TestBuildServiceDevelopment(t *testing.T) {
	cfg := config.Config{
		Environment:      config.Development,
		HTTPAddr:         "127.0.0.1:8080",
		WebRoot:          "web/dist",
		AdminEmail:       "admin@gugu.local",
		AdminPassword:    "gugu-dev-2026",
		AgentToken:       "gugu-agent-dev-token-1234",
		DevDataRoot:      t.TempDir(),
		OperationLatency: 0,
		LogLevel:         "info",
		LogFormat:        "json",
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service, err := buildService(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("buildService(development): %v", err)
	}
	if _, ok := service.(*store.Memory); !ok {
		t.Fatalf("buildService(development) returned %T, want *store.Memory", service)
	}
}

func TestControlPlaneHTTPOptionsDoNotInstallTypedNilDispatcher(t *testing.T) {
	options := controlPlaneHTTPOptions(config.Development, nil)
	if len(options) != 1 {
		t.Fatalf("development HTTP options = %d, want only environment option", len(options))
	}

	agentServer := &agentrpc.Server{}
	options = controlPlaneHTTPOptions(config.Production, agentServer)
	if len(options) != 2 {
		t.Fatalf("production HTTP options = %d, want environment and dispatcher options", len(options))
	}
}
