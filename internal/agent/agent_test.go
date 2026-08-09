package agent

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/gugumanager/gugumanager/api/proto/gugumanager/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ---------------------------------------------------------------------------
// 测试辅助：内存 CA（根证书 + 服务端证书 + 按 CSR 公钥签发客户端证书）
// ---------------------------------------------------------------------------

type testCA struct {
	cert *x509.Certificate
	key  *rsa.PrivateKey
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test ca key: %v", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "GuGuManager Test Root CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create test ca: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse test ca: %v", err)
	}
	return &testCA{cert: cert, key: key}
}

// signServerCert 签发 CN=control-plane 的 server auth 证书。
func (c *testCA) signServerCert() (certPEM, keyPEM []byte, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject:      pkix.Name{CommonName: "control-plane"},
		DNSNames:     []string{"control-plane"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM, nil
}

// signCSRCert 按 CSR 中的公钥签发 client auth 证书（CN 取自 CSR Subject）。
// 这是真实 Control Plane 的简化版：Enroll 必须返回与 Agent 私钥匹配的证书。
func (c *testCA) signCSRCert(csrPEM []byte) ([]byte, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, fmt.Errorf("no CERTIFICATE REQUEST block in csr")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse csr: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject:      csr.Subject,
		DNSNames:     []string{csr.Subject.CommonName},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, csr.PublicKey, c.key)
	if err != nil {
		return nil, fmt.Errorf("sign csr: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

func (c *testCA) rootPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.cert.Raw})
}

// ---------------------------------------------------------------------------
// fake Control Plane：内存实现 AgentGatewayServiceServer，经 bufconn 暴露
// ---------------------------------------------------------------------------

type fakeControlPlane struct {
	agentv1.UnimplementedAgentGatewayServiceServer
	ca *testCA

	mu         sync.Mutex
	enrolls    int
	lastCSR    []byte
	lastEnroll *agentv1.EnrollRequest
	hellos     []*agentv1.Hello
	heartbeats int
	taskAcks   []*agentv1.TaskAck
	results    []*agentv1.TaskResult
	observed   []*agentv1.ServerObserved

	// onConnect 在 fake 发送 Welcome 之后、进入 Recv 循环之前调用，
	// 测试可在此下发任务。
	onConnect func(stream agentv1.AgentGatewayService_ConnectServer) error
}

func (f *fakeControlPlane) Enroll(ctx context.Context, req *agentv1.EnrollRequest) (*agentv1.EnrollResponse, error) {
	f.mu.Lock()
	f.enrolls++
	f.lastCSR = req.GetCertificateSigningRequest()
	f.lastEnroll = req
	f.mu.Unlock()

	certPEM, err := f.ca.signCSRCert(req.GetCertificateSigningRequest())
	if err != nil {
		return nil, fmt.Errorf("fake enroll sign csr: %w", err)
	}
	return &agentv1.EnrollResponse{
		NodeId:           "node-" + req.GetNodeName(),
		CertificateChain: certPEM,
		CaCertificate:    f.ca.rootPEM(),
		ExpiresAt:        timestamppb.New(time.Now().Add(24 * time.Hour)),
	}, nil
}

