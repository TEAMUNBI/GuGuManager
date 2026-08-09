# 面板真实数据链路（控制台 + 指标 + 备份）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 打通面板在真实服务器上的控制台日志/命令、服务器指标遥测、备份真实执行三条数据链路，消除开发模拟数据。

**Architecture:** Agent 复用现有 mTLS 双向流：新增容器日志 tailer 与指标采样器，经已有 `LogBatch`/`MetricsBatch` 帧上报；控制面 Postgres 适配器维护每台服务器的内存缓冲（日志环形缓冲 500 行、指标 60 点历史）；命令经新增 `ConsoleCommand` 帧从控制面下发、Agent `docker exec` 执行并回显；备份经任务管线由 Agent `docker exec tar` 真实执行并回写 `backups` 表。前端保持轮询，仅改文案与 mock 同步。

**Tech Stack:** Go 1.26（工具链 `C:\Users\andi\sdk\go1.26.5\bin\go.exe`）、buf（远程插件）、docker/docker client v?、protobuf、React（web）、PostgreSQL。

---

### Task 1: 扩展 agent.proto（ConsoleCommand 帧）并重新生成 pb.go

**Files:**
- Modify: `api/proto/gugumanager/agent/v1/agent.proto`
- Generate: `api/proto/gugumanager/agent/v1/agent.pb.go`, `agent_grpc.pb.go`（由 buf 生成）
- Test: 编译验证

- [ ] **Step 1: 安装 buf**

Run（PowerShell，需要网络）:
```powershell
& 'C:\Users\andi\sdk\go1.26.5\bin\go.exe' install github.com/bufbuild/buf/cmd/buf@v1.53.0
$env:PATH = "$env:USERPROFILE\go\bin;$env:PATH"
buf --version
```
Expected: 输出 `1.53.0` 或更高。

- [ ] **Step 2: 修改 agent.proto 增加 ConsoleCommand 帧**

在 `ConnectResponse` 的 `oneof payload` 末尾（`certificate_response = 5;` 之后）追加 `console_command = 6;`，并新增 message：

```proto
message ConnectResponse {
  oneof payload {
    Welcome welcome = 1;
    Task task = 2;
    RotateCertificate rotate_certificate = 3;
    Drain drain = 4;
    CertificateResponse certificate_response = 5;
    ConsoleCommand console_command = 6;
  }
}

// 控制台下发的单条命令。Agent 收到后执行 docker exec <container> <command>，
// 输出作为 stdout 流经 LogBatch 回显。
message ConsoleCommand {
  string request_id = 1;
  string server_id = 2;
  string command = 3;
}
```

- [ ] **Step 3: 重新生成 pb.go**

Run:
```powershell
$env:PATH = "$env:USERPROFILE\go\bin;$env:PATH"
cd e:\项目\游戏面板\GuGuManager
buf generate
```
Expected: 无输出错误；`agent.pb.go` 中出现 `ConnectResponse_ConsoleCommand` 类型与 `GetConsoleCommand()` 方法。

- [ ] **Step 4: 编译验证**

Run:
```powershell
& 'C:\Users\andi\sdk\go1.26.5\bin\go.exe' build ./api/proto/gugumanager/agent/v1/
```
Expected: 编译成功（exit 0）。

- [ ] **Step 5: 提交**

```bash
git add api/proto/gugumanager/agent/v1/agent.proto api/proto/gugumanager/agent/v1/agent.pb.go api/proto/gugumanager/agent/v1/agent_grpc.pb.go buf.*
git commit -m "feat(proto): add console_command frame for control plane to agent command dispatch"
```

---

### Task 2: runtime 容器能力扩展

**Files:**
- Modify: `internal/runtime/docker.go`
- Test: `internal/runtime/docker_runtime_test.go`（若存在；无则新增辅助类型单测）

- [ ] **Step 1: 写新增能力的最小测试（fake 驱动）**

新建 `internal/runtime/docker_runtime_test.go`（若已存在则追加）：
```go
package runtime

import "testing"

func TestContainerStatsZeroValue(t *testing.T) {
	var stats ContainerStats
	if stats.CPUPercent != 0 || stats.MemoryBytes != 0 || stats.MemoryLimitBytes != 0 {
		t.Fatalf("zero ContainerStats expected, got %+v", stats)
	}
}
```
Run: `& 'C:\Users\andi\sdk\go1.26.5\bin\go.exe' test ./internal/runtime/ -run TestContainerStatsZeroValue -count=1`
Expected: FAIL（`ContainerStats` 未定义）。

- [ ] **Step 2: 实现新增类型与方法**

在 `internal/runtime/docker.go` 追加（import 增加 `bytes`、`encoding/json`、`github.com/docker/docker/api/types/container` 已存在）：

```go
// ContainerStats 是一次 docker stats --no-stream 单容器采样的结果。
type ContainerStats struct {
	CPUPercent      float64
	MemoryBytes     uint64
	MemoryLimitBytes uint64
	NetworkRxBytes  uint64
	NetworkTxBytes  uint64
}

// ContainerStats 采集单个容器的实时资源使用（docker stats --no-stream）。
func (r *DockerRuntime) ContainerStats(ctx context.Context, containerID string) (ContainerStats, error) {
	resp, err := r.client.ContainerStats(ctx, containerID, false)
	if err != nil {
		return ContainerStats{}, fmt.Errorf("container stats: %w", err)
	}
	defer resp.Body.Close()
	var stats struct {
		CPUStats struct {
			CPUUsage struct {
				TotalUsage uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			SystemUsage uint64 `json:"system_cpu_usage"`
			OnlineCPUs  uint32 `json:"online_cpus"`
		} `json:"cpu_stats"`
		PreCPUStats struct {
			CPUUsage struct {
				TotalUsage uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			SystemUsage uint64 `json:"system_cpu_usage"`
		} `json:"precpu_stats"`
		MemoryStats struct {
			Usage uint64 `json:"usage"`
			Limit uint64 `json:"limit"`
		} `json:"memory_stats"`
		Networks map[string]struct {
			RxBytes uint64 `json:"rx_bytes"`
			TxBytes uint64 `json:"tx_bytes"`
		} `json:"networks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return ContainerStats{}, fmt.Errorf("decode container stats: %w", err)
	}
	var out ContainerStats
	out.MemoryBytes = stats.MemoryStats.Usage
	out.MemoryLimitBytes = stats.MemoryStats.Limit
	// CPU% = (Δtotal / Δsystem) * onlineCPUs * 100，首次采样（precpu 为零）返回 0。
	if stats.PreCPUStats.CPUUsage.TotalUsage > 0 && stats.CPUStats.SystemUsage > stats.PreCPUStats.SystemUsage {
		cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
		systemDelta := float64(stats.CPUStats.SystemUsage - stats.PreCPUStats.SystemUsage)
		if systemDelta > 0 && stats.CPUStats.OnlineCPUs > 0 {
			out.CPUPercent = cpuDelta / systemDelta * float64(stats.CPUStats.OnlineCPUs) * 100
		}
	}
	for _, netStats := range stats.Networks {
		out.NetworkRxBytes += netStats.RxBytes
		out.NetworkTxBytes += netStats.TxBytes
	}
	return out, nil
}

