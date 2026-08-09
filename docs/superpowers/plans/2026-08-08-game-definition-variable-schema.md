# GameDefinition 启动变量契约收敛计划

> **For AI Agent Workers:** 全程使用 `test-driven-development`：每项行为先保留可复现红灯，再实现最小修复并做定向 mutation。`gamectl lint` 必须保持纯静态、离线和确定性。

**目标：** 让 canonical Schema、`gamectl lint`、Control Plane Store、HTTP 响应和 Web Mock 对启动变量使用同一个可执行子集；阻止 Secret 元数据回显，并在必填变量未配置时拒绝 Start/Restart 且不改变状态。

**架构：** `spec/game-definition` 定义 GuGuManager 支持的 closed-object 变量 Schema 子集和跨关键字语义校验。Canonical Draft 2020-12 Schema负责拒绝未知/类型不适用的关键字，共享 Go 校验负责范围一致性、default/const 物化语义、Secret 声明与安全整数域。CLI 与 Store 都调用共享校验；Store、HTTP 和 Mock 在公开视图边界再次无条件擦除 Secret 元数据。

**技术栈：** Go 1.26、Draft 2020-12 JSON Schema、OpenAPI 3.1、React/TypeScript/Vitest。

## 任务 1：用失败测试锁定受支持的变量子集

**文件：**

- Modify: `cmd/gamectl/main_test.go`
- Create: `spec/game-definition/variables_test.go`

**完成标准：** 当前实现会错误接受的 unsupported type/keyword、非法 default/const、矛盾范围、Secret material 和非安全整数全部出现定向红灯；三份内置 Bundle 仍是合法基线。

- [x] 覆盖缺失或不支持的 property type，以及 `pattern`、`format`、composition、数值 enum 等 Store 未实现关键字。
- [x] 覆盖负长度、空/重复 enum、minimum 大于 maximum、minLength 大于 maxLength。
- [x] 覆盖 default/const 类型错误、越界、不在 enum、二者不相等。
- [x] 覆盖非法 property identifier、重复 required、Secret default/const/enum。
- [x] 覆盖超出 JavaScript 安全整数域的 default/const/minimum/maximum。

## 任务 2：实现 canonical Schema 与共享语义校验

**文件：**

- Modify: `spec/game-definition/v1alpha1.schema.json`
- Create: `spec/game-definition/variables.go`
- Modify: `cmd/gamectl/main.go`
- Modify: `internal/store/game_bundles.go`

**完成标准：** 变量只允许 string/integer/boolean 及 Store 已实现关键字；lint 通过意味着 Store 可解码和物化同一份定义；不要求修改三份 Bundle，保持现有版本与 digest 稳定。

- [x] 在 canonical Schema 增加本地 `$defs`，结构性拒绝未知关键字、类型不适用字段、空/重复 enum 与非 identifier key。
- [x] 共享校验器验证 root object/closed-object 语义、范围关系、default/const 与全部约束、Secret material 和安全整数域。
- [x] `gamectl lint` 调用共享校验，并继续验证 required/secret/binding 的跨引用与路径门禁。
- [x] Store 在解析固定 Bundle 时再次调用共享校验，把失败映射为 `PACKAGE_INCOMPATIBLE`，避免绕过 lint 的旧/篡改 Bundle进入运行时。
- [x] mutation：分别放宽 property type、跳过 default/const 或 Secret const 检查，确认测试重新变红后恢复。

## 任务 3：关闭 Secret 公开视图泄漏

**文件：**

- Modify: `internal/store/network_startup.go`
- Modify: `internal/store/network_startup_test.go`
- Modify: `internal/httpapi/handler.go`
- Modify: `internal/httpapi/handler_test.go`
- Modify: `api/openapi/openapi.yaml`
- Modify: `web/src/lib/mock.ts`
- Modify: Web Mock/API contract tests

**完成标准：** 即使绕过 lint 或服务层返回防御性恶意数据，Secret 的 `value`、`default`、`constValue`、`enumValues` 都不会出现在公开 JSON；Mock 与真实 API 一致。

- [x] Store 公开视图无条件擦除 Secret 的值与全部候选元数据，只保留声明信息和 `hasValue`。
- [x] HTTP handler 在序列化前执行同样的最后边界擦除；OpenAPI conditional 明确禁止上述字段。
- [x] Web Mock 采用同样规则并加入契约测试。
- [x] mutation：分别恢复 Go/HTTP/Mock 的 `constValue` 输出，确认对应测试失败后恢复。

## 任务 4：必填变量启动前门禁

**文件：**

- Modify: `internal/store/operations.go`
- Modify: `internal/store/operations_test.go`
- Modify: `web/src/lib/mock.ts`
- Modify: Web power-flow tests

**完成标准：** Start/Restart 在创建 operation 和递增 generation 前检查全部 required 变量；缺值返回稳定验证错误，不改变 desired/observed power、generation、operation 或 accepted audit。

- [x] 添加缺失 required 值的 Start 与 Restart 红灯，并断言所有状态保持不变。
- [x] 复用可信固定 Bundle 模板做 preflight，只在错误中列变量 key，不输出任何值。
- [x] Mock 使用同一可观察错误语义。
- [x] mutation：跳过 preflight，确认无状态变化断言重新失败后恢复。

## 任务 5：文档、实现记录与全量回归

**文件：**

- Modify: `README.md`
- Modify: `docs/design/06-game-packages.md`
- Modify: `docs/design/11-testing-roadmap.md`
- Modify: `docs/development/LOCAL_DEVELOPMENT.md`
- Modify: `docs/development/CONTRIBUTING.md`
- Modify: `docs/changes/README.md`
- Create: `docs/changes/GM-20260808-012.md`

**完成标准：** 文档准确描述 GuGuManager 子集、closed-object/default/required/Secret 语义与未支持项；全仓回归和契约漂移门禁新鲜通过。

- [x] 更新变量能力矩阵，明确 `default` 会被物化、required 在启动前门禁、Secret 禁止 default/const/enum。
- [x] 明确不支持 number/array/object、pattern/format/composition，以及变量整数只支持 JavaScript 安全整数域。
- [ ] 运行 Go 定向/全量测试与 vet、三份 Bundle lint、前端 unit/typecheck/build、OpenAPI lint/generate/check、文档 UTF-8/本地链接检查。
- [ ] 追加 GM-20260808-012，记录红—绿—mutation 证据和未改变三份 Bundle digest 的兼容性结论。

## 非目标

- 不实现完整 JSON Schema Draft 2020-12 执行器，不接受远程 `$ref`，不让 lint 联网。
- 不在本切片实现 Artifact 下载、网络 allowlist、签名或真实 Agent 启动。
- 不强制三份既有 Bundle 新增显式 `additionalProperties:false`，从而避免无业务变化的定义版本与 digest 迁移；GuGuManager 在文档中明确把该子集解释为 closed object。
