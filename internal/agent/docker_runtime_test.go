package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	agentv1 "github.com/gugumanager/gugumanager/api/proto/gugumanager/agent/v1"
	"github.com/gugumanager/gugumanager/internal/runtime"
	"google.golang.org/protobuf/encoding/protojson"
)

// ---------------------------------------------------------------------------
// fakeDocker：containerRuntime 接口的内存实现
// ---------------------------------------------------------------------------

type fakeDocker struct {
	mu         sync.Mutex
	created    []runtime.ContainerConfig
	started    []string
	stopped    []string
	restarted  []string
	removed    []string
	inspected  []string
	status     runtime.ContainerStatus
	statusErr  error
	createID   string
	createErr  error
	actionErr  error

	execOut   string
	execErr   error
	execArgv  [][]string
	stats     runtime.ContainerStats
	statsErr  error
	env       map[string]string
	envErr    error
	containers []string
	listErr    error
	logReader  io.ReadCloser
	copiesTo   []archiveOp
	copiesFrom []archiveOp
}

type archiveOp struct {
	ContainerID string
	HostPath    string
	ContainerPath string
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{
		createID: "cont-abc",
		status:   runtime.ContainerStatus{ID: "cont-abc", State: "running", Status: "running", Running: true, Healthy: true},
		env:      map[string]string{"EULA": "TRUE"},
	}
}

func (f *fakeDocker) ExecInContainer(ctx context.Context, containerID string, argv []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execArgv = append(f.execArgv, argv)
	return f.execOut, f.execErr
}

func (f *fakeDocker) ContainerStats(ctx context.Context, containerID string) (runtime.ContainerStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stats, f.statsErr
}

func (f *fakeDocker) FollowLogs(ctx context.Context, containerID string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logReader, nil
}

func (f *fakeDocker) InspectEnv(ctx context.Context, containerID string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.env, f.envErr
}

func (f *fakeDocker) ListRunningContainers(ctx context.Context, namePrefix string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.containers...), f.listErr
}

func (f *fakeDocker) CopyArchiveToContainer(ctx context.Context, containerID, hostPath, containerPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.copiesTo = append(f.copiesTo, archiveOp{ContainerID: containerID, HostPath: hostPath, ContainerPath: containerPath})
	return nil
}

func (f *fakeDocker) CopyArchiveFromContainer(ctx context.Context, containerID, containerPath, hostPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.copiesFrom = append(f.copiesFrom, archiveOp{ContainerID: containerID, ContainerPath: containerPath, HostPath: hostPath})
	// fake 在宿主机目标路径生成一份归档，供后续断言与恢复路径使用。
	if hostPath != "" {
		_ = os.MkdirAll(filepath.Dir(hostPath), 0o755)
		_ = os.WriteFile(hostPath, []byte("fake-archive"), 0o644)
	}
	return nil
}

func (f *fakeDocker) CreateContainer(ctx context.Context, cfg runtime.ContainerConfig) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, cfg)
	if f.createErr != nil {
		return "", f.createErr
	}
	return f.createID, nil
}

func (f *fakeDocker) StartContainer(ctx context.Context, containerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, containerID)
	return f.actionErr
}

func (f *fakeDocker) StopContainer(ctx context.Context, containerID string, timeoutSec int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, containerID)
	return f.actionErr
}

func (f *fakeDocker) RestartContainer(ctx context.Context, containerID string, timeoutSec int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarted = append(f.restarted, containerID)
	return f.actionErr
}

func (f *fakeDocker) RemoveContainer(ctx context.Context, containerID string, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, containerID)
	return f.actionErr
}

func (f *fakeDocker) InspectContainer(ctx context.Context, containerID string) (runtime.ContainerStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspected = append(f.inspected, containerID)
	if f.statusErr != nil {
		return runtime.ContainerStatus{}, f.statusErr
	}
	return f.status, nil
}

func (f *fakeDocker) createdCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.created)
}

func (f *fakeDocker) lastCreated() runtime.ContainerConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.created) == 0 {
		return runtime.ContainerConfig{}
	}
	return f.created[len(f.created)-1]
}