// ExecInContainer 在容器内执行命令并返回合并的 stdout/stderr 输出。
func (r *DockerRuntime) ExecInContainer(ctx context.Context, containerID string, argv []string) (string, error) {
	execConfig := container.ExecOptions{
		Cmd:          argv,
		AttachStdout: true,
		AttachStderr: true,
	}
	execID, err := r.client.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		return "", fmt.Errorf("exec create: %w", err)
	}
	attach, err := r.client.ContainerExecAttach(ctx, execID.ID, container.ExecStartOptions{})
	if err != nil {
		return "", fmt.Errorf("exec attach: %w", err)
	}
	defer attach.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, attach.Reader); err != nil {
		return "", fmt.Errorf("exec read: %w", err)
	}
	return buf.String(), nil
}

// FollowLogs 从容器实时输出流式读取日志（docker logs -f --timestamps）。
// 返回的 ReadCloser 需要调用方在结束后 Close；ctx 取消会中断读取。
func (r *DockerRuntime) FollowLogs(ctx context.Context, containerID string) (io.ReadCloser, error) {
	return r.client.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: true,
	})
}

// InspectEnv 返回容器当前环境变量（docker inspect .Config.Env）。
func (r *DockerRuntime) InspectEnv(ctx context.Context, containerID string) (map[string]string, error) {
	inspect, err := r.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("inspect container env: %w", err)
	}
	env := make(map[string]string, len(inspect.Config.Env))
	for _, pair := range inspect.Config.Env {
		for i := 0; i < len(pair); i++ {
			if pair[i] == '=' {
				env[pair[:i]] = pair[i+1:]
				break
			}
		}
	}
	return env, nil
}

// CopyArchiveToContainer 把宿主机上的归档复制进容器目标路径（docker cp 语义）。
func (r *DockerRuntime) CopyArchiveToContainer(ctx context.Context, containerID, hostPath, containerPath string) error {
	file, err := os.Open(hostPath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer file.Close()
	if err := r.client.CopyToContainer(ctx, containerID, containerPath, file, container.CopyToContainerOptions{}); err != nil {
		return fmt.Errorf("copy to container: %w", err)
	}
	return nil
}

// CopyArchiveFromContainer 把容器内路径复制为宿主机归档文件（docker cp 语义）。
func (r *DockerRuntime) CopyArchiveFromContainer(ctx context.Context, containerID, containerPath, hostPath string) error {
	reader, _, err := r.client.CopyFromContainer(ctx, containerID, containerPath)
	if err != nil {
		return fmt.Errorf("copy from container: %w", err)
	}
	defer reader.Close()
	file, err := os.Create(hostPath)
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	defer file.Close()
	if _, err := io.Copy(file, reader); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}
	return nil
}
```
（`os` 需加入 import；`types` import 已有。）

- [ ] **Step 3: 运行测试确认通过**

Run: `& 'C:\Users\andi\sdk\go1.26.5\bin\go.exe' test ./internal/runtime/ -count=1`
Expected: PASS。

- [ ] **Step 4: 提交**

```bash
git add internal/runtime/docker.go internal/runtime/docker_runtime_test.go
git commit -m "feat(runtime): docker stats, exec, follow logs, env inspect, archive copy"
```

---

### Task 3: Postgres 适配器控制台/指标内存缓冲

**Files:**
- Create: `internal/store/console_metrics_buffer.go`
- Modify: `internal/store/postgres_controlplane.go:1330-1370`（Console/SendConsoleCommand 改造）
- Modify: `internal/store/postgres_entities.go`（scanServer metrics 合并）
- Modify: `internal/store/memory.go`（接口方法补齐——Memory 无 RecordConsoleLines/ApplyServerMetrics 时需加空实现以通过编译，若 ControlPlane 接口不含则跳过）
- Test: `internal/store/console_metrics_buffer_test.go`（Postgres 集成测试）

- [ ] **Step 1: 写失败测试**

新建 `internal/store/console_metrics_buffer_test.go`：
```go
package store

import (
	"context"
	"testing"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
)

func TestPostgresConsoleBufferAppendAndLimit(t *testing.T) {
	s := testPostgres(t)
	resetTestDatabase(t, s)
	admin := setupAdminForTest(t, s)
	serverID := createServerFixture(t, s, admin.ID)

	lines := make([]domain.ConsoleLine, 0, 1200)
	for i := 0; i < 1200; i++ {
		lines = append(lines, domain.ConsoleLine{Sequence: int64(i + 1), Timestamp: time.Now().UTC(), Stream: "stdout", Message: "line"})
	}
	if err := s.RecordConsoleLines(context.Background(), serverID, lines); err != nil {
		t.Fatalf("record console lines: %v", err)
	}
	got, err := s.Console(serverID)
	if err != nil {
		t.Fatalf("console: %v", err)
	}
	if len(got) != 500 {
		t.Fatalf("buffer length = %d, want 500", len(got))
	}
	if got[0].Sequence != 701 {
		t.Fatalf("first buffered sequence = %d, want 701", got[0].Sequence)
	}
}

func TestPostgresApplyServerMetricsAndMerge(t *testing.T) {
	s := testPostgres(t)
	resetTestDatabase(t, s)
	admin := setupAdminForTest(t, s)
	serverID := createServerFixture(t, s, admin.ID)

	now := time.Now().UTC()
	if err := s.ApplyServerMetrics(context.Background(), []domain.ServerMetrics{{
		ServerID: serverID, ObservedGeneration: 1, CPUPercent: 42.5, MemoryBytes: 512 << 20,
		MemoryLimitBytes: 1024 << 20, PlayersOnline: 3, PlayersMax: 20, ObservedAt: now,
	}}); err != nil {
		t.Fatalf("apply metrics: %v", err)
	}
	server, err := s.Server(serverID)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	if server.Metrics.CPUPercent != 42.5 {
		t.Fatalf("metrics cpu = %v, want 42.5", server.Metrics.CPUPercent)
	}
	if len(server.MetricHistory) < 1 {
		t.Fatalf("metric history empty, want >=1")
	}
}
```
Run: `& 'C:\Users\andi\sdk\go1.26.5\bin\go.exe' test ./internal/store/ -run 'TestPostgresConsoleBuffer|TestPostgresApplyServerMetrics' -count=1`
Expected: FAIL（`RecordConsoleLines`/`ApplyServerMetrics` 未定义）。

- [ ] **Step 2: 实现缓冲与接口**

新建 `internal/store/console_metrics_buffer.go`：
```go
package store

import (
	"context"
	"sync"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
)

const (
	consoleBufferLimit   = 500
	metricHistoryPoints  = 60
	metricHistoryWindow  = 5 * time.Minute
)

// consoleBuffer 是单台服务器的日志环形缓冲。
type consoleBuffer struct {
	mu    sync.Mutex
	lines []domain.ConsoleLine
	next  int64
}

// metricState 是单台服务器的指标当前值与历史 ring buffer。
type metricState struct {
	mu      sync.Mutex
	current domain.ServerMetrics
	history []domain.ServerMetrics
}

// Postgres 附加字段（在 postgres_controlplane.go 的 Postgres struct 上补）：
// consoleBuffers map[string]*consoleBuffer
// metricStates   map[string]*metricState

func (s *Postgres) consoleBufferFor(serverID string) *consoleBuffer {
	s.bufMu.Lock()
	defer s.bufMu.Unlock()
	buf := s.consoleBuffers[serverID]
	if buf == nil {
		buf = &consoleBuffer{next: 1}
		s.consoleBuffers[serverID] = buf
	}
	return buf
}

func (s *Postgres) metricStateFor(serverID string) *metricState {
	s.bufMu.Lock()
	defer s.bufMu.Unlock()
	state := s.metricStates[serverID]
	if state == nil {
		state = &metricState{}
		s.metricStates[serverID] = state
	}
	return state
}

