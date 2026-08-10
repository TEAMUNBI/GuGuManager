# 11 测试、路线图与验收

## 1. 测试层级

生产 MVP 的目标测试矩阵包括：

- 单元测试：状态维度、任务迁移、幂等、权限、配额、路径校验和错误映射。
- 契约测试：OpenAPI、Agent Protobuf、GameDefinition Schema 和 Extension ABI。
- 数据库集成：并发初始化、端口唯一性、任务租约、Outbox、恢复锁和事务回滚。
- Agent 集成：真实 Docker Engine 上的容器、端口、日志、文件、资源限制和重启盘点。
- 游戏包一致性：安装、配置、启动、健康、停止、更新、备份、恢复和幂等重试。
- 端到端：初始化/登录、节点注册、服务器创建、电源、控制台、文件、备份和审计。
- 安全测试：越权、CSRF、路径与链接逃逸、恶意包、Secret 泄露、重放和资源耗尽。

上述层级是目标矩阵。当前已具备的测试：Go 侧 18 个包全绿（含 Postgres store 的 ControlPlane 契约断言、000006 指标/日志持久化与跨重启恢复、迁移集成、Agent 执行器与 60+ 文件操作测试）、前端 206 个单元测试与 Chromium E2E；尚未具备真实 Docker 节点上的游戏生命周期一致性、跨浏览器矩阵与 Outbox/多副本恢复测试。

## 2. 当前本地与 CI 门禁

当前 CI 工作流声明以下门禁；本轮本地执行结果与未验证边界见最新 `docs/changes/GM-*`：

- `gofmt` 差异检查。
- `go test ./...`、`go vet ./...` 与固定版本 `govulncheck` 的可达 Go 漏洞扫描。
- 对 PaperMC、Factorio 和 Vintage Story 示例执行 `gamectl lint`；当前 lint 包含 Draft 2020-12 Schema、单 JSON 值、端口名称唯一性、非 `process` 健康检查的 `health.portRef`、closed-object 启动变量子集、default/const/范围/长度/enum 语义、安全整数域、`required`/Secret/binding 引用，以及 file binding/Artifact destination 的可移植规范相对路径与重复目标校验。
- 固定 Buf CLI 和 Go 生成插件版本，执行 Protobuf format/build/lint、仓库基线 breaking 检查、生成制品漂移检查；PR 另与目标分支比较兼容性。
- Redocly OpenAPI 3.1 lint、`openapi-typescript --check` 生成漂移检查；PR 由 oasdiff 与目标分支比较 breaking changes。
- `npm audit --audit-level=high`、`npm test`、`npm run typecheck` 与 `npm run build`。
- PostgreSQL 17 service 中的 `000001_core`/`000002_identity`/`000003_membership_permissions` 顺序 up、关键约束断言、逆序 down 与对象移除测试；`000003` 会覆盖 legacy 空权限 membership 的回填，并拒绝缺少 `servers.read`、未知、重复或 `NULL` permission。测试使用唯一 Schema，且连接前要求显式 URL 和以 `_test` 结尾的数据库名。
- Agent 与 Postgres store：`internal/agent` 的 Docker 任务执行器（provision/power/backup）与 60+ 文件操作测试（含备份下载、停止态容器写入、容器状态保持），`internal/store` 的 Postgres ControlPlane 契约测试（编译期断言覆盖全部契约方法）、000006 指标/日志持久化与跨重启恢复测试（`TestPostgresMetricsAndConsolePersistAcrossRestart`：在真实 PG 上经 `NewPostgres` 执行全部迁移后写入，再以新 store 实例 `RestoreTelemetry` 恢复断言）与迁移集成测试。
- Chromium E2E 使用构建后的 SPA 和同源真实 Go Control Plane，验证未登录深链、登录、固定服务器详情与退出，并要求观察真实 API 响应。
- 对同一 `github.sha` 开发 Control Plane 镜像执行回环 `/readyz` 与 SPA 烟雾测试，生成 Trivy 漏洞 JSON 和 CycloneDX SBOM，并阻断存在修复版本的 HIGH/CRITICAL 漏洞。

本地没有 PostgreSQL 时迁移集成测试会明确 `Skip`；这只验证跳过路径，真实 SQL 结果必须来自 PostgreSQL job。Protobuf/OpenAPI 的 PR 目标分支比较需要 Git 历史，本工作区没有 `.git`，因此本地只能执行仓库基线比较和工作流静态检查。

