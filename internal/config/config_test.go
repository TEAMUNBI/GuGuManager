package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadDevelopmentDefaults(t *testing.T) {
	cfg, err := load(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Environment != Development {
		t.Fatalf("Environment = %q, want %q", cfg.Environment, Development)
	}
	if cfg.HTTPAddr != "127.0.0.1:8080" {
		t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.AdminEmail != "admin@gugu.local" || cfg.AdminPassword != "gugu-dev-2026" {
		t.Fatalf("development credentials = %q / %q", cfg.AdminEmail, cfg.AdminPassword)
	}
	if cfg.OperationLatency != 850*time.Millisecond {
		t.Fatalf("OperationLatency = %s", cfg.OperationLatency)
	}
	if cfg.DevDataRoot != "var/development/servers" {
		t.Fatalf("DevDataRoot = %q", cfg.DevDataRoot)
	}
}

func TestLoadDevelopmentBootstrapModeRequiresHighEntropyToken(t *testing.T) {
	token := strings.Repeat("b", 32)
	cfg, err := load(mapLookup(map[string]string{"GUGU_DEV_BOOTSTRAP_TOKEN": token}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DevBootstrapToken != token {
		t.Fatalf("DevBootstrapToken = %q", cfg.DevBootstrapToken)
	}

	_, err = load(mapLookup(map[string]string{"GUGU_DEV_BOOTSTRAP_TOKEN": "too-short"}))
	var validation *ValidationError
	if !errors.As(err, &validation) || !validation.Has("GUGU_DEV_BOOTSTRAP_TOKEN") {
		t.Fatalf("short bootstrap token error = %v", err)
	}
}

func TestLoadRejectsMalformedDevelopmentValues(t *testing.T) {
	tests := []struct {
		name  string
		env   map[string]string
		field string
	}{
		{name: "environment", env: map[string]string{"GUGU_ENVIRONMENT": "staging"}, field: "GUGU_ENVIRONMENT"},
		{name: "http address", env: map[string]string{"GUGU_HTTP_ADDR": "8080"}, field: "GUGU_HTTP_ADDR"},
		{name: "admin email", env: map[string]string{"GUGU_DEV_ADMIN_EMAIL": "not-an-email"}, field: "GUGU_DEV_ADMIN_EMAIL"},
		{name: "admin password", env: map[string]string{"GUGU_DEV_ADMIN_PASSWORD": "short"}, field: "GUGU_DEV_ADMIN_PASSWORD"},
		{name: "agent token", env: map[string]string{"GUGU_DEV_AGENT_TOKEN": "short"}, field: "GUGU_DEV_AGENT_TOKEN"},
		{name: "operation latency", env: map[string]string{"GUGU_DEV_OPERATION_LATENCY": "eventually"}, field: "GUGU_DEV_OPERATION_LATENCY"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := load(mapLookup(test.env))
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("error = %v, want *ValidationError", err)
			}
			if !validation.Has(test.field) {
				t.Fatalf("validation fields = %+v, want %s", validation.Problems, test.field)
			}
		})
	}
}

func TestProductionValidationReportsAllRequiredGateFields(t *testing.T) {
	_, err := load(mapLookup(map[string]string{"GUGU_ENVIRONMENT": Production}))

	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
	for _, field := range []string{
		"GUGU_PUBLIC_URL",
		"GUGU_DATABASE_URL",
		"GUGU_SESSION_KEY_FILE",
		"GUGU_ENCRYPTION_KEY_FILE",
		"GUGU_AGENT_CA_CERT_FILE",
		"GUGU_AGENT_CA_KEY_FILE",
		"GUGU_TLS_TERMINATED",
	} {
		if !validation.Has(field) {
			t.Errorf("validation fields = %+v, missing %s", validation.Problems, field)
		}
	}
}

func TestCompleteProductionConfigLoadsSuccessfully(t *testing.T) {
	env := completeProductionEnv(t)

	cfg, err := load(mapLookup(env))
	if err != nil {
		t.Fatalf("complete production config failed to load: %v", err)
	}
	if cfg.Environment != Production {
		t.Fatalf("Environment = %q, want %q", cfg.Environment, Production)
	}
}

func TestProductionConfigRejectsDevelopmentCredentials(t *testing.T) {
	env := completeProductionEnv(t)
	env["GUGU_DEV_ADMIN_PASSWORD"] = "must-not-be-used-in-production"

	_, err := load(mapLookup(env))
	var validation *ValidationError
	if !errors.As(err, &validation) || !validation.Has("GUGU_DEV_ADMIN_PASSWORD") {
		t.Fatalf("error = %v, want development credential validation error", err)
	}
}

func TestProductionConfigRejectsDevelopmentBootstrapToken(t *testing.T) {
	env := completeProductionEnv(t)
	env["GUGU_DEV_BOOTSTRAP_TOKEN"] = strings.Repeat("b", 32)

	_, err := load(mapLookup(env))
	var validation *ValidationError
	if !errors.As(err, &validation) || !validation.Has("GUGU_DEV_BOOTSTRAP_TOKEN") {
		t.Fatalf("error = %v, want development bootstrap token validation error", err)
	}
}

func TestValidateProductionConfigRejectsDevelopmentBootstrapToken(t *testing.T) {
	env := completeProductionEnv(t)
	cfg := Config{
		Environment:       Production,
		HTTPAddr:          env["GUGU_HTTP_ADDR"],
		WebRoot:           env["GUGU_WEB_ROOT"],
		DevBootstrapToken: strings.Repeat("b", 32),
		PublicURL:         env["GUGU_PUBLIC_URL"],
		DatabaseURL:       env["GUGU_DATABASE_URL"],
		RedisURL:          env["GUGU_REDIS_URL"],
		SessionKeyFile:    env["GUGU_SESSION_KEY_FILE"],
		EncryptionKeyFile: env["GUGU_ENCRYPTION_KEY_FILE"],
		AgentCACertFile:   env["GUGU_AGENT_CA_CERT_FILE"],
		AgentCAKeyFile:    env["GUGU_AGENT_CA_KEY_FILE"],
		TLSTerminated:     true,
		LogLevel:          "info",
		LogFormat:         "json",
	}

	err := cfg.Validate()
	var validation *ValidationError
	if !errors.As(err, &validation) || !validation.Has("GUGU_DEV_BOOTSTRAP_TOKEN") {
		t.Fatalf("Validate error = %v, want GUGU_DEV_BOOTSTRAP_TOKEN validation error", err)
	}
}

func TestProductionConfigValidatesURLAndSecretFiles(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value string
	}{
		{name: "public URL requires HTTPS", field: "GUGU_PUBLIC_URL", value: "http://panel.example.test"},
		{name: "database URL requires PostgreSQL", field: "GUGU_DATABASE_URL", value: "mysql://db.internal/gugu"},
		{name: "secret file must exist", field: "GUGU_SESSION_KEY_FILE", value: filepath.Join(t.TempDir(), "missing.key")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := completeProductionEnv(t)
			env[test.field] = test.value
			_, err := load(mapLookup(env))
			var validation *ValidationError
			if !errors.As(err, &validation) || !validation.Has(test.field) {
				t.Fatalf("error = %v, want validation error for %s", err, test.field)
			}
		})
	}
}

