package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/runtime"
	"google.golang.org/protobuf/encoding/protojson"
)

// ---------------------------------------------------------------------------
// fakeDocker：containerRuntime 接口的内存实现
// ---------------------------------------------------------------------------

type fakeDocker struct {
	mu        sync.Mutex
	created   []runtime.ContainerConfig
	started   []string
	stopped   []string
	restarted []string
	killed    []string
	removed   []string
	renamed   []string
	inspected []string
	status    runtime.ContainerStatus
	statusErr error
	createID  string
	createErr error
	actionErr error

	execOut     string
	execErr     error
	execArgv    [][]string
	stats       runtime.ContainerStats
	statsErr    error
	env         map[string]string
	envErr      error
	containers  []string
	listErr     error
	logReader   io.ReadCloser
	copiesTo    []archiveOp
	copiesFrom  []archiveOp
	copyFromErr error
}

type archiveOp struct {
	ContainerID   string
	HostPath      string
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
	if f.copyFromErr != nil {
		return f.copyFromErr
	}
	// fake 在宿主机目标路径生成一份归档，供后续断言与恢复路径使用。
	if hostPath != "" {
		archive, err := fakeDockerBackupArchive(filepath.Base(containerPath))
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(hostPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(hostPath, archive, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func fakeBackupPayload() ([]byte, error) {
	var payload bytes.Buffer
	gzipWriter := gzip.NewWriter(&payload)
	inner := tar.NewWriter(gzipWriter)
	content := []byte("level-data")
	if err := inner.WriteHeader(&tar.Header{Name: "world/level.dat", Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		return nil, err
	}
	if _, err := inner.Write(content); err != nil {
		return nil, err
	}
	if err := inner.Close(); err != nil {
		return nil, err
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, err
	}
	return payload.Bytes(), nil
}

func fakeDockerBackupArchive(name string) ([]byte, error) {
	payload, err := fakeBackupPayload()
	if err != nil {
		return nil, err
	}
	var dockerArchive bytes.Buffer
	outer := tar.NewWriter(&dockerArchive)
	if err := outer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
		return nil, err
	}
	if _, err := outer.Write(payload); err != nil {
		return nil, err
	}
	if err := outer.Close(); err != nil {
		return nil, err
	}
	return dockerArchive.Bytes(), nil
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

func (f *fakeDocker) KillContainer(ctx context.Context, containerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, containerID)
	return f.actionErr
}

func (f *fakeDocker) RemoveContainer(ctx context.Context, containerID string, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, containerID)
	return f.actionErr
}

func (f *fakeDocker) RenameContainer(_ context.Context, containerID, newName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renamed = append(f.renamed, containerID+"->"+newName)
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
	target := domain.GameRuntimeTarget{
		Adapter: "container/v1", Image: paperMCRuntimeImage, User: "0:0", WorkingDir: "/data",
		Command:     domain.StartupCommand{Executable: "/image/scripts/start", Args: []string{}},
		Environment: map[string]string{"TYPE": "PAPER", "VERSION": "1.21.8", "PAPER_BUILD": "60", "EULA": "TRUE", "MEMORY": "{{ memory_mb }}M"},
		DataMounts:  []domain.RuntimeDataMount{{Name: "server-data", Target: "/data", Backup: true}},
		Ports:       []domain.RuntimePort{{Name: "game", Protocol: "tcp", ContainerPort: 25565, Role: "primary"}},
		Stop:        domain.RuntimeStop{Method: "console", Value: "stop", TimeoutSeconds: 30},
		Health:      domain.RuntimeHealth{Type: "tcp", PortRef: "game", IntervalSeconds: 10, TimeoutSeconds: 5, FailureThreshold: 6},
		Console:     &domain.RuntimeConsoleAdapter{Adapter: minecraftRCONAdapter, Port: 25575},
	}
	target.Digest, _ = runtimeTargetDigest(target)
	targetJSON, _ := json.Marshal(target)
	return &agentv1.ProvisionTaskPayload{
		GameDefinitionId:  "io.gugumanager.papermc",
		BundleDigest:      "sha256:a0118b857dacc2ffd27a56bcdd9cdfcd27f699a5d55ca424bffc447b0572fbfa",
		RuntimeTargetJson: string(targetJSON),
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
			"memory_mb": "2048",
		},
		StartAfterProvision: true,
	}
}

func reconcileTask(serverID string, generation uint64, desiredDigest string) *agentv1.Task {
	provision := provisionPayload()
	return &agentv1.Task{
		OperationId: "op-reconcile", ServerId: serverID, Generation: generation, Type: "reconcile",
		BundleDigest: provision.GetBundleDigest(),
		Payload: &agentv1.Task_Reconcile{Reconcile: &agentv1.ReconcileTaskPayload{Desired: &agentv1.DesiredRuntimeSpec{
			GameDefinitionId: provision.GetGameDefinitionId(), BundleDigest: provision.GetBundleDigest(),
			RuntimeTargetJson: provision.GetRuntimeTargetJson(), ResourceLimits: provision.GetResourceLimits(),
			Allocations: provision.GetAllocations(), Variables: provision.GetVariables(), DesiredRunning: true,
			Digest: desiredDigest, Generation: generation,
		}}},
	}
}

func TestDockerExecutorReconcileNoOpForMatchingDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("d", 64)
	fd := newFakeDocker()
	fd.env["GUGU_DESIRED_DIGEST"] = digest
	exec := &DockerExecutor{dataRoot: t.TempDir(), rt: fd}

	outcome, err := exec.ExecuteTask(context.Background(), reconcileTask("server-noop", 7, digest))
	if err != nil || !outcome.Succeeded {
		t.Fatalf("reconcile no-op = %+v, %v", outcome, err)
	}
	if fd.createdCount() != 0 {
		t.Fatalf("matching desired digest created %d candidates", fd.createdCount())
	}
	if outcome.Observed == nil || outcome.Observed.GetObservedGeneration() != 7 {
		t.Fatalf("observed = %+v, want generation 7", outcome.Observed)
	}
	if !strings.Contains(string(outcome.ResultJSON), `"noOp":true`) {
		t.Fatalf("result = %s, want noOp", outcome.ResultJSON)
	}
}

func TestDockerExecutorReconcileHealthySwitch(t *testing.T) {
	digest := "sha256:" + strings.Repeat("e", 64)
	fd := newFakeDocker()
	fd.status.ID = "old-container"
	fd.createID = "candidate-container"
	exec := &DockerExecutor{dataRoot: t.TempDir(), rt: fd}

	outcome, err := exec.ExecuteTask(context.Background(), reconcileTask("server-switch", 8, digest))
	if err != nil || !outcome.Succeeded {
		t.Fatalf("reconcile switch = %+v, %v", outcome, err)
	}
	if fd.createdCount() != 1 {
		t.Fatalf("created candidates = %d, want 1", fd.createdCount())
	}
	cfg := fd.lastCreated()
	if cfg.StartOnCreate == nil || *cfg.StartOnCreate || cfg.Env["GUGU_DESIRED_DIGEST"] != digest {
		t.Fatalf("candidate was not prepared stopped with desired identity: %+v", cfg)
	}
	fd.mu.Lock()
	renamed := append([]string(nil), fd.renamed...)
	removed := append([]string(nil), fd.removed...)
	fd.mu.Unlock()
	if len(renamed) != 2 || renamed[1] != "candidate-container->gugu-server-server-switch" {
		t.Fatalf("rename sequence = %v", renamed)
	}
	if len(removed) != 1 || removed[0] != "old-container" {
		t.Fatalf("removed = %v, want old generation", removed)
	}
	if outcome.Observed.GetObservedGeneration() != 8 || outcome.Observed.GetBundleDigest() != provisionPayload().GetBundleDigest() {
		t.Fatalf("observed = %+v", outcome.Observed)
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
	if cfg.Image != paperMCRuntimeImage {
		t.Errorf("image = %q, want pinned PaperMC image", cfg.Image)
	}
	if cfg.Env["EULA"] != "TRUE" {
		t.Errorf("env EULA = %q, want TRUE", cfg.Env["EULA"])
	}
	if cfg.Env["MEMORY"] != "2048M" || cfg.Env["TYPE"] != "PAPER" {
		t.Errorf("runtime environment not rendered: %+v", cfg.Env)
	}
	if cfg.PortBindings[25565] != 30000 {
		t.Errorf("port bindings = %+v, want 25565->30000", cfg.PortBindings)
	}
	if cfg.VolumeName != "gugu-server-server-1-data" || cfg.VolumeTarget != "/data" {
		t.Errorf("named volume = %q:%q", cfg.VolumeName, cfg.VolumeTarget)
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

func TestDockerExecutorProvisionRejectsTamperedRuntimeTarget(t *testing.T) {
	fd := newFakeDocker()
	exec := &DockerExecutor{dataRoot: t.TempDir(), rt: fd}
	payload := provisionPayload()
	var target map[string]any
	if err := json.Unmarshal([]byte(payload.RuntimeTargetJson), &target); err != nil {
		t.Fatal(err)
	}
	target["image"] = "attacker.invalid/server@sha256:" + strings.Repeat("a", 64)
	tampered, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	payload.RuntimeTargetJson = string(tampered)

	outcome, err := exec.ExecuteTask(context.Background(), &agentv1.Task{
		OperationId: "op-tampered-runtime", ServerId: "server-1", Type: "provision",
		Payload: &agentv1.Task_Provision{Provision: payload},
	})
	if err != nil || outcome.Succeeded || outcome.ErrorCode != "RUNTIME_TARGET_UNTRUSTED" || outcome.Retryable {
		t.Fatalf("tampered target outcome = %+v, err=%v", outcome, err)
	}
	if cfg := fd.lastCreated(); cfg.Name != "" {
		t.Fatalf("tampered target created container: %+v", cfg)
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

func TestDockerExecutorProvisionProtectsInternalConsoleAdapter(t *testing.T) {
	fd := newFakeDocker()
	exec := &DockerExecutor{dataRoot: t.TempDir(), rt: fd}
	payload := provisionPayload()
	payload.Variables["GUGU_CONSOLE_ADAPTER"] = "attacker-selected/v1"
	payload.Variables["RCON_PASSWORD"] = "attacker-password"
	task := &agentv1.Task{
		OperationId: "op-prov-console-adapter", ServerId: "server-1", Type: "provision",
		Payload: &agentv1.Task_Provision{Provision: payload},
	}
	outcome, err := exec.ExecuteTask(context.Background(), task)
	if err != nil || !outcome.Succeeded {
		t.Fatalf("provision = %+v, %v", outcome, err)
	}
	cfg := fd.lastCreated()
	if cfg.Env["GUGU_CONSOLE_ADAPTER"] != minecraftRCONAdapter {
		t.Fatalf("console adapter = %q, want trusted %q", cfg.Env["GUGU_CONSOLE_ADAPTER"], minecraftRCONAdapter)
	}
	if cfg.Env["RCON_PASSWORD"] == "attacker-password" || cfg.Env["RCON_PASSWORD"] == "" {
		t.Fatalf("internal RCON credential was overridden: %q", cfg.Env["RCON_PASSWORD"])
	}
}

func TestDockerExecutorProvisionRejectsUnsupportedGame(t *testing.T) {
	fd := newFakeDocker()
	exec := &DockerExecutor{dataRoot: t.TempDir(), rt: fd}
	payload := provisionPayload()
	payload.GameDefinitionId = "io.gugumanager.velocity"
	payload.Variables["GUGU_CONSOLE_ADAPTER"] = minecraftRCONAdapter
	payload.Variables["RCON_PASSWORD"] = "user-password"
	task := &agentv1.Task{
		OperationId: "op-prov-no-console", ServerId: "server-velocity", Type: "provision",
		Payload: &agentv1.Task_Provision{Provision: payload},
	}
	outcome, err := exec.ExecuteTask(context.Background(), task)
	if err != nil || outcome.Succeeded || outcome.ErrorCode != "RUNTIME_TARGET_UNTRUSTED" || outcome.Retryable {
		t.Fatalf("unsupported runtime target = %+v, %v", outcome, err)
	}
	if cfg := fd.lastCreated(); cfg.Name != "" {
		t.Fatalf("unsupported game created a container: %+v", cfg)
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
	killed := append([]string(nil), fd.killed...)
	removed := len(fd.removed)
	fd.mu.Unlock()
	if len(killed) != 1 || killed[0] != "gugu-server-server-1" {
		t.Errorf("killed = %v, want [gugu-server-server-1]", killed)
	}
	if removed != 0 {
		t.Errorf("remove calls = %d, want 0: kill must preserve the runtime", removed)
	}
	if outcome.Observed.GetObservedPower() != agentv1.ObservedPower_OBSERVED_POWER_STOPPED {
		t.Errorf("observed power = %v, want STOPPED", outcome.Observed.GetObservedPower())
	}
}

func TestDockerExecutorExtensionRequiresConfiguredRunner(t *testing.T) {
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
	if outcome.ErrorCode != "EXTENSION_UNSUPPORTED" {
		t.Errorf("error code = %q, want EXTENSION_UNSUPPORTED", outcome.ErrorCode)
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
	fd.env["GUGU_CONSOLE_ADAPTER"] = minecraftRCONAdapter
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

func TestExecuteConsoleCommandRejectsMissingAdapterWithoutShellFallback(t *testing.T) {
	fd := newFakeDocker()
	fd.env["RCON_PASSWORD"] = "user-variable-is-not-an-adapter"
	exec := &DockerExecutor{dataRoot: t.TempDir(), rt: fd}

	outcome, err := exec.ExecuteConsoleCommand(context.Background(), "server-1", "echo hello")
	if err != nil {
		t.Fatalf("execute console command: %v", err)
	}
	if outcome.Succeeded || outcome.ErrorCode != "CONSOLE_UNSUPPORTED" || outcome.Retryable {
		t.Fatalf("outcome = %+v, want CONSOLE_UNSUPPORTED non-retryable", outcome)
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if len(fd.execArgv) != 0 {
		t.Fatalf("exec calls = %d, want 0 without an explicit console adapter", len(fd.execArgv))
	}
}

func TestExecuteConsoleCommandConfiguredAdapterMissingPasswordIsCommandFailure(t *testing.T) {
	fd := newFakeDocker()
	fd.env["GUGU_CONSOLE_ADAPTER"] = minecraftRCONAdapter
	exec := &DockerExecutor{dataRoot: t.TempDir(), rt: fd}

	outcome, err := exec.ExecuteConsoleCommand(context.Background(), "server-1", "list")
	if err != nil {
		t.Fatalf("execute console command: %v", err)
	}
	if outcome.Succeeded || outcome.ErrorCode != "COMMAND_FAILED" || outcome.Retryable {
		t.Fatalf("outcome = %+v, want COMMAND_FAILED non-retryable", outcome)
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if len(fd.execArgv) != 0 {
		t.Fatalf("exec calls = %d, want 0 with missing RCON credential", len(fd.execArgv))
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
	fd.env["GUGU_CONSOLE_ADAPTER"] = minecraftRCONAdapter
	fd.env["RCON_PASSWORD"] = "configured-secret"
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

func TestExecuteConsoleCommandAdapterInspectionFailsClosed(t *testing.T) {
	fd := newFakeDocker()
	fd.envErr = errors.New("inspect failed")
	exec := &DockerExecutor{dataRoot: t.TempDir(), rt: fd}
	outcome, err := exec.ExecuteConsoleCommand(context.Background(), "server-1", "list")
	if err != nil {
		t.Fatalf("execute console command: %v", err)
	}
	if outcome.Succeeded || outcome.ErrorCode != "COMMAND_FAILED" || !outcome.Retryable {
		t.Errorf("outcome = %+v, want retryable COMMAND_FAILED", outcome)
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if len(fd.execArgv) != 0 {
		t.Fatalf("exec calls = %d, want 0 when adapter inspection failed", len(fd.execArgv))
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
		BackupID        string `json:"backupId"`
		Checksum        string `json:"checksum"`
		ManifestDigest  string `json:"manifestDigest"`
		SizeBytes       int64  `json:"sizeBytes"`
		StorageLocation string `json:"storageLocation"`
	}
	if err := json.Unmarshal(outcome.ResultJSON, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.StorageLocation != "backups/b-1.tar.gz" {
		t.Errorf("storage location = %q, want backups/b-1.tar.gz", result.StorageLocation)
	}
	if result.BackupID != "b-1" {
		t.Errorf("backup id = %q, want b-1", result.BackupID)
	}
	if !strings.HasPrefix(result.Checksum, "sha256:") {
		t.Errorf("checksum = %q, want sha256: prefix", result.Checksum)
	}
	if !strings.HasPrefix(result.ManifestDigest, "sha256:") {
		t.Errorf("manifest digest = %q, want sha256: prefix", result.ManifestDigest)
	}
	archive := filepath.Join(dir, "backups", "b-1.tar.gz")
	if _, err := os.Stat(archive); err != nil {
		t.Errorf("archive %s not created: %v", archive, err)
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if len(fd.execArgv) != 2 || !strings.Contains(strings.Join(fd.execArgv[0], " "), "tar -czf") || strings.Join(fd.execArgv[1], " ") != "rm -f /tmp/b-1.tar.gz" {
		t.Errorf("expected in-container tar exec, got %v", fd.execArgv)
	}
}

func TestExecuteRestoreBackupTask(t *testing.T) {
	dir := t.TempDir()
	// 预置归档和当前数据目录，验证恢复不会先破坏旧恢复点。
	if err := os.MkdirAll(filepath.Join(dir, "backups"), 0o755); err != nil {
		t.Fatalf("mkdir backups: %v", err)
	}
	archive, err := fakeBackupPayload()
	if err != nil {
		t.Fatalf("build archive: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "backups", "b-1.tar.gz"), archive, 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	dataDir := filepath.Join(dir, "srv-1")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir current data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "old.txt"), []byte("old-data"), 0o644); err != nil {
		t.Fatalf("write current data: %v", err)
	}
	fd := newFakeDocker()
	fd.status = runtime.ContainerStatus{ID: "cont-abc", State: "exited", Status: "exited", Running: false}
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
	if _, err := os.Stat(filepath.Join(dataDir, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("old data still present after restore, stat err = %v", err)
	}
	if restored, err := os.ReadFile(filepath.Join(dataDir, "world", "level.dat")); err != nil || string(restored) != "level-data" {
		t.Fatalf("restored data = %q, err=%v", restored, err)
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if len(fd.copiesTo) != 0 || len(fd.started) != 0 || len(fd.execArgv) != 0 {
		t.Fatalf("host restore unexpectedly touched container: copies=%v started=%v exec=%v", fd.copiesTo, fd.started, fd.execArgv)
	}
}

func TestExecuteRestoreBackupRejectsContentDigestMismatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "backups"), 0o755); err != nil {
		t.Fatalf("mkdir backups: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "backups", "b-2.tar.gz"), []byte("archive-bytes"), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	fd := newFakeDocker()
	fd.status = runtime.ContainerStatus{ID: "cont-abc", State: "exited", Status: "exited", Running: false}
	exec := &DockerExecutor{dataRoot: dir, rt: fd}
	payload, err := protojson.Marshal(&agentv1.BackupTaskPayload{Action: &agentv1.BackupTaskPayload_Restore{
		Restore: &agentv1.RestoreBackupPayload{BackupId: "b-2", StorageObjectKey: "backups/b-2.tar.gz", ExpectedContentDigest: "sha256:" + strings.Repeat("0", 64)},
	}})
	if err != nil {
		t.Fatalf("marshal restore payload: %v", err)
	}
	task := &agentv1.Task{OperationId: "op-r-digest", ServerId: "srv-1", Type: "backup", Attempt: 1, Payload: &agentv1.Task_PayloadJson{PayloadJson: payload}}
	outcome, err := exec.ExecuteTask(context.Background(), task)
	if err != nil {
		t.Fatalf("execute restore: %v", err)
	}
	if outcome.Succeeded || outcome.ErrorCode != "BACKUP_INTEGRITY_FAILED" {
		t.Fatalf("digest mismatch outcome = %+v, want non-retryable integrity failure", outcome)
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if len(fd.started) != 0 || len(fd.copiesTo) != 0 {
		t.Fatalf("digest failure touched container: started=%v copies=%v", fd.started, fd.copiesTo)
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
