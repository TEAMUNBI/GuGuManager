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
	"time"

	agentv1 "github.com/gugumanager/gugumanager/api/proto/gugumanager/agent/v1"
	"github.com/gugumanager/gugumanager/internal/agentca"
	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	protocolVersion    = "v1"
	enrollCertTTL      = 24 * time.Hour
	serverCertTTL      = 24 * time.Hour
	heartbeatInterval  = 10 * time.Second
	maxInFlightTasks   = 3
	defaultClaimPeriod = 2 * time.Second
)

// TaskStore 是 gRPC server 依赖的 Store 最小接口。
type TaskStore interface {
	RegisterNode(ctx context.Context, node domain.Node) (string, error)
	NodeByID(ctx context.Context, nodeID string) (domain.Node, error)
	EnqueueTask(ctx context.Context, serverID, nodeID, taskType string, generation int64, actorID string, idemKey string, requestDigest []byte) (string, error)
	ClaimTask(ctx context.Context, nodeID string) (*store.ClaimedTask, error)
	CompleteTask(ctx context.Context, operationID, nodeID string, succeeded bool, errCode *string) error
	RecordAgentHeartbeat(ctx context.Context, nodeID string, hb domain.Heartbeat) error
	ApplyServerObserved(ctx context.Context, obs domain.ServerObserved) error
	RecordAudit(ctx context.Context, event domain.AuditEvent) error
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

// Server 是 AgentGatewayService 的实现。
type Server struct {
	agentv1.UnimplementedAgentGatewayServiceServer

	ca               *agentca.CA
	store            TaskStore
	log              *slog.Logger
	registrationToken string
	claimPeriod      time.Duration
}

// NewServer 构造 Agent gRPC 服务器。
func NewServer(ca *agentca.CA, store TaskStore, logger *slog.Logger, opts ...Option) *Server {
	s := &Server{
		ca:          ca,
		store:       store,
		log:         logger,
		claimPeriod: defaultClaimPeriod,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
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
		if name := strings.TrimSpace(capability.Name); name != "" {
			node.Capabilities = append(node.Capabilities, name)
		}
	}

	nodeID, err := s.store.RegisterNode(ctx, node)
	if err != nil {
		s.log.Error("enroll: register node", "error", err)
		return nil, status.Error(codes.Internal, "failed to register node")
	}

	certPEM, err := s.ca.IssueAgentCertificate(nodeID, enrollCertTTL)
	if err != nil {
		s.log.Error("enroll: issue certificate", "node", nodeID, "error", err)
		return nil, status.Error(codes.Internal, "failed to issue certificate")
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

// ListenAndServe 启动 mTLS gRPC 服务器。tlsCert/tlsKey 为 nil 时自动用 CA
// 签发一张 CN="control-plane" 的 server auth 证书。ctx 取消时优雅停机。
func (s *Server) ListenAndServe(ctx context.Context, addr string, tlsCert []byte, tlsKey []byte) error {
	tlsConfig, err := s.tlsConfig(tlsCert, tlsKey)
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

// newGRPCServer 构造带 mTLS 凭证的 grpc.Server。
func (s *Server) newGRPCServer(tlsConfig *tls.Config) *grpc.Server {
	return grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsConfig)))
}

// tlsConfig 组装 mTLS 配置：服务端证书（缺省自动签发）+ 要求并校验客户端证书
// （客户端证书必须由本 CA 签发，验证链与 client auth 用途）。
func (s *Server) tlsConfig(certPEM, keyPEM []byte) (*tls.Config, error) {
	if len(certPEM) == 0 {
		var err error
		certPEM, keyPEM, err = s.ca.IssueServerCertificate(serverCertTTL)
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
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// verifyPeerNode 校验连接对端证书：链由本 CA 签发，且 CN 与声明的 nodeID 一致。
func (s *Server) verifyPeerNode(ctx context.Context, nodeID string) error {
	p, ok := peer.FromContext(ctx)
	if !ok || p.AuthInfo == nil {
		return status.Error(codes.Unauthenticated, "missing client authentication")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
		return status.Error(codes.Unauthenticated, "client certificate required")
	}
	cert := tlsInfo.State.PeerCertificates[0]
	if err := s.ca.VerifyCertificateChain(pemEncodeCert(cert.Raw)); err != nil {
		return status.Error(codes.Unauthenticated, "client certificate not issued by trusted ca")
	}
	if cert.Subject.CommonName != nodeID {
		return status.Errorf(codes.PermissionDenied, "certificate CN %q does not match node id %q", cert.Subject.CommonName, nodeID)
	}
	return nil
}

func pemEncodeCert(raw []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})
}