当前 CI 尚未声明：真实数据库并发初始化/租约测试、真实 Docker 节点上的游戏生命周期一致性、跨浏览器矩阵、镜像签名、来源证明与通用仓库 Secret/IaC 扫描。浏览器 E2E、容器启动、Trivy 和 SBOM 已进入工作流；本工作区没有 `.git` 或 Docker，本机已真实运行 Go 18 个包（含 Agent 文件操作与 Postgres store 契约测试）、前端 206 个测试和 Chromium E2E，CI 步骤本身仍需 GitHub run 证明。未真实执行的检查不得在 PR 或发布记录中写成已通过。

## 3. 目标 CI 与发布门禁

常规 PR 的目标门禁为格式化、静态检查、单元测试、OpenAPI/Protobuf/Schema 契约测试、迁移测试、前端测试与构建以及依赖扫描。

需要真实 Docker 节点或运行不可信游戏包的测试在隔离 CI 或 nightly 执行，并作为生产发布门禁；社区 PR Runner 不持有生产凭据。目标门禁只有在工作流文件实际执行且保存结果后才能标记完成。

## 4. 阶段 0：工程基础

当前已经交付：

- Go 模块、React 前端、开发配置、OpenAPI、Protobuf、Schema、`000001_core`/`000002_identity`/`000003_membership_permissions` 迁移骨架和 Compose 骨架。
- 开发内存适配器、模拟节点、开发 Agent 心跳与 `gamectl init/lint`。
- 状态维度、幂等、电源互斥、generation fencing、路径校验和前端测试基础。
- Argon2id 开发口令、Session Token 摘要、受控内存 setup、本地用户/重置/membership API 与开发页面、资源授权、reservation 限流、Argon2 并发门、生产配置字段校验与迁移 dry-run 清单。
- 受限开发 `ServerFS`、文件写入状态、备份恢复/删除状态机和 30 秒节点离线对账。
- 基于固定 Bundle Schema 的 Startup 变量编辑，以及单主 Allocation 的 Network 增删/切换与 generation fencing。
- 可从构建后的 Control Plane 或 Vite 启动管理界面。
- OpenAPI/Protobuf 的固定版本解析、生成、漂移和兼容门禁，以及 PostgreSQL 迁移 up/down 与基础约束集成测试。
- GameDefinition 文件绑定和安装 Artifact 目标的跨平台静态路径逃逸、非规范表示、长度及重复覆盖门禁。
- 构建 SPA 后由真实开发 Control Plane 托管的单浏览器 E2E，以及开发镜像启动、漏洞报告、CycloneDX SBOM 与可修复高危漏洞 CI 门禁定义。

阶段 0 仍待补齐：

- 对 GameDefinition 安装制品、权限、当前子集之外变量能力和网络策略的完整语义校验。
- GitHub Runner 上的真实容器门禁证据、跨浏览器/nightly 矩阵、不可变基础镜像 digest、签名、来源证明与发布 attestation。

当前完成信号只表示新开发者能启动开发 Web 与内存 Control Plane、执行已有测试并清楚看到模拟边界，不表示生产基础设施验收完成。

## 5. 当前开发切片验收

当前切片只验收：

