package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"strings"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/id"
	"github.com/gugumanager/gugumanager/internal/identity"
	"github.com/lib/pq"
)

const sessionTTL = 12 * time.Hour

// Login authenticates a user and creates a session.
func (s *Postgres) Login(email string, password string) (domain.SessionView, string, error) {
	normalizedEmail := strings.ToLower(strings.TrimSpace(email))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Fetch user with roles
	var userID, passwordHash, displayName, status string
	var roles []string
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.password_hash, u.display_name, u.status,
		       COALESCE(array_agg(r.role_key) FILTER (WHERE r.role_key IS NOT NULL), '{}') as roles
		FROM users u
		LEFT JOIN user_roles ur ON u.id = ur.user_id
		LEFT JOIN roles r ON ur.role_id = r.id
		WHERE u.normalized_email = $1
		GROUP BY u.id, u.password_hash, u.display_name, u.status
	`, normalizedEmail).Scan(&userID, &passwordHash, &displayName, &status, pq.Array(&roles))

	if err == sql.ErrNoRows || status != "active" {
		auditLoginFailure(ctx, s.db, "auth.login", "Unknown actor")
		return domain.SessionView{}, "", domain.NewProblem("AUTH_INVALID_CREDENTIALS", "邮箱或密码错误", false)
	}
	if err != nil {
		auditLoginFailure(ctx, s.db, "auth.login", "Unknown actor")
		return domain.SessionView{}, "", domain.NewProblem("INTERNAL_ERROR", "无法查询用户", true)
	}

	// Verify password
	passwordMatch, hashErr := identity.VerifyPassword(passwordHash, password)
	if hashErr != nil || !passwordMatch {
		auditLoginFailure(ctx, s.db, "auth.login", "Unknown actor")
		return domain.SessionView{}, "", domain.NewProblem("AUTH_INVALID_CREDENTIALS", "邮箱或密码错误", false)
	}

	// Create session token and CSRF token
	token := randomToken()
	csrf := randomToken()
	tokenDigestValue := sessionTokenDigest(token)
	csrfDigestValue := tokenDigest(csrf)
	expiresAt := time.Now().UTC().Add(sessionTTL)

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO sessions (user_id, token_digest, csrf_digest, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, userID, tokenDigestValue[:], csrfDigestValue[:], expiresAt, time.Now().UTC())
	if err != nil {
		return domain.SessionView{}, "", domain.NewProblem("INTERNAL_ERROR", "无法创建会话", true)
	}

	// Audit success
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO audit_events (actor_id, actor_type, action, target_type, target_id, result, trace_id, created_at)
		VALUES ($1, 'user', 'auth.login', 'session', 'Control Plane', 'success', $2, $3)
	`, userID, id.New(), time.Now().UTC())

	user := domain.User{
		ID:          userID,
		Email:       normalizedEmail,
		DisplayName: displayName,
		Roles:       roles,
		Status:      status,
	}

	return domain.SessionView{User: user, CSRFToken: csrf, Environment: s.environment}, token, nil
}

