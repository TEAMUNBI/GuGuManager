package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/id"
)

func (m *Memory) initializeBackupChecksums() {
	for _, backups := range m.backups {
		for _, backup := range backups {
			if backup.Checksum != nil {
				m.backupChecksums[backup.ID] = *backup.Checksum
			}
		}
	}
}

func (m *Memory) CreateBackup(serverID string, idempotencyKey string, actor domain.User) (domain.Operation, error) {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return domain.Operation{}, err
	}
	digest := requestDigest(struct{}{})
	scope := idempotencyScope("backup:create", actor.ID, serverID, idempotencyKey)
	m.mu.Lock()
	currentActor, authErr := m.authorizeServerLocked(actor.ID, serverID, "servers.backups.create")
	if authErr != nil {
		m.mu.Unlock()
		return domain.Operation{}, authErr
	}
	m.reconcileNodeLivenessLocked(time.Now().UTC())
	if existing, ok, err := m.idempotentOperationLocked(scope, digest); err != nil || ok {
		m.mu.Unlock()
		return existing, err
	}
	server, ok := m.servers[serverID]
	if !ok {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	if m.nodes[server.NodeID].Condition != "available" {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("NODE_OFFLINE", "节点当前离线，无法创建备份", true)
	}
	if server.LifecycleState != "ready" {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("RESTORE_LOCKED", "服务器当前不可创建备份", false)
	}
	if active, ok := m.activeExclusiveOperationLocked(serverID); ok {
		if active.Type == domain.PowerAction("backup") {
			m.idempotency[scope] = idempotencyRecord{OperationID: active.ID, RequestDigest: digest}
			m.mu.Unlock()
			return active, nil
		}
		m.mu.Unlock()
		return domain.Operation{}, operationInProgress(active)
	}
	now := time.Now().UTC()
	operation := domain.NewQueuedOperation(id.New(), serverID, server.NodeID, domain.PowerAction("backup"), server.Generation, idempotencyKey, now)
	backupID := id.New()
	backup := domain.Backup{ID: backupID, Name: "manual-" + now.Format("20060102-150405"), Status: "creating", CreatedAt: now}
	m.backups[serverID] = append([]domain.Backup{backup}, m.backups[serverID]...)
	m.backupOperations[operation.ID] = backupID
	m.operations[operation.ID] = operation
	m.idempotency[scope] = idempotencyRecord{OperationID: operation.ID, RequestDigest: digest}
	m.mu.Unlock()
	m.recordAudit(currentActor.DisplayName, "backup.create", "server", server.Name, "accepted", operation.ID)
	go m.finishBackup(operation.ID, serverID, currentActor.DisplayName)
	return operation, nil
}

func (m *Memory) RestoreBackup(serverID string, backupID string, idempotencyKey string, actor domain.User) (domain.Operation, error) {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return domain.Operation{}, err
	}
	digest := requestDigest(struct {
		BackupID string `json:"backupId"`
	}{BackupID: backupID})
	scope := idempotencyScope("backup:restore", actor.ID, serverID+"/"+backupID, idempotencyKey)
	gate := m.fileMutationGate(serverID)
	gate.Lock()
	defer gate.Unlock()
	m.mu.Lock()
	currentActor, authErr := m.authorizeServerLocked(actor.ID, serverID, "servers.backups.restore")
	if authErr != nil {
		m.mu.Unlock()
		return domain.Operation{}, authErr
	}
	m.reconcileNodeLivenessLocked(time.Now().UTC())
	if existing, ok, err := m.idempotentOperationLocked(scope, digest); err != nil || ok {
		m.mu.Unlock()
		return existing, err
	}
	server, ok := m.servers[serverID]
	if !ok {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	if m.nodes[server.NodeID].Condition != "available" {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("NODE_OFFLINE", "节点当前离线，无法恢复备份", true)
	}
	backupIndex, backup, ok := m.backupLocked(serverID, backupID)
	if !ok || backup.Status == "deleted" {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "备份不存在", false)
	}
	if server.LifecycleState != "ready" || server.ObservedPower != "stopped" {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("RESTORE_LOCKED", "恢复备份前必须停止服务器", false)
	}
	if backup.Status != "ready" {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("RESTORE_LOCKED", "备份当前不可恢复", false)
	}
	if !m.backupChecksumValidLocked(backup) {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("BACKUP_INTEGRITY_FAILED", "备份摘要校验失败", false)
	}
	if active, ok := m.activeExclusiveOperationLocked(serverID); ok {
		m.mu.Unlock()
		return domain.Operation{}, operationInProgress(active)
	}
	now := time.Now().UTC()
	server.Generation++
	server.DesiredPower = "stopped"
	server.UpdatedAt = now
	m.servers[serverID] = server
	backup.Status = "restoring"
	m.backups[serverID][backupIndex] = backup
	operation := domain.NewQueuedOperation(id.New(), serverID, server.NodeID, domain.PowerAction("restore"), server.Generation, idempotencyKey, now)
	m.operations[operation.ID] = operation
	m.idempotency[scope] = idempotencyRecord{OperationID: operation.ID, RequestDigest: digest}
	m.mu.Unlock()
	m.recordAudit(currentActor.DisplayName, "backup.restore", "server", server.Name, "accepted", operation.ID)
	go m.finishRestoreBackup(operation.ID, serverID, backupID, currentActor.DisplayName)
	return operation, nil
}

