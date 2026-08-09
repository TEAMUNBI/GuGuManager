# 12 工程与治理

## 1. 目标仓库结构

```text
GuGuManager/
├── cmd/
│   ├── control-plane/       # Control Plane 入口
│   ├── agent/               # Node Agent 入口
│   └── gamectl/             # 游戏包 CLI
├── internal/
│   ├── identity/
│   ├── catalog/
│   ├── server/
│   ├── provisioning/
│   ├── node/
│   ├── files/
│   ├── backup/
│   ├── audit/
│   └── httpapi/
├── api/
│   ├── openapi/
│   └── proto/
├── migrations/
├── web/                     # React 前端
├── sdk/extension/
├── spec/game-definition/
├── tests/conformance/
├── docs/
│   ├── adr/
│   ├── changes/
│   ├── design/
│   └── development/
├── deploy/
├── go.mod
└── DESIGN.md
```

Go 模块使用规范仓库路径 `github.com/gugumanager/gugumanager`。若项目迁移到不同组织，必须在第一次公开发布前通过一次机械变更统一修改，避免发布后破坏导入路径。

## 2. 模块规则

- 核心领域模块不依赖 HTTP Handler、数据库实现或 Docker SDK。
- 外部系统通过 ports/interfaces 隔离，适配器实现放在依赖方向的外侧。
- 稳定类型、日志字段、错误码、迁移和 API 字段使用英文标识符；界面文案可国际化。
- 模块保持单一职责，跨模块行为通过明确接口、事务或事件传递。
- 依赖只在确有价值时引入，锁定版本并接受漏洞与许可证检查。
- 生成契约不得手工修改，源文件和生成命令写入实现记录与 CI。

## 3. 许可建议

- Control Plane、Agent 和官方服务端代码建议使用 AGPL-3.0-or-later。
- OpenAPI、Protobuf、GameDefinition Schema、Extension SDK 和官方游戏定义建议使用 Apache-2.0。
- 社区 GameDefinition 必须声明自身许可证、来源和第三方依赖。

实际添加 LICENSE 前由项目所有者确认版权主体和双重许可边界；设计建议不自动构成法律选择。

## 4. 社区治理

公开仓库至少包含 `README.md`、`CONTRIBUTING.md`、`CODE_OF_CONDUCT.md`、`SECURITY.md`、`GOVERNANCE.md`、`ROADMAP.md` 和确认后的 `LICENSE`。

协议、SDK、许可、安全边界和支持矩阵变化需要 ADR 与版本说明。至少一名代码所有者审核普通变更；协议、安全和迁移变更需要维护者复核。安全漏洞通过私密渠道处理。

## 5. 设计维护

- 一个主题只有一个权威文件，其他文档使用链接，不复制长期维护内容。
- API、Agent、Schema 和迁移以机器契约为准，解释性章节与其同步评审。
- 每次发布检查兼容矩阵、迁移、回滚、安全模型、游戏包测试和未完成项。
- 每次实现使用集中 `GM-*` 记录；源码变更历史由 Git 与 PR 保存。