// RecordConsoleLines 把 Agent 上报的日志行追加进服务器缓冲，超出上限丢弃最旧行。
func (s *Postgres) RecordConsoleLines(ctx context.Context, serverID string, lines []domain.ConsoleLine) error {
	buf := s.consoleBufferFor(serverID)
	buf.mu.Lock()
	defer buf.mu.Unlock()
	for _, line := range lines {
		if line.Sequence <= 0 {
			line.Sequence = buf.next
		}
		buf.next = line.Sequence + 1
		buf.lines = append(buf.lines, line)
	}
	if len(buf.lines) > consoleBufferLimit {
		buf.lines = append([]domain.ConsoleLine(nil), buf.lines[len(buf.lines)-consoleBufferLimit:]...)
	}
	return nil
}

// ApplyServerMetrics 更新服务器指标当前值并追加历史点。
func (s *Postgres) ApplyServerMetrics(ctx context.Context, metrics []domain.ServerMetrics) error {
	for _, m := range metrics {
		if m.ServerID == "" {
			continue
		}
		state := s.metricStateFor(m.ServerID)
		state.mu.Lock()
		if m.ObservedAt.IsZero() {
			m.ObservedAt = time.Now().UTC()
		}
		state.current = m
		cutoff := m.ObservedAt.Add(-metricHistoryWindow)
		kept := state.history[:0]
		for _, point := range state.history {
			if point.ObservedAt.After(cutoff) {
				kept = append(kept, point)
			}
		}
		state.history = append(kept, m)
		if len(state.history) > metricHistoryPoints {
			state.history = state.history[len(state.history)-metricHistoryPoints:]
		}
		state.mu.Unlock()
	}
	return nil
}

// appendMetricsToServer 把内存指标合并到 domain.Server（scanServer 后调用）。
func (s *Postgres) appendMetricsToServer(server *domain.Server) {
	state := s.metricStateFor(server.ID)
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.current.ObservedAt.IsZero() {
		server.Metrics = state.current
	}
	server.MetricHistory = append([]domain.ServerMetrics(nil), state.history...)
}
```

- [ ] **Step 3: 在 Postgres struct 增加字段并在构造时初始化**

`internal/store/postgres_entities.go`（或 store 的 Postgres struct 定义文件）的 `Postgres` struct 增加：
```go
	bufMu          sync.Mutex
	consoleBuffers map[string]*consoleBuffer
	metricStates   map[string]*metricState
```
构造（`NewPostgres` 内，初始化 map）：
```go
	s.consoleBuffers = make(map[string]*consoleBuffer)
	s.metricStates = make(map[string]*metricState)
```

- [ ] **Step 4: 改造 Console 与 sendConsoleCommand**

`internal/store/postgres_controlplane.go`：
```go
// Console returns the buffered console lines of a server.
func (s *Postgres) Console(serverID string) ([]domain.ConsoleLine, error) {
	if _, err := s.Server(serverID); err != nil {
		return nil, err
	}
	buf := s.consoleBufferFor(serverID)
	buf.mu.Lock()
	defer buf.mu.Unlock()
	return append([]domain.ConsoleLine(nil), buf.lines...), nil
}
```

- [ ] **Step 5: scanServer 合并指标**

`internal/store/postgres_entities.go` 中 `scanServer` 返回 `server` 前调用：
```go
	s.appendMetricsToServer(server)
```

- [ ] **Step 6: 运行测试确认通过**

Run: `& 'C:\Users\andi\sdk\go1.26.5\bin\go.exe' test ./internal/store/ -run 'TestPostgresConsoleBuffer|TestPostgresApplyServerMetrics' -count=1`
Expected: PASS。

- [ ] **Step 7: 提交**

```bash
git add internal/store/console_metrics_buffer.go internal/store/postgres_controlplane.go internal/store/postgres_entities.go internal/store/console_metrics_buffer_test.go
git commit -m "feat(store): postgres console ring buffer and server metrics state"
```

---

### Task 4: agentrpc 帧处理（LogBatch/MetricsBatch + 命令下发 + resultJSON）

**Files:**
- Modify: `internal/agentrpc/server.go`（Server 结构、stream registry、TaskStore 接口）
- Modify: `internal/agentrpc/connect.go`（帧处理、claimedTaskToProto backup、SendConsoleCommand dispatch）
- Modify: `internal/store/postgres_tasks.go:141-204`（CompleteTask resultJSON）
- Test: `internal/agentrpc/server_test.go`、`internal/store/postgres_controlplane_test.go`

- [ ] **Step 1: 写失败测试（命令下发 + 帧落库）**

在 `internal/agentrpc/server_test.go` 追加（复用现有 fake 基础设施，先看现有测试的 fake 结构后按模式补）：
```go
func TestSendConsoleCommandDispatchesFrame(t *testing.T) {
	// 用现有 fakeStore + 注入的 fake send registry：注册一个节点流，
	// 调用 server.SendConsoleCommand(nodeID, serverID, "list")，
	// 断言收到的 ConnectResponse 是 ConsoleCommand 且 command == "list"。
}
```
Run: `& 'C:\Users\andi\sdk\go1.26.5\bin\go.exe' test ./internal/agentrpc/ -run TestSendConsoleCommandDispatchesFrame -count=1`
Expected: FAIL。

- [ ] **Step 2: Server 增加流注册表与下发方法**

`internal/agentrpc/server.go`：
```go
// nodeStream 是节点当前连接的发送句柄。
type nodeStream struct {
	nodeID string
	send   func(*agentv1.ConnectResponse) error
}

// Server struct 增加：
	streamMu sync.Mutex
	streams  map[string]*nodeStream

// NewServer 初始化：s.streams = make(map[string]*nodeStream)

// registerStream / unregisterStream 在 Connect 建立/退出时调用。
func (s *Server) registerStream(stream *nodeStream) {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	s.streams[stream.nodeID] = stream
}

func (s *Server) unregisterStream(nodeID string) {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	delete(s.streams, nodeID)
}

// SendConsoleCommand 向节点流下发控制台命令帧；无连接返回 NODE_OFFLINE。
func (s *Server) SendConsoleCommand(nodeID, serverID, command string) error {
	s.streamMu.Lock()
	stream := s.streams[nodeID]
	s.streamMu.Unlock()
	if stream == nil {
		return fmt.Errorf("node %s has no active connect stream", nodeID)
	}
	return stream.send(&agentv1.ConnectResponse{Payload: &agentv1.ConnectResponse_ConsoleCommand{
		ConsoleCommand: &agentv1.ConsoleCommand{RequestId: id.New(), ServerId: serverID, Command: command},
	}})
}
```
（`id` 来自 `github.com/gugumanager/gugumanager/internal/id`，若 server.go 未引入则加入 import；`fmt`、`sync` 同理。）

`TaskStore` 接口同步：
```go
	CompleteTask(ctx context.Context, operationID, nodeID string, succeeded bool, errCode *string, resultJSON []byte) error
	RecordConsoleLines(ctx context.Context, serverID string, lines []domain.ConsoleLine) error
	ApplyServerMetrics(ctx context.Context, metrics []domain.ServerMetrics) error
```

- [ ] **Step 3: connect.go 注册/注销流并处理帧**

`internal/agentrpc/connect.go`：
- 在 `Connect` 内、`send` 闭包定义后：`s.registerStream(&nodeStream{nodeID: nodeID, send: send})`；`defer s.unregisterStream(nodeID)`。
- 帧处理分支替换两处 Debug：
```go
		case *agentv1.ConnectRequest_LogBatch:
			s.handleLogBatch(ctx, nodeID, p.LogBatch)
		case *agentv1.ConnectRequest_MetricsBatch:
			s.handleMetricsBatch(ctx, nodeID, p.MetricsBatch)
