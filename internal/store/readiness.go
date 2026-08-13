package store

import (
	"context"
	"fmt"
	"time"
)

// Readiness performs live checks against the production facts that must hold
// before this replica accepts traffic: its DB pool is reachable, the canonical
// schema is fully applied, and a startup-secret cipher/keyring is loaded.
func (s *Postgres) Readiness(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if err := s.db.PingContext(checkCtx); err != nil {
		return fmt.Errorf("database unavailable: %w", err)
	}

	s.mu.RLock()
	required := append([]string(nil), s.requiredMigrationVersions...)
	keyReady := s.secretKeyring != nil || s.secretCipher != nil
	s.mu.RUnlock()
	if len(required) == 0 {
		return fmt.Errorf("canonical migration plan is not configured")
	}
	if !keyReady {
		return fmt.Errorf("startup secret encryption key is not loaded")
	}

	rows, err := s.db.QueryContext(checkCtx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("query schema migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	applied := make(map[string]struct{}, len(required))
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return fmt.Errorf("scan schema migration: %w", err)
		}
		applied[version] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate schema migrations: %w", err)
	}
	for _, version := range required {
		if _, ok := applied[version]; !ok {
			return fmt.Errorf("canonical migration %s is not applied", version)
		}
	}
	return nil
}
