package migrations

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "github.com/lib/pq"
)

func testPool(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("GUGU_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("GUGU_TEST_DATABASE_URL required")
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse GUGU_TEST_DATABASE_URL: %v", err)
	}
	databaseName := strings.Trim(parsed.Path, "/")
	if !strings.HasSuffix(databaseName, "_test") {
		t.Fatalf("refusing to run migrations against database %q: test database name must end in _test", databaseName)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func findMigrationsDir(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate migration runner test source")
	}
	return filepath.Join(filepath.Dir(currentFile), "..", "..", "migrations")
}

func TestRunMigrationsUpDown(t *testing.T) {
	db := testPool(t)
	dir := findMigrationsDir(t)
	ctx := context.Background()

	// Ensure a clean starting point: any leftovers from a previously
	// interrupted run must be rolled back first.
	if err := RunMigrations(ctx, db, dir, "down_all"); err != nil {
		t.Fatalf("clean start down_all: %v", err)
	}

	if err := RunMigrations(ctx, db, dir, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}
	for _, table := range []string{
		"users",
		"sessions",
		"nodes",
		"servers",
		"server_tasks",
		"password_reset_tokens",
		"audit_events",
	} {
		assertPublicTableExists(t, db, table, true)
	}
	var applied int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&applied); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if applied != 4 {
		t.Fatalf("schema_migrations rows = %d, want 4", applied)
	}

	// Idempotency: a repeated up must be a no-op without error.
	if err := RunMigrations(ctx, db, dir, "up"); err != nil {
		t.Fatalf("idempotent up: %v", err)
	}

	// Roll everything back and confirm the down path dropped the schema.
	if err := RunMigrations(ctx, db, dir, "down_all"); err != nil {
		t.Fatalf("down_all: %v", err)
	}
	for _, table := range []string{
		"users",
		"sessions",
		"nodes",
		"servers",
		"server_tasks",
		"password_reset_tokens",
		"audit_events",
	} {
		assertPublicTableExists(t, db, table, false)
	}
	if err := RunMigrations(ctx, db, dir, "down_all"); err != nil {
		t.Fatalf("idempotent down_all: %v", err)
	}
}

func assertPublicTableExists(t *testing.T, db *sql.DB, table string, want bool) {
	t.Helper()
	var exists bool
	err := db.QueryRowContext(context.Background(), `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = $1
		)`, table).Scan(&exists)
	if err != nil {
		t.Fatalf("inspect table %s: %v", table, err)
	}
	if exists != want {
		t.Fatalf("table %s exists = %t, want %t", table, exists, want)
	}
}
