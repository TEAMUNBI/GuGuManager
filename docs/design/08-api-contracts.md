# 08 API 契约

## 1. 契约状态

`api/openapi/openapi.yaml` 是当前 REST API 的机器契约。生产 MVP 的生产适配器（PostgreSQL store + mTLS gRPC Agent）已实现并复用同一组 REST 端点；个别已实现端点（如备份下载）尚未进入 OpenAPI，以 HTTP 契约测试为准。本章同时记录生产目标；目标端点只有进入 OpenAPI、实现和契约测试后才可视为可用。

当前门禁使用固定版本 Redocly 解析该 OpenAPI 3.1 文件，并由 `openapi-typescript` 生成 `web/src/lib/openapi.generated.ts`；PR 还会与目标分支执行 breaking comparison。生成类型只提供编译期漂移证据，不替代服务端或浏览器运行时响应校验。

开发专用的 `/api/v1/dev/*` 端点会在 OpenAPI 中单独标记为 development-only，以便当前 Web、测试和本地 Agent 适配器共享准确契约；它们不提供稳定性或生产兼容承诺。生产 Agent transport 已使用 mTLS/gRPC（Enroll/Connect 双向流），不沿用开发 Bearer HTTP 路由。

## 2. 通用约定

- REST 基础路径为 `/api/v1`；TLS 是生产必需项。
- 携带 JSON request body 的端点只接受 `application/json`，允许参数形式如 `application/json; charset=utf-8`；缺失、畸形或其他 media type 返回 `415 UNSUPPORTED_MEDIA_TYPE`。匿名 setup、登录和密码重置 body 上限为 64 KiB；其他当前 JSON body 上限为 12 MiB。
- 字段名为稳定英文 camelCase。
- ID 为小写 UUID；时间为 UTC RFC 3339；字节数和毫秒数使用整数。
- 成功的单资源响应为 `{ "data": ... }`；可分页列表额外包含 `page`。
- 客户端不得依赖 OpenAPI 未声明的字段。

当前开发切片中 `/servers` 与 `/operations` 声明不透明 cursor 和 `limit`：`limit` 默认 25、最大 100，响应提供 `nextCursor`，排序使用唯一 ID 作为最终稳定键；其他开发列表返回 `{ "data": [...] }`。生产 MVP 中所有可能无界增长的列表都保持该 cursor 分页约定。

### 游戏包版本字段

- `GameDefinition.version` 来自 `metadata.version`，表示定义自身的 SemVer。
- `GameDefinition.gameVersion` 来自 `spec.release.version`，表示上游游戏发行标识。
- `Server.gameDefinitionVersion`、`Server.gameVersion` 和 `Server.gameBundleDigest` 是创建时从同一个已审核目录记录取得的快照。
- `CreateServerRequest` 的版本字段只提交 `gameDefinitionId + gameBundleDigest`；请求仍按 OpenAPI 提交名称、节点和资源字段。服务端验证摘要后填充两个版本字段，客户端不得提交版本或把版本字符串当作完整性身份。

## 3. 当前开发切片端点

