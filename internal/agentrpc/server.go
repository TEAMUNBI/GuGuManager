// Package agentrpc 实现 Control Plane 的 Agent gRPC 服务器：
// mTLS 握手、Enroll（注册 + 签发证书）与 Connect 双向流（心跳、任务下发、状态回报）。
package agentrpc

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/gugumanager/gugumanager/api/proto/gugumanager/agent/v1"
	"github.com/gugumanager/gugumanager/internal/agentca"
	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/id"
	"github.com/gugumanager/gugumanager/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	protocolVersion       = "v1"
	enrollCertTTL         = 24 * time.Hour
	serverCertTTL         = 24 * time.Hour
	heartbeatInterval     = 10 * time.Second
	maxInFlightTasks      = 3
	defaultClaimPeriod    = 2 * time.Second
	consoleCommandTimeout = 10 * time.Second
	nodeOutboundQueueSize = 64
	// maxAgentMessageBytes 放宽单帧消息上限，容纳备份下载等大 payload
	// （备份归档以 base64 回传时体积约为原始大小的 4/3）。
	maxAgentMessageBytes = 512 << 20
)

// TaskStore 是 gRPC server 依赖的 Store 最小接口。任务消息必须携带
// TaskLeaseFence（operation、node、connection epoch、attempt、lease token）；
// 栅栏不匹配或任务已终态时，Ack/Progress/Renew/Complete 都只成为 no-op。
type TaskStore interface {
	RegisterNode(ctx context.Context, node domain.Node) (string, error)
	NodeByID(ctx context.Context, nodeID string) (domain.Node, error)
	EnqueueTask(ctx context.Context, serverID, nodeID, taskType string, generation int64, actorID string, idemKey string, requestDigest []byte) (string, error)
	ClaimTask(ctx context.Context, nodeID string, connectionEpoch int64) (*store.ClaimedTask, error)
	AckTask(ctx context.Context, fence store.TaskLeaseFence, accepted bool, errCode string) error
	ReportTaskProgress(ctx context.Context, fence store.TaskLeaseFence, percent int, checkpoint string) error
	RenewTaskLease(ctx context.Context, fence store.TaskLeaseFence) error
	CompleteTask(ctx context.Context, fence store.TaskLeaseFence, succeeded bool, errCode *string, resultJSON []byte) error
	RecordAgentHeartbeat(ctx context.Context, nodeID string, hb domain.Heartbeat) error
	ApplyServerObserved(ctx context.Context, obs domain.ServerObserved) error
	RecordAudit(ctx context.Context, event domain.AuditEvent) error
	RecordConsoleLines(ctx context.Context, serverID string, lines []domain.ConsoleLine) error
	ApplyServerMetrics(ctx context.Context, metrics []domain.ServerMetrics) error
}

type secretHandleStore interface {
	ResolveSecretHandle(ctx context.Context, operationID, serverID, nodeID, handle string) (string, time.Time, error)
}

// Option 配置 Server。
type Option func(*Server)

// WithRegistrationToken 启用 Enroll 的注册令牌校验；token 为空时跳过校验（开发模式）。
func WithRegistrationToken(token string) Option {
	return func(s *Server) {
		s.registrationToken = token
	}
}

// WithClaimPeriod 覆盖任务轮询间隔（测试可缩短）。
func WithClaimPeriod(period time.Duration) Option {
	return func(s *Server) {
		s.claimPeriod = period
	}
}

// WithServerIPs 追加服务器证书的 IP SAN。面板位于 NAT/端口转发之后时，
// Agent 用公网 IP 校验证书，而监听地址只是内网 IP，必须显式补充公网 IP。
func WithServerIPs(ips []net.IP) Option {
	return func(s *Server) {
		s.extraServerIPs = append(s.extraServerIPs, ips...)
	}
}

