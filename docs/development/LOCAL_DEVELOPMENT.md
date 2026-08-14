# 本地开发

## 1. 工具链

- Go 1.26 或更高的 1.x 补丁版本。
- Node.js 24+ 与 npm。
- Docker Desktop 可选：默认开发模式不依赖 Docker；调用 `EnableRealRuntime` 后 Memory store 的 provision/power 会使用本地 Docker，运行生产 Compose 或真实 Agent 也需要 Docker。

## 2. 前后端联合运行

终端一：

```powershell
go run ./cmd/control-plane
```

终端二：

```powershell
cd web
npm install
npm run dev
```

访问 `http://127.0.0.1:4173`。Vite 把 `/api` 和 `/healthz` 代理到 `http://127.0.0.1:8080`。

如果页面首次 API 探测即发生网络错误，前端会在该页面会话中固定使用浏览器 Mock；如果已收到 Control Plane 的 HTTP 响应，则后续断网只报告错误，不切换数据源。修改后端可达性后应重新加载页面，不能把同一 operation 跨真实与 Mock 适配器轮询。

开发登录默认值为 `admin@gugu.local` / `gugu-dev-2026`，可以通过 `.env.example` 中的环境变量覆盖。

服务器文件保存在 `GUGU_DEV_DATA_ROOT` 指定的目录，默认是 `var/development/servers`。文件内容会跨 Control Plane 重启保留；服务器、会话、operation、备份元数据和审计仍位于内存，重启后恢复种子状态。动态创建服务器的随机 ID 不会在重启后恢复，因此旧目录会保留但无法从面板访问。当前不自动删除这些目录；只能在 Control Plane 停止、确认目标确实位于专用开发根且不再需要内容后人工清理。不要把该目录指向生产游戏数据，完整决策见 [ADR-0002](../adr/0002-development-data-lifecycle.md)。

## 3. 单进程运行

先构建 Web，Control Plane 会从 `web/dist` 提供 SPA：

```powershell
cd web
npm run build
cd ..
go run ./cmd/control-plane
```

访问 `http://127.0.0.1:8080`。健康端点为 `/healthz` 和 `/readyz`。

## 4. 开发 Agent

Control Plane 运行后，可以发送一次模拟心跳：

```powershell
go run ./cmd/agent --once --node harbor-lab-03 --token gugu-agent-dev-token
```

这会把种子节点恢复为 available。它使用开发 HTTP 端点，不具备生产 mTLS、任务流或容器能力。

开发模式（Memory store）默认是纯模拟：provision/power/backup 等操作 sleep 后置成功，不创建真实容器；调用 `EnableRealRuntime` 后只有 provision/power 走本地 Docker，备份/恢复/删除与网络对账仍是模拟，`DownloadBackup` 返回 `NOT_FOUND`。真实 mTLS gRPC Agent（Enroll/Connect、任务队列、文件操作、备份下载）在 production 模式（PostgreSQL store）下运行，参考 `deploy/docker-compose.prod.yml`。

### 4.1 真实 Agent 首次注册

真实 Agent 不进行 TOFU，也不会在缺少信任根时降级连接。首次启动前必须把 Control Plane 的 `agent-ca.crt` 公钥证书预置到 Agent 主机；不要复制 `agent-ca.key`。生产 Compose 已把同一个 `agent-ca-cert` secret 只读挂载为 Agent 的 `GUGU_AGENT_TRUST_ROOT`。

平台管理员在 Web 的“节点”页签发一次性 enrollment token（默认 24 小时，允许 1 秒至 7 天），然后只把明文交给目标 Agent：

```powershell
$env:GUGU_AGENT_PANEL_ADDR = 'panel.example.com:8443'
$env:GUGU_AGENT_NODE_NAME = 'prod-node-01'
$env:GUGU_AGENT_TRUST_ROOT = 'C:\ProgramData\GuGuManager\agent\ca.crt'
$env:GUGU_AGENT_REGISTRATION_TOKEN = '<one-time-token>'
# 可选；openssl 输出的是证书 DER 指纹，不要用文件字节哈希代替。
$env:GUGU_AGENT_CA_FINGERPRINT = '<openssl-x509-sha256-fingerprint>'
go run ./cmd/agent
```

令牌由数据库原子消费且只保存 SHA-256 摘要；重放、过期或未知令牌都会拒绝。注册成功后应从环境或部署 secret 中清除明文，后续连接使用 Agent 证书。`GUGU_AGENT_REGISTRATION_TOKEN` 仍支持控制面配置的长期静态令牌，供旧部署兼容，但不具备单次消费能力；未配置静态令牌也不会匿名放行。

CA 指纹钉扎是正常证书链校验后的附加约束，不能替代 `GUGU_AGENT_TRUST_ROOT`。轮换 CA 或被钉扎的服务端证书前，必须先把新的信任根/指纹安全分发到节点，否则 Agent 会按失败关闭策略拒绝注册或重连。

## 5. GameDefinition

```powershell
go run ./cmd/gamectl lint spec/game-definition/examples/papermc.json
go run ./cmd/gamectl init --dir .tmp/new-game
```