| 能力 | 当前端点 | 当前行为 |
| --- | --- | --- |
| 初始化 | `GET /setup/status`、`POST /setup/admin` | 查询 setup 状态；使用短期 Bootstrap Token 原子创建首个平台管理员，成功后消费令牌并关闭 setup |
| 会话 | `POST /auth/login`、`GET /auth/session`、`POST /auth/logout` | 本地用户、Argon2id 口令、仅保存摘要的内存 Cookie 会话、CSRF、过期与撤销 |
| 用户 | `GET/POST /users`、`GET/PATCH /users/{userId}` | 平台管理员查询、创建、更新或停用内存用户；停用会撤销该用户全部内存会话 |
| 密码重置 | `POST /users/{userId}/password-reset-tokens`、`POST /auth/password-reset` | 签发短期单次令牌并匿名消费；明文只返回一次、仅保存 SHA-256 摘要，成功重置后撤销旧会话 |
| 总览 | `GET /overview` | 返回模拟节点、服务器、资源和审计摘要 |
| 服务器 | `GET/POST /servers`、`GET /servers/{id}` | 查询或创建内存服务器；创建返回 provision operation |
| 服务器成员 | `GET/PUT/DELETE /servers/{serverId}/members/{userId}` | 平台管理员查询、授予/替换或撤销 membership；变更立即影响当前 REST 资源授权 |
| 电源 | `POST /servers/{id}/power` | 开发模式模拟 `start`、`stop`、`restart`、`kill` 的异步收敛；生产模式由 Agent 对真实容器执行 |
| Operation | `GET /operations/{id}` | 返回 `serverId`、受理时不可变的 `nodeId` 节点快照、状态、进度、generation、attempt、lease、checkpoint、结构化错误和时间；查询按 `serverId` 重新授权，开发模式执行语义来自内存 worker，生产模式经数据库任务表与 Agent 执行 |
| 控制台 | `GET /servers/{id}/console`、`POST /servers/{id}/console/commands` | REST 快照与命令帧；不是 WebSocket。日志经 Agent LogBatch 帧双写内存缓冲与 PostgreSQL（`console_logs`），控制面重启时恢复 |
| 文件 | `/servers/{id}/files`、`/files/content`、`/files/directories`、`/files/moves` | 开发模式对本地数据根执行安全列表、读写、建目录、移动和删除；生产模式经 gRPC `FileOperation` 由 Agent 执行（list/read/write/mkdir/move/remove/下载备份） |
| 备份 | `GET/POST /servers/{id}/backups`、`POST /servers/{id}/backups/{backupId}/restore`、`DELETE /servers/{id}/backups/{backupId}`、`GET /servers/{id}/backups/{backupId}/download` | 开发模式模拟创建、停服恢复、异步删除与下载（`DownloadBackup` 返回 `NOT_FOUND`），具有幂等、摘要登记与恢复锁；生产模式由 Agent 创建真实归档并可下载 |
| Network | `GET/POST /servers/{id}/allocations`、`PATCH/DELETE /servers/{id}/allocations/{allocationId}` | 开发模式为内存 Allocation 增删与切主，使用 CSRF、幂等键、generation fencing 和模拟 `reconcile`，不绑定真实端口；生产模式经 PostgreSQL 事务与 Agent 对账真实绑定端口 |
| Startup | `GET/PUT /servers/{id}/startup` | 从服务器固定 Bundle 读取命令和受限变量 Schema，内存更新后模拟 `reconcile`；Secret 只返回 `hasValue` 等声明状态 |
| 节点 | `GET /nodes` | 开发模式查询种子节点和开发心跳结果；生产模式支持部署侧注册令牌、mTLS Enroll 注册、心跳、证书轮换与 30 秒离线判定 |
| 开发 Agent | `POST /dev/agent/heartbeat` | 使用 development Agent Token 上报种子节点心跳；仅供开发适配器，不是生产 Agent 协议 |
| 游戏目录 | `GET /game-definitions` | 开发模式查询内存中的开发游戏定义和固定 Bundle 摘要；生产模式从 PostgreSQL 读取已批准目录 |
| 审计 | `GET /audit-events` | 开发模式查询进程内审计记录；生产模式审计持久化到 PostgreSQL。setup、用户、重置、membership 和异步服务器操作均记录审计 |

上述 Identity 路由已有开发 Web 页面：首次 setup、本地用户与角色管理、一次性密码重置令牌签发和消费，以及 server membership 管理。开发模式（Memory store）的数据与令牌摘要保存在单进程内存；生产模式由 PostgreSQL store 持久化同一领域接口，节点任务经真实 mTLS gRPC Agent 执行（详见第 4 节）。仍未实现的能力：Redis 协调、多副本一致撤销、实时连接撤销（console token/WebSocket）与加密 Secret 静态存储。开发模式对未实现的真实副作用不得回退为模拟成功。

