package agentrpc

import (
	"bytes"
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
	"net"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/gugumanager/gugumanager/api/proto/gugumanager/agent/v1"
	"github.com/gugumanager/gugumanager/internal/agentca"
	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeStore 是 TaskStore 接口的内存实现，用于集成测试。
type fakeStore struct {
	mu           sync.Mutex
	nodes        map[string]domain.Node
	nextNode     int
	heartbeats   []domain.Heartbeat
	observed     []domain.ServerObserved
	completed    []taskCompleted
	queued       []*store.ClaimedTask
	audits       []domain.AuditEvent
	consoleLines []domain.ConsoleLine
	metrics      []domain.ServerMetrics
}

type taskCompleted struct {
	OperationID string
	NodeID      string
	Succeeded   bool
	ErrorCode   *string
	ResultJSON  []byte
}

func newFakeStore() *fakeStore {
	return &fakeStore{nodes: map[string]domain.Node{}}
}

func (f *fakeStore) RegisterNode(ctx context.Context, node domain.Node) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextNode++
	nodeID := fmt.Sprintf("node-%d", f.nextNode)
	node.ID = nodeID
	f.nodes[nodeID] = node
	return nodeID, nil
}

func (f *fakeStore) NodeByID(ctx context.Context, nodeID string) (domain.Node, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	node, ok := f.nodes[nodeID]
	if !ok {
		return domain.Node{}, domain.NewProblem("NOT_FOUND", "节点不存在", false)
	}
	return node, nil
}

func (f *fakeStore) EnqueueTask(ctx context.Context, serverID, nodeID, taskType string, generation int64, actorID string, idemKey string, requestDigest []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextNode++
	operationID := fmt.Sprintf("task-%d", f.nextNode)
	f.queued = append(f.queued, &store.ClaimedTask{
		OperationID: operationID,
		ServerID:    serverID,
		NodeID:      nodeID,
		Generation:  generation,
		TaskType:    taskType,
		Attempt:     1,
		PayloadJSON: requestDigest,
	})
	return operationID, nil
}

func (f *fakeStore) ClaimTask(ctx context.Context, nodeID string) (*store.ClaimedTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, task := range f.queued {
		if task.NodeID != nodeID {
			continue
		}
		f.queued = append(f.queued[:i], f.queued[i+1:]...)
		return task, nil
	}
	return nil, nil
}

func (f *fakeStore) CompleteTask(ctx context.Context, operationID, nodeID string, succeeded bool, errCode *string, resultJSON []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed = append(f.completed, taskCompleted{OperationID: operationID, NodeID: nodeID, Succeeded: succeeded, ErrorCode: errCode, ResultJSON: resultJSON})
	return nil
}

func (f *fakeStore) RecordAgentHeartbeat(ctx context.Context, nodeID string, hb domain.Heartbeat) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	hb.NodeID = nodeID
	f.heartbeats = append(f.heartbeats, hb)
	return nil
}

func (f *fakeStore) ApplyServerObserved(ctx context.Context, obs domain.ServerObserved) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed = append(f.observed, obs)
	return nil
}

func (f *fakeStore) RecordAudit(ctx context.Context, event domain.AuditEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, event)
	return nil
}

func (f *fakeStore) RecordConsoleLines(ctx context.Context, serverID string, lines []domain.ConsoleLine) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consoleLines = append(f.consoleLines, lines...)
	return nil
}

func (f *fakeStore) ApplyServerMetrics(ctx context.Context, metrics []domain.ServerMetrics) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metrics = append(f.metrics, metrics...)
	return nil
}

func (f *fakeStore) heartbeatCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.heartbeats)
}

func (f *fakeStore) lastHeartbeat() domain.Heartbeat {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.heartbeats) == 0 {
		return domain.Heartbeat{}
	}
	return f.heartbeats[len(f.heartbeats)-1]
}

func (f *fakeStore) observedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.observed)
}

func (f *fakeStore) completedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.completed)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func mustKeyPair(t *testing.T, certPEM, keyPEM []byte) tls.Certificate {
	t.Helper()
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("x509 key pair: %v", err)
	}
	return pair
}

