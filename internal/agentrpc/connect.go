package agentrpc

import (
	"context"
	"io"
	"sync"
	"time"

	agentv1 "github.com/gugumanager/gugumanager/api/proto/gugumanager/agent/v1"
	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Connect 处理 Agent 的双向流：首帧必须是 Hello（校验证书 CN 与声明的
// nodeID 一致），随后在收到 Heartbeat / TaskResult / ServerObserved /
// CertificateSigningRequest 等帧时执行对应动作，并周期性 claim 任务下发。
func (s *Server) Connect(stream agentv1.AgentGatewayService_ConnectServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello, ok := first.Payload.(*agentv1.ConnectRequest_Hello)
	if !ok || hello.Hello == nil {
		return status.Error(codes.FailedPrecondition, "first frame must be hello")
	}
	nodeID := hello.Hello.GetNodeId()
	if nodeID == "" {
		return status.Error(codes.InvalidArgument, "hello.node_id is required")
	}
	if err := s.verifyPeerNode(stream.Context(), nodeID); err != nil {
		return err
	}

	// 单个连接的 Send 必须串行化：claim goroutine 与 Recv 循环并发调用 stream.Send。
	var sendMu sync.Mutex
	send := func(resp *agentv1.ConnectResponse) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(resp)
	}

	if err := send(&agentv1.ConnectResponse{Payload: &agentv1.ConnectResponse_Welcome{Welcome: &agentv1.Welcome{
		ProtocolVersion:          protocolVersion,
		HeartbeatIntervalSeconds: uint32(heartbeatInterval / time.Second),
		MaxInFlightTasks:         maxInFlightTasks,
	}}}); err != nil {
		return err
	}
	s.log.Info("agent connected", "node", nodeID, "version", hello.Hello.GetAgentVersion())

	ctx := stream.Context()
	go s.claimLoop(ctx, nodeID, send)

	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch p := req.Payload.(type) {
		case *agentv1.ConnectRequest_Hello:
			// 重复 Hello 忽略。
			s.log.Debug("duplicate hello", "node", nodeID)
		case *agentv1.ConnectRequest_Heartbeat:
			s.handleHeartbeat(ctx, nodeID, p.Heartbeat)
		case *agentv1.ConnectRequest_TaskResult:
			s.handleTaskResult(ctx, nodeID, p.TaskResult)
		case *agentv1.ConnectRequest_ServerObserved:
			s.handleServerObserved(ctx, nodeID, p.ServerObserved)
		case *agentv1.ConnectRequest_CertificateSigningRequest:
			s.handleCertificateRotation(ctx, nodeID, p.CertificateSigningRequest, send)
		case *agentv1.ConnectRequest_TaskAck:
			s.log.Debug("task ack", "node", nodeID, "operation", p.TaskAck.GetOperationId(), "accepted", p.TaskAck.GetAccepted())
		case *agentv1.ConnectRequest_TaskProgress:
			s.log.Debug("task progress", "node", nodeID, "operation", p.TaskProgress.GetOperationId(), "percent", p.TaskProgress.GetPercent())
		case *agentv1.ConnectRequest_RunningTaskHeartbeat:
			s.log.Debug("running task heartbeat", "node", nodeID, "operation", p.RunningTaskHeartbeat.GetOperationId())
		case *agentv1.ConnectRequest_LogBatch:
			s.log.Debug("log batch", "node", nodeID, "server", p.LogBatch.GetServerId(), "lines", len(p.LogBatch.GetLines()))
		case *agentv1.ConnectRequest_MetricsBatch:
			s.log.Debug("metrics batch", "node", nodeID, "servers", len(p.MetricsBatch.GetServers()))
		default:
			s.log.Warn("unknown connect frame", "node", nodeID)
		}
	}
}

// claimLoop 周期性为节点 claim 任务并通过流下发。ctx 取消或 Send 失败即退出。
func (s *Server) claimLoop(ctx context.Context, nodeID string, send func(*agentv1.ConnectResponse) error) {
	period := s.claimPeriod
	if period <= 0 {
		period = defaultClaimPeriod
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			task, err := s.store.ClaimTask(ctx, nodeID)
			if err != nil {
				s.log.Warn("claim task", "node", nodeID, "error", err)
				continue
			}
			if task == nil {
				continue
			}
			if err := send(&agentv1.ConnectResponse{Payload: &agentv1.ConnectResponse_Task{Task: claimedTaskToProto(task)}}); err != nil {
				s.log.Debug("send task", "node", nodeID, "operation", task.OperationID, "error", err)
				return
			}
			s.log.Info("task dispatched", "node", nodeID, "operation", task.OperationID, "type", task.TaskType)
		}
	}
}

func (s *Server) handleHeartbeat(ctx context.Context, nodeID string, hb *agentv1.Heartbeat) {
	if hb == nil {
		return
	}
	if err := s.store.RecordAgentHeartbeat(ctx, nodeID, heartbeatToDomain(nodeID, hb)); err != nil {
		s.log.Warn("record heartbeat", "node", nodeID, "error", err)
	}
}

func (s *Server) handleTaskResult(ctx context.Context, nodeID string, result *agentv1.TaskResult) {
	if result == nil {
		return
	}
	var errCode *string
	if code := result.GetErrorCode(); code != "" {
		errCode = &code
	}
	if err := s.store.CompleteTask(ctx, result.GetOperationId(), nodeID, result.GetSucceeded(), errCode); err != nil {
		s.log.Warn("complete task", "node", nodeID, "operation", result.GetOperationId(), "error", err)
	}
}