func (f *fakeDocker) startedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.started)
}

func (f *fakeDocker) removedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.removed)
}

// ---------------------------------------------------------------------------
// 测试用例
// ---------------------------------------------------------------------------

func provisionPayload() *agentv1.ProvisionTaskPayload {
	return &agentv1.ProvisionTaskPayload{
		GameDefinitionId: "io.gugumanager.papermc",
		ResourceLimits: &agentv1.ResourceLimits{
			MemoryBytes:   2048 * 1024 * 1024,
			DiskBytes:     10 * 1024 * 1024 * 1024,
			CpuMillicores: 2048,
			Pids:          512,
		},
		Allocations: []*agentv1.PortAllocation{
			{AllocationId: "alloc-1", HostPort: 30000, ContainerPort: 25565, Protocol: agentv1.NetworkProtocol_NETWORK_PROTOCOL_TCP},
		},
		Variables: map[string]string{
			"MEMORY": "2048M",
			"MOTD":   "gugu server",
		},
		StartAfterProvision: true,
	}
}

func TestDockerExecutorProvisionTyped(t *testing.T) {
	fd := newFakeDocker()
	exec := &DockerExecutor{dataRoot: t.TempDir(), rt: fd}

	task := &agentv1.Task{
		OperationId: "op-prov",
		ServerId:    "server-1",
		Generation:  1,
		Type:        "provision",
		Attempt:     1,
		Payload:     &agentv1.Task_Provision{Provision: provisionPayload()},
	}

	outcome, err := exec.ExecuteTask(context.Background(), task)
	if err != nil {
		t.Fatalf("execute task: %v", err)
	}
	if !outcome.Succeeded {
		t.Fatalf("expected success, got error code %q retryable=%v", outcome.ErrorCode, outcome.Retryable)
	}
	if outcome.Observed != nil {
		t.Errorf("provision should not report observed, got %+v", outcome.Observed)
	}

	cfg := fd.lastCreated()
	if cfg.Name != "gugu-server-server-1" {
		t.Errorf("container name = %q, want gugu-server-server-1", cfg.Name)
	}
	if cfg.Image != "itzg/minecraft-server:latest" {
		t.Errorf("image = %q, want itzg/minecraft-server:latest", cfg.Image)
	}
	if cfg.Env["EULA"] != "TRUE" {
		t.Errorf("env EULA = %q, want TRUE", cfg.Env["EULA"])
	}
	if cfg.Env["MEMORY"] != "2048M" || cfg.Env["MOTD"] != "gugu server" {
		t.Errorf("env variables not copied: %+v", cfg.Env)
	}
	if cfg.PortBindings[25565] != 30000 {
		t.Errorf("port bindings = %+v, want 25565->30000", cfg.PortBindings)
	}
	wantVolume := filepath.Join(exec.dataRoot, "server-1")
	if cfg.VolumePath != wantVolume {
		t.Errorf("volume path = %q, want %q", cfg.VolumePath, wantVolume)
	}
	if cfg.MemoryMB != 2048 {
		t.Errorf("memory mb = %d, want 2048", cfg.MemoryMB)
	}
	if cfg.CPUShares != 2048 {
		t.Errorf("cpu shares = %d, want 2048", cfg.CPUShares)
	}
	if got := string(outcome.ResultJSON); got == "" {
		t.Error("expected result json recording container id")
	} else if want := `"containerId":"cont-abc"`; !strings.Contains(got, want) {
		t.Errorf("result json = %s, want to contain %s", got, want)
	}
}

func TestDockerExecutorProvisionPayloadJSON(t *testing.T) {
	fd := newFakeDocker()
	exec := &DockerExecutor{dataRoot: t.TempDir(), rt: fd}

	raw, err := protojson.Marshal(provisionPayload())
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	task := &agentv1.Task{
		OperationId: "op-prov-json",
		ServerId:    "server-2",
		Type:        "provision",
		Attempt:     1,
		Payload:     &agentv1.Task_PayloadJson{PayloadJson: raw},
	}

	outcome, err := exec.ExecuteTask(context.Background(), task)
	if err != nil {
		t.Fatalf("execute task: %v", err)
	}
	if !outcome.Succeeded {
		t.Fatalf("expected success, got %q", outcome.ErrorCode)
	}
	cfg := fd.lastCreated()
	if cfg.Name != "gugu-server-server-2" {
		t.Errorf("container name = %q", cfg.Name)
	}
	if cfg.PortBindings[25565] != 30000 {
		t.Errorf("port bindings = %+v", cfg.PortBindings)
	}
}