func caRootPool(t *testing.T, ca *agentca.CA) *x509.CertPool {
	t.Helper()
	rootPEM, err := ca.RootCAPEM()
	if err != nil {
		t.Fatalf("root ca pem: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(rootPEM) {
		t.Fatalf("append root ca to pool failed")
	}
	return pool
}

// newTestServer 启动一个带 mTLS 的 bufconn gRPC server 并返回 client 连接。
// client 使用 CN=nodeID 的 CA 签发证书建立连接。
func newTestServer(t *testing.T, ca *agentca.CA, store TaskStore, token string, nodeID string) (*Server, *grpc.ClientConn) {
	t.Helper()
	srv := NewServer(ca, store, discardLogger(), WithRegistrationToken(token))

	serverCert, serverKey, err := ca.IssueServerCertificate(24 * time.Hour)
	if err != nil {
		t.Fatalf("issue server cert: %v", err)
	}
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{mustKeyPair(t, serverCert, serverKey)},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    caRootPool(t, ca),
		MinVersion:   tls.VersionTLS12,
	})))
	srv.register(gs)
	go func() {
		_ = gs.Serve(lis)
	}()
	t.Cleanup(gs.Stop)

	clientCert, clientKey, err := ca.IssueAgentCertificateWithKey(nodeID, 24*time.Hour)
	if err != nil {
		t.Fatalf("issue client cert: %v", err)
	}
	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.Dial()
	}
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			Certificates: []tls.Certificate{mustKeyPair(t, clientCert, clientKey)},
			RootCAs:      caRootPool(t, ca),
			ServerName:   "control-plane",
			MinVersion:   tls.VersionTLS12,
		})),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return srv, conn
}

