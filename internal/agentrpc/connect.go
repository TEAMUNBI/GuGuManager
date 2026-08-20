package agentrpc

import (
	"context"
	"io"
	"time"

	agentv1 "github.com/gugumanager/gugumanager/api/proto/gugumanager/agent/v1"
	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
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
	// 已吊销或已删除的节点禁止建立新连接。
	if _, err := s.store.NodeByID(stream.Context(), nodeID); err != nil {
		s.log.Warn("node revoked or unknown; rejecting connect", "node", nodeID)
		return status.Error(codes.PermissionDenied, "node is revoked or unknown")
	}

	ctx := stream.Context()
	nodeConnection := &nodeStream{
		nodeID: nodeID, send: stream.Send, done: make(chan struct{}),
		consolePending: make(map[string]consolePending),
		filePending:    make(map[string]chan *agentv1.FileOperationResponse),
	}
	s.registerStream(nodeConnection)
	defer s.unregisterStream(nodeConnection)
	send := func(resp *agentv1.ConnectResponse) error {
		return nodeConnection.sendFrame(ctx, resp)
	}

	if err := send(&agentv1.ConnectResponse{Payload: &agentv1.ConnectResponse_Welcome{Welcome: &agentv1.Welcome{
		ProtocolVersion:          protocolVersion,
		HeartbeatIntervalSeconds: uint32(heartbeatInterval / time.Second),
		MaxInFlightTasks:         maxInFlightTasks,
	}}}); err != nil {
		return err
	}
	s.log.Info("agent connected", "node", nodeID, "version", hello.Hello.GetAgentVersion())

	// 证书剩余有效期 ≤ 20% 时立即请求轮换（Agent 生成新 CSR，回发后
	// CertificateResponse 落地新证书，下一次连接生效）。
	if cert := s.peerCertificate(ctx); cert != nil {
		if remaining := time.Until(cert.NotAfter); remaining > 0 && remaining <= enrollCertTTL/5 {
			if err := send(&agentv1.ConnectResponse{Payload: &agentv1.ConnectResponse_RotateCertificate{RotateCertificate: &agentv1.RotateCertificate{RotateBefore: timestamppb.New(cert.NotAfter)}}}); err != nil {
				return err
			}
			s.log.Info("agent certificate nearing expiry; requesting rotation", "node", nodeID, "remaining", remaining.Round(time.Second))
		}
	}

	// 每个 Connect 会话领取一个新 epoch；本会话 claim 的任务绑定该 epoch，
	// Agent 必须在 Ack/Progress/Heartbeat/Result 中原样回显。
	connectionEpoch := s.nextConnectionEpoch(nodeID)
	go s.claimLoop(ctx, nodeID, connectionEpoch, send)
	type receivedFrame struct {
		request *agentv1.ConnectRequest
		err     error
	}
	received := make(chan receivedFrame, 1)
	// Recv has no caller-controlled deadline. Keep exactly one receive pump per
	// connection so replacing/closing a nodeStream can return this RPC handler,
	// cancel the gRPC stream context, and thereby unblock its outbound writer.
	go func() {
		for {
			request, recvErr := stream.Recv()
			select {
			case received <- receivedFrame{request: request, err: recvErr}:
			case <-ctx.Done():
				return
			case <-nodeConnection.done:
				return
			}
			if recvErr != nil {
				return
			}
		}
	}()

	for {
		var incoming receivedFrame
		select {
		case <-nodeConnection.done:
			return status.Error(codes.Unavailable, "node connect stream closed")
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		case incoming = <-received:
		}
		if incoming.err == io.EOF {
			return nil
		}
		if incoming.err != nil {
			return incoming.err
		}
		req := incoming.request
		switch p := req.Payload.(type) {
		case *agentv1.ConnectRequest_Hello:
			// 重复 Hello 忽略。
			s.log.Debug("duplicate hello", "node", nodeID)
		case *agentv1.ConnectRequest_Heartbeat:
			s.handleHeartbeat(ctx, nodeID, p.Heartbeat)
			if s.nodeRevoked(ctx, nodeID) {
				// 吊销立即断开：心跳是最快的周期检查点。
				return status.Error(codes.PermissionDenied, "node revoked")
			}
		case *agentv1.ConnectRequest_TaskResult:
			s.handleTaskResult(ctx, nodeID, p.TaskResult)
		case *agentv1.ConnectRequest_ServerObserved:
			s.handleServerObserved(ctx, nodeID, p.ServerObserved)
		case *agentv1.ConnectRequest_CertificateSigningRequest:
			s.handleCertificateRotation(ctx, nodeID, p.CertificateSigningRequest, send)
		case *agentv1.ConnectRequest_TaskAck:
			s.handleTaskAck(ctx, nodeID, p.TaskAck)
		case *agentv1.ConnectRequest_TaskProgress:
			s.handleTaskProgress(ctx, nodeID, p.TaskProgress)
		case *agentv1.ConnectRequest_RunningTaskHeartbeat:
			s.handleRunningTaskHeartbeat(ctx, nodeID, p.RunningTaskHeartbeat)
		case *agentv1.ConnectRequest_LogBatch:
			s.handleLogBatch(ctx, nodeID, p.LogBatch)
		case *agentv1.ConnectRequest_MetricsBatch:
			s.handleMetricsBatch(ctx, nodeID, p.MetricsBatch)
		case *agentv1.ConnectRequest_FileOperationResponse:
			s.completeFileOperation(nodeID, p.FileOperationResponse)
		case *agentv1.ConnectRequest_ConsoleCommandResult:
			s.completeConsoleCommand(nodeConnection, p.ConsoleCommandResult)
		default:
			s.log.Warn("unknown connect frame", "node", nodeID)
		}
	}
}

