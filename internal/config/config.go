package config

import (
	"errors"
	"net"
	"net/mail"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	Development = "development"
	Production  = "production"
)

var ErrProductionAdapterUnavailable = errors.New("production adapter is not implemented")

type Config struct {
	Environment       string
	HTTPAddr          string
	WebRoot           string
	AdminEmail        string
	AdminPassword     string
	AgentToken        string
	DevBootstrapToken string
	OperationLatency  time.Duration
	DevDataRoot       string

	PublicURL          string
	DatabaseURL        string
	RedisURL           string
	SessionKeyFile     string
	EncryptionKeyFile  string
	AgentCACertFile    string
	AgentCAKeyFile     string
	BootstrapTokenFile string
	TLSTerminated      bool
	LogLevel           string
	LogFormat          string
}

type ValidationProblem struct {
	Field  string
	Reason string
}

type ValidationError struct {
	Problems []ValidationProblem
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Problems))
	for _, problem := range e.Problems {
		parts = append(parts, problem.Field+" "+problem.Reason)
	}
	return "invalid configuration: " + strings.Join(parts, "; ")
}

func (e *ValidationError) Has(field string) bool {
	for _, problem := range e.Problems {
		if problem.Field == field {
			return true
		}
	}
	return false
}

func Load() (Config, error) {
	return load(os.LookupEnv)
}

func load(lookup func(string) (string, bool)) (Config, error) {
	environment := lookupTrimmed(lookup, "GUGU_ENVIRONMENT", Development)
	cfg := Config{
		Environment:        strings.ToLower(environment),
		HTTPAddr:           lookupTrimmed(lookup, "GUGU_HTTP_ADDR", "127.0.0.1:8080"),
		WebRoot:            lookupTrimmed(lookup, "GUGU_WEB_ROOT", "web/dist"),
		PublicURL:          lookupTrimmed(lookup, "GUGU_PUBLIC_URL", ""),
		DatabaseURL:        lookupSecret(lookup, "GUGU_DATABASE_URL"),
		RedisURL:           lookupSecret(lookup, "GUGU_REDIS_URL"),
		SessionKeyFile:     lookupTrimmed(lookup, "GUGU_SESSION_KEY_FILE", ""),
		EncryptionKeyFile:  lookupTrimmed(lookup, "GUGU_ENCRYPTION_KEY_FILE", ""),
		AgentCACertFile:    lookupTrimmed(lookup, "GUGU_AGENT_CA_CERT_FILE", ""),
		AgentCAKeyFile:     lookupTrimmed(lookup, "GUGU_AGENT_CA_KEY_FILE", ""),
		BootstrapTokenFile: lookupTrimmed(lookup, "GUGU_BOOTSTRAP_TOKEN_FILE", ""),
		LogLevel:           strings.ToLower(lookupTrimmed(lookup, "GUGU_LOG_LEVEL", "info")),
		LogFormat:          strings.ToLower(lookupTrimmed(lookup, "GUGU_LOG_FORMAT", "json")),
	}

	problems := []ValidationProblem{}
	if cfg.Environment == Development {
		cfg.AdminEmail = strings.ToLower(lookupTrimmed(lookup, "GUGU_DEV_ADMIN_EMAIL", "admin@gugu.local"))
		cfg.AdminPassword = lookupSecretDefault(lookup, "GUGU_DEV_ADMIN_PASSWORD", "gugu-dev-2026")
		cfg.AgentToken = lookupSecretDefault(lookup, "GUGU_DEV_AGENT_TOKEN", "gugu-agent-dev-token")
		cfg.DevBootstrapToken = lookupSecret(lookup, "GUGU_DEV_BOOTSTRAP_TOKEN")
		cfg.OperationLatency = 850 * time.Millisecond
		cfg.DevDataRoot = lookupTrimmed(lookup, "GUGU_DEV_DATA_ROOT", "var/development/servers")
		if raw, ok := nonEmpty(lookup, "GUGU_DEV_OPERATION_LATENCY"); ok {
			parsed, err := time.ParseDuration(strings.TrimSpace(raw))
			if err != nil {
				problems = appendProblem(problems, "GUGU_DEV_OPERATION_LATENCY", "must be a valid duration")
			} else {
				cfg.OperationLatency = parsed
			}
		}
	} else {
		cfg.AdminEmail = strings.ToLower(lookupTrimmed(lookup, "GUGU_DEV_ADMIN_EMAIL", ""))
		cfg.AdminPassword = lookupSecret(lookup, "GUGU_DEV_ADMIN_PASSWORD")
		cfg.AgentToken = lookupSecret(lookup, "GUGU_DEV_AGENT_TOKEN")
		if _, ok := nonEmpty(lookup, "GUGU_DEV_OPERATION_LATENCY"); ok {
			problems = appendProblem(problems, "GUGU_DEV_OPERATION_LATENCY", "is only allowed in development")
		}
		if _, ok := nonEmpty(lookup, "GUGU_DEV_DATA_ROOT"); ok {
			problems = appendProblem(problems, "GUGU_DEV_DATA_ROOT", "is only allowed in development")
		}
		if _, ok := nonEmpty(lookup, "GUGU_DEV_BOOTSTRAP_TOKEN"); ok {
			problems = appendProblem(problems, "GUGU_DEV_BOOTSTRAP_TOKEN", "is only allowed in development")
		}
	}

	if raw, ok := nonEmpty(lookup, "GUGU_TLS_TERMINATED"); ok {
		parsed, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			problems = appendProblem(problems, "GUGU_TLS_TERMINATED", "must be true or false")
		} else {
			cfg.TLSTerminated = parsed
		}
	}

	for _, problem := range cfg.validationProblems() {
		problems = appendProblem(problems, problem.Field, problem.Reason)
	}
	if len(problems) > 0 {
		return Config{}, &ValidationError{Problems: problems}
	}
	return cfg, nil
}