func TestDockerExecutorPowerStart(t *testing.T) {
	fd := newFakeDocker()
	exec := &DockerExecutor{dataRoot: t.TempDir(), rt: fd}

	task := &agentv1.Task{
		OperationId: "op-power",
		ServerId:    "server-1",
		Type:        "power",
		Attempt:     1,
		Payload:     &agentv1.Task_Power{Power: &agentv1.PowerTaskPayload{Action: agentv1.PowerAction_POWER_ACTION_START}},
	}

	outcome, err := exec.ExecuteTask(context.Background(), task)
	if err != nil {
		t.Fatalf("execute task: %v", err)
	}
	if !outcome.Succeeded {
		t.Fatalf("expected success, got %q", outcome.ErrorCode)
	}
	fd.mu.Lock()
	started := len(fd.started)
	fd.mu.Unlock()
	if started != 1 || fd.started[0] != "gugu-server-server-1" {
		t.Errorf("started = %v, want [gugu-server-server-1]", fd.started)
	}
	if outcome.Observed == nil {
		t.Fatal("expected observed state after power task")
	}
	if outcome.Observed.GetServerId() != "server-1" {
		t.Errorf("observed server = %q", outcome.Observed.GetServerId())
	}
	if outcome.Observed.GetObservedPower() != agentv1.ObservedPower_OBSERVED_POWER_RUNNING {
		t.Errorf("observed power = %v, want RUNNING", outcome.Observed.GetObservedPower())
	}
	if outcome.Observed.GetHealthCondition() != agentv1.HealthCondition_HEALTH_CONDITION_HEALTHY {
		t.Errorf("observed health = %v, want HEALTHY", outcome.Observed.GetHealthCondition())
	}
}

func TestDockerExecutorPowerStop(t *testing.T) {
	fd := newFakeDocker()
	fd.status = runtime.ContainerStatus{ID: "cont-abc", State: "exited", Status: "exited", Running: false, Healthy: false}
	exec := &DockerExecutor{dataRoot: t.TempDir(), rt: fd}

	task := &agentv1.Task{
		OperationId: "op-power-stop",
		ServerId:    "server-1",
		Type:        "power",
		Payload:     &agentv1.Task_Power{Power: &agentv1.PowerTaskPayload{Action: agentv1.PowerAction_POWER_ACTION_STOP, GracefulTimeoutSeconds: 45}},
	}

	outcome, err := exec.ExecuteTask(context.Background(), task)
	if err != nil {
		t.Fatalf("execute task: %v", err)
	}
	if !outcome.Succeeded {
		t.Fatalf("expected success, got %q", outcome.ErrorCode)
	}
	fd.mu.Lock()
	stopped := len(fd.stopped)
	fd.mu.Unlock()
	if stopped != 1 {
		t.Fatalf("stop calls = %d, want 1", stopped)
	}
	if outcome.Observed.GetObservedPower() != agentv1.ObservedPower_OBSERVED_POWER_STOPPED {
		t.Errorf("observed power = %v, want STOPPED", outcome.Observed.GetObservedPower())
	}
	if outcome.Observed.GetHealthCondition() != agentv1.HealthCondition_HEALTH_CONDITION_UNKNOWN {
		t.Errorf("observed health = %v, want UNKNOWN", outcome.Observed.GetHealthCondition())
	}
}

