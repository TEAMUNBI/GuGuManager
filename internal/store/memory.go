package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	serverfiles "github.com/gugumanager/gugumanager/internal/files"
	"github.com/gugumanager/gugumanager/internal/id"
	"github.com/gugumanager/gugumanager/internal/identity"
)

type idempotencyRecord struct {
	OperationID   string
	RequestDigest [32]byte
}

type storedUser struct {
	User        domain.User
	PasswordPHC string
}

type passwordResetRecord struct {
	UserID    string
	ExpiresAt time.Time
}

type Memory struct {
	mu                  sync.RWMutex
	fileMutationGates   sync.Map
	environment         string
	adminEmail          string
	adminPasswordPHC    string
	agentToken          [32]byte
	requestDigestKey    [32]byte
	operationLatency    time.Duration
	runtimeAdapter      *RuntimeAdapter // nil = simulated, non-nil = real Docker
	users               map[string]storedUser
	userOrder           []string
	userByEmail         map[string]string
	sessions            map[[32]byte]domain.Session
	memberships         map[string]map[string]domain.ServerMembership
	passwordResetTokens map[[32]byte]passwordResetRecord
	setupRequired       bool
	bootstrapToken      [32]byte
	bootstrapExpiresAt  time.Time
	servers             map[string]domain.Server
	serverOrder         []string
	allocations         map[string]domain.Allocation
	allocationOrder     map[string][]string
	startups            map[string]domain.Startup
	startupValues       map[string]map[string]any
	nodes               map[string]domain.Node
	nodeOrder           []string
	games               map[string]domain.GameDefinition
	gameOrder           []string
	operations          map[string]domain.Operation
	idempotency         map[string]idempotencyRecord
	audit               []domain.AuditEvent
	console             map[string][]domain.ConsoleLine
	consoleHub          *consoleHub
	files               map[string][]domain.FileEntry
	fileSystems         map[string]*serverfiles.ServerFS
	// fileMutationHook is a development/test seam invoked while the Store
	// write lock is held immediately before a physical file mutation. It is
	// nil in normal operation.
	fileMutationHook func()
	fileRoot         string
	ownedFileRoot    bool
	backups          map[string][]domain.Backup
	backupChecksums  map[string]string
}

func NewMemory(environment string, adminEmail string, adminPassword string, agentToken string, operationLatency time.Duration) *Memory {
	root, err := os.MkdirTemp("", "gugu-manager-memory-")
	if err != nil {
		panic("initialize development file root: " + err.Error())
	}
	store, err := newMemoryAt(environment, adminEmail, adminPassword, agentToken, operationLatency, root, true)
	if err != nil {
		panic("initialize development file system: " + err.Error())
	}
	return store
}

// NewMemoryAt is used by the control plane and integration tests when the
// server-data root must be explicit. It creates one isolated directory per
// seeded server below dataRoot.
func NewMemoryAt(environment string, adminEmail string, adminPassword string, agentToken string, operationLatency time.Duration, dataRoot string) (*Memory, error) {
	return newMemoryAt(environment, adminEmail, adminPassword, agentToken, operationLatency, dataRoot, false)
}

