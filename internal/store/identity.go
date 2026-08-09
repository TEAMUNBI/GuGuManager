package store

import (
	"crypto/subtle"
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/id"
	"github.com/gugumanager/gugumanager/internal/identity"
)

const passwordResetTTL = 15 * time.Minute

// passwordHashFunc is kept behind a package-level seam so the identity state
// machine can be tested without paying the production Argon2 cost. Production
// code always uses identity.HashPassword; tests replace it temporarily to
// prove that invalid or replayed credentials never reach the expensive step.
var passwordHashFunc = identity.HashPassword

var allowedGlobalRoles = map[string]struct{}{
	"platform_admin": {},
	"server_owner":   {},
}

var allowedServerPermissions = map[string]struct{}{
	"servers.read":            {},
	"servers.power":           {},
	"servers.console":         {},
	"servers.files.read":      {},
	"servers.files.write":     {},
	"servers.backups.read":    {},
	"servers.backups.create":  {},
	"servers.backups.restore": {},
	"servers.backups.delete":  {},
	"servers.network.read":    {},
	"servers.network.write":   {},
	"servers.startup.read":    {},
	"servers.startup.write":   {},
}

func (m *Memory) SetupStatus() domain.SetupStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	status := domain.SetupStatus{Required: m.setupRequired}
	if m.setupRequired {
		expiresAt := m.bootstrapExpiresAt
		status.BootstrapExpiresAt = &expiresAt
	}
	return status
}

func (m *Memory) SetupAdmin(input domain.SetupAdminInput) (domain.User, error) {
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
	providedDigest := tokenDigest(input.BootstrapToken)
	m.mu.RLock()
	if err := m.validateSetupLocked(providedDigest, time.Now().UTC()); err != nil {
		m.mu.RUnlock()
		return domain.User{}, err
	}
	m.mu.RUnlock()

	passwordHash, err := passwordHashFunc(input.Password, identity.DefaultArgon2idParams())
	if err != nil {
		return domain.User{}, domain.NewProblem("INTERNAL_ERROR", "无法安全保存管理员口令", true)
	}

	m.mu.Lock()
	now := time.Now().UTC()
	if err := m.validateSetupLocked(providedDigest, now); err != nil {
		m.mu.Unlock()
		return domain.User{}, err
	}
	user := domain.User{
		ID: id.New(), Email: email, DisplayName: displayName, Roles: []string{"platform_admin"},
		Status: "active", CreatedAt: now, UpdatedAt: now,
	}
	m.users[user.ID] = storedUser{User: user, PasswordPHC: passwordHash}
	m.userOrder = append(m.userOrder, user.ID)
	m.userByEmail[email] = user.ID
	m.setupRequired = false
	m.bootstrapToken = [32]byte{}
	m.bootstrapExpiresAt = time.Time{}
	m.recordAuditLocked(user.DisplayName, "setup.admin.create", "user", user.ID, "success", "")
	m.mu.Unlock()
	return cloneUser(user), nil
}

func (m *Memory) Users() []domain.User {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.User, 0, len(m.userOrder))
	for _, userID := range m.userOrder {
		if record, ok := m.users[userID]; ok {
			result = append(result, cloneUser(record.User))
		}
	}
	return result
}

func (m *Memory) UserByID(userID string) (domain.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	record, ok := m.users[userID]
	if !ok {
		return domain.User{}, domain.NewProblem("NOT_FOUND", "用户不存在", false)
	}
	return cloneUser(record.User), nil
}

func (m *Memory) CreateUser(input domain.CreateUserInput, actor domain.User) (domain.User, error) {
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
	now := time.Now().UTC()

	m.mu.Lock()
	if err := m.requirePlatformAdminLocked(actor.ID); err != nil {
		m.mu.Unlock()
		return domain.User{}, err
	}
	if _, exists := m.userByEmail[email]; exists {
		m.mu.Unlock()
		return domain.User{}, domain.NewProblem("EMAIL_CONFLICT", "该邮箱已被使用", false)
	}
	user := domain.User{
		ID: id.New(), Email: email, DisplayName: displayName, Roles: roles,
		Status: "active", CreatedAt: now, UpdatedAt: now,
	}
	m.users[user.ID] = storedUser{User: user, PasswordPHC: passwordHash}
	m.userOrder = append(m.userOrder, user.ID)
	m.userByEmail[email] = user.ID
	m.recordAuditLocked(actor.DisplayName, "user.create", "user", user.ID, "success", "")
	m.mu.Unlock()
	return cloneUser(user), nil
}

