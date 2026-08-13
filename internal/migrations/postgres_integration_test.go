package migrations

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresMigrationsUpAndDown(t *testing.T) {
	config := testDatabaseConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect to PostgreSQL test database %q: %v", config.Database, err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	schema := uniqueTestSchema(t)
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create isolated schema %q: %v", schema, err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := dropIsolatedSchema(cleanupCtx, config, quotedSchema); err != nil {
			t.Errorf("drop isolated schema %q: %v", schema, err)
		}
	}()

	if _, err := conn.Exec(ctx, "SET search_path TO "+quotedSchema); err != nil {
		t.Fatalf("set search_path to isolated schema %q: %v", schema, err)
	}

	for _, name := range []string{"000001_core.up.sql", "000002_identity.up.sql"} {
		applyMigration(t, ctx, conn, name)
	}
	legacyServerID, legacyUserID := createServerMemberFixture(t, ctx, conn)
	if _, err := conn.Exec(ctx, `
		INSERT INTO server_members (server_id, user_id, permissions)
		VALUES ($1, $2, ARRAY[]::text[])`, legacyServerID, legacyUserID); err != nil {
		t.Fatalf("insert legacy empty-permission membership: %v", err)
	}
	applyMigration(t, ctx, conn, "000003_membership_permissions.up.sql")

	criticalTables := []string{
		"users",
		"sessions",
		"server_members",
		"server_tasks",
		"backups",
		"audit_events",
		"password_reset_tokens",
	}
	for _, table := range criticalTables {
		assertTableExists(t, ctx, conn, schema, table, true)
	}
	assertUpdatedAtColumn(t, ctx, conn, schema)
	assertDigestAndExpiryConstraints(t, ctx, conn)
	assertServerMemberPermissionConstraints(t, ctx, conn, legacyServerID, legacyUserID)

	applyMigration(t, ctx, conn, "000003_membership_permissions.down.sql")
	assertMembershipPermissionConstraintsAreRemoved(t, ctx, conn, legacyServerID)
	applyMigration(t, ctx, conn, "000002_identity.down.sql")
	assertTableExists(t, ctx, conn, schema, "password_reset_tokens", false)
	assertColumnExists(t, ctx, conn, schema, "server_members", "updated_at", false)

	applyMigration(t, ctx, conn, "000001_core.down.sql")
	for _, table := range criticalTables {
		assertTableExists(t, ctx, conn, schema, table, false)
	}
}