// nodeStream 是节点当前 Connect 连接的发送句柄。注册进 Server.streams，
// 供 SendConsoleCommand 在流上直接下发命令帧。
type nodeStream struct {
	nodeID string
	// send is owned exclusively by the outbound writer. Callers enqueue frames
	// through sendFrame so one blocked transport send cannot block the HTTP
	// request goroutine or create one goroutine per attempted command.
	send     func(*agentv1.ConnectResponse) error
	done     chan struct{}
	doneOnce sync.Once

	outbound   chan nodeOutboundFrame
	writerOnce sync.Once
	writerDone chan struct{}

	consoleMu      sync.Mutex
	consolePending map[string]consolePending

	// 文件操作请求-响应关联：request_id → 响应通道。
	fileMu      sync.Mutex
	filePending map[string]chan *agentv1.FileOperationResponse
}

type consolePending struct {
	serverID string
	result   chan domain.ConsoleCommandResult
}

type nodeOutboundFrame struct {
	ctx    context.Context
	frame  *agentv1.ConnectResponse
	result chan error
}

func (f nodeOutboundFrame) complete(err error) {
	select {
	case f.result <- err:
	default:
	}
}

// startWriter starts exactly one bounded outbound worker for this connection.
// A transport Send may block until the gRPC stream is torn down, but it can
// consume at most this one lifecycle goroutine; callers remain bounded by their
// own context and the fixed-size queue provides backpressure.
func (s *nodeStream) startWriter() {
	s.writerOnce.Do(func() {
		go func() {
			defer close(s.writerDone)
			for {
				select {
				case <-s.done:
					return
				case outbound := <-s.outbound:
					if err := outbound.ctx.Err(); err != nil {
						outbound.complete(err)
						continue
					}
					select {
					case <-s.done:
						outbound.complete(errNodeStreamClosed)
						return
					default:
					}
					if s.send == nil {
						outbound.complete(errNodeStreamClosed)
						s.close()
						return
					}
					err := s.send(outbound.frame)
					outbound.complete(err)
					if err != nil {
						s.close()
						return
					}
				}
			}
		}()
	})
}

var errNodeStreamClosed = errors.New("node connect stream is closed")

// sendFrame enqueues a frame and waits for either its transport result, the
// caller deadline, or disconnect. It never launches a per-request goroutine.
func (s *nodeStream) sendFrame(ctx context.Context, frame *agentv1.ConnectResponse) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	outbound := nodeOutboundFrame{ctx: ctx, frame: frame, result: make(chan error, 1)}
	select {
	case <-s.done:
		return errNodeStreamClosed
	case <-ctx.Done():
		return ctx.Err()
	case s.outbound <- outbound:
	}
	select {
	case err := <-outbound.result:
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	case <-s.done:
		return errNodeStreamClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *nodeStream) close() {
	s.doneOnce.Do(func() {
		if s.done != nil {
			close(s.done)
		}
	})
}

// Server 是 AgentGatewayService 的实现。
type Server struct {
	agentv1.UnimplementedAgentGatewayServiceServer

	ca                *agentca.CA
	store             TaskStore
	log               *slog.Logger
	registrationToken string
	claimPeriod       time.Duration
	consoleTimeout    time.Duration
	extraServerIPs    []net.IP

	streamMu sync.Mutex
	streams  map[string]*nodeStream

	// connectionEpochs 为每个节点维护单调递增的连接序号。每次 Connect 会话
	// 领取一个新 epoch；claim 的任务绑定该 epoch，Ack/Progress/Heartbeat/
	// Result 必须原样回显，旧连接的迟到消息因此无法影响新连接的任务。
	epochMu          sync.Mutex
	connectionEpochs map[string]int64
}