func (m *Memory) UpdateUser(userID string, input domain.UpdateUserInput, actor domain.User) (domain.User, error) {
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

	m.mu.Lock()
	if err := m.requirePlatformAdminLocked(actor.ID); err != nil {
		m.mu.Unlock()
		return domain.User{}, err
	}
	record, ok := m.users[userID]
	if !ok {
		m.mu.Unlock()
		return domain.User{}, domain.NewProblem("NOT_FOUND", "用户不存在", false)
	}
	futureStatus := record.User.Status
	if input.Status != nil {
		futureStatus = *input.Status
	}
	futureRoles := record.User.Roles
	if input.Roles != nil {
		futureRoles = roles
	}
	removesActiveAdmin := hasRole(record.User, "platform_admin") && record.User.Status == "active" &&
		(futureStatus != "active" || !containsString(futureRoles, "platform_admin"))
	if removesActiveAdmin && m.activeAdminCountLocked() <= 1 {
		m.mu.Unlock()
		return domain.User{}, domain.NewProblem("OPERATION_CONFLICT", "不能停用或降级最后一个平台管理员", false)
	}
	if input.DisplayName != nil {
		record.User.DisplayName = displayName
	}
	if input.Status != nil {
		record.User.Status = *input.Status
	}
	if input.Roles != nil {
		record.User.Roles = roles
	}
	record.User.UpdatedAt = time.Now().UTC()
	m.users[userID] = record
	if record.User.Status != "active" {
		m.revokeUserSessionsLocked(userID)
		m.revokePasswordResetTokensLocked(userID)
	}
	m.recordAuditLocked(actor.DisplayName, "user.update", "user", userID, "success", "")
	m.mu.Unlock()
	return cloneUser(record.User), nil
}

func (m *Memory) IssuePasswordResetToken(userID string, actor domain.User) (domain.PasswordResetToken, error) {
	m.mu.Lock()
	if err := m.requirePlatformAdminLocked(actor.ID); err != nil {
		m.mu.Unlock()
		return domain.PasswordResetToken{}, err
	}
	record, ok := m.users[userID]
	if !ok {
		m.recordAuditLocked(actor.DisplayName, "auth.password_reset.issue", "user", userID, "failure", "")
		m.mu.Unlock()
		return domain.PasswordResetToken{}, domain.NewProblem("NOT_FOUND", "用户不存在", false)
	}
	if record.User.Status != "active" {
		m.recordAuditLocked(actor.DisplayName, "auth.password_reset.issue", "user", userID, "failure", "")
		m.mu.Unlock()
		return domain.PasswordResetToken{}, domain.NewProblem("OPERATION_CONFLICT", "已停用用户不能签发密码重置令牌", false)
	}
	token := randomToken()
	expiresAt := time.Now().UTC().Add(passwordResetTTL)
	digest := tokenDigest(token)
	m.passwordResetTokens[digest] = passwordResetRecord{UserID: userID, ExpiresAt: expiresAt}
	m.recordAuditLocked(actor.DisplayName, "auth.password_reset.issue", "user", userID, "success", "")
	m.mu.Unlock()
	return domain.PasswordResetToken{Token: token, ExpiresAt: expiresAt}, nil
}

