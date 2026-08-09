package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "github.com/lib/pq"
)

// Postgres implements the Store interface with PostgreSQL persistence.
// It replaces the in-memory development adapter for production deployments.
type Postgres struct {
	db                   *sql.DB
	mu                   sync.RWMutex
	environment          string
	agentToken           [32]byte
	bootstrapTokenDigest [32]byte
	fileRoot             string
	fileMutationGates    sync.Map
}

// NewPostgres creates a new PostgreSQL-backed store.
// The database connection string should include sslmode and appropriate timeouts.
func NewPostgres(ctx context.Context, dsn string, environment string, agentToken string, fileRoot string) (*Postgres, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(1 * time.Minute)

	// Verify connection
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	var agentTokenBytes [32]byte = sha256.Sum256([]byte(agentToken))

	store := &Postgres{
		db:          db,
		environment: environment,
		agentToken:  agentTokenBytes,
		fileRoot:    fileRoot,
	}

	return store, nil
}

// SetBootstrapToken 记录 bootstrap token 的 SHA-256 摘要，供 SetupAdmin 校验。
// 仅用于首次初始化；生产从 GUGU_BOOTSTRAP_TOKEN_FILE 读取后调用。
func (s *Postgres) SetBootstrapToken(token string) {
	if token == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bootstrapTokenDigest = sha256.Sum256([]byte(token))
}

// Close closes the database connection pool.
func (s *Postgres) Close() error {
	return s.db.Close()
}

// Environment returns the runtime environment identifier.
func (s *Postgres) Environment() string {
	return s.environment
}