// NewServer 构造 Agent gRPC 服务器。
func NewServer(ca *agentca.CA, store TaskStore, logger *slog.Logger, opts ...Option) *Server {
	s := &Server{
		ca:               ca,
		store:            store,
		log:              logger,
		claimPeriod:      defaultClaimPeriod,
		consoleTimeout:   consoleCommandTimeout,
		streams:          make(map[string]*nodeStream),
		connectionEpochs: make(map[string]int64),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// nextConnectionEpoch 为节点的新连接分配严格递增的 epoch（首个为 1）。
// 任务 fence 中 epoch=0 永远匹配不到新行，因此旧协议消息只能走 legacy 校验。
func (s *Server) nextConnectionEpoch(nodeID string) int64 {
	s.epochMu.Lock()
	defer s.epochMu.Unlock()
	s.connectionEpochs[nodeID]++
	return s.connectionEpochs[nodeID]
}

// withConsoleCommandTimeout shortens the hard command deadline in tests. The
// production default remains consoleCommandTimeout (10 seconds).
func withConsoleCommandTimeout(timeout time.Duration) Option {
	return func(s *Server) {
		if timeout > 0 {
			s.consoleTimeout = timeout
		}
	}
}

// registerStream 记录节点当前连接，供控制面命令下发定位。
func (s *Server) registerStream(stream *nodeStream) {
	if stream.done == nil {
		stream.done = make(chan struct{})
	}
	if stream.outbound == nil {
		stream.outbound = make(chan nodeOutboundFrame, nodeOutboundQueueSize)
	}
	if stream.writerDone == nil {
		stream.writerDone = make(chan struct{})
	}
	if stream.consolePending == nil {
		stream.consolePending = make(map[string]consolePending)
	}
	stream.startWriter()
	s.streamMu.Lock()
	previous := s.streams[stream.nodeID]
	s.streams[stream.nodeID] = stream
	s.streamMu.Unlock()
	if previous != nil && previous != stream {
		previous.close()
	}
}

// unregisterStream 在 Connect 退出时移除节点连接。
func (s *Server) unregisterStream(stream *nodeStream) {
	s.streamMu.Lock()
	if s.streams[stream.nodeID] == stream {
		delete(s.streams, stream.nodeID)
	}
	s.streamMu.Unlock()
	stream.close()
}

// SendConsoleCommand dispatches one command and waits for the correlated Agent
// result. It fails closed on timeout, disconnect, a mismatched response, or an
// Agent error code.
func (s *Server) SendConsoleCommand(ctx context.Context, nodeID, serverID, command string) (domain.ConsoleCommandResult, error) {
	requestID := id.New()
	baseResult := domain.ConsoleCommandResult{RequestID: requestID, ServerID: serverID}
	s.streamMu.Lock()
	stream := s.streams[nodeID]
	s.streamMu.Unlock()
	if stream == nil {
		baseResult.ErrorCode = "NODE_OFFLINE"
		baseResult.Retryable = true
		return baseResult, domain.NewProblem("NODE_OFFLINE", "node has no active connect stream", true)
	}
	ctx, cancel := context.WithTimeout(ctx, s.consoleTimeout)
	defer cancel()
	resultCh := make(chan domain.ConsoleCommandResult, 1)
	stream.consoleMu.Lock()
	stream.consolePending[requestID] = consolePending{serverID: serverID, result: resultCh}
	stream.consoleMu.Unlock()
	defer func() {
		stream.consoleMu.Lock()
		delete(stream.consolePending, requestID)
		stream.consoleMu.Unlock()
	}()

	if err := stream.sendFrame(ctx, &agentv1.ConnectResponse{Payload: &agentv1.ConnectResponse_ConsoleCommand{
		ConsoleCommand: &agentv1.ConsoleCommand{RequestId: requestID, ServerId: serverID, Command: command},
	}}); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			// The frame may already be inside a blocked transport Send. Retire the
			// connection so the RPC handler cancels that Send and the command cannot
			// be delivered later after the HTTP deadline has expired.
			stream.close()
			baseResult.ErrorCode = "CONSOLE_TIMEOUT"
			baseResult.Retryable = true
			return baseResult, domain.NewProblem("CONSOLE_TIMEOUT", "console command timed out", true)
		}
		baseResult.ErrorCode = "NODE_OFFLINE"
		baseResult.Retryable = true
		return baseResult, domain.NewProblem("NODE_OFFLINE", "failed to send console command", true)
	}

	select {
	case result := <-resultCh:
		if ctx.Err() != nil {
			baseResult.ErrorCode = "CONSOLE_TIMEOUT"
			baseResult.Retryable = true
			return baseResult, domain.NewProblem("CONSOLE_TIMEOUT", "console command timed out", true)
		}
		if result.Succeeded {
			return result, nil
		}
		code := result.ErrorCode
		if code == "" {
			code = "COMMAND_FAILED"
		}
		problem := domain.NewProblem(code, "console command was rejected by the runtime", result.Retryable)
		problem.Details["requestId"] = result.RequestID
		return result, problem
	case <-stream.done:
		baseResult.ErrorCode = "NODE_OFFLINE"
		baseResult.Retryable = true
		return baseResult, domain.NewProblem("NODE_OFFLINE", "node disconnected before console command completed", true)
	case <-ctx.Done():
		baseResult.ErrorCode = "CONSOLE_TIMEOUT"
		baseResult.Retryable = true
		return baseResult, domain.NewProblem("CONSOLE_TIMEOUT", "console command timed out", true)
	}
}