## 4. 生产 MVP 目标矩阵

| 能力 | 目标端点或协议 | 状态 |
| --- | --- | --- |
| 初始化 | `GET /setup/status`、`POST /setup/admin` | 已实现：PostgreSQL 原子状态、单次 Bootstrap 摘要校验与 setup 关闭；多副本关闭未实现 |
| 登录与会话 | `/auth/login`、`/auth/session`、`/auth/logout` | 已实现：Argon2id、本地用户、PostgreSQL 持久会话（token 摘要 + `csrf_digest`）、CSRF 会话恢复、账号/IP 限流与撤销；密钥轮换与多副本撤销未实现 |
| 用户与成员 | `/users`、`/users/{userId}`、`/users/{userId}/password-reset-tokens`、`POST /auth/password-reset`、`/servers/{serverId}/members/{userId}` | 已实现：PostgreSQL 持久用户/membership、单次重置、会话撤销与资源授权；跨副本和实时连接撤销未实现 |
| 注册令牌 | 部署侧注册令牌（环境变量注入，回退 bootstrap token） | 已实现：注册令牌经 gRPC `Enroll` 原子消费 |
| Agent 注册 | 版本化 gRPC `Enroll` 服务 | 已实现：消费令牌、校验 CSR 并签发短期客户端证书（mTLS） |
| 节点 | `/nodes` | 已实现：mTLS 注册、心跳、证书轮换、30 秒离线判定与吊销；维护模式和自动放置未实现 |
| Bundle | `/game-definitions` 与版本化 Bundle 资源 | 已实现固定目录与不可变摘要；API 分别暴露签名、验证、可运行和支持证据。当前内置项均为 L0、未验证且不可运行，签名验证与可拉取 Bundle 安装未实现 |
| 服务器 | `/servers`、`/servers/{id}` | 已有 PostgreSQL 事务、Allocation 与 Agent OCI 执行器；当前目录没有可运行 Runtime target，因此新建请求在写入前返回 `PACKAGE_INCOMPATIBLE` |
| Network | `/servers/{id}/allocations`、`/servers/{id}/allocations/{allocationId}` | PostgreSQL 事务与权限已实现；写入要求节点声明 `server.reconcile/v1`，当前 Agent 不声明，故生产路径 fail closed；`portRef`/多端口 Bundle 映射未实现 |
| Startup | `/servers/{id}/startup` | PostgreSQL 持久化、权限与 Secret 密钥环/一次性 mTLS Handle 已实现；修改要求 `server.reconcile/v1`，当前 Agent 不声明，故不会伪装成已对账 |
| 电源 | `/servers/{id}/power` | 已实现：数据库任务表投递 + Agent 真实 Runtime 执行 |
| Operation | `/operations/{id}` | 已实现：必填 `nodeId` 执行节点快照、数据库任务表、attempt、lease、checkpoint、结构化错误与按 `serverId` 的读取授权；Outbox 与跨副本恢复未实现 |
| 实时控制台 | `POST /servers/{id}/console-token` 与 WebSocket | 已有第一版：当前 Session Cookie 握手和进程内 Hub；短期连接 Token、Origin allowlist、sequence 续传和 Redis 多副本广播仍未实现 |
| 文件 | `/servers/{id}/files` 的读写方法 | 已实现：Agent 传输（list/read/write/mkdir/move/remove）与备份下载；解压上传与磁盘配额未实现 |
| 备份 | `/servers/{id}/backups` 及 restore/delete/download | 已实现：真实归档创建/恢复/删除/下载与摘要校验；S3/对象存储与保留策略未实现 |
| 审计 | `/audit-events` | 已实现：PostgreSQL 持久化审计；过滤、分页和保留策略未实现 |

生产目标端点进入 OpenAPI 时必须同时补认证、权限、错误响应、幂等要求和契约测试，不能只增加路由占位符。