func TestDockerExecutorPowerRestart(t *testing.T) {
	fd := newFakeDocker()
	exec := &DockerExecutor{dataRoot: t.TempDir(), rt: fd}

	task := &agentv1.Task{
		OperationId: "op-power-restart",
		ServerId:    "server-1",
		Type:        "power",
		Payload:     &agentv1.Task_Power{Power: &agentv1.PowerTaskPayload{Action: agentv1.PowerAction_POWER_ACTION_RESTART}},
	}

	outcome, err := exec.ExecuteTask(context.Background(), task)
	if err != nil {
		t.Fatalf("execute task: %v", err)
	}
	if !outcome.Succeeded {
		t.Fatalf("expected success, got %q", outcome.ErrorCode)
	}
	fd.mu.Lock()
	restarted := len(fd.restarted)
	fd.mu.Unlock()
	if restarted != 1 {
		t.Errorf("restart calls = %d, want 1", restarted)
	}
}

func TestDockerExecutorPowerKill(t *testing.T) {
	fd := newFakeDocker()
	fd.status = runtime.ContainerStatus{ID: "cont-abc", State: "dead", Status: "dead", Running: false}
	exec := &DockerExecutor{dataRoot: t.TempDir(), rt: fd}

	task := &agentv1.Task{
		OperationId: "op-power-kill",
		ServerId:    "server-1",
		Type:        "power",
		Payload:     &agentv1.Task_Power{Power: &agentv1.PowerTaskPayload{Action: agentv1.PowerAction_POWER_ACTION_KILL}},
	}

	outcome, err := exec.ExecuteTask(context.Background(), task)
	if err != nil {
		t.Fatalf("execute task: %v", err)
	}
	if !outcome.Succeeded {
		t.Fatalf("expected success, got %q", outcome.ErrorCode)
	}
	fd.mu.Lock()
	removed := len(fd.removed)
	fd.mu.Unlock()
	if removed != 1 || fd.removed[0] != "gugu-server-server-1" {
		t.Errorf("removed = %v, want [gugu-server-server-1]", fd.removed)
	}
	if outcome.Observed.GetObservedPower() != agentv1.ObservedPower_OBSERVED_POWER_STOPPED {
		t.Errorf("observed power = %v, want STOPPED", outcome.Observed.GetObservedPower())
	}
}

func TestDockerExecutorUnsupportedTask(t *testing.T) {
	fd := newFakeDocker()
	exec := &DockerExecutor{dataRoot: t.TempDir(), rt: fd}

	task := &agentv1.Task{
		OperationId: "op-extension",
		ServerId:    "server-1",
		Type:        "extension",
		Payload:     &agentv1.Task_Extension{Extension: &agentv1.ExtensionTaskPayload{}},
	}

	outcome, err := exec.ExecuteTask(context.Background(), task)
	if err != nil {
		t.Fatalf("execute task: %v", err)
	}
	if outcome.Succeeded {
		t.Error("unsupported task should not succeed")
	}
	if outcome.ErrorCode != "UNSUPPORTED_TASK" {
		t.Errorf("error code = %q, want UNSUPPORTED_TASK", outcome.ErrorCode)
	}
	if outcome.Retryable {
		t.Error("unsupported task should not be retryable")
	}
	if fd.createdCount() != 0 && fd.startedCount() != 0 {
		t.Error("runtime should not be touched for unsupported task")
	}
}

func TestDockerExecutorRuntimeUnavailable(t *testing.T) {
	exec := &DockerExecutor{
		dataRoot: t.TempDir(),
		newRuntime: func() (containerRuntime, error) {
			return nil, errors.New("no docker daemon")
		},
	}

	task := &agentv1.Task{
		OperationId: "op-prov",
		ServerId:    "server-1",
		Type:        "provision",
		Payload:     &agentv1.Task_Provision{Provision: provisionPayload()},
	}

	outcome, err := exec.ExecuteTask(context.Background(), task)
	if err != nil {
		t.Fatalf("execute task: %v", err)
	}
	if outcome.Succeeded {
		t.Error("expected failure when runtime unavailable")
	}
	if outcome.ErrorCode != "RUNTIME_UNAVAILABLE" {
		t.Errorf("error code = %q, want RUNTIME_UNAVAILABLE", outcome.ErrorCode)
	}
	if !outcome.Retryable {
		t.Error("runtime unavailable should be retryable")
	}
}