func (f *fakeControlPlane) Connect(stream agentv1.AgentGatewayService_ConnectServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if hello := first.GetHello(); hello != nil {
		f.mu.Lock()
		f.hellos = append(f.hellos, hello)
		f.mu.Unlock()
	} else {
		return fmt.Errorf("fake connect: first frame is not hello")
	}

	if err := stream.Send(&agentv1.ConnectResponse{Payload: &agentv1.ConnectResponse_Welcome{Welcome: &agentv1.Welcome{
		ProtocolVersion:          "v1",
		HeartbeatIntervalSeconds: 1,
		MaxInFlightTasks:         3,
	}}}); err != nil {
		return err
	}

	f.mu.Lock()
	cb := f.onConnect
	f.mu.Unlock()
	if cb != nil {
		if err := cb(stream); err != nil {
			return err
		}
	}

	for {
		req, err := stream.Recv()
		if err != nil {
			return err
		}
		switch p := req.Payload.(type) {
		case *agentv1.ConnectRequest_Heartbeat:
			f.mu.Lock()
			f.heartbeats++
			f.mu.Unlock()
		case *agentv1.ConnectRequest_TaskAck:
			f.mu.Lock()
			f.taskAcks = append(f.taskAcks, p.TaskAck)
			f.mu.Unlock()
		case *agentv1.ConnectRequest_TaskResult:
			f.mu.Lock()
			f.results = append(f.results, p.TaskResult)
			f.mu.Unlock()
		case *agentv1.ConnectRequest_ServerObserved:
			f.mu.Lock()
			f.observed = append(f.observed, p.ServerObserved)
			f.mu.Unlock()
		}
	}
}

func (f *fakeControlPlane) enrollCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enrolls
}

func (f *fakeControlPlane) heartbeatCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.heartbeats
}

func (f *fakeControlPlane) taskAckCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.taskAcks)
}

func (f *fakeControlPlane) taskResultCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.results)
}

func (f *fakeControlPlane) lastResult() *agentv1.TaskResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.results) == 0 {
		return nil
	}
	return f.results[len(f.results)-1]
}

// startFakeServer 启动带 TLS 的 bufconn gRPC server（不要求客户端证书，
// Agent 侧仍以真实 mTLS 客户端身份连接），返回 dialer 与 serverName。
func startFakeServer(t *testing.T, cp *fakeControlPlane) (dialer func(context.Context, string) (net.Conn, error), serverName string) {
	t.Helper()
	serverCert, serverKey, err := cp.ca.signServerCert()
	if err != nil {
		t.Fatalf("sign server cert: %v", err)
	}
	pair, err := tls.X509KeyPair(serverCert, serverKey)
	if err != nil {
		t.Fatalf("server key pair: %v", err)
	}
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{pair},
		MinVersion:   tls.VersionTLS12,
	})))
	agentv1.RegisterAgentGatewayServiceServer(gs, cp)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	dialer = func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.Dial()
	}
	return dialer, "control-plane"
}

// ---------------------------------------------------------------------------
// fake executor
// ---------------------------------------------------------------------------

type fakeExecutor struct {
	mu    sync.Mutex
	calls int
	tasks []*agentv1.Task
}

func (f *fakeExecutor) ExecuteTask(ctx context.Context, task *agentv1.Task) (*ExecutionOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.tasks = append(f.tasks, task)
	return &ExecutionOutcome{Succeeded: true}, nil
}

func (f *fakeExecutor) ExecuteConsoleCommand(ctx context.Context, serverID, command string) (*ExecutionOutcome, error) {
	return &ExecutionOutcome{Succeeded: true, ResultJSON: []byte(`{"output":"fake echo"}`)}, nil
}

func (f *fakeExecutor) Runtime() (containerRuntime, error) {
	return nil, errors.New("fake executor has no runtime")
}

func (f *fakeExecutor) ListRunningServers(ctx context.Context) ([]string, error) {
	return nil, nil
}

func (f *fakeExecutor) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeExecutor) receivedTask() *agentv1.Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tasks) == 0 {
		return nil
	}
	return f.tasks[0]
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func testConfig() Config {
	return Config{
		PanelAddr:         "bufnet",
		RegistrationToken: "reg-token",
		NodeName:          "test-node",
		AgentVersion:      "0.1.0-test",
		DataRoot:          "",
		CertDir:           "",
		TrustRootPath:     "",
	}
}

// ---------------------------------------------------------------------------
// 测试用例
// ---------------------------------------------------------------------------

