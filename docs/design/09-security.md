# 09 安全基线

## 1. 身份和会话

- 首次管理员初始化需要部署侧随机 Bootstrap Token；服务端只保存 SHA-256 摘要，成功后原子消费并关闭初始化入口。
- 密码使用 Argon2id PHC 格式，参数随版本记录；登录响应不区分账号不存在与密码错误。
- 会话令牌至少 256 位随机，只在数据库保存 SHA-256 摘要；登录后轮换，支持过期、撤销和密钥轮换。
- Cookie 使用 `Secure`、`HttpOnly`、`SameSite=Lax`，生产域名和路径最小化。
- 浏览器写请求校验 CSRF；登录、初始化和令牌接口按 IP 与账号维度限流。
- API Token 保存摘要，具有 scope、资源边界、到期时间和最后使用时间，可独立撤销。
- 无邮件部署的本地密码重置令牌使用高熵随机值、只保存 SHA-256 摘要、短 TTL、单次消费且只显示一次；消费后撤销该用户旧会话并记录签发者、目标和结果，管理员不能读取旧口令。

开发登录只能在显式 `development` 配置中启用，响应与界面必须标识环境；生产配置不得回退到默认密码或内存会话。

当前开发适配器已经使用带随机盐和参数记录的 Argon2id PHC 口令，并只把 Session Token 的 SHA-256 摘要放入内存。退出与过期会撤销内存会话，过期会话不能通过 CSRF 校验。登录按规范化账号和直接来源 IP 两个维度执行 5 分钟窗口、5 次失败和 15 分钟阻断，返回 `429` 与 `Retry-After`；reservation 会把 in-flight 尝试计入阈值，避免并发请求在第一轮 Argon2 完成前绕过限流。setup 与 reset 使用独立来源 IP reservation；登录成功只清除账号维度状态，不抹去来源 IP 失败历史。Argon2id 派生在进程内最多同时执行 2 个任务；它不信任未经配置的代理转发头，也不是 Redis 分布式限流。限流表默认最多保留 4096 个维度，达到容量时先清理过期条目，再只淘汰最旧的未阻断且没有 in-flight reservation 的条目。

生产模式（PostgreSQL store）对会话与 CSRF 都只保存不可逆摘要（`csrf_digest`）。由于无法从库中还原 CSRF 明文，Session(token) 恢复时会轮换 CSRF Token、更新数据库摘要并把新明文返回给前端，因此页面刷新后写操作不再被拦截。

1A Identity 开发适配器还实现了 setup、本地用户、密码重置和 membership API，并提供对应的首次 setup、用户与访问管理及匿名密码重置页面。Bootstrap/reset 明文不会持久保存；setup 与 reset 在 Argon2 前和提交前都校验 Token 摘要、TTL 与消费状态。密码重置和用户停用会撤销目标用户全部会话；停用还会撤销未消费 reset token，因此“停用后重新启用”不会使旧 token 复活。setup、签发、消费、用户变更和 membership 变更写入审计。生产模式已把用户、令牌、会话、setup 与 membership 接入 PostgreSQL（只保存摘要）；Redis 协调、分布式限流、全局代理来源策略、密钥轮换、多副本一致性和实时连接撤销仍未完成。

## 2. RBAC

权限使用稳定键，例如 `servers.read`、`servers.power`、`servers.files.read`、`servers.files.write`、`servers.backups.restore`、`nodes.manage` 和 `audit.read`。

平台管理员拥有全局权限；服主权限通过 server membership 约束到具体服务器。当前内存适配器在服务器列表、详情和受保护 REST 路由上重新执行资源授权，并且在所有服务器写操作的副作用、active operation 复用或幂等 replay 前重新读取当前用户和 membership。membership 授予、替换或撤销会在下一次请求立即生效；撤权后的慢 HTTP body 不会在 Store 提交 mutation。生产实现还必须让下载、实时通道和后台任务领取保留资源作用域，并在授权撤销后主动关闭既有实时连接；不得只在前端过滤后返回全量数据。

## 3. Agent 与工作负载

- 一次性注册、mTLS、短期证书、轮换和吊销按 [Agent 章节](05-agent-runtime.md)执行。
- Agent 具有接近宿主 root 的能力，必须使用独立账户、最小主机权限、签名升级和可审计配置。
- 游戏容器禁止 privileged、Docker Socket、任意宿主挂载和不受控 capabilities。
- CPU、内存、PIDs、磁盘和网络限制由 Runtime 实际执行。

## 4. 输入、文件和控制台

