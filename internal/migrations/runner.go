package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Migration is one canonical up/down SQL pair from the migrations directory.
type Migration struct {
	Version    int
	VersionKey string
	Name       string
	Up         []byte
	Down       []byte
}

var migrationFile = regexp.MustCompile(`^([0-9]{6})_([a-z0-9][a-z0-9_-]*)\.(up|down)\.sql$`)

// LoadMigrations reads dir and returns its canonical migration pairs sorted by
// ascending version. Filenames must follow the NNNNNN_name.up.sql / .down.sql
// convention with contiguous, duplicate-free versions, and every pair must
// have both an up and a down migration.
func LoadMigrations(dir string) ([]Migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migration directory: %w", err)
	}

	type pending struct {
		Version    int
		VersionKey string
		Name       string
		Up         []byte
		Down       []byte
		hasUp      bool
		hasDown    bool
	}
	byVersion := map[int]*pending{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(name), ".sql") {
			continue
		}
		parts := migrationFile.FindStringSubmatch(name)
		if parts == nil {
			return nil, fmt.Errorf("non-canonical migration filename %q", name)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("migration %q must be a regular file, not a symlink", name)
		}

		version, err := strconv.Atoi(parts[1])
		if err != nil || version < 1 {
			return nil, fmt.Errorf("invalid migration version %q", parts[1])
		}
		item, exists := byVersion[version]
		if exists && item.Name != parts[2] {
			return nil, fmt.Errorf("duplicate migration version %s", parts[1])
		}
		if !exists {
			item = &pending{Version: version, VersionKey: parts[1], Name: parts[2]}
			byVersion[version] = item
		}

		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", name, err)
		}
		if len(strings.TrimSpace(string(content))) == 0 {
			return nil, fmt.Errorf("migration %q is empty", name)
		}
		switch parts[3] {
		case "up":
			if item.hasUp {
				return nil, fmt.Errorf("duplicate up migration for version %s", parts[1])
			}
			item.Up = content
			item.hasUp = true
		case "down":
			if item.hasDown {
				return nil, fmt.Errorf("duplicate down migration for version %s", parts[1])
			}
			item.Down = content
			item.hasDown = true
		default:
			return nil, errors.New("unreachable migration direction")
		}
	}

	if len(byVersion) == 0 {
		return nil, errors.New("no migrations found")
	}
	versions := make([]int, 0, len(byVersion))
	for version := range byVersion {
		versions = append(versions, version)
	}
	sort.Ints(versions)

	plan := make([]Migration, 0, len(versions))
	for index, version := range versions {
		expected := index + 1
		item := byVersion[version]
		if version != expected {
			return nil, fmt.Errorf("expected migration version %06d, found %s", expected, item.VersionKey)
		}
		if !item.hasUp {
			return nil, fmt.Errorf("missing up migration for version %s", item.VersionKey)
		}
		if !item.hasDown {
			return nil, fmt.Errorf("missing down migration for version %s", item.VersionKey)
		}
		plan = append(plan, Migration{
			Version:    item.Version,
			VersionKey: item.VersionKey,
			Name:       item.Name,
			Up:         item.Up,
			Down:       item.Down,
		})
	}
	return plan, nil
}

// RunMigrations applies or rolls back the SQL migrations in dir.
//
// Applied versions are tracked in the schema_migrations table
// (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now()).
// The action "up" applies every not-yet-applied migration in ascending version
// order; "down_all" rolls back every applied migration in descending order.
// Re-running an action is a no-op for versions already in the target state.
func RunMigrations(ctx context.Context, db *sql.DB, dir string, action string) error {
	plan, err := LoadMigrations(dir)
	if err != nil {
		return err
	}

	// Run the whole sequence on one dedicated connection so migration SQL and
	// the schema_migrations bookkeeping share a session without pool interleaving.
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return err
	}

	switch action {
	case "up":
		for _, item := range plan {
			if applied[item.VersionKey] {
				continue
			}
			if err := applyOne(ctx, conn, item.VersionKey, item.Up, false); err != nil {
				return err
			}
		}
		return nil
	case "down_all":
		for index := len(plan) - 1; index >= 0; index-- {
			item := plan[index]
			if !applied[item.VersionKey] {
				continue
			}
			if err := applyOne(ctx, conn, item.VersionKey, item.Down, true); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown migration action %q (want %q or %q)", action, "up", "down_all")
	}
}

func appliedVersions(ctx context.Context, conn *sql.Conn) (map[string]bool, error) {
	rows, err := conn.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("query applied migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := map[string]bool{}
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan applied migration version: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied migrations: %w", err)
	}
	return applied, nil
}

// execer is satisfied by both *sql.Conn and *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// applyOne runs one migration and updates the schema_migrations bookkeeping.
//
// Migration files that manage their own transaction (BEGIN ... COMMIT) are
// executed as-is: their own transaction provides atomic rollback on failure,
// and the version row is recorded immediately afterward on the same session.
// Files without transaction control are wrapped together with the bookkeeping
// row in a single transaction, so a failure rolls back both.
func applyOne(ctx context.Context, conn *sql.Conn, version string, content []byte, rollingBack bool) error {
	apply := func(target execer) error {
		if _, err := target.ExecContext(ctx, string(content)); err != nil {
			return fmt.Errorf("execute migration %s: %w", version, err)
		}
		if rollingBack {
			if _, err := target.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = $1`, version); err != nil {
				return fmt.Errorf("record rollback of migration %s: %w", version, err)
			}
			return nil
		}
		if _, err := target.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			return fmt.Errorf("record migration %s: %w", version, err)
		}
		return nil
	}

	if hasTransactionControl(string(content)) {
		return apply(conn)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	if err := apply(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// hasTransactionControl reports whether the SQL text manages its own
// transaction block. Such files must not be double-wrapped in another
// transaction, otherwise the file's COMMIT would commit (or confuse) the
// outer transaction.
func hasTransactionControl(sqlText string) bool {
	upper := strings.ToUpper(sqlText)
	for _, token := range []string{"BEGIN", "START TRANSACTION", "COMMIT", "ROLLBACK"} {
		if strings.Contains(upper, token) {
			return true
		}
	}
	return false
}