// completeConsoleCommand only accepts a result from the current node stream
// when both correlation identifiers match the pending request.
func (s *Server) completeConsoleCommand(stream *nodeStream, resp *agentv1.ConsoleCommandResult) {
	if stream == nil || resp == nil || resp.GetRequestId() == "" {
		return
	}
	stream.consoleMu.Lock()
	pending, ok := stream.consolePending[resp.GetRequestId()]
	stream.consoleMu.Unlock()
	if !ok || pending.serverID != resp.GetServerId() {
		return
	}
	result := domain.ConsoleCommandResult{
		RequestID: resp.GetRequestId(), ServerID: resp.GetServerId(),
		Succeeded: resp.GetSucceeded(), ErrorCode: resp.GetErrorCode(), Retryable: resp.GetRetryable(),
	}
	select {
	case pending.result <- result:
	default:
	}
}

// DispatchFileOperation 在指定节点的 Connect 流上发送文件操作请求并等待结构化响应。
// 请求必须设置 ServerId；若 RequestId 为空则自动生成。调用方通过 ctx 控制超时。
func (s *Server) DispatchFileOperation(ctx context.Context, nodeID string, req *agentv1.FileOperationRequest) (*agentv1.FileOperationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("file operation request is nil")
	}
	s.streamMu.Lock()
	stream := s.streams[nodeID]
	s.streamMu.Unlock()
	if stream == nil {
		return nil, fmt.Errorf("node %s has no active connect stream", nodeID)
	}

	requestID := req.GetRequestId()
	if requestID == "" {
		requestID = id.New()
		req.RequestId = requestID
	}

	ch := make(chan *agentv1.FileOperationResponse, 1)
	stream.fileMu.Lock()
	if stream.filePending == nil {
		stream.filePending = make(map[string]chan *agentv1.FileOperationResponse)
	}
	stream.filePending[requestID] = ch
	stream.fileMu.Unlock()
	defer func() {
		stream.fileMu.Lock()
		delete(stream.filePending, requestID)
		stream.fileMu.Unlock()
	}()

	if err := stream.sendFrame(ctx, &agentv1.ConnectResponse{Payload: &agentv1.ConnectResponse_FileOperationRequest{
		FileOperationRequest: req,
	}}); err != nil {
		return nil, fmt.Errorf("send file operation request: %w", err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-stream.done:
		return nil, fmt.Errorf("node %s disconnected during file operation", nodeID)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// completeFileOperation 将 Agent 回发的文件操作响应投递给等待中的 DispatchFileOperation 调用。
func (s *Server) completeFileOperation(nodeID string, resp *agentv1.FileOperationResponse) {
	if resp == nil {
		return
	}
	s.streamMu.Lock()
	stream := s.streams[nodeID]
	s.streamMu.Unlock()
	if stream == nil {
		return
	}
	stream.fileMu.Lock()
	ch, ok := stream.filePending[resp.GetRequestId()]
	stream.fileMu.Unlock()
	if ok {
		select {
		case ch <- resp:
		default:
		}
	}
}

// Enroll 校验注册令牌、注册节点，并为其签发 24h 的 client auth 证书。
func (s *Server) Enroll(ctx context.Context, req *agentv1.EnrollRequest) (*agentv1.EnrollResponse, error) {
	if s.registrationToken != "" {
		provided := []byte(req.GetRegistrationToken())
		expected := []byte(s.registrationToken)
		if subtle.ConstantTimeCompare(provided, expected) != 1 {
			return nil, status.Error(codes.PermissionDenied, "invalid registration token")
		}
	}
	nodeName := strings.TrimSpace(req.GetNodeName())
	if nodeName == "" {
		return nil, status.Error(codes.InvalidArgument, "node name is required")
	}

	// Enroll 阶段节点尚未上报资源，RegisterNode 要求正整数，先用占位值，
	// 首次 Heartbeat 由 RecordAgentHeartbeat 覆盖为真实资源。
	node := domain.Node{
		Name:        nodeName,
		Version:     req.GetAgentVersion(),
		Condition:   "available",
		Region:      "unknown",
		CPUCores:    1,
		MemoryBytes: 1,
		DiskBytes:   1,
	}
	for _, capability := range req.GetCapabilities() {
		if capability == nil {
			continue
		}
		if canonical, ok := domain.CanonicalNodeCapability(capability.GetName(), capability.GetVersion()); ok {
			node.Capabilities = append(node.Capabilities, canonical)
		}
	}

	nodeID, err := s.store.RegisterNode(ctx, node)
	if err != nil {
		s.log.Error("enroll: register node", "error", err)
		return nil, status.Error(codes.Internal, "failed to register node")
	}

	// 优先基于 Agent 提交的 CSR 公钥签发证书，保证其私钥可配对；
	// 未携带 CSR 时回退到服务器生成的密钥（测试与工具路径）。
	var certPEM []byte
	if csr := req.GetCertificateSigningRequest(); len(csr) > 0 {
		certPEM, err = s.ca.IssueAgentCertificateFromCSR(csr, nodeID, enrollCertTTL)
		if err != nil {
			s.log.Error("enroll: issue certificate from csr", "node", nodeID, "error", err)
			return nil, status.Error(codes.InvalidArgument, "invalid certificate signing request")
		}
	} else {
		certPEM, err = s.ca.IssueAgentCertificate(nodeID, enrollCertTTL)
		if err != nil {
			s.log.Error("enroll: issue certificate", "node", nodeID, "error", err)
			return nil, status.Error(codes.Internal, "failed to issue certificate")
		}
	}
	rootPEM, err := s.ca.RootCAPEM()
	if err != nil {
		s.log.Error("enroll: root ca pem", "error", err)
		return nil, status.Error(codes.Internal, "failed to read root ca")
	}

	s.log.Info("node enrolled", "node", nodeID, "name", nodeName)
	return &agentv1.EnrollResponse{
		NodeId:           nodeID,
		CertificateChain: certPEM,
		CaCertificate:    rootPEM,
		ExpiresAt:        timestamppb.New(time.Now().UTC().Add(enrollCertTTL)),
	}, nil
}

// ResolveSecret exchanges a short-lived, one-time handle for the matching
// startup value. The mTLS peer identity supplies the node binding; callers
// cannot select another node by changing the request body.
func (s *Server) ResolveSecret(ctx context.Context, req *agentv1.ResolveSecretRequest) (*agentv1.ResolveSecretResponse, error) {
	if req == nil || req.GetOperationId() == "" || req.GetServerId() == "" || req.GetHandle() == "" {
		return nil, status.Error(codes.InvalidArgument, "operation_id, server_id, and handle are required")
	}
	nodeID, err := s.peerNodeID(ctx)
	if err != nil {
		return nil, err
	}
	resolver, ok := s.store.(secretHandleStore)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "secret handles are unavailable")
	}
	value, expiresAt, err := resolver.ResolveSecretHandle(ctx, req.GetOperationId(), req.GetServerId(), nodeID, req.GetHandle())
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, "secret handle rejected")
	}
	return &agentv1.ResolveSecretResponse{Value: value, ExpiresAt: timestamppb.New(expiresAt)}, nil
}