- 开发管理员能够登录、退出，并对写请求使用 CSRF；并发登录尝试不会在 Argon2 前绕过账号／来源 IP 限流，非 JSON 登录请求返回 415，匿名 setup／登录／重置 body 超过 64 KiB 会被拒绝。
- 未初始化开发适配器能够在 Web 显示 setup，使用有效 Bootstrap Token 原子创建首个平台管理员，并拒绝过期、错误或重放令牌。
- 平台管理员能够通过 `Users` 页面和对应 API 查询、创建、更新或停用本地用户，签发短期单次密码重置令牌；匿名重置页面可以消费令牌，停用或重置会撤销目标用户旧会话。
- 平台管理员能够通过 `Users` 页面和对应 API 查询、授予/替换或撤销 server membership；变更立即影响服务器列表、详情、operation 查询和受保护 REST 资源。Store 会在写入副作用与幂等 replay 前重新授权；operation 查询按其 `serverId` 校验当前 `servers.read`，撤权后即使知道 operation ID 也会被拒绝。撤权后的慢 HTTP 请求及文件写入均不产生新副作用。当前不验收实时连接撤销或撤权前已受理异步 operation 的取消。
- 用户能够浏览总览、节点、游戏目录、服务器和审计页面；平台管理员还能够访问用户与服务器授权管理页面。
- 服务器创建、电源、备份创建、恢复、删除以及 Network/Startup 对账返回可轮询 operation；幂等键复用和互斥操作具有明确结果。当前 operation 还公开不可变 `nodeId` 节点快照、`attempt/maxAttempts`、运行 checkpoint、短暂内存 lease 和结构化错误；queued、running、succeeded 与 failed 生命周期均有回归覆盖。provision、电源、备份创建、恢复、备份删除和 reconcile 的 stale generation 或节点重分配以不可重试的 `OPERATION_STALE` 收敛，不能出现“资源未应用但 operation 成功”或“旧节点任务在新节点归属下成功”的假终态。Go 回归覆盖 stale 与备份摘要损坏、备份缺失的组合优先级，以及 Server 缺失时的 failure 审计；Web Mock 对六类路径逐一验证 stale 终态和副作用隔离。HTTP 回归还验证 `nodeId` 必填且 nullable 执行字段必须显式编码为 `null`。这里只验收单进程开发元数据，不验收持久任务、租约抢占、自动重试、Outbox、Agent 进度、节点迁移编排或跨副本恢复。
- 创建后的服务器同时固定正确的定义版本、上游游戏版本和 Bundle 摘要；目录记录变化不反向修改已有快照。
- 文件页面能够在受限开发数据根中读写、建目录、移动和删除；新服务器同步获得空数据根。
- 备份页面能够演示创建、停服恢复、摘要元数据拒绝、恢复锁和异步删除；这些操作不包含真实归档。
- 节点在 30 秒无心跳后由读取对账或后台 sweep 标记 offline，并阻止创建服务器、电源、备份、恢复、Network 和 Startup 任务。
- Network 能增删或切换内存 Allocation，保持单一 primary，拒绝 unspecified 地址与精确 endpoint 冲突，并以十进制 generation 拒绝过期写入；不验收真实端口绑定或多端口映射。
- Startup 只接受服务器固定 Bundle 的 string/integer/boolean closed-object 子集，default/const 和约束可执行，integer 限于 JavaScript 安全域；缺少 required 值时 Start/Restart 在创建 operation 与状态变化前失败。Secret 只允许替换或清除，公开视图省略 value/default/const/enum；不验收持久加密、Secret 句柄或 Agent 投递。
- 桌面和移动端主要页面没有横向溢出，核心导航和操作可用。
- 进程退出后内存控制面状态重置，开发文件根中的服务器文件保留；固定种子重新挂接，动态服务器旧目录按 ADR-0002 的孤儿目录策略处理，界面明确显示 `development`。

模拟指标、内存备份、开发本地文件根、REST 控制台和开发心跳不能替代生产验收。

## 6. 当前阶段 1 状态

- `1A Identity`：已完成开发 Argon2id 口令、受控 Bootstrap setup、本地用户管理页面、单次密码重置页面、用户停用/重置会话与 reset token 撤销、membership 资源授权、in-flight reservation 限流、Argon2 并发门、写入提交前重新授权、production 配置硬门禁，以及 PostgreSQL 持久会话（token 摘要 + `csrf_digest`，会话恢复时轮换 CSRF）；Redis 协调、多副本与实时连接撤销未完成。
- `1B Node`：已完成一次性注册令牌、CSR、mTLS/gRPC Enroll/Connect 双向流、证书轮换、吊销、心跳与 30 秒离线判定。
- `1C Catalog`：保留 Schema 与固定摘要校验，固定目录已持久化到 PostgreSQL；签名、审核工作流和可拉取 PaperMC Bundle 未完成。
- `1D Provisioning`：已完成 PostgreSQL 事务、Allocation 关联、持久任务表与 Agent 的 OCI provision；Outbox 投递与自动放置未完成。
- `1E Operations`：已完成真实 Runtime 电源收敛、控制台命令帧、备份创建/恢复/删除/下载与文件操作，指标与控制台日志已持久化到 PostgreSQL（000006 迁移，控制面重启经 `RestoreTelemetry` 恢复）；实时控制台 WebSocket 未完成。

