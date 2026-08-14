# 05 Agent 与 Runtime

## 1. 连接方向

Agent 主动向 Control Plane 建立出站 mTLS 双向 gRPC 流，以适应 NAT 和防火墙。Control Plane 不主动连接节点管理端口，也不要求节点暴露 Docker API。

流中包含：协议协商、心跳、任务投递、确认、进度、检查点、结果、日志、指标和证书轮换。Agent 至少兼容当前与前一个稳定协议版本；未知必需能力导致任务被明确拒绝。

## 2. 注册和证书

1. 管理员创建一次性注册令牌，默认 24 小时过期，显式有效期限制为 1 秒至 7 天；可记录最多 100 个字符的节点名称提示，但当前提示仅用于运维记录，不构成注册身份绑定，也尚未实现来源 CIDR 绑定。
2. Agent 本地生成私钥，发送令牌、CSR、版本和能力摘要。
3. Control Plane 原子消费令牌、创建节点并签发短期客户端证书。
4. Agent 将私钥和证书写入仅服务账户可读的目录，随后只使用 mTLS。
5. 剩余有效期低于 20% 时通过现有连接轮换；节点被吊销后新连接立即拒绝，现有连接在下一次心跳断开，并停止领取新任务。

私钥从不离开节点。注册、轮换、吊销和失败尝试写入审计。

首次注册和后续连接都必须使用部署时预置的 CA 根证书校验服务端，信任根缺失时失败关闭，不允许 TOFU 或 `InsecureSkipVerify`。可选 SHA-256 指纹钉扎只作为正常证书链校验后的附加约束，不能替代信任根。

该注册与证书轮换流程已在生产模式落地：Agent 经 gRPC `Enroll` 注册并消费一次性令牌，`RotateCertificate` 在现有连接上轮换短期证书；节点被吊销后新连接和任务领取立即拒绝，已有连接在下一次心跳断开。

## 3. Proto 互操作约定

- `Task.payload` 的 `payload_json`、`provision`、`power`、`backup` 和 `extension` 同属一个 `oneof`。`payload_json` 仅用于旧协议兼容；新协议必须选择对应的 typed arm，不得发送或回退到 `payload_json`。
- `Task.type` 是兼容字段，使用 typed arm 时必须与 arm 名称一致。接收方必须拒绝类型不匹配、未选择 payload 或同时编码多个 arm 的任务，不得猜测或静默转换。
- `Task.bundle_digest` 表示任务要求的目标 Bundle，`ServerObserved.bundle_digest` 表示 Agent 实际运行的 Bundle。版本字符串在任务身份和运行对账中只用于展示、兼容判断及审计上下文，不能替代摘要作为任务身份或对账依据。
- `certificate_signing_request` 必须是 PEM 编码的 PKCS#10 CSR，且只包含一个 `CERTIFICATE REQUEST` 块。
- `certificate_chain` 必须是 PEM 证书链，顺序固定为叶证书在前、随后为中间证书，不包含根 CA。根 CA 通过独立的 `ca_certificate` PEM trust bundle 传递，可包含一个或多个根证书；注册和轮换使用相同约定。

## 4. 心跳与恢复

- Agent 每 10 秒发送一次心跳，包含时间、版本、容量、压力和运行任务摘要。
- 连续 30 秒没有有效心跳时节点标记 `offline`，但服务器保留最后 observedPower 和 observedAt。
- 重连采用有上限的指数退避与随机抖动；Control Plane 根据 operation ID 补发未完成任务。Agent 以 bbolt 操作日志和 payload digest 去重，同一进程中的并发重投共享广播结果。
- Agent 重启时遗留为 `running` 的记录不能证明外部副作用是否发生，因此安全终结为不可自动重试的 `AGENT_RESTARTED_DURING_OPERATION`；控制面需要重试时必须创建新的 operation，不得自动重执行旧任务。
- Agent 启动后盘点带 GuGuManager 标签的容器、端口和数据目录，先回报差异再接受新破坏性任务。
- 日志与指标通道有界；背压时优先保留任务确认和结果，日志允许产生带计数的丢弃事件。

当前控制台日志与指标由 Agent 通过 `LogBatch`/`MetricsBatch` 帧上报，Control Plane 双写内存缓冲（热读缓存）与 PostgreSQL：日志写入 `console_logs`（每服务器保留最近 500 行），指标 upsert 到 `server_metrics` 并追加 `server_metric_history`（每服务器保留最近 60 点）；控制面启动时 `RestoreTelemetry` 从 DB 恢复，重启后数据不丢失。

## 5. 节点布局

建议的 Linux 默认目录：

```text
/var/lib/gugumanager/
├── agent.db              # operation 去重与本地检查点
├── servers/<server-id>/
│   ├── data/             # 游戏持久数据
│   ├── temp/             # 同文件系统原子写临时区
│   └── runtime/          # 非 Secret 运行元数据
├── backups/              # 本地备份暂存
└── extensions/           # 固定摘要的受限扩展缓存
```

目录归专用服务账户所有。服务器 ID 必须通过受控映射解析，不能直接把用户路径拼接到宿主路径。

## 6. OCI Runtime 约束

- 容器名称和标签由 Agent 生成，至少包含 server ID、operation ID、Bundle 摘要和 managed 标记。
- 禁止 `privileged`、宿主 PID/IPC、Docker Socket 挂载和任意宿主路径。
- 默认非 root、只读根文件系统、丢弃 capabilities，并应用 CPU、内存、PIDs、磁盘和网络限制。
- 数据目录只挂载到定义声明的目标；安装容器与运行容器使用不同权限。
- 端口预留、容器创建和失败补偿有明确检查点；端口绑定失败返回 `PORT_CONFLICT`。
- 删除仅作用于标签与服务器记录均匹配的资源，不按名称通配。

## 7. 文件服务

路径解析必须从已打开的服务器根目录句柄开始，逐段拒绝 `..`、绝对路径、设备路径和越界链接。仅做字符串前缀判断不满足安全要求。

- 写入使用同目录临时文件、fsync 和原子替换。
- 上传限制单文件、总请求、服务器配额和并发数。
- 压缩包先扫描条目数量、展开总大小、压缩比、绝对路径和链接，再解压到隔离目录并原子提交。
- 下载、删除、覆盖、移动和解压均重新授权；删除与解压写入审计。

文件操作已通过 gRPC `FileOperation` 帧在 Agent 上真实执行：list/read/mkdir/move/remove 在容器停止时短暂启动、操作完成后恢复原运行状态；write 使用 `CopyArchiveToContainer`（docker cp 语义），在停止容器上直接工作，不要求容器启动。

## 8. 备份与恢复

备份记录包含格式版本、Bundle 摘要、文件清单摘要、内容摘要、大小和一致性方式。恢复流程为：停服、获取恢复锁、校验摘要、暂存解包、原子切换、启动与健康检查、提交或回滚。

控制面数据库备份与游戏数据备份是两个独立运维对象。生产恢复演练必须同时覆盖二者和对象存储凭据轮换。

备份创建已由 Agent 在容器内打包数据卷并回传 checksum/size/storageLocation，归档保存在节点本地备份目录；备份下载通过 REST `GET /api/v1/servers/{serverID}/backups/{backupID}/download` 或 gRPC `DownloadBackup` 从节点本地归档回传内容。