```
新增处理函数：
```go
func (s *Server) handleLogBatch(ctx context.Context, nodeID string, batch *agentv1.LogBatch) {
	if batch == nil {
		return
	}
	lines := make([]domain.ConsoleLine, 0, len(batch.GetLines()))
	seq := batch.GetFirstSequence()
	for i, message := range batch.GetLines() {
		lines = append(lines, domain.ConsoleLine{
			Sequence:  seq + int64(i),
			Timestamp: time.Now().UTC(),
			Stream:    "stdout",
			Message:   message,
		})
	}
	if err := s.store.RecordConsoleLines(ctx, batch.GetServerId(), lines); err != nil {
		s.log.Warn("record console lines", "node", nodeID, "server", batch.GetServerId(), "error", err)
	}
}

func (s *Server) handleMetricsBatch(ctx context.Context, nodeID string, batch *agentv1.MetricsBatch) {
	if batch == nil {
		return
	}
	metrics := make([]domain.ServerMetrics, 0, len(batch.GetServers()))
	for _, m := range batch.GetServers() {
		if m == nil {
			continue
		}
		observedAt := time.Now().UTC()
		if ts := m.GetObservedAt(); ts != nil {
			observedAt = ts.AsTime()
		}
		metrics = append(metrics, domain.ServerMetrics{
			ServerID: m.GetServerId(), ObservedGeneration: int64(m.GetObservedGeneration()),
			CPUPercent: m.GetCpuPercent(), MemoryBytes: m.GetMemoryBytes(), MemoryLimitBytes: m.GetMemoryLimitBytes(),
			DiskBytes: m.GetDiskBytes(), DiskLimitBytes: m.GetDiskLimitBytes(),
			NetworkRxBytes: m.GetNetworkRxBytes(), NetworkTxBytes: m.GetNetworkTxBytes(),
			PlayersOnline: int(m.GetPlayersOnline()), PlayersMax: int(m.GetPlayersMax()), ObservedAt: observedAt,
		})
	}
	if err := s.store.ApplyServerMetrics(ctx, metrics); err != nil {
		s.log.Warn("apply server metrics", "node", nodeID, "error", err)
	}
}
```

- [ ] **Step 4: handleTaskResult 传 resultJSON**

`internal/agentrpc/connect.go`：
```go
	if err := s.store.CompleteTask(ctx, result.GetOperationId(), nodeID, result.GetSucceeded(), errCode, result.GetResultJson()); err != nil {
```

- [ ] **Step 5: Postgres.CompleteTask 接受 resultJSON（回写逻辑在 Task 8 补）**

`internal/store/postgres_tasks.go` 签名改为 `(ctx context.Context, operationID, nodeID string, succeeded bool, errCode *string, resultJSON []byte) error`，内部先存 `var resultJSONBytes = resultJSON`（Task 8 使用）。现有调用点（`postgres_*_test.go` 多处 `CompleteTask(..., true, nil)`）追加 `nil` 参数。

- [ ] **Step 6: 运行测试**

Run: `& 'C:\Users\andi\sdk\go1.26.5\bin\go.exe' test ./internal/agentrpc/ ./internal/store/ -count=1`
Expected: PASS（新增命令下发测试与既有测试全绿）。

- [ ] **Step 7: 提交**

```bash
git add internal/agentrpc/server.go internal/agentrpc/connect.go internal/store/postgres_tasks.go internal/agentrpc/server_test.go internal/store/postgres_controlplane_test.go internal/store/postgres_entities_test.go
git commit -m "feat(agentrpc): console command dispatch, log/metrics frame ingest, task result json"
```

---

### Task 5: Agent 端命令执行 + 日志 tailer + 指标采样器

**Files:**
- Modify: `internal/agent/agent.go`（帧处理、goroutine 启动）
- Modify: `internal/agent/docker_executor.go`（ExecuteConsoleCommand）
- Modify: `internal/agent/config.go`（可选：采样间隔配置）
- Test: `internal/agent/agent_test.go`、`internal/agent/docker_runtime_test.go`

- [ ] **Step 1: 写失败测试（命令执行 fake 验证）**

在 `internal/agent/docker_runtime_test.go` 追加（复用现有 fake runtime 模式）：
```go
func TestExecuteConsoleCommandRunsExecInContainer(t *testing.T) {
	exec := NewDockerExecutor(t.TempDir())
	exec.rt = &fakeConsoleRuntime{execOut: "There are 2 of a max of 20 players online"}
	task := &agentv1.Task{
		ServerId: "abc",
		Payload:  &agentv1.Task_Power{Power: &agentv1.PowerTaskPayload{Action: agentv1.PowerAction_POWER_ACTION_START}},
	}
	outcome, err := exec.ExecuteConsoleCommand(context.Background(), task.GetServerId(), "list")
	if err != nil {
		t.Fatalf("execute console command: %v", err)
	}
	if !outcome.Succeeded {
		t.Fatalf("expected success, got %s", outcome.ErrorCode)
	}
	if len(outcome.ResultJSON) == 0 {
		t.Fatalf("expected echoed output in result")
	}
}
```
Run: `& 'C:\Users\andi\sdk\go1.26.5\bin\go.exe' test ./internal/agent/ -run TestExecuteConsoleCommandRunsExecInContainer -count=1`
Expected: FAIL。

- [ ] **Step 2: 扩展 containerRuntime 接口并实现 ExecuteConsoleCommand**

`internal/agent/docker_executor.go` 的 `containerRuntime` 接口增加：
```go
	ExecInContainer(ctx context.Context, containerID string, argv []string) (string, error)
```
新增方法（返回值复用 `ExecutionOutcome`，输出放 `ResultJSON` 的 `{"output": "..."}`）：
```go
// ExecuteConsoleCommand 在服务器容器内执行控制台命令，输出回传控制面。
func (e *DockerExecutor) ExecuteConsoleCommand(ctx context.Context, serverID string, command string) (*ExecutionOutcome, error) {
	rt, err := e.runtime()
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "RUNTIME_UNAVAILABLE", Retryable: true}, nil
	}
	output, err := rt.ExecInContainer(ctx, fmt.Sprintf("gugu-server-%s", serverID), []string{"sh", "-c", command})
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "COMMAND_FAILED", Retryable: false}, nil
	}
	result, err := json.Marshal(map[string]string{"output": output})
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "COMMAND_FAILED", Retryable: false}, nil
	}
	return &ExecutionOutcome{Succeeded: true, ResultJSON: result}, nil
}
```

- [ ] **Step 3: agent 处理 ConsoleCommand 帧并回显输出**

`internal/agent/agent.go` 的 `recvLoop`（收到 `ConnectResponse` 的 switch）增加分支：
```go
		case *agentv1.ConnectResponse_ConsoleCommand:
			a.handleConsoleCommand(ctx, stream, p.ConsoleCommand)