备份处于 `creating`、`failed`、`restoring` 或 `deleting` 时，`sizeBytes`、`checksum`、`storageLocation` 与 `completedAt` 可以缺省或为 `null`；只有 `ready` 备份可以用于恢复，且此时摘要必须是完整 `sha256`。生产模式由 Agent 在创建收敛后回传真实归档的 checksum/size/storageLocation 并持久化到数据库；Store 映射数据库 `NULL` 时必须使用可空表示或省略字段，不能把大小映射为 `0`、把摘要映射为空字符串。迟到的重复 Agent 回调对已终态任务是幂等 no-op，恢复或删除失败回到可重试的 `ready`。

## 5. 身份、会话与授权边界

当前登录失败统一返回 `AUTH_INVALID_CREDENTIALS`，不泄露账号存在性。开发模式（Memory store）的 Cookie 会话、用户、角色、setup、reset token 和 membership 保存在进程内存；生产模式由 PostgreSQL store 持久化这些事实。默认快速启动使用已初始化的种子管理员，未初始化构造路径用于 setup 契约和本地开发流程。

`GET /setup/status` 不返回 Bootstrap Token。`POST /setup/admin` 只接受仍有效的部署侧 Token，比较其 SHA-256 摘要并原子创建首个平台管理员；成功、过期或重放均有明确结果。首次管理员初始化和节点注册是不同信任流程，Bootstrap Token 不能复用为 Agent 注册令牌。

平台管理员可以管理本地用户、签发 15 分钟单次密码重置令牌并维护服务器 membership。重置令牌明文只显示一次，服务端只保存 SHA-256 摘要；setup 与 reset 在执行昂贵 Argon2id 前先验证 Token 摘要、到期和消费状态，并在获得写锁后再次验证，避免无效凭据消耗哈希资源或并发重放。匿名消费成功后替换 Argon2id 口令并撤销旧会话，过期或重放统一返回 `AUTH_INVALID_RESET_TOKEN`。用户变为 `disabled` 时同样撤销其会话和全部未消费重置令牌。

登录按账号和直接来源 IP 使用 reservation 限流：正在进行的尝试也会占用阈值，成功只清理账号维度的失败状态，来源 IP 历史保留。setup 和 reset 使用独立的来源 IP reservation 限流。Argon2id 派生受进程级并发门限制为 2 个同时任务，以限制开发 Control Plane 的认证内存峰值；这不是 Redis 分布式限流或多副本全局门。

membership 的写入必须包含 `servers.read`，后续服务器列表、详情、operation 查询和各受保护 REST 路由按权限重新授权。`GET /operations/{id}` 先读取 operation，再以它的 `serverId` 校验当前 `servers.read`；撤销 membership 后，即使调用方仍知道 operation ID，也不能继续读取其状态和节点快照。Store 的写操作在产生副作用、复用 active operation 或返回幂等 replay 前重新读取当前用户与 membership，而不是信任 HTTP session 中的旧用户快照。文件写入、建目录、移动和删除持有服务器门控与 Store 写锁直到物理操作完成，因此停用、角色降级或撤销 membership 与该副作用互斥。删除 membership 后的新 REST 请求、慢请求体提交和幂等 replay 均立即失效；已经在撤权前受理的异步 operation 不会被本开发适配器主动取消。由于当前没有 WebSocket，尚不能验证实时连接主动关闭。

生产会话必须通过 `Secure`、`HttpOnly`、`SameSite=Lax` Cookie 传递；非安全方法需要 CSRF Token，WebSocket 还必须校验 Origin 和短时连接令牌。生产 Identity 已把用户、令牌、会话、setup 和 membership 接入 PostgreSQL（token 与 CSRF 均只保存摘要）；多副本撤销、密钥轮换、审计保留和实时连接失效仍未完成。

## 6. 异步操作、幂等与并发