func TestBackupFailureMetadataMigrationBackfillsAndConstrainsState(t *testing.T) {
	config := testDatabaseConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect to PostgreSQL test database %q: %v", config.Database, err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	schema := uniqueTestSchema(t)
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create isolated schema %q: %v", schema, err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := dropIsolatedSchema(cleanupCtx, config, quotedSchema); err != nil {
			t.Errorf("drop isolated schema %q: %v", schema, err)
		}
	}()
	if _, err := conn.Exec(ctx, "SET search_path TO "+quotedSchema); err != nil {
		t.Fatalf("set search_path to isolated schema %q: %v", schema, err)
	}

	applyMigration(t, ctx, conn, "000001_core.up.sql")
	serverID, ownerID := createServerMemberFixture(t, ctx, conn)

	var failedID, deletedID string
	if err := conn.QueryRow(ctx, `
		INSERT INTO backups (server_id, creator_id, name, status, format_version, game_bundle_digest)
		VALUES ($1, $2, 'legacy-failed', 'failed', 'v1', 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')
		RETURNING id::text`, serverID, ownerID).Scan(&failedID); err != nil {
		t.Fatalf("insert legacy failed backup: %v", err)
	}
	if err := conn.QueryRow(ctx, `
		INSERT INTO backups (server_id, creator_id, name, status, format_version, game_bundle_digest, completed_at)
		VALUES ($1, $2, 'legacy-deleted', 'deleted', 'v1', 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', now() - interval '1 minute')
		RETURNING id::text`, serverID, ownerID).Scan(&deletedID); err != nil {
		t.Fatalf("insert legacy deleted backup: %v", err)
	}

	applyMigration(t, ctx, conn, "000008_backup_failure_metadata.up.sql")
	for _, column := range []string{"manifest_digest", "failure_code", "failure_message", "deleted_at"} {
		assertColumnExists(t, ctx, conn, schema, "backups", column, true)
	}

	var failureCode, failureMessage string
	if err := conn.QueryRow(ctx, `SELECT failure_code, failure_message FROM backups WHERE id = $1`, failedID).Scan(&failureCode, &failureMessage); err != nil {
		t.Fatalf("read failed backup backfill: %v", err)
	}
	if failureCode != "BACKUP_FAILED" || failureMessage == "" {
		t.Fatalf("failed backup backfill = %q/%q", failureCode, failureMessage)
	}
	var deletedAt time.Time
	if err := conn.QueryRow(ctx, `SELECT deleted_at FROM backups WHERE id = $1`, deletedID).Scan(&deletedAt); err != nil {
		t.Fatalf("read deleted backup backfill: %v", err)
	}
	if deletedAt.IsZero() {
		t.Fatal("deleted backup did not receive deleted_at backfill")
	}

	_, err = conn.Exec(ctx, `UPDATE backups SET manifest_digest = 'sha256:not-a-digest' WHERE id = $1`, failedID)
	requireSQLState(t, err, "23514", "backups.manifest_digest must be a sha256 digest")
	_, err = conn.Exec(ctx, `UPDATE backups SET failure_message = NULL WHERE id = $1`, failedID)
	requireSQLState(t, err, "23514", "backup failure metadata must remain paired")
	_, err = conn.Exec(ctx, `UPDATE backups SET failure_code = NULL, failure_message = NULL WHERE id = $1`, failedID)
	requireSQLState(t, err, "23514", "failed backups must retain failure metadata")
	_, err = conn.Exec(ctx, `UPDATE backups SET deleted_at = NULL WHERE id = $1`, deletedID)
	requireSQLState(t, err, "23514", "deleted backups must retain deleted_at")
	if _, err := conn.Exec(ctx, `UPDATE backups SET manifest_digest = 'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' WHERE id = $1`, failedID); err != nil {
		t.Fatalf("set valid manifest digest: %v", err)
	}

	applyMigration(t, ctx, conn, "000008_backup_failure_metadata.down.sql")
	for _, column := range []string{"manifest_digest", "failure_code", "failure_message", "deleted_at"} {
		assertColumnExists(t, ctx, conn, schema, "backups", column, false)
	}
	applyMigration(t, ctx, conn, "000001_core.down.sql")
}

