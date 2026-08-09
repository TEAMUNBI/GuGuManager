package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/id"
	"github.com/gugumanager/gugumanager/internal/identity"
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
		err := rows.Scan(&user.ID, &user.Email, &user.DisplayName, &user.Status, &user.CreatedAt, &user.UpdatedAt, &roles)
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
	`, userID).Scan(&user.ID, &user.Email, &user.DisplayName, &user.Status, &user.CreatedAt, &user.UpdatedAt, &roles)

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