// claimLoop 周期性为节点 claim 任务并通过流下发。ctx 取消或 Send 失败即退出。
// 领取到的任务绑定本连接的 epoch，Agent 回显的 fence 必须与之匹配。
func (s *Server) claimLoop(ctx context.Context, nodeID string, connectionEpoch int64, send func(*agentv1.ConnectResponse) error) {
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
			task, err := s.store.ClaimTask(ctx, nodeID, connectionEpoch)
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
			s.log.Info("task dispatched", "node", nodeID, "operation", task.OperationID, "type", task.TaskType, "epoch", connectionEpoch, "attempt", task.Attempt)
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

// handleTaskAck 应用 Agent 的 TaskAck：fence 匹配时 leased -> running，
// 拒绝时终态失败；fence 不匹配或已终态则静默成为 no-op。
func (s *Server) handleTaskAck(ctx context.Context, nodeID string, ack *agentv1.TaskAck) {
	if ack == nil || ack.GetOperationId() == "" {
		return
	}
	fence := store.TaskLeaseFence{
		OperationID: ack.GetOperationId(),
		NodeID:      nodeID,
		Epoch:       int64(ack.GetConnectionEpoch()),
		Attempt:     int(ack.GetAttempt()),
		LeaseToken:  ack.GetLeaseToken(),
	}
	if err := s.store.AckTask(ctx, fence, ack.GetAccepted(), ack.GetErrorCode()); err != nil {
		s.log.Warn("ack task", "node", nodeID, "operation", fence.OperationID, "error", err)
		return
	}
	s.log.Debug("task ack", "node", nodeID, "operation", fence.OperationID, "accepted", ack.GetAccepted(), "epoch", fence.Epoch, "attempt", fence.Attempt)
}

// handleTaskProgress 应用 Agent 的进度帧：单调推进 progress/checkpoint 并续租。
func (s *Server) handleTaskProgress(ctx context.Context, nodeID string, progress *agentv1.TaskProgress) {
	if progress == nil || progress.GetOperationId() == "" {
		return
	}
	fence := store.TaskLeaseFence{
		OperationID: progress.GetOperationId(),
		NodeID:      nodeID,
		Epoch:       int64(progress.GetConnectionEpoch()),
		Attempt:     int(progress.GetAttempt()),
		LeaseToken:  progress.GetLeaseToken(),
	}
	if err := s.store.ReportTaskProgress(ctx, fence, int(progress.GetPercent()), progress.GetCheckpoint()); err != nil {
		s.log.Warn("task progress", "node", nodeID, "operation", fence.OperationID, "error", err)
		return
	}
	s.log.Debug("task progress", "node", nodeID, "operation", fence.OperationID, "percent", progress.GetPercent())
}

// handleRunningTaskHeartbeat 续租运行中任务的租约；旧 attempt/旧连接的
// 心跳在 store 层被 fence 判为 stale，不会复活已重新入队的任务。
func (s *Server) handleRunningTaskHeartbeat(ctx context.Context, nodeID string, heartbeat *agentv1.RunningTaskHeartbeat) {
	if heartbeat == nil || heartbeat.GetOperationId() == "" {
		return
	}
	fence := store.TaskLeaseFence{
		OperationID: heartbeat.GetOperationId(),
		NodeID:      nodeID,
		Epoch:       int64(heartbeat.GetConnectionEpoch()),
		Attempt:     int(heartbeat.GetAttempt()),
		LeaseToken:  heartbeat.GetLeaseToken(),
	}
	if err := s.store.RenewTaskLease(ctx, fence); err != nil {
		s.log.Warn("renew task lease", "node", nodeID, "operation", fence.OperationID, "error", err)
		return
	}
	s.log.Debug("running task heartbeat", "node", nodeID, "operation", fence.OperationID)
}

func (s *Server) handleTaskResult(ctx context.Context, nodeID string, result *agentv1.TaskResult) {
	if result == nil || result.GetOperationId() == "" {
		return
	}
	var errCode *string
	if code := result.GetErrorCode(); code != "" {
		errCode = &code
	}
	fence := store.TaskLeaseFence{
		OperationID: result.GetOperationId(),
		NodeID:      nodeID,
		Epoch:       int64(result.GetConnectionEpoch()),
		Attempt:     int(result.GetAttempt()),
		LeaseToken:  result.GetLeaseToken(),
	}
	if err := s.store.CompleteTask(ctx, fence, result.GetSucceeded(), errCode, result.GetResultJson()); err != nil {
		s.log.Warn("complete task", "node", nodeID, "operation", result.GetOperationId(), "error", err)
	}
}

// handleLogBatch 把 Agent 上报的日志行写入服务器控制台缓冲。
func (s *Server) handleLogBatch(ctx context.Context, nodeID string, batch *agentv1.LogBatch) {
	if batch == nil {
		return
	}
	lines := make([]domain.ConsoleLine, 0, len(batch.GetLines()))
	seq := int64(batch.GetFirstSequence())
	for i, message := range batch.GetLines() {
		lines = append(lines, domain.ConsoleLine{
			Sequence:  seq + int64(i),
			Timestamp: time.Now().UTC(),
			Stream:    "stdout",
			Message:   message,
		})
	}
	if err := s.store.RecordConsoleLines(ctx, batch.GetServerId(), lines); err != nil {
		s.log.Warn("record console lines", "node", nodeID, "server", batch.GetServerId(), "error", err)
	}
}

// handleMetricsBatch 把 Agent 上报的服务器指标写入指标状态。
func (s *Server) handleMetricsBatch(ctx context.Context, nodeID string, batch *agentv1.MetricsBatch) {
	if batch == nil {
		return
	}
	observedAt := time.Now().UTC()
	if ts := batch.GetObservedAt(); ts != nil {
		observedAt = ts.AsTime()
	}
	metrics := make([]domain.ServerMetrics, 0, len(batch.GetServers()))
	for _, m := range batch.GetServers() {
		if m == nil {
			continue
		}
		metrics = append(metrics, domain.ServerMetrics{
			ServerID:           m.GetServerId(),
			ObservedGeneration: int64(m.GetObservedGeneration()),
			CPUPercent:         m.GetCpuPercent(),
			MemoryBytes:        int64(m.GetMemoryBytes()),
			MemoryLimitBytes:   int64(m.GetMemoryLimitBytes()),
			DiskBytes:          int64(m.GetDiskBytes()),
			DiskLimitBytes:     int64(m.GetDiskLimitBytes()),
			NetworkRxBytes:     int64(m.GetNetworkRxBytes()),
			NetworkTxBytes:     int64(m.GetNetworkTxBytes()),
			PlayersOnline:      int(m.GetPlayersOnline()),
			PlayersMax:         int(m.GetPlayersMax()),
			ObservedAt:         observedAt,
		})
	}
	if err := s.store.ApplyServerMetrics(ctx, metrics); err != nil {
		s.log.Warn("apply server metrics", "node", nodeID, "error", err)
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
// 电源类任务在 server_tasks 中类型为 start/stop/restart/kill，Agent 执行器
// 只按 provision/power 分发，因此下发前归一化为 Type="power" 并填充 Power
// 字段（含动作与优雅停机超时）。000009 起新任务输入位于 task_input 并只
// 通过 typed arm 下发（provision/backup/reconcile），不再创建新的 payload_json 任务；
// pre-000009 的旧行仍以 payload_json 兼容当前稳定 Agent。
func claimedTaskToProto(task *store.ClaimedTask) *agentv1.Task {
	proto := &agentv1.Task{
		OperationId:     task.OperationID,
		ServerId:        task.ServerID,
		Generation:      uint64(task.Generation),
		Type:            task.TaskType,
		Attempt:         uint32(task.Attempt),
		LeaseToken:      task.LeaseToken,
		ConnectionEpoch: uint64(task.ConnectionEpoch),
	}
	if action, ok := powerActionForTaskType(task.TaskType); ok {
		proto.Type = "power"
		proto.Payload = &agentv1.Task_Power{
			Power: &agentv1.PowerTaskPayload{
				Action:                 action,
				GracefulTimeoutSeconds: 30,
			},
		}
		return proto
	}
	if task.TaskType == "provision" && len(task.TaskInputJSON) > 0 {
		payload := &agentv1.ProvisionTaskPayload{}
		if err := protojson.Unmarshal(task.TaskInputJSON, payload); err == nil {
			proto.BundleDigest = payload.GetBundleDigest()
			proto.Payload = &agentv1.Task_Provision{Provision: payload}
			return proto
		}
		s2 := *task
		proto.Payload = &agentv1.Task_PayloadJson{PayloadJson: s2.PayloadJSON}
		return proto
	}
	if task.TaskType == "reconcile" && len(task.TaskInputJSON) > 0 {
		payload := &agentv1.ReconcileTaskPayload{}
		if err := protojson.Unmarshal(task.TaskInputJSON, payload); err == nil {
			if payload.GetDesired() != nil {
				proto.BundleDigest = payload.GetDesired().GetBundleDigest()
			}
			proto.Payload = &agentv1.Task_Reconcile{Reconcile: payload}
			return proto
		}
		// A corrupt current-format row must remain visible to the Agent as an
		// invalid legacy payload so it fails deterministically instead of being
		// mistaken for an empty desired spec.
		proto.Payload = &agentv1.Task_PayloadJson{PayloadJson: task.TaskInputJSON}
		return proto
	}
	if task.TaskType == "backup" || task.TaskType == "restore" || task.TaskType == "backup-delete" {
		proto.Type = "backup"
		payload := &agentv1.BackupTaskPayload{}
		decoded := false
		if input := task.InputJSON(); len(input) > 0 {
			switch task.TaskType {
			case "backup":
				create := &agentv1.CreateBackupPayload{}
				if err := protojson.Unmarshal(input, create); err == nil {
					payload.Action = &agentv1.BackupTaskPayload_Create{Create: create}
					decoded = true
				}
			case "restore":
				restore := &agentv1.RestoreBackupPayload{}
				if err := protojson.Unmarshal(input, restore); err == nil {
					payload.Action = &agentv1.BackupTaskPayload_Restore{Restore: restore}
					decoded = true
				}
			case "backup-delete":
				del := &agentv1.DeleteBackupPayload{}
				if err := protojson.Unmarshal(input, del); err == nil {
					payload.Action = &agentv1.BackupTaskPayload_Delete{Delete: del}
					decoded = true
				}
			}
		}
		if decoded {
			proto.Payload = &agentv1.Task_Backup{Backup: payload}
			return proto
		}
		// 无法解析的旧行回退 legacy 载体，由 Agent 侧自行解码。
		proto.Payload = &agentv1.Task_PayloadJson{PayloadJson: task.PayloadJSON}
		return proto
	}
	// 其余任务类型（如 extension 占位）维持 legacy 载体。
	proto.Payload = &agentv1.Task_PayloadJson{PayloadJson: task.PayloadJSON}
	return proto
}

// powerActionForTaskType 把 server_tasks 中的电源操作类型映射为下发给
// Agent 的 PowerAction；非电源类型返回 ok=false。
func powerActionForTaskType(taskType string) (agentv1.PowerAction, bool) {
	switch taskType {
	case "start":
		return agentv1.PowerAction_POWER_ACTION_START, true
	case "stop":
		return agentv1.PowerAction_POWER_ACTION_STOP, true
	case "restart":
		return agentv1.PowerAction_POWER_ACTION_RESTART, true
	case "kill":
		return agentv1.PowerAction_POWER_ACTION_KILL, true
	default:
		return agentv1.PowerAction_POWER_ACTION_UNSPECIFIED, false
	}
}