func (cfg Config) Validate() error {
	problems := cfg.validationProblems()
	if len(problems) == 0 {
		return nil
	}
	return &ValidationError{Problems: problems}
}

func (cfg Config) validationProblems() []ValidationProblem {
	problems := []ValidationProblem{}
	if cfg.Environment != Development && cfg.Environment != Production {
		problems = appendProblem(problems, "GUGU_ENVIRONMENT", "must be development or production")
	}
	if !validHTTPAddress(cfg.HTTPAddr) {
		problems = appendProblem(problems, "GUGU_HTTP_ADDR", "must be a valid host:port address")
	}
	if strings.TrimSpace(cfg.WebRoot) == "" {
		problems = appendProblem(problems, "GUGU_WEB_ROOT", "must not be empty")
	}
	if !oneOf(cfg.LogLevel, "debug", "info", "warn", "error") {
		problems = appendProblem(problems, "GUGU_LOG_LEVEL", "must be debug, info, warn, or error")
	}
	if !oneOf(cfg.LogFormat, "json", "text") {
		problems = appendProblem(problems, "GUGU_LOG_FORMAT", "must be json or text")
	}

	switch cfg.Environment {
	case Development:
		parsedEmail, err := mail.ParseAddress(cfg.AdminEmail)
		if err != nil || parsedEmail.Address != cfg.AdminEmail || len([]rune(cfg.AdminEmail)) > 254 {
			problems = appendProblem(problems, "GUGU_DEV_ADMIN_EMAIL", "must be a valid email address")
		}
		passwordLength := len([]rune(cfg.AdminPassword))
		if passwordLength < 8 || passwordLength > 1024 {
			problems = appendProblem(problems, "GUGU_DEV_ADMIN_PASSWORD", "must contain between 8 and 1024 characters")
		}
		if len([]rune(cfg.AgentToken)) < 16 {
			problems = appendProblem(problems, "GUGU_DEV_AGENT_TOKEN", "must contain at least 16 characters")
		}
		if cfg.DevBootstrapToken != "" {
			length := len([]rune(cfg.DevBootstrapToken))
			if length < 32 || length > 256 {
				problems = appendProblem(problems, "GUGU_DEV_BOOTSTRAP_TOKEN", "must contain between 32 and 256 characters")
			}
		}
		if cfg.OperationLatency < 0 || cfg.OperationLatency > time.Minute {
			problems = appendProblem(problems, "GUGU_DEV_OPERATION_LATENCY", "must be between 0 and 1m")
		}
		if strings.TrimSpace(cfg.DevDataRoot) == "" {
			problems = appendProblem(problems, "GUGU_DEV_DATA_ROOT", "must not be empty")
		}
	case Production:
		if cfg.AdminEmail != "" {
			problems = appendProblem(problems, "GUGU_DEV_ADMIN_EMAIL", "is not allowed in production")
		}
		if cfg.AdminPassword != "" {
			problems = appendProblem(problems, "GUGU_DEV_ADMIN_PASSWORD", "is not allowed in production")
		}
		if cfg.AgentToken != "" {
			problems = appendProblem(problems, "GUGU_DEV_AGENT_TOKEN", "is not allowed in production")
		}
		if cfg.DevBootstrapToken != "" {
			problems = appendProblem(problems, "GUGU_DEV_BOOTSTRAP_TOKEN", "is not allowed in production")
		}
		if !validPublicURL(cfg.PublicURL) {
			problems = appendProblem(problems, "GUGU_PUBLIC_URL", "must be an absolute HTTPS URL without credentials, query, or fragment")
		}
		if !validDatabaseURL(cfg.DatabaseURL) {
			problems = appendProblem(problems, "GUGU_DATABASE_URL", "must be a PostgreSQL URL with a host and database name")
		}
		if cfg.RedisURL != "" && !validRedisURL(cfg.RedisURL) {
			problems = appendProblem(problems, "GUGU_REDIS_URL", "must be a redis or rediss URL")
		}
		for _, secretFile := range []struct {
			field string
			path  string
		}{
			{field: "GUGU_SESSION_KEY_FILE", path: cfg.SessionKeyFile},
			{field: "GUGU_ENCRYPTION_KEY_FILE", path: cfg.EncryptionKeyFile},
			{field: "GUGU_AGENT_CA_CERT_FILE", path: cfg.AgentCACertFile},
			{field: "GUGU_AGENT_CA_KEY_FILE", path: cfg.AgentCAKeyFile},
		} {
			if !validSecretFile(secretFile.path) {
				problems = appendProblem(problems, secretFile.field, "must reference a readable, non-empty regular file")
			}
		}
		if cfg.BootstrapTokenFile != "" && !validSecretFile(cfg.BootstrapTokenFile) {
			problems = appendProblem(problems, "GUGU_BOOTSTRAP_TOKEN_FILE", "must reference a readable, non-empty regular file")
		}
		if !cfg.TLSTerminated {
			problems = appendProblem(problems, "GUGU_TLS_TERMINATED", "must be explicitly true in production")
		}
	}
	return problems
}