当前开发切片的服务器创建、电源、备份创建、备份恢复、备份删除、Allocation 写入和 Startup 更新要求 `Idempotency-Key`，长度为 16 至 128 个可打印 ASCII 字符。同一 actor、路由作用域、键和请求摘要返回原 operation；同键不同请求返回 `409 IDEMPOTENCY_KEY_REUSED`。Startup 摘要包含 Secret，但使用每进程随机密钥的 HMAC-SHA256，不保存普通 SHA-256 指纹。开发模式的内存幂等记录和密钥随进程退出而丢失；生产模式将幂等记录持久化到数据库任务表。

`Operation` 响应要求返回必填 UUID `nodeId`，以及 `attempt`、`maxAttempts`、`leaseOwner`、`leaseExpiresAt`、`checkpoint` 和 `error`。`nodeId` 是受理时不可变的执行节点快照，不是查询时从 Server 动态派生的展示字段；幂等回放必须返回原 operation 及其原节点。nullable 字段即使为空也必须显式返回 `null`；`error` 非空时固定包含 `code`、`message` 和 `retryable`。当前 queued operation 固定为第一次也是唯一一次尝试；运行阶段只模拟一个 `development-memory-worker` lease；终态清除 lease。`OPERATION_STALE` 表示开发 Store 在提交 provision、电源、备份创建、恢复、备份删除或 reconcile 结果前发现目标服务器不存在、operation generation 已不再匹配，或服务器当前节点不再等于 operation 的节点快照；它是不可重试的终态错误，并优先于同一完成时刻观察到的备份摘要损坏或备份缺失。development-memory Web Mock 使用相同的服务器存在、generation 和 `nodeId` 完成栅栏，不能把未提交资源副作用的 operation 标记为成功。Web 可以展示安全的错误 message、checkpoint 和尝试次数，但不得向普通用户暴露或依赖 lease owner。创建服务器和电源界面只把 `accepted` 显示为 warning，必须轮询终态后才能显示成功或跳转；failed/canceled 优先展示结构化错误 message。

上述开发契约不构成持久任务执行保证。开发模式进程退出会同时丢失 operation、lease、checkpoint 和幂等记录；生产模式通过数据库任务表持久化 operation/lease/checkpoint，由 Worker 领取租约并经 mTLS 流投递给 Agent，再持久化 Agent 检查点与结果。生产客户端不得把固定 lease 当作任务超时、互斥锁或重试依据。

Allocation 与 Startup 写入还要求 `If-Match`，当前值是服务器 generation 的十进制文本，例如 `If-Match: 12`。缺失或畸形值返回 `422 VALIDATION_FAILED`，过期值返回 `412 PRECONDITION_FAILED`；成功受理返回 `202` `reconcile` operation 并递增 generation。该开发契约不是 RFC entity-tag，尚未提供响应 `ETag`，接入缓存、代理或通用 SDK 前必须迁移到标准强 ETag。

开发 Allocation 拒绝 `0.0.0.0` 与 `::`，同节点精确 `bindIp + port + protocol` 重复返回 `409 PORT_CONFLICT`。它没有检查操作系统监听状态，也没有 Agent Runtime，因此不能产生或验证绑定阶段的异步端口冲突；Allocation 也尚无 `portRef`、`containerPort` 或端口角色。

Startup 只接受固定 Bundle GameDefinition 可执行子集声明的变量；integer 更新限于 JavaScript 安全整数域。Secret 响应不含 `value`、`default`、`constValue` 或 `enumValues`，只用 `hasValue` 表示是否已有值；`null` 表示清除 optional 值。OpenAPI 3.1 Schema 对 `secret: true` 的响应机器约束这四个字段不得出现。Start/Restart 会在创建 operation 和改变 generation/power 前拒绝尚未配置的 required 变量。

上述异步请求成功受理时写入 `result: accepted` 的审计事件。备份、Network 和 Startup 操作还会在开发适配器收敛后记录终态结果；目标 Server 已不存在时使用稳定的 `serverId` 作为审计目标名称，仍写入 `failure`。`accepted` 只表示请求已通过同步校验并创建或复用 operation，不表示异步工作成功；最终结果以 operation 的 `succeeded`、`failed` 或 `canceled` 终态为准。受理审计与 operation 终态是不同事实，不得把 `accepted` 映射为 `success`，也不得仅凭该审计事件推断任务完成。