func TestTaskFencingMigrationCompactsLegacyStates(t *testing.T) {
	config := testDatabaseConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect to PostgreSQL test database %q: %v", config.Database, err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	schema := uniqueTestSchema(t)
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create isolated schema %q: %v", schema, err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := dropIsolatedSchema(cleanupCtx, config, quotedSchema); err != nil {
			t.Errorf("drop isolated schema %q: %v", schema, err)
		}
	}()
	if _, err := conn.Exec(ctx, "SET search_path TO "+quotedSchema); err != nil {
		t.Fatalf("set search_path to isolated schema %q: %v", schema, err)
	}

	applyMigration(t, ctx, conn, "000001_core.up.sql")
	serverID, _ := createServerMemberFixture(t, ctx, conn)
	var nodeID string
	if err := conn.QueryRow(ctx, `SELECT node_id::text FROM servers WHERE id = $1`, serverID).Scan(&nodeID); err != nil {
		t.Fatalf("read fixture node: %v", err)
	}
	digest := make([]byte, 32)
	for i := range digest {
		digest[i] = byte(i)
	}

	var dispatchedID, canceledID string
	if err := conn.QueryRow(ctx, `
		INSERT INTO server_tasks (id, server_id, node_id, task_type, status, generation,
			idempotency_scope, idempotency_key, request_digest)
		VALUES (gen_random_uuid(), $1, $2, 'start', 'dispatched', 1,
			'test-dispatched-scope', 'test-dispatched-key-00000001', $3)
		RETURNING id::text`, serverID, nodeID, digest).Scan(&dispatchedID); err != nil {
		t.Fatalf("insert legacy dispatched task: %v", err)
	}
	if err := conn.QueryRow(ctx, `
		INSERT INTO server_tasks (id, server_id, node_id, task_type, status, generation,
			idempotency_scope, idempotency_key, request_digest)
		VALUES (gen_random_uuid(), $1, $2, 'stop', 'canceled', 1,
			'test-canceled-scope', 'test-canceled-key-000000001', $3)
		RETURNING id::text`, serverID, nodeID, digest).Scan(&canceledID); err != nil {
		t.Fatalf("insert legacy canceled task: %v", err)
	}

	applyMigration(t, ctx, conn, "000009_task_fencing.up.sql")

	for _, column := range []string{"task_input", "lease_token", "connection_epoch", "state_version", "lease_renewed_at"} {
		assertColumnExists(t, ctx, conn, schema, "server_tasks", column, true)
	}

	// dispatched → queued（重新入队），租约字段清空。
	var status string
	if err := conn.QueryRow(ctx, `SELECT status FROM server_tasks WHERE id = $1`, dispatchedID).Scan(&status); err != nil {
		t.Fatalf("read converted dispatched task: %v", err)
	}
	if status != "queued" {
		t.Fatalf("dispatched task converted to %q, want queued", status)
	}
	// canceled → failed（结构化终态），completed_at 补全。
	var errorCode string
	var retryable bool
	var completedAt time.Time
	if err := conn.QueryRow(ctx, `
		SELECT COALESCE(error_code, ''), COALESCE(error_retryable, false), completed_at
		FROM server_tasks WHERE id = $1`, canceledID).Scan(&errorCode, &retryable, &completedAt); err != nil {
		t.Fatalf("read converted canceled task: %v", err)
	}
	if errorCode != "CANCELED" || retryable {
		t.Fatalf("canceled task conversion = %q/%t, want CANCELED/false", errorCode, retryable)
	}
	if completedAt.IsZero() {
		t.Fatal("canceled task did not receive completed_at")
	}

	// 新状态域：dispatched/canceled 被 CHECK 拒绝。
	_, err = conn.Exec(ctx, `
		INSERT INTO server_tasks (id, server_id, node_id, task_type, status, generation,
			idempotency_scope, idempotency_key, request_digest)
		VALUES (gen_random_uuid(), $1, $2, 'start', 'dispatched', 1,
			'test-dispatched-reject', 'test-dispatched-reject-0001', $3)
	`, serverID, nodeID, digest)
	requireSQLState(t, err, "23514", "server_tasks.status must reject dispatched after 000009")

	// 新索引存在（租约到期对账用）。
	var leaseIndexExists bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			WHERE schemaname = $1 AND indexname = 'server_tasks_lease_expiry_idx'
		)`, schema).Scan(&leaseIndexExists); err != nil {
		t.Fatalf("inspect lease expiry index: %v", err)
	}
	if !leaseIndexExists {
		t.Fatal("server_tasks_lease_expiry_idx missing after 000009")
	}

	applyMigration(t, ctx, conn, "000009_task_fencing.down.sql")
	for _, column := range []string{"task_input", "lease_token", "connection_epoch", "state_version", "lease_renewed_at"} {
		assertColumnExists(t, ctx, conn, schema, "server_tasks", column, false)
	}
	// 清掉被 up 压缩成 queued 的历史行，否则它与下面的 dispatched 行在
	// 恢复后的互斥索引上冲突。
	if _, err := conn.Exec(ctx, `DELETE FROM server_tasks WHERE id = $1`, dispatchedID); err != nil {
		t.Fatalf("delete converted task before down-phase insert: %v", err)
	}
	// 旧状态域恢复：dispatched 可再次写入。
	if _, err := conn.Exec(ctx, `
		INSERT INTO server_tasks (id, server_id, node_id, task_type, status, generation,
			idempotency_scope, idempotency_key, request_digest)
		VALUES (gen_random_uuid(), $1, $2, 'start', 'dispatched', 1,
			'test-dispatched-restored', 'test-dispatched-restored-001', $3)
	`, serverID, nodeID, digest); err != nil {
		t.Fatalf("dispatched insert after down migration: %v", err)
	}
	applyMigration(t, ctx, conn, "000001_core.down.sql")
}

func TestPostgresMigrationFailureCleansIsolatedSchema(t *testing.T) {
	config := testDatabaseConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect to PostgreSQL test database %q: %v", config.Database, err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	schema := uniqueTestSchema(t)
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create isolated schema %q: %v", schema, err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := dropIsolatedSchema(cleanupCtx, config, quotedSchema); err != nil {
			t.Errorf("fallback cleanup for isolated schema %q: %v", schema, err)
		}
	}()

	if _, err := conn.Exec(ctx, "SET search_path TO "+quotedSchema); err != nil {
		t.Fatalf("set search_path to isolated schema %q: %v", schema, err)
	}
	if _, err := conn.Exec(ctx, `
		BEGIN;
		CREATE TABLE cleanup_probe (id integer NOT NULL);
		INSERT INTO cleanup_probe (id) VALUES ('not-an-integer');
		COMMIT;`); err == nil {
		t.Fatal("intentionally invalid migration unexpectedly succeeded")
	}

	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cleanupCancel()
	if err := dropIsolatedSchema(cleanupCtx, config, quotedSchema); err != nil {
		t.Fatalf("clean isolated schema after an aborted migration transaction: %v", err)
	}

	inspector, err := pgx.ConnectConfig(cleanupCtx, config.Copy())
	if err != nil {
		t.Fatalf("connect to inspect migration cleanup: %v", err)
	}
	defer func() { _ = inspector.Close(context.Background()) }()
	var exists bool
	if err := inspector.QueryRow(cleanupCtx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)`, schema).Scan(&exists); err != nil {
		t.Fatalf("inspect isolated schema cleanup: %v", err)
	}
	if exists {
		t.Fatalf("isolated schema %q remains after cleanup", schema)
	}
}

