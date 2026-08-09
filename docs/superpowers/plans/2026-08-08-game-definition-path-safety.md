# GameDefinition 目标路径安全门禁实现计划

> **For AI Agent Workers:** 使用 `test-driven-development` 逐项先红后绿；只实现确定性的清单静态路径校验，不让 `gamectl lint` 联网。

**目标：** 阻止 GameDefinition 的安装 Artifact 与文件变量绑定把内容写到服务器数据根之外或使用跨平台歧义路径，并拒绝多个 Artifact 确定性覆盖同一目标。

**架构：** `gamectl` 在现有 JSON Schema 通过后读取 `variables.bindings[].path` 与 `install.artifacts[].destination`，复用 `internal/files.NormalizeRelative` 的跨平台逃逸、NUL 和长度边界，再收紧为清单规范 `/` 表示。路径错误只引用清单字段和值，不解析或输出宿主绝对路径。

**技术栈：** Go 1.26、现有 Draft 2020-12 Schema、`internal/files` 可移植路径规范化、表驱动测试。

## 任务 1：锁定危险路径与重复目标

**文件：**

- Modify: `cmd/gamectl/main_test.go`

**完成标准：** 当前实现对危险 Artifact destination、危险 file binding path 和重复 Artifact destination 的测试先失败；`paper.jar`、`mods/plugin.jar`、`config/server.properties` 保持合法。

- [x] 添加 Artifact 危险路径表：父目录、POSIX 绝对路径、Windows 盘符、UNC、内嵌父目录、NUL、`.`、反斜杠、非规范 `./`/重复分隔符、总长度或分量超限。
- [x] 添加 file binding 同类危险路径测试。
- [x] 添加重复 destination 失败测试和规范相对路径成功测试。
- [x] 运行定向测试并记录当前错误接受这些路径的红灯。

## 任务 2：实现清单路径门禁

**文件：**

- Modify: `cmd/gamectl/main.go`

**完成标准：** 两类路径只接受非空、规范 `/` 相对路径；重复 Artifact destination 被拒绝；现有 init 模板与三份示例继续通过。

- [x] 扩展语义 DTO 读取 binding path 与 Artifact destination。
- [x] 新增清单相对路径 helper，复用 `NormalizeRelative` 并拒绝反斜杠、空清理结果和非规范表示。
- [x] 对 file binding 校验 path，对 Artifact 校验 destination 并按规范结果判重；错误包含精确字段索引。
- [x] 运行定向测试转绿；临时绕过 Artifact 或 binding 校验做 mutation，确认对应测试重新失败后恢复。

## 任务 3：文档、记录与回归

**文件：**

- Modify: `docs/design/06-game-packages.md`
- Modify: `docs/design/11-testing-roadmap.md`
- Modify: `docs/changes/README.md`
- Create: `docs/changes/GM-20260808-010.md`

**完成标准：** 文档只声称静态目标路径安全，不声称已下载 Artifact 或验证真实摘要；修正 argument binding 示例与当前 placeholder 语义的偏差。

- [x] 更新当前 lint 能力、剩余网络/摘要/挂载/签名边界和生产示例 placeholder。
- [x] 运行 `go test ./cmd/gamectl ./internal/files -count=1`、全量 Go 测试/vet、三份示例 lint 与文档链接检查。
- [x] 追加 GM 记录，写明 TDD/mutation 证据、API/数据库无影响及未联网边界。

## 非目标

- 不下载 Artifact，不验证内容 SHA-256、重定向、DNS、SSRF 或 `networkAllowlist`。
- 不验证数据挂载重叠、镜像签名、来源、SBOM、生命周期实现或真实安装器。
- 不增加 YAML 支持，不把清单路径解析成宿主路径。
