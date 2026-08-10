# 03 系统架构

## 1. 拓扑

```mermaid
flowchart LR
    W["React Web"] -->|"HTTPS / WebSocket"| C["Go Control Plane"]
    C --> P[("PostgreSQL")]
    C --> R[("Redis")]
    C --> O[("Object Storage")]
    A["Node Agent"] -->|"outbound mTLS gRPC stream"| C
    A --> D["OCI Runtime Adapter"]
    D --> G["Game containers"]
    A --> E["Extension Runner"]
```

Control Plane 是模块化单体；Worker 与 HTTP API 可以先部署为同一二进制中的不同运行单元，后续按负载独立扩展。Node Agent 是独立 Go 进程。

## 2. 组件边界

### Control Plane

负责身份、RBAC、节点目录、游戏目录、服务器期望状态、持久任务、审计和公共 API。它不访问节点 Docker Socket、游戏数据目录或宿主端口。

### Worker 与 Reconciler

PostgreSQL 中的任务表和事务 Outbox 是生产事实源。Worker 通过租约领取任务，将任务至少一次投递给 Agent；Reconciler 比较 generation、observedGeneration 与节点观测，创建必要的收敛任务。

Redis 只用于可重建的唤醒、短期缓存、限流和实时广播。Redis 丢失不能导致业务事实或任务永久丢失。

### Node Agent

Agent 主动建立出站 mTLS 双向流，负责节点能力、Runtime、端口、数据卷、日志、指标和扩展 Runner。Agent 使用本地 operation 日志抵御重复投递，并在重启后盘点受管容器。

### Runtime Adapter

统一封装 OCI、未来 VM 或其他 Runtime。游戏包不能直接调用 Runtime；只有 Agent 能调用适配器。生产适配器使用 Docker Engine API（已实现），禁止把 Docker Socket 挂载给游戏容器。

### Extension Runner

只运行明确批准且固定摘要的扩展。扩展通过版本化 Host API 工作，默认无网络、无宿主文件访问、无进程派生。

## 3. 信任边界

| 边界 | 信任结论 |
| --- | --- |
| 浏览器到 Control Plane | 不可信输入；每次请求执行认证、授权、CSRF/Origin 与 Schema 校验 |
| Control Plane 到 Agent | 双向证书身份；任务仍需节点绑定、能力和幂等校验 |
| Agent 到 Runtime | Agent 接近宿主 root 信任级别，是重点加固与升级对象 |
| 游戏容器到宿主 | 不可信工作负载；非特权、最小挂载、资源限制、seccomp/AppArmor |
| GameDefinition/Extension | 未审核供应链输入；签名、来源、摘要、许可和权限检查 |

## 4. 一次异步操作的数据流

1. Web 发送会话、`Idempotency-Key` 和目标 generation。
2. API 在一个数据库事务中校验 RBAC、配额、节点与 Bundle，写业务状态、任务和 Outbox。
3. Worker 领取租约，选择绑定节点的 Agent 流并投递任务。
4. Agent 检查能力、资源、端口、本地 operation 日志和目标 generation。
5. Agent 调用 Runtime 或 Extension Runner，回报进度、检查点和结构化错误。
6. Control Plane 持久化任务结果和审计，再向 Web 广播更新。
7. Reconciler 处理超时、失联、重试与观测漂移。

同一服务器的破坏性任务串行执行；不同服务器可以并行。事件广播不是事实源，客户端重连后总能从 REST 快照恢复。

## 5. 存储与运行时适配器

Control Plane 通过同一领域接口（`httpapi.ControlPlane`）对接两类适配器：生产 Postgres store 与开发 Memory store。

### 生产适配器（PostgreSQL + mTLS gRPC Agent）

生产模式已完整实现：PostgreSQL store 实现领域接口的全部契约方法（编译期断言 `var _ httpapi.ControlPlane = (*Postgres)(nil)`），migrations 000001-000006 在启动前按序执行；节点任务（provision、power、backup、文件操作、控制台命令、备份下载）经数据库任务表投递，由真实 mTLS gRPC Agent 以 Enroll/Connect 双向流执行，无模拟路径。

指标与控制台日志已由 PostgreSQL 持久化（迁移 000006 的 `server_metrics`/`server_metric_history`/`console_logs` 表）。Agent 的 MetricsBatch/LogBatch 帧经 `ApplyServerMetrics`/`RecordConsoleLines` 双写内存缓冲（热读缓存）与数据库，概览页 CPU/内存用量由 `server_metrics` 表聚合（AVG/SUM）；控制面启动时 `RestoreTelemetry` 从 DB 恢复缓冲，重启不丢失。

任务与 Outbox 采用事务化落库：任务入队（`enqueueTaskTx`/`CreateServer`/`EnqueueTask`）与完成（`CompleteTask`）在同一事务内写 `server_tasks` 业务行与 `outbox_events` 事件（`task.created`/`task.completed`），杜绝"业务已写、事件缺失"的半成功状态。每副本运行 Outbox 发布器（`PublishOutboxEvents`，`FOR UPDATE SKIP LOCKED`）消费未发布事件并标记 `published_at`，多副本只消费一次，为实时推送保留统一扩展点。

多副本恢复由租约回收器兜底：`ReconcileTaskLeases` 周期把 `lease_expires_at` 过期的任务重新入队（`attempt < max_attempts`）或按重试上限判终态失败（`MAX_ATTEMPTS` + 系统审计）；`ClaimTask` 只领取 `attempt < max_attempts` 的任务，避免重试耗尽后仍被反复下发。多个控制面副本共享 PostgreSQL 时，领取（`FOR UPDATE SKIP LOCKED`）、发布与回收均幂等且逐行原子，副本故障切换后任务不卡死。

当前已知限制：

- 加密 Secret 静态存储、实时控制台 WebSocket 尚未实现。

### 开发适配器（Memory）

本地垂直切片允许使用内存存储、模拟节点和模拟控制台，但必须满足领域接口与 API 契约。开发模式具备以下限制：

- 进程退出后状态重置。
- 默认纯模拟：sleep 后置成功，不创建真实容器；即使调用 `EnableRealRuntime`，也只有 provision/power 走本地 Docker，备份/恢复/删除与网络对账仍是模拟。
- Network/Startup 只修改内存期望状态并创建模拟 `reconcile` operation；不执行真实容器、端口或资源限制。
- 当前 Allocation 只有单端口 bind IP/port/protocol/primary，Startup Secret 只在内存中保存并不回显。
- `DownloadBackup` 在内存模式返回 `NOT_FOUND`。
- 不生成生产证书，不接受公网部署承诺。
- API 与界面显式返回 `environment: development`。

生产配置不得自动回退到开发适配器；缺少数据库、密钥或 Agent 信任根时必须启动失败。