func TestProductionValidationNeverIncludesSecretValues(t *testing.T) {
	env := completeProductionEnv(t)
	secret := "postgres://operator:do-not-log-me@/"
	env["GUGU_DATABASE_URL"] = secret

	_, err := load(mapLookup(env))
	if err == nil {
		t.Fatal("expected invalid database URL")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "do-not-log-me") {
		t.Fatalf("validation error leaked a secret: %v", err)
	}
}

func completeProductionEnv(t *testing.T) map[string]string {
	t.Helper()
	directory := t.TempDir()
	files := map[string]string{
		"GUGU_SESSION_KEY_FILE":    "session.key",
		"GUGU_ENCRYPTION_KEY_FILE": "encryption.key",
		"GUGU_AGENT_CA_CERT_FILE":  "agent-ca.crt",
		"GUGU_AGENT_CA_KEY_FILE":   "agent-ca.key",
	}
	for key, name := range files {
		filePath := filepath.Join(directory, name)
		if err := os.WriteFile(filePath, []byte("test-only-non-empty-material"), 0o600); err != nil {
			t.Fatal(err)
		}
		files[key] = filePath
	}

	return map[string]string{
		"GUGU_ENVIRONMENT":         Production,
		"GUGU_HTTP_ADDR":           "0.0.0.0:8080",
		"GUGU_WEB_ROOT":            "web/dist",
		"GUGU_PUBLIC_URL":          "https://panel.example.test",
		"GUGU_DATABASE_URL":        "postgres://gugu:secret@db.internal/gugu?sslmode=require",
		"GUGU_REDIS_URL":           "redis://cache.internal:6379/0",
		"GUGU_SESSION_KEY_FILE":    files["GUGU_SESSION_KEY_FILE"],
		"GUGU_ENCRYPTION_KEY_FILE": files["GUGU_ENCRYPTION_KEY_FILE"],
		"GUGU_AGENT_CA_CERT_FILE":  files["GUGU_AGENT_CA_CERT_FILE"],
		"GUGU_AGENT_CA_KEY_FILE":   files["GUGU_AGENT_CA_KEY_FILE"],
		"GUGU_TLS_TERMINATED":      "true",
		"GUGU_LOG_LEVEL":           "info",
		"GUGU_LOG_FORMAT":          "json",
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
