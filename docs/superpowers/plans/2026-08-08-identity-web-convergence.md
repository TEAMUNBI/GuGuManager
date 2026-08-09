# Identity Web 开发切片收口实现计划

> **For AI Agent Workers:** Prioritize using `subagent-driven-development` (when the environment supports implementation subagents and tasks are mostly independent) or `executing-plans` (when sequential execution in the current session is needed or implementation subagents are unsupported) to implement this plan task by task. Use checkbox (`- [ ]`) syntax to track progress.

**目标:** 验收并加固已存在的 Setup、匿名密码重置、用户与 membership 管理页面，消除 membership 切换时的旧状态竞态，并让权威文档、迁移状态和实现记录与实际代码一致。

**架构:** 保持现有 React `api` 适配器和 Go 内存 Identity 服务边界不变，只在前端状态切换处清除不再属于当前选择的 membership 快照。测试通过 `App` 路由覆盖真实页面组合；文档明确这些能力仍是单进程开发适配器，不扩大为 PostgreSQL、Redis、多副本或实时连接撤销。

**技术栈:** React 19、React Router 7、TypeScript 5.9、Vitest 3、Testing Library、Go 1.26、Markdown。

---

## 文件结构与职责

- `web/src/app/AppIdentity.test.tsx`：覆盖匿名 setup/reset 路由、管理员用户操作、membership 状态切换和角色导航。
- `web/src/pages/UsersPage.tsx`：维护用户目录及当前用户/服务器对应的 membership 快照；选择变化时不得暴露上一选择的操作。
- `README.md`、`DESIGN.md`：顶层能力与非生产边界摘要。
- `docs/design/01-product-scope.md`、`02-user-experience.md`、`04-domain-model.md`、`08-api-contracts.md`、`09-security.md`、`11-testing-roadmap.md`：分别同步产品矩阵、用户流程、迁移、API、安全和路线图的权威状态。
- `docs/development/CONTRIBUTING.md`：修复生产验收章节锚点。
- `docs/changes/README.md`、`docs/changes/GM-20260808-006.md`：追加本切片索引和真实验收记录；不改写历史 GM 文件。

### 任务 1：锁定并修复 membership 选择竞态

**文件:**
- Modify: `web/src/app/AppIdentity.test.tsx`（`application identity routing`）
- Modify: `web/src/pages/UsersPage.tsx`（membership `useEffect` 与 Revoke 操作状态）

**前置条件:** `npm test -- src/app/AppIdentity.test.tsx` 的现有 4 项测试通过。

**完成标准:** 从已有 membership 的用户切换到另一个用户或服务器时，旧 membership 立即从界面移除；新查询完成前不能执行 Revoke；过期异步响应不能覆盖当前选择。

- [x] **Step 1: 写失败测试**

  在 `AppIdentity.test.tsx` 构造两个 `server_owner` 和一台服务器。让第一个用户的 `api.serverMembership` 立即返回，让第二个用户的请求保持 pending；依次选择两个用户后断言 `Revoke` 不再可执行，并断言尚未调用 `api.deleteServerMembership`。

- [x] **Step 2: 验证红灯**

  Run: `npm test -- src/app/AppIdentity.test.tsx`

  Expected: FAIL；当前实现保留第一个用户的 `membership`，pending 期间仍渲染可点击的 `Revoke`。

- [x] **Step 3: 最小实现**

  在 membership effect 启动新请求前执行 `setMembership(null)` 和默认权限复位；Revoke 按钮同时受 `membershipLoading` 约束。保留现有 `active` 清理标志，禁止旧请求回写。

- [x] **Step 4: 验证绿灯与变异保护**

  Run: `npm test -- src/app/AppIdentity.test.tsx`

  Expected: PASS。随后临时移除 `setMembership(null)`，确认同一测试因旧 Revoke 再现而失败，立即恢复实现并再次确认 PASS。

### 任务 2：补齐 Identity Web 行为覆盖

**文件:**
- Modify: `web/src/app/AppIdentity.test.tsx`

**前置条件:** 任务 1 通过。

**完成标准:** 自动测试证明 setup 表单调用 `api.setupAdmin` 后进入登录流程；匿名 reset 表单调用 `api.resetPassword` 并显示旧会话撤销结果；管理员可停用非当前用户并授予/撤销 membership；服主看不到管理员入口。

- [x] **Step 1: 添加现有行为的特征测试**

  使用 `vi.spyOn(api, ...)` 和 `userEvent` 覆盖上述交互，断言 API 参数包含 Bootstrap Token、CSRF、用户 ID、服务器 ID 和权限数组。对单次 reset token 只断言当前 Modal 展示及关闭后不再显示，不把明文写入持久状态。

- [x] **Step 2: 运行定向测试**

  Run: `npm test -- src/app/AppIdentity.test.tsx src/lib/identity.test.ts`

  Expected: 全部通过；若测试暴露真实行为缺口，先新增最小失败用例再修改实现，不通过放宽断言制造假绿。

