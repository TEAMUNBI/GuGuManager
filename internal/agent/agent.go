package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	agentv1 "github.com/gugumanager/gugumanager/api/proto/gugumanager/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// 证书持久化文件名（位于 Config.CertDir）。
	certFile   = "agent.crt"
	keyFile    = "agent.key"
	nodeIDFile = "node-id"

	// Agent 支持的协议版本。
	protocolV1 = "v1"

	// 心跳与重连的兜底值。
	defaultHeartbeatInterval = 30 * time.Second
	reconnectDelay           = 3 * time.Second
)

// runOptions 控制 run 的连接与执行行为，测试通过它注入 fake 组件。
type runOptions struct {
	dialer     func(context.Context, string) (net.Conn, error) // nil 时直连 PanelAddr
	serverName string                                          // TLS ServerName
	executor   TaskExecutor                                    // nil 时用 DockerExecutor
}

// TaskExecutor 执行 Control Plane 下发的任务，并提供容器级控制台命令
// 执行与运行中服务器枚举（供日志 tailer 与指标采样器使用）。
type TaskExecutor interface {
	ExecuteTask(ctx context.Context, task *agentv1.Task) (*ExecutionOutcome, error)
	ExecuteConsoleCommand(ctx context.Context, serverID, command string) (*ExecutionOutcome, error)
	Runtime() (containerRuntime, error)
	ListRunningServers(ctx context.Context) ([]string, error)
}

// connectRequestSender is the outbound half of the Agent Connect stream.
// grpc-go permits one concurrent sender and one receiver, but not multiple
// concurrent Send calls. serializedConnectSender is therefore shared by every
// heartbeat, task, console, file, log, metric, and certificate path belonging
// to one connection.
type connectRequestSender interface {
	Send(*agentv1.ConnectRequest) error
}

type serializedConnectSender struct {
	mu     sync.Mutex
	stream connectRequestSender
}

func (s *serializedConnectSender) Send(request *agentv1.ConnectRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream.Send(request)
}

// agent 持有一次进程生命周期内的连接状态。
type agent struct {
	cfg      Config
	logger   *slog.Logger
	executor TaskExecutor
	nodeID   string
	cert     tls.Certificate

	// 证书轮换：RotateCertificate 下行后暂存的新私钥，等待 CertificateResponse。
	rotationKey *rsa.PrivateKey
	rotationID  string

	// 日志序号：为每台服务器维护单调递增的序列，供 LogBatch 回显排序。
	seqMu     sync.Mutex
	sequences map[string]int64

	// 持久操作日志：payload digest 与终态结果跨进程重启保留，重投不重复
	// 执行副作用。每个 serveSession 打开一次。
	journal *OperationJournal

	// 有界 worker pool：Welcome.MaxInFlightTasks 决定并行度，任务经缓冲
	// 通道分发，recv 循环只负责收帧。
	taskWorkers int
	tasks       chan *agentv1.Task

	// 每服务器互斥：同一台服务器的任务串行执行，互不踩踏数据目录。
	serverLocksMu sync.Mutex
	serverLocks   map[string]*sync.Mutex

	// 执行中的操作：同 operation 并发重投时等待同一份结果，不启动第二个
	// 执行（journal 的 running 记录配合内存登记闭环竞态）。
	inflightMu sync.Mutex
	inflight   map[string]*inflightExecution
}

// inflightExecution 是一次执行中任务的结果通道。
type inflightExecution struct {
	done   chan struct{}
	result inflightResult
	mu     sync.RWMutex
	doneMu sync.Once
}

// inflightResult 是执行完成后交给重投等待者的可重放结果。
type inflightResult struct {
	succeeded  bool
	errorCode  string
	retryable  bool
	resultJSON []byte
	observed   []byte
}

// keyedLock 返回每服务器互斥锁。
func (a *agent) keyedLock(serverID string) *sync.Mutex {
	a.serverLocksMu.Lock()
	defer a.serverLocksMu.Unlock()
	if a.serverLocks == nil {
		a.serverLocks = make(map[string]*sync.Mutex)
	}
	lock, ok := a.serverLocks[serverID]
	if !ok {
		lock = &sync.Mutex{}
		a.serverLocks[serverID] = lock
	}
	return lock
}

// registerInflight 登记执行中的操作；已存在时返回既有执行（并发重投）。
func (a *agent) registerInflight(operationID string) (*inflightExecution, bool) {
	a.inflightMu.Lock()
	defer a.inflightMu.Unlock()
	if a.inflight == nil {
		a.inflight = make(map[string]*inflightExecution)
	}
	if existing, ok := a.inflight[operationID]; ok {
		return existing, false
	}
	execution := &inflightExecution{done: make(chan struct{})}
	a.inflight[operationID] = execution
	return execution, true
}

func (a *agent) unregisterInflight(operationID string) {
	a.inflightMu.Lock()
	delete(a.inflight, operationID)
	a.inflightMu.Unlock()
}

func (e *inflightExecution) complete(result inflightResult) {
	e.doneMu.Do(func() {
		e.mu.Lock()
		e.result = result
		e.mu.Unlock()
		close(e.done)
	})
}

func (e *inflightExecution) getResult() inflightResult {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.result
}

// Run 运行 Agent 主循环（断开后自动重连、证书复用），直到 ctx 取消。
func Run(ctx context.Context, cfg Config, logger *slog.Logger) error {
	opts, err := defaultRunOptions(cfg)
	if err != nil {
		return err
	}
	return run(ctx, cfg, logger, opts)
}