// TestAgentEnrollsAndConnects 验证完整生命周期：
// Enroll（含 CSR）→ 保存证书/信任根 → 建立 Connect → Hello → Welcome → 心跳。
func TestAgentEnrollsAndConnects(t *testing.T) {
	ca := newTestCA(t)
	cp := &fakeControlPlane{ca: ca}
	dialer, serverName := startFakeServer(t, cp)

	dir := t.TempDir()
	cfg := testConfig()
	cfg.DataRoot = dir
	cfg.CertDir = filepath.Join(dir, "certs")
	cfg.TrustRootPath = filepath.Join(cfg.CertDir, "ca.crt")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- run(ctx, cfg, discardLogger(), runOptions{dialer: dialer, serverName: serverName, executor: &fakeExecutor{}}) }()

	waitFor(t, "enroll", 10*time.Second, func() bool { return cp.enrollCount() == 1 })

	// Enroll 请求应携带 CSR 与节点信息
	cp.mu.Lock()
	lastEnroll := cp.lastEnroll
	hasCSR := len(cp.lastCSR) > 0
	cp.mu.Unlock()
	if !hasCSR {
		t.Fatal("expected a certificate signing request in enroll request")
	}
	if lastEnroll.GetNodeName() != "test-node" || lastEnroll.GetAgentVersion() != "0.1.0-test" {
		t.Errorf("enroll request fields wrong: %+v", lastEnroll)
	}
	if lastEnroll.GetRegistrationToken() != "reg-token" {
		t.Errorf("enroll registration token = %q, want reg-token", lastEnroll.GetRegistrationToken())
	}

	// 证书与信任根应已持久化
	for _, p := range []string{filepath.Join(cfg.CertDir, "agent.crt"), filepath.Join(cfg.CertDir, "agent.key"), cfg.TrustRootPath} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected credential file %s: %v", p, err)
		}
	}

	// Connect 流：Hello → Welcome → 心跳
	waitFor(t, "heartbeat", 10*time.Second, func() bool { return cp.heartbeatCount() >= 1 })
	cp.mu.Lock()
	helloCount := len(cp.hellos)
	hello := cp.hellos[0]
	cp.mu.Unlock()
	if helloCount != 1 {
		t.Errorf("hello frames = %d, want 1", helloCount)
	}
	if hello.GetNodeId() != "node-test-node" {
		t.Errorf("hello node id = %q, want node-test-node", hello.GetNodeId())
	}
	if hello.GetAgentVersion() != "0.1.0-test" {
		t.Errorf("hello agent version = %q", hello.GetAgentVersion())
	}

	// 取消后 Run 应正常退出
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not exit after cancel")
	}
}

// TestAgentExecutesPowerTask 验证：fake Control Plane 在 Connect 流上下发
// power start 任务 → Agent 注入的 fake executor 被执行 → 回 TaskAck + TaskResult。
func TestAgentExecutesPowerTask(t *testing.T) {
	ca := newTestCA(t)
	cp := &fakeControlPlane{ca: ca}
	cp.onConnect = func(stream agentv1.AgentGatewayService_ConnectServer) error {
		return stream.Send(&agentv1.ConnectResponse{Payload: &agentv1.ConnectResponse_Task{Task: &agentv1.Task{
			OperationId: "op-1",
			ServerId:    "server-1",
			Generation:  1,
			Type:        "power",
			Attempt:     1,
			Payload:     &agentv1.Task_Power{Power: &agentv1.PowerTaskPayload{Action: agentv1.PowerAction_POWER_ACTION_START}},
		}}})
	}
	dialer, serverName := startFakeServer(t, cp)

	dir := t.TempDir()
	cfg := testConfig()
	cfg.DataRoot = dir
	cfg.CertDir = filepath.Join(dir, "certs")
	cfg.TrustRootPath = filepath.Join(cfg.CertDir, "ca.crt")

	exec := &fakeExecutor{}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- run(ctx, cfg, discardLogger(), runOptions{dialer: dialer, serverName: serverName, executor: exec}) }()

	waitFor(t, "executor call", 10*time.Second, func() bool { return exec.callCount() == 1 })
	task := exec.receivedTask()
	if task == nil {
		t.Fatal("executor did not receive task")
	}
	if task.GetOperationId() != "op-1" || task.GetServerId() != "server-1" {
		t.Errorf("executor received wrong task: %+v", task)
	}
	if task.GetPower() == nil || task.GetPower().GetAction() != agentv1.PowerAction_POWER_ACTION_START {
		t.Errorf("expected power start task, got %+v", task.GetPower())
	}

	waitFor(t, "task ack", 10*time.Second, func() bool { return cp.taskAckCount() == 1 })
	cp.mu.Lock()
	ack := cp.taskAcks[0]
	cp.mu.Unlock()
	if ack.GetOperationId() != "op-1" || !ack.GetAccepted() {
		t.Errorf("task ack wrong: %+v", ack)
	}

	waitFor(t, "task result", 10*time.Second, func() bool { return cp.taskResultCount() == 1 })
	result := cp.lastResult()
	if result.GetOperationId() != "op-1" || !result.GetSucceeded() {
		t.Errorf("task result wrong: %+v", result)
	}
	if result.GetAttempt() != 1 {
		t.Errorf("task result attempt = %d, want 1", result.GetAttempt())
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not exit after cancel")
	}
}