func (m *Memory) DeleteBackup(serverID string, backupID string, idempotencyKey string, actor domain.User) (domain.Operation, error) {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return domain.Operation{}, err
	}
	digest := requestDigest(struct {
		BackupID string `json:"backupId"`
	}{BackupID: backupID})
	scope := idempotencyScope("backup:delete", actor.ID, serverID+"/"+backupID, idempotencyKey)
	m.mu.Lock()
	currentActor, authErr := m.authorizeServerLocked(actor.ID, serverID, "servers.backups.delete")
	if authErr != nil {
		m.mu.Unlock()
		return domain.Operation{}, authErr
	}
	if existing, ok, err := m.idempotentOperationLocked(scope, digest); err != nil || ok {
		m.mu.Unlock()
		return existing, err
	}
	server, ok := m.servers[serverID]
	if !ok {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	backupIndex, backup, ok := m.backupLocked(serverID, backupID)
	if !ok || backup.Status == "deleted" {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "备份不存在", false)
	}
	if !domain.BackupStatusTransitionAllowed(backup.Status, "deleting") {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("RESTORE_LOCKED", "备份当前不可删除", false)
	}
	if active, ok := m.activeExclusiveOperationLocked(serverID); ok {
		m.mu.Unlock()
		return domain.Operation{}, operationInProgress(active)
	}
	now := time.Now().UTC()
	deleteFrom := backup.Status
	backup.Status = "deleting"
	m.backups[serverID][backupIndex] = backup
	operation := domain.NewQueuedOperation(id.New(), serverID, server.NodeID, domain.PowerAction("backup-delete"), server.Generation, idempotencyKey, now)
	m.backupDeleteFrom[operation.ID] = deleteFrom
	m.operations[operation.ID] = operation
	m.idempotency[scope] = idempotencyRecord{OperationID: operation.ID, RequestDigest: digest}
	m.mu.Unlock()
	m.recordAudit(currentActor.DisplayName, "backup.delete", "server", server.Name, "accepted", operation.ID)
	go m.finishDeleteBackup(operation.ID, serverID, backupID, currentActor.DisplayName)
	return operation, nil
}