func TestEnrollAndConnect(t *testing.T) {
	ca, err := agentca.NewCA(t.TempDir())
	if err != nil {
		t.Fatalf("new ca: %v", err)
	}
	fs := newFakeStore()
	const nodeID = "test-node-1"
	srv, conn := newTestServer(t, ca, fs, "reg-token", nodeID)
	_ = srv

	client := agentv1.NewAgentGatewayServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rootPEM, err := ca.RootCAPEM()
	if err != nil {
		t.Fatalf("root ca pem: %v", err)
	}

	// 1. Enroll：正确 token + CSR → 返回 node id + 可校验的证书链 + 根证书
	//    （证书必须使用 CSR 公钥，保证 Agent 私钥可配对——真实 mTLS 路径）
	agentKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate agent key: %v", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "node-1"},
	}, agentKey)
	if err != nil {
		t.Fatalf("create csr: %v", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	resp, err := client.Enroll(ctx, &agentv1.EnrollRequest{
		RegistrationToken:         "reg-token",
		CertificateSigningRequest: csrPEM,
		NodeName:                  "node-1",
		AgentVersion:              "0.1.0",
		Capabilities: []*agentv1.Capability{
			{Name: "runtime.docker", Version: "1"},
		},
	})
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if resp.NodeId == "" {
		t.Fatal("expected non-empty node id")
	}
	if len(resp.CertificateChain) == 0 {
		t.Fatal("expected certificate chain")
	}
	if err := ca.VerifyPeerCertificate(resp.CertificateChain, resp.NodeId); err != nil {
		t.Fatalf("enrolled certificate not valid for node %q: %v", resp.NodeId, err)
	}
	issuedBlock, _ := pem.Decode(resp.CertificateChain)
	issuedCert, err := x509.ParseCertificate(issuedBlock.Bytes)
	if err != nil {
		t.Fatalf("parse issued certificate: %v", err)
	}
	if !issuedCert.PublicKey.(*rsa.PublicKey).Equal(&agentKey.PublicKey) {
		t.Fatal("enrolled certificate public key does not match csr public key")
	}
	if !bytes.Equal(resp.CaCertificate, rootPEM) {
		t.Fatal("ca certificate mismatch")
	}
	if resp.ExpiresAt == nil || resp.ExpiresAt.AsTime().Before(time.Now()) {
		t.Fatal("expected future expiry")
	}

	// 2. Enroll：错误 token → PermissionDenied
	_, err = client.Enroll(ctx, &agentv1.EnrollRequest{RegistrationToken: "wrong", NodeName: "node-x"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}

	// 3. Connect：Hello → Welcome
	stream, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := stream.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{
		NodeId:       nodeID,
		AgentVersion: "0.1.0",
	}}}); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	first, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv welcome: %v", err)
	}
	welcome := first.GetWelcome()
	if welcome == nil {
		t.Fatalf("expected welcome, got %T", first.Payload)
	}
	if welcome.ProtocolVersion != "v1" {
		t.Errorf("protocol version = %q, want v1", welcome.ProtocolVersion)
	}
	if welcome.HeartbeatIntervalSeconds != 10 {
		t.Errorf("heartbeat interval = %d, want 10", welcome.HeartbeatIntervalSeconds)
	}
	if welcome.MaxInFlightTasks != 3 {
		t.Errorf("max in flight = %d, want 3", welcome.MaxInFlightTasks)
	}

	// 4. Heartbeat → fakeStore 记录
	err = stream.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_Heartbeat{Heartbeat: &agentv1.Heartbeat{
		ObservedAt:           timestamppbNow(),
		MemoryTotalBytes:     8192,
		MemoryAvailableBytes: 4096,
		DiskTotalBytes:       100,
		DiskAvailableBytes:   50,
		CpuLoad:              0.25,
		AgentVersion:         "0.1.0",
	}}})
	if err != nil {
		t.Fatalf("send heartbeat: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for fs.heartbeatCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if fs.heartbeatCount() != 1 {
		t.Fatalf("heartbeats recorded = %d, want 1", fs.heartbeatCount())
	}
	hb := fs.lastHeartbeat()
	if hb.NodeID != nodeID {
		t.Errorf("heartbeat node = %q, want %q", hb.NodeID, nodeID)
	}
	if hb.MemoryTotalBytes != 8192 || hb.DiskTotalBytes != 100 {
		t.Errorf("heartbeat resources not mapped: %+v", hb)
	}

	// 5. ServerObserved → fakeStore 记录
	err = stream.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_ServerObserved{ServerObserved: &agentv1.ServerObserved{
		ServerId:           "server-1",
		ObservedGeneration: 2,
		ObservedPower:      agentv1.ObservedPower_OBSERVED_POWER_RUNNING,
		HealthCondition:    agentv1.HealthCondition_HEALTH_CONDITION_HEALTHY,
		RuntimeId:          "cont-1",
		ObservedAt:         timestamppbNow(),
	}}})
	if err != nil {
		t.Fatalf("send server observed: %v", err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for fs.observedCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if fs.observedCount() != 1 {
		t.Fatalf("observed snapshots = %d, want 1", fs.observedCount())
	}
	obs := fs.observed[0]
	if obs.ServerID != "server-1" || obs.ObservedPower != "running" || obs.HealthCondition != "healthy" {
		t.Errorf("observed not mapped correctly: %+v", obs)
	}

	// 6. TaskResult → fakeStore 记录
	err = stream.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_TaskResult{TaskResult: &agentv1.TaskResult{
		OperationId: "task-1",
		Succeeded:   true,
		Attempt:     1,
	}}})
	if err != nil {
		t.Fatalf("send task result: %v", err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for fs.completedCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if fs.completedCount() != 1 {
		t.Fatalf("completed tasks = %d, want 1", fs.completedCount())
	}
	if !fs.completed[0].Succeeded {
		t.Errorf("expected succeeded task completion")
	}

	// 7. 预置任务 → 服务端 claim 并通过流下发 Task
	enqueuedID, err := fs.EnqueueTask(ctx, "server-1", nodeID, "provision", 1, "actor-1", "test-idempotency-key-1", []byte(`{"game":"factorio"}`))
	if err != nil {
		t.Fatalf("enqueue task: %v", err)
	}
	var dispatched *agentv1.Task
	deadline = time.Now().Add(8 * time.Second)
	for dispatched == nil && time.Now().Before(deadline) {
		msg, recvErr := stream.Recv()
		if recvErr != nil {
			t.Fatalf("recv task: %v", recvErr)
		}
		dispatched = msg.GetTask()
	}
	if dispatched == nil {
		t.Fatal("timed out waiting for dispatched task")
	}
	if dispatched.OperationId != enqueuedID {
		t.Errorf("dispatched operation = %q, want %q", dispatched.OperationId, enqueuedID)
	}
	if dispatched.Type != "provision" || dispatched.ServerId != "server-1" || dispatched.Attempt != 1 {
		t.Errorf("dispatched task fields wrong: %+v", dispatched)
	}
	if !bytes.Equal(dispatched.GetPayloadJson(), []byte(`{"game":"factorio"}`)) {
		t.Errorf("dispatched payload = %q", dispatched.GetPayloadJson())
	}
}

func TestConnectRejectsMismatchedNode(t *testing.T) {
	ca, err := agentca.NewCA(t.TempDir())
	if err != nil {
		t.Fatalf("new ca: %v", err)
	}
	fs := newFakeStore()
	_, conn := newTestServer(t, ca, fs, "reg-token", "cert-node")

	client := agentv1.NewAgentGatewayServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// Hello 声称的 node id 与证书 CN（cert-node）不一致
	if err := stream.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{
		NodeId: "other-node",
	}}}); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	_, err = stream.Recv()
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied for node mismatch, got %v", err)
	}
}

