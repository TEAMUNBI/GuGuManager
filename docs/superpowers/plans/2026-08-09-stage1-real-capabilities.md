# 阶段 1：真实垂直能力实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 GuGuManager 从"内存 Store + 模拟节点"的开发切片演进为**首个真实垂直能力**：PostgreSQL 持久化 Identity、真实 mTLS/gRPC Node Agent、真实 Docker OCI 安装与运行 PaperMC。

**Architecture:** 保留现有 `ControlPlane` 接口与 `httpapi` 层不变，新增完整 `Postgres` Store 适配器与 `Memory` 并列，由 `GUGU_ENVIRONMENT=production` 选择。Agent 通过出站 mTLS gRPC 连接 Control Plane，双向流承载心跳、任务下发与回报。Agent 侧 Docker Runtime 执行真实容器操作。任务通过持久 `server_tasks` 表（Outbox 模式）下发。

**Tech Stack:** Go 1.26、pgx/v5、google.golang.org/grpc、docker/docker v27、buf（proto 生成）、PostgreSQL 16-18、Redis 7.2（可选，本期仅缓存/限流）。

**前置约定：**
- Go 工具链：`C:\Users\andi\sdk\go1.26.5\bin\go.exe`（不在系统 PATH，所有 go 命令用该路径）
- 工作区无 `.git`，Task 0 先 `git init`
- 项目 go.sum 不完整，Task 0 先 `go mod tidy` 修复编译
- 所有操作在 `e:\项目\游戏面板\GuGuManager` 下进行
- 每个任务完成后按 `docs/changes/` 惯例新增 `GM-YYYYMMDD-NNN.md`

---

## Task 0：工程基线修复（git + 依赖 + 编译）

**Files:**
- Create: `.gitignore`（若不存在）
- Modify: `go.mod` / `go.sum`（通过 go mod tidy）
- Test: 无新测试，验证编译

- [ ] **Step 1: 初始化 git 仓库**

```powershell
cd "e:\项目\游戏面板\GuGuManager"
git init
```

- [ ] **Step 2: 修复依赖并补齐 go.sum**

```powershell
& "C:\Users\andi\sdk\go1.26.5\bin\go.exe" mod tidy
```

Expected: 无报错，go.sum 更新，`github.com/docker/docker`、`github.com/jackc/pgx/v5` 等条目补齐。

- [ ] **Step 3: 验证全量编译**

```powershell
& "C:\Users\andi\sdk\go1.26.5\bin\go.exe" build ./...
& "C:\Users\andi\sdk\go1.26.5\bin\go.exe" vet ./...
```

Expected: 均无输出、退出码 0。

- [ ] **Step 4: 确认当前测试基线**

```powershell
& "C:\Users\andi\sdk\go1.26.5\bin\go.exe" test ./... -count=1
```

Expected: 全部通过（当前环境无 PostgreSQL，migration 集成测试 Skip）。

- [ ] **Step 5: Commit**

```powershell
git add -A
git commit -m "chore: restore toolchain baseline, fix go.sum"
```

---

## Task 1：PostgreSQL Store — Identity 扩展（membership + reset 令牌 + bootstrap 校验）

**重要现状（Task 0 已核实）：** `internal/store/postgres.go` / `postgres_identity.go` / `postgres_session.go` 已存在且实现了部分 Identity：`SetupStatus`、`SetupAdmin`、`Users`、`UserByID`、`CreateUser`、`Login`、`Session`、`Logout`、`ValidateCSRF`。它们使用 `database/sql` + `lib/pq`（`Postgres.db *sql.DB` 字段，`NewPostgres(ctx, dsn, environment, agentToken, fileRoot)` 签名）。**不要**改成 pgxpool——保持现有 `database/sql` 风格，只在现有基础上扩展。

本任务补全缺失的 Identity 能力：
1. `SetupAdmin` 增加 bootstrap token 校验（当前完全不校验，生产不安全）
2. `UpdateUser`、`IssuePasswordResetToken`、`ResetPassword`
3. `ServerMembership`、`PutServerMembership`、`DeleteServerMembership`
4. `ValidateAgentToken`