因此阶段 1 的真实垂直能力已基本完成；剩余未闭合项为实时控制台 WebSocket、加密 Secret 静态存储、Outbox/多副本恢复与真实 Docker 节点上的游戏生命周期一致性测试。

## 7. 阶段 1：首个真实垂直能力

阶段 1 拆成可独立验收的切片：

1. `1A Identity`：受控初始化、管理员登录、持久 Cookie 会话、CSRF、撤销、限流和审计。
2. `1B Node`：一次性注册令牌、CSR、mTLS、心跳、能力协商、证书轮换和离线检测。
3. `1C Catalog`：完整 Schema、签名审核、固定 Bundle 与 PaperMC 声明式包。
4. `1D Provisioning`：单节点、单端口服务器创建、OCI 安装和持久幂等任务。
5. `1E Operations`：启动、停止、只读日志、基础指标和状态对账。

完成信号：全新受支持环境可在 15 分钟内接入节点并创建可运行的 PaperMC 服务器；重复操作不产生重复容器或端口。重启、文件、备份恢复已随真实 Agent 落地；多节点调度和实时控制台流延后到阶段 2。

## 8. 阶段 2：可用生产 MVP

- 多节点与端口分配。
- 控制台双向流、文件管理、资源配额和持久审计。
- Factorio 声明式参考包，验证不同端口与存档模型。
- 本地备份、停服恢复、失败重试和状态对账。
- 本地用户管理与 server membership 的 PostgreSQL 持久化、生产管理页面和跨副本撤销；授权撤销会使 API 与实时连接立即失效。
- `Network` Allocation 事务和 `Startup` Schema 变量编辑，过期 generation 写入被拒绝。

完成信号：2 个节点、50 个已创建服务器、10 个同时运行服务器的烟雾测试通过；节点失联 30 秒内标记 offline 并禁止新任务。

## 9. 阶段 3 与 4

阶段 3：稳定 Extension ABI、WASI/隔离 OCI Runner、Catalog、签名、SBOM、社区模板与一致性服务。第三方不修改 Control Plane 即可提交新游戏包。

阶段 4：S3 备份、保留策略、细粒度协作、定时任务、通知、自动放置、节点排空、迁移、Windows/VM Runtime 和组织配额。只在核心协议稳定后进入。

## 10. 生产 MVP 验收

- 首次初始化不可抢注，服主无法访问其他用户资源。
- 管理员可创建/停用用户、签发一次性本地密码重置令牌并授予/撤销 server membership；令牌过期或重放被拒绝，重置后旧会话失效，membership 撤销后旧 Cookie、下载和实时连接均不能继续访问该服务器。当前开发页面与 API 已验证内存状态、令牌流程和后续 REST 请求授权，但没有持久事实、多副本传播、下载令牌或实时连接关闭，不能替代该生产验收。
- 节点注册、证书轮换、离线和吊销按协议工作。
- 创建服务器不产生重复容器、端口或无法释放的分配。
- `Network` 增删/切换主 Allocation 保持节点端口唯一性，冲突事务完整回滚；`Startup` 只接受 GameDefinition 支持子集声明的变量，缺 required 值不能启动，Secret 不回显任何值或候选元数据且更新递增 generation。
- 重复启动、停止和重启复用 operation 或返回明确冲突。
- 控制台支持授权、实时收发和序号重连。
- CPU、内存、磁盘和网络限制由 Runtime 实际执行。
- 文件 API 阻断路径、链接、设备文件和压缩包逃逸。
- 备份可恢复到已知存档，恢复期间持有排他锁并保留结果。
- 节点离线、镜像失败、端口冲突、磁盘不足和扩展超时返回结构化、可执行错误。
- 关键操作均有包含操作者、目标、结果、时间和 operation ID 的持久审计。
- PaperMC 与 Factorio 一致性测试通过，且不修改 Control Plane 核心代码。
- GameDefinition 门禁拒绝缺失或仍为 `latest` 的上游发行版本，并拒绝把同一 `(id, definitionVersion)` 重新绑定到不同 Bundle 摘要。

当前开发垂直切片只验收第 5 节明确列出的开发行为，不能用模拟结果替代上述生产验收。
