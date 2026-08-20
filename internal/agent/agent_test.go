package agent

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/gugumanager/gugumanager/api/proto/gugumanager/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type concurrentSendProbe struct {
	mu        sync.Mutex
	active    int
	maxActive int
	calls     int
}

func (p *concurrentSendProbe) Send(*agentv1.ConnectRequest) error {
	p.mu.Lock()
	p.active++
	p.calls++
	if p.active > p.maxActive {
		p.maxActive = p.active
	}
	p.mu.Unlock()

	time.Sleep(time.Millisecond)

	p.mu.Lock()
	p.active--
	p.mu.Unlock()
	return nil
}

func TestSerializedConnectSenderNeverCallsUnderlyingConcurrently(t *testing.T) {
	probe := &concurrentSendProbe{}
	outbound := &serializedConnectSender{stream: probe}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := outbound.Send(&agentv1.ConnectRequest{}); err != nil {
				t.Errorf("send: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.calls != 32 {
		t.Fatalf("underlying sends = %d, want 32", probe.calls)
	}
	if probe.maxActive != 1 {
		t.Fatalf("maximum concurrent underlying sends = %d, want 1", probe.maxActive)
	}
}

// Keep every Agent Connect Send behind serializedConnectSender. The only raw
// Send allowed in agent.go is the wrapper's call to its underlying stream;
// operational paths must send through the parameter named outbound.
func TestAgentConnectSendPathsUseSerializedSender(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "agent.go", nil, 0)
	if err != nil {
		t.Fatalf("parse agent.go: %v", err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Send" {
			return true
		}
		allowed := false
		switch receiver := selector.X.(type) {
		case *ast.Ident:
			allowed = receiver.Name == "outbound"
		case *ast.SelectorExpr:
			base, ok := receiver.X.(*ast.Ident)
			allowed = ok && base.Name == "s" && receiver.Sel.Name == "stream"
		}
		if !allowed {
			t.Errorf("raw Connect Send at %s; route it through serialized outbound", fset.Position(call.Pos()))
		}
		return true
	})
}