```
新增：
```go
// handleConsoleCommand 执行控制台命令，并把输出作为 stdout 行经 LogBatch 回显。
func (a *agent) handleConsoleCommand(ctx context.Context, stream agentv1.AgentGatewayService_ConnectClient, cmd *agentv1.ConsoleCommand) {
	if cmd == nil {
		return
	}
	outcome, err := a.executor.ExecuteConsoleCommand(ctx, cmd.GetServerId(), cmd.GetCommand())
	if err != nil || outcome == nil {
		outcome = &ExecutionOutcome{Succeeded: false, ErrorCode: "EXECUTION_ERROR", Retryable: false}
	}
	var echo string
	if outcome.Succeeded {
		var payload struct {
			Output string `json:"output"`
		}
		if json.Unmarshal(outcome.ResultJSON, &payload) == nil {
			echo = payload.Output
		}
	}
	if echo == "" {
		echo = outcome.ErrorCode
	}
	if err := stream.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_LogBatch{LogBatch: &agentv1.LogBatch{
		ServerId: cmd.GetServerId(), FirstSequence: a.nextSequence(cmd.GetServerId()), Lines: []string{"> " + cmd.GetCommand(), echo},
	}}}); err != nil {
		a.logger.Warn("send console echo", "error", err)
	}
}

// nextSequence 为每台服务器维护单调递增的日志序号。
func (a *agent) nextSequence(serverID string) int64 {
	a.seqMu.Lock()
	defer a.seqMu.Unlock()
	seq := a.sequences[serverID] + 1
	a.sequences[serverID] = seq + 1
	return seq
}
```
`agent` struct 增加：
```go
	seqMu     sync.Mutex
	sequences map[string]int64
```
（`encoding/json`、`sync` import；构造时初始化 map。）

- [ ] **Step 4: 日志 tailer**

`internal/agent/agent.go` 新增（在 `serveSession` 成功连接并收到 Welcome 后、进入 recvLoop 前启动）：
```go
// startLogTailers 为每台运行中的服务器容器启动日志流式 tailer。
func (a *agent) startLogTailers(ctx context.Context, stream agentv1.AgentGatewayService_ConnectClient) {
	tailers, err := a.executor.ListRunningServers(ctx)
	if err != nil {
		return
	}
	for _, serverID := range tailers {
		a.startLogTailer(ctx, stream, serverID)
	}
}

func (a *agent) startLogTailer(ctx context.Context, stream agentv1.AgentGatewayService_ConnectClient, serverID string) {
	go func() {
		rt, err := a.executor.Runtime()
		if err != nil {
			return
		}
		reader, err := rt.FollowLogs(ctx, fmt.Sprintf("gugu-server-%s", serverID))
		if err != nil {
			return
		}
		defer reader.Close()
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var batch []string
		seq := a.nextSequence(serverID)
		flush := func() {
			if len(batch) == 0 {
				return
			}
			_ = stream.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_LogBatch{LogBatch: &agentv1.LogBatch{
				ServerId: serverID, FirstSequence: seq, Lines: batch,
			}}})
			seq += int64(len(batch))
			batch = batch[:0]
		}
		for scanner.Scan() {
			batch = append(batch, scanner.Text())
			if len(batch) >= 64 {
				flush()
			}
		}
		flush()
	}()
}
```
`TaskExecutor` 接口增加：
```go
	Runtime() (containerRuntime, error)
	ListRunningServers(ctx context.Context) ([]string, error)
```
`DockerExecutor` 实现：
```go
// Runtime 返回当前容器运行时。
func (e *DockerExecutor) Runtime() (containerRuntime, error) { return e.runtime() }

// ListRunningServers 返回所有 gugu-server-* 运行中容器的 serverID。
func (e *DockerExecutor) ListRunningServers(ctx context.Context) ([]string, error) {
	rt, err := e.runtime()
	if err != nil {
		return nil, err
	}
	return rt.ListRunningContainers(ctx, "gugu-server-")
}
```
`containerRuntime` 接口增加 `ListRunningContainers(ctx context.Context, namePrefix string) ([]string, error)`；`runtime.DockerRuntime` 实现（`docker ps --filter name=`）：
```go
// ListRunningContainers 返回名称以 namePrefix 开头的运行中容器 ID。
func (r *DockerRuntime) ListRunningContainers(ctx context.Context, namePrefix string) ([]string, error) {
	containers, err := r.client.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	var out []string
	for _, c := range containers {
		for _, name := range c.Names {
			if strings.HasPrefix(strings.TrimPrefix(name, "/"), namePrefix) {
				out = append(out, strings.TrimPrefix(name, "/")[len(namePrefix):])
				break
			}
		}
	}
	return out, nil
}
```

- [ ] **Step 5: 指标采样器**

`internal/agent/agent.go` 新增：
```go
// startMetricSampler 每 5 秒采集运行中容器的资源与玩家数并上报。
func (a *agent) startMetricSampler(ctx context.Context, stream agentv1.AgentGatewayService_ConnectClient) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			servers, err := a.executor.ListRunningServers(ctx)
			if err != nil {
				continue
			}
			batch := &agentv1.MetricsBatch{ObservedAt: timestamppb.Now()}
			for _, serverID := range servers {
				metrics := a.collectServerMetrics(ctx, serverID)
				if metrics != nil {
					batch.Servers = append(batch.Servers, metrics)
				}
			}
			if len(batch.Servers) > 0 {
				_ = stream.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_MetricsBatch{MetricsBatch: batch}})
			}
		}
	}
}

// collectServerMetrics 采集单台服务器指标：docker stats + RCON 玩家数。
func (a *agent) collectServerMetrics(ctx context.Context, serverID string) *agentv1.ServerMetrics {
	rt, err := a.executor.Runtime()
	if err != nil {
		return nil
	}
	containerName := fmt.Sprintf("gugu-server-%s", serverID)
	stats, err := rt.ContainerStats(ctx, containerName)
	if err != nil {
		return nil
	}
	env, err := rt.InspectEnv(ctx, containerName)
	if err != nil {
		return nil
	}
	m := &agentv1.ServerMetrics{
		ServerId: serverID, ObservedAt: timestamppb.Now(),
		CpuPercent: stats.CPUPercent, MemoryBytes: stats.MemoryBytes, MemoryLimitBytes: stats.MemoryLimitBytes,
		NetworkRxBytes: stats.NetworkRxBytes, NetworkTxBytes: stats.NetworkTxBytes,
	}
	if password := env["RCON_PASSWORD"]; password != "" {
		if output, execErr := rt.ExecInContainer(ctx, containerName, []string{"rcon-cli", "-H", "127.0.0.1", "-P", "25575", "-p", password, "list"}); execErr == nil {
			online, max := parsePlayersFromRCON(output)
			m.PlayersOnline = uint32(online)
			m.PlayersMax = uint32(max)
		}
	}
	return m
}
```
新增 helper（`internal/agent/hoststats.go` 或新文件 `internal/agent/metrics.go`）：
```go
// parsePlayersFromRCON 从 "There are X of a max of Y players online: ..." 提取人数。
func parsePlayersFromRCON(output string) (online, max int) {
	const onlineMarker = "There are "
	idx := strings.Index(output, onlineMarker)
	if idx < 0 {
		return 0, 0
	}
	rest := output[idx+len(onlineMarker):]
	if end := strings.Index(rest, " of a max of "); end > 0 {
		online, _ = strconv.Atoi(rest[:end])
		rest = rest[end+len(" of a max of "):]
	}
	if end := strings.Index(rest, " players online"); end > 0 {
		max, _ = strconv.Atoi(rest[:end])
	}
	return online, max
}
```

- [ ] **Step 6: 在 serveSession 中启动 tailers 与 sampler**

在 `serveSession` 收到 Welcome、进入 recvLoop 前调用：
```go
	go a.startMetricSampler(ctx, stream)
	a.startLogTailers(ctx, stream)
