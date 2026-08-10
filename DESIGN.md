# GuGuManager 设计索引

> 文档状态：阶段 0 工程基线与阶段 1 垂直能力；生产适配器（PostgreSQL store + mTLS Agent）已实现，完整生产发布仍待补强
>
> 文档版本：0.8.0
>
> 更新日期：2026-08-10

GuGuManager 是面向自托管用户和小型游戏托管团队的开源游戏服务器管理平台。项目采用“模块化单体控制面 + 独立节点 Agent”，以版本化游戏定义接入不同 Dedicated Server。

本文件只维护文档导航、权威契约映射和当前实现边界。每个主题只在一个章节中定义，跨模块或破坏兼容性的决策记录在 ADR 中。

## 前端视觉权威

- [Liquid Command 设计系统](design-system/gugumanager/MASTER.md) 是当前前端视觉方向、颜色角色、排版、材料、密度与动效约束的唯一事实源，并取代已退役的《06.1_UI设计语言.md》与此前暖色编辑式方向。
- [Web 设计实现记录](web/DESIGN.md) 用于记录前端落地后的组件、页面与响应式细节；该文件由前端设计文档流程维护，并始终从属于 Liquid Command 主规范。
- 本文件继续作为系统架构与工程文档索引，不复制视觉规范，也不改变安全、权限、协议和领域契约的权威归属。

## 章节

| 章节 | 权威内容 |
| --- | --- |
| [01 产品范围](docs/design/01-product-scope.md) | 定位、目标、非目标、角色与版本范围 |
| [02 用户体验](docs/design/02-user-experience.md) | 信息架构、核心流程、页面状态与权限可见性 |
| [03 系统架构](docs/design/03-architecture.md) | 组件边界、部署拓扑、信任边界与数据流 |
| [04 领域与任务模型](docs/design/04-domain-model.md) | 实体、分离状态机、幂等任务与事务不变量 |
| [05 Agent 与 Runtime](docs/design/05-agent-runtime.md) | 注册、通信、心跳、Runtime 与节点恢复 |
| [06 游戏包规范](docs/design/06-game-packages.md) | GameDefinition、生命周期、Bundle 与发布 |
| [07 Extension ABI](docs/design/07-extension-abi.md) | 扩展边界、Host API、沙箱和版本兼容 |
| [08 API 契约](docs/design/08-api-contracts.md) | REST、实时协议、错误、分页和版本策略 |
| [09 安全基线](docs/design/09-security.md) | 身份、RBAC、Secret、文件与供应链安全 |
| [10 运维与部署](docs/design/10-operations.md) | 配置、部署、迁移、备份、恢复与可观测性 |
| [11 测试与路线图](docs/design/11-testing-roadmap.md) | 测试矩阵、阶段计划和验收标准 |
| [12 工程与治理](docs/design/12-engineering-governance.md) | 仓库结构、模块规则、许可和社区治理 |
| [贡献与发布](docs/development/CONTRIBUTING.md) | 核心开发、游戏包、发布和安全事件流程 |
| [本地开发](docs/development/LOCAL_DEVELOPMENT.md) | 工具链、启动、开发数据生命周期和验证命令 |
| [实现记录](docs/changes/README.md) | `GM-*` 变更记录索引 |
| [架构决策](docs/adr/README.md) | 跨模块和不可逆决策 |

## 机器可读契约

设计文档解释约束，以下路径在实现后分别作为机器契约的唯一事实源：

- `api/openapi/openapi.yaml`：公共 REST API。
- `api/proto/gugumanager/agent/v1/agent.proto`：Control Plane 与 Agent 协议。
- `spec/game-definition/v1alpha1.schema.json`：GameDefinition Schema。
- `migrations/`：PostgreSQL 数据结构和迁移顺序。
- `web/src/lib/types.ts`：当前与 OpenAPI 人工同步的前端类型；接入代码生成后由生成类型替代，不得独立发明字段。

机器契约与章节冲突时，在合并前必须修正文档或契约，并通过 ADR 说明兼容性影响。

## 当前实现边界

本轮交付是可运行的开发垂直切片 + 已实现的生产适配器，不冒充完整生产发布：