// RunOnce 执行一次 Enroll/Connect 会话后返回，不自动重连。
func RunOnce(ctx context.Context, cfg Config, logger *slog.Logger) error {
	opts, err := defaultRunOptions(cfg)
	if err != nil {
		return err
	}
	return serveSession(ctx, cfg, logger, opts)
}

func defaultRunOptions(cfg Config) (runOptions, error) {
	exec, err := NewDockerExecutor(cfg.DataRoot)
	if err != nil {
		return runOptions{}, err
	}
	return runOptions{serverName: hostOf(cfg.PanelAddr), executor: exec}, nil
}

// hostOf 从 host:port 地址中提取 host，作为 TLS ServerName。
func hostOf(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// run 是 Agent 的生命周期入口：凭据就绪 → 会话 → 断开重连，直到 ctx 取消。
func run(ctx context.Context, cfg Config, logger *slog.Logger, opts runOptions) error {
	if logger == nil {
		logger = slog.Default()
	}
	for {
		err := serveSession(ctx, cfg, logger, opts)
		if err == nil || ctx.Err() != nil {
			return nil
		}
		logger.Warn("connect session ended; reconnecting", "error", err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(reconnectDelay):
		}
	}
}

// serveSession 完成一次"凭据 → Enroll（如需）→ Connect 会话"的完整生命周期。
func serveSession(ctx context.Context, cfg Config, logger *slog.Logger, opts runOptions) error {
	a := &agent{cfg: cfg, logger: logger, executor: opts.executor, sequences: make(map[string]int64)}
	if a.executor == nil {
		exec, err := NewDockerExecutor(cfg.DataRoot)
		if err != nil {
			return fmt.Errorf("create docker executor: %w", err)
		}
		a.executor = exec
	}
	// 持久操作日志：重投恢复与终态重放都依赖它跨会话存在。
	journal, err := OpenOperationJournal(filepath.Join(cfg.DataRoot, "agent-journal.db"))
	if err != nil {
		return err
	}
	defer journal.Close()
	a.journal = journal

	nodeID, cert, err := a.ensureCredentials(ctx, opts)
	if err != nil {
		return err
	}
	a.nodeID = nodeID
	a.cert = cert
	return a.serveOnce(ctx, opts)
}

// ensureCredentials 复用已持久化的证书（CertDir/agent.crt + agent.key）；
// 不存在时生成 RSA 密钥与 CSR，Enroll 换取证书链并落地（含信任根）。
func (a *agent) ensureCredentials(ctx context.Context, opts runOptions) (string, tls.Certificate, error) {
	certPath := filepath.Join(a.cfg.CertDir, certFile)
	keyPath := filepath.Join(a.cfg.CertDir, keyFile)
	if pair, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return a.loadNodeID(), pair, nil
	}
	if a.cfg.RegistrationToken == "" {
		return "", tls.Certificate{}, fmt.Errorf("no credentials on disk and no registration token configured")
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", tls.Certificate{}, fmt.Errorf("generate key: %w", err)
	}
	csrPEM, err := buildCSR(key, a.cfg.NodeName)
	if err != nil {
		return "", tls.Certificate{}, fmt.Errorf("build csr: %w", err)
	}
	resp, err := a.enroll(ctx, csrPEM, opts)
	if err != nil {
		return "", tls.Certificate{}, fmt.Errorf("enroll: %w", err)
	}

	if err := os.MkdirAll(a.cfg.CertDir, 0o700); err != nil {
		return "", tls.Certificate{}, fmt.Errorf("create cert dir: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(certPath, resp.GetCertificateChain(), 0o600); err != nil {
		return "", tls.Certificate{}, fmt.Errorf("write agent cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return "", tls.Certificate{}, fmt.Errorf("write agent key: %w", err)
	}
	if len(resp.GetCaCertificate()) > 0 {
		if err := a.persistTrustRoot(resp.GetCaCertificate()); err != nil {
			return "", tls.Certificate{}, fmt.Errorf("write trust root: %w", err)
		}
	}
	if resp.GetNodeId() != "" {
		_ = os.WriteFile(filepath.Join(a.cfg.CertDir, nodeIDFile), []byte(resp.GetNodeId()), 0o644)
	}
	pair, err := tls.X509KeyPair(resp.GetCertificateChain(), keyPEM)
	if err != nil {
		return "", tls.Certificate{}, fmt.Errorf("load enrolled key pair: %w", err)
	}
	a.logger.Info("agent enrolled", "node_id", resp.GetNodeId())
	return resp.GetNodeId(), pair, nil
}

// loadNodeID 读取持久化的 NodeId；缺失时回退到 "node-<NodeName>"。
func (a *agent) loadNodeID() string {
	if b, err := os.ReadFile(filepath.Join(a.cfg.CertDir, nodeIDFile)); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id
		}
	}
	return "node-" + a.cfg.NodeName
}