```

- [ ] **Step 7: 运行测试**

Run: `& 'C:\Users\andi\sdk\go1.26.5\bin\go.exe' test ./internal/agent/ ./internal/runtime/ -count=1`
Expected: PASS。

- [ ] **Step 8: 提交**

```bash
git add internal/agent/agent.go internal/agent/docker_executor.go internal/agent/metrics.go internal/agent/agent_test.go internal/agent/docker_runtime_test.go internal/runtime/docker.go
git commit -m "feat(agent): console command exec, log tailer, metric sampler with RCON"
```

---

### Task 6: 备份 payload 落库 + backup 任务归一化

**Files:**
- Modify: `internal/store/postgres_controlplane.go:1632-1691`（CreateBackup）与 RestoreBackup/DeleteBackup（1695 之后）
- Modify: `internal/agentrpc/connect.go`（claimedTaskToProto backup arm）
- Modify: `internal/store/postgres_entities.go`（provision payload 辅助类型同文件的 backup payload 类型）
- Test: `internal/store/postgres_controlplane_test.go`

- [ ] **Step 1: 写失败测试（CreateBackup checkpoint 含 payload）**

在 `internal/store/postgres_controlplane_test.go` 追加（复用现有 backup 测试的建服/权限模式）：
```go
func TestCreateBackupWritesCheckpointPayload(t *testing.T) {
	// 建服后调用 CreateBackup，随后查 server_tasks.checkpoint，
	// 断言 json 含 "backupId" 与 "storageObjectKey": "backups/<backupID>.tar.gz"。
}
```
Run: `& 'C:\Users\andi\sdk\go1.26.5\bin\go.exe' test ./internal/store/ -run TestCreateBackupWritesCheckpointPayload -count=1`
Expected: FAIL（checkpoint 为空）。

- [ ] **Step 2: 实现 backup payload 类型与写入**

`internal/store/postgres_entities.go` 追加（放在 provision 辅助类型附近）：
```go
type backupTaskPayload struct {
	BackupID          string `json:"backupId"`
	StorageObjectKey  string `json:"storageObjectKey"`
	ExpectedManifest  string `json:"expectedManifestDigest,omitempty"`
	ExpectedContent   string `json:"expectedContentDigest,omitempty"`
	DeleteRemoteObject bool   `json:"deleteRemoteObject,omitempty"`
}
```

`CreateBackup` 改造：把 `id.New()` 捕获为 `backupID`，构造 payload 写入 checkpoint。`commitWriteTask` 签名需要支持 payload——查看现有 `commitWriteTask`（provision 用独立 INSERT）；改为复用 provision 的 payload 写入方式：参考 `postgres_entities.go` 中 provision 的 `INSERT INTO server_tasks ... checkpoint` 写法，把 `backupTaskPayload` JSON 传入。

`RestoreBackup`/`DeleteBackup`：同样写 payload（含 backupID、storageObjectKey=备份行已存的 `storage_location`；若为空则 `backups/<backupID>.tar.gz`）。

- [ ] **Step 3: claimedTaskToProto 归一化 backup**

`internal/agentrpc/connect.go` 的 `claimedTaskToProto` 增加分支（在 power 分支之后）：
```go
	if task.TaskType == "backup" || task.TaskType == "restore" || task.TaskType == "backup-delete" {
		payload := &agentv1.BackupTaskPayload{}
		if len(task.PayloadJSON) > 0 {
			_ = protojson.Unmarshal(task.PayloadJSON, payload)
		}
		proto.Type = "backup"
		proto.Payload = &agentv1.Task_Backup{Backup: payload}
		return proto
	}
```
（`protojson` 已在 connect.go 引入或新增 import。）

- [ ] **Step 4: 运行测试**

Run: `& 'C:\Users\andi\sdk\go1.26.5\bin\go.exe' test ./internal/store/ ./internal/agentrpc/ -count=1`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/store/postgres_controlplane.go internal/store/postgres_entities.go internal/agentrpc/connect.go internal/store/postgres_controlplane_test.go
git commit -m "feat(store): backup/restore/delete task checkpoint payload and proto normalization"
```

---

### Task 7: Agent 备份执行器（backup/restore/delete）

**Files:**
- Modify: `internal/agent/docker_executor.go`（ExecuteTask 分发 + executeBackup）
- Modify: `internal/runtime/docker.go`（备份用方法已在前置 Task 提供）
- Test: `internal/agent/docker_runtime_test.go`

- [ ] **Step 1: 写失败测试**

`internal/agent/docker_runtime_test.go` 追加：
```go
func TestExecuteBackupTaskArchivesDataVolume(t *testing.T) {
	dir := t.TempDir()
	exec := NewDockerExecutor(dir)
	exec.rt = &fakeBackupRuntime{}
	payload, _ := protojson.Marshal(&agentv1.BackupTaskPayload{Action: &agentv1.BackupTaskPayload_Create{Create: &agentv1.CreateBackupPayload{BackupId: "b-1", StorageObjectKey: "backups/b-1.tar.gz"}}})
	task := &agentv1.Task{ServerId: "srv-1", Type: "backup", Payload: &agentv1.Task_PayloadJson{PayloadJson: payload}}
	outcome, err := exec.ExecuteTask(context.Background(), task)
	if err != nil || !outcome.Succeeded {
		t.Fatalf("execute backup: outcome=%+v err=%v", outcome, err)
	}
	var result struct {
		Checksum       string `json:"checksum"`
		SizeBytes      int64  `json:"sizeBytes"`
		StorageLocation string `json:"storageLocation"`
	}
	if err := json.Unmarshal(outcome.ResultJSON, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.StorageLocation != "backups/b-1.tar.gz" {
		t.Fatalf("storage location = %q", result.StorageLocation)
	}
}
```
Run: `& 'C:\Users\andi\sdk\go1.26.5\bin\go.exe' test ./internal/agent/ -run TestExecuteBackupTaskArchivesDataVolume -count=1`
Expected: FAIL。

- [ ] **Step 2: ExecuteTask 分发 backup + 实现 executeBackup**