当前 `gamectl lint` 接受 JSON。`variables.schema` 是可执行的 closed-object 子集，只支持 string/integer/boolean 及已实现的 default、const、字符串 enum、范围和长度约束；integer 限于 JavaScript 安全整数域。Secret property 禁止 default、const 和 enum。lint 会检查 default/const 满足全部约束，但不会联网或执行安装器。Schema 同时是 YAML/JSON 数据模型的机器契约；YAML 解析将在正式 Schema 工具接入时提供。

浏览器 Mock 也会拒绝畸形变量容器、未知关键字、不满足约束的 default/const、Secret material 和不安全 file binding 路径；它消费的却是 JavaScript 已解析对象，无法恢复 JSON 原始数字词法。高精度整数判断和发布兼容性仍以对原始文件执行的 `gamectl lint` 为准，Mock 校验不能替代该门禁。

如果自定义 required 变量没有 default，必须先通过 Startup API 配置值；否则开发适配器会在创建 Start/Restart operation、递增 generation 或改变电源状态之前返回 `VALIDATION_FAILED`。Secret 的 Startup 响应只显示 `hasValue` 状态，不显示值、default、const 或 enum 候选。

## 6. 测试

```powershell
go test ./...
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
go run github.com/bufbuild/buf/cmd/buf@v1.72.0 format --diff --exit-code
go run github.com/bufbuild/buf/cmd/buf@v1.72.0 build
go run github.com/bufbuild/buf/cmd/buf@v1.72.0 lint
go run github.com/bufbuild/buf/cmd/buf@v1.72.0 breaking --against api/proto/agent-v1.baseline.binpb

cd web
npm audit --audit-level=high
npm run api:lint
npm run api:check
npm test
npm run typecheck
npm run build
npx playwright install chromium
npm run e2e
```

`npm run e2e` 会先构建 SPA，再在 `127.0.0.1:18080` 启动单独的 Go Control Plane；`npm run e2e:ui` 使用相同前置构建和服务配置打开交互调试界面。它们使用公开且仅用于测试的开发凭据、`0s` operation latency 和 `web/test-results/run/server-data` 隔离数据根；Chromium 必须观察真实 setup/status、session、login、server detail 与 logout API，浏览器 Mock 不满足验收。失败 trace、截图、视频和 HTML 报告位于 `web/test-results` 与 `web/playwright-report`，两者都不会提交。当前只覆盖 Chromium。

在 Windows 上，如果 `buf format --diff` 报告找不到外部 `diff`，需要把 Git for Windows 的 `usr/bin` 加入当前终端 `PATH`；这不是 Protobuf 格式失败。真实 Docker 节点上的游戏生命周期一致性、PostgreSQL 并发租约、跨副本恢复和游戏包完整生命周期不属于本地开发模式的验证范围。

## 7. 迁移清单与生产门禁

当前迁移命令只校验 up/down 配对、连续版本和内容摘要，不连接数据库，也不执行 SQL：

```powershell
go run ./cmd/migrate -dir migrations plan
```

输出中的 `DRY RUN`、`no database connection was attempted` 和 `no SQL was executed` 是有意边界；`cmd/migrate plan` 仍不是迁移执行器。

迁移 SQL 的 up/down 与基础约束测试使用单独的 PostgreSQL 测试数据库：

```powershell
$env:GUGU_TEST_DATABASE_URL = 'postgres://gugumanager:integration-test-only@127.0.0.1:5432/gugumanager_test?sslmode=disable'
go test -v ./internal/migrations -run '^TestPostgresMigrationsUpAndDown$' -count=1
```

未设置 URL 时该集成测试明确跳过；数据库名没有非空前缀或不以 `_test` 结尾时，会在建立网络连接前拒绝执行。测试只创建并清理本次随机命名的 Schema；即使 migration 在显式事务中失败，也会改用独立连接清理，并由故意失败的回归场景断言 Schema 已消失。CI 使用 PostgreSQL 17 service 执行此路径；生产 Control Plane 启动路径也会在启动前连接 PostgreSQL 执行全部迁移。

开发 Compose 会在全新 `postgres-data` volume 中按编号执行 `000001_core` 与 `000002_identity` 的 up 文件。PostgreSQL 官方入口只在空数据目录执行初始化脚本，已有 volume 不会自动升级；不要删除生产数据卷来模拟迁移。生产 Control Plane 启动路径会在启动前连接 PostgreSQL 执行当前全部迁移（000001-000010），也支持通过 `GUGU_MIGRATIONS_DIR` 指定迁移目录。

`GUGU_ENVIRONMENT=production` 会先完整校验 HTTPS Public URL、PostgreSQL URL、TLS 终止声明、Encryption Key 文件和 Agent CA 文件，校验通过后连接 PostgreSQL 执行迁移并启动生产适配器（PostgreSQL store 与 mTLS gRPC Agent），不再返回 `ErrProductionAdapterUnavailable`。

CI 的 `development-container` job 使用 `node:24.18.0-alpine3.23` 构建 Web，构建一次以 `github.sha` 标记的 development preview 镜像，再显式选择 development 并在宿主回环端口检查 `/readyz` 与 SPA 首页。镜像裸启动默认进入 production 配置的 fail-closed 路径。随后固定 Trivy 0.72.0 生成漏洞 JSON 与 CycloneDX SBOM，并阻断存在修复版本的 HIGH/CRITICAL 漏洞。工作流不推送镜像、不使用部署 Secret；只有真实 GitHub run 才能证明容器步骤通过，本机没有 Docker 时只能验证 Dockerfile 与工作流静态配置。