// buildCSR 生成 CN=节点名的 PKCS#10 CSR（PEM 编码）。
func buildCSR(key *rsa.PrivateKey, cn string) ([]byte, error) {
	tmpl := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: cn},
		DNSNames: []string{cn},
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// enroll 调用 Control Plane 的 Enroll RPC。注册必须基于预置的信任根
// （TrustRootPath）或指纹完成服务器证书校验；禁止 InsecureSkipVerify，
// 信任根缺失时拒绝注册而不是降级为明文信任。
func (a *agent) enroll(ctx context.Context, csrPEM []byte, opts runOptions) (*agentv1.EnrollResponse, error) {
	pool := a.rootPool()
	if pool == nil {
		return nil, fmt.Errorf("trust root not available at %s; refusing insecure enrollment", a.cfg.TrustRootPath)
	}
	tlsCfg := &tls.Config{
		ServerName: opts.serverName,
		MinVersion: tls.VersionTLS12,
		RootCAs:    pool,
	}
	if a.cfg.CAFingerprint != "" {
		tlsCfg.VerifyPeerCertificate = verifyServerFingerprint(a.cfg.CAFingerprint)
	}
	conn, err := a.dial(ctx, tlsCfg, opts)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	client := agentv1.NewAgentGatewayServiceClient(conn)
	return client.Enroll(ctx, &agentv1.EnrollRequest{
		RegistrationToken:         a.cfg.RegistrationToken,
		CertificateSigningRequest: csrPEM,
		NodeName:                  a.cfg.NodeName,
		AgentVersion:              a.cfg.AgentVersion,
		Capabilities:              defaultCapabilities(),
	})
}

// verifyServerFingerprint 在校验链之后附加指纹钉扎：服务器证书链中任一
// 证书（叶子或 CA 根）的 SHA-256 与配置值一致才放行。
func verifyServerFingerprint(expected string) func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
	want := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(expected), ":", ""))
	matches := func(raw []byte) bool {
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:]) == want
	}
	return func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
		for _, raw := range rawCerts {
			if matches(raw) {
				return nil
			}
		}
		for _, chain := range verifiedChains {
			for _, cert := range chain {
				if matches(cert.Raw) {
					return nil
				}
			}
		}
		return fmt.Errorf("server certificate chain fingerprint does not match the pinned value")
	}
}

func defaultCapabilities() []*agentv1.Capability {
	return []*agentv1.Capability{
		{Name: "runtime.container", Version: "1"},
		{Name: "platform." + runtime.GOOS + "." + runtime.GOARCH, Version: "1"},
	}
}

// rootPool 从 TrustRootPath 加载 CA 根证书池；文件缺失或不可解析时返回 nil。
func (a *agent) rootPool() *x509.CertPool {
	raw, err := os.ReadFile(a.cfg.TrustRootPath)
	if err != nil {
		return nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil
	}
	return pool
}

// dial 建立带 TLS 的 gRPC 客户端连接；测试通过 opts.dialer 注入 bufconn。
func (a *agent) dial(ctx context.Context, tlsCfg *tls.Config, opts runOptions) (*grpc.ClientConn, error) {
	// maxAgentMessageBytes 与服务端对应：放宽单帧上限以支撑备份下载等大帧。
	const maxAgentMessageBytes = 512 << 20
	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(maxAgentMessageBytes),
			grpc.MaxCallSendMsgSize(maxAgentMessageBytes),
		),
	}
	target := a.cfg.PanelAddr
	if opts.dialer != nil {
		dialOpts = append(dialOpts, grpc.WithContextDialer(opts.dialer))
		// 自定义 dialer（如测试的 bufconn）配合显式 passthrough，
		// 避免默认 dns resolver 尝试解析非 DNS 地址。
		target = "passthrough:///" + target
	}
	conn, err := grpc.NewClient(target, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("create grpc client: %w", err)
	}
	return conn, nil
}

// serveOnce 执行一次 Connect 双向流会话：Hello → Welcome → 心跳 → 处理下行帧。
func (a *agent) serveOnce(ctx context.Context, opts runOptions) error {
	ctx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()

	tlsCfg := &tls.Config{
		ServerName:   opts.serverName,
		MinVersion:   tls.VersionTLS12,
		RootCAs:      a.rootPool(),
		Certificates: []tls.Certificate{a.cert},
	}
	if tlsCfg.RootCAs == nil {
		return fmt.Errorf("trust root not available at %s", a.cfg.TrustRootPath)
	}
	if a.cfg.CAFingerprint != "" {
		tlsCfg.VerifyPeerCertificate = verifyServerFingerprint(a.cfg.CAFingerprint)
	}
	conn, err := a.dial(ctx, tlsCfg, opts)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := agentv1.NewAgentGatewayServiceClient(conn)
	stream, err := client.Connect(ctx)
	if err != nil {
		return fmt.Errorf("open connect stream: %w", err)
	}
	outbound := &serializedConnectSender{stream: stream}
	if err := outbound.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{
		NodeId:           a.nodeID,
		AgentVersion:     a.cfg.AgentVersion,
		ProtocolVersions: []string{protocolV1},
		Capabilities:     defaultCapabilities(),
	}}}); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	resp, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("receive welcome: %w", err)
	}
	welcome := resp.GetWelcome()
	if welcome == nil {
		return fmt.Errorf("expected welcome frame, got %T", resp.GetPayload())
	}
	a.logger.Info("agent connected", "node_id", a.nodeID, "protocol", welcome.GetProtocolVersion())

	// 有界 worker pool：并发度与服务端 MaxInFlightTasks 对齐（上限 8），
	// 任务经缓冲通道分发，recv 循环保持只收帧、不执行。
	workerCount := int(welcome.GetMaxInFlightTasks())
	if workerCount <= 0 {
		workerCount = 1
	}
	if workerCount > 8 {
		workerCount = 8
	}
	a.taskWorkers = workerCount
	a.tasks = make(chan *agentv1.Task, workerCount*2)
	var workers sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case task, ok := <-a.tasks:
					if !ok {
						return
					}
					a.executeTask(ctx, outbound, client, task)
				}
			}
		}()
	}
	defer func() {
		// 会话结束：先取消会话上下文，再关闭任务通道，最后等待 worker。
		// recv 循环是唯一的发送者，因此退出后关闭通道不会与发送竞态。
		sessionCancel()
		close(a.tasks)
		workers.Wait()
	}()

	// 日志流式上报与指标采样：连接建立后即启动，会话结束随 ctx 取消。
	go a.startMetricSampler(ctx, outbound)
	a.startLogTailers(ctx, outbound)

	interval := time.Duration(welcome.GetHeartbeatIntervalSeconds()) * time.Second
	if interval <= 0 {
		interval = defaultHeartbeatInterval
	}
	stopHB := make(chan struct{})
	defer close(stopHB)
	go a.heartbeatLoop(ctx, outbound, interval, stopHB)

	for {
		req, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("connect recv: %w", err)
		}
		switch p := req.GetPayload().(type) {
		case *agentv1.ConnectResponse_Task:
			// 任务绝不丢弃：通道满时短暂阻塞 recv（心跳由独立 goroutine 发送）。
			select {
			case a.tasks <- p.Task:
			case <-ctx.Done():
				return nil
			}
		case *agentv1.ConnectResponse_ConsoleCommand:
			a.handleConsoleCommand(ctx, outbound, p.ConsoleCommand)
		case *agentv1.ConnectResponse_RotateCertificate:
			if err := a.rotateCertificate(ctx, outbound); err != nil {
				return fmt.Errorf("rotate certificate: %w", err)
			}
		case *agentv1.ConnectResponse_Drain:
			a.logger.Info("drain requested", "reason", p.Drain.GetReason())
		case *agentv1.ConnectResponse_CertificateResponse:
			if err := a.applyCertificateResponse(p.CertificateResponse); err != nil {
				a.logger.Warn("failed to apply rotated certificate", "error", err)
			}
		case *agentv1.ConnectResponse_FileOperationRequest:
			a.handleFileOperation(ctx, outbound, p.FileOperationRequest)
		}
	}
}