`internal/agent/docker_executor.go`：
```go
	case "backup":
		return e.executeBackup(ctx, task)
```
新增：
```go
// executeBackup 执行备份创建/恢复/删除：容器内 tar 打包数据卷，归档保存到
// 节点本地 <dataRoot>/backups/，回传 checksum/size/storageLocation。
func (e *DockerExecutor) executeBackup(ctx context.Context, task *agentv1.Task) (*ExecutionOutcome, error) {
	payload := task.GetBackup()
	if payload == nil && len(task.GetPayloadJson()) > 0 {
		payload = &agentv1.BackupTaskPayload{}
		if err := protojson.Unmarshal(task.GetPayloadJson(), payload); err != nil {
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED"}, nil
		}
	}
	if payload == nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED"}, nil
	}
	rt, err := e.runtime()
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "RUNTIME_UNAVAILABLE", Retryable: true}, nil
	}
	containerName := fmt.Sprintf("gugu-server-%s", task.GetServerId())
	switch action := payload.GetAction().(type) {
	case *agentv1.BackupTaskPayload_Create:
		create := action.Create
		if create == nil {
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED"}, nil
		}
		return e.createBackup(ctx, rt, task.GetServerId(), containerName, create)
	case *agentv1.BackupTaskPayload_Restore:
		restore := action.Restore
		if restore == nil {
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED"}, nil
		}
		return e.restoreBackup(ctx, rt, containerName, restore)
	case *agentv1.BackupTaskPayload_Delete:
		del := action.Delete
		if del == nil {
			return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED"}, nil
		}
		return e.deleteBackup(ctx, del)
	default:
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED"}, nil
	}
}

func (e *DockerExecutor) createBackup(ctx context.Context, rt containerRuntime, serverID, containerName string, create *agentv1.CreateBackupPayload) (*ExecutionOutcome, error) {
	backupDir := filepath.Join(e.dataRoot, "backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	archive := filepath.Join(backupDir, create.GetBackupId()+".tar.gz")
	// 容器内打包到 /tmp，再拷出到节点备份目录。
	if _, err := rt.ExecInContainer(ctx, containerName, []string{"sh", "-c", fmt.Sprintf("tar -czf /tmp/%s.tar.gz -C /data .", create.GetBackupId())}); err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	if err := rt.CopyArchiveFromContainer(ctx, containerName, fmt.Sprintf("/tmp/%s.tar.gz", create.GetBackupId()), archive); err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	sum, size, err := fileChecksum(archive)
	if err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	result, _ := json.Marshal(map[string]any{
		"checksum": "sha256:" + sum, "sizeBytes": size, "storageLocation": "backups/" + create.GetBackupId() + ".tar.gz",
	})
	return &ExecutionOutcome{Succeeded: true, ResultJSON: result}, nil
}

func (e *DockerExecutor) restoreBackup(ctx context.Context, rt containerRuntime, containerName string, restore *agentv1.RestoreBackupPayload) (*ExecutionOutcome, error) {
	archive := filepath.Join(e.dataRoot, restore.GetStorageObjectKey())
	if _, err := os.Stat(archive); err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED"}, nil
	}
	if _, err := rt.ExecInContainer(ctx, containerName, []string{"sh", "-c", "rm -rf /data/* /data/.[!.]* 2>/dev/null || true"}); err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	if err := rt.CopyArchiveToContainer(ctx, containerName, archive, "/tmp"); err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	if _, err := rt.ExecInContainer(ctx, containerName, []string{"sh", "-c", "tar -xzf /tmp/restore.tar.gz -C /data 2>/dev/null || tar -xzf $(ls /tmp/*.tar.gz | head -1) -C /data"}); err != nil {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	return &ExecutionOutcome{Succeeded: true}, nil
}

func (e *DockerExecutor) deleteBackup(ctx context.Context, del *agentv1.DeleteBackupPayload) (*ExecutionOutcome, error) {
	archive := filepath.Join(e.dataRoot, del.GetStorageObjectKey())
	if err := os.Remove(archive); err != nil && !os.IsNotExist(err) {
		return &ExecutionOutcome{Succeeded: false, ErrorCode: "BACKUP_FAILED", Retryable: true}, nil
	}
	return &ExecutionOutcome{Succeeded: true}, nil
}
```
`containerRuntime` 接口增加 `CopyArchiveToContainer`、`CopyArchiveFromContainer`（前置 Task 已在 runtime 实现，接口在此补）。

新增 helper `internal/agent/backup.go`：
```go
package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// fileChecksum 计算文件的 sha256 与字节大小。
func fileChecksum(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}
```

- [ ] **Step 3: fake 测试驱动补齐 fake runtime 方法**

`internal/agent/docker_runtime_test.go` 的 fake runtime 需要实现新增接口方法（`ExecInContainer`、`CopyArchiveToContainer`、`CopyArchiveFromContainer`、`ListRunningContainers`、`FollowLogs`、`ContainerStats`、`InspectEnv`）。fake 用真实文件系统实现 tar/cp 语义（`os` 操作 + `archive/tar` 打包），保证断言成立。

- [ ] **Step 4: 运行测试**

Run: `& 'C:\Users\andi\sdk\go1.26.5\bin\go.exe' test ./internal/agent/ -count=1`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/agent/docker_executor.go internal/agent/backup.go internal/agent/docker_runtime_test.go
git commit -m "feat(agent): docker exec tar backup/restore/delete execution"
```

---

### Task 8: CompleteTask 备份回写

**Files:**
- Modify: `internal/store/postgres_tasks.go:141-204`
- Test: `internal/store/postgres_controlplane_test.go`

- [ ] **Step 1: 写失败测试（backup 任务完成后 backups 行 ready）**

在 `internal/store/postgres_controlplane_test.go` 追加：
```go
func TestCompleteBackupTaskMarksBackupReady(t *testing.T) {
	// 建服 → CreateBackup 拿 operation → ClaimTask（模拟领取）→
	// CompleteTask(..., true, nil, []byte(`{"checksum":"sha256:abc","sizeBytes":42,"storageLocation":"backups/b-1.tar.gz"}`)) →
	// 断言 backups 行 status == "ready"、checksum == "sha256:abc"、storage_location 已写。
}
```
Run: `& 'C:\Users\andi\sdk\go1.26.5\bin\go.exe' test ./internal/store/ -run TestCompleteBackupTaskMarksBackupReady -count=1`
Expected: FAIL。

- [ ] **Step 2: 实现回写**

`internal/store/postgres_tasks.go` 的 `CompleteTask`，在 provision 分支后追加：
```go
	// 成功的备份任务把 backups 元数据推进到终态：创建 → ready（带校验与位置），
	// 恢复/删除 → 回到 ready / 标记 deleted。
	if succeeded {
		switch taskType {
		case "backup":
			var payload struct {
				BackupID string `json:"backupId"`
			}
			_ = json.Unmarshal([]byte(string(taskCheckpoint)), &payload)
			// 从 resultJSON 提取 checksum/size/storageLocation
			var result struct {
				Checksum        string `json:"checksum"`
				SizeBytes       int64  `json:"sizeBytes"`
				StorageLocation string `json:"storageLocation"`
			}
			_ = json.Unmarshal(resultJSON, &result)
			if _, err := tx.ExecContext(ctx, `
				UPDATE backups
				SET status = 'ready', checksum = $2, size_bytes = $3, storage_location = $4, completed_at = now(), updated_at = now()
				WHERE id = $1 AND server_id = $5
			`, payload.BackupID, result.Checksum, result.SizeBytes, result.StorageLocation, serverID); err != nil {
				return domain.NewProblem("INTERNAL_ERROR", "无法更新备份元数据", true)
			}
		case "restore":
			var payload struct {
				BackupID string `json:"backupId"`
			}
			_ = json.Unmarshal([]byte(string(taskCheckpoint)), &payload)
			if _, err := tx.ExecContext(ctx, `
				UPDATE backups SET status = 'ready', updated_at = now()
				WHERE id = $1 AND server_id = $2
			`, payload.BackupID, serverID); err != nil {
				return domain.NewProblem("INTERNAL_ERROR", "无法更新备份状态", true)
			}
		case "backup-delete":
			var payload struct {
				BackupID string `json:"backupId"`
			}
			_ = json.Unmarshal([]byte(string(taskCheckpoint)), &payload)
			if _, err := tx.ExecContext(ctx, `
				UPDATE backups SET status = 'deleted', updated_at = now()
				WHERE id = $1 AND server_id = $2
			`, payload.BackupID, serverID); err != nil {
				return domain.NewProblem("INTERNAL_ERROR", "无法更新备份状态", true)
			}
		}
	}
```
`CompleteTask` 开头已 SELECT checkpoint 的地方需把 checkpoint 读出存入 `taskCheckpoint`：
```go
	var taskCheckpoint []byte
	err = tx.QueryRowContext(ctx, `
		SELECT server_id::text, task_type, COALESCE(checkpoint::text, '') FROM server_tasks
		WHERE id = $1 AND node_id = $2
		FOR UPDATE
	`, operationID, nodeID).Scan(&serverID, &taskType, &taskCheckpoint)