// AuthenticateSession validates an opaque session for ordinary authenticated
// requests. It never rotates or returns a CSRF token.
func (s *Postgres) AuthenticateSession(token string) (domain.SessionView, error) {
	digest := sessionTokenDigest(token)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var userID, displayName, email, status string
	var roles []string
	var expiresAt time.Time
	err := s.db.QueryRowContext(ctx, `
		SELECT s.user_id, s.expires_at,
		       u.email, u.display_name, u.status,
		       COALESCE(array_agg(r.role_key) FILTER (WHERE r.role_key IS NOT NULL), '{}') as roles
		FROM sessions s
		JOIN users u ON s.user_id = u.id
		LEFT JOIN user_roles ur ON u.id = ur.user_id
		LEFT JOIN roles r ON ur.role_id = r.id
		WHERE s.token_digest = $1 AND s.revoked_at IS NULL
		GROUP BY s.user_id, s.expires_at, u.email, u.display_name, u.status
	`, digest[:]).Scan(&userID, &expiresAt, &email, &displayName, &status, pq.Array(&roles))

	if err == sql.ErrNoRows {
		return domain.SessionView{}, domain.NewProblem("AUTH_REQUIRED", "请先登录", false)
	}
	if err != nil {
		return domain.SessionView{}, domain.NewProblem("INTERNAL_ERROR", "无法查询会话", true)
	}
	if status != "active" || time.Now().UTC().After(expiresAt) {
		// Cleanup expired session
		_, _ = s.db.ExecContext(ctx, `UPDATE sessions SET revoked_at = $1 WHERE token_digest = $2`, time.Now().UTC(), digest[:])
		return domain.SessionView{}, domain.NewProblem("AUTH_REQUIRED", "请先登录", false)
	}

	// Update last_seen_at
	_, _ = s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = $1 WHERE token_digest = $2`, time.Now().UTC(), digest[:])

	user := domain.User{
		ID:          userID,
		Email:       email,
		DisplayName: displayName,
		Roles:       roles,
		Status:      status,
	}

	return domain.SessionView{User: user, Environment: s.environment}, nil
}

// RecoverSession atomically consumes the old opaque session and returns a new
// session cookie plus CSRF plaintext. Rotating both values makes concurrent
// recovery deterministic: only one request can consume the old session, so a
// successful response cannot be invalidated by another successful recovery.
func (s *Postgres) RecoverSession(token string) (domain.SessionView, string, error) {
	oldDigest := sessionTokenDigest(token)
	newToken := randomToken()
	newDigest := sessionTokenDigest(newToken)
	csrf := randomToken()
	csrfDigestValue := tokenDigest(csrf)
	now := time.Now().UTC()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.SessionView{}, "", domain.NewProblem("INTERNAL_ERROR", "无法恢复会话", true)
	}
	defer func() { _ = tx.Rollback() }()

	var userID, displayName, email, status string
	var expiresAt time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT s.user_id, s.expires_at, u.email, u.display_name, u.status
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_digest = $1 AND s.revoked_at IS NULL
		FOR UPDATE OF s
	`, oldDigest[:]).Scan(&userID, &expiresAt, &email, &displayName, &status)
	if err == sql.ErrNoRows {
		return domain.SessionView{}, "", domain.NewProblem("AUTH_REQUIRED", "请先登录", false)
	}
	if err != nil {
		return domain.SessionView{}, "", domain.NewProblem("INTERNAL_ERROR", "无法查询会话", true)
	}
	if status != "active" || !expiresAt.After(now) {
		if _, updateErr := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = $1 WHERE token_digest = $2`, now, oldDigest[:]); updateErr != nil {
			return domain.SessionView{}, "", domain.NewProblem("INTERNAL_ERROR", "无法撤销过期会话", true)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return domain.SessionView{}, "", domain.NewProblem("INTERNAL_ERROR", "无法撤销过期会话", true)
		}
		return domain.SessionView{}, "", domain.NewProblem("AUTH_REQUIRED", "请先登录", false)
	}

	var roles []string
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(array_agg(r.role_key) FILTER (WHERE r.role_key IS NOT NULL), '{}')
		FROM users u
		LEFT JOIN user_roles ur ON u.id = ur.user_id
		LEFT JOIN roles r ON ur.role_id = r.id
		WHERE u.id = $1
		GROUP BY u.id
	`, userID).Scan(pq.Array(&roles)); err != nil {
		return domain.SessionView{}, "", domain.NewProblem("INTERNAL_ERROR", "无法查询会话角色", true)
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE sessions
		SET token_digest = $1, csrf_digest = $2, last_seen_at = $3
		WHERE token_digest = $4 AND revoked_at IS NULL
	`, newDigest[:], csrfDigestValue[:], now, oldDigest[:])
	if err != nil {
		return domain.SessionView{}, "", domain.NewProblem("INTERNAL_ERROR", "无法轮换会话安全令牌", true)
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
		return domain.SessionView{}, "", domain.NewProblem("AUTH_REQUIRED", "请先登录", false)
	}
	if err := tx.Commit(); err != nil {
		return domain.SessionView{}, "", domain.NewProblem("INTERNAL_ERROR", "无法提交会话恢复", true)
	}

	return domain.SessionView{User: domain.User{
		ID: userID, Email: email, DisplayName: displayName, Roles: roles, Status: status,
	}, CSRFToken: csrf, Environment: s.environment}, newToken, nil
}

// Logout revokes a session.
func (s *Postgres) Logout(token string) {
	digest := sessionTokenDigest(token)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Get user for audit
	var userID string
	err := s.db.QueryRowContext(ctx, `SELECT user_id FROM sessions WHERE token_digest = $1 AND revoked_at IS NULL`, digest[:]).Scan(&userID)
	if err != nil {
		return
	}

	// Revoke session
	now := time.Now().UTC()
	_, _ = s.db.ExecContext(ctx, `UPDATE sessions SET revoked_at = $1 WHERE token_digest = $2`, now, digest[:])

	// Audit
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO audit_events (actor_id, actor_type, action, target_type, target_id, result, trace_id, created_at)
		VALUES ($1, 'user', 'auth.logout', 'session', 'Control Plane', 'success', $2, $3)
	`, userID, id.New(), now)
}

// ValidateCSRF checks if a CSRF token is valid for a session.
func (s *Postgres) ValidateCSRF(token string, csrf string) bool {
	digest := sessionTokenDigest(token)
	csrfDigest := tokenDigest(csrf)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var storedCSRF []byte
	var expiresAt time.Time
	var status string
	err := s.db.QueryRowContext(ctx, `
		SELECT s.csrf_digest, s.expires_at, u.status
		FROM sessions s
		JOIN users u ON s.user_id = u.id
		WHERE s.token_digest = $1 AND s.revoked_at IS NULL
	`, digest[:]).Scan(&storedCSRF, &expiresAt, &status)

	if err != nil || status != "active" || time.Now().UTC().After(expiresAt) {
		return false
	}

	return subtle.ConstantTimeCompare(storedCSRF, csrfDigest[:]) == 1
}

// Helper to audit login failures
func auditLoginFailure(ctx context.Context, db *sql.DB, action string, actor string) {
	_, _ = db.ExecContext(ctx, `
		INSERT INTO audit_events (actor_type, action, target_type, target_id, result, trace_id, created_at)
		VALUES ('user', $1, 'session', 'Control Plane', 'failure', $2, $3)
	`, action, id.New(), time.Now().UTC())
}