func (m *Memory) ResetPassword(token string, password string) error {
	if err := validatePassword(password); err != nil {
		return err
	}
	digest := tokenDigest(token)
	m.mu.RLock()
	if err := m.validateResetTokenLocked(digest, time.Now().UTC()); err != nil {
		m.mu.RUnlock()
		m.mu.Lock()
		delete(m.passwordResetTokens, digest)
		m.mu.Unlock()
		return err
	}
	m.mu.RUnlock()

	passwordHash, err := passwordHashFunc(password, identity.DefaultArgon2idParams())
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法安全保存新口令", true)
	}

	m.mu.Lock()
	now := time.Now().UTC()
	if err := m.validateResetTokenLocked(digest, now); err != nil {
		delete(m.passwordResetTokens, digest)
		m.mu.Unlock()
		return err
	}
	reset := m.passwordResetTokens[digest]
	record := m.users[reset.UserID]
	record.PasswordPHC = passwordHash
	record.User.UpdatedAt = now
	m.users[record.User.ID] = record
	for candidate, pending := range m.passwordResetTokens {
		if pending.UserID == record.User.ID {
			delete(m.passwordResetTokens, candidate)
		}
	}
	m.revokeUserSessionsLocked(record.User.ID)
	m.recordAuditLocked(record.User.DisplayName, "auth.password_reset.consume", "user", record.User.ID, "success", "")
	m.mu.Unlock()
	return nil
}

func (m *Memory) validateSetupLocked(providedDigest [32]byte, now time.Time) error {
	if !m.setupRequired || len(m.users) > 0 {
		return domain.NewProblem("SETUP_ALREADY_COMPLETE", "初始化已完成", false)
	}
	if !now.Before(m.bootstrapExpiresAt) || subtle.ConstantTimeCompare(providedDigest[:], m.bootstrapToken[:]) != 1 {
		return domain.NewProblem("BOOTSTRAP_TOKEN_INVALID", "初始化凭据无效或已过期", false)
	}
	return nil
}

func (m *Memory) validateResetTokenLocked(digest [32]byte, now time.Time) error {
	reset, ok := m.passwordResetTokens[digest]
	record, userOK := m.users[reset.UserID]
	if !ok || !userOK || record.User.Status != "active" || !now.Before(reset.ExpiresAt) {
		return domain.NewProblem("AUTH_INVALID_RESET_TOKEN", "密码重置凭据无效或已过期", false)
	}
	return nil
}

func (m *Memory) ServerMembership(serverID string, userID string) (domain.ServerMembership, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	membership, ok := m.memberships[serverID][userID]
	if !ok {
		return domain.ServerMembership{}, domain.NewProblem("NOT_FOUND", "服务器成员关系不存在", false)
	}
	return cloneMembership(membership), nil
}