- [x] **Step 3: 运行前端回归**

  Run: `npm test && npm run typecheck && npm run build`

  Expected: 全部退出码为 0，Vitest 无失败，TypeScript 无诊断，Vite 成功生成 `dist`。

### 任务 3：同步权威文档与实现记录

**文件:**
- Modify: `README.md`
- Modify: `DESIGN.md`
- Modify: `docs/design/01-product-scope.md`
- Modify: `docs/design/02-user-experience.md`
- Modify: `docs/design/04-domain-model.md`
- Modify: `docs/design/08-api-contracts.md`
- Modify: `docs/design/09-security.md`
- Modify: `docs/design/11-testing-roadmap.md`
- Modify: `docs/development/CONTRIBUTING.md`
- Modify: `docs/changes/README.md`
- Create: `docs/changes/GM-20260808-006.md`

**前置条件:** 任务 1、2 的实际验证结果已知。

**完成标准:** 权威文档不再声称 Identity 仅有 API 或没有 Web 页面；默认种子管理员与可创建本地用户的边界不矛盾；`000002_identity` 被准确描述为迁移骨架；生产验收链接有效；GM 记录只写实际执行结果和未验证边界。

- [x] **Step 1: 修正文档事实**

  统一写成：开发 Web 已提供受控 setup、匿名密码重置、用户/角色和 server membership 管理；状态、令牌摘要、会话、授权与审计仍为单进程内存，未接 PostgreSQL/Redis、多副本或实时连接撤销。

- [x] **Step 2: 修正迁移与路线图边界**

  记录 `000002_identity` 已增加 `password_reset_tokens` 和 `server_members.updated_at`，同时明确缺少 setup state、真实 migration up/down、生产 Store 和事务验证。阶段 1A 保留生产持久会话目标；阶段 2 保留持久多用户/membership 与跨副本/实时撤销目标。

- [x] **Step 3: 修复链接并追加 GM 记录**

  把 `#9-生产-mvp-验收` 改为 `#10-生产-mvp-验收`；新增并索引 `GM-20260808-006`，列出代码/文档文件、API/数据库/权限影响、真实命令结果、Docker/PostgreSQL/浏览器 E2E 等未验证范围，以及仓库没有 `.git` 的状态。

- [x] **Step 4: 文档漂移检查**

  Run: `rg -n 'Identity 管理能力当前只提供 REST API|Web 尚无 setup|当前没有用户管理前端|当前没有重置页面|上述 Identity 路由当前没有对应前端页面|#9-生产-mvp-验收' README.md DESIGN.md docs/design docs/development`

  Expected: 无匹配（`rg` 退出码 1 表示预期的零结果）。

- [x] **Step 5: 全量验证**

  Run: `go test ./... -count=1`、`go vet ./...`、`go run ./cmd/migrate -dir migrations plan`、`npm test`、`npm run typecheck`、`npm run build`。

  Expected: 所有可用命令退出码 0；migration plan 明确识别 `000001`、`000002` 两对且未连接数据库/未执行 SQL。

## 计划自检

- Spec Coverage：覆盖 Identity Web 已实现功能、已确认竞态、权威文档漂移、`000002_identity` 状态、失效锚点和 GM 治理要求。
- Placeholder Scan：无 `TODO`、`TBD` 或未定义实现步骤。
- Type Consistency：沿用现有 `User`、`ServerMembership`、`ServerPermission`、`Session` 和 `api` 方法，不新增公共类型或 API 字段。
- Scope Boundary：不引入数据库驱动、Redis、Agent、OCI、WebSocket 或生产 Secret 存储。
- Git Constraint：当前目录没有 `.git`，因此本计划不包含 commit 步骤；验证完成后记录这一限制，不伪造 branch/commit 状态。

## 下游上下文

**[Context Payload]**
**Architecture:** React 页面通过现有 `api` 适配器访问开发内存 Identity；修复只处理选择态隔离，不改变服务端协议。
**Key Interfaces:** `api.setupAdmin`、`api.resetPassword`、`api.users`、`api.updateUser`、`api.serverMembership`、`api.putServerMembership`、`api.deleteServerMembership`。
**Conventions:** 行为变化使用 Vitest/Testing Library 先红后绿；历史 `GM-*` 只追加；验证命令必须记录真实输出。
**Constraints:** 工作区无 Git 元数据、Docker 与 `psql`；Go 使用已定位的 `C:\Users\andi\.cache\codex-go\go1.26.5\go\bin\go.exe`。
**Uncertainties:** 真实浏览器端视觉验收不在本切片自动化；其缺口进入后续 E2E 门禁计划。
**Handoff Files:** `docs/superpowers/plans/2026-08-08-identity-web-convergence.md`、`docs/design/11-testing-roadmap.md`、`web/src/app/AppIdentity.test.tsx`、`web/src/pages/UsersPage.tsx`。
