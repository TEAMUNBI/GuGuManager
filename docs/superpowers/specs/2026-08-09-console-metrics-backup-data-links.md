# 2026-08-09：面板真实数据链路设计（控制台 + 指标 + 备份）

日期：2026-08-09
状态：已获用户认可

## 1. 背景与目标

阶段 1 验收完成后，Web 前端骨架已完整（服务器工作区 8 个 tab、四语言、权限控制），但在真实服务器（Postgres 适配器 + mTLS Agent + Docker 节点）上运行时，多个核心展示位是空数据或模拟数据：

- **控制台**：`Console` 恒返回空数组、`POST /console/commands` 只写审计不下发，前端显示"轮询 / 开发环境"误导文案（`internal/store/postgres_controlplane.go:1330-1370`；Agent 执行器无对应任务类型 `internal/agent/docker_executor.go:82-95`）。
- **指标**：CPU/内存/玩家数为 0/空（`scanServer` 只回填 limit 字段 `postgres_entities.go:209-227`）；Agent 从不发送 `MetricsBatch`，控制面收到只打 Debug 日志（`agentrpc/connect.go:85-86`）。开发模式靠 seed 硬编码与波浪公式伪造（`internal/store/seed.go:49-133`）。
- **备份**：生产 `CreateBackup` 只 INSERT 元数据 + 入队 `backup` 任务（`postgres_controlplane.go:1632-1691`），但 Agent 不支持 backup 任务，`CompleteTask` 也不回写 `backups` 行 → 备份卡在 `creating`。开发模式伪造 checksum/size（`internal/store/backups.go:203-205`）。

**目标**：打通这三条真实数据链路，让面板在真实服务器上展示真实日志、真实指标、真实备份，消除所有"开发环境快照/占位"痕迹。

**成功标准**：
- 真实服务器（103.42.182.201，节点 `ad36ac57-…`）上打开 paper-05/06 工作区，控制台显示容器真实 stdout 日志滚动，命令输入真实下发并回显。
- 概览页 CPU/内存/玩家数与容器实际状态一致（5 秒粒度），玩家遥测在 RCON 不可用时降级为"暂无遥测"。
- 备份创建/恢复/删除任务经 Agent 真实执行并收敛到终态，`backups` 行 `ready` 且含 checksum/size/storage_location。

## 2. 总体架构

```
Agent (Docker 节点)                         Control Plane (Postgres)              Web
┌──────────────────────────┐              ┌───────────────────────────┐        ┌──────────┐
│ logTailer: docker logs -f │──LogBatch──▶│ consoleBuffer[serverID]    │◀─轮询──│ 控制台 tab │
│ metricSampler: stats+RCON │─MetricsBatch▶│ metricStore[serverID]     │◀─轮询──│ 概览 tab  │
│ backupExecutor: docker exec│──TaskResult─▶│ backups 表 + tasks 回写   │◀─轮询──│ 备份 tab  │
│ commandRunner: docker exec│◀──console_command 帧──│ SendConsoleCommand │◀─POST──│ 命令输入   │
└──────────────────────────┘              └───────────────────────────┘        └──────────┘
```

原则：
- **协议层尽量复用**：日志走已有 `LogBatch` 帧（`agent.proto:205-210`）、指标走已有 `MetricsBatch`/`ServerMetrics`（`agent.proto:250-268`）、备份走已有 `Task` 的 `backup` arm（`agent.proto:147-174`）。唯一新增帧是命令下发 `ConnectResponse.console_command`。
- **控制面缓冲在内存**：Postgres 适配器为每台服务器维护日志环形缓冲（500 行）与指标状态（当前值 + 60 点历史 ring buffer）。重启丢失可接受（MVP），持久化留待后续阶段。
- **前端保持轮询**：现有轮询逻辑（控制台 1.8s、概览 4s、备份 1s）不变，数据真实后自然呈现。

## 3. 子能力设计

### 3.1 控制台（日志 + 命令）

**日志采集（Agent）**
- 新增 `runtime` 接口方法 `Logs(ctx, containerID, since, follow)`，封装 `docker logs -f`（stderr 并入 stdout 或单独标记）。
- Agent 启动 `logTailer`：对每个 `observed_power == running` 的容器起协程，从当前时间起 follow，按 ≤64 行/批 或 ≤1s 聚合，经 `LogBatch` 帧上报（`server_id`、`first_sequence` 单调递增、`lines`、丢行计数）。
- 容器停止/移除时 tailer 退出；容器重启后 tailer 从新容器重新 follow（起始行序号延续，避免前端 key 冲突）。
- 只在容器生命周期内缓冲；控制面重启后历史丢失（MVP 可接受）。

