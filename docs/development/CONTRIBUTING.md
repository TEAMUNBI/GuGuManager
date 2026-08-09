# 开发与贡献流程

## 1. 核心功能

1. 说明用户价值、范围、非目标和验收标准。
2. 涉及模块边界、公共协议、数据模型或安全模型时先新增 ADR。
3. 先更新 OpenAPI、Protobuf、Schema 或领域接口，再写实现。
4. 添加失败测试或契约测试，确认测试因缺少行为而失败。
5. 编写最小实现，补齐单元、集成和错误路径。
6. 运行当前仓库实际提供的格式化、静态检查、测试与构建；未实现的契约、迁移、Runtime 或安全门禁写入未验证范围，不能用计划中的命令代替结果。
7. 创建唯一 `GM-YYYYMMDD-NNN` 实现记录，列出文件、契约影响和真实验证结果。
8. 提交 PR，说明风险、迁移、回滚和未验证范围。

## 2. 游戏包

1. 使用 `gamectl init` 生成能够通过当前 Schema 的 JSON 起始文件，优先声明式能力。模板中的保留域镜像不可直接发布。
2. 只有声明式能力不足时才申请 GameExtension 权限。
3. 使用 `gamectl lint` 执行当前 JSON Schema、单 JSON 值、端口名称、非 `process` 健康检查的 `health.portRef`、变量可执行子集及 file binding/Artifact destination 静态路径校验。
4. 变量 Schema 只能使用文档列出的 string/integer/boolean closed-object 子集；扩展关键字时必须同时更新 canonical Schema、`spec/game-definition` 共享语义校验、Store 防御、Web Mock parity、OpenAPI 和 mutation tests。Secret 不得携带 default、const 或 enum。
5. 人工复核当前 lint 尚未覆盖的模板转义、真实制品摘要/签名、EULA、挂载重叠、权限、网络和生命周期运行语义，并在 PR 中逐项声明未验证内容；file binding 与 Artifact destination 的静态相对路径安全已由 lint 覆盖。
6. 分别声明 `metadata.version` 的定义版本与 `spec.release.version` 的上游游戏版本，并提交来源、许可证、平台、资源建议和已知限制；不得复用已发布的定义版本替换 Bundle 内容或摘要。
7. 完整生命周期一致性工具尚未实现；在其进入仓库前，不得声称仅凭 lint 已验证安装、启动、更新、备份或恢复。
8. 在无生产凭据的隔离环境审核和测试后，才可发布签名 Bundle。

## 3. Git 和 PR

- 默认分支 `main`；功能分支 `feat/<scope>-<name>`，修复分支 `fix/<scope>-<name>`。
- 提交使用 Conventional Commits，例如 `feat(agent): add capability handshake`。
- 一个 PR 聚焦一个可审查主题，不夹带无关重构。
- PR 声明 API、数据库、配置、协议、安全、兼容和验证影响。
- PR 只列出实际执行且能够复现的门禁；当前 CI 与目标门禁的差异见 [测试路线图](../design/11-testing-roadmap.md)。
- 采用 DCO；贡献者不需要生产环境或私有凭据。

## 4. 实现记录

每次可独立验收的实现创建 `docs/changes/GM-YYYYMMDD-NNN.md`，包含：

- 准确时间、目标和用户可见行为。
- 新增、修改、移动和删除文件。
- 主要模块、类型、接口和配置变化。
- API、数据库、权限、配置、协议和兼容性影响。
- 实际执行的测试、构建、静态检查及结果。
- 未完成或未验证范围，以及 Git/PR 状态。

记录只追加；旧记录错误通过新更正记录修正。源码文件不维护重复的逐文件变更历史，使用 Git、PR、ADR 和集中实现记录追踪。

## 5. 发布

只有 [生产 MVP 验收](../design/11-testing-roadmap.md#10-生产-mvp-验收) 与目标发布门禁全部落地后，才能执行生产发布流程。届时冻结 Schema 和迁移，运行完整 CI、游戏包一致性和升级/回滚演练，构建 Control Plane、Agent、CLI、Web 和容器制品，记录 SBOM，并发布迁移说明与兼容矩阵。先发布预览版，再发布稳定版，并观察错误、离线、重试和恢复成功率。

## 6. 安全事件

未修复安全问题通过私密渠道报告，不在公开 Issue 暴露细节。维护者确认范围、建立临时修复分支、补回归测试、发布修复与公告，并在事后更新威胁模型和 ADR。