func TestDefaultCapabilitiesDeclareOnlyImplementedRuntimeAndPlatform(t *testing.T) {
	capabilities := defaultCapabilities()
	want := map[string]string{
		"runtime.container": "1",
		"server.reconcile":  "1",
		"platform." + runtime.GOOS + "." + runtime.GOARCH: "1",
	}
	if len(capabilities) != len(want) {
		t.Fatalf("capabilities = %+v, want exactly runtime and platform", capabilities)
	}
	for _, capability := range capabilities {
		if capability == nil {
			t.Fatal("nil capability")
		}
		version, ok := want[capability.GetName()]
		if !ok || capability.GetVersion() != version {
			t.Fatalf("unexpected capability %q version %q", capability.GetName(), capability.GetVersion())
		}
	}
}

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
	onConnect         func(stream agentv1.AgentGatewayService_ConnectServer) error
	closeAfterWelcome bool
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
	if f.closeAfterWelcome {
		return nil
	}

	f.mu.Lock()
	cb := f.onConnect
	f.mu.Unlock()
	if cb != nil {
		// 异步执行：允许回调等待 Agent 回发的结果再下发后续帧。
		go func() {
			if err := cb(stream); err != nil {
				return
			}
		}()
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
// 预置 CA → Enroll（含 CSR）→ 保存证书 → 建立 Connect → Hello → Welcome → 心跳。
// 000010 起注册必须基于预置信任根校验服务器证书，禁止 InsecureSkipVerify。
func TestAgentEnrollsAndConnects(t *testing.T) {
	ca := newTestCA(t)
	cp := &fakeControlPlane{ca: ca}
	dialer, serverName := startFakeServer(t, cp)

	dir := t.TempDir()
	cfg := testConfig()
	cfg.DataRoot = dir
	cfg.CertDir = filepath.Join(dir, "certs")
	cfg.TrustRootPath = filepath.Join(cfg.CertDir, "ca.crt")
	// 部署时由运维预置面板 CA 根证书；Agent 注册与连接都强制基于它校验。
	if err := os.MkdirAll(cfg.CertDir, 0o700); err != nil {
		t.Fatalf("create cert dir: %v", err)
	}
	if err := os.WriteFile(cfg.TrustRootPath, ca.rootPEM(), 0o644); err != nil {
		t.Fatalf("pre-provision trust root: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, cfg, discardLogger(), runOptions{dialer: dialer, serverName: serverName, executor: &fakeExecutor{}})
	}()

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

	// 证书与信任根应已持久化。Enroll 计数在服务端返回响应前递增，
	// 而写文件发生在 Agent 收到响应之后，这里轮询等待避免时序竞态。
	for _, p := range []string{filepath.Join(cfg.CertDir, "agent.crt"), filepath.Join(cfg.CertDir, "agent.key"), cfg.TrustRootPath} {
		path := p
		waitFor(t, "credential file "+path, 5*time.Second, func() bool {
			_, err := os.Stat(path)
			return err == nil
		})
	}

	// Connect 流：Hello → Welcome → 心跳。Hello 的 nodeID 必须是 Enroll
	// 返回的节点 ID（证书 CN 与之一致，服务端同时校验吊销状态）。
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

// TestAgentEnrollRequiresPreProvisionedTrustRoot 验证安全注册：没有预置
// 信任根时绝不降级为 InsecureSkipVerify，也不发出任何 Enroll 请求。
func TestAgentEnrollRequiresPreProvisionedTrustRoot(t *testing.T) {
	ca := newTestCA(t)
	cp := &fakeControlPlane{ca: ca}
	dialer, serverName := startFakeServer(t, cp)

	dir := t.TempDir()
	cfg := testConfig()
	cfg.DataRoot = dir
	cfg.CertDir = filepath.Join(dir, "certs")
	cfg.TrustRootPath = filepath.Join(cfg.CertDir, "ca.crt")
	// 故意不预置 CA。

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, cfg, discardLogger(), runOptions{dialer: dialer, serverName: serverName, executor: &fakeExecutor{}})
	}()

	time.Sleep(500 * time.Millisecond)
	if cp.enrollCount() != 0 {
		t.Fatalf("enroll called %d times without a trust root", cp.enrollCount())
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

// TestAgentFingerprintPinRejectsMismatch 验证指纹钉扎：CA 校验通过但指纹
// 不匹配时握手失败，Enroll 请求不会到达服务端。
func TestAgentFingerprintPinRejectsMismatch(t *testing.T) {
	ca := newTestCA(t)
	cp := &fakeControlPlane{ca: ca}
	dialer, serverName := startFakeServer(t, cp)

	dir := t.TempDir()
	cfg := testConfig()
	cfg.DataRoot = dir
	cfg.CertDir = filepath.Join(dir, "certs")
	cfg.TrustRootPath = filepath.Join(cfg.CertDir, "ca.crt")
	if err := os.MkdirAll(cfg.CertDir, 0o700); err != nil {
		t.Fatalf("create cert dir: %v", err)
	}
	if err := os.WriteFile(cfg.TrustRootPath, ca.rootPEM(), 0o644); err != nil {
		t.Fatalf("pre-provision trust root: %v", err)
	}
	cfg.CAFingerprint = strings.Repeat("ab", 32) // 与真实链不符的指纹

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, cfg, discardLogger(), runOptions{dialer: dialer, serverName: serverName, executor: &fakeExecutor{}})
	}()

	time.Sleep(500 * time.Millisecond)
	if cp.enrollCount() != 0 {
		t.Fatalf("enroll called %d times despite fingerprint mismatch", cp.enrollCount())
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

func TestAgentFingerprintPinAcceptsVerifiedRoot(t *testing.T) {
	ca := newTestCA(t)
	sum := sha256.Sum256(ca.cert.Raw)
	verify := verifyServerFingerprint(hex.EncodeToString(sum[:]))

	if err := verify(nil, [][]*x509.Certificate{{ca.cert}}); err != nil {
		t.Fatalf("verify root fingerprint: %v", err)
	}
}

// TestAgentExecutesPowerTask 验证：fake Control Plane 在 Connect 流上下发
// power start 任务 → Agent 注入的 fake executor 被执行 → 回 TaskAck + TaskResult。
func TestAgentExecutesPowerTask(t *testing.T) {
	ca := newTestCA(t)
	cp := &fakeControlPlane{ca: ca}
	cp.onConnect = func(stream agentv1.AgentGatewayService_ConnectServer) error {
		return stream.Send(&agentv1.ConnectResponse{Payload: &agentv1.ConnectResponse_Task{Task: &agentv1.Task{
			OperationId:     "op-1",
			ServerId:        "server-1",
			Generation:      1,
			Type:            "power",
			Attempt:         1,
			LeaseToken:      "lease-op-1",
			ConnectionEpoch: 4,
			Payload:         &agentv1.Task_Power{Power: &agentv1.PowerTaskPayload{Action: agentv1.PowerAction_POWER_ACTION_START}},
		}}})
	}
	dialer, serverName := startFakeServer(t, cp)

	dir := t.TempDir()
	cfg := testConfig()
	cfg.DataRoot = dir
	cfg.CertDir = filepath.Join(dir, "certs")
	cfg.TrustRootPath = filepath.Join(cfg.CertDir, "ca.crt")
	if err := os.MkdirAll(cfg.CertDir, 0o700); err != nil {
		t.Fatalf("create cert dir: %v", err)
	}
	if err := os.WriteFile(cfg.TrustRootPath, ca.rootPEM(), 0o644); err != nil {
		t.Fatalf("pre-provision trust root: %v", err)
	}

	exec := &fakeExecutor{}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, cfg, discardLogger(), runOptions{dialer: dialer, serverName: serverName, executor: exec})
	}()

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
	// 000009 栅栏：Agent 必须在 Ack/Result 中原样回显租约凭据与连接 epoch。
	if ack.GetLeaseToken() != "lease-op-1" || ack.GetConnectionEpoch() != 4 {
		t.Errorf("task ack did not echo the lease fence: %+v", ack)
	}

	waitFor(t, "task result", 10*time.Second, func() bool { return cp.taskResultCount() == 1 })
	result := cp.lastResult()
	if result.GetOperationId() != "op-1" || !result.GetSucceeded() {
		t.Errorf("task result wrong: %+v", result)
	}
	if result.GetAttempt() != 1 {
		t.Errorf("task result attempt = %d, want 1", result.GetAttempt())
	}
	if result.GetLeaseToken() != "lease-op-1" || result.GetConnectionEpoch() != 4 {
		t.Errorf("task result did not echo the lease fence: %+v", result)
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
	t.Setenv("GUGU_AGENT_CA_FINGERPRINT", "aa:bb:cc")
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
	if cfg.CAFingerprint != "aa:bb:cc" {
		t.Errorf("ca fingerprint = %q, want aa:bb:cc", cfg.CAFingerprint)
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

// ---------------------------------------------------------------------------
// Operation Journal 集成：重投重放、digest 拒绝、每服务器串行
// ---------------------------------------------------------------------------

func fencedPowerTask(opID, serverID string, action agentv1.PowerAction) *agentv1.Task {
	return &agentv1.Task{
		OperationId: opID, ServerId: serverID, Generation: 2, Type: "power",
		Attempt: 1, LeaseToken: "lease-" + opID, ConnectionEpoch: 7,
		Payload: &agentv1.Task_Power{Power: &agentv1.PowerTaskPayload{Action: action}},
	}
}

func testAgentConfig(t *testing.T, ca *testCA) Config {
	t.Helper()
	dir := t.TempDir()
	cfg := testConfig()
	cfg.DataRoot = dir
	cfg.CertDir = filepath.Join(dir, "certs")
	cfg.TrustRootPath = filepath.Join(cfg.CertDir, "ca.crt")
	if err := os.MkdirAll(cfg.CertDir, 0o700); err != nil {
		t.Fatalf("create cert dir: %v", err)
	}
	if err := os.WriteFile(cfg.TrustRootPath, ca.rootPEM(), 0o644); err != nil {
		t.Fatalf("pre-provision trust root: %v", err)
	}
	return cfg
}

func startAgentRun(t *testing.T, cfg Config, exec TaskExecutor, cp *fakeControlPlane) (cancel context.CancelFunc, errCh <-chan error) {
	t.Helper()
	dialer, serverName := startFakeServer(t, cp)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	err := make(chan error, 1)
	go func() {
		err <- run(ctx, cfg, discardLogger(), runOptions{dialer: dialer, serverName: serverName, executor: exec})
	}()
	return cancel, err
}

type recordingConnectSender struct {
	mu       sync.Mutex
	requests []*agentv1.ConnectRequest
}

func (s *recordingConnectSender) Send(request *agentv1.ConnectRequest) error {
	s.mu.Lock()
	s.requests = append(s.requests, request)
	s.mu.Unlock()
	return nil
}

func (s *recordingConnectSender) taskResults() []*agentv1.TaskResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	var results []*agentv1.TaskResult
	for _, request := range s.requests {
		if result := request.GetTaskResult(); result != nil {
			results = append(results, result)
		}
	}
	return results
}

func TestInflightExecutionBroadcastsResultToAllWaiters(t *testing.T) {
	execution := &inflightExecution{done: make(chan struct{})}
	results := make(chan inflightResult, 3)
	for range 3 {
		go func() {
			<-execution.done
			results <- execution.getResult()
		}()
	}
	execution.complete(inflightResult{succeeded: true, errorCode: "", resultJSON: []byte(`{"ok":true}`)})
	for range 3 {
		select {
		case result := <-results:
			if !result.succeeded || string(result.resultJSON) != `{"ok":true}` {
				t.Fatalf("broadcast result = %+v", result)
			}
		case <-time.After(time.Second):
			t.Fatal("in-flight waiter did not receive the broadcast result")
		}
	}
}

func TestAgentMarksOrphanedRunningJournalEntryFailed(t *testing.T) {
	journal, err := OpenOperationJournal(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	defer journal.Close()
	task := fencedPowerTask("op-orphaned-1", "server-1", agentv1.PowerAction_POWER_ACTION_START)
	digest := taskPayloadDigest(task)
	if err := journal.RecordRunning(task.GetOperationId(), digest, task.GetAttempt()); err != nil {
		t.Fatalf("record running: %v", err)
	}
	sender := &recordingConnectSender{}
	a := &agent{journal: journal, logger: discardLogger(), inflight: make(map[string]*inflightExecution)}
	a.awaitInflight(context.Background(), sender, task, digest)

	results := sender.taskResults()
	if len(results) != 1 || results[0].GetSucceeded() || results[0].GetRetryable() || results[0].GetErrorCode() != "AGENT_RESTARTED_DURING_OPERATION" {
		t.Fatalf("orphaned operation result = %+v", results)
	}
	entry, ok, err := journal.Lookup(task.GetOperationId())
	if err != nil || !ok {
		t.Fatalf("lookup recovered entry: ok=%t err=%v", ok, err)
	}
	if entry.Status != "failed" || entry.Checkpoint != "failed" || entry.ErrorCode != "AGENT_RESTARTED_DURING_OPERATION" || entry.Retryable {
		t.Fatalf("recovered journal entry = %+v", entry)
	}
}

func TestAgentReconnectsAfterConnectStreamEnds(t *testing.T) {
	ca := newTestCA(t)
	cp := &fakeControlPlane{ca: ca, closeAfterWelcome: true}
	dialer, serverName := startFakeServer(t, cp)
	cfg := testAgentConfig(t, ca)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, cfg, discardLogger(), runOptions{dialer: dialer, serverName: serverName, executor: &fakeExecutor{}})
	}()
	waitFor(t, "second connect session", 9*time.Second, func() bool {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		return len(cp.hellos) >= 2
	})
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not stop after reconnect test cancellation")
	}
}

// TestAgentJournalReplaysTerminalResultWithoutReexecuting 验证同 operation
// 重投：终态结果从操作日志重放，执行器只被调用一次。
func TestAgentJournalReplaysTerminalResultWithoutReexecuting(t *testing.T) {
	ca := newTestCA(t)
	cp := &fakeControlPlane{ca: ca}
	task := fencedPowerTask("op-replay-1", "server-1", agentv1.PowerAction_POWER_ACTION_START)
	cp.onConnect = func(stream agentv1.AgentGatewayService_ConnectServer) error {
		if err := stream.Send(&agentv1.ConnectResponse{Payload: &agentv1.ConnectResponse_Task{Task: task}}); err != nil {
			return err
		}
		deadline := time.Now().Add(10 * time.Second)
		for cp.taskResultCount() == 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		// 同一 operation 重投：应重放日志里的终态，而不是再次执行。
		return stream.Send(&agentv1.ConnectResponse{Payload: &agentv1.ConnectResponse_Task{Task: task}})
	}
	exec := &fakeExecutor{}
	cancel, errCh := startAgentRun(t, testAgentConfig(t, ca), exec, cp)
	defer cancel()

	waitFor(t, "replayed result", 15*time.Second, func() bool { return cp.taskResultCount() >= 2 })
	if exec.callCount() != 1 {
		t.Fatalf("executor calls = %d, want 1 (replayed, not re-executed)", exec.callCount())
	}
	cp.mu.Lock()
	second := cp.results[1]
	cp.mu.Unlock()
	if !second.GetSucceeded() {
		t.Errorf("replayed result not succeeded: %+v", second)
	}
	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("run returned error: %v", err)
	}
}

