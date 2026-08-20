package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/id"
	"github.com/lib/pq"
)

const consoleConnectionTTL = time.Minute

var allowedAPITokenScopes = func() map[string]struct{} {
	result := map[string]struct{}{
		"platform.admin": {}, "audit.read": {}, "nodes.manage": {},
		"catalog.manage": {}, "automation.manage": {}, "notifications.manage": {},
		"storage.manage": {}, "quotas.manage": {},
	}
	for permission := range allowedServerPermissions {
		result[permission] = struct{}{}
	}
	return result
}()

func normalizeAPITokenInput(input domain.CreateAPITokenInput, now time.Time) (domain.CreateAPITokenInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	if length := len([]rune(input.Name)); length < 1 || length > 64 {
		return input, domain.NewProblem("VALIDATION_FAILED", "API Token 名称需要在 1 到 64 个字符之间", false)
	}
	if len(input.Scopes) == 0 {
		return input, domain.NewProblem("VALIDATION_FAILED", "API Token 至少需要一个 scope", false)
	}
	seen := make(map[string]struct{}, len(input.Scopes))
	scopes := make([]string, 0, len(input.Scopes))
	for _, scope := range input.Scopes {
		scope = strings.TrimSpace(scope)
		if _, ok := allowedAPITokenScopes[scope]; !ok {
			return input, domain.NewProblem("VALIDATION_FAILED", "API Token 包含不支持的 scope", false)
		}
		if _, duplicate := seen[scope]; duplicate {
			continue
		}
		seen[scope] = struct{}{}
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	input.Scopes = scopes
	if input.ExpiresAt != nil {
		expires := input.ExpiresAt.UTC()
		if !expires.After(now.Add(time.Minute)) || expires.After(now.Add(366*24*time.Hour)) {
			return input, domain.NewProblem("VALIDATION_FAILED", "API Token 有效期必须在 1 分钟到 366 天之间", false)
		}
		input.ExpiresAt = &expires
	}
	return input, nil
}

func (m *Memory) CreateAPIToken(input domain.CreateAPITokenInput, actor domain.User) (domain.APITokenCredential, error) {
	now := time.Now().UTC()
	normalized, err := normalizeAPITokenInput(input, now)
	if err != nil {
		return domain.APITokenCredential{}, err
	}
	plain := randomToken()
	digest := tokenDigest(plain)
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.currentActorLocked(actor.ID)
	if err != nil {
		return domain.APITokenCredential{}, err
	}
	token := domain.APIToken{ID: id.New(), Name: normalized.Name, Scopes: normalized.Scopes, ExpiresAt: normalized.ExpiresAt, CreatedAt: now}
	m.apiTokens[token.ID] = memoryAPITokenRecord{Token: token, UserID: current.ID}
	m.apiTokenByDigest[digest] = token.ID
	m.recordAuditLocked(current.DisplayName, "api_token.create", "api_token", token.Name, "success", id.New())
	return domain.APITokenCredential{APIToken: token, Token: plain}, nil
}

func (m *Memory) APITokens(userID string) []domain.APIToken {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.APIToken, 0)
	for _, record := range m.apiTokens {
		if record.UserID == userID {
			token := record.Token
			token.Scopes = append([]string(nil), token.Scopes...)
			result = append(result, token)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result
}

func (m *Memory) RevokeAPIToken(tokenID string, actor domain.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.currentActorLocked(actor.ID)
	if err != nil {
		return err
	}
	record, ok := m.apiTokens[tokenID]
	if !ok || record.UserID != current.ID {
		return domain.NewProblem("NOT_FOUND", "API Token 不存在", false)
	}
	delete(m.apiTokens, tokenID)
	for digest, idValue := range m.apiTokenByDigest {
		if idValue == tokenID {
			delete(m.apiTokenByDigest, digest)
		}
	}
	m.recordAuditLocked(current.DisplayName, "api_token.revoke", "api_token", record.Token.Name, "success", id.New())
	return nil
}

func (m *Memory) AuthenticateAPIToken(plain string) (domain.APITokenPrincipal, error) {
	digest := tokenDigest(plain)
	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	tokenID, ok := m.apiTokenByDigest[digest]
	record, exists := m.apiTokens[tokenID]
	if !ok || !exists || (record.Token.ExpiresAt != nil && !record.Token.ExpiresAt.After(now)) {
		return domain.APITokenPrincipal{}, domain.NewProblem("AUTH_REQUIRED", "API Token 无效或已过期", false)
	}
	user, err := m.currentActorLocked(record.UserID)
	if err != nil {
		return domain.APITokenPrincipal{}, domain.NewProblem("AUTH_REQUIRED", "API Token 无效或已撤销", false)
	}
	record.Token.LastUsedAt = &now
	m.apiTokens[tokenID] = record
	return domain.APITokenPrincipal{User: user, Scopes: append([]string(nil), record.Token.Scopes...), APITokenID: tokenID, Environment: m.environment}, nil
}

func (m *Memory) IssueConsoleConnectionToken(serverID, userID string) (domain.ConsoleConnectionCredential, error) {
	now := time.Now().UTC()
	plain := randomToken()
	digest := tokenDigest(plain)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.authorizeServerLocked(userID, serverID, "servers.console"); err != nil {
		return domain.ConsoleConnectionCredential{}, err
	}
	expires := now.Add(consoleConnectionTTL)
	m.consoleTokens[digest] = domain.ConsoleConnectionPrincipal{UserID: userID, ServerID: serverID, ExpiresAt: expires}
	return domain.ConsoleConnectionCredential{Token: plain, ExpiresAt: expires}, nil
}

func (m *Memory) ConsumeConsoleConnectionToken(plain string) (domain.ConsoleConnectionPrincipal, error) {
	digest := tokenDigest(plain)
	m.mu.Lock()
	defer m.mu.Unlock()
	principal, ok := m.consoleTokens[digest]
	delete(m.consoleTokens, digest)
	if !ok || !principal.ExpiresAt.After(time.Now().UTC()) {
		return domain.ConsoleConnectionPrincipal{}, domain.NewProblem("AUTH_REQUIRED", "控制台连接令牌无效或已过期", false)
	}
	if _, err := m.authorizeServerLocked(principal.UserID, principal.ServerID, "servers.console"); err != nil {
		return domain.ConsoleConnectionPrincipal{}, domain.NewProblem("FORBIDDEN", "控制台访问已撤销", false)
	}
	return principal, nil
}

func (s *Postgres) CreateAPIToken(input domain.CreateAPITokenInput, actor domain.User) (domain.APITokenCredential, error) {
	now := time.Now().UTC()
	normalized, err := normalizeAPITokenInput(input, now)
	if err != nil {
		return domain.APITokenCredential{}, err
	}
	current, err := s.UserByID(actor.ID)
	if err != nil || current.Status != "active" {
		return domain.APITokenCredential{}, domain.NewProblem("FORBIDDEN", "当前用户已无权执行该操作", false)
	}
	plain := randomToken()
	digest := sha256.Sum256([]byte(plain))
	token := domain.APIToken{ID: id.New(), Name: normalized.Name, Scopes: normalized.Scopes, ExpiresAt: normalized.ExpiresAt, CreatedAt: now}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.APITokenCredential{}, domain.NewProblem("INTERNAL_ERROR", "无法创建 API Token", true)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO api_tokens (id, user_id, name, token_digest, scopes, expires_at, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`, token.ID, current.ID, token.Name, digest[:], pq.Array(token.Scopes), token.ExpiresAt, now); err != nil {
		return domain.APITokenCredential{}, domain.NewProblem("INTERNAL_ERROR", "无法创建 API Token", true)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events (actor_id, actor_type, action, target_type, target_id, result, trace_id, metadata) VALUES ($1,'user','api_token.create','api_token',$2,'success',$3,jsonb_build_object('name',$4))`, current.ID, token.ID, id.New(), token.Name); err != nil {
		return domain.APITokenCredential{}, domain.NewProblem("INTERNAL_ERROR", "无法记录 API Token 审计", true)
	}
	if err = tx.Commit(); err != nil {
		return domain.APITokenCredential{}, domain.NewProblem("INTERNAL_ERROR", "无法提交 API Token", true)
	}
	return domain.APITokenCredential{APIToken: token, Token: plain}, nil
}

func (s *Postgres) APITokens(userID string) []domain.APIToken {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT id::text, name, scopes, expires_at, last_used_at, created_at FROM api_tokens WHERE user_id=$1 AND revoked_at IS NULL ORDER BY created_at DESC`, userID)
	if err != nil {
		return []domain.APIToken{}
	}
	defer rows.Close()
	result := []domain.APIToken{}
	for rows.Next() {
		var token domain.APIToken
		if rows.Scan(&token.ID, &token.Name, pq.Array(&token.Scopes), &token.ExpiresAt, &token.LastUsedAt, &token.CreatedAt) == nil {
			result = append(result, token)
		}
	}
	return result
}

func (s *Postgres) RevokeAPIToken(tokenID string, actor domain.User) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET revoked_at=now() WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL`, tokenID, actor.ID)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法撤销 API Token", true)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return domain.NewProblem("NOT_FOUND", "API Token 不存在", false)
	}
	return nil
}