func validHTTPAddress(value string) bool {
	_, port, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	parsed, err := strconv.Atoi(port)
	return err == nil && parsed >= 1 && parsed <= 65535
}

func validPublicURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func validDatabaseURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return false
	}
	return parsed.Hostname() != "" && strings.Trim(parsed.Path, "/") != ""
}

func validRedisURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && (parsed.Scheme == "redis" || parsed.Scheme == "rediss") && parsed.Hostname() != ""
}

func validSecretFile(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	return file.Close() == nil
}

func appendProblem(problems []ValidationProblem, field string, reason string) []ValidationProblem {
	for _, existing := range problems {
		if existing.Field == field {
			return problems
		}
	}
	return append(problems, ValidationProblem{Field: field, Reason: reason})
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func lookupTrimmed(lookup func(string) (string, bool), key string, fallback string) string {
	if current, ok := nonEmpty(lookup, key); ok {
		return strings.TrimSpace(current)
	}
	return fallback
}

func lookupSecret(lookup func(string) (string, bool), key string) string {
	if current, ok := nonEmpty(lookup, key); ok {
		return current
	}
	return ""
}

func lookupSecretDefault(lookup func(string) (string, bool), key string, fallback string) string {
	if current, ok := nonEmpty(lookup, key); ok {
		return current
	}
	return fallback
}

func nonEmpty(lookup func(string) (string, bool), key string) (string, bool) {
	current, ok := lookup(key)
	return current, ok && strings.TrimSpace(current) != ""
}