func TestConnectRequiresHelloFirst(t *testing.T) {
	ca, err := agentca.NewCA(t.TempDir())
	if err != nil {
		t.Fatalf("new ca: %v", err)
	}
	fs := newFakeStore()
	_, conn := newTestServer(t, ca, fs, "reg-token", "hello-node")

	client := agentv1.NewAgentGatewayServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// 首帧不是 Hello：直接发 Heartbeat
	if err := stream.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_Heartbeat{Heartbeat: &agentv1.Heartbeat{}}}); err != nil {
		t.Fatalf("send first frame: %v", err)
	}
	_, err = stream.Recv()
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition for non-hello first frame, got %v", err)
	}
}

func TestListenAndServeAutoCert(t *testing.T) {
	ca, err := agentca.NewCA(t.TempDir())
	if err != nil {
		t.Fatalf("new ca: %v", err)
	}
	fs := newFakeStore()
	srv := NewServer(ca, fs, discardLogger(), WithRegistrationToken("reg-token"))

	// 预留一个可用端口
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe(ctx, addr, nil, nil) }()

	// 客户端用 CA 签发证书 + 自动签发的 server 证书（CN=control-plane）建立 mTLS
	clientCert, clientKey, err := ca.IssueAgentCertificateWithKey("listen-client", 24*time.Hour)
	if err != nil {
		t.Fatalf("issue client cert: %v", err)
	}
	ctx2, cancel2 := context.WithTimeout(ctx, 10*time.Second)
	defer cancel2()
	conn, err := grpc.DialContext(ctx2, addr,
		grpc.WithBlock(),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			Certificates: []tls.Certificate{mustKeyPair(t, clientCert, clientKey)},
			RootCAs:      caRootPool(t, ca),
			ServerName:   "control-plane",
			MinVersion:   tls.VersionTLS12,
		})),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	resp, err := agentv1.NewAgentGatewayServiceClient(conn).Enroll(ctx2, &agentv1.EnrollRequest{
		RegistrationToken: "reg-token",
		NodeName:          "listen-node",
	})
	if err != nil {
		t.Fatalf("enroll over listenandserve: %v", err)
	}
	if resp.NodeId == "" {
		t.Fatal("expected node id from listenandserve enroll")
	}

	cancel()
	select {
	case serveErr := <-errCh:
		if serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
			t.Fatalf("listenandserve returned: %v", serveErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listenandserve did not stop after cancel")
	}
}

func timestamppbNow() *timestamppb.Timestamp {
	return timestamppb.New(time.Now())
}

func TestSendConsoleCommandDispatchesFrame(t *testing.T) {
	ca, err := agentca.NewCA(t.TempDir())
	if err != nil {
		t.Fatalf("new ca: %v", err)
	}
	fs := newFakeStore()
	srv := NewServer(ca, fs, discardLogger())

	sent := make(chan *agentv1.ConnectResponse, 1)
	stream := &nodeStream{
		nodeID: "node-1",
		send: func(resp *agentv1.ConnectResponse) error {
			sent <- resp
			return nil
		},
	}
	srv.registerStream(stream)
	defer srv.unregisterStream(stream)
	type dispatchResult struct {
		result domain.ConsoleCommandResult
		err    error
	}
	dispatched := make(chan dispatchResult, 1)
	go func() {
		result, err := srv.SendConsoleCommand(context.Background(), "node-1", "server-1", "list")
		dispatched <- dispatchResult{result: result, err: err}
	}()
	got := <-sent
	cmd := got.GetConsoleCommand()
	if cmd == nil {
		t.Fatalf("expected console command frame, got %T", got.Payload)
	}
	if cmd.GetCommand() != "list" || cmd.GetServerId() != "server-1" || cmd.GetRequestId() == "" {
		t.Errorf("console command frame fields wrong: %+v", cmd)
	}
	srv.completeConsoleCommand(stream, &agentv1.ConsoleCommandResult{
		RequestId: cmd.GetRequestId(), ServerId: cmd.GetServerId(), Succeeded: true,
	})
	completed := <-dispatched
	if completed.err != nil || !completed.result.Succeeded {
		t.Fatalf("send console command = %+v, %v", completed.result, completed.err)
	}
	if _, err := srv.SendConsoleCommand(context.Background(), "no-such-node", "server-1", "list"); err == nil {
		t.Fatal("expected error for offline node")
	}
}