// TestAgentJournalRejectsDigestMismatch 验证同 operation 不同 payload：
// 拒绝执行（Ack 拒绝），不触碰执行器，也不覆盖已有终态。
func TestAgentJournalRejectsDigestMismatch(t *testing.T) {
	ca := newTestCA(t)
	cp := &fakeControlPlane{ca: ca}
	original := fencedPowerTask("op-mismatch-1", "server-1", agentv1.PowerAction_POWER_ACTION_START)
	changed := fencedPowerTask("op-mismatch-1", "server-1", agentv1.PowerAction_POWER_ACTION_STOP)
	cp.onConnect = func(stream agentv1.AgentGatewayService_ConnectServer) error {
		if err := stream.Send(&agentv1.ConnectResponse{Payload: &agentv1.ConnectResponse_Task{Task: original}}); err != nil {
			return err
		}
		deadline := time.Now().Add(10 * time.Second)
		for cp.taskResultCount() == 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		return stream.Send(&agentv1.ConnectResponse{Payload: &agentv1.ConnectResponse_Task{Task: changed}})
	}
	exec := &fakeExecutor{}
	cancel, errCh := startAgentRun(t, testAgentConfig(t, ca), exec, cp)
	defer cancel()

	waitFor(t, "reject ack", 15*time.Second, func() bool { return cp.taskAckCount() >= 2 })
	if exec.callCount() != 1 {
		t.Fatalf("executor calls = %d, want 1 (digest mismatch must not execute)", exec.callCount())
	}
	cp.mu.Lock()
	reject := cp.taskAcks[1]
	cp.mu.Unlock()
	if reject.GetAccepted() || reject.GetErrorCode() != "OPERATION_DIGEST_MISMATCH" {
		t.Errorf("expected OPERATION_DIGEST_MISMATCH rejection, got %+v", reject)
	}
	if cp.taskResultCount() != 1 {
		t.Errorf("results = %d, want exactly the original terminal result", cp.taskResultCount())
	}
	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("run returned error: %v", err)
	}
}

