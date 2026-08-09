# ADR-0001：开发垂直切片与生产边界

- 状态：accepted
- 日期：2026-08-07

## 背景

仓库只有 GoLand 示例和远期设计，完整 Pterodactyl 类平台涉及身份、数据库、Agent、容器、文件、备份和供应链，不适合把未验证骨架包装成生产实现。本机起初也没有 Go 与 Docker。

## 决策

本轮实现阶段 0 工程骨架和阶段 1 的可交互开发切片。Control Plane 使用与未来生产适配器相同的领域接口，但默认采用内存存储和模拟节点；Web 与 API 明确展示 development 环境。补充 OpenAPI、Schema、迁移与 Compose 作为后续生产实现契约。

PostgreSQL 是生产业务和任务事实源，Redis 只做可重建能力。Agent 使用出站 mTLS 双向流；当前 HTTP 模拟心跳不宣称满足该协议。参考游戏包保持声明式，不依赖未实现的 Extension ABI。

## 后果

- 用户可以立即运行和评审完整管理界面、API 形状、状态模型与幂等交互。
- 进程重启会清空内存控制面状态，模拟电源不会启动真实游戏容器；开发文件根按 [ADR-0002](0002-development-data-lifecycle.md) 跨重启保留。
- 上生产前必须完成 Identity 生产实现、PostgreSQL、mTLS/gRPC Agent、OCI Runtime、文件与备份安全测试。
- 生产配置不得自动回退到开发适配器。
