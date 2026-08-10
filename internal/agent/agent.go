package agent

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/gugumanager/gugumanager/api/proto/gugumanager/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/encoding/protojson"
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
		if err := os.WriteFile(a.cfg.TrustRootPath, resp.GetCaCertificate(), 0o644); err != nil {
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

// enroll 调用 Control Plane 的 Enroll RPC。首次注册尚无信任根，
// 因此跳过服务器证书校验；凭据落地后 Connect 切换为完整 mTLS 校验。
func (a *agent) enroll(ctx context.Context, csrPEM []byte, opts runOptions) (*agentv1.EnrollResponse, error) {
	tlsCfg := &tls.Config{
		ServerName: opts.serverName,
		MinVersion: tls.VersionTLS12,
	}
	if pool := a.rootPool(); pool != nil {
		tlsCfg.RootCAs = pool
	} else {
		tlsCfg.InsecureSkipVerify = true
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

func defaultCapabilities() []*agentv1.Capability {
	return []*agentv1.Capability{
		{Name: "runtime.docker", Version: "1"},
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
	tlsCfg := &tls.Config{
		ServerName:   opts.serverName,
		MinVersion:   tls.VersionTLS12,
		RootCAs:      a.rootPool(),
		Certificates: []tls.Certificate{a.cert},
	}
	if tlsCfg.RootCAs == nil {
		return fmt.Errorf("trust root not available at %s", a.cfg.TrustRootPath)
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
	if err := stream.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{
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

	// 日志流式上报与指标采样：连接建立后即启动，会话结束随 ctx 取消。
	go a.startMetricSampler(ctx, stream)
	a.startLogTailers(ctx, stream)

	interval := time.Duration(welcome.GetHeartbeatIntervalSeconds()) * time.Second
	if interval <= 0 {
		interval = defaultHeartbeatInterval
	}
	stopHB := make(chan struct{})
	defer close(stopHB)
	go a.heartbeatLoop(ctx, stream, interval, stopHB)

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
			if err := a.handleTask(ctx, stream, client, p.Task); err != nil {
				return err
			}
		case *agentv1.ConnectResponse_ConsoleCommand:
			a.handleConsoleCommand(ctx, stream, p.ConsoleCommand)
		case *agentv1.ConnectResponse_RotateCertificate:
			if err := a.rotateCertificate(ctx, stream); err != nil {
				return fmt.Errorf("rotate certificate: %w", err)
			}
		case *agentv1.ConnectResponse_Drain:
			a.logger.Info("drain requested", "reason", p.Drain.GetReason())
		case *agentv1.ConnectResponse_CertificateResponse:
			if err := a.applyCertificateResponse(p.CertificateResponse); err != nil {
				a.logger.Warn("failed to apply rotated certificate", "error", err)
			}
		case *agentv1.ConnectResponse_FileOperationRequest:
			a.handleFileOperation(ctx, stream, p.FileOperationRequest)
		}
	}
}

// handleTask 处理下行任务：先回 TaskAck，执行后回 TaskResult（含 Observed）。
func (a *agent) handleTask(ctx context.Context, stream agentv1.AgentGatewayService_ConnectClient, gateway agentv1.AgentGatewayServiceClient, task *agentv1.Task) error {
	ack := &agentv1.TaskAck{
		OperationId: task.GetOperationId(),
		Accepted:    true,
		Attempt:     task.GetAttempt(),
	}
	if err := stream.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_TaskAck{TaskAck: ack}}); err != nil {
		return fmt.Errorf("send task ack: %w", err)
	}

	if err := resolveTaskSecretHandles(ctx, gateway, task); err != nil {
		outcome := &ExecutionOutcome{Succeeded: false, ErrorCode: "SECRET_HANDLE_FAILED", Retryable: true}
		result := &agentv1.TaskResult{OperationId: task.GetOperationId(), Succeeded: false, ErrorCode: outcome.ErrorCode, Retryable: outcome.Retryable, Attempt: task.GetAttempt()}
		if sendErr := stream.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_TaskResult{TaskResult: result}}); sendErr != nil {
			return fmt.Errorf("send secret handle failure: %w", sendErr)
		}
		return nil
	}
	outcome, err := a.executor.ExecuteTask(ctx, task)
	if err != nil || outcome == nil {
		outcome = &ExecutionOutcome{Succeeded: false, ErrorCode: "EXECUTION_ERROR", Retryable: true}
	}
	result := &agentv1.TaskResult{
		OperationId: task.GetOperationId(),
		Succeeded:   outcome.Succeeded,
		ErrorCode:   outcome.ErrorCode,
		Retryable:   outcome.Retryable,
		ResultJson:  outcome.ResultJSON,
		Attempt:     task.GetAttempt(),
	}
	if err := stream.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_TaskResult{TaskResult: result}}); err != nil {
		return fmt.Errorf("send task result: %w", err)
	}
	if outcome.Observed != nil {
		if err := stream.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_ServerObserved{ServerObserved: outcome.Observed}}); err != nil {
			return fmt.Errorf("send server observed: %w", err)
		}
	}
	return nil
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
func (a *agent) heartbeatLoop(ctx context.Context, stream agentv1.AgentGatewayService_ConnectClient, interval time.Duration, stop <-chan struct{}) {
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
			if err := stream.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_Heartbeat{Heartbeat: hb}}); err != nil {
				return
			}
		}
	}
}

// rotateCertificate 响应 RotateCertificate 下行：生成新密钥 + CSR 并经
// 当前流上报；Control Plane 随后以 CertificateResponse 返回新证书。
func (a *agent) rotateCertificate(ctx context.Context, stream agentv1.AgentGatewayService_ConnectClient) error {
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
	if err := stream.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_CertificateSigningRequest{CertificateSigningRequest: req}}); err != nil {
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
		if err := os.WriteFile(a.cfg.TrustRootPath, resp.GetCaCertificate(), 0o644); err != nil {
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
func (a *agent) handleFileOperation(ctx context.Context, stream agentv1.AgentGatewayService_ConnectClient, req *agentv1.FileOperationRequest) {
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

	if err := stream.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_FileOperationResponse{FileOperationResponse: resp}}); err != nil {
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
func (a *agent) startLogTailers(ctx context.Context, stream agentv1.AgentGatewayService_ConnectClient) {
	servers, err := a.executor.ListRunningServers(ctx)
	if err != nil {
		a.logger.Debug("list running servers for log tailers", "error", err)
		return
	}
	for _, serverID := range servers {
		a.startLogTailer(ctx, stream, serverID)
	}
}

// startLogTailer 在独立 goroutine 中跟随容器日志，按行分组成批上报。
func (a *agent) startLogTailer(ctx context.Context, stream agentv1.AgentGatewayService_ConnectClient, serverID string) {
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
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var batch []string
		seq := a.nextSequence(serverID)
		flush := func() {
			if len(batch) == 0 {
				return
			}
			_ = stream.Send(&agentv1.ConnectRequest{Payload: &agentv1.ConnectRequest_LogBatch{LogBatch: &agentv1.LogBatch{
				ServerId: serverID, FirstSequence: uint64(seq), Lines: batch,
			}}})
			seq += int64(len(batch))
			batch = batch[:0]
		}
		for scanner.Scan() {
			// docker 日志可能包含非法 UTF-8（ANSI 控制序列、乱码字节），
			// gRPC proto string 字段要求合法 UTF-8，发送前统一替换。
			batch = append(batch, strings.ToValidUTF8(scanner.Text(), "\uFFFD"))
			if len(batch) >= 64 {
				flush()
			}
		}
		flush()
	}()
}

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
