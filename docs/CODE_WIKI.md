# GuGuManager Code Wiki

> 文档版本：1.0 ｜ 生成日期：2026-08-10
>
> 本文件是基于当前代码仓库的结构化技术文档，涵盖项目整体架构、模块职责、关键类与函数、依赖关系与运行方式。权威设计与边界以 [DESIGN.md](../DESIGN.md) 与 `docs/design/*` 为准。

## 目录

- [1. 项目概述](#1-项目概述)
- [2. 整体架构](#2-整体架构)
- [3. 仓库结构](#3-仓库结构)
- [4. 后端模块（Go）](#4-后端模块go)
  - [4.1 cmd — 程序入口](#41-cmd--程序入口)
  - [4.2 internal/domain — 领域模型](#42-internaldomain--领域模型)
  - [4.3 internal/config — 配置加载](#43-internalconfig--配置加载)
  - [4.4 internal/httpapi — REST API 层](#44-internalhttpapi--rest-api-层)
  - [4.5 internal/store — 存储适配器](#45-internalstore--存储适配器)
  - [4.6 internal/identity — 身份与密码](#46-internalidentity--身份与密码)
  - [4.7 internal/agent — Node Agent](#47-internalagent--node-agent)
  - [4.8 internal/agentca — Agent CA](#48-internalagentca--agent-ca)
  - [4.9 internal/agentrpc — Agent gRPC 服务](#49-internalagentrpc--agent-grpc-服务)
  - [4.10 internal/files — 服务器文件系统](#410-internalfiles--服务器文件系统)
  - [4.11 internal/install — 制品安装器](#411-internalinstall--制品安装器)
  - [4.12 internal/migrations — 迁移执行器](#412-internalmigrations--迁移执行器)
  - [4.13 internal/runtime — 容器运行时](#413-internalruntime--容器运行时)
  - [4.14 internal/id — ID 生成](#414-internalid--id-生成)
  - [4.15 spec/game-definition — 游戏定义契约](#415-specgame-definition--游戏定义契约)
- [5. 前端模块（React / TypeScript）](#5-前端模块react--typescript)
- [6. 机器契约](#6-机器契约)
- [7. 数据库迁移](#7-数据库迁移)
- [8. 依赖关系](#8-依赖关系)
- [9. 项目运行方式](#9-项目运行方式)
- [10. 测试与 CI](#10-测试与-ci)
- [11. 部署](#11-部署)

---

## 1. 项目概述

**GuGuManager** 是一个面向自托管用户和小型游戏托管团队的游戏服务器控制面（Control Plane）。它用 Web 控制台统一管理 Linux 节点上的 Dedicated Server，通过“模块化单体控制面 + 独立节点 Agent + 版本化 GameDefinition”表达不同游戏的安装与运行差异。

当前仓库处于**阶段 0 / 阶段 1 开发切片**状态，提供可运行的开发垂直切片，但不是生产 MVP：

- Control Plane 提供 REST API、异步 operation、CSRF/幂等键、结构化错误，使用内存适配器（开发）或 PostgreSQL（生产骨架）。
- Node Agent 实现出站 mTLS gRPC（Enroll + Connect 双向流）与 Docker 任务执行器。
- `gamectl` CLI 提供 GameDefinition 初始化与 lint。
- React 前端覆盖总览、服务器工作区、节点、游戏目录、用户权限、任务队列、审计、Network/Startup 等界面，支持简中/英/日/韩四语言。

**技术栈**：Go 1.26（后端）、React 19 + TypeScript + Vite（前端）、PostgreSQL 17 + Redis（生产依赖）、Docker（Agent 运行时与部署）、gRPC + Protobuf（Agent 协议）、OpenAPI（REST 契约）。

---

## 2. 整体架构

```
┌─────────────────────────────────────────────────────────────────────┐
│                        浏览器（React SPA）                           │
│   Overview / Servers / ServerWorkspace / Nodes / Users / Audit ...   │
└──────────────────────────────┬──────────────────────────────────────┘
                               │ HTTPS REST + Cookie/CSRF
┌──────────────────────────────▼──────────────────────────────────────┐
│                    Control Plane（cmd/control-plane）               │
│  ┌──────────────┐  ┌──────────────┐  ┌────────────────────────────┐ │
│  │  httpapi     │  │   config     │  │       store 适配器          │ │
│  │  REST + SPA  │  │ 环境/校验     │  │  ┌─────────┐ ┌──────────┐  │ │
│  │  鉴权/幂等    │  │              │  │  │ Memory  │ │ Postgres │  │ │
│  └──────┬───────┘  └──────────────┘  │  │ (开发)  │ │ (生产)   │  │ │
│         │                            │  └─────────┘ └──────────┘  │ │
│         │           ┌────────────────┴───────────────────────────┐ │ │
│         │           │  domain（实体/状态机/Operation/错误）        │ │ │
│         │           │  identity（Argon2id/限流）                   │ │ │
│         │           │  files / install / migrations              │ │ │
│         │           └────────────────────────────────────────────┘ │ │
│         │                                                            │
│  ┌──────▼───────┐         ┌──────────────────────────────────────┐  │
│  │  agentrpc    │◄────────│  Agent mTLS gRPC 网关（仅 production）│  │
│  │  (gRPC 服务) │         │  agentca（CA 签发）                   │  │
│  └──────┬───────┘         └──────────────────────────────────────┘  │
└─────────┼───────────────────────────────────────────────────────────┘
          │ gRPC 双向流（出站连接，由 Agent 主动建立）
┌─────────▼───────────────────────────────────────────────────────────┐
│                  Node Agent（cmd/agent）                             │
│   Enroll（CSR 换证书） → Connect（Hello/Welcome/心跳/任务）           │
│   DockerExecutor → Docker 容器（provision / power / backup）         │
└─────────────────────────────────────────────────────────────────────┘
          │
   ┌──────▼──────┐
   │ Docker 守护  │ → 游戏容器（Minecraft / Factorio / ...）
   └─────────────┘
```

**核心设计原则**：

1. **默认拒绝权限**，用户只能看到被授权的服务器资源；前端隐藏入口不是授权证明。
2. **状态分离建模**：期望电源、实际电源、节点条件、生命周期、操作状态各自独立。
3. **PostgreSQL 是生产事实源**；Redis 只承担可重建的队列/缓存/广播。
4. **Agent 主动出站连接**，Control Plane 不直接访问节点 Docker Socket。
5. **异步 operation 收敛**：所有改变状态的请求返回 `operationId`，可重试写请求需幂等键。
6. **版本化 GameDefinition**：服务器绑定明确的定义版本和不可变 Bundle 摘要，不自动跟随 `latest`。
7. **开发适配器与生产适配器共用同一领域接口**，但在配置和界面中明确标识。

---

## 3. 仓库结构

```
/workspace
├── cmd/                      # Go 程序入口
│   ├── control-plane/        # 控制面 HTTP 服务
│   ├── agent/                # Node Agent
│   ├── gamectl/              # GameDefinition lint/init CLI
│   └── migrate/              # 迁移清单只读工具
├── internal/                 # 后端内部包（不对外暴露）
│   ├── domain/               # 领域模型、状态机、错误
│   ├── config/               # 环境变量配置加载与校验
│   ├── httpapi/              # REST API handler + 中间件
│   ├── store/                # 存储适配器（Memory / Postgres）
│   ├── identity/             # Argon2id 密码、登录限流
│   ├── agent/                # Agent 主循环 + Docker 执行器
│   ├── agentca/              # Agent mTLS CA
│   ├── agentrpc/             # Agent gRPC 服务端
│   ├── files/                # 服务器文件系统抽象
│   ├── install/              # builtin.artifacts 安装器
│   ├── migrations/           # SQL 迁移加载与执行
│   ├── runtime/              # Docker 运行时封装
│   └── id/                   # UUID 生成
├── api/                      # 机器契约
│   ├── openapi/openapi.yaml  # REST API 契约
│   └── proto/.../agent.proto # Agent gRPC 协议
├── spec/game-definition/     # GameDefinition Schema + Go 校验
├── migrations/               # PostgreSQL up/down SQL 迁移
├── web/                      # React 前端
│   └── src/
│       ├── app/              # 应用 Shell 与路由
│       ├── pages/            # 页面组件
│       ├── components/       # 通用 UI 组件
│       ├── domain/           # 前端领域逻辑（电源状态等）
│       ├── lib/              # API 客户端、类型、Mock、工具
│       └── i18n/             # 四语言国际化
├── deploy/                   # Dockerfile + docker-compose
├── docs/                     # 设计文档、ADR、变更记录
└── design-system/            # Liquid Command 视觉规范
```

---

## 4. 后端模块（Go）

### 4.1 cmd — 程序入口

#### cmd/control-plane/main.go

控制面 HTTP 服务入口，职责：

- 加载配置（`config.Load()`）并按 `GUGU_ENVIRONMENT` 选择适配器：
  - `development` → `store.NewMemoryAt` / `store.NewMemoryForSetupAt`（内存适配器）
  - `production` → 连接 PostgreSQL → 执行迁移 → `store.NewPostgres`（生产适配器）
- 构造 `httpapi.Handler`，用 `spa()` 包装为同时服务 REST API 和 SPA 静态文件的 handler。
- 开发模式启动节点存活探测协程（`reconcileNodeLiveness`，每 5 秒）。
- 生产模式启动 Agent mTLS gRPC 网关（`serveAgentGRPC`，默认 `127.0.0.1:8443`）。
- 优雅关闭（SIGINT/SIGTERM，10 秒超时）。

关键函数：

- `buildService(ctx, cfg, logger)`：按环境分发构建 `httpapi.ControlPlane` 实例。
- `buildProductionService`：连接 PG → 迁移 → 构造 Postgres store → 注入 bootstrap token。
- `spa(api, root, logger)`：将 `/api/*`、`/healthz`、`/readyz` 路由到 API，其余回退到 SPA `index.html`。
- `serveAgentGRPC`：初始化 `agentca.CA`、`agentrpc.Server` 并监听 gRPC。

#### cmd/agent/main.go

Node Agent 入口，解析 `-once` 标志后调用 `agent.Run`（持续重连）或 `agent.RunOnce`（单次会话）。

#### cmd/gamectl/main.go

GameDefinition CLI 工具，子命令：

- `lint <definition.json>`：JSON 解析 → Schema 校验 → 端口唯一性 → `health.portRef` 引用 → 变量 closed-object 子集 → Secret/binding 引用 → file binding 路径安全 → Artifact 校验。
- `init --dir <directory>`：生成示例 GameDefinition 模板。

关键函数：`lint()`、`validateInstall()`、`validateBundleTargetPath()`。

#### cmd/migrate/main.go

只读迁移工具，校验 `migrations/` 下 up/down 配对、版本连续性、生成摘要清单。**不执行数据库迁移**。

---

### 4.2 internal/domain — 领域模型

定义与存储无关的领域实体、值对象、状态机和错误。

**核心实体**（[models.go](../internal/domain/models.go)）：

| 类型 | 说明 |
| --- | --- |
| `User` | 用户：ID、邮箱、角色、状态 |
| `Session` / `SessionView` | 会话：Token、CSRFToken、用户、过期时间 |
| `Server` | 服务器：含生命周期状态、期望/实际电源、generation、指标、分配 |
| `Node` | 节点：条件、版本、资源容量与分配 |
| `GameDefinition` | 游戏定义：ID、Bundle 摘要、版本、能力 |
| `Allocation` | 网络分配：绑定 IP、端口、协议、是否主端口 |
| `Startup` / `StartupVariable` / `StartupBinding` | 启动配置：命令、变量（含 Secret）、绑定 |
| `Backup` | 备份：状态、大小、校验和 |
| `Operation` / `OperationError` | 异步操作：状态、进度、generation、租约、错误 |
| `AuditEvent` | 审计事件：操作者、动作、目标、结果 |
| `Heartbeat` / `ServerObserved` | Agent 上报的节点资源与服务器实际状态 |
| `Overview` | 总览聚合：服务器/节点计数、CPU/内存、最近活动 |

**电源与操作**（[power.go](../internal/domain/power.go)）：

- `PowerAction`：`start` / `stop` / `restart` / `kill`
- `NewQueuedOperation()`：创建基线 operation（status=`queued`，attempt=1，maxAttempts=1）
- `PowerCoordinator`：进程内幂等协调器，相同 `(serverID, idempotencyKey)` 重放返回原 operation。

**错误**（[errors.go](../internal/domain/errors.go)）：

- `Problem`：结构化错误，含 Code、Message、Retryable、Details。
- `ErrIdempotencyKeyReused`：幂等键被不同请求复用时返回。

---

### 4.3 internal/config — 配置加载

[config.go](../internal/config/config.go) 从环境变量加载 `Config`，区分 `development` 与 `production` 两套校验规则。

**关键配置项**：

| 环境变量 | 说明 | 默认值 |
| --- | --- | --- |
| `GUGU_ENVIRONMENT` | `development` / `production` | `development` |
| `GUGU_HTTP_ADDR` | HTTP 监听地址 | `127.0.0.1:8080` |
| `GUGU_WEB_ROOT` | SPA 静态文件根 | `web/dist` |
| `GUGU_DEV_ADMIN_EMAIL` | 开发管理员邮箱 | `admin@gugu.local` |
| `GUGU_DEV_ADMIN_PASSWORD` | 开发管理员密码 | `gugu-dev-2026` |
| `GUGU_DEV_AGENT_TOKEN` | 开发 Agent Token | `gugu-agent-dev-token` |
| `GUGU_DEV_DATA_ROOT` | 开发服务器数据根 | `var/development/servers` |
| `GUGU_DEV_OPERATION_LATENCY` | 开发 operation 模拟延迟 | `850ms` |
| `GUGU_DATABASE_URL` | PostgreSQL 连接串（生产必填） | — |
| `GUGU_REDIS_URL` | Redis 连接串（生产可选） | — |
| `GUGU_PUBLIC_URL` | 公开 HTTPS URL（生产必填） | — |
| `GUGU_SESSION_KEY_FILE` | 会话密钥文件（生产必填） | — |
| `GUGU_AGENT_CA_CERT_FILE` | Agent CA 证书文件（生产必填） | — |
| `GUGU_BOOTSTRAP_TOKEN_FILE` | Bootstrap Token 文件 | — |
| `GUGU_TLS_TERMINATED` | 是否 TLS 终止（生产必须 `true`） | `false` |
| `GUGU_LOG_LEVEL` | `debug`/`info`/`warn`/`error` | `info` |
| `GUGU_LOG_FORMAT` | `json`/`text` | `json` |

**校验特点**：生产环境禁止所有 `GUGU_DEV_*` 变量，强制 HTTPS 公开 URL、PostgreSQL DSN、密钥文件可读、`TLS_TERMINATED=true`。`ValidationError` 聚合所有问题一次性返回。

---

### 4.4 internal/httpapi — REST API 层

[handler.go](../internal/httpapi/handler.go) 实现全部 REST 端点。

**`ControlPlane` 接口**：定义了 handler 依赖的 store 契约（约 40 个方法），覆盖 Setup、Auth、Users、Membership、Servers、Power、Allocations、Startup、Operations、Nodes、Games、Audit、Console、Files、Backups、Heartbeat。

**`Handler` 结构**：持有 service、logger 和两个 `identity.AttemptLimiter`（登录限流 + 敏感操作限流，各 5 次/5 分钟，封禁 15 分钟）。

**路由**（`http.NewServeMux` Go 1.22+ 方法路由）：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/healthz`、`/readyz` | 健康与就绪探测 |
| GET | `/api/v1/setup/status` | 初始化状态 |
| POST | `/api/v1/setup/admin` | 首次管理员初始化 |
| POST | `/api/v1/auth/login` | 登录 |
| POST | `/api/v1/auth/password-reset` | 密码重置（匿名消费令牌） |
| GET | `/api/v1/auth/session` | 当前会话 |
| POST | `/api/v1/auth/logout` | 退出 |
| GET/POST | `/api/v1/users` | 用户列表/创建 |
| GET/PATCH | `/api/v1/users/{userID}` | 用户详情/更新 |
| POST | `/api/v1/users/{userID}/password-reset-tokens` | 签发重置令牌 |
| GET | `/api/v1/overview` | 总览 |
| GET/POST | `/api/v1/servers` | 服务器列表/创建 |
| GET | `/api/v1/servers/{serverID}` | 服务器详情 |
| POST | `/api/v1/servers/{serverID}/power` | 电源操作 |
| GET/POST | `/api/v1/servers/{serverID}/allocations` | 网络分配 |
| PATCH/DELETE | `/api/v1/servers/{serverID}/allocations/{allocationID}` | 主端口切换/删除 |
| GET/PUT | `/api/v1/servers/{serverID}/startup` | 启动配置 |
| GET/PUT/DELETE | `/api/v1/servers/{serverID}/members/{userID}` | 服务器 membership |
| GET | `/api/v1/servers/{serverID}/console` | 控制台日志 |
| POST | `/api/v1/servers/{serverID}/console/commands` | 发送命令 |
| GET/PUT | `/api/v1/servers/{serverID}/files`、`/files/content` | 文件列表/读写 |
| POST | `/.../files/directories`、`/files/moves` | 建目录/移动 |
| DELETE | `/api/v1/servers/{serverID}/files` | 删除文件 |
| GET/POST | `/api/v1/servers/{serverID}/backups` | 备份列表/创建 |
| POST | `/.../backups/{backupID}/restore` | 恢复备份 |
| GET | `/api/v1/operations` | 任务列表 |
| GET | `/api/v1/nodes`、`/games`、`/audit` | 节点/游戏/审计 |

**中间件**：`h.auth()` 包装需认证的 handler，校验 session cookie + CSRF token，将 `principal` 注入 context。

**安全特性**：

- Cookie/CSRF 双 token 会话
- `Idempotency-Key` 头 + 十进制 `If-Match` generation 乐观并发
- 精确端口冲突返回 `409 PORT_CONFLICT`，过期 generation 返回 `412 PRECONDITION_FAILED`
- 写操作在副作用前重新核验角色与 membership（不信任 session 旧快照）
- JSON body 大小限制（认证 12 MiB，匿名 64 KiB）

---

### 4.5 internal/store — 存储适配器

实现 `httpapi.ControlPlane` 接口的两个适配器，共用同一领域模型。

#### Memory（开发适配器）

[memory.go](../internal/store/memory.go) — 进程内内存存储，进程退出后丢失（文件数据除外）。

**`Memory` 结构**：`sync.RWMutex` 保护，持有 users、sessions、memberships、passwordResetTokens、servers、allocations、startups、nodes、games、operations、idempotency、audit、console、files、backups 等映射。

**构造函数**：

- `NewMemory(...)`：临时目录，用于测试。
- `NewMemoryAt(...)`：显式数据根，控制面使用。
- `NewMemoryForSetupAt(...)`：bootstrap 模式（待初始化）。

**关键行为**：

- Argon2id 哈希管理员密码，SHA-256 哈希 agent token。
- 幂等记录使用进程内随机密钥的 HMAC 摘要。
- 文件操作通过 `serverfiles.ServerFS` 落地到本地数据根，与用户停用/角色降级/membership 撤销互斥（`fileMutationGates`）。
- `RuntimeAdapter` 字段为 nil 时模拟电源收敛，非 nil 时对接真实 Docker。
- `ReconcileNodeLiveness(now)`：节点存活探测（5 秒无心跳 → offline）。

#### Postgres（生产适配器）

[postgres.go](../internal/store/postgres.go) — PostgreSQL 持久化，当前为骨架。

- `NewPostgres(ctx, dsn, environment, agentToken, fileRoot)`：连接池配置（25 连接，5 分钟生命周期）。
- `SetBootstrapToken(token)`：SHA-256 摘要存储 bootstrap token。
- 实现分布在多个文件：`postgres_controlplane.go`、`postgres_entities.go`、`postgres_identity.go`、`postgres_session.go`、`postgres_tasks.go`。
- `TaskStore` 相关方法（`RegisterNode`、`EnqueueTask`、`ClaimTask`、`CompleteTask`、`RecordAgentHeartbeat`、`ApplyServerObserved`）供 `agentrpc.Server` 使用。

#### 其他 store 文件

| 文件 | 职责 |
| --- | --- |
| `runtime_adapter.go` | Memory 对接真实 Docker 的适配层 |
| `runtime_toggle.go` | 运行时模式切换 |
| `seed.go` | 开发种子数据（管理员、节点、游戏、服务器） |
| `files.go` | 文件存储操作 |
| `backups.go` | 备份元数据 |
| `game_bundles.go` | 固定 GameDefinition Bundle 加载 |
| `network_startup.go` | Allocation 期望状态管理 |
| `node_liveness.go` | 节点存活探测逻辑 |
| `operations.go` | Operation 状态机 |
| `identity.go` | 用户/会话/membership 内存实现 |

---

### 4.6 internal/identity — 身份与密码

[password.go](../internal/identity/password.go) — Argon2id 密码哈希。

- `Argon2idParams`：默认 64 MiB 内存、3 次迭代、并行度 2、16 字节盐、32 字节密钥。
- `HashPassword(password, params)`：生成 PHC 格式字符串 `$argon2id$v=19$m=...,t=...,p=...$salt$hash`。
- `VerifyPassword(encoded, password)`：常量时间比较。
- `argon2Gate`：进程级并发门（最多 2 个并发推导），防止内存耗尽。
- `parsePHC()` / `validateParams()`：严格解析与参数范围校验。

[login_limiter.go](../internal/identity/login_limiter.go) — 基于 reservation 的登录限流器。

---

### 4.7 internal/agent — Node Agent

[agent.go](../internal/agent/agent.go) — Agent 主循环。

**生命周期**：

1. `Run(ctx, cfg, logger)` → `run()`：断开后自动重连（3 秒间隔），直到 ctx 取消。
2. `serveSession()`：凭据就绪 → 会话。
3. `ensureCredentials()`：复用持久化证书（`CertDir/agent.crt` + `agent.key`），不存在时生成 RSA 2048 密钥 + CSR，调用 `Enroll` RPC 换取证书链并落地。
4. `serveOnce()`：建立 mTLS gRPC 双向流 → 发送 `Hello` → 接收 `Welcome` → 启动心跳循环 → 处理下行帧（Task / RotateCertificate / Drain / CertificateResponse）。

**`TaskExecutor` 接口**：`ExecuteTask(ctx, task) (*ExecutionOutcome, error)`。

**任务处理**（`handleTask`）：先回 `TaskAck`，执行后回 `TaskResult`，若有 `ServerObserved` 则额外上报。

**证书轮换**：收到 `RotateCertificate` → 生成新 CSR 上报 → 收到 `CertificateResponse` → 落盘并更新内存证书。

[config.go](../internal/agent/config.go) — 从 `GUGU_AGENT_*` 环境变量读取配置（含旧变量兼容回退）。

[docker_executor.go](../internal/agent/docker_executor.go) — `DockerExecutor` 实现 `TaskExecutor`，按 `task.Type` 分发 provision/power 任务到 Docker 容器。运行时惰性初始化。`gameImageMap` 将 GameDefinition ID 映射到 Docker 镜像。

---

### 4.8 internal/agentca — Agent CA

[agentca.go](../internal/agentca/agentca.go) — 管理 Agent mTLS 证书签发。

- `NewCA(dir)`：加载或首次生成根 CA（RSA，10 年 TTL），持久化 `ca.crt` / `ca.key`。
- `RootCAPEM()`：返回根证书 PEM。
- `IssueAgentCertificate(nodeID)`：签发 CN=nodeID 的 client auth 叶证书（24 小时 TTL）。

---

### 4.9 internal/agentrpc — Agent gRPC 服务

[server.go](../internal/agentrpc/server.go) + [connect.go](../internal/agentrpc/connect.go) — 实现 `AgentGatewayService`。

**`TaskStore` 接口**：gRPC server 依赖的 store 最小契约（注册节点、入队任务、认领任务、完成任务、记录心跳、应用观察状态、记录审计）。

**`Enroll` RPC**：

1. 校验注册令牌（常量时间比较）。
2. 解析 CSR，校验单证书请求块。
3. 注册节点（`RegisterNode`），返回 Control Plane 分配的 nodeID。
4. 签发 24 小时 client auth 证书，返回证书链 + CA 根证书 + 过期时间。

**`Connect` RPC**（双向流）：

1. 首帧必须是 `Hello`，校验 mTLS 证书 CN 与声明的 nodeID 一致（`verifyPeerNode`）。
2. 回 `Welcome`（协议版本、心跳间隔 10 秒、最大并发任务 3）。
3. 启动 claim 协程：每 2 秒轮询 `ClaimTask`，有任务则下发 `Task` 帧。
4. 接收循环处理：`Heartbeat` → 记录；`TaskAck` / `TaskResult` → 更新 operation；`ServerObserved` → 应用实际状态；`CertificateSigningRequest` → 签发并回 `CertificateResponse`。
5. `Send` 通过 `sendMu` 串行化（claim 协程与 recv 循环并发调用）。

**配置选项**：`WithRegistrationToken(token)`、`WithClaimPeriod(period)`。

---

### 4.10 internal/files — 服务器文件系统

[filesystem.go](../internal/files/filesystem.go) — 服务器数据目录的安全文件操作抽象。

- `ServerFS`：绑定到单个服务器数据根，`sync.Mutex` 保护，`Limits` 限制读写大小。
- 路径安全：拒绝 `..`、符号链接、根目录修改；`NormalizeRelative()` 规范化相对路径。
- 跨平台：`atomic_other.go` / `atomic_windows.go` 处理原子写入差异。
- 错误类型：`ErrUnsafePath`、`ErrNotDirectory`、`ErrNotRegularFile`、`ErrSizeLimit`、`ErrRootMutation` 等。

[path.go](../internal/files/path.go) — 路径规范化与安全校验，供 store 和 gamectl 共用。

---

### 4.11 internal/install — 制品安装器

[artifacts.go](../internal/install/artifacts.go) — 实现 `builtin.artifacts` 生命周期 handler。

**安全规则**（运行时强制，不信任 manifest）：

- HTTPS only（`ErrNotHTTPS`）
- 每个请求主机必须在声明的 network allowlist 内（`ErrHostNotAllowed`）
- 解析地址必须公开可路由（`ErrPrivateAddress`）
- 响应有字节上限
- 完整 SHA-256 校验后才提交（`ErrDigestMismatch`）
- 目标路径通过 `files.ServerFS` 解析，无法逃逸服务器数据根
- 目标唯一（`ErrDuplicateTarget`）

**`ValidateArtifacts(artifacts)`** / **`ValidateAllowlist(hosts)`**：供 gamectl lint 复用的校验函数。

---

### 4.12 internal/migrations — 迁移执行器

[runner.go](../internal/migrations/runner.go) — PostgreSQL 迁移加载与执行。

- `LoadMigrations(dir)`：读取 `NNNNNN_name.up.sql` / `.down.sql` 配对，校验版本连续、无重复、无符号链接。
- `RunMigrations(ctx, db, dir, direction)`：创建 `schema_migrations` 表，按版本顺序执行 up 或逆序执行 down，事务包裹。
- 生产模式由 `cmd/control-plane` 在启动时调用。

---

### 4.13 internal/runtime — 容器运行时

[docker.go](../internal/runtime/docker.go) — Docker SDK 封装。

- `NewDockerRuntime()`：创建 Docker 客户端。
- `ContainerConfig`：镜像、命令、数据卷挂载、端口映射、资源限制。
- 方法：`CreateContainer`、`StartContainer`、`StopContainer`、`RestartContainer`、`RemoveContainer`、`InspectContainer`。
- `ContainerStatus`：容器状态快照。

---

### 4.14 internal/id — ID 生成

[uuid.go](../internal/id/uuid.go) — 基于 `crypto/rand` 的 UUID v4 生成器，供 store 和 operation 使用。

---

### 4.15 spec/game-definition — 游戏定义契约

[schema.go](../spec/game-definition/schema.go) — GameDefinition v1alpha1 JSON Schema 的 Go 校验入口。

- `//go:embed v1alpha1.schema.json examples/*.json`：嵌入 Schema 与示例，校验独立于工作目录。
- `compiledV1Alpha1Schema`：`sync.OnceValues` 惰性编译 Draft 2020-12 Schema。
- `ValidateV1Alpha1(document)`：校验已解码文档。
- `DecodeStartupVariableSchema()` / `StartupVariableProperty`：解析启动变量 Schema（closed-object 语义，支持 string/integer/boolean 的 default/const/range/length/enum）。
- `render.go`：Bundle 文档渲染。
- `variables.go`：变量校验与渲染逻辑。

**示例 Bundle**：`papermc.json`、`factorio.json`、`vintagestory.json`。

---

## 5. 前端模块（React / TypeScript）

### 技术栈

React 19 + React Router 7 + Vite 7 + TypeScript 5.9，原生 CSS（Liquid Command 设计系统），Lucide 图标，四语言 i18n（zh-CN / en / ja / ko）。

### 目录结构（web/src/）

| 目录 | 职责 |
| --- | --- |
| `app/App.tsx` | 应用 Shell、路由、bootstrap 探测、侧边栏导航 |
| `pages/` | 页面组件：Overview、Servers、ServerWorkspace、Nodes、Games、Users、Audit、Operations、Login、Setup、ResetPassword |
| `components/` | 通用组件：Modal、StatusBadge、MetricBars、PageState |
| `domain/power.ts` | 前端电源状态机 |
| `lib/api.ts` | HTTP API 客户端（含 Mock 回退） |
| `lib/types.ts` | 前端类型定义（与 OpenAPI 人工同步） |
| `lib/openapi.generated.ts` | openapi-typescript 生成的只读类型 |
| `lib/mock.ts` | 浏览器内开发 Mock 适配器 |
| `lib/operations.ts` | Operation 轮询与状态展示逻辑 |
| `lib/format.ts` | 格式化工具（字节、时间等） |
| `lib/identity.ts` | 前端权限判断 |
| `i18n/I18n.tsx` | i18n Provider 与 `useCopy` hook |

### 关键行为

- **Bootstrap 探测**：首屏先调 `setupStatus()`，未初始化进 SetupPage，已初始化但未登录进 Login，已登录进 AppShell。
- **API 回退**：首次探测 Control Plane 不可达时切换到浏览器内 Mock 适配器（仅本机 UI 演示）；一旦收到真实 HTTP 响应，后续网络错误原样显示，不在 operation 中途切 Mock。
- **路由权限**：`platform_admin` 可见 Overview/Nodes/Audit/Users；`server_owner` 直接跳转 `/servers`。
- **ServerWorkspace**：服务器详情工作区，含电源、控制台、文件、备份、Network、Startup 子视图。
- **i18n**：`LocalizedCopy<T>` 类型 + `useCopy(copy)` hook，四语言切换不裁切布局。
- **无障碍**：键盘导航、焦点归位、触控目标 ≥44px、`prefers-reduced-motion`、状态文字独立于颜色。

---

## 6. 机器契约

| 契约 | 路径 | 工具链 |
| --- | --- | --- |
| REST API | [api/openapi/openapi.yaml](../api/openapi/openapi.yaml) | Redocly lint + openapi-typescript 生成前端类型 |
| Agent gRPC | [api/proto/gugumanager/agent/v1/agent.proto](../api/proto/gugumanager/agent/v1/agent.proto) | Buf lint + breaking（对 `agent-v1.baseline.binpb`）+ 生成 Go 类型 |
| GameDefinition Schema | [spec/game-definition/v1alpha1.schema.json](../spec/game-definition/v1alpha1.schema.json) | JSON Schema Draft 2020-12（santhosh-tekuri/jsonschema/v6） |
| PostgreSQL 迁移 | [migrations/](../migrations/) | `internal/migrations.RunMigrations` |
| 前端类型 | [web/src/lib/types.ts](../web/src/lib/types.ts) | 与 OpenAPI 人工同步，未来由生成类型替代 |

### Agent gRPC 协议概要

```
service AgentGatewayService {
  rpc Connect(stream ConnectRequest) returns (stream ConnectResponse);
  rpc Enroll(EnrollRequest) returns (EnrollResponse);
}
```

- **Enroll**：注册令牌 + CSR → nodeID + 证书链 + CA 根证书。
- **Connect**：Hello → Welcome → 双向流（心跳、任务下发、TaskAck/TaskResult、ServerObserved、证书轮换、Drain）。
- **Task 类型**：provision / power / backup / extension，含 typed payload（`ProvisionTaskPayload`、`PowerTaskPayload` 等）。

---

## 7. 数据库迁移

[migrations/](../migrations/) 下 5 个迁移，遵循 `NNNNNN_name.up.sql` / `.down.sql` 约定：

| 版本 | 名称 | 关键表 |
| --- | --- | --- |
| 000001 | core | users, roles, user_roles, sessions, nodes, node_capabilities, game_definitions, game_bundles, servers, server_members, allocations, server_tasks, outbox_events, backups, audit_events |
| 000002 | identity | 密码重置令牌表 |
| 000003 | membership_permissions | membership 权限枚举约束（非空、唯一、已知、含 `servers.read`） |
| 000004 | password_reset | 密码重置流程扩展 |
| 000005 | controlplane_stage1 | `startup_values` 表、`allocations.is_primary` 列 + 唯一索引 |

**关键约束**：

- `servers` 表分离 `desired_power` / `observed_power` / `node_condition` / `health_condition` / `generation` / `observed_generation`。
- `server_tasks` 含幂等唯一约束 `(idempotency_scope, idempotency_key)`、每服务器单活跃任务约束、claim 索引。
- `allocations` 活跃端点唯一索引 `(node_id, bind_ip, port, protocol) WHERE released_at IS NULL`。
- `audit_events` 记录操作者、动作、目标、结果、trace_id。

---

## 8. 依赖关系

### Go 依赖（go.mod）

| 依赖 | 用途 |
| --- | --- |
| `github.com/docker/docker` | Agent Docker 容器管理 |
| `github.com/docker/go-connections` | Docker 连接 |
| `github.com/jackc/pgx/v5` | PostgreSQL 驱动（pgx） |
| `github.com/lib/pq` | PostgreSQL 驱动（database/sql，迁移用） |
| `github.com/santhosh-tekuri/jsonschema/v6` | GameDefinition JSON Schema 校验 |
| `golang.org/x/crypto` | Argon2id 密码哈希 |
| `google.golang.org/grpc` | Agent gRPC 通信 |
| `google.golang.org/protobuf` | Protobuf 序列化 |
| `go.opentelemetry.io/otel`（间接） | 可观测性 instrumentation |

### 模块内依赖（简化）

```
cmd/control-plane → config, httpapi, store, agentrpc, agentca, migrations
cmd/agent         → agent, agent.LoadConfig
cmd/gamectl       → spec/game-definition, internal/files, internal/install

httpapi → domain, identity, id
store   → domain, identity, id, files, runtime
agent   → agentv1(proto), runtime
agentrpc → agentv1(proto), agentca, domain, store
identity → golang.org/x/crypto/argon2
install  → files
migrations → (database/sql, lib/pq)
```

### 前端依赖（web/package.json）

| 依赖 | 用途 |
| --- | --- |
| `react` / `react-dom` 19 | UI 框架 |
| `react-router-dom` 7 | 路由 |
| `lucide-react` | 图标 |
| `@fontsource-variable/*` | 字体（JetBrains Mono、Noto Sans SC、Plus Jakarta Sans） |
| `vite` 7 | 构建工具 |
| `vitest` 3 | 单元测试 |
| `@playwright/test` | E2E 测试 |
| `@redocly/cli` | OpenAPI lint |
| `openapi-typescript` | OpenAPI → TypeScript 类型生成 |
| `@testing-library/react` | 组件测试 |

---

## 9. 项目运行方式

### 前置要求

- Go 1.26+
- Node.js 24+ 和 npm
- （可选）Docker，用于 Compose 部署

### 本地开发（前后端联调）

```bash
# 1. 构建前端
cd web
npm install
npm run build
cd ..

# 2. 启动控制面（开发模式，内存适配器）
go run ./cmd/control-plane
```

打开 http://127.0.0.1:8080 ，开发账号：

- Email：`admin@gugu.local`
- Password：`gugu-dev-2026`

### 仅前端开发

```bash
cd web
npm run dev
```

打开 http://127.0.0.1:4173 。Control Plane 不可达时自动切换到浏览器内 Mock 适配器。

### Agent 开发

```bash
# 设置环境变量（示例）
export GUGU_AGENT_PANEL_ADDR=127.0.0.1:8443
export GUGU_AGENT_REGISTRATION_TOKEN=<token>
export GUGU_AGENT_NODE_NAME=node-01

# 运行（持续重连）
go run ./cmd/agent

# 单次会话（不重连）
go run ./cmd/agent -once
```

> 注意：Agent 的 mTLS gRPC 网关仅在 `production` 环境启动。开发模式 Control Plane 不启动 gRPC 服务。

### gamectl 工具

```bash
# 初始化示例 GameDefinition
go run ./cmd/gamectl init --dir my-game

# lint 校验
go run ./cmd/gamectl lint spec/game-definition/examples/papermc.json
go run ./cmd/gamectl lint spec/game-definition/examples/factorio.json
go run ./cmd/gamectl lint spec/game-definition/examples/vintagestory.json
```

### 迁移工具（只读清单）

```bash
go run ./cmd/migrate
```

### Docker Compose 部署

```bash
docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml up --build
```

Compose 启动三个服务，均只发布到宿主 `127.0.0.1`：

| 服务 | 端口 | 说明 |
| --- | --- | --- |
| control-plane | 8080 | 开发模式控制面 |
| postgres | 5432 | PostgreSQL 17（初始化时自动执行前 3 个迁移 SQL） |
| redis | 6379 | Redis 7.4 |

> 默认账号/密码/Token 仅为本机开发，禁止用于局域网、公网或生产。

### 环境变量配置示例（生产）

```bash
GUGU_ENVIRONMENT=production
GUGU_HTTP_ADDR=0.0.0.0:8080
GUGU_WEB_ROOT=/app/web
GUGU_PUBLIC_URL=https://panel.example.com
GUGU_DATABASE_URL=postgres://user:pass@db:5432/gugumanager?sslmode=require
GUGU_REDIS_URL=redis://redis:6379
GUGU_SESSION_KEY_FILE=/run/secrets/session-key
GUGU_ENCRYPTION_KEY_FILE=/run/secrets/encryption-key
GUGU_AGENT_CA_CERT_FILE=/run/secrets/agent-ca.crt
GUGU_AGENT_CA_KEY_FILE=/run/secrets/agent-ca.key
GUGU_BOOTSTRAP_TOKEN_FILE=/run/secrets/bootstrap-token
GUGU_TLS_TERMINATED=true
GUGU_AGENT_GRPC_ADDR=0.0.0.0:8443
GUGU_AGENT_REGISTRATION_TOKEN=<registration-token>
GUGU_AGENT_TOKEN=<agent-http-token>
GUGU_FILE_ROOT=/var/lib/gugumanager/servers
```

---

## 10. 测试与 CI

### 后端测试

```bash
go test ./...                                          # 全部测试
go vet ./...                                           # 静态检查
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...  # 漏洞扫描
```

PostgreSQL 迁移集成测试需 `GUGU_TEST_DATABASE_URL` 环境变量。

### 前端测试

```bash
cd web
npm test           # Vitest 单元测试
npm run typecheck  # TypeScript 类型检查
npm run build      # 生产构建
npm run e2e        # Playwright E2E（需先 npx playwright install chromium）
```

E2E 测试使用构建后的 SPA 和同源真实 Go Control Plane，固定监听 `127.0.0.1:18080`，验证 Chromium 深链、Cookie/CSRF 会话、固定服务器详情与退出。

### Protobuf 契约校验

```bash
go run github.com/bufbuild/buf/cmd/buf@v1.72.0 lint
go run github.com/bufbuild/buf/cmd/buf@v1.72.0 breaking --against api/proto/agent-v1.baseline.binpb
```

### OpenAPI 契约校验

```bash
cd web
npm run api:lint    # Redocly lint
npm run api:check   # 校验生成类型未漂移
```

### CI 工作流（.github/workflows/ci.yml）

| Job | 内容 |
| --- | --- |
| `go` | gofmt + Buf format/lint/breaking + 生成代码校验 + go test/vet/govulncheck + gamectl lint |
| `web` | npm ci + audit + api lint/check + vitest + typecheck + build |
| `openapi-compatibility` | oasdiff 破坏性变更检测（PR） |
| `postgres-migrations` | PostgreSQL 17 上迁移集成测试 |
| `browser-e2e` | Playwright Chromium E2E（真实 Control Plane + 构建 SPA） |
| `development-container` | Docker 构建 + 回环启动 + `/readyz` 烟测 + Trivy 漏洞/SBOM + 可修复 HIGH/CRITICAL 门禁 |

---

## 11. 部署

### Dockerfile（deploy/Dockerfile）

多阶段构建：

1. **web-build**（`node:24-alpine`）：`npm ci` → `npm run build` 生成 SPA 静态文件。
2. **go-build**（`golang:1.26-alpine`）：`go mod download` → `go build` 生成静态二进制 `gugu-control-plane`。
3. **runtime**（`alpine:3.23`）：非 root 用户（UID 10001），复制二进制和 SPA，默认 `GUGU_ENVIRONMENT=production`（fail-closed）。

### 生产部署要点

- 镜像默认进入 production fail-closed 路径，必须显式提供全部生产环境变量才能启动。
- Control Plane 前需 TLS 终止反向代理（`GUGU_TLS_TERMINATED=true`）。
- Agent gRPC 监听需独立证书与 mTLS CA（`GUGU_AGENT_CA_*`）。
- PostgreSQL 迁移在控制面启动时自动执行（`RunMigrations`）。
- 密钥文件（session key、encryption key、CA key、bootstrap token）应通过 secret 机制注入，不应硬编码。

### 开发与生产边界

> 详见 [ADR-0001](adr/0001-development-slice.md) 与 [ADR-0002](adr/0002-development-data-lifecycle.md)。

- 开发适配器数据随进程退出丢失（文件除外）；生产使用 PostgreSQL 持久化。
- 开发模拟电源/指标/控制台；生产由 Agent 上报真实状态。
- 开发单进程内存会话；生产需多副本一致撤销（尚未实现）。
- 不得把开发默认账号、密码、Token 用于共享网络或生产环境。
