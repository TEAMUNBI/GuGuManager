# 10 运维与部署

## 1. 支持基线

以下内容是首个生产 MVP 的支持目标，不是当前开发切片的运行承诺：

- Control Plane：Linux amd64/arm64，Go 1.26 构建制品。
- Node Agent：受支持的 Linux LTS，Docker Engine 27+ 或兼容 OCI Runtime。
- PostgreSQL 16 至 18，作为业务与任务事实源。
- Redis 7.2+，仅用于可重建缓存、限流、唤醒和广播。
- S3 兼容对象存储在远程备份里程碑启用。
- Chromium、Firefox 和 Safari 当前两个稳定大版本作为 Web 支持范围。

精确版本矩阵随每个发布制品冻结，升级前先验证数据库扩展、Runtime API 和 Agent 协议兼容性。

## 2. 网络

| 端点 | 默认 | 方向 |
| --- | --- | --- |
| Web/API | 8080（内部） | 反向代理到 Control Plane |
| HTTPS | 443 | 用户到反向代理 |
| Agent gRPC | 8443 | Agent 出站到 Control Plane |
| PostgreSQL | 5432 | 仅 Control Plane 私网 |
| Redis | 6379 | 仅 Control Plane 私网 |

生产 TLS 在受管反向代理或 Control Plane 终止，但浏览器和 Agent 全链路必须加密。数据库、Redis 和 Docker API 不暴露公网。

## 3. 配置

生产目标的配置优先级为命令行、环境变量、配置文件、内置安全默认值。Secret 只从环境、受限文件或 Secret Manager 引用，不写入普通配置文件。

生产目标的稳定配置键：

- `GUGU_ENVIRONMENT`：`development` 或 `production`。
- `GUGU_HTTP_ADDR`、`GUGU_PUBLIC_URL`。
- `GUGU_DATABASE_URL`、`GUGU_REDIS_URL`。
- `GUGU_SESSION_KEY_FILE`、`GUGU_ENCRYPTION_KEY_FILE`。
- `GUGU_AGENT_CA_CERT_FILE`、`GUGU_AGENT_CA_KEY_FILE`。
- `GUGU_BOOTSTRAP_TOKEN_FILE`。
- `GUGU_LOG_LEVEL`、`GUGU_LOG_FORMAT`。
- `GUGU_DEV_DATA_ROOT`、`GUGU_DEV_OPERATION_LATENCY` 仅用于 `development`。

敏感环境变量不会输出到启动日志。生产模式按 [安全门禁](09-security.md#7-生产启动门禁)校验。

当前加载器在 `development` 读取 Web、开发管理员、Agent Token、operation 延迟和开发数据根；Control Plane 启动器会按 `GUGU_LOG_LEVEL`（`debug`、`info`、`warn`、`error`）和 `GUGU_LOG_FORMAT`（`json`、`text`）构造日志处理器。在 `production` 会读取并聚合校验 HTTPS Public URL、PostgreSQL/可选 Redis URL、TLS 终止声明、Session/Encryption Key 与 Agent CA 文件，并拒绝所有 `GUGU_DEV_*` 身份凭据，包括 `GUGU_DEV_BOOTSTRAP_TOKEN`；`Config.Validate()` 与环境加载使用相同的禁止项。全部字段有效后正常启动生产适配器：连接 PostgreSQL 执行迁移、构造 Postgres store，并启用 Agent 的 mTLS gRPC 网关；不再返回 `ErrProductionAdapterUnavailable`。

## 4. 数据库迁移

- 迁移是只追加、单调编号并进入制品；已发布迁移不改写。
- Control Plane 启动前运行独立迁移步骤，不允许多个实例竞态执行破坏性 DDL。
- 展开/收缩迁移跨两个兼容版本完成：先新增兼容结构，再切换写入，最后删除旧结构。
- 每个发布说明包含升级、回滚和不可逆点；迁移前创建并验证数据库备份。

当前仓库的 `000003_membership_permissions` 在 `server_members.permissions text[]` 上追加以下约束：数组必须非空、包含 `servers.read`、没有 `NULL`、没有重复项，并且每一项属于稳定的 13 个 `servers.*` permission key。up migration 会为历史缺少 `servers.read` 的 membership 补齐该权限；若历史数据含未知或重复权限，迁移会失败并回滚，要求显式修复而不是静默丢失授权。down migration 只移除约束和辅助函数，不移除已经补齐的 `servers.read`，因此该数据变化是有意不可逆点。

## 5. 备份和灾备

控制面数据库、加密密钥、Agent CA 与游戏数据分别备份。数据库默认目标 RPO 15 分钟、RTO 4 小时；游戏数据按用户策略声明，不默认为零数据丢失。

恢复演练至少每季度执行一次，包括：新环境恢复数据库、对象存储可读性、密钥解密、Agent 重新连接和一个参考游戏备份恢复。未验证的备份不能显示为“可恢复”。

## 6. 可观测性

- 日志使用结构化 JSON，携带 trace ID、operation ID、actor、resource、node 和安全裁剪后的 error code。
- 指标包含 API 延迟/错误率、任务队列/租约/重试、节点心跳延迟、容器状态、资源使用和备份恢复结果。
- 健康检查拆分 `/healthz`（进程存活）与 `/readyz`。生产模式 `/readyz` 校验数据库连接与迁移、Agent 网关等关键依赖就绪；开发模式只报告内存适配器可用。
- 告警至少覆盖高错误率、任务长期积压、节点批量离线、证书即将过期、磁盘不足和备份连续失败。

## 7. 开发运行

开发垂直切片可以不依赖 Docker、PostgreSQL 和 Redis，以内存存储与模拟节点启动。该模式用于 UI、契约和状态机开发；控制面状态在进程退出时重置，但 `GUGU_DEV_DATA_ROOT` 下的服务器文件保留。动态创建服务器使用随机 ID，重启后对应内存记录消失而目录不自动删除；开发者只能在 Control Plane 停止、确认没有需要保留的数据后人工清理开发根。该目录不能指向生产数据，详见 [ADR-0002](../adr/0002-development-data-lifecycle.md)。

仓库包含开发 Compose（Control Plane + PostgreSQL + Redis）与生产 Compose（`deploy/docker-compose.prod.yml`，含 control-plane、agent、postgres、redis 服务，agent 挂载 Docker Socket 执行真实容器任务）。开发 Compose 提供本地依赖拓扑，但“配置可以解析”不代表真实节点一致性测试已通过。

仓库中的 Compose 文件具有严格的开发边界：

- Control Plane、PostgreSQL 和 Redis 的宿主端口默认只绑定 `127.0.0.1`，不对局域网或公网发布。
- Compose 中的默认管理员密码、Agent Token 和数据库密码只用于本机开发，不是示例生产 Secret。
- 不允许通过修改宿主绑定地址把当前开发 Control Plane 当作共享服务。需要远程评审时，应使用受控隧道并在结束后关闭。
- 生产部署使用已实现的生产适配器（PostgreSQL store 与 mTLS gRPC Agent）；`deploy/docker-compose.prod.yml` 已提供含 agent 服务的示例拓扑，在 TLS、Secret 管理和真实恢复演练就绪后上线，不能沿用开发 Compose。