// taskPayloadMessage 提取 typed arm 的内层 proto.Message（legacy
// payload_json 返回 nil，由调用方按原始字节处理）。
func taskPayloadMessage(task *agentv1.Task) proto.Message {
	switch p := task.Payload.(type) {
	case *agentv1.Task_Provision:
		return p.Provision
	case *agentv1.Task_Power:
		return p.Power
	case *agentv1.Task_Backup:
		return p.Backup
	case *agentv1.Task_Extension:
		return p.Extension
	default:
		return nil
	}
}

// taskPayloadDigest 计算任务输入的稳定摘要（不含租约栅栏字段），作为
// Operation Journal 的去重键。同一 operation 重投时只有 digest 一致才允许
// 恢复或重放，digest 变化说明指令内容被改变，必须拒绝执行。protojson 对
// map 键排序，输出确定；legacy payload_json 原样参与摘要。
func taskPayloadDigest(task *agentv1.Task) string {
	var canonical strings.Builder
	canonical.WriteString(task.GetType())
	canonical.WriteByte('\n')
	canonical.WriteString(strconv.FormatUint(task.GetGeneration(), 10))
	canonical.WriteByte('\n')
	if task.Payload != nil {
		if message := taskPayloadMessage(task); message != nil {
			encoded, marshalErr := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(message)
			if marshalErr == nil {
				canonical.Write(encoded)
			}
		} else if raw := task.GetPayloadJson(); len(raw) > 0 {
			canonical.Write(raw)
		}
	}
	sum := sha256.Sum256([]byte(canonical.String()))
	return hex.EncodeToString(sum[:])
}

