# GuGuManager 生产级完整平台实施计划

> 按里程碑顺序执行。每项先补失败测试，再实现完整闭环，完成定向与全量验证后独立提交并推送 `origin/main`，禁止强制推送。

## 目标与边界

- PostgreSQL 是生产事实来源，Memory Store 仅用于开发和单元测试。
- Redis Streams 承担跨副本事件和 Console 广播，生产环境不依赖进程内 Hub 保证一致性。
- Agent 通过 mTLS gRPC 出站连接 Control Plane，并通过 Docker Runtime 执行任务。
- 所有异步变更使用 operation、`server_tasks`、租约、幂等键和 generation fencing。
- S3 采用兼容接口；本地文件对象存储仅用于开发和契约测试。
- Bundle 使用 Ed25519 信任根和不可变 revision，服务器不跟随 `latest` 漂移。
- 当前工作直接提交到 `main`；保留并整合开始实施前已有的 Secret、WebSocket 和 Backup 工作区改动。

## 进度总览

| # | 里程碑 | 状态 | 提交 |
| ---: | --- | --- | --- |
| 1 | Secret 生命周期闭环 | 本地实现与回归完成，待提交/推送/CI | `feat(security): 完成 Secret 存储、投递与轮换闭环` |
| 2 | 备份状态机、完整性与失败补偿 | 待实施 | `fix(backup): 完善备份终态、空值语义与失败补偿` |
| 3 | 可恢复、多副本实时控制台 | 待实施 | `feat(console): 增加可恢复的多副本实时控制台` |
| 4 | 可靠 Outbox、租约恢复与跨副本一致性 | 待实施 | `feat(tasks): 实现可靠 Outbox 与多副本任务恢复` |
| 5 | Startup、Network 与容器 Reconcile | 待实施 | `feat(runtime): 实现 Startup 与 Network 容器对账` |
| 6 | 签名 Game Bundle 目录与升级回滚 | 待实施 | `feat(catalog): 增加签名 Bundle 发布、升级与回滚` |
| 7 | 对象存储、保留策略与灾备数据链 | 待实施 | `feat(storage): 增加对象存储、保留策略与数据库恢复` |
| 8 | 定时任务、通知、自动放置与资源配额 | 待实施 | `feat(operations): 增加调度、通知、自动放置与配额治理` |
| 9 | 审计、可观测性与安全运维 | 待实施 | `feat(observability): 完善审计、指标、追踪与告警` |
| 10 | 发布供应链、跨环境测试与灾备门禁 | 待实施 | `ci(release): 建立生产发布、供应链与灾备门禁` |

## 1. Secret 生命周期闭环

目标：Secret 在创建、更新、读取和 Agent 投递全链路中不以明文持久化或泄露。

- [x] 新写入使用 `enc:v2:<key-id>:<ciphertext>`，兼容读取 `enc:v1`。
- [x] 支持活动密钥和历史解密密钥组成的 Keyring，并提供 PostgreSQL 后台重加密入口。
- [x] 生产环境缺少密钥、未知 Key ID、错误密钥和损坏密文全部 fail closed。
- [x] API 读取 Secret 仅返回 `hasValue`。
- [x] 使用与 operation/server/node/attempt 绑定、两分钟有效的一次性 Secret Handle。
- [x] Agent 使用 mTLS 身份解析 Handle，数据库仅保存 Handle SHA-256 摘要，成功解析后立即消费。
- [x] Agent checkpoint、任务结果、Outbox、审计和前端状态不保存 Secret 明文。
- [x] README、设计文档、配置示例和轮换说明同步。
- [ ] 在真实 PostgreSQL 与 mTLS Agent 环境中执行一次创建、轮换、过期和错误节点解析集成验收。
- [ ] 推送并确认远程 SHA 与 GitHub CI。

## 2. 备份状态机、完整性与失败补偿

目标：任何成功、失败、取消、租约耗尽或重启路径都收敛到明确且可恢复的备份状态。

- [x] `sizeBytes`、`checksum`、`storageLocation`、`completedAt` 和 `retentionUntil` 使用 nullable 语义。
- [x] 校验 Agent 成功结果中的 backup ID、非负大小、SHA-256 和规范存储位置。
- [x] 创建失败进入 `failed`；恢复/删除失败回到 `ready`；删除成功进入软删除终态。
- [x] 租约耗尽与迟到/重复 Result 使用同一补偿和终态幂等规则。
- [ ] 增加 nullable `failureCode`、`failureMessage` 并在成功重试时清理。
- [ ] 前端显示未知大小、失败原因和可恢复动作。
- [ ] 恢复使用暂存目录、摘要验证和原子切换，不先破坏当前数据目录。
- [ ] 完成状态迁移表驱动测试、真实 PostgreSQL 重启测试和 Docker 备份/恢复/删除集成测试。
- [ ] 推送并确认远程 SHA 与 GitHub CI。

