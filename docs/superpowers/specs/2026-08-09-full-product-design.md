# GuGuManager 完整产品设计 Spec

日期：2026-08-09
状态：已确认
范围：完整产品（阶段 1-4），本次会话从阶段 1 开始逐步实施
部署：本地开发验证 → 部署到 Linux 服务器（普通节点 + NAT 节点）

## 1. 目标

把当前"开发垂直切片"（内存 Store + 模拟节点）演进为**生产可用的完整游戏开服面板**：

- 真实 Node Agent（出站 mTLS/gRPC），支持普通服务器与 NAT 服务器两种节点
- PostgreSQL 作为唯一业务事实源，Redis 仅作可重建缓存/限流/广播
- 真实 Docker OCI 容器编排：安装、启停、日志、指标、控制台、文件、备份
- 一键部署脚本，简单接入节点
- 完成阶段 1→2→3→4 全部能力（Extension ABI、S3 备份、定时任务、通知等）

## 2. 架构

```
┌─────────────┐  HTTPS  ┌──────────────────────┐        ┌────────────────┐
│  Web Browser │ ──────▶ │   Control Plane      │        │  Node Agent    │
│  (React SPA) │         │  Go + PostgreSQL +   │ ◀───── │  (Go, 出站)    │
└─────────────┘          │  Redis               │ mTLS   └───────┬────────┘
                         └──────┬───────────────┘ gRPC/心跳      │ Docker
                                 │                                ▼
                           PostgreSQL/Redis              Docker Engine
```

- Agent 主动出站拨号 Control Plane，普通节点与 NAT 节点无差异
- Control Plane 不直接访问节点 Docker Socket 或宿主文件系统
- 所有状态变更走持久 operation 任务，幂等、可重试、带 generation 栅栏

## 3. 组件演进

### 3.1 Control Plane (`cmd/control-plane`)
- PostgreSQL Store 适配器（替换开发内存适配器；保留 development 模式）
- Agent gRPC 服务器：mTLS（CA 签发 Agent 证书）、注册、心跳、能力协商、任务下发
- 持久任务队列 + Outbox：任务租约、重试、checkpoint
- 持久会话、令牌、membership 与跨副本撤销
- `/readyz` 真实验证数据库/迁移就绪

### 3.2 Node Agent (`cmd/agent`)
- 出站 mTLS gRPC 客户端，自动重连
- 注册流程：一次性注册令牌 → CSR → 签发证书 → 心跳
- Docker Runtime 适配器：容器创建/启停/日志/指标/健康检查
- 文件服务（受限根目录）、控制台流、备份执行（tar/恢复）

### 3.3 Web (`web`)
- 现有 UI 适配真实 API 与持久数据
- 控制台实时流（WebSocket 或 gRPC-Web）
- 真实指标展示、真实备份/文件数据

### 3.4 部署 (`deploy`)
- Control Plane：docker-compose（control-plane + postgres + redis + 反代说明）
- 节点：一键安装脚本（生成密钥 → 注册 → 建立 mTLS）
- 普通节点与 NAT 节点同一接入路径

## 4. 数据层

- PostgreSQL 表：nodes、servers、server_tasks（Outbox）、sessions、tokens、memberships、allocations、startup_variables、audit、backups、files 元数据、scheduled_tasks、notifications、organization_quotas
- 迁移遵循展开/收缩模式，只追加、已发布不改写
- Secret 加密存储：加密密钥文件，Agent 侧 Secret 句柄投递

## 5. 阶段划分与完成信号

### 阶段 1：真实垂直能力
1. `1A Identity`：PostgreSQL 持久会话/令牌/membership/撤销
2. `1B Node`：注册令牌、CSR、mTLS、gRPC、心跳、能力协商、证书轮换、离线检测
3. `1C Catalog`：完整 Schema、固定 Bundle、可拉取 PaperMC 声明式包
4. `1D Provisioning`：真实 OCI 安装、持久幂等任务、端口事务
5. `1E Operations`：启停、只读日志、基础指标、状态对账

**完成信号**：全新环境 15 分钟内接入节点并创建可运行 PaperMC 服务器；重复操作不产生重复容器或端口。

### 阶段 2：可用生产 MVP
- 多节点与端口分配
- 控制台双向流、Agent 文件服务、资源配额、持久审计
- Factorio 声明式参考包
- 本地备份、停服恢复、失败重试、状态对账
- 用户与 membership 跨副本撤销

**完成信号**：2 节点、50 个已创建服务器、10 个同时运行服务器烟雾测试；节点失联 30 秒内标记 offline。

### 阶段 3：扩展生态
- Extension ABI、WASI/隔离 OCI Runner
- Catalog 签名、SBOM、社区模板与一致性服务

### 阶段 4：平台化
- S3 备份、保留策略、细粒度协作、定时任务、通知
- 自动放置、节点排空、迁移、组织配额

## 6. 安全

- 默认拒绝权限，服务端重复授权（沿用现有原则）
- mTLS：Agent 证书轮换与吊销
- Secret 不回显、不落地明文；加密密钥文件管理
- 文件访问路径/链接/设备文件逃逸阻断
- 审计：操作者、目标、结果、时间、operation ID 持久化

## 7. 测试

- Go 单元测试、PostgreSQL 集成测试（真实库）、契约测试（OpenAPI/Buf/Schema）
- Docker Agent 集成测试（真实容器）
- 游戏包一致性测试（PaperMC/Factorio）
- E2E：初始化/登录 → 节点注册 → 服务器创建 → 电源 → 控制台 → 文件 → 备份 → 审计
- 安全测试：越权、CSRF、路径逃逸、Secret 泄露、重放

## 8. 部署目标

- 面板服务器：`103.42.182.201`（root）
- NAT 节点：`160.202.238.49:49489`（root）
- 先本地开发验证，再部署到上述服务器
