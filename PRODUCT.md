# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

- 自托管管理员：在一台控制平面中持续监控和维护多个 Linux 游戏节点与服务器。
- 小型游戏托管团队：分工处理服务器、节点、用户权限、后台任务与审计记录。
- 服主：只运维被授予访问权限的服务器，重点使用电源、控制台、文件、备份、网络与启动配置。
- 游戏包贡献者：维护版本化 GameDefinition，但不因此自动获得面板运行权限。

## Product Purpose

GuGuManager 用一个 Web 控制台统一管理 Linux 节点上的 Dedicated Server。成功意味着用户能快速判断系统是否健康、定位异常对象、执行受控操作，并在后台任务和操作日志中追踪结果。

## Positioning

GuGuManager 通过“模块化控制平面 + 独立 Node Agent + 版本化 GameDefinition”表达游戏差异；服务器绑定明确的定义版本和摘要，不把不同游戏的安装与运行逻辑硬编码进通用面板。

## Operating Context

- 桌面端是高频、长时间使用的主要运维环境，需要高信息密度、稳定位置和可快速扫描的状态。
- 移动端用于查看状态和处理少量紧急操作，导航折叠为抽屉，主要任务不能依赖横向滚动。
- 用户在总览、服务器列表、服务器工作区、节点、游戏模板、用户权限、后台任务和操作日志之间频繁切换。
- 服务器状态、节点条件、期望状态、实际状态和后台任务状态分别建模，界面不得混为一个“状态”。

## Capabilities and Constraints

- 当前仓库提供开发垂直切片（内存/模拟）与已实现的生产适配器（PostgreSQL store + 真实 mTLS gRPC Agent）；开发模式能力不得包装成真实生产执行结果。概览 CPU/实时内存与控制台日志/指标已由 PostgreSQL 持久化（重启可恢复）；生产模式其余已知限制（加密 Secret 静态存储、实时控制台 WebSocket、Outbox/多副本恢复）必须如实呈现。
- 权限默认拒绝。前端隐藏入口不是授权证明，服务端必须重复鉴权。
- 状态改变通过异步 operation 收敛；界面必须区分“请求已受理”和“执行成功”。
- 已有 React 19、TypeScript、Vite、原生 CSS、Lucide 图标以及简体中文、英文、日文、韩文四语言基础设施。
- 重设计必须保留现有功能、路由、动态数据、权限边界和四语言切换。

## Brand Commitments

- 产品名称保持 GuGuManager。
- 当前前端采用 **Liquid Command** 方向，[`design-system/gugumanager/MASTER.md`](design-system/gugumanager/MASTER.md) 是唯一视觉权威；原《06.1_UI设计语言.md》及此前暖色编辑式方向已退役，不再构成设计约束。
- 视觉约束用于服务高频运维任务，不把产品改造成营销站、展示页或低密度杂志排版。
- 文案直接、克制、可操作，不夸大开发适配器或模拟数据。

## Evidence on Hand

- 产品范围：`docs/design/01-product-scope.md`
- 用户体验与页面状态：`docs/design/02-user-experience.md`
- 系统设计索引与实现边界：`DESIGN.md`
- 当前前端视觉系统：`design-system/gugumanager/MASTER.md`
- 可运行的 React 前端、开发 API、测试数据与现有页面。
- 没有真实客户证明、生产指标、计费信息或可用于宣传的商业声明；不得虚构。

## Product Principles

1. 首屏优先回答“现在是否正常、哪里需要处理、下一步做什么”。
2. 熟悉的运维结构优先于装饰性表达，视觉个性来自精确排版、材料感和状态细节。
3. 状态必须可扫描、可解释且不只依赖颜色。
4. 异步操作必须持续暴露目标、阶段、结果与 operation ID。
5. 开发能力与生产能力始终明确区分。

## Accessibility & Inclusion

- 键盘可完成导航、表单、标签页、对话框和控制台输入，焦点在打开与关闭后正确归位。
- 触控目标不小于 44 × 44 CSS 像素；视觉按钮高度可按设计语言保持 36px，但需要通过外部点击区域满足触控要求。
- 状态文字与可访问名称独立表达含义，颜色只作辅助。
- 动效遵守 `prefers-reduced-motion`。
- 四种界面语言切换后布局不得裁切或溢出。