func (m *Memory) finishBackup(operationID string, serverID string, actor string) {
	if !m.beginOperation(operationID) {
		return
	}
	time.Sleep(m.operationLatency)
	now := time.Now().UTC()
	m.mu.Lock()
	operation, operationOK := m.operations[operationID]
	server, serverOK := m.servers[serverID]
	finished := operationOK && completeOperationForGenerationLocked(&operation, server, serverOK, now)
	if finished {
		m.operations[operationID] = operation
	}
	succeeded := finished && operation.Status == "succeeded"
	backupID := m.backupOperations[operationID]
	if finished && backupID != "" {
		backupIndex, backup, backupOK := m.backupLocked(serverID, backupID)
		if !backupOK {
			finished = false
			succeeded = false
			failure := domain.OperationError{Code: "BACKUP_FAILED", Message: "备份元数据在任务完成前丢失", Retryable: false}
			operation.Status = "failed"
			operation.Checkpoint = "failed"
			operation.Error = &failure
			m.operations[operationID] = operation
		} else if succeeded {
			checksumBytes := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d:%d", backupID, server.GameBundleDigest, server.Metrics.DiskBytes, now.UnixNano())))
			checksum := "sha256:" + hex.EncodeToString(checksumBytes[:])
			manifestBytes := sha256.Sum256([]byte("manifest:" + checksum))
			manifestDigest := "sha256:" + hex.EncodeToString(manifestBytes[:])
			backup.Status = "ready"
			backup.SizeBytes = valuePointer(server.Metrics.DiskBytes)
			backup.Checksum = valuePointer(checksum)
			backup.ManifestDigest = valuePointer(manifestDigest)
			backup.CompletedAt = valuePointer(now)
			backup.FailureCode = nil
			backup.FailureMessage = nil
			m.backups[serverID][backupIndex] = backup
			m.backupChecksums[backup.ID] = *backup.Checksum
		} else if finished {
			backup.Status = "failed"
			backup.FailureCode, backup.FailureMessage = backupFailureMetadata(operation.Error)
			backup.CompletedAt = nil
			m.backups[serverID][backupIndex] = backup
		}
	}
	delete(m.backupOperations, operationID)
	m.mu.Unlock()
	if finished {
		result := "failure"
		if succeeded {
			result = "success"
		}
		targetName := serverID
		if serverOK {
			targetName = server.Name
		}
		m.recordAudit(actor, "backup.create", "server", targetName, result, operationID)
	}
}

func (m *Memory) finishRestoreBackup(operationID string, serverID string, backupID string, actor string) {
	if !m.beginOperation(operationID) {
		return
	}
	time.Sleep(m.operationLatency)
	now := time.Now().UTC()
	m.mu.Lock()
	operation, operationOK := m.operations[operationID]
	server, serverOK := m.servers[serverID]
	backupIndex, backup, backupOK := m.backupLocked(serverID, backupID)
	succeeded := false
	completed := false
	if operationOK {
		if !operationTargetsCurrentServer(operation, server, serverOK) {
			failure := domain.OperationError{Code: "OPERATION_STALE", Message: "恢复操作对应的服务器状态已变化", Retryable: false}
			completed = failOperationLocked(&operation, failure, now)
		} else if !backupOK || !m.backupChecksumValidLocked(backup) {
			completed = failOperationLocked(&operation, domain.OperationError{Code: "BACKUP_INTEGRITY_FAILED", Message: "恢复期间备份摘要校验失败", Retryable: false}, now)
		} else {
			completed = completeOperationLocked(&operation, now)
			succeeded = completed
		}
		if completed {
			m.operations[operationID] = operation
		}
	}
	if backupOK && completed {
		// A restore failure is compensatable: preserve the last known-good
		// recovery point so operators can retry without destructive metadata.
		backup.Status = "ready"
		if succeeded {
			backup.FailureCode = nil
			backup.FailureMessage = nil
		} else {
			backup.FailureCode, backup.FailureMessage = backupFailureMetadata(operation.Error)
		}
		m.backups[serverID][backupIndex] = backup
	}
	if succeeded && completed {
		server.DesiredPower = "stopped"
		server.ObservedPower = "stopped"
		server.HealthCondition = "unknown"
		server.ObservedGeneration = server.Generation
		server.ObservedAt = now
		server.UpdatedAt = now
		m.servers[serverID] = server
	}
	m.mu.Unlock()
	if completed {
		result := "failure"
		if succeeded {
			result = "success"
		}
		targetName := serverID
		if serverOK {
			targetName = server.Name
		}
		m.recordAudit(actor, "backup.restore", "server", targetName, result, operationID)
	}
}