func newMemoryAt(environment string, adminEmail string, adminPassword string, agentToken string, operationLatency time.Duration, dataRoot string, ownedFileRoot bool) (*Memory, error) {
	passwordForDummyHash := adminPassword
	if passwordForDummyHash == "" {
		passwordForDummyHash = randomToken()
	}
	adminHash, err := identity.HashPassword(passwordForDummyHash, identity.DefaultArgon2idParams())
	if err != nil {
		return nil, fmt.Errorf("initialize development administrator password: %w", err)
	}
	agentHash := sha256.Sum256([]byte(agentToken))
	var requestDigestKey [32]byte
	if _, err := rand.Read(requestDigestKey[:]); err != nil {
		return nil, fmt.Errorf("initialize idempotency digest key: %w", err)
	}
	store := &Memory{
		environment:         environment,
		adminEmail:          strings.ToLower(strings.TrimSpace(adminEmail)),
		adminPasswordPHC:    adminHash,
		agentToken:          agentHash,
		requestDigestKey:    requestDigestKey,
		operationLatency:    operationLatency,
		users:               map[string]storedUser{},
		userByEmail:         map[string]string{},
		sessions:            map[[32]byte]domain.Session{},
		memberships:         map[string]map[string]domain.ServerMembership{},
		passwordResetTokens: map[[32]byte]passwordResetRecord{},
		servers:             map[string]domain.Server{},
		allocations:         map[string]domain.Allocation{},
		allocationOrder:     map[string][]string{},
		startups:            map[string]domain.Startup{},
		startupValues:       map[string]map[string]any{},
		nodes:               map[string]domain.Node{},
		games:               map[string]domain.GameDefinition{},
		operations:          map[string]domain.Operation{},
		idempotency:         map[string]idempotencyRecord{},
		console:             map[string][]domain.ConsoleLine{},
		consoleHub:          newConsoleHub(),
		files:               map[string][]domain.FileEntry{},
		fileSystems:         map[string]*serverfiles.ServerFS{},
		fileRoot:            filepath.Clean(dataRoot),
		ownedFileRoot:       ownedFileRoot,
		backups:             map[string][]domain.Backup{},
		backupChecksums:     map[string]string{},
	}
	if store.adminEmail != "" && adminPassword != "" {
		now := time.Now().UTC()
		admin := domain.User{
			ID: "00000000-0000-4000-8000-000000000001", Email: store.adminEmail,
			DisplayName: "GuGu Admin", Roles: []string{"platform_admin"}, Status: "active",
			CreatedAt: now, UpdatedAt: now,
		}
		store.users[admin.ID] = storedUser{User: admin, PasswordPHC: adminHash}
		store.userOrder = append(store.userOrder, admin.ID)
		store.userByEmail[admin.Email] = admin.ID
	}
	if err := store.seed(); err != nil {
		if ownedFileRoot {
			_ = os.RemoveAll(dataRoot)
		}
		return nil, fmt.Errorf("initialize development catalog: %w", err)
	}
	store.initializeBackupChecksums()
	if err := store.initializeFileSystems(); err != nil {
		if ownedFileRoot {
			_ = os.RemoveAll(dataRoot)
		}
		return nil, err
	}
	return store, nil
}

// NewMemoryForSetup creates an uninitialized development adapter for setup
// contract tests and local bootstrap flows. The bootstrap token is kept only as
// a digest and the setup entry point closes after the first administrator.
func NewMemoryForSetup(environment string, bootstrapToken string, bootstrapExpiresAt time.Time, agentToken string, operationLatency time.Duration) *Memory {
	root, err := os.MkdirTemp("", "gugu-manager-setup-")
	if err != nil {
		panic("initialize setup file root: " + err.Error())
	}
	store, err := newMemoryForSetupAt(environment, bootstrapToken, bootstrapExpiresAt, agentToken, operationLatency, root, true)
	if err != nil {
		panic("initialize setup memory store: " + err.Error())
	}
	return store
}

// NewMemoryForSetupAt is the explicit-data-root variant used by the optional
// development bootstrap mode in the control-plane entry point.
func NewMemoryForSetupAt(environment string, bootstrapToken string, bootstrapExpiresAt time.Time, agentToken string, operationLatency time.Duration, dataRoot string) (*Memory, error) {
	return newMemoryForSetupAt(environment, bootstrapToken, bootstrapExpiresAt, agentToken, operationLatency, dataRoot, false)
}

func newMemoryForSetupAt(environment string, bootstrapToken string, bootstrapExpiresAt time.Time, agentToken string, operationLatency time.Duration, dataRoot string, ownedFileRoot bool) (*Memory, error) {
	store, err := newMemoryAt(environment, "", "", agentToken, operationLatency, dataRoot, ownedFileRoot)
	if err != nil {
		return nil, err
	}
	store.setupRequired = true
	store.bootstrapToken = tokenDigest(bootstrapToken)
	store.bootstrapExpiresAt = bootstrapExpiresAt.UTC()
	return store, nil
}

func (m *Memory) Close() error {
	if !m.ownedFileRoot || m.fileRoot == "" {
		return nil
	}
	return os.RemoveAll(m.fileRoot)
}