func (s *Server) handleServerObserved(ctx context.Context, nodeID string, obs *agentv1.ServerObserved) {
	if obs == nil {
		return
	}
	if err := s.store.ApplyServerObserved(ctx, serverObservedToDomain(obs)); err != nil {
		s.log.Warn("apply server observed", "node", nodeID, "server", obs.GetServerId(), "error", err)
	}
}

// handleCertificateRotation 应 Agent 的轮换请求签发新证书并回发。
// 与 Enroll 一致：优先基于轮换 CSR 的公钥签发，保证 Agent 新私钥可配对。
func (s *Server) handleCertificateRotation(ctx context.Context, nodeID string, csr *agentv1.CertificateSigningRequest, send func(*agentv1.ConnectResponse) error) {
	if csr == nil {
		return
	}
	var certPEM []byte
	var err error
	if raw := csr.GetCertificateSigningRequest(); len(raw) > 0 {
		certPEM, err = s.ca.IssueAgentCertificateFromCSR(raw, nodeID, enrollCertTTL)
	} else {
		certPEM, err = s.ca.IssueAgentCertificate(nodeID, enrollCertTTL)
	}
	if err != nil {
		s.log.Warn("rotate certificate", "node", nodeID, "error", err)
		return
	}
	rootPEM, err := s.ca.RootCAPEM()
	if err != nil {
		s.log.Warn("rotate certificate: root pem", "node", nodeID, "error", err)
		return
	}
	if err := send(&agentv1.ConnectResponse{Payload: &agentv1.ConnectResponse_CertificateResponse{CertificateResponse: &agentv1.CertificateResponse{
		RequestId:        csr.GetRequestId(),
		CertificateChain: certPEM,
		CaCertificate:    rootPEM,
		ExpiresAt:        timestamppb.New(time.Now().UTC().Add(enrollCertTTL)),
	}}}); err != nil {
		s.log.Debug("send certificate response", "node", nodeID, "error", err)
	}
	s.log.Info("certificate rotated", "node", nodeID)
}

func heartbeatToDomain(nodeID string, hb *agentv1.Heartbeat) domain.Heartbeat {
	observedAt := time.Now().UTC()
	if ts := hb.GetObservedAt(); ts != nil {
		observedAt = ts.AsTime()
	}
	var running []domain.RunningOperation
	for _, op := range hb.GetRunningOperations() {
		if op == nil {
			continue
		}
		running = append(running, domain.RunningOperation{
			OperationID: op.GetOperationId(),
			Checkpoint:  op.GetCheckpoint(),
			Attempt:     int(op.GetAttempt()),
			ServerID:    op.GetServerId(),
		})
	}
	return domain.Heartbeat{
		NodeID:               nodeID,
		MemoryTotalBytes:     int64(hb.GetMemoryTotalBytes()),
		MemoryAvailableBytes: int64(hb.GetMemoryAvailableBytes()),
		DiskTotalBytes:       int64(hb.GetDiskTotalBytes()),
		DiskAvailableBytes:   int64(hb.GetDiskAvailableBytes()),
		CPULoad:              hb.GetCpuLoad(),
		AgentVersion:         hb.GetAgentVersion(),
		ObservedAt:           observedAt,
		RunningOperations:    running,
	}
}

func serverObservedToDomain(obs *agentv1.ServerObserved) domain.ServerObserved {
	observedAt := time.Now().UTC()
	if ts := obs.GetObservedAt(); ts != nil {
		observedAt = ts.AsTime()
	}
	return domain.ServerObserved{
		ServerID:           obs.GetServerId(),
		ObservedGeneration: int64(obs.GetObservedGeneration()),
		ObservedPower:      observedPowerString(obs.GetObservedPower()),
		HealthCondition:    healthConditionString(obs.GetHealthCondition()),
		RuntimeID:          obs.GetRuntimeId(),
		BundleDigest:       obs.GetBundleDigest(),
		Detail:             obs.GetDetail(),
		ObservedAt:         observedAt,
	}
}

func observedPowerString(p agentv1.ObservedPower) string {
	switch p {
	case agentv1.ObservedPower_OBSERVED_POWER_UNKNOWN:
		return "unknown"
	case agentv1.ObservedPower_OBSERVED_POWER_STOPPED:
		return "stopped"
	case agentv1.ObservedPower_OBSERVED_POWER_STARTING:
		return "starting"
	case agentv1.ObservedPower_OBSERVED_POWER_RUNNING:
		return "running"
	case agentv1.ObservedPower_OBSERVED_POWER_STOPPING:
		return "stopping"
	default:
		return ""
	}
}

func healthConditionString(h agentv1.HealthCondition) string {
	switch h {
	case agentv1.HealthCondition_HEALTH_CONDITION_UNKNOWN:
		return "unknown"
	case agentv1.HealthCondition_HEALTH_CONDITION_HEALTHY:
		return "healthy"
	case agentv1.HealthCondition_HEALTH_CONDITION_UNHEALTHY:
		return "unhealthy"
	default:
		return ""
	}
}

// claimedTaskToProto 把 store 领取到的任务转换为下发用的 proto Task。
// 简化：payload 走 PayloadJson（deprecated 但可用），IdempotencyKey/Deadline
// 当前 server_tasks 未下发，置空。
func claimedTaskToProto(task *store.ClaimedTask) *agentv1.Task {
	return &agentv1.Task{
		OperationId: task.OperationID,
		ServerId:    task.ServerID,
		Generation:  uint64(task.Generation),
		Type:        task.TaskType,
		Attempt:     uint32(task.Attempt),
		Payload:     &agentv1.Task_PayloadJson{PayloadJson: task.PayloadJSON},
	}
}
