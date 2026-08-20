package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/id"
	"github.com/gugumanager/gugumanager/internal/identity"
	"github.com/lib/pq"
)

// SetupStatus returns whether first-administrator setup is still required.
func (s *Postgres) SetupStatus() domain.SetupStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count)
	if err != nil {
		return domain.SetupStatus{Required: false}
	}

	return domain.SetupStatus{Required: count == 0}
}

// SetupAdmin creates the first platform administrator atomically.
func (s *Postgres) SetupAdmin(input domain.SetupAdminInput) (domain.User, error) {
	if err := s.verifyBootstrapToken(input.BootstrapToken); err != nil {
		return domain.User{}, err
	}
	email, err := normalizeEmail(input.Email)
	if err != nil {
		return domain.User{}, err
	}
	displayName, err := validateDisplayName(input.DisplayName)
	if err != nil {
		return domain.User{}, err
	}
	if err := validatePassword(input.Password); err != nil {
		return domain.User{}, err
	}

	passwordHash, err := identity.HashPassword(input.Password, identity.DefaultArgon2idParams())
	if err != nil {
		return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法安全保存管理员口令", true)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	// Verify no users exist
	var count int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count)
	if err != nil {
		return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法查询用户数量", true)
	}
	if count > 0 {
		return domain.User{}, domain.NewProblem("SETUP_ALREADY_COMPLETE", "初始化已完成", false)
	}

	// Create the user
	userID := id.New()
	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO users (id, email, normalized_email, display_name, password_hash, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'active', $6, $6)
	`, userID, email, email, displayName, passwordHash, now)
	if err != nil {
		return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法创建管理员", true)
	}

	// Assign platform_admin role
	var roleID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM roles WHERE role_key = 'platform_admin'`).Scan(&roleID)
	if err != nil {
		return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法查询管理员角色", true)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO user_roles (user_id, role_id, created_at)
		VALUES ($1, $2, $3)
	`, userID, roleID, now)
	if err != nil {
		return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法分配管理员角色", true)
	}

	// Audit log
	_, err = tx.ExecContext(ctx, `
		INSERT INTO audit_events (actor_type, action, target_type, target_id, result, trace_id, created_at)
		VALUES ('system', 'setup.admin.create', 'user', $1, 'success', $2, $3)
	`, userID, id.New(), now)
	if err != nil {
		return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法记录审计日志", true)
	}

	if err := tx.Commit(); err != nil {
		return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法提交事务", true)
	}

	user := domain.User{
		ID:          userID,
		Email:       email,
		DisplayName: displayName,
		Roles:       []string{"platform_admin"},
		Status:      "active",
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	return user, nil
}

// Users returns all users ordered by creation time.
func (s *Postgres) Users() []domain.User {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `
		SELECT u.id, u.email, u.display_name, u.status, u.created_at, u.updated_at,
		       COALESCE(array_agg(r.role_key) FILTER (WHERE r.role_key IS NOT NULL), '{}') as roles
		FROM users u
		LEFT JOIN user_roles ur ON u.id = ur.user_id
		LEFT JOIN roles r ON ur.role_id = r.id
		GROUP BY u.id, u.email, u.display_name, u.status, u.created_at, u.updated_at
		ORDER BY u.created_at ASC
	`)
	if err != nil {
		return []domain.User{}
	}
	defer rows.Close()

	users := []domain.User{}
	for rows.Next() {
		var user domain.User
		var roles []string
		err := rows.Scan(&user.ID, &user.Email, &user.DisplayName, &user.Status, &user.CreatedAt, &user.UpdatedAt, pq.Array(&roles))
		if err != nil {
			continue
		}
		user.Roles = roles
		users = append(users, user)
	}

	return users
}

// UserByID retrieves a single user by ID.
func (s *Postgres) UserByID(userID string) (domain.User, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var user domain.User
	var roles []string
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.email, u.display_name, u.status, u.created_at, u.updated_at,
		       COALESCE(array_agg(r.role_key) FILTER (WHERE r.role_key IS NOT NULL), '{}') as roles
		FROM users u
		LEFT JOIN user_roles ur ON u.id = ur.user_id
		LEFT JOIN roles r ON ur.role_id = r.id
		WHERE u.id = $1
		GROUP BY u.id, u.email, u.display_name, u.status, u.created_at, u.updated_at
	`, userID).Scan(&user.ID, &user.Email, &user.DisplayName, &user.Status, &user.CreatedAt, &user.UpdatedAt, pq.Array(&roles))

	if err == sql.ErrNoRows {
		return domain.User{}, domain.NewProblem("NOT_FOUND", "用户不存在", false)
	}
	if err != nil {
		return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法查询用户", true)
	}

	user.Roles = roles
	return user, nil
}

