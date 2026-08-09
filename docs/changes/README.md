# GuGuManager 实现记录

实现记录按 `GM-YYYYMMDD-NNN` 编号，只追加，不改写已发布历史。

- [GM-20260807-001](GM-20260807-001.md)：建立统一设计基线。
- [GM-20260807-002](GM-20260807-002.md)：建立原始集中记录与源码头注释规范。
- [GM-20260807-003](GM-20260807-003.md)：拆分设计并实现开发垂直切片，明确生产边界并完成本轮验证。
- [GM-20260807-004](GM-20260807-004.md)：加固开发 Identity，交付安全文件操作、备份恢复/删除和节点离线契约。
- [GM-20260807-005](GM-20260807-005.md)：收口 Network/Startup 开发适配器、固定 Bundle 派生、generation fencing、端口冲突和 Secret 元数据边界，并记录生产缺口。
- [GM-20260808-006](GM-20260808-006.md)：收口 Identity Web、会话探测、membership 目标隔离、危险操作确认和重置令牌失败语义。
- [GM-20260808-007](GM-20260808-007.md)：以每服务器门控关闭恢复登记与文件系统变更之间的并发窗口。
- [GM-20260808-008](GM-20260808-008.md)：让日志配置真实生效，并补齐开发容器构建与全新数据库卷初始化输入。
- [GM-20260808-009](GM-20260808-009.md)：建立 OpenAPI、Protobuf、PostgreSQL 迁移与依赖扫描的阶段 0 自动门禁。
- [GM-20260808-010](GM-20260808-010.md)：拒绝 GameDefinition 文件绑定与安装 Artifact 的路径逃逸、非规范表示、长度超限和重复目标。
- [GM-20260808-011](GM-20260808-011.md)：建立真实 Control Plane Chromium E2E，并声明开发镜像启动、漏洞报告与 CycloneDX SBOM 门禁。
- [GM-20260808-012](GM-20260808-012.md)：收紧 GameDefinition 启动变量可执行子集，阻断 Secret 候选回显和缺少 required 值的启动。
- [GM-20260808-013](GM-20260808-013.md)：复核并修复 Identity 并发、撤权 TOCTOU、请求体契约、membership migration 与 OpenAPI 漂移。
- [GM-20260808-014](GM-20260808-014.md)：统一开发 Operation 的 attempt、lease、checkpoint 与结构化错误契约，并同步 Web 展示和生产边界。
- [GM-20260808-015](GM-20260808-015.md)：为 Operation 固定目标节点快照，并以 node 与 generation 双重栅栏阻止跨节点假成功。