func (s *Postgres) AuthenticateAPIToken(plain string) (domain.APITokenPrincipal, error) {
	digest := sha256.Sum256([]byte(plain))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.APITokenPrincipal{}, domain.NewProblem("AUTH_REQUIRED", "API Token 无法验证", true)
	}
	defer tx.Rollback()
	var tokenID, userID string
	var scopes []string
	err = tx.QueryRowContext(ctx, `UPDATE api_tokens SET last_used_at=now() WHERE token_digest=$1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now()) RETURNING id::text,user_id::text,scopes`, digest[:]).Scan(&tokenID, &userID, pq.Array(&scopes))
	if err != nil {
		return domain.APITokenPrincipal{}, domain.NewProblem("AUTH_REQUIRED", "API Token 无效或已过期", false)
	}
	if err = tx.Commit(); err != nil {
		return domain.APITokenPrincipal{}, domain.NewProblem("AUTH_REQUIRED", "API Token 无法验证", true)
	}
	user, err := s.UserByID(userID)
	if err != nil || user.Status != "active" {
		return domain.APITokenPrincipal{}, domain.NewProblem("AUTH_REQUIRED", "API Token 无效或已撤销", false)
	}
	return domain.APITokenPrincipal{User: user, Scopes: scopes, APITokenID: tokenID, Environment: s.environment}, nil
}