// CreateUser creates a new local user.
func (s *Postgres) CreateUser(input domain.CreateUserInput, actor domain.User) (domain.User, error) {
	email, err := normalizeEmail(input.Email)
	if err != nil {
		return domain.User{}, err
	}
	displayName, err := validateDisplayName(input.DisplayName)
	if err != nil {
		return domain.User{}, err
	}
	if err := validatePassword(input.Password); err != nil {
		return domain.User{}, err
	}
	roles, err := normalizeRoles(input.Roles)
	if err != nil {
		return domain.User{}, err
	}

	passwordHash, err := identity.HashPassword(input.Password, identity.DefaultArgon2idParams())
	if err != nil {
		return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法安全保存用户口令", true)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	// Check actor is platform admin
	if err := requirePlatformAdmin(ctx, tx, actor.ID); err != nil {
		return domain.User{}, err
	}

	// Check email conflict
	var exists bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE normalized_email = $1)`, email).Scan(&exists)
	if err != nil {
		return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法检查邮箱冲突", true)
	}
	if exists {
		return domain.User{}, domain.NewProblem("EMAIL_CONFLICT", "该邮箱已被使用", false)
	}

	// Insert user
	userID := id.New()
	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO users (id, email, normalized_email, display_name, password_hash, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'active', $6, $6)
	`, userID, email, email, displayName, passwordHash, now)
	if err != nil {
		return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法创建用户", true)
	}

	// Assign roles
	for _, role := range roles {
		var roleID string
		err = tx.QueryRowContext(ctx, `SELECT id FROM roles WHERE role_key = $1`, role).Scan(&roleID)
		if err != nil {
			return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法查询角色", true)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2)`, userID, roleID)
		if err != nil {
			return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法分配角色", true)
		}
	}

	// Audit
	_, err = tx.ExecContext(ctx, `
		INSERT INTO audit_events (actor_id, actor_type, action, target_type, target_id, result, trace_id, created_at)
		VALUES ($1, 'user', 'user.create', 'user', $2, 'success', $3, $4)
	`, actor.ID, userID, id.New(), now)
	if err != nil {
		return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法记录审计", true)
	}

	if err := tx.Commit(); err != nil {
		return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法提交事务", true)
	}

	user := domain.User{
		ID:          userID,
		Email:       email,
		DisplayName: displayName,
		Roles:       roles,
		Status:      "active",
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	return user, nil
}

// Helper: check if user is platform admin
func requirePlatformAdmin(ctx context.Context, tx *sql.Tx, userID string) error {
	var isAdmin bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM users u
			JOIN user_roles ur ON u.id = ur.user_id
			JOIN roles r ON ur.role_id = r.id
			WHERE u.id = $1 AND u.status = 'active' AND r.role_key = 'platform_admin'
		)
	`, userID).Scan(&isAdmin)
	if err != nil || !isAdmin {
		return domain.NewProblem("FORBIDDEN", "需要平台管理员权限", false)
	}
	return nil
}

// requirePlatformAdminDB is the connection-level variant of requirePlatformAdmin
// for methods that do not need an explicit transaction.
func requirePlatformAdminDB(ctx context.Context, db *sql.DB, userID string) error {
	var isAdmin bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM users u
			JOIN user_roles ur ON u.id = ur.user_id
			JOIN roles r ON ur.role_id = r.id
			WHERE u.id = $1 AND u.status = 'active' AND r.role_key = 'platform_admin'
		)
	`, userID).Scan(&isAdmin)
	if err != nil || !isAdmin {
		return domain.NewProblem("FORBIDDEN", "需要平台管理员权限", false)
	}
	return nil
}