// executeTask 执行一个任务：回显栅栏 Ack → 操作日志去重（同 digest 重投
// 恢复/重放，不同 digest 拒绝）→ 每服务器互斥 → 执行期每 10 秒续租 →
// 先持久化终态再回发结果（结果帧不可丢）。
func (a *agent) executeTask(ctx context.Context, outbound connectRequestSender, gateway agentv1.AgentGatewayServiceClient, task *agentv1.Task) {
	digest := taskPayloadDigest(task)
	leaseToken := task.GetLeaseToken()
	connectionEpoch := task.GetConnectionEpoch()

	sendAck := func(accepted bool, code string) {
		frame := &agentv1.TaskAck{
			OperationId:     task.GetOperationId(),
			Accepted:        accepted,
			ErrorCode:       code,
			Attempt:         task.GetAttempt(),
			LeaseToken:      leaseToken,
			ConnectionEpoch: connectionEpoch,
		}
		if err := outbound.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_TaskAck{TaskAck: frame}}); err != nil {
			a.logger.Warn("send task ack failed", "operation", task.GetOperationId(), "error", err)
		}
	}

	// 操作日志去重：先查历史，再登记执行。
	if entry, ok, err := a.journal.Lookup(task.GetOperationId()); err != nil {
		a.logger.Warn("operation journal lookup failed", "operation", task.GetOperationId(), "error", err)
	} else if ok {
		if entry.Digest != digest {
			// 同 operation 不同 digest：指令被改变，拒绝执行。
			a.logger.Warn("operation redelivered with a different payload digest", "operation", task.GetOperationId())
			sendAck(false, "OPERATION_DIGEST_MISMATCH")
			return
		}
		if entry.Status == "succeeded" || entry.Status == "failed" {
			// 终态重放：不重复执行副作用，直接重放已持久化的结果。
			sendAck(true, "")
			a.replayJournalResult(outbound, task, entry)
			return
		}
		// running：等待执行中的结果（重投恢复）。
		sendAck(true, "")
		a.awaitInflight(ctx, outbound, task, digest)
		return
	}

	execution, isNew := a.registerInflight(task.GetOperationId())
	if !isNew {
		// 并发重投：等待同一次执行的结果。
		sendAck(true, "")
		a.awaitInflight(ctx, outbound, task, digest)
		return
	}
	defer a.unregisterInflight(task.GetOperationId())

	if err := a.journal.RecordRunning(task.GetOperationId(), digest, task.GetAttempt()); err != nil {
		if errors.Is(err, ErrJournalDigestMismatch) {
			sendAck(false, "OPERATION_DIGEST_MISMATCH")
			return
		}
		a.logger.Warn("journal record running failed", "operation", task.GetOperationId(), "error", err)
	}
	sendAck(true, "")

	// 每服务器互斥：同服务器任务串行，避免并发写同一数据目录。
	unlock := a.keyedLock(task.GetServerId())
	unlock.Lock()
	defer unlock.Unlock()

	// 有栅栏的任务在执行期每 10 秒续租，防止长任务（备份/恢复）在 30 秒
	// 租约窗口内被对账重新入队造成重复执行。
	stopRenew := make(chan struct{})
	defer close(stopRenew)
	if leaseToken != "" {
		go func() {
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-stopRenew:
					return
				case <-ctx.Done():
					return
				case <-ticker.C:
					heartbeat := &agentv1.RunningTaskHeartbeat{
						OperationId:     task.GetOperationId(),
						Attempt:         task.GetAttempt(),
						ObservedAt:      timestamppb.Now(),
						LeaseToken:      leaseToken,
						ConnectionEpoch: connectionEpoch,
					}
					if err := outbound.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_RunningTaskHeartbeat{RunningTaskHeartbeat: heartbeat}}); err != nil {
						return
					}
				}
			}
		}()
	}

	var outcome *ExecutionOutcome
	if err := resolveTaskSecretHandles(ctx, gateway, task); err != nil {
		outcome = &ExecutionOutcome{Succeeded: false, ErrorCode: "SECRET_HANDLE_FAILED", Retryable: true}
	} else {
		outcome, err = a.executor.ExecuteTask(ctx, task)
		if err != nil || outcome == nil {
			outcome = &ExecutionOutcome{Succeeded: false, ErrorCode: "EXECUTION_ERROR", Retryable: true}
		}
	}

	// 先持久化终态（含观测结果），再回发：任务结果不可丢，重投可重放。
	var observedRaw []byte
	if outcome.Observed != nil {
		observedRaw, _ = protojson.MarshalOptions{UseProtoNames: true}.Marshal(outcome.Observed)
	}
	if err := a.journal.Complete(task.GetOperationId(), digest, task.GetAttempt(), outcome.Succeeded, outcome.ErrorCode, outcome.Retryable, "", outcome.ResultJSON, observedRaw); err != nil {
		a.logger.Warn("journal complete failed", "operation", task.GetOperationId(), "error", err)
	}
	execution.complete(inflightResult{
		succeeded:  outcome.Succeeded,
		errorCode:  outcome.ErrorCode,
		retryable:  outcome.Retryable,
		resultJSON: outcome.ResultJSON,
		observed:   observedRaw,
	})
	sendResult := func() {
		frame := &agentv1.TaskResult{
			OperationId:     task.GetOperationId(),
			Succeeded:       outcome.Succeeded,
			ErrorCode:       outcome.ErrorCode,
			Retryable:       outcome.Retryable,
			ResultJson:      outcome.ResultJSON,
			Attempt:         task.GetAttempt(),
			LeaseToken:      leaseToken,
			ConnectionEpoch: connectionEpoch,
		}
		if err := outbound.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_TaskResult{TaskResult: frame}}); err != nil {
			a.logger.Warn("send task result failed", "operation", task.GetOperationId(), "error", err)
		}
	}
	sendResult()
	if outcome.Observed != nil {
		if err := outbound.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_ServerObserved{ServerObserved: outcome.Observed}}); err != nil {
			a.logger.Warn("send server observed failed", "operation", task.GetOperationId(), "error", err)
		}
	}
}

// replayJournalResult 重放操作日志里的终态结果，不触碰执行器。
func (a *agent) replayJournalResult(outbound connectRequestSender, task *agentv1.Task, entry journalEntry) {
	succeeded := entry.Status == "succeeded"
	errorCode := entry.ErrorCode
	if succeeded {
		errorCode = ""
	} else if errorCode == "" {
		errorCode = "OPERATION_FAILED"
	}
	frame := &agentv1.TaskResult{
		OperationId:     task.GetOperationId(),
		Succeeded:       succeeded,
		ErrorCode:       errorCode,
		Retryable:       entry.Retryable,
		ResultJson:      entry.ResultJSON,
		Attempt:         task.GetAttempt(),
		LeaseToken:      task.GetLeaseToken(),
		ConnectionEpoch: task.GetConnectionEpoch(),
	}
	if err := outbound.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_TaskResult{TaskResult: frame}}); err != nil {
		a.logger.Warn("replay task result failed", "operation", task.GetOperationId(), "error", err)
		return
	}
	if len(entry.Observed) > 0 {
		var observed agentv1.ServerObserved
		if err := protojson.Unmarshal(entry.Observed, &observed); err == nil {
			_ = outbound.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_ServerObserved{ServerObserved: &observed}})
		}
	}
	a.logger.Info("operation result replayed from journal", "operation", task.GetOperationId(), "status", entry.Status)
}