func TestDockerExecutorPowerActionError(t *testing.T) {
	fd := newFakeDocker()
	fd.actionErr = errors.New("container not found")
	exec := &DockerExecutor{dataRoot: t.TempDir(), rt: fd}

	task := &agentv1.Task{
		OperationId: "op-power",
		ServerId:    "server-1",
		Type:        "power",
		Payload:     &agentv1.Task_Power{Power: &agentv1.PowerTaskPayload{Action: agentv1.PowerAction_POWER_ACTION_START}},
	}

	outcome, err := exec.ExecuteTask(context.Background(), task)
	if err != nil {
		t.Fatalf("execute task: %v", err)
	}
	if outcome.Succeeded {
		t.Error("expected failure on runtime error")
	}
	if outcome.ErrorCode != "POWER_OPERATION_FAILED" {
		t.Errorf("error code = %q, want POWER_OPERATION_FAILED", outcome.ErrorCode)
	}
	if !outcome.Retryable {
		t.Error("power operation failure should be retryable")
	}
}

func TestDockerExecutorProvisionCreateError(t *testing.T) {
	fd := newFakeDocker()
	fd.createErr = errors.New("image pull failed")
	exec := &DockerExecutor{dataRoot: t.TempDir(), rt: fd}

	task := &agentv1.Task{
		OperationId: "op-prov",
		ServerId:    "server-1",
		Type:        "provision",
		Payload:     &agentv1.Task_Provision{Provision: provisionPayload()},
	}

	outcome, err := exec.ExecuteTask(context.Background(), task)
	if err != nil {
		t.Fatalf("execute task: %v", err)
	}
	if outcome.Succeeded {
		t.Error("expected failure on create error")
	}
	if outcome.ErrorCode != "PROVISION_FAILED" {
		t.Errorf("error code = %q, want PROVISION_FAILED", outcome.ErrorCode)
	}
	if !outcome.Retryable {
		t.Error("provision failure should be retryable")
	}
}

func TestDockerExecutorDefaultRuntimeLazy(t *testing.T) {
	// NewDockerExecutor 在无 Docker 环境下构造不应失败（延迟到 ExecuteTask）。
	exec, err := NewDockerExecutor(t.TempDir())
	if err != nil {
		t.Fatalf("new docker executor: %v", err)
	}
	if exec == nil {
		t.Fatal("expected non-nil executor")
	}
	if exec.rt != nil {
		t.Error("runtime should be lazily initialized, not at construction")
	}
}

