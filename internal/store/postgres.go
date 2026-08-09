package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "github.com/lib/pq"
)

// Postgres implements the Store interface with PostgreSQL persistence.
// It replaces the in-memory development adapter for production deployments.
type Postgres struct {
	db              *sql.DB
	mu              sync.RWMutex
	environment     string
	agentToken      [32]byte
	fileRoot        string
	fileMutationGates sync.Map
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

	var agentTokenBytes [32]byte
	copy(agentTokenBytes[:], agentToken)

	store := &Postgres{
		db:          db,
		environment: environment,
		agentToken:  agentTokenBytes,
		fileRoot:    fileRoot,
	}

	return store, nil
}

// Close closes the database connection pool.
func (s *Postgres) Close() error {
	return s.db.Close()
}

// Environment returns the runtime environment identifier.
func (s *Postgres) Environment() string {
	return s.environment
}
