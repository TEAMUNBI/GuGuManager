package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	serverfiles "github.com/gugumanager/gugumanager/internal/files"
	_ "github.com/lib/pq"
)

// AgentFileError 携带 Agent 返回的稳定错误码，供 store 层映射为 domain.Problem。
type AgentFileError struct {
	Code string
}

func (e *AgentFileError) Error() string {
	return fmt.Sprintf("agent file operation: %s", e.Code)
}

// FileDispatcher 将容器内文件操作转发到对应节点上的 Agent 执行。
// 由 agentrpc.Server 实现，通过 SetFileDispatcher 注入 Postgres store。
type FileDispatcher interface {
	ListFiles(ctx context.Context, nodeID, serverID, path string) ([]domain.FileEntry, error)
	ReadFile(ctx context.Context, nodeID, serverID, path string) (domain.FileContent, error)
	WriteFile(ctx context.Context, nodeID, serverID, path string, content []byte, base64 bool) error
	MakeDirectory(ctx context.Context, nodeID, serverID, path string) error
	MoveFile(ctx context.Context, nodeID, serverID, source, destination string, replace bool) error
	RemoveFile(ctx context.Context, nodeID, serverID, path string, recursive bool) error
	DownloadBackup(ctx context.Context, nodeID, serverID, backupID string) (domain.BackupContent, error)
}

// Postgres implements the Store interface with PostgreSQL persistence.
// It replaces the in-memory development adapter for production deployments.
type Postgres struct {
	db                   *sql.DB
	mu                   sync.RWMutex
	environment          string
	agentToken           [32]byte
	bootstrapTokenDigest [32]byte
	fileRoot             string
	fileMutationGates    sync.Map
	fileSystems          map[string]*serverfiles.ServerFS
	fileDispatcher       FileDispatcher
	// 控制台日志与服务器指标的内存缓冲（生产链路由 Agent 帧上报写入）。
	bufMu          sync.Mutex
	consoleBuffers map[string]*consoleBuffer
	metricStates   map[string]*metricState
	// 实时控制台日志订阅中心（WebSocket 推送）。
	consoleHub *consoleHub
	// Secret 启动变量的静态加密器；未注入时加密禁用（明文存储）。
	secretCipher *secretCipher
	// secretKeyring is preferred for production writes and supports rotation.
	secretKeyring *secretKeyring
}

// NewPostgres creates a new PostgreSQL-backed store.
// The database connection string should include sslmode and appropriate timeouts.
func NewPostgres(ctx context.Context, dsn string, environment string, agentToken string, fileRoot string) (*Postgres, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(1 * time.Minute)

	// Verify connection
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	var agentTokenBytes [32]byte = sha256.Sum256([]byte(agentToken))

	store := &Postgres{
		db:             db,
		environment:    environment,
		agentToken:     agentTokenBytes,
		fileRoot:       fileRoot,
		fileSystems:    map[string]*serverfiles.ServerFS{},
		consoleBuffers: map[string]*consoleBuffer{},
		metricStates:   map[string]*metricState{},
		consoleHub:     newConsoleHub(),
	}

	return store, nil
}

// SetBootstrapToken 记录 bootstrap token 的 SHA-256 摘要，供 SetupAdmin 校验。
// 仅用于首次初始化；生产从 GUGU_BOOTSTRAP_TOKEN_FILE 读取后调用。
func (s *Postgres) SetBootstrapToken(token string) {
	if token == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bootstrapTokenDigest = sha256.Sum256([]byte(token))
}

// SetFileDispatcher 注入远程 Agent 文件操作调度器。
// 注入后，文件读写操作经由目标节点上的 Agent 在容器内执行，
// 而非访问控制面本地文件系统。
func (s *Postgres) SetFileDispatcher(fd FileDispatcher) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fileDispatcher = fd
}

// SetSecretCipher 注入 Secret 启动变量的静态加密器。密钥来自
// GUGU_ENCRYPTION_KEY_FILE（生产必填）；development 不注入则加密禁用。
// 注入应在任何 Startup 读写之前完成；已落库的明文旧数据（无 enc:v1: 前缀）
// 仍可正常读取，不会被破坏。
func (s *Postgres) SetSecretCipher(masterKey []byte) error {
	if len(masterKey) == 0 {
		return nil
	}
	sealer, err := newSecretCipher(masterKey)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secretCipher = sealer
	return nil
}

// SetSecretKeyring injects the production Secret keyring. The active key is
// used for new writes while all configured keys remain available for reads
// during rotation. It is safe to call before any Startup operation.
func (s *Postgres) SetSecretKeyring(activeID string, keys map[string][]byte) error {
	keyring, err := newSecretKeyring(activeID, keys)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secretKeyring = keyring
	return nil
}

// secretCipherLocked 返回当前加密器（无锁调用方持有 mu 时使用）。
func (s *Postgres) secretCipherLocked() *secretCipher {
	return s.secretCipher
}

// Close closes the database connection pool.
func (s *Postgres) Close() error {
	return s.db.Close()
}

// Environment returns the runtime environment identifier.
func (s *Postgres) Environment() string {
	return s.environment
}