## 3. 可恢复、多副本实时控制台

- [ ] 增加短期、单服务器、单权限、不可复用的 Console Connection Token。
- [ ] 按生产 Public URL 校验 Origin allowlist。
- [ ] 统一 `stream.snapshot/line/reset/error/revoked` 版本化帧协议。
- [ ] 支持客户端携带最后 sequence 重连；保留窗口外发送 reset 和新快照。
- [ ] 使用 Redis Streams 跨 Control Plane 副本广播，并按数据库 console sequence 去重。
- [ ] membership、用户或 Session 撤销时立即关闭对应连接。
- [ ] 客户端实现指数退避、jitter、REST 降级轮询和恢复后自动切回。
- [ ] 增加慢消费者队列上限、丢弃计数和指标。
- [ ] 完成双副本、撤权、重连、reset、malformed frame 和降级轮询测试。
- [ ] 推送并确认远程 SHA 与 GitHub CI。

## 4. 可靠 Outbox、租约恢复与跨副本一致性

- [ ] Outbox 增加稳定事件 UUID、聚合键、事件版本和原始业务时间。
- [ ] 仅在 Redis Streams 发布成功后写入 `published_at`，失败按退避重试。
- [ ] 消费者按事件 UUID 幂等，接受发布成功但数据库标记前崩溃的至少一次投递。
- [ ] 增加失败次数、最后错误、下次重试、死信和管理员重放入口。
- [ ] 租约回收、节点断线、Agent 重连和多副本启动使用数据库锁与 generation fencing。
- [ ] 终态任务不能被迟到 Ack、Progress 或 Result 回退。
- [ ] Session、membership、节点和任务事件进入统一事件总线。
- [ ] 完成 PostgreSQL/Redis 故障注入、并发发布器和重复消费测试。
- [ ] 推送并确认远程 SHA 与 GitHub CI。

## 5. Startup、Network 与容器 Reconcile

- [ ] Protobuf 增加类型化 `ReconcileTaskPayload` 和完整 desired runtime spec。
- [ ] Allocation 增加 `portRef`、`containerPort`、协议和角色，并与 GameDefinition ports 校验。
- [ ] Control Plane 以稳定摘要和 generation 构建不可变期望配置。
- [ ] Agent 实现校验、准备新容器、停止旧容器、切换、健康检查和 observed generation checkpoint。
- [ ] 端口、环境、命令和资源变化执行受控重建；相同摘要重放保持 no-op。
- [ ] 新容器失败时恢复旧容器和旧配置，迟到 generation 被拒绝。
- [ ] maintenance/drain 节点拒绝新建和普通 Reconcile。
- [ ] Docker inspect 验证 Startup/Network 真实生效，并覆盖 provision/reconcile/restart/rollback。
- [ ] 推送并确认远程 SHA 与 GitHub CI。

## 6. 签名 Game Bundle 目录与升级回滚

- [ ] 建立不可变 Bundle revision，服务器固定 revision。
- [ ] `gamectl` 增加 `bundle build/sign/verify/publish`。
- [ ] Ed25519 签名覆盖规范化 Bundle、Artifact manifest、SBOM、许可证和兼容矩阵摘要。
- [ ] Artifact 下载强制 HTTPS allowlist、重定向/大小/私网/展开比例限制和 SHA-256。
- [ ] Catalog 增加草稿、审核、批准、拒绝和撤销状态及审计字段。
- [ ] Agent 安装前复验 Bundle、签名、平台、能力和 Artifact。
- [ ] 增加升级 operation，保存旧 revision 和配置，健康检查失败自动回滚。
- [ ] Web 展示版本、签名、SBOM、许可证、兼容性、审核、升级和回滚。
- [ ] PaperMC、Factorio、Vintage Story 示例通过签名和生命周期测试。
- [ ] 推送并确认远程 SHA 与 GitHub CI。

## 7. 对象存储、保留策略与灾备数据链

- [ ] 建立 ObjectStore 接口、本地开发适配器和 S3 兼容生产适配器。
- [ ] 流式上传内容寻址对象和 manifest；恢复、下载、删除均校验对象身份。
- [ ] 支持保留期限、数量、手动保护、自动过期和孤儿对象回收。
- [ ] 删除使用任务与 Tombstone，避免数据库和对象状态不可恢复分叉。
- [ ] 增加 PostgreSQL `pg_dump`、加密上传、清单和隔离恢复脚本。
- [ ] 恢复前校验迁移版本、摘要、密钥和目标环境，禁止覆盖当前生产数据库。
- [ ] 记录 RPO、RTO、密钥恢复和 S3 生命周期要求。
- [ ] 本地/S3 契约、大文件流式传输、断点失败和隔离恢复演练通过。
- [ ] 推送并确认远程 SHA 与 GitHub CI。