// TestLoadConfig 验证环境变量读取与兼容回退。
func TestLoadConfig(t *testing.T) {
	t.Setenv("GUGU_AGENT_PANEL_ADDR", "10.0.0.5:8443")
	t.Setenv("GUGU_AGENT_REGISTRATION_TOKEN", "tok-1")
	t.Setenv("GUGU_AGENT_NODE_NAME", "node-env")
	t.Setenv("GUGU_AGENT_VERSION", "9.9.9")
	t.Setenv("GUGU_AGENT_DATA_ROOT", filepath.Join(t.TempDir(), "data"))
	t.Setenv("GUGU_AGENT_CERT_DIR", filepath.Join(t.TempDir(), "certs"))
	t.Setenv("GUGU_AGENT_TRUST_ROOT", filepath.Join(t.TempDir(), "trust.pem"))
	// 旧变量不应覆盖新的
	t.Setenv("GUGU_PANEL_URL", "http://127.0.0.1:9999")
	t.Setenv("GUGU_NODE_NAME", "old-node")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.PanelAddr != "10.0.0.5:8443" {
		t.Errorf("panel addr = %q", cfg.PanelAddr)
	}
	if cfg.RegistrationToken != "tok-1" {
		t.Errorf("registration token = %q", cfg.RegistrationToken)
	}
	if cfg.NodeName != "node-env" {
		t.Errorf("node name = %q", cfg.NodeName)
	}
	if cfg.AgentVersion != "9.9.9" {
		t.Errorf("agent version = %q", cfg.AgentVersion)
	}
}

// TestLoadConfigLegacyFallback 验证旧环境变量回退。
func TestLoadConfigLegacyFallback(t *testing.T) {
	t.Setenv("GUGU_PANEL_URL", "http://127.0.0.1:8080")
	t.Setenv("GUGU_NODE_NAME", "legacy-node")
	t.Setenv("GUGU_AGENT_TOKEN", "legacy-token")
	t.Setenv("GUGU_AGENT_PANEL_ADDR", "")
	t.Setenv("GUGU_AGENT_REGISTRATION_TOKEN", "")
	t.Setenv("GUGU_AGENT_NODE_NAME", "")
	t.Setenv("GUGU_AGENT_VERSION", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.PanelAddr != "127.0.0.1:8080" {
		t.Errorf("panel addr fallback = %q, want 127.0.0.1:8080", cfg.PanelAddr)
	}
	if cfg.NodeName != "legacy-node" {
		t.Errorf("node name fallback = %q", cfg.NodeName)
	}
	if cfg.RegistrationToken != "legacy-token" {
		t.Errorf("registration token fallback = %q", cfg.RegistrationToken)
	}
	if cfg.CertDir == "" || cfg.TrustRootPath == "" {
		t.Errorf("expected derived cert dir and trust root, got %q / %q", cfg.CertDir, cfg.TrustRootPath)
	}
}