// awaitInflight 等待执行中的同 operation 结果，并把终态回发给服务端。
func (a *agent) awaitInflight(ctx context.Context, outbound connectRequestSender, task *agentv1.Task, digest string) {
	a.inflightMu.Lock()
	execution := a.inflight[task.GetOperationId()]
	a.inflightMu.Unlock()
	if execution == nil {
		// 登记已消失（执行刚结束）：重查日志，有终态则重放；仍为 running
		// 说明 Agent 在副作用未知时重启，安全地终止而不是重复执行。
		if entry, ok, err := a.journal.Lookup(task.GetOperationId()); err == nil && ok && entry.Digest == digest {
			if entry.Status == "succeeded" || entry.Status == "failed" {
				a.replayJournalResult(outbound, task, entry)
				return
			}
			const recoveryCode = "AGENT_RESTARTED_DURING_OPERATION"
			resultJSON, _ := json.Marshal(map[string]any{"code": recoveryCode})
			if completeErr := a.journal.Complete(task.GetOperationId(), digest, task.GetAttempt(), false, recoveryCode, false, "failed", resultJSON, nil); completeErr != nil {
				a.logger.Warn("journal recovery completion failed", "operation", task.GetOperationId(), "error", completeErr)
			}
			a.sendTaskResult(outbound, task, false, recoveryCode, false, resultJSON)
		}
		return
	}
	select {
	case <-execution.done:
		result := execution.getResult()
		a.sendTaskResult(outbound, task, result.succeeded, result.errorCode, result.retryable, result.resultJSON)
		if len(result.observed) > 0 {
			var observed agentv1.ServerObserved
			if err := protojson.Unmarshal(result.observed, &observed); err == nil {
				_ = outbound.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_ServerObserved{ServerObserved: &observed}})
			}
		}
	case <-ctx.Done():
		return
	}
}

func (a *agent) sendTaskResult(outbound connectRequestSender, task *agentv1.Task, succeeded bool, errorCode string, retryable bool, resultJSON []byte) {
	if !succeeded && errorCode == "" {
		errorCode = "OPERATION_FAILED"
	}
	frame := &agentv1.TaskResult{
		OperationId:     task.GetOperationId(),
		Succeeded:       succeeded,
		ErrorCode:       errorCode,
		Retryable:       retryable,
		ResultJson:      resultJSON,
		Attempt:         task.GetAttempt(),
		LeaseToken:      task.GetLeaseToken(),
		ConnectionEpoch: task.GetConnectionEpoch(),
	}
	if err := outbound.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_TaskResult{TaskResult: frame}}); err != nil {
		a.logger.Warn("send task result failed", "operation", task.GetOperationId(), "error", err)
	}
}

func resolveTaskSecretHandles(ctx context.Context, gateway agentv1.AgentGatewayServiceClient, task *agentv1.Task) error {
	if task == nil || task.GetType() != "provision" || gateway == nil {
		return nil
	}
	payload := task.GetProvision()
	legacy := false
	if payload == nil && len(task.GetPayloadJson()) > 0 {
		payload = &agentv1.ProvisionTaskPayload{}
		if err := protojson.Unmarshal(task.GetPayloadJson(), payload); err != nil {
			return fmt.Errorf("decode provision payload: %w", err)
		}
		legacy = true
	}
	if payload == nil {
		return nil
	}
	for key, value := range payload.GetVariables() {
		if !strings.HasPrefix(value, "sh:v1:") {
			continue
		}
		resolved, err := gateway.ResolveSecret(ctx, &agentv1.ResolveSecretRequest{
			OperationId: task.GetOperationId(), ServerId: task.GetServerId(), Handle: value,
		})
		if err != nil {
			return fmt.Errorf("resolve startup secret %q: %w", key, err)
		}
		payload.Variables[key] = resolved.GetValue()
	}
	if legacy {
		encoded, err := protojson.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode resolved provision payload: %w", err)
		}
		task.Payload = &agentv1.Task_PayloadJson{PayloadJson: encoded}
	}
	return nil
}

// heartbeatLoop 按 Welcome 指定间隔发送 Heartbeat，直到会话结束或 ctx 取消。
// 心跳携带主机资源快照（内存/磁盘总量与可用量），供 Control Plane 落库并
// 参与节点容量校验；探测失败时相关字段为零值。
func (a *agent) heartbeatLoop(ctx context.Context, outbound connectRequestSender, interval time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			stats := collectHostStats(a.cfg.DataRoot)
			hb := &agentv1.Heartbeat{
				ObservedAt:           timestamppb.Now(),
				AgentVersion:         a.cfg.AgentVersion,
				MemoryTotalBytes:     uint64(stats.MemoryTotalBytes),
				MemoryAvailableBytes: uint64(stats.MemoryAvailableBytes),
				DiskTotalBytes:       uint64(stats.DiskTotalBytes),
				DiskAvailableBytes:   uint64(stats.DiskAvailableBytes),
			}
			if err := outbound.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_Heartbeat{Heartbeat: hb}}); err != nil {
				return
			}
		}
	}
}