func (m *Memory) Login(email string, password string) (domain.SessionView, string, error) {
	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	m.mu.RLock()
	userID, found := m.userByEmail[normalizedEmail]
	record := m.users[userID]
	passwordHash := m.adminPasswordPHC
	if found {
		passwordHash = record.PasswordPHC
	}
	m.mu.RUnlock()
	passwordMatch, hashErr := identity.VerifyPassword(passwordHash, password)
	if !found || record.User.Status != "active" || hashErr != nil || !passwordMatch {
		m.recordAudit("Unknown actor", "auth.login", "session", "Control Plane", "failure", id.New())
		return domain.SessionView{}, "", domain.NewProblem("AUTH_INVALID_CREDENTIALS", "邮箱或密码错误", false)
	}

	token := randomToken()
	csrf := randomToken()
	user := cloneUser(record.User)
	session := domain.Session{CSRFToken: csrf, UserID: user.ID, User: user, ExpiresAt: time.Now().Add(12 * time.Hour)}
	m.mu.Lock()
	current, currentOK := m.users[user.ID]
	if !currentOK || current.User.Status != "active" || current.PasswordPHC != passwordHash {
		m.mu.Unlock()
		m.recordAudit("Unknown actor", "auth.login", "session", "Control Plane", "failure", id.New())
		return domain.SessionView{}, "", domain.NewProblem("AUTH_INVALID_CREDENTIALS", "邮箱或密码错误", false)
	}
	user = cloneUser(current.User)
	session.User = user
	m.sessions[tokenDigest(token)] = session
	m.mu.Unlock()
	m.recordAudit(user.DisplayName, "auth.login", "session", "Control Plane", "success", id.New())
	return domain.SessionView{User: user, CSRFToken: csrf, Environment: m.environment}, token, nil
}

func (m *Memory) Session(token string) (domain.SessionView, error) {
	digest := tokenDigest(token)
	m.mu.Lock()
	session, ok := m.sessions[digest]
	record, userOK := m.users[session.UserID]
	if !ok || !userOK || record.User.Status != "active" || time.Now().After(session.ExpiresAt) {
		delete(m.sessions, digest)
		m.mu.Unlock()
		return domain.SessionView{}, domain.NewProblem("AUTH_REQUIRED", "请先登录", false)
	}
	user := cloneUser(record.User)
	m.mu.Unlock()
	return domain.SessionView{User: user, CSRFToken: session.CSRFToken, Environment: m.environment}, nil
}

func (m *Memory) Logout(token string) {
	digest := tokenDigest(token)
	m.mu.Lock()
	session, ok := m.sessions[digest]
	delete(m.sessions, digest)
	m.mu.Unlock()
	if ok {
		m.recordAudit(session.User.DisplayName, "auth.logout", "session", "Control Plane", "success", id.New())
	}
}

func (m *Memory) ValidateCSRF(token string, csrf string) bool {
	digest := tokenDigest(token)
	m.mu.RLock()
	session, ok := m.sessions[digest]
	record, userOK := m.users[session.UserID]
	m.mu.RUnlock()
	return ok && userOK && record.User.Status == "active" && time.Now().Before(session.ExpiresAt) && subtle.ConstantTimeCompare([]byte(session.CSRFToken), []byte(csrf)) == 1
}

func (m *Memory) ValidateAgentToken(token string) bool {
	hash := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(hash[:], m.agentToken[:]) == 1
}

func (m *Memory) Overview() domain.Overview {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reconcileNodeLivenessLocked(time.Now().UTC())
	result := domain.Overview{Environment: m.environment, ServerCount: len(m.servers), TotalNodeCount: len(m.nodes)}
	for _, server := range m.servers {
		if server.ObservedPower == "running" {
			result.RunningServerCount++
		}
		result.CPUPercent += server.Metrics.CPUPercent
		result.MemoryUsedBytes += server.Metrics.MemoryBytes
		result.MemoryTotalBytes += server.Metrics.MemoryLimit
	}
	for _, node := range m.nodes {
		if node.Condition == "available" {
			result.OnlineNodeCount++
		}
	}
	for _, operation := range m.operations {
		if operation.Status != "succeeded" && operation.Status != "failed" && operation.Status != "canceled" {
			result.QueuedOperationCount++
		}
	}
	limit := 5
	if len(m.audit) < limit {
		limit = len(m.audit)
	}
	result.RecentActivity = append([]domain.AuditEvent(nil), m.audit[:limit]...)
	return result
}