- 包含管理员开发登录、首次 setup、本地用户与服务器访问管理、一次性密码重置、总览、节点、游戏目录、服务器创建、电源操作、控制台、文件、备份（含下载）、Network、Startup 和审计界面；生产模式经真实 Agent 执行建服、电源、备份、文件与控制台命令。
- Control Plane 提供版本化 REST API、结构化错误和幂等异步任务；开发模式使用内存存储，生产模式使用 PostgreSQL store（任务经数据库任务表投递给 mTLS gRPC Agent）。每个 operation 固定受理时的目标节点快照，并在提交终态前同时校验 generation 与节点归属。
- `Network` 在内存中维护单主 Allocation，并以 generation 和模拟 `reconcile` operation 演示增删与切主；它不预留宿主端口，也没有面向多端口 Bundle 的 `portRef`。
- `Startup` 从服务器固定 Bundle 的受限 GameDefinition Schema 解析命令和变量；缺少 required 值时 Start/Restart 在状态变化前被拒绝。Secret 禁止 Bundle 内置 default/const/enum，公开响应只保留 `hasValue` 等声明状态，并使用 HMAC 幂等摘要；这不等同于加密持久化或 Agent Secret 句柄。
- `1A Identity` 开发适配器与 Web 提供受控首次初始化、本地用户查询/创建/更新/停用、短期单次密码重置和服务器 membership 管理。Bootstrap Token 与重置令牌只保存 SHA-256 摘要；无效、过期或已消费的 setup/reset 凭据会在 Argon2 前被拒绝，密码重置或用户停用会撤销该用户的内存会话和未消费重置令牌。登录、setup 与重置使用 reservation 限流，Argon2id 受进程级并发门保护；membership 变更会立即影响当前 REST 资源授权。
- 开发 Store 在创建服务器、电源、Network、Startup、控制台、备份和文件写入的提交／幂等回放前重新读取当前用户与 membership，不信任 HTTP session 的旧用户快照。文件写入、建目录、移动和删除与用户停用、角色降级或 membership 撤销互斥，避免撤权后仍产生本地磁盘副作用；这不取消撤权前已经受理的异步 operation。
- Agent 与 `gamectl` 提供可运行入口；Agent 的真实 mTLS/gRPC（Enroll/Connect 双向流）、OCI Runtime 和持久任务执行已实现并接入生产模式。
- PostgreSQL 迁移 000001-000006 齐备，生产 Control Plane 启动时按序执行；Identity 数据、令牌、会话、membership 与审计已由 PostgreSQL store 持久化（token/CSRF 只保存摘要，会话恢复时轮换 CSRF 并返回新明文）；指标与控制台日志经 000006 迁移持久化（`server_metrics`/`server_metric_history`/`console_logs`），控制面启动时 `RestoreTelemetry` 恢复内存缓冲。任务入队/完成与 `outbox_events` 事件同事务落库，每副本发布器（`FOR UPDATE SKIP LOCKED`）消费标记 `published_at`；过期任务租约由 `ReconcileTaskLeases` 回收（回队或按 `MAX_ATTEMPTS` 判失败），多副本故障切换不卡死任务。开发模式（Memory store）仍随进程退出而丢失；Redis、多副本撤销与实时连接撤销尚未实现。
- PaperMC、Factorio 与 Vintage Story 作为声明式示例，不依赖尚未实现的 Extension ABI。
- `gamectl lint` 当前解析 JSON、执行内嵌 Draft 2020-12 Schema，要求明确的上游发行版本，并检查端口名称唯一性、非进程健康检查的 `health.portRef`、变量 closed-object 子集、`required`/Secret/binding 引用、Secret material 禁令，以及 file binding 与 Artifact destination 的规范相对路径；制品可下载性、摘要真实性、权限和复杂网络语义仍属于后续门禁。
- Network/Startup 开发适配器拒绝 `0.0.0.0`、`::` 等 wildcard 绑定地址，精确 endpoint 冲突返回 `409 PORT_CONFLICT`，过期十进制 generation 返回 `412 PRECONDITION_FAILED`；这些检查只针对内存期望状态，不代表节点 OS 端口已经绑定。
- 开发 Compose 只允许默认发布到宿主回环地址；仓库内默认账号、密码和 Agent Token 只用于本机开发，不能用于共享网络或生产环境。

当前与目标 API、页面和验证边界分别见 [API 契约](docs/design/08-api-contracts.md)、[用户体验](docs/design/02-user-experience.md) 和 [测试路线图](docs/design/11-testing-roadmap.md)。除“当前开发切片”明确列出的行为外，其余内容均为生产 MVP 或后续目标。

禁止在 README、界面或发布说明中把演示状态、模拟指标或内存数据描述为真实容器执行结果；生产模式由真实 Agent 执行的结果可以如实呈现，但加密 Secret、实时控制台 WebSocket 等已知限制不得包装。

## 设计原则

1. 默认拒绝权限，用户只看到被授权的服务器资源。
2. 期望电源、实际电源、节点条件、生命周期和操作状态分别建模。
3. PostgreSQL 业务事实与任务记录是生产事实源；Redis 只承担可重建的队列、缓存和广播。
4. Agent 主动建立出站连接，Control Plane 不直接访问节点 Docker Socket 或宿主文件系统。
5. 所有异步改变状态的请求都有 `operationId`；允许客户端重试的写请求还必须有明确作用域的幂等键。控制台命令等非 operation 写入必须单独定义投递语义。
6. 游戏实例原子快照定义版本、上游游戏版本和不可变 Bundle 摘要，不自动跟随 `latest`。
7. 声明式能力优先；扩展必须在受限 Runner 中运行，不能向控制面或前端注入任意代码。
8. 开发适配器与生产适配器使用同一领域接口，但必须在配置和界面中明确标识。

## 文档维护

- 产品范围、公共协议、状态模型、安全边界或验收标准变化时，先更新对应权威章节或新增 ADR。
- 实现细节由代码和测试表达；文档不复制函数级说明。
- 每次可独立验收的实现创建一个 `docs/changes/GM-YYYYMMDD-NNN.md`。不在每个源码文件头重复维护变更历史。
- 生成文件、锁文件和二进制文件不添加追踪注释，其来源和生成命令写入实现记录。
- 发布前检查协议兼容、迁移与回滚、安全模型、游戏包一致性和文档链接。