// rotateCertificate 响应 RotateCertificate 下行：生成新密钥 + CSR 并经
// 当前流上报；Control Plane 随后以 CertificateResponse 返回新证书。
func (a *agent) rotateCertificate(ctx context.Context, outbound connectRequestSender) error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("generate rotation key: %w", err)
	}
	csrPEM, err := buildCSR(key, a.cfg.NodeName)
	if err != nil {
		return fmt.Errorf("build rotation csr: %w", err)
	}
	req := &agentv1.CertificateSigningRequest{
		RequestId:                 fmt.Sprintf("rot-%d", time.Now().UnixNano()),
		CertificateSigningRequest: csrPEM,
		CurrentCertificateSerial:  a.currentSerial(),
	}
	if err := outbound.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_CertificateSigningRequest{CertificateSigningRequest: req}}); err != nil {
		return fmt.Errorf("send rotation csr: %w", err)
	}
	a.rotationKey = key
	a.rotationID = req.RequestId
	return nil
}

// applyCertificateResponse 落盘轮换后的证书/信任根，并更新内存中的 TLS 证书。
func (a *agent) applyCertificateResponse(resp *agentv1.CertificateResponse) error {
	if a.rotationID != "" && resp.GetRequestId() != "" && resp.GetRequestId() != a.rotationID {
		return fmt.Errorf("rotation request id mismatch: got %q want %q", resp.GetRequestId(), a.rotationID)
	}
	if len(resp.GetCertificateChain()) == 0 {
		return fmt.Errorf("empty certificate chain in certificate response")
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(a.rotationKey)})
	if err := os.WriteFile(filepath.Join(a.cfg.CertDir, certFile), resp.GetCertificateChain(), 0o600); err != nil {
		return fmt.Errorf("write rotated cert: %w", err)
	}
	if err := os.WriteFile(filepath.Join(a.cfg.CertDir, keyFile), keyPEM, 0o600); err != nil {
		return fmt.Errorf("write rotated key: %w", err)
	}
	if len(resp.GetCaCertificate()) > 0 {
		if err := a.persistTrustRoot(resp.GetCaCertificate()); err != nil {
			return fmt.Errorf("write rotated trust root: %w", err)
		}
	}
	pair, err := tls.X509KeyPair(resp.GetCertificateChain(), keyPEM)
	if err != nil {
		return fmt.Errorf("load rotated key pair: %w", err)
	}
	a.cert = pair
	a.rotationKey = nil
	a.rotationID = ""
	a.logger.Info("certificate rotated")
	return nil
}

// persistTrustRoot keeps a pre-provisioned trust root authoritative. Enrollment
// responses repeat the same root, so a read-only secret mount is valid; a
// different root is rejected and must be rotated through deployment config.
func (a *agent) persistTrustRoot(contents []byte) error {
	if strings.TrimSpace(a.cfg.TrustRootPath) == "" {
		return errors.New("trust root path is empty")
	}
	if existing, err := os.ReadFile(a.cfg.TrustRootPath); err == nil {
		if sameCertificatePEM(existing, contents) {
			return nil
		}
		return errors.New("pre-provisioned trust root differs from server root")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.cfg.TrustRootPath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(a.cfg.TrustRootPath, contents, 0o644)
}

func sameCertificatePEM(left, right []byte) bool {
	leftBlock, _ := pem.Decode(left)
	rightBlock, _ := pem.Decode(right)
	if leftBlock == nil || rightBlock == nil || leftBlock.Type != "CERTIFICATE" || rightBlock.Type != "CERTIFICATE" {
		return bytes.Equal(left, right)
	}
	leftCert, leftErr := x509.ParseCertificate(leftBlock.Bytes)
	rightCert, rightErr := x509.ParseCertificate(rightBlock.Bytes)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCert.Raw, rightCert.Raw)
}

// currentSerial 读取当前证书的序列号（用于轮换请求；缺失时返回空串）。
func (a *agent) currentSerial() string {
	raw, err := os.ReadFile(filepath.Join(a.cfg.CertDir, certFile))
	if err != nil {
		return ""
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return ""
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return ""
	}
	return cert.SerialNumber.String()
}

// handleConsoleCommand 执行控制台命令，并把命令与输出作为 stdout 行经 LogBatch 回显，
// 让控制面日志缓冲出现一条「命令 + 结果」的记录。
func (a *agent) handleConsoleCommand(ctx context.Context, outbound connectRequestSender, cmd *agentv1.ConsoleCommand) {
	if cmd == nil {
		return
	}
	outcome, err := a.executor.ExecuteConsoleCommand(ctx, cmd.GetServerId(), cmd.GetCommand())
	if err != nil || outcome == nil {
		outcome = &ExecutionOutcome{Succeeded: false, ErrorCode: "EXECUTION_ERROR", Retryable: false}
	}
	result := &agentv1.ConsoleCommandResult{
		RequestId: cmd.GetRequestId(), ServerId: cmd.GetServerId(),
		Succeeded: outcome.Succeeded, ErrorCode: outcome.ErrorCode, Retryable: outcome.Retryable,
	}
	if sendErr := outbound.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_ConsoleCommandResult{ConsoleCommandResult: result}}); sendErr != nil {
		a.logger.Warn("send console command result", "request", cmd.GetRequestId(), "error", sendErr)
		return
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
	if err := outbound.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_LogBatch{LogBatch: &agentv1.LogBatch{
		ServerId: cmd.GetServerId(), FirstSequence: uint64(a.nextSequence(cmd.GetServerId())), Lines: []string{"> " + cmd.GetCommand(), echo},
	}}}); err != nil {
		a.logger.Warn("send console echo", "error", err)
	}
}

