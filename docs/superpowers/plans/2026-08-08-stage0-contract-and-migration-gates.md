# 阶段 0 契约与迁移门禁实现计划

> **For AI Agent Workers:** 优先使用 `subagent-driven-development`（任务可独立分发时）或 `executing-plans`（需要在当前会话顺序执行时）逐项实现；使用复选框记录真实进度，未运行的门禁不得标记完成。

**目标：** 把路线图中仍为人工声明的 OpenAPI、Protobuf 与 PostgreSQL 迁移检查变成可重复的本地/CI 门禁，并让生成制品与源契约发生漂移时明确失败。

**架构：** OpenAPI 3.1 继续作为 HTTP 权威契约，由 Redocly 做结构校验、`openapi-typescript` 生成并检查前端只读类型制品；Agent Protobuf 由 Buf v2 编译、lint、生成 Go 类型，并在 PR 中与目标分支比较 `FILE` 级兼容性；迁移集成测试使用显式测试数据库依次执行 up、约束断言和逆序 down，本地没有数据库时明确跳过，CI 的 PostgreSQL service 必须执行。生产 Store、Redis、Agent Runtime 与真实迁移编排不在本切片中伪装完成。

**技术栈：** OpenAPI 3.1、Redocly CLI、openapi-typescript 7、Buf CLI 1、Protocol Buffers、Go 1.26、pgx v5、PostgreSQL 17、GitHub Actions。

---

## 任务 1：OpenAPI 解析、生成与漂移检查

**文件：**

- Modify: `web/package.json`
- Modify: `web/package-lock.json`
- Create: `web/src/lib/openapi.generated.ts`
- Modify: `.github/workflows/ci.yml`

**完成标准：** `api/openapi/openapi.yaml` 能通过固定版本 Redocly 的 OpenAPI 3.1 结构校验；生成的 TypeScript 类型进入源码；修改契约但不重新生成时 `npm run api:check` 失败。

- [x] 固定 `@redocly/cli` 与 `openapi-typescript` 开发依赖，新增 `api:lint`、`api:generate`、`api:check` 脚本。
- [x] 先生成类型，再临时改变生成制品，确认 `api:check` 红灯；恢复生成制品并确认绿灯。
- [x] 将 `api:lint` 与 `api:check` 加入 Web CI，并运行全量前端测试、typecheck 与 build。

## 任务 2：Protobuf 编译、生成与兼容门禁

**文件：**

- Create: `buf.yaml`
- Create: `buf.gen.yaml`
- Create: `api/proto/gugumanager/agent/v1/agent.pb.go`
- Create: `api/proto/agent-v1.baseline.binpb`
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `.github/workflows/ci.yml`

**完成标准：** `buf build`、`buf lint`、`buf format --diff --exit-code` 和 `buf generate` 可重复执行；生成的 Go 代码通过编译；PR 对目标分支执行 `buf breaking`，删除/改号等不兼容变化会使门禁失败。

- [x] 建立 Buf v2 workspace，module 指向 `api/proto`，采用 `STANDARD` lint 与 `FILE` breaking 规则。
- [x] 固定远程 Go 插件版本并生成 source-relative Go 类型；加入必要的 protobuf runtime 依赖。
- [x] 用临时字段删除验证本地 breaking 测试能够红灯，随后恢复契约与生成制品。
- [x] CI 固定 Buf CLI 版本，运行 format/build/lint/generate 漂移检查；仅在 PR 事件中拉取目标分支并执行 compatibility gate。

## 任务 3：PostgreSQL up/down 与约束集成测试

**文件：**

- Create: `internal/migrations/postgres_integration_test.go`
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `.github/workflows/ci.yml`
- Modify: `deploy/docker-compose.yml`

**完成标准：** 在专用测试数据库中顺序执行 `000001_core.up.sql`、`000002_identity.up.sql`，断言核心表、Identity 列与摘要/状态/唯一性约束，再逆序执行 down 并确认对象移除；失败 SQL 不得被误报为通过。

- [x] 集成测试只接受显式 `GUGU_TEST_DATABASE_URL`，未设置时 `Skip`，不得连接开发者默认数据库。
- [ ] 首次红灯证明当前 CI 没有运行真实迁移；加入 PostgreSQL 17 service 与测试环境变量后转绿。
- [ ] 测试执行 up → 约束断言 → down → 对象移除断言，并用 mutation（放宽一个约束或打乱 down 顺序）证明测试敏感。
- [x] Compose 的全新数据卷按编号初始化两份 up 文件；文档明确已有 volume 不会自动升级，真实生产 migrator 仍是后续任务。

## 任务 4：文档、实现记录与总回归

**文件：**

- Modify: `README.md`
- Modify: `docs/design/11-testing-roadmap.md`
- Modify: `docs/development/LOCAL_DEVELOPMENT.md`
- Modify: `docs/changes/README.md`
- Create: `docs/changes/GM-20260808-009.md`（编号以前序实际记录为准）

**完成标准：** 当前门禁表只列 CI 实际执行项；兼容比较只在有 Git 基线的 PR 中声称启用；本机缺少 Docker/PostgreSQL/Git 时记录边界，不伪造结果。

- [x] 更新本地命令和 CI 状态，保留真实迁移编排、生产 Store、浏览器 E2E、制品扫描等未完成项。
- [x] 运行 `gofmt -l .`、`go test ./... -count=1`、`go vet ./...`、迁移 plan、三份 GameDefinition lint、OpenAPI lint/check、Buf format/build/lint/generate、前端测试/typecheck/build。
- [x] 在可用 PostgreSQL 的 CI/本地环境运行迁移集成测试；若当前环境不可用，GM 必须写明只完成了跳过路径和工作流静态配置，不能声称数据库验证通过。
- [x] 追加唯一 GM 记录，列出版本固定、生成制品、兼容策略、真实输出、数据库与 Git/Docker 验证边界。

## 计划自检

- 契约边界：OpenAPI 生成的是编译期类型，不等同于运行时请求/响应校验；Protobuf 生成代码不等同于 Agent/gRPC 已实现。
- 数据边界：迁移测试验证 SQL 和约束，不代表 Control Plane 已接 PostgreSQL Store。
- 安全边界：PR compatibility gate 不上传 Secret；外部 `$ref` 默认禁用；测试数据库 URL 必须显式提供。
- 可重复性：工具与生成插件固定版本，生成文件进入源码，CI 使用 check/diff 检测漂移。
- Git 边界：当前工作区无 `.git`，因此本地只能验证解析、生成与静态 workflow；`buf breaking`/OpenAPI breaking 的真实目标分支比较只能在 PR CI 验证。