func testDatabaseConfig(t *testing.T) *pgx.ConnConfig {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("GUGU_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set GUGU_TEST_DATABASE_URL to run PostgreSQL migration integration tests")
	}
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse GUGU_TEST_DATABASE_URL: %v", err)
	}
	if err := validateTestDatabaseName(config.Database); err != nil {
		t.Fatal(err)
	}
	config.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	return config
}

func dropIsolatedSchema(ctx context.Context, config *pgx.ConnConfig, quotedSchema string) error {
	cleanupConn, err := pgx.ConnectConfig(ctx, config.Copy())
	if err != nil {
		return fmt.Errorf("open independent cleanup connection: %w", err)
	}
	defer func() { _ = cleanupConn.Close(context.Background()) }()
	if _, err := cleanupConn.Exec(ctx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE"); err != nil {
		return fmt.Errorf("drop schema: %w", err)
	}
	return nil
}

func uniqueTestSchema(t *testing.T) string {
	t.Helper()
	randomBytes := make([]byte, 12)
	if _, err := rand.Read(randomBytes); err != nil {
		t.Fatalf("generate unique schema suffix: %v", err)
	}
	return "gugu_migrations_test_" + hex.EncodeToString(randomBytes)
}

func validateTestDatabaseName(databaseName string) error {
	normalized := strings.ToLower(strings.TrimSpace(databaseName))
	if len(normalized) <= len("_test") || !strings.HasSuffix(normalized, "_test") {
		return fmt.Errorf("refusing to run migrations against database %q: test database name must end in _test", databaseName)
	}
	return nil
}

func applyMigration(t *testing.T, ctx context.Context, conn *pgx.Conn, name string) {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate migration integration test source")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "migrations", name)
	sql, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration %s: %v", name, err)
	}
	if _, err := conn.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("apply migration %s: %v", name, err)
	}
}

func assertTableExists(t *testing.T, ctx context.Context, conn *pgx.Conn, schema string, table string, want bool) {
	t.Helper()
	var exists bool
	err := conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.tables
			WHERE table_schema = $1 AND table_name = $2
		)`, schema, table).Scan(&exists)
	if err != nil {
		t.Fatalf("inspect table %s.%s: %v", schema, table, err)
	}
	if exists != want {
		t.Fatalf("table %s.%s exists = %t, want %t", schema, table, exists, want)
	}
}

func assertColumnExists(t *testing.T, ctx context.Context, conn *pgx.Conn, schema string, table string, column string, want bool) {
	t.Helper()
	var exists bool
	err := conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = $2 AND column_name = $3
		)`, schema, table, column).Scan(&exists)
	if err != nil {
		t.Fatalf("inspect column %s.%s.%s: %v", schema, table, column, err)
	}
	if exists != want {
		t.Fatalf("column %s.%s.%s exists = %t, want %t", schema, table, column, exists, want)
	}
}

