# GuGuManager

GuGuManager 是一个面向自托管用户和小型游戏托管团队的游戏服务器控制面。当前仓库提供阶段 0 工程基础、开发垂直切片（内存/模拟），以及已实现的生产适配器（PostgreSQL store + 真实 mTLS gRPC Agent，覆盖建服、电源、网络分配、启动配置、备份创建/恢复/删除/下载、文件操作、控制台命令、节点注册/心跳/离线判定）。项目仍在迭代，尚未达到完整生产发布。

![GuGuManager status](https://img.shields.io/badge/status-development%20slice-e98245)

## 当前边界

以下边界针对默认开发模式（Memory store）；生产模式的差异见后文：

- 开发模式下 Control Plane 的用户、会话、Bootstrap/reset 令牌摘要、membership、服务器、operation、备份元数据和审计使用内存存储，退出后重置；开发服务器文件保存在受限本地数据根，可跨重启保留。
- 动态创建服务器的本地目录不会随内存状态重置自动删除，可能成为开发孤儿目录；不要把开发数据根指向生产数据，清理规则见 [ADR-0002](docs/adr/0002-development-data-lifecycle.md)。
- 开发模式下节点、指标、控制台和电源收敛由内存适配器模拟，不启动真实游戏容器；生产模式由真实 Agent 在 Docker 容器上执行。
- Network 只维护内存 Allocation 和模拟 reconcile，不预留或绑定宿主端口；当前单端口模型没有 `portRef`、容器端口或端口角色。
- Startup 从固定 GameDefinition Bundle 读取命令和受限变量 Schema。string/integer/boolean 的 default、const、范围、长度和字符串 enum 在 CLI 与 Store 使用同一语义校验，integer 限于 JavaScript 安全整数域；Start/Restart 会在任何状态变化前拒绝缺少 required 值的服务器。Secret 禁止在 Bundle 声明 default/const/enum，读取时只暴露声明状态和 `hasValue`；含 Secret 的幂等摘要使用进程内随机密钥的 HMAC。Secret 本身仍只保存在进程内存中，没有生产加密存储或 Agent 传输。
- Network/Startup 写入使用 CSRF、`Idempotency-Key` 和十进制 generation `If-Match`；精确 endpoint 冲突返回 `409 PORT_CONFLICT`，过期 generation 返回 `412 PRECONDITION_FAILED`。这些是内存期望状态校验，不代表宿主端口已绑定或 Runtime 已对账。
- Identity 开发 API 已支持首次管理员初始化、本地用户查询/创建/更新/停用、短期单次密码重置，以及服务器 membership 查询/授予/替换/撤销。无效或已消费的 setup/reset 令牌在 Argon2 前被拒绝；登录、setup 和重置使用 reservation 限流，Argon2id 有进程级并发门。密码重置或用户停用会撤销旧内存会话和未消费重置令牌，membership 变更立即影响当前 REST 资源授权。
- Store 写操作会在副作用和幂等回放前重新核验当前角色与 membership；文件写入、建目录、移动和删除与撤权互斥，避免已撤权请求继续修改开发数据根。这仍是单进程内存适配器，不代表生产多副本撤权或实时连接关闭。
- 当前 Web 已提供首次 setup、用户与访问管理、一次性密码重置令牌签发及匿名消费页面。快速启动仍使用已初始化的种子管理员；未初始化的开发 Control Plane 会先进入 setup。Identity 开发适配器未接 PostgreSQL/Redis，不提供持久会话、多副本一致撤销或实时连接撤销。
- `cmd/agent` 提供真实 mTLS gRPC Agent（Enroll/Connect 双向流、任务队列、文件操作、备份下载）；开发 HTTP 心跳仅用于开发模式。
- OpenAPI 已由固定版本 Redocly 校验并生成只读 TypeScript 类型；Agent Protobuf 已由 Buf 编译、lint、生成 Go 类型并建立兼容基线。它们与 HTTP 契约测试、Agent 执行器测试（60+ 文件操作测试）共同构成契约门禁，不替代运行时请求校验。
- PostgreSQL 迁移 000001-000006 齐备（含 membership permission 约束与集成测试；000006 持久化指标与控制台日志），生产 Control Plane 启动时按序执行；开发模式仍使用内存 store。迁移已承载 Allocation 的 primary 标记与 Startup 变量持久化（000005），尚未承载 Allocation 的 portRef 约束或加密 Secret 静态存储。
- `gamectl lint` 当前执行 JSON Schema、端口引用、可执行变量子集及 Secret/required/binding 语义，以及文件绑定与 Artifact destination 的跨平台相对路径安全和重复目标校验。变量对象采用 closed-object 语义；未实现的类型和 JSON Schema 关键字会被拒绝而不是静默忽略。lint 不联网下载制品，也不替代游戏安装、摘要/签名、Runtime、安全审核或生命周期一致性测试。
- `npm run e2e` 使用构建后的 SPA 和同源真实 Go Control Plane，在隔离开发数据根上验证 Chromium 深链、Cookie/CSRF 会话、固定服务器详情与退出；测试要求观察真实 `/api/v1/*` 响应，浏览器 Mock 不能让它假绿。当前没有 Firefox/WebKit、生产 Store 或真实 Agent Runtime 的端到端覆盖。
- CI 工作流已声明浏览器 E2E，以及同一 commit 开发镜像的回环启动、Trivy 漏洞 JSON、CycloneDX SBOM 和可修复 HIGH/CRITICAL 门禁。当前工作区没有 `.git` 和 Docker，本机已真实运行 Go 18 个包（含 Agent 文件操作与 Postgres store 契约测试）、前端 206 个测试和 Chromium E2E；不得把工作流静态配置写成 GitHub Runner 或容器扫描已通过。

完整范围、架构和路线图见 [DESIGN.md](DESIGN.md)，当前与目标端点差异见 [API 契约](docs/design/08-api-contracts.md)。

## 生产模式

`GUGU_ENVIRONMENT=production` 启动 PostgreSQL store（实现 Control Plane 全部契约方法，编译期断言覆盖）与 mTLS gRPC Agent 网关：迁移 000001-000006 在启动前执行，节点任务经数据库任务表投递给真实 Agent。参考 [deploy/docker-compose.prod.yml](deploy/docker-compose.prod.yml)（control-plane + agent + postgres + redis）。指标与控制台日志已持久化：Agent 的 MetricsBatch/LogBatch 帧经 `ApplyServerMetrics`/`RecordConsoleLines` 双写内存缓冲（热读缓存）与 PostgreSQL（`server_metrics`/`server_metric_history`/`console_logs`），概览页 CPU/内存用量由 `server_metrics` 聚合，控制面重启时经 `RestoreTelemetry` 恢复。已知限制：

- 加密 Secret 静态存储、实时控制台 WebSocket、Outbox/多副本恢复仍未实现。

## 快速启动

需要 Go 1.26+、Node.js 24+ 和 npm。

```powershell
cd web
npm install
npm run build
cd ..
go run ./cmd/control-plane
```

打开 [http://127.0.0.1:8080](http://127.0.0.1:8080)，开发账号：

- Email：`admin@gugu.local`
- Password：`gugu-dev-2026`

也可以只运行前端；页面首次探测 Control Plane 不可达时会固定切换到浏览器内开发适配器。一旦本次页面会话已经收到真实 HTTP 响应，后续网络错误会原样显示，不会在 operation 中途切到 Mock。该回退只用于本机 UI 演示，不是生产容错：

```powershell
cd web
npm run dev
```

打开 [http://127.0.0.1:4173](http://127.0.0.1:4173)。

## 验证

```powershell
go test ./...
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
go run ./cmd/gamectl lint spec/game-definition/examples/papermc.json
go run ./cmd/gamectl lint spec/game-definition/examples/factorio.json
go run ./cmd/gamectl lint spec/game-definition/examples/vintagestory.json
go run github.com/bufbuild/buf/cmd/buf@v1.72.0 lint
go run github.com/bufbuild/buf/cmd/buf@v1.72.0 breaking --against api/proto/agent-v1.baseline.binpb

cd web
npm audit --audit-level=high
npm run api:lint
npm run api:check
npm test
npm run typecheck
npm run build
npm run e2e
```

首次运行 E2E 需要先在 `web` 目录执行 `npx playwright install chromium`。测试固定监听 `127.0.0.1:18080`，拒绝复用已有服务，并把临时服务器文件和失败证据写入已忽略的 `web/test-results` 与 `web/playwright-report`；测试结束后应释放端口。

Docker 可用时可从仓库根目录运行开发 Compose：

```powershell
docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml up --build
```

Compose 默认只把 Control Plane、PostgreSQL 和 Redis 发布到宿主 `127.0.0.1`。其中的管理员密码、Agent Token 和数据库密码均为公开的开发默认值，禁止用于局域网、公网或生产部署。

PostgreSQL 的两个 `docker-entrypoint-initdb.d` 脚本只会在全新 `postgres-data` volume 初始化时执行；已有 volume 不会自动升级。生产 Control Plane 在启动前连接 PostgreSQL 执行迁移，不要用重建 volume 代替升级流程。

CI 的 `development-container` job 只验证 development-memory 预览路径，不推送仓库，也不读取部署 Secret。镜像默认进入 production 配置的 fail-closed 路径；job 必须显式选择 development，再从同一 `github.sha` 镜像启动回环容器，检查 `/readyz` 与 SPA 首页，并用固定 Trivy 版本生成报告和 SBOM。基础镜像标签仍不是不可变 digest，后续需要依赖更新机器人和发布证明治理。

## 目录

- `cmd/control-plane`：开发 Control Plane 入口。
- `cmd/agent`：真实 mTLS gRPC Agent（生产模式）与开发 HTTP 心跳入口。
- `cmd/gamectl`：GameDefinition 初始化与 lint CLI。
- `cmd/migrate`：只读迁移配对、版本和摘要清单工具，不执行数据库迁移。
- `internal`：领域、安全路径、存储和 HTTP API。
- `web`：React 管理面板。
- `api`、`spec`、`migrations`：机器契约。
- `docs`：拆分后的设计、ADR、开发流程和实现记录。

## 文档

- [设计索引](DESIGN.md)
- [本地开发](docs/development/LOCAL_DEVELOPMENT.md)
- [贡献流程](docs/development/CONTRIBUTING.md)
- [开发与生产边界 ADR](docs/adr/0001-development-slice.md)