// ListenAndServe 启动 mTLS gRPC 服务器。tlsCert/tlsKey 为 nil 时自动用 CA
// 签发一张 CN="control-plane" 的 server auth 证书。ctx 取消时优雅停机。
func (s *Server) ListenAndServe(ctx context.Context, addr string, tlsCert []byte, tlsKey []byte) error {
	tlsConfig, err := s.tlsConfig(addr, tlsCert, tlsKey)
	if err != nil {
		return err
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	gs := s.newGRPCServer(tlsConfig)
	s.register(gs)

	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			gs.GracefulStop()
		case <-stopped:
		}
	}()
	defer close(stopped)

	s.log.Info("agent gRPC listening", "addr", addr)
	return gs.Serve(lis)
}

// register 把 AgentGatewayService 注册到 grpc server。
func (s *Server) register(gs grpc.ServiceRegistrar) {
	agentv1.RegisterAgentGatewayServiceServer(gs, s)
}

// newGRPCServer 构造带 mTLS 凭证的 grpc.Server。消息大小上限放宽到
// maxAgentMessageBytes，支撑备份下载等大帧双向传输。
func (s *Server) newGRPCServer(tlsConfig *tls.Config) *grpc.Server {
	return grpc.NewServer(
		grpc.Creds(credentials.NewTLS(tlsConfig)),
		grpc.MaxRecvMsgSize(maxAgentMessageBytes),
		grpc.MaxSendMsgSize(maxAgentMessageBytes),
	)
}