**命令下发（新增帧）**
- `agent.proto`：`ConnectResponse` 的 `oneof payload` 增加 `ConsoleCommand console_command = 6;`；`message ConsoleCommand { string request_id = 1; string server_id = 2; string command = 3; }`（沿用 `Drain`=4、`CertificateResponse`=5 后的编号）。
- 控制面 `POST /api/v1/servers/{serverID}/console/commands` → `SendConsoleCommand` 校验权限/CSRF/非空/≤512 → 通过已连接 Agent 的双向流下发 `ConsoleCommand` 帧（无连接或节点非 available → `NODE_OFFLINE`）。
- Agent 收到帧：`docker exec <container> <command>`（stdout+stderr 捕获），输出追加为 `stdout` 流经 `LogBatch` 回显；命令本身以 `command` 流回显 `> <command>`。
- 前端零改动：轮询 `GET /console` 即见命令回显与容器日志。

**控制面缓冲**
- Postgres 适配器新增内存字段：`consoleBuf map[string]*consoleBuffer`（互斥锁保护），`Console(serverID)` 返回缓冲快照（含 sequence、timestamp、stream、message），形状与 `domain.ConsoleLine`/前端 `ConsoleLine` 一致。
- 现有 `GET /console` handler 不变。

### 3.2 指标（CPU/内存/玩家）

**采集（Agent）**
- 新增 `runtime` 接口方法 `Stats(ctx, containerID)`（封装 `docker stats --no-stream` 单容器）：返回 CPU%、内存/limit、磁盘（从 bind 卷）等；`ExecInContainer(ctx, containerID, argv)` 执行容器内命令并返回输出。
- Agent 启动 `metricSampler`（5s 间隔）：对每个 running 容器采集 `docker stats` + 玩家数（RCON `list` 命令）；组装 `MetricsBatch` 上报（`server_id`、`observed_generation`、`cpu_percent`、`memory_bytes`、`memory_limit_bytes`、`players_online`、`players_max` 等）。
- **RCON**：provision 时注入 `ENABLE_RCON=true`、`RCON_PORT=25575`、`RCON_PASSWORD=<随机>` 环境变量（并暴露 TCP 25575 端口，container_port=25575、host_port 由节点端口分配）；Agent 通过 `docker inspect` 读取容器 `RCON_PASSWORD`，执行 `docker exec <container> rcon-cli -H 127.0.0.1 -P 25575 -p <pwd> list` 解析 `players online/max`。RCON 不可用（非 Minecraft、命令失败、无玩家项）→ 玩家数降级为空（前端显示"暂无遥测"），CPU/内存仍正常。

**控制面存储与展示**
- Postgres 适配器新增内存字段：`metrics map[string]*metricState`（当前 `domain.ServerMetrics` + 60 点历史 ring buffer）。
- `scanServer` 后合并：`Server()`/`Servers()` 返回的 `metrics` 与 `metricHistory` 用缓冲值（有则用，无则零值/空）。`ObservedGeneration` 与 `metrics.observed_generation` 一致时视为有效。
- 概览页去掉 `developmentSnapshot` 文案，显示真实 CPU/内存/玩家。

### 3.3 备份（真实执行）

**payload 落库（控制面）**
- `CreateBackup`：构造 `BackupTaskPayload`（`backup_id`、`storage_object_key = "backups/<backupID>.tar.gz"`）写入 `server_tasks.checkpoint`（与 provision 同模式，此前 checkpoint 为空导致 Agent 无法执行）。
- `RestoreBackup`/`DeleteBackup`：同样构造对应 payload 写 checkpoint（restore 需服务器 `stopped`，沿用现有栅栏）。
- `claimedTaskToProto`：`backup` 任务归一化为 Task 的 `backup` arm（按 checkpoint 中 action 填 create/restore/delete）。