func (m *Memory) finishDeleteBackup(operationID string, serverID string, backupID string, actor string) {
	if !m.beginOperation(operationID) {
		return
	}
	time.Sleep(m.operationLatency)
	now := time.Now().UTC()
	m.mu.Lock()
	operation, operationOK := m.operations[operationID]
	server, serverOK := m.servers[serverID]
	backupIndex, backup, backupOK := m.backupLocked(serverID, backupID)
	completed := false
	succeeded := false
	if operationOK {
		if !operationTargetsCurrentServer(operation, server, serverOK) {
			completed = failOperationLocked(&operation, domain.OperationError{
				Code:      "OPERATION_STALE",
				Message:   "操作对应的服务器状态已变化",
				Retryable: false,
			}, now)
		} else if !backupOK {
			completed = failOperationLocked(&operation, domain.OperationError{
				Code:      "OPERATION_FAILED",
				Message:   "备份在删除完成前已不存在",
				Retryable: false,
			}, now)
		} else {
			completed = completeOperationLocked(&operation, now)
			succeeded = completed
		}
		if completed {
			m.operations[operationID] = operation
		}
	}
	deleteFrom := m.backupDeleteFrom[operationID]
	if backupOK && succeeded {
		backup.Status = "deleted"
		backup.DeletedAt = valuePointer(now)
		backup.FailureCode = nil
		backup.FailureMessage = nil
		m.backups[serverID][backupIndex] = backup
		delete(m.backupChecksums, backupID)
	} else if backupOK && completed {
		backup.Status = "ready"
		if deleteFrom == "failed" {
			backup.Status = "failed"
		}
		backup.FailureCode, backup.FailureMessage = backupFailureMetadata(operation.Error)
		m.backups[serverID][backupIndex] = backup
	}
	delete(m.backupDeleteFrom, operationID)
	m.mu.Unlock()
	if completed {
		result := "failure"
		if succeeded {
			result = "success"
		}
		targetName := serverID
		if serverOK {
			targetName = server.Name
		}
		m.recordAudit(actor, "backup.delete", "server", targetName, result, operationID)
	}
}

// DownloadBackup 在内存模式下不可用：开发环境不落盘真实备份归档，
// 仅保留备份元数据，无法提供可下载的文件内容。
func (m *Memory) DownloadBackup(serverID string, backupID string, actor domain.User) (domain.BackupContent, error) {
	if err := m.AuthorizeServer(actor.ID, serverID, "servers.backups.read"); err != nil {
		return domain.BackupContent{}, err
	}
	return domain.BackupContent{}, domain.NewProblem("NOT_FOUND", "备份文件在内存模式下不可下载", false)
}

func (m *Memory) backupLocked(serverID string, backupID string) (int, domain.Backup, bool) {
	for index, backup := range m.backups[serverID] {
		if backup.ID == backupID {
			return index, backup, true
		}
	}
	return -1, domain.Backup{}, false
}

func (m *Memory) backupChecksumValidLocked(backup domain.Backup) bool {
	if backup.Checksum == nil {
		return false
	}
	expected, ok := m.backupChecksums[backup.ID]
	if !ok || expected != *backup.Checksum || !strings.HasPrefix(*backup.Checksum, "sha256:") {
		return false
	}
	digest, err := hex.DecodeString(strings.TrimPrefix(*backup.Checksum, "sha256:"))
	return err == nil && len(digest) == sha256.Size
}

func backupFailureMetadata(operationError *domain.OperationError) (*string, *string) {
	code := "BACKUP_FAILED"
	if operationError != nil && operationError.Code != "" {
		code = operationError.Code
	}
	code, message := backupFailureDetails(code)
	return &code, &message
}

func backupFailureDetails(code string) (string, string) {
	switch code {
	case "BACKUP_INTEGRITY_FAILED":
		return code, "备份摘要或结果校验失败"
	case "MAX_ATTEMPTS":
		return code, "备份任务租约耗尽，已停止重试"
	case "NODE_OFFLINE":
		return code, "备份所在节点离线"
	case "RUNTIME_UNAVAILABLE":
		return code, "备份运行时不可用"
	case "OPERATION_STALE":
		return code, "备份任务对应的服务器状态已变化"
	case "TASK_CANCELED":
		return code, "备份任务已取消"
	case "BACKUP_FAILED":
		return code, "备份任务失败，请检查节点和任务日志"
	default:
		return "BACKUP_FAILED", "备份任务失败，请检查节点和任务日志"
	}
}

func valuePointer[T any](value T) *T {
	return &value
}