func (m *Memory) PutServerMembership(serverID string, userID string, permissions []string, actor domain.User) (domain.ServerMembership, error) {
	permissions, err := normalizePermissions(permissions)
	if err != nil {
		return domain.ServerMembership{}, err
	}
	now := time.Now().UTC()

	m.mu.Lock()
	if err := m.requirePlatformAdminLocked(actor.ID); err != nil {
		m.mu.Unlock()
		return domain.ServerMembership{}, err
	}
	if _, ok := m.servers[serverID]; !ok {
		m.mu.Unlock()
		return domain.ServerMembership{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	user, ok := m.users[userID]
	if !ok || user.User.Status != "active" {
		m.mu.Unlock()
		return domain.ServerMembership{}, domain.NewProblem("NOT_FOUND", "用户不存在", false)
	}
	if m.memberships[serverID] == nil {
		m.memberships[serverID] = map[string]domain.ServerMembership{}
	}
	membership, exists := m.memberships[serverID][userID]
	if !exists {
		membership = domain.ServerMembership{ServerID: serverID, UserID: userID, CreatedAt: now}
	}
	membership.Permissions = permissions
	membership.UpdatedAt = now
	m.memberships[serverID][userID] = membership
	m.recordAuditLocked(actor.DisplayName, "server.membership.put", "server", serverID, "success", "")
	m.mu.Unlock()
	return cloneMembership(membership), nil
}

func (m *Memory) DeleteServerMembership(serverID string, userID string, actor domain.User) error {
	m.mu.Lock()
	if err := m.requirePlatformAdminLocked(actor.ID); err != nil {
		m.mu.Unlock()
		return err
	}
	if _, ok := m.servers[serverID]; !ok {
		m.mu.Unlock()
		return domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	members := m.memberships[serverID]
	if _, ok := members[userID]; !ok {
		m.mu.Unlock()
		return domain.NewProblem("NOT_FOUND", "服务器成员关系不存在", false)
	}
	delete(members, userID)
	m.recordAuditLocked(actor.DisplayName, "server.membership.delete", "server", serverID, "success", "")
	m.mu.Unlock()
	return nil
}

func (m *Memory) AuthorizeServer(userID string, serverID string, permission string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	user, userOK := m.users[userID]
	if !userOK || user.User.Status != "active" {
		return domain.NewProblem("AUTH_REQUIRED", "请先登录", false)
	}
	if _, serverOK := m.servers[serverID]; !serverOK {
		return domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	if hasRole(user.User, "platform_admin") {
		return nil
	}
	membership, member := m.memberships[serverID][userID]
	if !member {
		return domain.NewProblem("NOT_FOUND", "服务器不存在或未授权", false)
	}
	if !containsString(membership.Permissions, permission) {
		return domain.NewProblem("FORBIDDEN", "缺少服务器操作权限", false)
	}
	return nil
}

// EffectiveServerPermissions returns the permissions the current actor can
// use for one server. It intentionally resolves from the authoritative store
// instead of exposing an arbitrary user's membership to another member.
func (m *Memory) EffectiveServerPermissions(userID string, serverID string) (domain.ServerPermissions, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	user, userOK := m.users[userID]
	if !userOK || user.User.Status != "active" {
		return domain.ServerPermissions{}, domain.NewProblem("AUTH_REQUIRED", "请先登录", false)
	}
	if _, serverOK := m.servers[serverID]; !serverOK {
		return domain.ServerPermissions{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}

	permissions := []string(nil)
	if hasRole(user.User, "platform_admin") {
		permissions = allServerPermissions()
	} else {
		membership, member := m.memberships[serverID][userID]
		if !member || !containsString(membership.Permissions, "servers.read") {
			return domain.ServerPermissions{}, domain.NewProblem("NOT_FOUND", "服务器不存在或未授权", false)
		}
		permissions = append([]string(nil), membership.Permissions...)
		sort.Strings(permissions)
	}
	return domain.ServerPermissions{ServerID: serverID, Permissions: permissions}, nil
}

func (m *Memory) VisibleServers(userID string, query string) []domain.Server {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reconcileNodeLivenessLocked(time.Now().UTC())
	user, ok := m.users[userID]
	if !ok || user.User.Status != "active" {
		return []domain.Server{}
	}
	admin := hasRole(user.User, "platform_admin")
	query = strings.ToLower(strings.TrimSpace(query))
	result := make([]domain.Server, 0, len(m.serverOrder))
	for _, serverID := range m.serverOrder {
		if !admin {
			membership, member := m.memberships[serverID][userID]
			if !member || !containsString(membership.Permissions, "servers.read") {
				continue
			}
		}
		server := m.servers[serverID]
		if query != "" && !strings.Contains(strings.ToLower(server.Name+" "+server.GameName+" "+server.NodeName), query) {
			continue
		}
		result = append(result, server)
	}
	return result
}

func normalizeEmail(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	parsed, err := mail.ParseAddress(normalized)
	if err != nil || parsed.Address != normalized || len([]rune(normalized)) > 254 {
		return "", domain.NewProblem("VALIDATION_FAILED", "邮箱格式无效", false)
	}
	return normalized, nil
}

func validateDisplayName(value string) (string, error) {
	value = strings.TrimSpace(value)
	length := len([]rune(value))
	if length < 1 || length > 64 {
		return "", domain.NewProblem("VALIDATION_FAILED", "显示名需要在 1 到 64 个字符之间", false)
	}
	return value, nil
}

func validatePassword(value string) error {
	length := len([]rune(value))
	if length < 8 || length > 1024 {
		return domain.NewProblem("VALIDATION_FAILED", "密码需要在 8 到 1024 个字符之间", false)
	}
	return nil
}

func normalizeRoles(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, domain.NewProblem("VALIDATION_FAILED", "用户至少需要一个角色", false)
	}
	seen := map[string]struct{}{}
	roles := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if _, allowed := allowedGlobalRoles[value]; !allowed {
			return nil, domain.NewProblem("VALIDATION_FAILED", "包含不支持的用户角色", false)
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		roles = append(roles, value)
	}
	sort.Strings(roles)
	return roles, nil
}

func normalizePermissions(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, domain.NewProblem("VALIDATION_FAILED", "membership 至少需要一个权限", false)
	}
	seen := map[string]struct{}{}
	permissions := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if _, allowed := allowedServerPermissions[value]; !allowed {
			return nil, domain.NewProblem("VALIDATION_FAILED", "包含不支持的服务器权限", false)
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		permissions = append(permissions, value)
	}
	sort.Strings(permissions)
	if _, readable := seen["servers.read"]; !readable {
		return nil, domain.NewProblem("VALIDATION_FAILED", "membership 必须包含 servers.read", false)
	}
	return permissions, nil
}

func allServerPermissions() []string {
	permissions := make([]string, 0, len(allowedServerPermissions))
	for permission := range allowedServerPermissions {
		permissions = append(permissions, permission)
	}
	sort.Strings(permissions)
	return permissions
}

func (m *Memory) revokeUserSessionsLocked(userID string) {
	for digest, session := range m.sessions {
		if session.UserID == userID {
			delete(m.sessions, digest)
		}
	}
}

func (m *Memory) revokePasswordResetTokensLocked(userID string) {
	for digest, record := range m.passwordResetTokens {
		if record.UserID == userID {
			delete(m.passwordResetTokens, digest)
		}
	}
}

func (m *Memory) activeAdminCountLocked() int {
	count := 0
	for _, record := range m.users {
		if record.User.Status == "active" && hasRole(record.User, "platform_admin") {
			count++
		}
	}
	return count
}

func (m *Memory) requirePlatformAdminLocked(userID string) error {
	record, ok := m.users[userID]
	if !ok || record.User.Status != "active" || !hasRole(record.User, "platform_admin") {
		return domain.NewProblem("FORBIDDEN", "需要平台管理员权限", false)
	}
	return nil
}

// currentActorLocked resolves the actor from the authoritative identity store.
// Callers must hold m.mu. The User value supplied by an HTTP/session layer is
// deliberately not trusted for status or role decisions.
func (m *Memory) currentActorLocked(userID string) (domain.User, error) {
	record, ok := m.users[userID]
	if !ok || record.User.Status != "active" {
		return domain.User{}, domain.NewProblem("FORBIDDEN", "当前用户已无权执行该操作", false)
	}
	return cloneUser(record.User), nil
}

// authorizeServerLocked performs the Store-side authorization check for a
// server mutation. Platform administrators bypass membership permissions;
// other users need an active membership carrying the requested permission.
// Callers must hold m.mu.
func (m *Memory) authorizeServerLocked(actorID string, serverID string, permission string) (domain.User, error) {
	actor, err := m.currentActorLocked(actorID)
	if err != nil {
		return domain.User{}, err
	}
	if _, ok := m.servers[serverID]; !ok {
		return domain.User{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	if hasRole(actor, "platform_admin") {
		return actor, nil
	}
	membership, ok := m.memberships[serverID][actorID]
	if !ok {
		return domain.User{}, domain.NewProblem("NOT_FOUND", "服务器不存在或未授权", false)
	}
	if !containsString(membership.Permissions, permission) {
		return domain.User{}, domain.NewProblem("FORBIDDEN", "缺少服务器操作权限", false)
	}
	return actor, nil
}

func cloneUser(user domain.User) domain.User {
	user.Roles = append([]string(nil), user.Roles...)
	return user
}

func cloneMembership(membership domain.ServerMembership) domain.ServerMembership {
	membership.Permissions = append([]string(nil), membership.Permissions...)
	return membership
}

func hasRole(user domain.User, role string) bool {
	return containsString(user.Roles, role)
}