- 用户输入、模板变量和命令参数全部经过 Schema 校验；命令使用参数数组。
- 文件 API 使用目录句柄安全解析，阻止 `..`、绝对路径、链接逃逸、设备文件、压缩炸弹和配额绕过。
- 控制台 token 短时、单服务器、可撤销并限流；Origin 与当前 membership 都必须验证。
- 用户可见错误、日志和审计正文不包含 Secret、宿主绝对路径、凭据或完整敏感命令。

开发 `ServerFS` 已阻止绝对路径、任何 `..` 组件、NUL、Windows 设备名、NTFS ADS 风格冒号、符号链接、非常规文件和根目录变更；写入通过同目录临时文件、同步和原子替换完成，读写各有 8 MiB 上限，递归删除会先完整预检再删除。文件写入、建目录、移动和删除会在物理操作完成前持有服务器门控和 Store 写锁，因而与用户停用、角色降级和 membership 撤销互斥。生产模式由 Agent 在容器内执行 `FileOperation`（list/read/write/mkdir/move/remove/下载备份），路径解析同样拒绝越界；磁盘配额、流式上传、压缩包解压和跨平台真实节点测试仍待补齐。

开发备份恢复只比对内存登记的完整 SHA-256 元数据并验证格式，用于测试恢复锁和状态机；每服务器门控会阻止恢复登记期间并发的文件写入、建目录、移动和删除。它没有读取、散列或恢复真实归档内容，门控也不跨进程或副本，不得描述为生产备份完整性或分布式锁验证。生产模式由 Agent 创建/恢复/删除/下载真实归档，Agent 回传并校验 `sha256` 摘要；恢复完整性最终依赖 Agent 执行结果与恢复后健康检查。

## 5. Secret 与供应链

- Secret 使用独立密钥加密，传给 Agent 时使用不透明句柄或单次密封载荷。
- 镜像、安装器、Bundle 和扩展固定摘要并验证签名、来源、许可证与 SBOM。
- 社区包先静态校验，再在无生产凭据、受配额限制的隔离 Runner 中执行一致性测试。
- 依赖和基础镜像定期扫描；高危漏洞有明确升级 SLA 和回滚制品。

当前 Startup 开发适配器由服务器固定 Bundle Schema 决定哪些变量是 Secret，调用方不能把 Secret 降级为普通变量。Bundle lint 与 Store 都拒绝 Secret 的 `default`、`const` 和 `enum`；Store 与 HTTP 边界会再次无条件删除 `value`、`default`、`constValue` 和 `enumValues`，读取时只保留 `hasValue` 等声明状态。包含 Secret 的幂等请求摘要使用每进程随机密钥的 HMAC-SHA256。Secret 明文本身仍保存在进程内存，没有静态加密、持久密钥、轮换、Agent 句柄或密封传输，因此只能用于本机开发演示。

## 6. 审计和威胁模型

首次 setup、用户创建/更新/停用、密码重置签发/消费、membership 变化、创建/删除服务器、电源、文件删除或覆盖、备份恢复、节点注册/吊销和包审核必须写只追加审计。审计包含操作者、动作、目标、结果、时间、客户端上下文和 operation ID。

当前 setup、用户、密码重置、membership、文件写入/建目录/移动/删除、备份创建/恢复/删除以及 Network/Startup 的受理和完成均产生审计；开发模式为进程内记录（退出丢失），生产模式持久化到 PostgreSQL 审计表。文件同步操作尚未关联持久 operation，只追加持久审计的保留与查询策略仍需补齐。

威胁模型至少维护：管理员初始化抢注、会话劫持、CSRF、越权、SSRF、恶意镜像、供应链替换、端口竞态、磁盘耗尽、容器逃逸、链接/压缩包逃逸、备份泄露、Agent 私钥泄露和任务重放。

## 7. 生产启动门禁

生产模式缺少数据库、会话签名密钥、公开 URL、TLS 终止声明、Agent CA 或加密密钥时必须启动失败。默认凭据、开发种子数据、调试错误和宽松 CORS 不允许出现在生产构建配置中。

当前配置加载器会聚合校验生产 URL、PostgreSQL URL、TLS 终止声明和 Secret/Agent CA 文件，并拒绝 `GUGU_DEV_ADMIN_EMAIL`、`GUGU_DEV_ADMIN_PASSWORD`、`GUGU_DEV_AGENT_TOKEN` 与 `GUGU_DEV_BOOTSTRAP_TOKEN`。`Config.Validate()` 与环境加载走同一 production 禁止项校验；字段全部有效后正常启动生产适配器（PostgreSQL store 与 mTLS gRPC Agent），不再返回 `ErrProductionAdapterUnavailable`。