// fileOperator 是执行器可选实现的文件操作能力。DockerExecutor 实现此接口；
// 测试 fake 可选择不实现，此时文件请求返回 UNSUPPORTED。
type fileOperator interface {
	ExecuteFileOperation(ctx context.Context, req *agentv1.FileOperationRequest) *agentv1.FileOperationResponse
}

// handleFileOperation 执行容器内文件操作并回发结构化响应。
func (a *agent) handleFileOperation(ctx context.Context, outbound connectRequestSender, req *agentv1.FileOperationRequest) {
	if req == nil {
		return
	}
	var resp *agentv1.FileOperationResponse
	fop, ok := a.executor.(fileOperator)
	if !ok {
		resp = &agentv1.FileOperationResponse{RequestId: req.GetRequestId(), Succeeded: false, ErrorCode: "UNSUPPORTED"}
	} else {
		resp = fop.ExecuteFileOperation(ctx, req)
		if resp == nil {
			resp = &agentv1.FileOperationResponse{RequestId: req.GetRequestId(), Succeeded: false, ErrorCode: "FILE_OPERATION_FAILED"}
		}
		resp.RequestId = req.GetRequestId()
	}

	if err := outbound.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_FileOperationResponse{FileOperationResponse: resp}}); err != nil {
		a.logger.Warn("send file operation response", "request", req.GetRequestId(), "error", err)
	}
}

// nextSequence 为每台服务器分配一对单调递增的日志序号（返回首个序号）。
func (a *agent) nextSequence(serverID string) int64 {
	a.seqMu.Lock()
	defer a.seqMu.Unlock()
	seq := a.sequences[serverID] + 1
	a.sequences[serverID] = seq + 1
	return seq
}

// startLogTailers 为每台运行中的服务器容器启动日志流式 tailer。
func (a *agent) startLogTailers(ctx context.Context, outbound connectRequestSender) {
	servers, err := a.executor.ListRunningServers(ctx)
	if err != nil {
		a.logger.Debug("list running servers for log tailers", "error", err)
		return
	}
	for _, serverID := range servers {
		a.startLogTailer(ctx, outbound, serverID)
	}
}

// startLogTailer 在独立 goroutine 中跟随容器日志，按行分组成批上报。
// 上报被限流：每 250ms 最多一批 64 行；缓冲已满时丢弃行并计入 dropped_lines，
// 绝不阻塞 Docker 日志读取或挤占任务结果所需的 outbound 带宽。
func (a *agent) startLogTailer(ctx context.Context, outbound connectRequestSender, serverID string) {
	go func() {
		rt, err := a.executor.Runtime()
		if err != nil {
			return
		}
		reader, err := rt.FollowLogs(ctx, fmt.Sprintf("gugu-server-%s", serverID))
		if err != nil {
			a.logger.Debug("follow container logs", "server", serverID, "error", err)
			return
		}
		defer reader.Close()

		lines := make(chan string, 256)
		var dropped atomic.Uint64
		go func() {
			defer close(lines)
			scanner := bufio.NewScanner(reader)
			scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for scanner.Scan() {
				// docker 日志可能包含非法 UTF-8（ANSI 控制序列、乱码字节），
				// gRPC proto string 字段要求合法 UTF-8，发送前统一替换。
				select {
				case lines <- strings.ToValidUTF8(scanner.Text(), "\uFFFD"):
				case <-ctx.Done():
					return
				default:
					// 限流保护：缓冲满即丢行，不阻塞日志读取。
					dropped.Add(1)
				}
			}
		}()

		var batch []string
		seq := a.nextSequence(serverID)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		flush := func() {
			if len(batch) == 0 && dropped.Load() == 0 {
				return
			}
			_ = outbound.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_LogBatch{LogBatch: &agentv1.LogBatch{
				ServerId: serverID, FirstSequence: uint64(seq), Lines: batch, DroppedLines: dropped.Swap(0),
			}}})
			seq += int64(len(batch))
			batch = batch[:0]
		}
		for {
			select {
			case <-ctx.Done():
				flush()
				return
			case <-ticker.C:
				flush()
			case line, ok := <-lines:
				if !ok {
					flush()
					return
				}
				batch = append(batch, line)
				if len(batch) >= 64 {
					flush()
				}
			}
		}
	}()
}

// startMetricSampler 每 5 秒采集运行中容器的资源与玩家数并上报。
func (a *agent) startMetricSampler(ctx context.Context, outbound connectRequestSender) {
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
				_ = outbound.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_MetricsBatch{MetricsBatch: batch}})
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
		a.logger.Debug("collect container stats", "server", serverID, "error", err)
		return nil
	}
	env, err := rt.InspectEnv(ctx, containerName)
	if err != nil {
		a.logger.Debug("inspect container env", "server", serverID, "error", err)
		return nil
	}
	m := &agentv1.ServerMetrics{
		ServerId:         serverID,
		CpuPercent:       stats.CPUPercent,
		MemoryBytes:      stats.MemoryBytes,
		MemoryLimitBytes: stats.MemoryLimitBytes,
		NetworkRxBytes:   stats.NetworkRxBytes,
		NetworkTxBytes:   stats.NetworkTxBytes,
	}
	if password := env["RCON_PASSWORD"]; password != "" {
		if output, execErr := rt.ExecInContainer(ctx, containerName, []string{"rcon-cli", "--host", "127.0.0.1", "--port", "25575", "--password", password, "list"}); execErr == nil {
			online, max := parsePlayersFromRCON(output)
			m.PlayersOnline = uint32(online)
			m.PlayersMax = uint32(max)
		}
	}
	return m
}