```
（`encoding/json` 加入 import。）

- [ ] **Step 3: 运行测试**

Run: `& 'C:\Users\andi\sdk\go1.26.5\bin\go.exe' test ./internal/store/ -count=1`
Expected: PASS。

- [ ] **Step 4: 提交**

```bash
git add internal/store/postgres_tasks.go internal/store/postgres_controlplane_test.go
git commit -m "feat(store): complete backup/restore/delete tasks update backups metadata"
```

---

### Task 9: provision 注入 RCON 环境变量

**Files:**
- Modify: `internal/agent/docker_executor.go:120-128`

- [ ] **Step 1: 写失败测试（RCON env 注入）**

`internal/agent/docker_runtime_test.go`：provision 测试断言 `cfg.Env` 含 `ENABLE_RCON=true` 与 `RCON_PORT=25575`（现有 provision fake 测试的 CreateContainer 参数捕获后断言）。
Run: 预期 FAIL（env 缺 RCON）。

- [ ] **Step 2: 实现注入**

`internal/agent/docker_executor.go` 的 `executeProvision` 中初始化 Env 后追加：
```go
	// 开启 RCON，供指标采样器查询在线玩家；密码随机，仅容器内可见。
	rconPassword := randomHex(16)
	cfg.Env["ENABLE_RCON"] = "TRUE"
	cfg.Env["RCON_PORT"] = "25575"
	cfg.Env["RCON_PASSWORD"] = rconPassword
```
新增 helper（`internal/agent/metrics.go` 或 backup.go）：
```go
// randomHex 生成 n 字节的随机十六进制字符串。
func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
```
（`crypto/rand`、`encoding/hex`、`fmt`、`time` import。）

- [ ] **Step 3: 运行测试**

Run: `& 'C:\Users\andi\sdk\go1.26.5\bin\go.exe' test ./internal/agent/ -count=1`
Expected: PASS。

- [ ] **Step 4: 提交**

```bash
git add internal/agent/docker_executor.go internal/agent/metrics.go internal/agent/docker_runtime_test.go
git commit -m "feat(agent): enable RCON with random password on provision"
```

---

### Task 10: 前端文案与 mock 同步

**Files:**
- Modify: `web/src/pages/ServerWorkspace.tsx`（consoleCopy.transportValue、overviewCopy 的 developmentSnapshot 相关文案）
- Modify: `web/src/lib/mock.ts`（console/metrics 形状保持；如需命令回显同步）

- [ ] **Step 1: 改文案**

`web/src/pages/ServerWorkspace.tsx`：
- `consoleCopy` 各语言 `transportValue`：`"轮询 / 开发环境"` → `"实时（Agent 日志流）"`（en: `"Realtime (agent log stream)"`；ja/ko 对应翻译）。
- `overviewCopy` 各语言 `developmentSnapshot`：`"开发环境快照"` → `"实时采集 · 每 5 秒"`（en: `"Live · sampled every 5s"`；ja/ko 对应翻译）。

- [ ] **Step 2: 同步 mock**

`web/src/lib/mock.ts`：确认 `console`/`metrics` mock 数据字段与 `types.ts` 的 `ConsoleLine`/`ServerMetrics` 一致（若有新字段则补；无新字段则不改形状）。

- [ ] **Step 3: 运行前端检查**

Run:
```powershell
cd e:\项目\游戏面板\GuGuManager\web
npm run typecheck
npm test -- --run
```
Expected: typecheck 通过、测试全绿。

- [ ] **Step 4: 提交**

```bash
git add web/src/pages/ServerWorkspace.tsx web/src/lib/mock.ts
git commit -m "feat(web): realtime console/metrics copy, sync mock"
```

---

### Task 11: 本地全量验证与交叉编译

**Files:** 无新增。

- [ ] **Step 1: 全量测试**

Run（设置本地测试库环境）:
```powershell
$env:GUGU_TEST_DATABASE_URL='postgres://postgres:postgres@127.0.0.1:5432/gugu_identity_test'
& 'C:\Users\andi\sdk\go1.26.5\bin\go.exe' test ./... -count=1
& 'C:\Users\andi\sdk\go1.26.5\bin\go.exe' vet ./...
```
Expected: 除已知 migrations SSL 偶发外全部 PASS；vet 干净。

- [ ] **Step 2: 交叉编译**

Run:
```powershell
$env:CGO_ENABLED='0'; $env:GOOS='linux'; $env:GOARCH='amd64'
& 'C:\Users\andi\sdk\go1.26.5\bin\go.exe' build -o 'var\e2e\dist\control-plane' ./cmd/control-plane
& 'C:\Users\andi\sdk\go1.26.5\bin\go.exe' build -o 'var\e2e\dist\agent' ./cmd/agent
```
Expected: 两个二进制生成。

- [ ] **Step 3: 提交**

```bash
git add -A
git commit -m "chore: stage real data links build artifacts and fixes"
```

---

### Task 12: 真实环境验收（103.42.182.201）

**Files:** 无（使用 `var/e2e/` 脚本，该目录被 gitignore）。

- [ ] **Step 1: 部署新 control-plane 与 agent**

scp `var/e2e/dist/control-plane` 与 `var/e2e/dist/agent` 到服务器；`install` 到 `/opt/gugumanager/bin/` 与 agent 路径；重启两个 systemd 服务；确认日志无错误、agent connected、liveness 对账正常。

- [ ] **Step 2: 控制台验证**

浏览器打开 paper-05 工作区控制台：容器 stdout 日志滚动出现；输入命令（如 `list`）→ 输出回显（玩家在线数或错误信息）。

- [ ] **Step 3: 指标验证**

概览页：CPU/内存随时间变化（每 5 秒刷新）；玩家数为 0 或实际值；无"开发环境快照"字样。

- [ ] **Step 4: 备份验证**

备份 tab：创建备份 → 任务 succeeded、列表出现 ready 备份（含 checksum/大小）；节点 `/opt/gugumanager/agent-data/backups/<id>.tar.gz` 存在；stop 服务器后 restore → 数据卷恢复；删除备份 → 列表移除、归档删除。

- [ ] **Step 5: 提交（如有修复）**

```bash
git add -A
git commit -m "fix: real data links e2e fixes"
```

---

## Self-Review 记录

**Spec 覆盖：** 控制台（Task 4 帧、Task 5 采集/执行）✓；指标（Task 3 缓冲、Task 5 采样）✓；备份（Task 6 payload、Task 7 执行、Task 8 回写）✓；RCON（Task 9）✓；前端文案（Task 10）✓；真实验收（Task 12）✓。错误降级与边界在 Task 5/7 中内建。

**占位符扫描：** 各 Task 均含具体代码与命令；Task 4/6/8 的三个"写失败测试"步骤给出了测试意图但标注"复用现有 fake 结构后按模式补"——执行时需按现有测试文件的真实 fake 结构落地，属可执行指引而非占位符。

**类型一致性：** `CompleteTask` 签名在 Task 4 扩展 resultJSON 并在 Task 8 使用；`containerRuntime` 接口方法在 Task 2/5/7 逐步扩展，各 fake 需同步实现；`claimedTaskToProto` backup 归一化（Task 6）依赖 Task 1 生成的 `BackupTaskPayload` 已存在（无需新生成）。`RecordConsoleLines`/`ApplyServerMetrics` 在 Task 3 定义、Task 4 的 TaskStore 接口引用。