// verifyBootstrapToken checks the bootstrap token presented to SetupAdmin
// against the configured digest. When no bootstrap token has been configured
// (development mode), validation is skipped.
func (s *Postgres) verifyBootstrapToken(token string) error {
	s.mu.RLock()
	digest := s.bootstrapTokenDigest
	s.mu.RUnlock()
	if digest == [32]byte{} {
		return nil // 未配置 bootstrap（开发模式），跳过校验
	}
	if sha256.Sum256([]byte(token)) != digest {
		return domain.NewProblem("SETUP_TOKEN_INVALID", "无效或已过期的初始化令牌", false)
	}
	return nil
}

// UpdateUser updates a user's display name, status, and/or roles.
func (s *Postgres) UpdateUser(userID string, input domain.UpdateUserInput, actor domain.User) (domain.User, error) {
	if input.DisplayName == nil && input.Status == nil && input.Roles == nil {
		return domain.User{}, domain.NewProblem("VALIDATION_FAILED", "至少需要更新一个用户字段", false)
	}
	var displayName string
	var err error
	if input.DisplayName != nil {
		displayName, err = validateDisplayName(*input.DisplayName)
		if err != nil {
			return domain.User{}, err
		}
	}
	var roles []string
	if input.Roles != nil {
		roles, err = normalizeRoles(*input.Roles)
		if err != nil {
			return domain.User{}, err
		}
	}
	if input.Status != nil && *input.Status != "active" && *input.Status != "disabled" {
		return domain.User{}, domain.NewProblem("VALIDATION_FAILED", "用户状态必须是 active 或 disabled", false)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	if err := requirePlatformAdmin(ctx, tx, actor.ID); err != nil {
		return domain.User{}, err
	}

	var currentEmail, currentDisplayName, currentStatus string
	var currentRoles []string
	var createdAt time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT u.email, u.display_name, u.status, u.created_at,
		       COALESCE(array_agg(r.role_key) FILTER (WHERE r.role_key IS NOT NULL), '{}') as roles
		FROM users u
		LEFT JOIN user_roles ur ON u.id = ur.user_id
		LEFT JOIN roles r ON ur.role_id = r.id
		WHERE u.id = $1
		GROUP BY u.email, u.display_name, u.status, u.created_at
	`, userID).Scan(&currentEmail, &currentDisplayName, &currentStatus, &createdAt, &currentRoles)
	if err == sql.ErrNoRows {
		return domain.User{}, domain.NewProblem("NOT_FOUND", "用户不存在", false)
	}
	if err != nil {
		return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法查询用户", true)
	}

	futureStatus := currentStatus
	if input.Status != nil {
		futureStatus = *input.Status
	}
	futureRoles := currentRoles
	if input.Roles != nil {
		futureRoles = roles
	}
	removesActiveAdmin := containsString(currentRoles, "platform_admin") && currentStatus == "active" &&
		(futureStatus != "active" || !containsString(futureRoles, "platform_admin"))
	if removesActiveAdmin {
		var adminCount int
		err = tx.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM user_roles ur
			JOIN roles r ON ur.role_id = r.id
			JOIN users u ON u.id = ur.user_id
			WHERE r.role_key = 'platform_admin' AND u.status = 'active'
		`).Scan(&adminCount)
		if err != nil {
			return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法统计平台管理员", true)
		}
		if adminCount <= 1 {
			return domain.User{}, domain.NewProblem("OPERATION_CONFLICT", "不能停用或降级最后一个平台管理员", false)
		}
	}

	now := time.Now().UTC()
	if input.DisplayName != nil {
		_, err = tx.ExecContext(ctx, `UPDATE users SET display_name = $1, updated_at = $2 WHERE id = $3`, displayName, now, userID)
	} else if input.Status != nil {
		_, err = tx.ExecContext(ctx, `UPDATE users SET status = $1, updated_at = $2 WHERE id = $3`, *input.Status, now, userID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE users SET updated_at = $1 WHERE id = $2`, now, userID)
	}
	if err != nil {
		return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法更新用户", true)
	}

	if input.Roles != nil {
		if _, err = tx.ExecContext(ctx, `DELETE FROM user_roles WHERE user_id = $1`, userID); err != nil {
			return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法更新用户角色", true)
		}
		for _, role := range roles {
			var roleID string
			err = tx.QueryRowContext(ctx, `SELECT id FROM roles WHERE role_key = $1`, role).Scan(&roleID)
			if err != nil {
				return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法查询角色", true)
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2)`, userID, roleID); err != nil {
				return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法分配角色", true)
			}
		}
	}

	if futureStatus != "active" {
		if _, err = tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = $1 WHERE user_id = $2 AND revoked_at IS NULL`, now, userID); err != nil {
			return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法撤销用户会话", true)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE password_reset_tokens SET consumed_at = $1 WHERE user_id = $2 AND consumed_at IS NULL`, now, userID); err != nil {
			return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法撤销重置令牌", true)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = $1 WHERE user_id = $2 AND revoked_at IS NULL`, now, userID); err != nil {
			return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法撤销 API Token", true)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE console_connection_tokens SET consumed_at = $1 WHERE user_id = $2 AND consumed_at IS NULL`, now, userID); err != nil {
			return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法撤销控制台连接令牌", true)
		}
	}

	_, _ = tx.ExecContext(ctx, `
		INSERT INTO audit_events (actor_id, actor_type, action, target_type, target_id, result, trace_id, created_at)
		VALUES ($1, 'user', 'user.update', 'user', $2, 'success', $3, $4)
	`, actor.ID, userID, id.New(), now)

	if err := tx.Commit(); err != nil {
		return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法提交事务", true)
	}

	updated := domain.User{
		ID:          userID,
		Email:       currentEmail,
		DisplayName: currentDisplayName,
		Roles:       futureRoles,
		Status:      futureStatus,
		CreatedAt:   createdAt,
		UpdatedAt:   now,
	}
	if input.DisplayName != nil {
		updated.DisplayName = displayName
	}
	return updated, nil
}