func (s *Postgres) IssueConsoleConnectionToken(serverID, userID string) (domain.ConsoleConnectionCredential, error) {
	if err := s.AuthorizeServer(userID, serverID, "servers.console"); err != nil {
		return domain.ConsoleConnectionCredential{}, err
	}
	plain := randomToken()
	digest := sha256.Sum256([]byte(plain))
	expires := time.Now().UTC().Add(consoleConnectionTTL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO console_connection_tokens (user_id,server_id,token_digest,expires_at) VALUES ($1,$2,$3,$4)`, userID, serverID, digest[:], expires); err != nil {
		return domain.ConsoleConnectionCredential{}, domain.NewProblem("INTERNAL_ERROR", "无法签发控制台连接令牌", true)
	}
	return domain.ConsoleConnectionCredential{Token: plain, ExpiresAt: expires}, nil
}

func (s *Postgres) ConsumeConsoleConnectionToken(plain string) (domain.ConsoleConnectionPrincipal, error) {
	digest := sha256.Sum256([]byte(plain))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var principal domain.ConsoleConnectionPrincipal
	err := s.db.QueryRowContext(ctx, `UPDATE console_connection_tokens SET consumed_at=now() WHERE token_digest=$1 AND consumed_at IS NULL AND expires_at>now() RETURNING user_id::text,server_id::text,expires_at`, digest[:]).Scan(&principal.UserID, &principal.ServerID, &principal.ExpiresAt)
	if err == sql.ErrNoRows {
		return domain.ConsoleConnectionPrincipal{}, domain.NewProblem("AUTH_REQUIRED", "控制台连接令牌无效或已过期", false)
	}
	if err != nil {
		return domain.ConsoleConnectionPrincipal{}, domain.NewProblem("INTERNAL_ERROR", "无法验证控制台连接令牌", true)
	}
	if err := s.AuthorizeServer(principal.UserID, principal.ServerID, "servers.console"); err != nil {
		return domain.ConsoleConnectionPrincipal{}, domain.NewProblem("FORBIDDEN", "控制台访问已撤销", false)
	}
	return principal, nil
}