**Agent 执行（docker exec tar）**
- `backup`：`docker exec <container> tar -czf /tmp/<backupID>.tar.gz -C /data .` → `docker cp` 归档到节点 `<dataRoot>/backups/<backupID>.tar.gz`（Agent 先 `os.MkdirAll`）→ 计算 sha256 + size → `result_json: {checksum, sizeBytes, storageLocation}`。
- `restore`：校验容器 stopped → 清空数据卷（`docker exec <container> sh -c 'rm -rf /data/*'`）→ `docker cp` 归档到容器内 `/tmp` → 解包到 `/data`。
- `delete`：删除节点 `<dataRoot>/backups/<backupID>.tar.gz`。
- `docker_executor.go` 分发增加 `backup` 类型（含三种 action）。

**终态回写（控制面）**
- `CompleteTask`：`task_type == "backup"` 成功时按 result_json 回写 `backups` 行（`status='ready'`、`checksum`、`size_bytes`、`storage_location`、`completed_at`）；`restore` 任务成功时备份状态回 `ready`、`restoring` 结束；`backup-delete` 成功时标记 `deleted`/删除行。失败保持 `failed`/`creating` 供重试。

## 4. 错误处理与降级

| 场景 | 行为 |
|---|---|
| 容器日志流中断/容器重启 | tailer 退出或重连，丢行计数上报，控制面缓冲继续追加 |
| 命令下发时节点离线/无连接 | HTTP 返回 `NODE_OFFLINE`（retryable），前端 toast 提示 |
| 命令执行失败（容器停止/不存在） | 输出经 stdout 回显错误信息，任务不重试 |
| 指标采集单容器失败 | 跳过该容器，不阻塞整批上报 |
| RCON 不可用 | 玩家数降级为空，界面"暂无遥测"，CPU/内存正常 |
| 备份执行失败 | 任务 `failed`（可重试），`backups` 行保持 `creating`/`failed`，控制面不置 `ready` |
| 日志缓冲满 | 丢弃最旧行，`dropped_lines` 累加（前端不展示丢行，仅保证顺序键稳定） |

## 5. 前端改动

- 概览页：移除"开发环境快照"小字，玩家遥测不可用时保留"暂无玩家遥测"降级文案。
- 控制台侧栏：`transportValue` 从"轮询 / 开发环境"改为"实时（Agent 日志流）"文案。
- 其余页面无交互改动；备份列表在数据真实后自然显示 checksum/size/时间。
- `web/src/lib/mock.ts` 同步：mock 的 console/metrics/backups 数据保持形状不变（开发模式继续可用）。

## 6. 测试与验证

**单元/集成测试**
- `internal/runtime`：新增 docker 接口方法（fake 实现可注入）。
- `internal/agent`：`docker_executor` backup/restore/delete 单测（fake runtime 注入 tar/cp 行为）；`logTailer`/`metricSampler` 上报帧形状测试。
- `internal/agentrpc`：`ConsoleCommand` 帧下发测试；`LogBatch`/`MetricsBatch` 接收 → 缓冲落库测试。
- `internal/store`：`Console` 缓冲追加/上限；`Server` metrics 合并；`CompleteTask` backup/restore/delete 回写测试（Postgres 集成测试，`GUGU_TEST_DATABASE_URL`）。
- 前端：mock 形状保持，既有 Vitest 全绿；`npm run typecheck`/`npm run build`。

**真实环境验收（103.42.182.201）**
- 交叉编译部署新 control-plane + agent；agent 重启后重连。
- paper-05/06：容器 stdout 出现在控制台并滚动；发送命令（如 `list`）回显玩家；概览 CPU/内存随容器变化。
- 创建备份 → 任务 succeeded、`backups` 行 ready、节点目录存在归档；restore（先 stop）→ 数据卷恢复；delete → 归档移除。
- 前端 build 后部署，浏览器打开确认真实数据呈现。

## 7. 边界与后续

- 日志/指标历史为控制面内存态，重启丢失；持久化（`server_console_logs`/`server_metrics` 表）与审计留待后续阶段。
- 玩家遥测依赖 RCON（provision 自动开启）；自定义/非 Minecraft 镜像可手动关 RCON，此时玩家数不可用但其余功能正常。
- 备份归档存节点本地磁盘，无异地/对象存储；S3 备份属阶段 4。
- 文件服务（files）维持控制面本机执行现状，不在本次范围；Agent 端文件服务属阶段 2。
- 控制台命令无鉴权分级（发送方已过 `servers.console` 权限与 CSRF），执行目标为容器内 shell 命令，风险与 Panel 类产品一致。