// IssuePasswordResetToken issues a single-use password reset token for a user.
func (s *Postgres) IssuePasswordResetToken(userID string, actor domain.User) (domain.PasswordResetToken, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := requirePlatformAdminDB(ctx, s.db, actor.ID); err != nil {
		return domain.PasswordResetToken{}, err
	}

	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM users WHERE id = $1`, userID).Scan(&status)
	if err == sql.ErrNoRows {
		auditIdentityFailure(ctx, s.db, "auth.password_reset.issue", "user", userID)
		return domain.PasswordResetToken{}, domain.NewProblem("NOT_FOUND", "用户不存在", false)
	}
	if err != nil {
		return domain.PasswordResetToken{}, domain.NewProblem("INTERNAL_ERROR", "无法查询用户", true)
	}
	if status != "active" {
		auditIdentityFailure(ctx, s.db, "auth.password_reset.issue", "user", userID)
		return domain.PasswordResetToken{}, domain.NewProblem("OPERATION_CONFLICT", "已停用用户不能签发密码重置令牌", false)
	}

	token := randomToken()
	expiresAt := time.Now().UTC().Add(passwordResetTTL)
	digest := tokenDigest(token)

	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO password_reset_tokens (user_id, token_digest, expires_at)
		VALUES ($1, $2, $3)
	`, userID, digest[:], expiresAt); err != nil {
		return domain.PasswordResetToken{}, domain.NewProblem("INTERNAL_ERROR", "无法保存密码重置令牌", true)
	}

	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO audit_events (actor_id, actor_type, action, target_type, target_id, result, trace_id, created_at)
		VALUES ($1, 'user', 'auth.password_reset.issue', 'user', $2, 'success', $3, $4)
	`, actor.ID, userID, id.New(), time.Now().UTC())

	return domain.PasswordResetToken{Token: token, ExpiresAt: expiresAt}, nil
}

// ResetPassword consumes a password reset token and sets a new password,
// revoking every active session for the user in the same transaction.
func (s *Postgres) ResetPassword(token string, password string) error {
	if err := validatePassword(password); err != nil {
		return err
	}
	passwordHash, err := identity.HashPassword(password, identity.DefaultArgon2idParams())
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法安全保存新口令", true)
	}
	digest := tokenDigest(token)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	var userID string
	var expiresAt time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT user_id, expires_at
		FROM password_reset_tokens
		WHERE token_digest = $1 AND consumed_at IS NULL
		FOR UPDATE
	`, digest[:]).Scan(&userID, &expiresAt)
	if err == sql.ErrNoRows {
		return domain.NewProblem("AUTH_INVALID_RESET_TOKEN", "密码重置凭据无效或已过期", false)
	}
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法查询密码重置令牌", true)
	}
	if !time.Now().UTC().Before(expiresAt) {
		_, _ = tx.ExecContext(ctx, `UPDATE password_reset_tokens SET consumed_at = now() WHERE token_digest = $1`, digest[:])
		return domain.NewProblem("AUTH_INVALID_RESET_TOKEN", "密码重置凭据无效或已过期", false)
	}

	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM users WHERE id = $1`, userID).Scan(&status)
	if err == sql.ErrNoRows || status != "active" {
		_, _ = tx.ExecContext(ctx, `UPDATE password_reset_tokens SET consumed_at = now() WHERE token_digest = $1`, digest[:])
		return domain.NewProblem("AUTH_INVALID_RESET_TOKEN", "密码重置凭据无效或已过期", false)
	}
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法查询用户", true)
	}

	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = $1, updated_at = $2 WHERE id = $3`, passwordHash, now, userID); err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法更新口令", true)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE password_reset_tokens SET consumed_at = $1 WHERE user_id = $2 AND consumed_at IS NULL`, now, userID); err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法消费密码重置令牌", true)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = $1 WHERE user_id = $2 AND revoked_at IS NULL`, now, userID); err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法撤销用户会话", true)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = $1 WHERE user_id = $2 AND revoked_at IS NULL`, now, userID); err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法撤销 API Token", true)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE console_connection_tokens SET consumed_at = $1 WHERE user_id = $2 AND consumed_at IS NULL`, now, userID); err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法撤销控制台连接令牌", true)
	}

	_, _ = tx.ExecContext(ctx, `
		INSERT INTO audit_events (actor_type, action, target_type, target_id, result, trace_id, created_at)
		VALUES ('system', 'auth.password_reset.consume', 'user', $1, 'success', $2, $3)
	`, userID, id.New(), now)

	if err := tx.Commit(); err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法提交事务", true)
	}
	return nil
}