func TestEnrollCanonicalizesCapabilityNameAndVersion(t *testing.T) {
	ca, err := agentca.NewCA(t.TempDir())
	if err != nil {
		t.Fatalf("new ca: %v", err)
	}
	fs := newFakeStore()
	srv := NewServer(ca, fs, discardLogger())
	response, err := srv.Enroll(context.Background(), &agentv1.EnrollRequest{
		NodeName: "capability-node", AgentVersion: "test",
		Capabilities: []*agentv1.Capability{
			{Name: "runtime.container", Version: "1"},
			{Name: "platform.linux.amd64", Version: "2"},
			{Name: "invalid capability", Version: "1"},
			nil,
		},
	})
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	fs.mu.Lock()
	node := fs.nodes[response.GetNodeId()]
	fs.mu.Unlock()
	want := []string{"runtime.container/v1", "platform.linux.amd64/v2"}
	if len(node.Capabilities) != len(want) {
		t.Fatalf("capabilities = %v, want %v", node.Capabilities, want)
	}
	for index := range want {
		if node.Capabilities[index] != want[index] {
			t.Fatalf("capabilities = %v, want %v", node.Capabilities, want)
		}
	}
}

func TestConsoleCommandRejectsMismatchedServerResult(t *testing.T) {
	ca, err := agentca.NewCA(t.TempDir())
	if err != nil {
		t.Fatalf("new ca: %v", err)
	}
	srv := NewServer(ca, newFakeStore(), discardLogger())
	sent := make(chan *agentv1.ConnectResponse, 1)
	stream := &nodeStream{nodeID: "node-1", send: func(resp *agentv1.ConnectResponse) error { sent <- resp; return nil }}
	srv.registerStream(stream)
	defer srv.unregisterStream(stream)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := srv.SendConsoleCommand(ctx, "node-1", "server-a", "list")
		done <- err
	}()
	cmd := (<-sent).GetConsoleCommand()
	srv.completeConsoleCommand(stream, &agentv1.ConsoleCommandResult{RequestId: cmd.GetRequestId(), ServerId: "server-b", Succeeded: true})
	if err := <-done; err == nil {
		t.Fatal("mismatched server result completed the pending command")
	}
}

func TestUnregisterOldStreamDoesNotRemoveReplacement(t *testing.T) {
	ca, err := agentca.NewCA(t.TempDir())
	if err != nil {
		t.Fatalf("new ca: %v", err)
	}
	srv := NewServer(ca, newFakeStore(), discardLogger())
	oldStream := &nodeStream{nodeID: "node-1", send: func(*agentv1.ConnectResponse) error { return nil }}
	newStream := &nodeStream{nodeID: "node-1", send: func(*agentv1.ConnectResponse) error { return nil }}
	srv.registerStream(oldStream)
	srv.registerStream(newStream)
	defer srv.unregisterStream(newStream)
	srv.unregisterStream(oldStream)
	srv.streamMu.Lock()
	current := srv.streams["node-1"]
	srv.streamMu.Unlock()
	if current != newStream {
		t.Fatal("old connection unregister removed the replacement stream")
	}
}

func TestSendConsoleCommandDeadlineCoversBlockedTransportSend(t *testing.T) {
	ca, err := agentca.NewCA(t.TempDir())
	if err != nil {
		t.Fatalf("new ca: %v", err)
	}
	srv := NewServer(ca, newFakeStore(), discardLogger(), withConsoleCommandTimeout(50*time.Millisecond))
	started := make(chan struct{})
	release := make(chan struct{})
	stream := &nodeStream{
		nodeID: "node-1",
		send: func(*agentv1.ConnectResponse) error {
			close(started)
			<-release
			return nil
		},
	}
	srv.registerStream(stream)

	before := time.Now()
	result, err := srv.SendConsoleCommand(context.Background(), "node-1", "server-1", "list")
	elapsed := time.Since(before)
	if err == nil {
		t.Fatal("blocked transport send unexpectedly succeeded")
	}
	if result.ErrorCode != "CONSOLE_TIMEOUT" || !result.Retryable {
		t.Fatalf("result = %+v, want retryable CONSOLE_TIMEOUT", result)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("blocked send returned after %v, want caller deadline to bound it", elapsed)
	}
	select {
	case <-started:
	default:
		t.Fatal("test did not exercise a blocked transport send")
	}

	srv.unregisterStream(stream)
	close(release)
	select {
	case <-stream.writerDone:
	case <-time.After(time.Second):
		t.Fatal("outbound writer did not stop after transport was released")
	}
}

