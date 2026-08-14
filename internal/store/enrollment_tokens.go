package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gugumanager/gugumanager/internal/domain"
)

// EnrollmentTokenStore 颁发与消费一次性 Agent 注册令牌。明文令牌只在颁发
// 响应中出现一次；存储侧只有 SHA-256 摘要，消费是原子的单次操作。
type EnrollmentTokenStore interface {
	IssueEnrollmentToken(ctx context.Context, actorID, nodeNameHint string, ttl time.Duration) (token string, expiresAt time.Time, err error)
	ConsumeEnrollmentToken(ctx context.Context, rawToken string) error
}

const enrollmentTokenBytes = 32

// enrollmentTokenTTL 默认有效期与最大有效期：短期令牌，单次使用。
const (
	enrollmentTokenDefaultTTL   = 24 * time.Hour
	enrollmentTokenMaxTTL       = 7 * 24 * time.Hour
	enrollmentTokenMaxHintRunes = 100
)

func normalizeEnrollmentTokenInput(nodeNameHint string, ttl time.Duration) (string, time.Duration, error) {
	nodeNameHint = strings.TrimSpace(nodeNameHint)
	if utf8.RuneCountInString(nodeNameHint) > enrollmentTokenMaxHintRunes {
		return "", 0, domain.NewProblem("VALIDATION_FAILED", "节点名称提示不能超过 100 个字符", false)
	}
	if ttl < 0 || ttl > enrollmentTokenMaxTTL {
		return "", 0, domain.NewProblem("VALIDATION_FAILED", "注册令牌有效期必须在 1 秒至 7 天之间", false)
	}
	if ttl == 0 {
		ttl = enrollmentTokenDefaultTTL
	}
	return nodeNameHint, ttl, nil
}

func enrollmentTokenDigest(token string) [32]byte {
	return sha256.Sum256([]byte(token))
}

