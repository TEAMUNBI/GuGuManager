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
			m.backupChecksums[backup.ID] = backup.Checksum
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
	if backup.Status != "ready" {
		m.mu.Unlock()
		return domain.Operation{}, domain.NewProblem("RESTORE_LOCKED", "备份当前不可删除", false)
	}
	if active, ok := m.activeExclusiveOperationLocked(serverID); ok {
		m.mu.Unlock()
		return domain.Operation{}, operationInProgress(active)
	}
	now := time.Now().UTC()
	backup.Status = "deleting"
	m.backups[serverID][backupIndex] = backup
	operation := domain.NewQueuedOperation(id.New(), serverID, server.NodeID, domain.PowerAction("backup-delete"), server.Generation, idempotencyKey, now)
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
	if succeeded {
		backupID := id.New()
		checksumBytes := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d:%d", backupID, server.GameBundleDigest, server.Metrics.DiskBytes, now.UnixNano())))
		checksum := "sha256:" + hex.EncodeToString(checksumBytes[:])
		backup := domain.Backup{ID: backupID, Name: "manual-" + now.Format("20060102-150405"), Status: "ready", SizeBytes: server.Metrics.DiskBytes, Checksum: checksum, CreatedAt: now}
		m.backups[serverID] = append([]domain.Backup{backup}, m.backups[serverID]...)
		m.backupChecksums[backup.ID] = backup.Checksum
	}
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
	integrityFailed := false
	if operationOK {
		if !operationTargetsCurrentServer(operation, server, serverOK) {
			failure := domain.OperationError{Code: "OPERATION_STALE", Message: "恢复操作对应的服务器状态已变化", Retryable: false}
			completed = failOperationLocked(&operation, failure, now)
		} else if !backupOK || !m.backupChecksumValidLocked(backup) {
			integrityFailed = true
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
		if integrityFailed {
			backup.Status = "failed"
		} else {
			backup.Status = "ready"
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
	if backupOK && succeeded {
		backups := m.backups[serverID]
		m.backups[serverID] = append(backups[:backupIndex], backups[backupIndex+1:]...)
		delete(m.backupChecksums, backupID)
	} else if backupOK && completed {
		backup.Status = "ready"
		m.backups[serverID][backupIndex] = backup
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
		m.recordAudit(actor, "backup.delete", "server", targetName, result, operationID)
	}
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
	expected, ok := m.backupChecksums[backup.ID]
	if !ok || expected != backup.Checksum || !strings.HasPrefix(backup.Checksum, "sha256:") {
		return false
	}
	digest, err := hex.DecodeString(strings.TrimPrefix(backup.Checksum, "sha256:"))
	return err == nil && len(digest) == sha256.Size
}