生产 MVP 还必须满足：

- 幂等记录持久化并按策略保留，不能依赖进程内存。
- 同一服务器的等价活动 operation 可以复用；互斥但不等价的请求返回 `409 OPERATION_IN_PROGRESS`，并在 details 中提供现有 operation ID。
- 生产配置更新使用标准实体标签 `If-Match`；若采用从当前十进制 generation 迁移的兼容窗口，必须通过 ADR 和响应头明确标记。过期写入返回 `412 PRECONDITION_FAILED`。
- 创建、删除、电源、备份、Allocation 和 Startup 更新成功受理后返回 `202 Accepted` 与 operation 资源。
- 同步创建只在所有持久事实已提交且没有异步副作用时返回 `201 Created`。

当前控制台命令不创建 operation，也没有可重试保证；客户端不得自动重放。生产实时协议必须单独定义命令 ID、确认、超时和重复投递行为。

## 7. 错误模型

```json
{
  "error": {
    "code": "NODE_OFFLINE",
    "message": "节点当前离线，无法接收新任务",
    "retryable": true,
    "operationId": "7ef65479-3d0a-4b01-97bb-6d4c8dc41577",
    "traceId": "08e6db0d-ae1e-4887-a02b-44166fc19280",
    "details": {}
  }
}
```

当前与目标共用的错误码包括：`AUTH_REQUIRED`、`AUTH_INVALID_CREDENTIALS`、`AUTH_INVALID_RESET_TOKEN`、`BOOTSTRAP_TOKEN_INVALID`、`SETUP_ALREADY_COMPLETE`、`EMAIL_CONFLICT`、`RATE_LIMITED`、`CSRF_FAILED`、`UNSUPPORTED_MEDIA_TYPE`、`VALIDATION_FAILED`、`NOT_FOUND`、`FORBIDDEN`、`NODE_OFFLINE`、`INSUFFICIENT_RESOURCE`、`PACKAGE_INCOMPATIBLE`、`PATH_ESCAPE_BLOCKED`、`BACKUP_INTEGRITY_FAILED`、`RESTORE_LOCKED`、`OPERATION_IN_PROGRESS`、`OPERATION_CONFLICT`、`IDEMPOTENCY_KEY_REUSED`、`PRECONDITION_FAILED`、`PORT_CONFLICT` 和 `INTERNAL_ERROR`。初始化或重置凭据无效统一返回 401；不支持 JSON media type 返回 415；邮箱、已完成 setup 和同步状态冲突返回 409；`OPERATION_IN_PROGRESS` 表示已有互斥异步任务，`PORT_CONFLICT` 专门表示端口 endpoint 冲突。仅目标 Runtime 或 Extension 可能产生的其他错误码在对应实现进入契约后启用。

常用映射为：认证失败 401、授权失败 403、不存在 404、冲突 409、前置条件失败 412、不支持 media type 415、校验失败 422、限流 429、不可用依赖 503。message 不泄露 Secret、宿主路径、内部凭据或完整命令行。

## 8. 生产实时协议目标

控制台和状态流使用 WebSocket。连接前通过 REST 获取短时、单服务器、可吊销 token。事件至少包含 `type`、`sequence`、`serverId`、`timestamp` 和 payload。

客户端重连时提交最后 sequence。服务端能够续传时补发，否则发送 `stream.reset` 并要求客户端重新获取 REST 快照。命令、日志和状态事件分别限流；授权变化或服务器成员移除后立即关闭连接。

该协议当前未实现，REST 控制台快照不得被描述为实时流验证。

## 9. 版本与废弃

兼容字段只追加、不重解释。废弃字段至少保留一个稳定小版本，并在 OpenAPI、响应 `Deprecation`/`Sunset` Header 和发布说明中同步标记。破坏性变化使用新的 API 主路径或经过 ADR 定义的迁移窗口。