// tlsConfig 组装 mTLS 配置：服务端证书（缺省自动签发）+ 要求并校验客户端证书
// （客户端证书必须由本 CA 签发，验证链与 client auth 用途）。
func (s *Server) tlsConfig(addr string, certPEM, keyPEM []byte) (*tls.Config, error) {
	if len(certPEM) == 0 {
		var err error
		// 服务器证书携带监听地址的 IP SAN，使 Agent 用 IP 而非仅 DNS 名校验证书；
		// WithServerIPs 补充的公网 IP（NAT/端口转发场景）一并加入。
		sanIPs := listenerIPs(addr)
		sanIPs = append(sanIPs, s.extraServerIPs...)
		certPEM, keyPEM, err = s.ca.IssueServerCertificateWithSAN(serverCertTTL, sanIPs)
		if err != nil {
			return nil, fmt.Errorf("issue server certificate: %w", err)
		}
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("load server key pair: %w", err)
	}
	rootPEM, err := s.ca.RootCAPEM()
	if err != nil {
		return nil, fmt.Errorf("root ca pem: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(rootPEM) {
		return nil, errors.New("failed to append root ca to client pool")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{pair},
		// TLS 层请求但不强制客户端证书：首次 Enroll 时 Agent 尚无证书
		// （Enroll 正是为签发证书），必须允许匿名 TLS 连接；
		// Connect 双向流在应用层用 verifyPeerNode 强制 mTLS 校验。
		ClientAuth: tls.VerifyClientCertIfGiven,
		ClientCAs:  pool,
		MinVersion: tls.VersionTLS12,
	}, nil
}

// verifyPeerNode 校验连接对端证书：链由本 CA 签发，且 CN 与声明的 nodeID 一致。
func (s *Server) verifyPeerNode(ctx context.Context, nodeID string) error {
	actual, err := s.peerNodeID(ctx)
	if err != nil {
		return err
	}
	if actual != nodeID {
		return status.Errorf(codes.PermissionDenied, "certificate CN %q does not match node id %q", actual, nodeID)
	}
	return nil
}

func (s *Server) peerNodeID(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok || p.AuthInfo == nil {
		return "", status.Error(codes.Unauthenticated, "missing client authentication")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
		return "", status.Error(codes.Unauthenticated, "client certificate required")
	}
	cert := tlsInfo.State.PeerCertificates[0]
	if err := s.ca.VerifyCertificateChain(pemEncodeCert(cert.Raw)); err != nil {
		return "", status.Error(codes.Unauthenticated, "client certificate not issued by trusted ca")
	}
	return cert.Subject.CommonName, nil
}

func pemEncodeCert(raw []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})
}

// listenerIPs 从 host:port 监听地址提取 IP（host 为空或非 IP 时返回 nil，
// 例如 host 为域名或 0.0.0.0 时交给 DNS SAN / 证书默认行为）。
func listenerIPs(addr string) []net.IP {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	return []net.IP{ip}
}