// IssueEnrollmentToken 生成 256 位随机令牌，只保存摘要并返回明文一次。
func (s *Postgres) IssueEnrollmentToken(ctx context.Context, actorID, nodeNameHint string, ttl time.Duration) (string, time.Time, error) {
	var err error
	if nodeNameHint, ttl, err = normalizeEnrollmentTokenInput(nodeNameHint, ttl); err != nil {
		return "", time.Time{}, err
	}
	raw := make([]byte, enrollmentTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, domain.NewProblem("INTERNAL_ERROR", "无法生成注册令牌", true)
	}
	token := hex.EncodeToString(raw)
	digest := enrollmentTokenDigest(token)
	expiresAt := time.Now().UTC().Add(ttl)
	var actorIDValue, hintValue any
	if actorID != "" {
		actorIDValue = actorID
	}
	if nodeNameHint != "" {
		hintValue = nodeNameHint
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO enrollment_tokens (token_digest, node_name_hint, created_by, expires_at)
		VALUES ($1, $2, $3, $4)
	`, digest[:], hintValue, actorIDValue, expiresAt); err != nil {
		return "", time.Time{}, domain.NewProblem("INTERNAL_ERROR", "无法保存注册令牌", true)
	}
	return token, expiresAt, nil
}

// ConsumeEnrollmentToken 原子地消费一枚注册令牌：摘要匹配、未消费且未过期
// 才成功；重放、篡改或过期一律拒绝。
func (s *Postgres) ConsumeEnrollmentToken(ctx context.Context, rawToken string) error {
	if rawToken == "" {
		return domain.NewProblem("ENROLLMENT_TOKEN_INVALID", "注册令牌无效或已过期", false)
	}
	digest := enrollmentTokenDigest(rawToken)
	result, err := s.db.ExecContext(ctx, `
		UPDATE enrollment_tokens
		SET consumed_at = now()
		WHERE token_digest = $1 AND consumed_at IS NULL AND expires_at > now()
	`, digest[:])
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法校验注册令牌", true)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法校验注册令牌", true)
	}
	if affected != 1 {
		return domain.NewProblem("ENROLLMENT_TOKEN_INVALID", "注册令牌无效或已过期", false)
	}
	return nil
}

// RevokeNode 立即吊销节点：后续 Connect/心跳全部被拒，正在运行的租约
// 也随对账过期回收。幂等：已吊销的节点再次吊销返回 NOT_FOUND。
func (s *Postgres) RevokeNode(nodeID string, actor domain.User) error {
	if actor.ID == "" || !hasRole(actor, "platform_admin") {
		return domain.NewProblem("FORBIDDEN", "需要平台管理员权限", false)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := s.db.ExecContext(ctx, `
		UPDATE nodes SET revoked_at = now()
		WHERE id = $1 AND revoked_at IS NULL
	`, nodeID)
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法吊销节点", true)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return domain.NewProblem("INTERNAL_ERROR", "无法吊销节点", true)
	}
	if affected != 1 {
		return domain.NewProblem("NOT_FOUND", "节点不存在或已吊销", false)
	}
	return nil
}

// IssueAgentEnrollmentToken 是 HTTP 面向的一次性注册令牌颁发：管理员调用，
// 明文令牌只返回一次。
func (s *Postgres) IssueAgentEnrollmentToken(nodeNameHint string, ttl time.Duration, actor domain.User) (string, time.Time, error) {
	if actor.ID == "" || !hasRole(actor, "platform_admin") {
		return "", time.Time{}, domain.NewProblem("FORBIDDEN", "需要平台管理员权限", false)
	}
	return s.IssueEnrollmentToken(context.Background(), actor.ID, nodeNameHint, ttl)
}

// memory 实现：开发模式一次性注册令牌（同进程内单次消费）。
func (m *Memory) IssueEnrollmentToken(ctx context.Context, actorID, nodeNameHint string, ttl time.Duration) (string, time.Time, error) {
	var err error
	if nodeNameHint, ttl, err = normalizeEnrollmentTokenInput(nodeNameHint, ttl); err != nil {
		return "", time.Time{}, err
	}
	raw := make([]byte, enrollmentTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, domain.NewProblem("INTERNAL_ERROR", "无法生成注册令牌", true)
	}
	token := hex.EncodeToString(raw)
	digest := enrollmentTokenDigest(token)
	expiresAt := time.Now().UTC().Add(ttl)
	m.mu.Lock()
	if m.enrollmentTokens == nil {
		m.enrollmentTokens = make(map[[32]byte]time.Time)
	}
	m.enrollmentTokens[digest] = expiresAt
	m.mu.Unlock()
	return token, expiresAt, nil
}

func (m *Memory) ConsumeEnrollmentToken(ctx context.Context, rawToken string) error {
	if rawToken == "" {
		return domain.NewProblem("ENROLLMENT_TOKEN_INVALID", "注册令牌无效或已过期", false)
	}
	digest := enrollmentTokenDigest(rawToken)
	m.mu.Lock()
	expiresAt, ok := m.enrollmentTokens[digest]
	if ok {
		delete(m.enrollmentTokens, digest)
	}
	m.mu.Unlock()
	if !ok || !time.Now().UTC().Before(expiresAt) {
		return domain.NewProblem("ENROLLMENT_TOKEN_INVALID", "注册令牌无效或已过期", false)
	}
	return nil
}

var _ EnrollmentTokenStore = (*Postgres)(nil)
var _ EnrollmentTokenStore = (*Memory)(nil)

// IssueAgentEnrollmentToken 是开发模式的 HTTP 面颁发：行为与生产一致，
// 令牌在进程内单次消费，进程退出即失效。
func (m *Memory) IssueAgentEnrollmentToken(nodeNameHint string, ttl time.Duration, actor domain.User) (string, time.Time, error) {
	m.mu.RLock()
	currentActor, authErr := m.currentActorLocked(actor.ID)
	m.mu.RUnlock()
	if authErr != nil || !hasRole(currentActor, "platform_admin") {
		return "", time.Time{}, domain.NewProblem("FORBIDDEN", "需要平台管理员权限", false)
	}
	return m.IssueEnrollmentToken(context.Background(), actor.ID, nodeNameHint, ttl)
}

// RevokeNode 立即吊销节点：节点从列表消失，心跳被拒。
func (m *Memory) RevokeNode(nodeID string, actor domain.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	currentActor, authErr := m.currentActorLocked(actor.ID)
	if authErr != nil || !hasRole(currentActor, "platform_admin") {
		return domain.NewProblem("FORBIDDEN", "需要平台管理员权限", false)
	}
	if _, ok := m.nodes[nodeID]; !ok {
		return domain.NewProblem("NOT_FOUND", "节点不存在", false)
	}
	if m.revokedNodes == nil {
		m.revokedNodes = make(map[string]bool)
	}
	m.revokedNodes[nodeID] = true
	return nil
}