func assertUpdatedAtColumn(t *testing.T, ctx context.Context, conn *pgx.Conn, schema string) {
	t.Helper()
	var valid bool
	err := conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_schema = $1
			  AND table_name = 'server_members'
			  AND column_name = 'updated_at'
			  AND data_type = 'timestamp with time zone'
			  AND is_nullable = 'NO'
			  AND column_default IS NOT NULL
		)`, schema).Scan(&valid)
	if err != nil {
		t.Fatalf("inspect server_members.updated_at: %v", err)
	}
	if !valid {
		t.Fatal("server_members.updated_at is missing its required timestamptz, NOT NULL, or default definition")
	}
}

func assertDigestAndExpiryConstraints(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	var userID string
	err := conn.QueryRow(ctx, `
		INSERT INTO users (email, normalized_email, display_name, password_hash)
		VALUES ('migration-test@gugu.invalid', 'migration-test@gugu.invalid', 'Migration Test', 'test-only-hash')
		RETURNING id::text`).Scan(&userID)
	if err != nil {
		t.Fatalf("insert constraint test user: %v", err)
	}

	_, err = conn.Exec(ctx, `
		INSERT INTO users (email, normalized_email, display_name, password_hash, status)
		VALUES ('invalid-status@gugu.invalid', 'invalid-status@gugu.invalid', 'Invalid Status', 'test-only-hash', 'pending')`)
	requireSQLState(t, err, "23514", "users.status must reject values outside active and disabled")

	_, err = conn.Exec(ctx, `
		INSERT INTO users (email, normalized_email, display_name, password_hash)
		VALUES ('duplicate@gugu.invalid', 'migration-test@gugu.invalid', 'Duplicate User', 'test-only-hash')`)
	requireSQLState(t, err, "23505", "users.normalized_email must remain unique")

	validCSRF := bytes.Repeat([]byte{0x11}, 32)
	_, err = conn.Exec(ctx, `
		INSERT INTO sessions (user_id, token_digest, csrf_digest, expires_at)
		VALUES ($1, $2, $3, now() + interval '1 hour')`, userID, []byte{0x01}, validCSRF)
	requireSQLState(t, err, "23514", "sessions.token_digest must reject digests that are not 32 bytes")

	if _, err := conn.Exec(ctx, `
		INSERT INTO sessions (user_id, token_digest, csrf_digest, expires_at)
		VALUES ($1, $2, $3, now() + interval '1 hour')`, userID, bytes.Repeat([]byte{0x22}, 32), validCSRF); err != nil {
		t.Fatalf("insert session with 32-byte digests: %v", err)
	}

	createdAt := time.Now().UTC()
	_, err = conn.Exec(ctx, `
		INSERT INTO password_reset_tokens (user_id, token_digest, expires_at, created_at)
		VALUES ($1, $2, $3, $3)`, userID, bytes.Repeat([]byte{0x33}, 32), createdAt)
	requireSQLState(t, err, "23514", "password_reset_tokens.expires_at must be later than created_at")

	if _, err := conn.Exec(ctx, `
		INSERT INTO password_reset_tokens (user_id, token_digest, expires_at, created_at)
		VALUES ($1, $2, $3, $4)`, userID, bytes.Repeat([]byte{0x44}, 32), createdAt.Add(time.Hour), createdAt); err != nil {
		t.Fatalf("insert password reset token with a future expiry: %v", err)
	}
}

func createServerMemberFixture(t *testing.T, ctx context.Context, conn *pgx.Conn) (serverID string, ownerID string) {
	t.Helper()
	ownerID = insertMigrationTestUser(t, ctx, conn, "permission-owner@gugu.invalid", "Permission Owner")
	var nodeID string
	if err := conn.QueryRow(ctx, `
		INSERT INTO nodes (name, agent_version, protocol_version, condition, region, cpu_cores, memory_bytes, disk_bytes)
		VALUES ('permission-migration-node', 'test-agent', 'v1', 'available', 'test', 1, 1073741824, 1073741824)
		RETURNING id::text`).Scan(&nodeID); err != nil {
		t.Fatalf("insert permission migration node: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO game_definitions (id, name, source_url, review_status)
		VALUES ('permission-migration-game', 'Permission Migration Game', 'https://example.invalid/game.json', 'approved')`); err != nil {
		t.Fatalf("insert permission migration game definition: %v", err)
	}
	var bundleID string
	if err := conn.QueryRow(ctx, `
		INSERT INTO game_bundles (game_definition_id, definition_version, game_version, digest, schema_version, license, compatibility, published_at)
		VALUES ('permission-migration-game', '1.0.0', '1.0.0', 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'v1', 'test', '{}'::jsonb, now())
		RETURNING id::text`).Scan(&bundleID); err != nil {
		t.Fatalf("insert permission migration game bundle: %v", err)
	}
	if err := conn.QueryRow(ctx, `
		INSERT INTO servers (
			owner_id, node_id, game_bundle_id, game_version, name, lifecycle_state,
			desired_power, observed_power, node_condition, health_condition, memory_limit_bytes, disk_limit_bytes
		)
		VALUES ($1, $2, $3, '1.0.0', 'Permission Migration Server', 'ready', 'stopped', 'stopped', 'available', 'healthy', 1073741824, 1073741824)
		RETURNING id::text`, ownerID, nodeID, bundleID).Scan(&serverID); err != nil {
		t.Fatalf("insert permission migration server: %v", err)
	}
	return serverID, ownerID
}