func (m *Memory) Servers(query string) []domain.Server {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reconcileNodeLivenessLocked(time.Now().UTC())
	query = strings.ToLower(strings.TrimSpace(query))
	result := make([]domain.Server, 0, len(m.serverOrder))
	for _, serverID := range m.serverOrder {
		server := m.servers[serverID]
		if query != "" && !strings.Contains(strings.ToLower(server.Name+" "+server.GameName+" "+server.NodeName), query) {
			continue
		}
		result = append(result, server)
	}
	return result
}

func (m *Memory) Server(serverID string) (domain.Server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reconcileNodeLivenessLocked(time.Now().UTC())
	server, ok := m.servers[serverID]
	if !ok {
		return domain.Server{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	return server, nil
}

func (m *Memory) Nodes() []domain.Node {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reconcileNodeLivenessLocked(time.Now().UTC())
	result := make([]domain.Node, 0, len(m.nodeOrder))
	for _, nodeID := range m.nodeOrder {
		result = append(result, m.nodes[nodeID])
	}
	return result
}

func (m *Memory) GameDefinitions() []domain.GameDefinition {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.GameDefinition, 0, len(m.gameOrder))
	for _, gameID := range m.gameOrder {
		result = append(result, m.games[gameID])
	}
	return result
}

func (m *Memory) Operation(operationID string) (domain.Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	operation, ok := m.operations[operationID]
	if !ok {
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "操作不存在", false)
	}
	normalizeOperationMetadata(&operation)
	m.operations[operationID] = operation
	return operation, nil
}

func (m *Memory) AuditEvents() []domain.AuditEvent {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]domain.AuditEvent(nil), m.audit...)
}

func (m *Memory) Console(serverID string) ([]domain.ConsoleLine, error) {
	if _, err := m.Server(serverID); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]domain.ConsoleLine{}, m.console[serverID]...), nil
}

// SubscribeConsoleLines 订阅服务器实时控制台日志（WebSocket 推送用）。
// 与 Postgres 版语义一致：返回 channel 与取消函数。
func (m *Memory) SubscribeConsoleLines(serverID string) (<-chan domain.ConsoleLine, func()) {
	return m.consoleHub.Subscribe(serverID)
}

func (m *Memory) Files(serverID string, requestedPath string) ([]domain.FileEntry, error) {
	if _, err := m.Server(serverID); err != nil {
		return nil, err
	}
	filesystem, err := m.serverFileSystem(serverID)
	if err != nil {
		return nil, err
	}
	entries, err := filesystem.List(requestedPath)
	if err != nil {
		return nil, mapFileError(err)
	}
	result := make([]domain.FileEntry, 0, len(entries))
	for _, entry := range entries {
		kind := "file"
		if entry.Directory {
			kind = "directory"
		}
		result = append(result, domain.FileEntry{Name: entry.Name, Path: entry.Path, Kind: kind, SizeBytes: entry.SizeBytes, ModifiedAt: entry.ModifiedAt})
	}
	return result, nil
}

func (m *Memory) Backups(serverID string) ([]domain.Backup, error) {
	if _, err := m.Server(serverID); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]domain.Backup{}, m.backups[serverID]...), nil
}

func (m *Memory) recordAudit(actor string, action string, targetType string, targetName string, result string, operationID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recordAuditLocked(actor, action, targetType, targetName, result, operationID)
}

func (m *Memory) recordAuditLocked(actor string, action string, targetType string, targetName string, result string, operationID string) {
	event := domain.AuditEvent{
		ID: id.New(), ActorName: actor, Action: action, TargetType: targetType,
		TargetName: targetName, Result: result, OperationID: operationID, CreatedAt: time.Now().UTC(),
	}
	m.audit = append([]domain.AuditEvent{event}, m.audit...)
}

func randomToken() string {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer)
}

func tokenDigest(token string) [32]byte {
	return sha256.Sum256([]byte(token))
}