func TestExecuteConsoleCommandRunsExecInContainer(t *testing.T) {
	fd := newFakeDocker()
	fd.execOut = "There are 2 of a max of 20 players online"
	fd.env["RCON_PASSWORD"] = "secret123"
	exec := &DockerExecutor{dataRoot: t.TempDir(), rt: fd}

	outcome, err := exec.ExecuteConsoleCommand(context.Background(), "server-1", "list")
	if err != nil {
		t.Fatalf("execute console command: %v", err)
	}
	if !outcome.Succeeded {
		t.Fatalf("expected success, got %s", outcome.ErrorCode)
	}
	var payload struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(outcome.ResultJSON, &payload); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !strings.Contains(payload.Output, "2 of a max of 20") {
		t.Errorf("output = %q, want rcon player list echo", payload.Output)
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if len(fd.execArgv) != 1 {
		t.Fatalf("exec calls = %d, want 1", len(fd.execArgv))
	}
	if got := strings.Join(fd.execArgv[0], " "); got != "rcon-cli --host 127.0.0.1 --port 25575 --password secret123 list" {
		t.Errorf("exec argv = %q, want rcon-cli dispatch", got)
	}
}

func TestExecuteConsoleCommandFallsBackToShellWithoutRCON(t *testing.T) {
	fd := newFakeDocker()
	fd.execOut = "hello"
	// newFakeDocker 的 env 不含 RCON_PASSWORD。
	exec := &DockerExecutor{dataRoot: t.TempDir(), rt: fd}

	outcome, err := exec.ExecuteConsoleCommand(context.Background(), "server-1", "echo hello")
	if err != nil {
		t.Fatalf("execute console command: %v", err)
	}
	if !outcome.Succeeded {
		t.Fatalf("expected success, got %s", outcome.ErrorCode)
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if len(fd.execArgv) != 1 {
		t.Fatalf("exec calls = %d, want 1", len(fd.execArgv))
	}
	if got := strings.Join(fd.execArgv[0], " "); got != "sh -c echo hello" {
		t.Errorf("exec argv = %q, want 'sh -c echo hello'", got)
	}
}

func TestExecuteConsoleCommandRuntimeUnavailable(t *testing.T) {
	exec := &DockerExecutor{
		dataRoot: t.TempDir(),
		newRuntime: func() (containerRuntime, error) {
			return nil, errors.New("no docker daemon")
		},
	}
	outcome, err := exec.ExecuteConsoleCommand(context.Background(), "server-1", "list")
	if err != nil {
		t.Fatalf("execute console command: %v", err)
	}
	if outcome.Succeeded || outcome.ErrorCode != "RUNTIME_UNAVAILABLE" || !outcome.Retryable {
		t.Errorf("outcome = %+v, want RUNTIME_UNAVAILABLE retryable", outcome)
	}
}

func TestExecuteConsoleCommandExecError(t *testing.T) {
	fd := newFakeDocker()
	fd.execErr = errors.New("exec failed")
	exec := &DockerExecutor{dataRoot: t.TempDir(), rt: fd}
	outcome, err := exec.ExecuteConsoleCommand(context.Background(), "server-1", "list")
	if err != nil {
		t.Fatalf("execute console command: %v", err)
	}
	if outcome.Succeeded || outcome.ErrorCode != "COMMAND_FAILED" || outcome.Retryable {
		t.Errorf("outcome = %+v, want COMMAND_FAILED non-retryable", outcome)
	}
}

func backupCreateTaskPayload(t *testing.T, backupID string) []byte {
	t.Helper()
	payload, err := protojson.Marshal(&agentv1.BackupTaskPayload{Action: &agentv1.BackupTaskPayload_Create{
		Create: &agentv1.CreateBackupPayload{BackupId: backupID, StorageObjectKey: "backups/" + backupID + ".tar.gz"},
	}})
	if err != nil {
		t.Fatalf("marshal backup payload: %v", err)
	}
	return payload
}

func TestExecuteBackupTaskArchivesDataVolume(t *testing.T) {
	dir := t.TempDir()
	fd := newFakeDocker()
	exec := &DockerExecutor{dataRoot: dir, rt: fd}

	task := &agentv1.Task{
		OperationId: "op-b",
		ServerId:    "srv-1",
		Type:        "backup",
		Attempt:     1,
		Payload:     &agentv1.Task_PayloadJson{PayloadJson: backupCreateTaskPayload(t, "b-1")},
	}
	outcome, err := exec.ExecuteTask(context.Background(), task)
	if err != nil || !outcome.Succeeded {
		t.Fatalf("execute backup: outcome=%+v err=%v", outcome, err)
	}
	var result struct {
		Checksum        string `json:"checksum"`
		SizeBytes       int64  `json:"sizeBytes"`
		StorageLocation string `json:"storageLocation"`
	}
	if err := json.Unmarshal(outcome.ResultJSON, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.StorageLocation != "backups/b-1.tar.gz" {
		t.Errorf("storage location = %q, want backups/b-1.tar.gz", result.StorageLocation)
	}
	if !strings.HasPrefix(result.Checksum, "sha256:") {
		t.Errorf("checksum = %q, want sha256: prefix", result.Checksum)
	}
	archive := filepath.Join(dir, "backups", "b-1.tar.gz")
	if _, err := os.Stat(archive); err != nil {
		t.Errorf("archive %s not created: %v", archive, err)
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if len(fd.execArgv) != 1 || !strings.Contains(strings.Join(fd.execArgv[0], " "), "tar -czf") {
		t.Errorf("expected in-container tar exec, got %v", fd.execArgv)
	}
}

func TestExecuteRestoreBackupTask(t *testing.T) {
	dir := t.TempDir()
	// 预置归档供恢复读取。
	if err := os.MkdirAll(filepath.Join(dir, "backups"), 0o755); err != nil {
		t.Fatalf("mkdir backups: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "backups", "b-1.tar.gz"), []byte("archive-bytes"), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	fd := newFakeDocker()
	exec := &DockerExecutor{dataRoot: dir, rt: fd}

	payload, err := protojson.Marshal(&agentv1.BackupTaskPayload{Action: &agentv1.BackupTaskPayload_Restore{
		Restore: &agentv1.RestoreBackupPayload{BackupId: "b-1", StorageObjectKey: "backups/b-1.tar.gz"},
	}})
	if err != nil {
		t.Fatalf("marshal restore payload: %v", err)
	}
	task := &agentv1.Task{OperationId: "op-r", ServerId: "srv-1", Type: "backup", Attempt: 1, Payload: &agentv1.Task_PayloadJson{PayloadJson: payload}}
	outcome, err := exec.ExecuteTask(context.Background(), task)
	if err != nil || !outcome.Succeeded {
		t.Fatalf("execute restore: outcome=%+v err=%v", outcome, err)
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if len(fd.copiesTo) != 1 {
		t.Fatalf("copy to container calls = %d, want 1", len(fd.copiesTo))
	}
	if fd.copiesTo[0].HostPath != filepath.Join(dir, "backups", "b-1.tar.gz") {
		t.Errorf("restore source = %q", fd.copiesTo[0].HostPath)
	}
	if len(fd.started) != 1 || fd.started[0] != "gugu-server-srv-1" {
		t.Errorf("restore should start container first, started = %v", fd.started)
	}
}

func TestExecuteDeleteBackupTask(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "backups"), 0o755); err != nil {
		t.Fatalf("mkdir backups: %v", err)
	}
	archive := filepath.Join(dir, "backups", "b-1.tar.gz")
	if err := os.WriteFile(archive, []byte("bytes"), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	fd := newFakeDocker()
	exec := &DockerExecutor{dataRoot: dir, rt: fd}

	payload, err := protojson.Marshal(&agentv1.BackupTaskPayload{Action: &agentv1.BackupTaskPayload_Delete{
		Delete: &agentv1.DeleteBackupPayload{BackupId: "b-1", StorageObjectKey: "backups/b-1.tar.gz", DeleteRemoteObject: true},
	}})
	if err != nil {
		t.Fatalf("marshal delete payload: %v", err)
	}
	task := &agentv1.Task{OperationId: "op-d", ServerId: "srv-1", Type: "backup", Attempt: 1, Payload: &agentv1.Task_PayloadJson{PayloadJson: payload}}
	outcome, err := exec.ExecuteTask(context.Background(), task)
	if err != nil || !outcome.Succeeded {
		t.Fatalf("execute delete: outcome=%+v err=%v", outcome, err)
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Errorf("archive should be removed, stat err = %v", err)
	}
}

func TestDockerExecutorProvisionEnablesRCON(t *testing.T) {
	fd := newFakeDocker()
	exec := &DockerExecutor{dataRoot: t.TempDir(), rt: fd}

	task := &agentv1.Task{
		OperationId: "op-prov-rcon",
		ServerId:    "server-1",
		Type:        "provision",
		Attempt:     1,
		Payload:     &agentv1.Task_Provision{Provision: provisionPayload()},
	}
	outcome, err := exec.ExecuteTask(context.Background(), task)
	if err != nil || !outcome.Succeeded {
		t.Fatalf("execute provision: outcome=%+v err=%v", outcome, err)
	}
	cfg := fd.lastCreated()
	if cfg.Env["ENABLE_RCON"] != "TRUE" {
		t.Errorf("env ENABLE_RCON = %q, want TRUE", cfg.Env["ENABLE_RCON"])
	}
	if cfg.Env["RCON_PORT"] != "25575" {
		t.Errorf("env RCON_PORT = %q, want 25575", cfg.Env["RCON_PORT"])
	}
	if cfg.Env["RCON_PASSWORD"] == "" {
		t.Error("expected a non-empty RCON_PASSWORD")
	}
}