func TestSendConsoleCommandDisconnectUnblocksBlockedTransportSend(t *testing.T) {
	ca, err := agentca.NewCA(t.TempDir())
	if err != nil {
		t.Fatalf("new ca: %v", err)
	}
	srv := NewServer(ca, newFakeStore(), discardLogger())
	started := make(chan struct{})
	release := make(chan struct{})
	stream := &nodeStream{
		nodeID: "node-1",
		send: func(*agentv1.ConnectResponse) error {
			close(started)
			<-release
			return nil
		},
	}
	srv.registerStream(stream)
	type dispatchResult struct {
		result domain.ConsoleCommandResult
		err    error
	}
	done := make(chan dispatchResult, 1)
	go func() {
		result, sendErr := srv.SendConsoleCommand(context.Background(), "node-1", "server-1", "list")
		done <- dispatchResult{result: result, err: sendErr}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("transport send did not start")
	}
	srv.unregisterStream(stream)
	select {
	case completed := <-done:
		if completed.err == nil {
			t.Fatal("disconnect unexpectedly completed command successfully")
		}
		if completed.result.ErrorCode != "NODE_OFFLINE" || !completed.result.Retryable {
			t.Fatalf("result = %+v, want retryable NODE_OFFLINE", completed.result)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("disconnect did not unblock command waiting on transport send")
	}
	close(release)
	select {
	case <-stream.writerDone:
	case <-time.After(time.Second):
		t.Fatal("outbound writer did not stop after transport was released")
	}
}

func TestClaimedTaskToProtoBackup(t *testing.T) {
	payload := `{"backupId":"b-1","storageObjectKey":"backups/b-1.tar.gz"}`
	base := &store.ClaimedTask{
		OperationID: "op-1", ServerID: "srv-1", NodeID: "node-1",
		Generation: 1, Attempt: 1, TaskType: "backup", PayloadJSON: []byte(payload),
	}

	create := claimedTaskToProto(base)
	if create.GetType() != "backup" {
		t.Fatalf("backup task type = %q, want backup", create.GetType())
	}
	if create.GetBackup() == nil {
		t.Fatalf("expected backup payload arm, got %T", create.GetPayload())
	}
	if got := create.GetBackup().GetCreate(); got == nil {
		t.Fatalf("expected create arm, got %T", create.GetBackup().GetAction())
	} else if got.GetBackupId() != "b-1" || got.GetStorageObjectKey() != "backups/b-1.tar.gz" {
		t.Errorf("create payload wrong: %+v", got)
	}

	restore := claimedTaskToProto(&store.ClaimedTask{OperationID: "op-2", ServerID: "srv-1", NodeID: "node-1", TaskType: "restore", Generation: 1, Attempt: 1, PayloadJSON: []byte(payload)})
	if restore.GetType() != "backup" || restore.GetBackup().GetRestore() == nil {
		t.Fatalf("expected normalized backup/restore, got type=%q payload=%T", restore.GetType(), restore.GetPayload())
	}
	if got := restore.GetBackup().GetRestore(); got.GetBackupId() != "b-1" {
		t.Errorf("restore payload wrong: %+v", got)
	}

	del := claimedTaskToProto(&store.ClaimedTask{OperationID: "op-3", ServerID: "srv-1", NodeID: "node-1", TaskType: "backup-delete", Generation: 1, Attempt: 1, PayloadJSON: []byte(`{"backupId":"b-1","storageObjectKey":"backups/b-1.tar.gz","deleteRemoteObject":true}`)})
	if del.GetType() != "backup" || del.GetBackup().GetDelete() == nil {
		t.Fatalf("expected normalized backup/delete, got type=%q payload=%T", del.GetType(), del.GetPayload())
	}
	if got := del.GetBackup().GetDelete(); !got.GetDeleteRemoteObject() {
		t.Errorf("delete payload should request remote object removal: %+v", got)
	}
}