// gatedExecutor 记录开始顺序并可阻塞执行，用于验证每服务器串行。
type gatedExecutor struct {
	mu      sync.Mutex
	started []string
	release chan struct{}
}

func (e *gatedExecutor) ExecuteTask(ctx context.Context, task *agentv1.Task) (*ExecutionOutcome, error) {
	e.mu.Lock()
	e.started = append(e.started, task.GetOperationId())
	e.mu.Unlock()
	select {
	case <-e.release:
		return &ExecutionOutcome{Succeeded: true}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (e *gatedExecutor) ExecuteConsoleCommand(ctx context.Context, serverID, command string) (*ExecutionOutcome, error) {
	return &ExecutionOutcome{Succeeded: true}, nil
}

func (e *gatedExecutor) Runtime() (containerRuntime, error) {
	return nil, errors.New("gated executor has no runtime")
}

func (e *gatedExecutor) ListRunningServers(ctx context.Context) ([]string, error) {
	return nil, nil
}

func (e *gatedExecutor) startedOperations() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.started...)
}

// TestAgentSerializesTasksPerServer 验证每服务器互斥：同一服务器的两个任务
// 串行执行（第二个不早于第一个结束）；不同服务器的任务可并行。
func TestAgentSerializesTasksPerServer(t *testing.T) {
	ca := newTestCA(t)
	cp := &fakeControlPlane{ca: ca}
	first := fencedPowerTask("op-serial-1", "server-1", agentv1.PowerAction_POWER_ACTION_START)
	second := fencedPowerTask("op-serial-2", "server-1", agentv1.PowerAction_POWER_ACTION_STOP)
	parallel := fencedPowerTask("op-parallel-1", "server-2", agentv1.PowerAction_POWER_ACTION_START)
	cp.onConnect = func(stream agentv1.AgentGatewayService_ConnectServer) error {
		for _, task := range []*agentv1.Task{first, second, parallel} {
			if err := stream.Send(&agentv1.ConnectResponse{Payload: &agentv1.ConnectResponse_Task{Task: task}}); err != nil {
				return err
			}
		}
		return nil
	}
	exec := &gatedExecutor{release: make(chan struct{})}
	cancel, errCh := startAgentRun(t, testAgentConfig(t, ca), exec, cp)
	defer cancel()

	// server-1 只能有一个任务与 server-2 的任务并行开始；另一个
	// server-1 任务必须等前一个结束（每服务器互斥，不依赖 worker 调度顺序）。
	waitFor(t, "parallel start", 10*time.Second, func() bool {
		started := exec.startedOperations()
		serialStarted := 0
		if containsOp(started, "op-serial-1") {
			serialStarted++
		}
		if containsOp(started, "op-serial-2") {
			serialStarted++
		}
		return serialStarted == 1 && containsOp(started, "op-parallel-1")
	})
	if started := exec.startedOperations(); containsOp(started, "op-serial-1") && containsOp(started, "op-serial-2") {
		t.Fatalf("both tasks of the same server started concurrently: %v", started)
	}
	close(exec.release)
	waitFor(t, "serial tail", 10*time.Second, func() bool {
		started := exec.startedOperations()
		return containsOp(started, "op-serial-1") && containsOp(started, "op-serial-2")
	})
	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("run returned error: %v", err)
	}
}

func containsOp(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