## 8. 定时任务、通知、自动放置与资源配额

- [ ] Schedule 支持 Cron、时区、启停、下次运行、missed-run 和并发策略。
- [ ] 支持定时备份、启动、停止、重启和保留策略清理。
- [ ] PostgreSQL advisory lock 选主，schedule ID + 计划时间保证多副本幂等。
- [ ] Notification/Delivery/Acknowledgement 支持站内通知和签名 Webhook。
- [ ] 失败、离线、备份、容量和安全事件生成去重通知，Webhook 可重试和死信。
- [ ] 放置考虑 maintenance、region、capabilities、CPU、内存、磁盘、端口和服务器上限。
- [ ] 默认 binpack，允许显式节点；资源不足返回可解释原因。
- [ ] 创建、启动、备份、上传和调度执行时服务端并发强制配额。
- [ ] 完成 DST、重启恢复、多副本、Webhook、放置解释和配额并发测试。
- [ ] 推送并确认远程 SHA 与 GitHub CI。

## 9. 审计、可观测性与安全运维

- [ ] 审计 API 支持稳定游标分页和执行人、动作、目标、结果、时间过滤。
- [ ] 定义审计保留、归档、敏感字段清洗和安全事件保护策略。
- [ ] 暴露 HTTP、任务、租约、Outbox、WebSocket、Agent、备份、调度、通知和容量指标。
- [ ] OpenTelemetry 贯通 HTTP、数据库、Outbox、Agent task 和 Webhook。
- [ ] 日志统一 traceId、operationId、serverId、nodeId 并执行 Secret 泄露扫描。
- [ ] `/readyz` 校验迁移、Redis、Keyring 和关键配置。
- [ ] 提供告警规则和积压、离线、重复失败、容量不足、备份过期运行手册。
- [ ] 故障注入、追踪关联和日志/trace/metrics 泄露测试通过。
- [ ] 推送并确认远程 SHA 与 GitHub CI。

## 10. 发布供应链、跨环境测试与灾备门禁

- [ ] CI 强制 Go test/vet/vulncheck、OpenAPI lint/check、Buf lint/breaking、Web test/typecheck/build。
- [ ] 增加真实 PostgreSQL 并发租约/Outbox 和真实 Docker Agent 生命周期测试。
- [ ] Playwright 覆盖 Chromium、Firefox、WebKit 的核心生产流程。
- [ ] 固定基础镜像 digest，生成 CycloneDX SBOM、Trivy 报告、来源证明和签名镜像。
- [ ] 依赖与 Secret 扫描阻断可修复 HIGH/CRITICAL。
- [ ] 版本化迁移执行升级/回滚演练，并保持滚动部署窗口向前兼容。
- [ ] 演练 Control Plane、PostgreSQL、Redis、S3、Agent 证书和 Secret Keyring 灾难恢复。
- [ ] 发布检查表保存真实 CI、恢复演练和制品签名证据。
- [ ] 推送并确认远程 SHA 与 GitHub CI。

## 基础验证集合

```powershell
go test ./...
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
go run github.com/bufbuild/buf/cmd/buf@v1.72.0 lint
go run github.com/bufbuild/buf/cmd/buf@v1.72.0 breaking --against api/proto/agent-v1.baseline.binpb

Set-Location web
npm audit --audit-level=high
npm run api:lint
npm run api:check
npm test
npm run typecheck
npm run build
npm run e2e
```

## 当前已知验证边界

- `go test ./...`、`go vet ./...`、Buf lint/breaking、Web 206 个单元测试、OpenAPI、类型检查、构建和 Chromium E2E 已在本地执行。
- `govulncheck v1.6.0` 当前因旧模块路径 `github.com/docker/docker` 命中 GO-2026-5668、GO-2026-4887、GO-2026-4883。Go 官方漏洞库只在 `github.com/moby/moby/v2` 提供修复版本，Runtime 必须迁移到至少 `v2.0.0-beta.14` 后才能关闭该门禁。
- 本机未配置 `GUGU_TEST_DATABASE_URL`、`GUGU_REDIS_URL` 或 Docker Engine；真实 PostgreSQL、Redis、Docker 和多副本验收尚未执行，不得标记为已通过。
- 当前构建存在既有 Vite chunk 警告：主 JS 约 568.70 kB、CSS 约 320.59 kB，后续发布门禁应完成代码分割和预算治理。