**Files:**
- Modify: `internal/store/postgres.go` — 增加 `bootstrapTokenDigest [32]byte` 字段与 `SetBootstrapToken(token string)` 方法；保持 `NewPostgres` 签名不变
- Modify: `internal/store/postgres_identity.go` — 补全上述方法（保持无 ctx 签名，内部 `context.WithTimeout` + `s.db`）
- Create: `migrations/000004_password_reset.up.sql` / `migrations/000004_password_reset.down.sql`
- Test: `internal/store/postgres_identity_ext_test.go` — 真实 PostgreSQL 集成测试

- [ ] **Step 1: 编写失败测试**（postgres_identity_ext_test.go）

```go
package store

import (
	"os"
	"strings"
	"testing"

	"github.com/gugumanager/gugumanager/internal/domain"
)

func testPostgres(t *testing.T) *Postgres {
	t.Helper()
	dsn := os.Getenv("GUGU_TEST_DATABASE_URL")
	if dsn == "" || !strings.HasSuffix(dsn, "_test") {
		t.Skip("GUGU_TEST_DATABASE_URL required, must end in _test")
	}
	s, err := NewPostgres(context.Background(), dsn, Production, "test-agent-token-1234567890", "")
	if err != nil {
		t.Fatalf("new postgres: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPostgresMembershipAndReset(t *testing.T) {
	s := testPostgres(t)
	s.SetBootstrapToken("bootstrap-token-12345678901234567890123456789012")

	// Setup 必须校验 bootstrap token
	if _, err := s.SetupAdmin(domain.SetupAdminInput{Email: "admin@test.local", DisplayName: "Admin", Password: "correct-horse-battery", BootstrapToken: "wrong-token"}); err == nil {
		t.Fatal("expected wrong bootstrap token to be rejected")
	}
	admin, err := s.SetupAdmin(domain.SetupAdminInput{Email: "admin@test.local", DisplayName: "Admin", Password: "correct-horse-battery", BootstrapToken: "bootstrap-token-12345678901234567890123456789012"})
	if err != nil {
		t.Fatalf("setup admin: %v", err)
	}

	// membership 写入/读取/撤销
	member, err := s.PutServerMembership("server-1", admin.ID, []string{"servers.read", "servers.power"}, admin)
	if err != nil {
		t.Fatalf("put membership: %v", err)
	}
	if len(member.Permissions) != 2 {
		t.Fatalf("expected 2 permissions, got %v", member.Permissions)
	}
	got, err := s.ServerMembership("server-1", admin.ID)
	if err != nil || len(got.Permissions) != 2 {
		t.Fatalf("read membership: %v %v", got, err)
	}
	if err := s.DeleteServerMembership("server-1", admin.ID, admin); err != nil {
		t.Fatalf("delete membership: %v", err)
	}
	if _, err := s.ServerMembership("server-1", admin.ID); err == nil {
		t.Fatal("expected membership to be gone")
	}

	// reset 令牌签发与消费；旧会话应被撤销
	_, loginToken, err := s.Login("admin@test.local", "correct-horse-battery")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	resetToken, err := s.IssuePasswordResetToken(admin.ID, admin)
	if err != nil {
		t.Fatalf("issue reset token: %v", err)
	}
	if err := s.ResetPassword(resetToken.Token, "new-password-12345"); err != nil {
		t.Fatalf("reset password: %v", err)
	}
	if _, err := s.Session(loginToken); err == nil {
		t.Fatal("expected pre-reset session to be revoked")
	}
	if _, err := s.Login("admin@test.local", "new-password-12345"); err != nil {
		t.Fatalf("login with new password: %v", err)
	}
	if !s.ValidateAgentToken("test-agent-token-1234567890") {
		t.Fatal("expected agent token to validate")
	}
	if s.ValidateAgentToken("wrong-agent-token") {
		t.Fatal("expected wrong agent token to fail")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

本机已安装 PostgreSQL 17（服务 `postgresql-x64-17`，监听 127.0.0.1:5432，密码 `postgres`）。测试库 `gugu_identity_test` 已创建（若被删，重建）：

```powershell
$env:PGPASSWORD="postgres"; & "C:\Program Files\PostgreSQL\17\bin\psql.exe" -U postgres -h 127.0.0.1 -c "CREATE DATABASE gugu_identity_test;" 2>$null
$env:GUGU_TEST_DATABASE_URL="postgres://postgres:postgres@127.0.0.1:5432/gugu_identity_test"
& "C:\Users\andi\sdk\go1.26.5\bin\go.exe" test ./internal/store/ -run TestPostgresMembershipAndReset -v
```

Expected: FAIL——`SetBootstrapToken` 等方法不存在（编译错误或 `no such method`）。若本机 PG 不可用则测试 Skip，此时需在任一真实 PG（含远端）上验证；不得把 Skip 当通过。

- [ ] **Step 3: 扩展 postgres.go（bootstrap 字段，保持 database/sql）**

在 `Postgres` 结构体增加字段，并新增 `SetBootstrapToken` 方法（文件顶部需已 import `crypto/sha256`，若无则补）：

```go
type Postgres struct {
	db                   *sql.DB
	mu                   sync.RWMutex
	environment          string
	agentToken           [32]byte
	bootstrapTokenDigest [32]byte
	fileRoot             string
	fileMutationGates    sync.Map
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
```

**注意**：现有 `NewPostgres(ctx, dsn, environment, agentToken, fileRoot)` 的签名保持不变，`agentToken` 继续写入 `s.agentToken`（sha256 摘要）。不要引入 pgxpool。

- [ ] **Step 4: 补全 postgres_identity.go（membership + reset + bootstrap 校验）**

在现有 `SetupAdmin` 开头（email 校验前）插入 bootstrap 校验：

```go
func (s *Postgres) SetupAdmin(input domain.SetupAdminInput) (domain.User, error) {
	if err := s.verifyBootstrapToken(input.BootstrapToken); err != nil {
		return domain.User{}, err
	}
	// ... 原有校验与事务逻辑保持不变 ...
}
```

新增私有方法（放同一文件底部，需 import `crypto/sha256`）：

```go
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
```

其余新增方法签名（无 ctx，与 ControlPlane 接口一致）：

```go
func (s *Postgres) UpdateUser(userID string, input domain.UpdateUserInput, actor domain.User) (domain.User, error)
func (s *Postgres) IssuePasswordResetToken(userID string, actor domain.User) (domain.PasswordResetToken, error)
func (s *Postgres) ResetPassword(token string, password string) error
func (s *Postgres) ServerMembership(serverID string, userID string) (domain.ServerMembership, error)
func (s *Postgres) PutServerMembership(serverID string, userID string, permissions []string, actor domain.User) (domain.ServerMembership, error)
func (s *Postgres) DeleteServerMembership(serverID string, userID string, actor domain.User) error
func (s *Postgres) ValidateAgentToken(token string) bool
```

实现要点：
- `IssuePasswordResetToken`：校验 actor 是 platform_admin（复用 `requirePlatformAdmin`，注意它接收 `*sql.Tx`，若走非事务路径需改造成接收 `*sql.DB` 的重载）；生成随机 token（复用 `randomToken()`），只存 SHA-256 摘要到新表 `password_reset_tokens`；返回 `domain.PasswordResetToken{Token: rawToken, ExpiresAt: now+15m}`
- `ResetPassword`：按摘要查未消费且未过期的令牌 → 事务内更新 password_hash、标记 consumed、撤销该用户所有活跃 session（`UPDATE sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`）
- `ServerMembership`：查 `server_members`，无记录返回 `domain.NewProblem("NOT_FOUND", "该用户不是服务器成员", false)`
- `PutServerMembership`：actor 校验 platform_admin → upsert permissions（`INSERT ... ON CONFLICT (server_id, user_id) DO UPDATE SET permissions = EXCLUDED.permissions, updated_at = now()`）
- `ValidateAgentToken`：常量时间比较 `sha256.Sum256([]byte(token)) == s.agentToken`

参考 Memory 对应实现 `internal/store/identity.go` 与 `internal/store/memory.go` 的语义保持一致（状态码、错误码）。

- [ ] **Step 5: 实现 migration 000004**

Create: `migrations/000004_password_reset.up.sql`：

```sql
BEGIN;
CREATE TABLE password_reset_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_digest bytea NOT NULL UNIQUE CHECK (octet_length(token_digest) = 32),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX password_reset_tokens_user_idx ON password_reset_tokens (user_id) WHERE consumed_at IS NULL;
COMMIT;
```

Create: `migrations/000004_password_reset.down.sql`：

```sql
BEGIN;
DROP TABLE IF EXISTS password_reset_tokens;
COMMIT;
```

（可选但推荐：在 `internal/migrations/postgres_integration_test.go` 中按现有模式追加 000004 up/down 断言。）

- [ ] **Step 6: 测试转绿 + memory 回归**

```powershell
& "C:\Users\andi\sdk\go1.26.5\bin\go.exe" test ./internal/store/ -run TestPostgresMembershipAndReset -v
& "C:\Users\andi\sdk\go1.26.5\bin\go.exe" test ./internal/store/ -run TestMemory -count=1
& "C:\Users\andi\sdk\go1.26.5\bin\go.exe" test ./internal/store/ -run TestPostgres -count=1
```

Expected: 均 PASS（真实 PG 下）。

- [ ] **Step 7: Commit**

```powershell
git add internal/store/postgres.go internal/store/postgres_identity.go internal/store/postgres_identity_ext_test.go migrations/000004_password_reset.up.sql migrations/000004_password_reset.down.sql
git commit -m "feat(store): PostgreSQL membership, reset tokens, bootstrap validation"
```

---

## Task 2：PostgreSQL Store — 节点/服务器/任务/审计（1A 剩余 + 1D 持久层）

实现节点、服务器、server_tasks（Outbox 下发）、审计事件的 PG 读写。这是后续 Agent 任务持久化的底座。所有 ControlPlane 接口方法保持无 ctx 签名（与 httpapi.ControlPlane 一致），内部使用 context.Background()；新增的 Agent 专用方法（RegisterNode/ClaimTask/CompleteTask）可以带 ctx。

**Files:**
- Create: `internal/store/postgres_entities.go` — nodes/servers/allocations/backups
- Create: `internal/store/postgres_tasks.go` — server_tasks 队列（Enqueue、Claim、Complete）
- Test: `internal/store/postgres_entities_test.go`

- [ ] **Step 1: 失败测试：任务队列租约**

```go
func TestPostgresTaskClaimAndComplete(t *testing.T) {
	s := testPostgres(t)
	s.SetBootstrapToken("bootstrap-token-12345678901234567890123456789012")
	admin, err := s.SetupAdmin(domain.SetupAdminInput{Email: "admin2@test.local", DisplayName: "Admin", Password: "correct-horse-battery", BootstrapToken: "bootstrap-token-12345678901234567890123456789012"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	node, err := s.RegisterNode(context.Background(), domain.Node{Name: "n1", AgentVersion: "a1", ProtocolVersion: "p1", Condition: "available", Region: "cn", CPUCores: 4, MemoryBytes: 1 << 30, DiskBytes: 1 << 32})
	if err != nil {
		t.Fatalf("register node: %v", err)
	}
	// CreateServer 走 ControlPlane 接口（无 ctx）；需先有可用的 game_bundle 与 allocation，
	// 具体 input 字段以 domain.CreateServerInput 为准
	op, err := s.CreateServer(domain.CreateServerInput{Name: "test-srv", NodeID: node.ID, GameDefinitionID: "papermc", GameVersion: "1.21.4"}, "k1", admin)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	claimed, err := s.ClaimTask(context.Background(), node.ID)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	if claimed == nil || claimed.OperationID != op.ID {
		t.Fatalf("expected claimed task, got %+v", claimed)
	}
	if err := s.CompleteTask(context.Background(), claimed.OperationID, node.ID, true, nil); err != nil {
		t.Fatalf("complete task: %v", err)
	}
}
```

- [ ] **Step 2: 运行确认失败**

同上模式；无 PG 则 Skip。

- [ ] **Step 3: 实现 postgres_entities.go**

`RegisterNode`、`NodeByID`、`Nodes`、`CreateServer`、`Server`、`Servers`、`UpdateServerObserved`、`RecordAudit`。字段对齐 `domain.Node`/`domain.Server` 与 `000001_core.up.sql` 表结构。

- [ ] **Step 4: 实现 postgres_tasks.go**

```go
type ClaimedTask struct {
	OperationID string
	ServerID    string
	NodeID      string
	Generation  int64
	TaskType    string
	Attempt     int
	PayloadJSON []byte
}

func (s *Postgres) EnqueueTask(ctx context.Context, serverID, nodeID, taskType string, generation int64, actorID string, idemKey string, requestDigest []byte) (string, error)
func (s *Postgres) ClaimTask(ctx context.Context, nodeID string) (*ClaimedTask, error)
func (s *Postgres) CompleteTask(ctx context.Context, operationID, nodeID string, succeeded bool, errCode *string) error
```

`ClaimTask` 使用 `SELECT ... FOR UPDATE SKIP LOCKED` 配合 `server_tasks_claim_idx`；`CompleteTask` 更新终态并写审计。

- [ ] **Step 5: 测试转绿 + memory 回归**

- [ ] **Step 6: Commit**

```powershell
git commit -am "feat(store): PostgreSQL nodes, servers, task queue with lease claim"
```

---

## Task 3：Agent gRPC 契约生成 + mTLS CA（1B 基础设施）

**Files:**
- Modify: `buf.gen.yaml` — 添加 `connectrpc.com` 或 `grpc-go` 插件（决定：标准 gRPC，用 `protoc-gen-go-grpc`）
- Create: `api/proto/gugumanager/agent/v1/agent_grpc.pb.go`（生成）
- Create: `internal/agentca/` — CA 签发/验证包
- Test: `internal/agentca/agentca_test.go`

- [ ] **Step 1: 添加 grpc 依赖并更新 buf.gen.yaml**

```yaml
plugins:
  - remote: buf.build/protocolbuffers/go:v1.36.11
    out: api/proto
    opt: [paths=source_relative]
  - remote: buf.build/grpc/go:v1.5.1
    out: api/proto
    opt: [paths=source_relative]
```

```powershell
& "C:\Users\andi\sdk\go1.26.5\bin\go.exe" get google.golang.org/grpc@latest
& "C:\Users\andi\sdk\go1.26.5\bin\go.exe" get google.golang.org/protobuf@latest
```

- [ ] **Step 2: 重新生成 proto**

```powershell
& "C:\Users\andi\sdk\go1.26.5\bin\go.exe" run github.com/bufbuild/buf/cmd/buf@v1.72.0 generate
```

Expected: 生成 `agent_grpc.pb.go`，含 `AgentGatewayServiceServer`/`Client`。

- [ ] **Step 3: 失败测试 CA 包**

```go
func TestCABasicLifecycle(t *testing.T) {
	dir := t.TempDir()
	ca, err := agentca.NewCA(dir)
	if err != nil { t.Fatalf("new ca: %v", err) }
	certPEM, err := ca.IssueAgentCertificate("node-1", 24*time.Hour)
	if err != nil { t.Fatalf("issue: %v", err) }
	if err := ca.VerifyPeerCertificate(certPEM, "node-1"); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := ca.VerifyPeerCertificate(certPEM, "node-2"); err == nil {
		t.Fatal("expected subject mismatch to fail")
	}
}
```

- [ ] **Step 4: 实现 internal/agentca**

- 生成/加载自签根 CA（`GUGU_AGENT_CA_CERT_FILE` / `GUGU_AGENT_CA_KEY_FILE`），无则首启生成
- `IssueAgentCertificate(nodeID, ttl)`：校验 CSR 中 CN 与 nodeID 一致，签发叶证书（用途：client auth）
- `VerifyPeerCertificate(pem, expectedNodeID)`：校验证书链 + CN 匹配
- 支持吊销列表（`revoked_serials` 内存 + 持久化到 PG 后续任务接）

- [ ] **Step 5: 测试转绿**

```powershell
& "C:\Users\andi\sdk\go1.26.5\bin\go.exe" test ./internal/agentca/ -v
```

- [ ] **Step 6: Commit**

```powershell
git commit -am "feat(agent): generate gRPC stubs and mTLS CA package"
```

---

## Task 4：Control Plane gRPC 服务器（Enroll + Connect 双向流）

**Files:**
- Create: `internal/agentrpc/server.go` — gRPC 服务端 + mTLS
- Create: `internal/agentrpc/connect.go` — Connect 双向流状态机
- Modify: `internal/domain/models.go` — 新增 `Heartbeat`、`ServerObserved`、`NodeCapability` 类型
- Test: `internal/agentrpc/server_test.go` — 内存级测试（用 bufconn 或真实 TCP + 测试 CA）

- [ ] **Step 0: 新增领域类型（供 TaskStore 使用）**

在 `internal/domain/models.go` 追加：

```go
// NodeCapability 描述节点支持的一项能力（如 docker/oci、files、backup）。
type NodeCapability struct {
	Name    string
	Version string
}

// RunningOperation 描述 Agent 当前正在执行的 operation（恢复/续期租约用）。
type RunningOperation struct {
	OperationID string
	Checkpoint  string
	Attempt     int
	ServerID    string
}

// Heartbeat 是 Agent 周期上报的节点健康快照。
type Heartbeat struct {
	NodeID              string
	MemoryTotalBytes    uint64
	MemoryAvailableBytes uint64
	DiskTotalBytes      uint64
	DiskAvailableBytes  uint64
	CPULoad             float64
	AgentVersion        string
	ObservedAt          time.Time
	RunningOperations   []RunningOperation
}

// ServerObserved 是 Agent 对某个服务器实际状态的回报。
type ServerObserved struct {
	ServerID          string
	ObservedGeneration int64
	ObservedPower     string // unknown|stopped|starting|running|stopping
	HealthCondition   string // unknown|healthy|unhealthy
	RuntimeID         string
	BundleDigest      string
	Detail            string
	ObservedAt        time.Time
}
```

注意：`agentv1` proto 已有等价 message，可在此直接转换；保持 domain 类型独立避免 httpapi 依赖 proto 生成代码。

- [ ] **Step 1: 失败测试 Enroll**

```go
func TestEnrollFlow(t *testing.T) {
	// 用 agentca + bufconn 建立 mTLS 连接，注册令牌后签发证书
	// 断言 EnrollResponse.NodeID == "node-1" 且证书链可被 CA 验证
}
```

- [ ] **Step 2: 实现 server.go**

gRPC server 依赖的 Store 边界（仅这些方法，不耦合整个 ControlPlane）：

```go
package agentrpc

// TaskStore 是 gRPC server 从 Store 获取/回报任务所需的最小接口。
// 由 *store.Postgres 实现；测试用 fake。
type TaskStore interface {
	RegisterNode(ctx context.Context, node domain.Node) (string, error) // 返回 nodeID
	NodeByID(ctx context.Context, nodeID string) (domain.Node, error)
	EnqueueTask(ctx context.Context, serverID, nodeID, taskType string, generation int64, actorID string, idemKey string, requestDigest []byte) (string, error)
	ClaimTask(ctx context.Context, nodeID string) (*store.ClaimedTask, error)
	CompleteTask(ctx context.Context, operationID, nodeID string, succeeded bool, errCode *string) error
	RecordAgentHeartbeat(ctx context.Context, nodeID string, hb domain.Heartbeat) error
	ApplyServerObserved(ctx context.Context, obs domain.ServerObserved) error
	RecordAudit(ctx context.Context, event domain.AuditEvent) error
}

type Server struct {
	ca    *agentca.CA
	store TaskStore
	log   *slog.Logger
}

func NewServer(ca *agentca.CA, store TaskStore, logger *slog.Logger) *Server {
	return &Server{ca: ca, store: store, log: logger}
}

func (s *Server) ListenAndServe(ctx context.Context, addr string) error
```

- gRPC `AgentGatewayServiceServer` 实现：
  - `Enroll(ctx, req)`：校验 `registration_token`（一次性，存 PG `nodes` 的 pending 状态）→ 校验 CSR → CA 签发 → 写 `nodes` 行 + `node_capabilities`
  - `Connect(stream)`：维护 agent 会话（nodeID → stream），处理 Hello/Heartbeat/TaskAck/TaskProgress/TaskResult/ServerObserved/MetricsBatch；服务端用 goroutine 从 `ClaimTask` 轮询并下发 Task

- [ ] **Step 3: 实现 connect.go 状态机**

- Hello 里带 `running_operations`，服务端续期这些 operation 的 lease
- Heartbeat 更新 `nodes.last_heartbeat_at`（配合现有 30s 离线对账）
- TaskResult 走 `CompleteTask` 并触发 operation 完成回写
- RotateCertificate：证书到期前服务端下发，Agent 重新 CSR

- [ ] **Step 4: 测试转绿（enroll + heartbeat + task 下发）**

- [ ] **Step 5: Commit**

```powershell
git commit -am "feat(agentrpc): Control Plane gRPC server with mTLS enroll and connect"
```

---

## Task 5：真实 Node Agent（出站 mTLS gRPC + Docker Runtime）

重写 `cmd/agent`：从开发 HTTP 心跳改为完整 Agent。

**Files:**
- Rewrite: `cmd/agent/main.go`
- Create: `internal/agent/agent.go` — Agent 生命周期（注册→连接→心跳→任务执行）
- Create: `internal/agent/docker_runtime.go` — 把 `internal/runtime/docker.go` 包装为任务执行器（provision/power/backup）
- Create: `internal/agent/config.go` — Agent 配置加载
- Test: `internal/agent/agent_test.go` — 用 fake Control Plane gRPC server 测注册与任务执行（mock runtime）

- [ ] **Step 1: 失败测试：Agent 注册并执行 start 任务**

```go
func TestAgentEnrollAndExecutePower(t *testing.T) {
	// fake server: 接收 Enroll → 返回 nodeID+cert；Connect 流上先下发 Power START 任务
	// agent 用 fake runtime 断言调用了 Start
}
```

- [ ] **Step 2: 实现 agent/config.go**

环境变量：`GUGU_PANEL_URL`（gRPC 地址）、`GUGU_REGISTRATION_TOKEN`、`GUGU_NODE_NAME`、`GUGU_AGENT_DATA_ROOT`、`GUGU_AGENT_CERT_DIR`、`GUGU_AGENT_CA_CERT`（信任根）、`GUGU_AGENT_CERT`、`GUGU_AGENT_KEY`、`GUGU_AGENT_VERSION`。

- [ ] **Step 3: 实现 docker_runtime.go**

实现 `ExecuteTask(ctx, task *agentv1.Task) (*TaskOutcome, error)`：
- `provision`：读 `ProvisionTaskPayload`，复用 `internal/runtime/docker.go` 的 `CreateContainer`（镜像、env、端口、卷、资源限制）
- `power`：`start/stop/restart/kill` → 对应容器操作 + `ServerObserved` 回报
- `backup`：`docker exec tar -czf` 生成归档到 Agent 数据根（阶段 2 完善，本期留接口）
- 返回 `TaskResult`（succeeded/error_code/retryable）

- [ ] **Step 4: 实现 agent.go**

```go
func Run(ctx context.Context, cfg Config, logger *slog.Logger) error {
	// 1) 加载/生成密钥与 CSR，向面板 Enroll（mTLS 或注册令牌）
	// 2) 与面板建立 Connect 双向流
	// 3) 周期心跳 + ServerObserved + Metrics
	// 4) 处理下行 Task：TaskAck → ExecuteTask → TaskResult
	// 5) 证书轮换：收到 RotateCertificate → 重新 CSR
}
```

- [ ] **Step 5: 测试转绿**

- [ ] **Step 6: Commit**

```powershell
git commit -am "feat(agent): real outbound mTLS gRPC agent with Docker task executor"
```

---

## Task 6：Production 接线（config + Control Plane 入口选择 Postgres + 迁移执行器）

**Files:**
- Modify: `internal/config/config.go` — production 不再 `ErrProductionAdapterUnavailable`，移除该硬失败；`RedisURL` 可选
- Modify: `cmd/control-plane/main.go` — 按 environment 选择 Memory/Postgres；启动 gRPC server；后台 `ClaimTask` 分发协程
- Create: `internal/migrations/runner.go` — 迁移执行器（up 顺序执行 migrations/*.sql）
- Test: `cmd/control-plane/main_test.go`（现有，扩展 production 选择逻辑测试）

- [ ] **Step 1: 失败测试：production 配置不再硬失败**

```go
func TestProductionConfigWithoutDBFails(t *testing.T) {
	// 无 DatabaseURL → 校验失败
}
func TestProductionConfigValid(t *testing.T) {
	// 完整生产字段 → 通过且不返回 ErrProductionAdapterUnavailable
}
```

- [ ] **Step 2: 修改 config.go**

移除 `return Config{}, fmt.Errorf(...ErrProductionAdapterUnavailable...)`，把 `err` 声明改为 nil 返回；保留字段校验。

- [ ] **Step 3: 实现 migrations/runner.go**

```go
func RunMigrations(ctx context.Context, pool *pgxpool.Pool, dir string) error {
	// 读取 migrations/*.up.sql 按数字前缀排序；维护 schema_migrations 表
	// 每个迁移在事务中执行并记录版本
}
```

- [ ] **Step 4: 修改 control-plane/main.go**

```go
var service httpapi.ControlPlane
if cfg.Environment == config.Production {
	pool, err := connectPostgres(ctx, cfg) // 含 RunMigrations
	pg := store.NewPostgres(ctx, cfg.DatabaseURL, cfg.Environment, readBootstrap(cfg), cfg.DevDataRoot)
	service = pg
	go dispatchTasks(ctx, pg, agentServer) // ClaimTask → 经 gRPC 下发
} else {
	service = development
}
api := httpapi.New(service, logger)
```

同时启动 `internal/agentrpc.Server`（mTLS，`GUGU_AGENT_CA_*`）。

- [ ] **Step 5: 测试转绿 + go build**

- [ ] **Step 6: Commit**

```powershell
git commit -am "feat(control-plane): production wiring with Postgres store, migrations, agent gRPC"
```

---

## Task 7：PaperMC 声明式包落地 + gamectl bundle（1C）

**Files:**
- Modify: `spec/game-definition/examples/papermc.json` — 校验并固定 digest
- Create: `cmd/gamectl` bundle 子命令 — `gamectl bundle build` 生成固定 Bundle（json + digest）
- Modify: `internal/install/artifacts.go` — 支持从 URL 下载 PaperMC jar（版本映射表）
- Test: `spec/game-definition/render_test.go` 扩展

- [ ] **Step 1: 失败测试：bundle 构建产生稳定 digest**

- [ ] **Step 2: 实现 `gamectl bundle build`**

读取 schema + variables + commands → 输出 `{definitionVersion, gameVersion, digest, ...}`，digest = sha256(canonical JSON)。

- [ ] **Step 3: 扩展 install/artifacts.go**

PaperMC 下载：`https://api.papermc.io/v2/projects/paper/versions/{version}/builds/{build}/downloads/paper-{version}-{build}.jar`，版本→build 解析；校验 SHA-256（固定 bundle 里声明）。

- [ ] **Step 4: 测试转绿 + lint 现有示例不回归**

- [ ] **Step 5: Commit**

```powershell
git commit -am "feat(catalog): papermc declarative bundle with digest and artifact download"
```

---

## Task 8：阶段 1 端到端验证（15 分钟信号）

**Files:**
- Create: `docs/changes/GM-20260809-001.md`（阶段 1 变更记录）
- Test: 手动验证脚本 `deploy/dev-e2e.md` 记录步骤

- [ ] **Step 1: 本地真实验证（有 Docker 时）**

```powershell
# 1) 启动 PostgreSQL（Docker 或本机）
docker run -d --name gugu-pg -e POSTGRES_PASSWORD=postgres -p 5432:5432 postgres:17
# 2) 启动 Control Plane（production 模式）
$env:GUGU_ENVIRONMENT="production"; $env:GUGU_DATABASE_URL="postgres://postgres:postgres@127.0.0.1:5432/gugu"; ...
go run ./cmd/control-plane
# 3) 面板签发注册令牌 → 启动 Agent → Agent 自动 Enroll + 连接
go run ./cmd/agent -panel 127.0.0.1:8443 -token <注册令牌>
# 4) Web 创建 PaperMC 服务器 → 观察 provision/power 任务由 Agent 真实执行
# 5) 浏览器打开面板，确认服务器 running、日志可见
```

- [x] **Step 2: 验收清单**

- [x] 新环境（空 DB + 空节点）能在 15 分钟内接入节点并创建可运行 PaperMC
- [x] 重复创建不产生重复容器或端口冲突
- [x] 节点 30 秒无心跳被标记 offline 并禁止新任务
- [x] Web 全程使用真实数据（无 memory 模拟）
- [x] 关键操作有持久审计（audit_events）

- [x] **Step 3: 写变更记录并 commit**（GM-20260809-001，commit 8a54a51）

```powershell
git commit -am "docs(changes): GM-20260809-001 stage 1 real vertical capability"
```

---

## 阶段 1 完成信号（对齐 spec）

- [x] 全新受支持环境 15 分钟内接入节点并创建可运行 PaperMC 服务器
- [x] 重复操作不产生重复容器或端口
- [x] Identity 全部 PostgreSQL 持久化，多副本可撤销（阶段 2 补实时撤销）
- [x] Agent 真实 mTLS/gRPC 出站，NAT 节点可接入

---

## 后续阶段（单独 plan）

- **阶段 2 plan**：多节点端口分配、控制台双向流、Agent 文件服务、真实备份/恢复、Factorio 参考包、跨副本撤销
- **阶段 3 plan**：Extension ABI、WASI Runner、Catalog 签名、SBOM、社区模板
- **阶段 4 plan**：S3 备份、保留策略、定时任务、通知、自动放置、节点排空、迁移、组织配额
- **部署 plan**：一键脚本 + docker-compose 生产化 + 两台服务器验收