func assertServerMemberPermissionConstraints(t *testing.T, ctx context.Context, conn *pgx.Conn, serverID string, legacyUserID string) {
	t.Helper()
	var permissions []string
	if err := conn.QueryRow(ctx, `
		SELECT permissions
		FROM server_members
		WHERE server_id = $1 AND user_id = $2`, serverID, legacyUserID).Scan(&permissions); err != nil {
		t.Fatalf("read backfilled legacy membership: %v", err)
	}
	if len(permissions) != 1 || permissions[0] != "servers.read" {
		t.Fatalf("backfilled legacy permissions = %#v, want [servers.read]", permissions)
	}

	memberID := insertMigrationTestUser(t, ctx, conn, "permission-member@gugu.invalid", "Permission Member")
	_, err := conn.Exec(ctx, `
		INSERT INTO server_members (server_id, user_id, permissions)
		VALUES ($1, $2, ARRAY['servers.power'])`, serverID, memberID)
	requireSQLState(t, err, "23514", "server_members.permissions must require servers.read")

	_, err = conn.Exec(ctx, `
		INSERT INTO server_members (server_id, user_id, permissions)
		VALUES ($1, $2, ARRAY['servers.read', 'servers.unknown'])`, serverID, memberID)
	requireSQLState(t, err, "23514", "server_members.permissions must reject unknown values")

	_, err = conn.Exec(ctx, `
		INSERT INTO server_members (server_id, user_id, permissions)
		VALUES ($1, $2, ARRAY['servers.read', 'servers.power', 'servers.power'])`, serverID, memberID)
	requireSQLState(t, err, "23514", "server_members.permissions must reject duplicate values")

	_, err = conn.Exec(ctx, `
		INSERT INTO server_members (server_id, user_id, permissions)
		VALUES ($1, $2, ARRAY['servers.read', NULL]::text[])`, serverID, memberID)
	requireSQLState(t, err, "23514", "server_members.permissions must reject NULL values")

	if _, err := conn.Exec(ctx, `
		INSERT INTO server_members (server_id, user_id, permissions)
		VALUES ($1, $2, ARRAY['servers.read', 'servers.power'])`, serverID, memberID); err != nil {
		t.Fatalf("insert valid server membership permissions: %v", err)
	}
}

func assertMembershipPermissionConstraintsAreRemoved(t *testing.T, ctx context.Context, conn *pgx.Conn, serverID string) {
	t.Helper()
	memberID := insertMigrationTestUser(t, ctx, conn, "permission-down@gugu.invalid", "Permission Down")
	if _, err := conn.Exec(ctx, `
		INSERT INTO server_members (server_id, user_id, permissions)
		VALUES ($1, $2, ARRAY['servers.power'])`, serverID, memberID); err != nil {
		t.Fatalf("insert membership without servers.read after down migration: %v", err)
	}
}

func insertMigrationTestUser(t *testing.T, ctx context.Context, conn *pgx.Conn, email string, displayName string) string {
	t.Helper()
	var userID string
	if err := conn.QueryRow(ctx, `
		INSERT INTO users (email, normalized_email, display_name, password_hash)
		VALUES ($1, $1, $2, 'test-only-hash')
		RETURNING id::text`, email, displayName).Scan(&userID); err != nil {
		t.Fatalf("insert migration test user %q: %v", email, err)
	}
	return userID
}

func requireSQLState(t *testing.T, err error, wantCode string, context string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: insert unexpectedly succeeded", context)
	}
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) {
		t.Fatalf("%s: error = %v, want PostgreSQL SQLSTATE %s", context, err, wantCode)
	}
	if pgError.Code != wantCode {
		t.Fatalf("%s: SQLSTATE = %s, want %s; error=%v", context, pgError.Code, wantCode, err)
	}
}

func TestPostgresMigrationsRejectNonTestDatabase(t *testing.T) {
	for _, databaseName := range []string{"", "_test", "postgres", "gugumanager", "production"} {
		t.Run(databaseName, func(t *testing.T) {
			if err := validateTestDatabaseName(databaseName); err == nil {
				t.Fatalf("validateTestDatabaseName(%q) unexpectedly allowed a non-test database", databaseName)
			}
		})
	}

	if err := validateTestDatabaseName("gugumanager_test"); err != nil {
		t.Fatalf("validateTestDatabaseName(gugumanager_test): %v", err)
	}
}