// ServerMembership returns the membership of a user on a server.
func (s *Postgres) ServerMembership(serverID string, userID string) (domain.ServerMembership, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var member domain.ServerMembership
	var permissions []string
	err := s.db.QueryRowContext(ctx, `
		SELECT server_id, user_id, permissions, created_at, updated_at
		FROM server_members
		WHERE server_id = $1 AND user_id = $2
	`, serverID, userID).Scan(&member.ServerID, &member.UserID, pq.Array(&permissions), &member.CreatedAt, &member.UpdatedAt)
	if err == sql.ErrNoRows {
		return domain.ServerMembership{}, domain.NewProblem("NOT_FOUND", "该用户不是服务器成员", false)
	}
	if err != nil {
		return domain.ServerMembership{}, domain.NewProblem("INTERNAL_ERROR", "无法查询服务器成员关系", true)
	}
	member.Permissions = permissions
	return member, nil
}

// PutServerMembership upserts the permissions of a user on a server.
func (s *Postgres) PutServerMembership(serverID string, userID string, permissions []string, actor domain.User) (domain.ServerMembership, error) {
	permissions, err := normalizePermissions(permissions)
	if err != nil {
		return domain.ServerMembership{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ServerMembership{}, domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	if err := requirePlatformAdmin(ctx, tx, actor.ID); err != nil {
		return domain.ServerMembership{}, err
	}

	var serverExists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM servers WHERE id = $1)`, serverID).Scan(&serverExists); err != nil {
		return domain.ServerMembership{}, domain.NewProblem("INTERNAL_ERROR", "无法检查服务器", true)
	}
	if !serverExists {
		return domain.ServerMembership{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}

	var userStatus string
	err = tx.QueryRowContext(ctx, `SELECT status FROM users WHERE id = $1`, userID).Scan(&userStatus)
	if err == sql.ErrNoRows || userStatus != "active" {
		return domain.ServerMembership{}, domain.NewProblem("NOT_FOUND", "用户不存在", false)
	}
	if err != nil {
		return domain.ServerMembership{}, domain.NewProblem("INTERNAL_ERROR", "无法检查用户", true)
	}

	member := domain.ServerMembership{ServerID: serverID, UserID: userID, Permissions: permissions}
	err = tx.QueryRowContext(ctx, `
		INSERT INTO server_members (server_id, user_id, permissions, created_at, updated_at)
		VALUES ($1, $2, $3, now(), now())
		ON CONFLICT (server_id, user_id) DO UPDATE
		SET permissions = EXCLUDED.permissions, updated_at = now()
		RETURNING created_at, updated_at
	`, serverID, userID, permissions).Scan(&member.CreatedAt, &member.UpdatedAt)
	if err != nil {
		return domain.ServerMembership{}, domain.NewProblem("INTERNAL_ERROR", "无法保存服务器成员关系", true)
	}

	_, _ = tx.ExecContext(ctx, `
		INSERT INTO audit_events (actor_id, actor_type, action, target_type, target_id, result, trace_id, created_at)
		VALUES ($1, 'user', 'server.membership.put', 'server', $2, 'success', $3, $4)
	`, actor.ID, serverID, id.New(), time.Now().UTC())

	if err := tx.Commit(); err != nil {
		return domain.ServerMembership{}, domain.NewProblem("INTERNAL_ERROR", "无法提交事务", true)
	}
	return member, nil
}

// DeleteServerMembership removes a user's membership on a server.
func (s *Postgres) DeleteServerMembership(serverID string, userID string, actor domain.User) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法开始事务", true)
	}
	defer tx.Rollback()

	if err := requirePlatformAdmin(ctx, tx, actor.ID); err != nil {
		return err
	}

	var serverExists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM servers WHERE id = $1)`, serverID).Scan(&serverExists); err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法检查服务器", true)
	}
	if !serverExists {
		return domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM server_members WHERE server_id = $1 AND user_id = $2`, serverID, userID)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法删除服务器成员关系", true)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return domain.NewProblem("NOT_FOUND", "服务器成员关系不存在", false)
	}

	_, _ = tx.ExecContext(ctx, `
		INSERT INTO audit_events (actor_id, actor_type, action, target_type, target_id, result, trace_id, created_at)
		VALUES ($1, 'user', 'server.membership.delete', 'server', $2, 'success', $3, $4)
	`, actor.ID, serverID, id.New(), time.Now().UTC())

	if err := tx.Commit(); err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法提交事务", true)
	}
	return nil
}

// ValidateAgentToken verifies an agent token using a constant-time comparison.
func (s *Postgres) ValidateAgentToken(token string) bool {
	digest := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(digest[:], s.agentToken[:]) == 1
}

// auditIdentityFailure records a failed identity operation for audit purposes.
func auditIdentityFailure(ctx context.Context, db *sql.DB, action string, targetType string, targetID string) {
	_, _ = db.ExecContext(ctx, `
		INSERT INTO audit_events (actor_type, action, target_type, target_id, result, trace_id, created_at)
		VALUES ('system', $1, $2, $3, 'failure', $4, $5)
	`, action, targetType, targetID, id.New(), time.Now().UTC())
}
