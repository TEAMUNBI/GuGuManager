package store

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
)

const (
	availableNodeID = "11111111-1111-4111-8111-111111111111"
	stoppedServerID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

func TestCreateServerIdempotencyIncludesRequestDigestAndActor(t *testing.T) {
	service := newTestMemory(time.Second)
	input := validCreateServerInput()

	first, err := service.CreateServer(input, "create-key-00001", testActor("admin-1", "GuGu Admin"))
	if err != nil {
		t.Fatalf("first CreateServer failed: %v", err)
	}

	changed := input
	changed.Name = "Different world"
	_, err = service.CreateServer(changed, "create-key-00001", testActor("admin-1", "GuGu Admin"))
	requireProblemCode(t, err, "IDEMPOTENCY_KEY_REUSED")

	secondActorUser, err := service.CreateUser(domain.CreateUserInput{
		Email:       "idempotency-second-admin@example.test",
		DisplayName: "Second Admin",
		Password:    "second admin secure password",
		Roles:       []string{"platform_admin"},
	}, testActor("admin-1", "GuGu Admin"))
	if err != nil {
		t.Fatalf("creating second admin failed: %v", err)
	}
	secondActor, err := service.CreateServer(input, "create-key-00001", secondActorUser)
	if err != nil {
		t.Fatalf("same key in another actor scope failed: %v", err)
	}
	if secondActor.ID == first.ID || secondActor.ServerID == first.ServerID {
		t.Fatalf("actor-scoped request reused first operation: first=%+v second=%+v", first, secondActor)
	}
}

func TestPowerOperationsAreSerializedPerServer(t *testing.T) {
	service := newTestMemory(time.Second)
	first, err := service.RequestPower(stoppedServerID, domain.PowerStart, "power-key-000001", testActor("admin-1", "GuGu Admin"))
	if err != nil {
		t.Fatalf("start request failed: %v", err)
	}

	duplicate, err := service.RequestPower(stoppedServerID, domain.PowerStart, "power-key-000002", testActor("admin-1", "GuGu Admin"))
	if err != nil {
		t.Fatalf("equivalent active request failed: %v", err)
	}
	if duplicate.ID != first.ID {
		t.Fatalf("equivalent active request created %q, want %q", duplicate.ID, first.ID)
	}

	_, err = service.RequestPower(stoppedServerID, domain.PowerStop, "power-key-000003", testActor("admin-1", "GuGu Admin"))
	problem := requireProblemCode(t, err, "OPERATION_IN_PROGRESS")
	if problem.Details["operationId"] != first.ID {
		t.Fatalf("conflict operationId = %v, want %q", problem.Details["operationId"], first.ID)
	}
}

func TestAcceptedOperationsSnapshotTheirTargetNode(t *testing.T) {
	actor := testActor("admin-1", "GuGu Admin")
	tests := []struct {
		name           string
		expectedNodeID string
		request        func(*Memory) (domain.Operation, error)
	}{
		{
			name:           "provision",
			expectedNodeID: availableNodeID,
			request: func(service *Memory) (domain.Operation, error) {
				return service.CreateServer(validCreateServerInput(), "node-snapshot-create-01", actor)
			},
		},
		{
			name:           "power",
			expectedNodeID: "22222222-2222-4222-8222-222222222222",
			request: func(service *Memory) (domain.Operation, error) {
				return service.RequestPower(stoppedServerID, domain.PowerStart, "node-snapshot-power-001", actor)
			},
		},
		{
			name:           "backup",
			expectedNodeID: "22222222-2222-4222-8222-222222222222",
			request: func(service *Memory) (domain.Operation, error) {
				return service.CreateBackup(stoppedServerID, "node-snapshot-backup-01", actor)
			},
		},
		{
			name:           "restore",
			expectedNodeID: "22222222-2222-4222-8222-222222222222",
			request: func(service *Memory) (domain.Operation, error) {
				return service.RestoreBackup(stoppedServerID, "d3333333-3333-4333-8333-333333333333", "node-snapshot-restore-1", actor)
			},
		},
		{
			name:           "backup delete",
			expectedNodeID: "22222222-2222-4222-8222-222222222222",
			request: func(service *Memory) (domain.Operation, error) {
				return service.DeleteBackup(stoppedServerID, "d3333333-3333-4333-8333-333333333333", "node-snapshot-delete-01", actor)
			},
		},
		{
			name:           "reconcile",
			expectedNodeID: "22222222-2222-4222-8222-222222222222",
			request: func(service *Memory) (domain.Operation, error) {
				server, err := service.Server(stoppedServerID)
				if err != nil {
					return domain.Operation{}, err
				}
				return service.CreateAllocation(stoppedServerID, domain.CreateAllocationInput{
					BindIP: "10.0.20.14", Port: 34198, Protocol: "udp", Primary: false,
				}, server.Generation, "node-snapshot-reconcile", actor)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newTestMemory(time.Second)
			defer func() { _ = service.Close() }()

			operation, err := test.request(service)
			if err != nil {
				t.Fatal(err)
			}
			if operation.NodeID != test.expectedNodeID {
				t.Fatalf("operation nodeId = %q, want %q", operation.NodeID, test.expectedNodeID)
			}
		})
	}
}

func TestMemoryOperationMetadataTracksQueuedRunningAndSucceededLifecycle(t *testing.T) {
	service := newTestMemory(250 * time.Millisecond)
	defer func() { _ = service.Close() }()

	accepted, err := service.RequestPower(stoppedServerID, domain.PowerStart, "operation-metadata-power-001", testActor("admin-1", "GuGu Admin"))
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != "queued" || accepted.Checkpoint != "queued" {
		t.Fatalf("accepted operation lifecycle = status=%q checkpoint=%q, want queued/queued", accepted.Status, accepted.Checkpoint)
	}
	if accepted.Attempt != 1 || accepted.MaxAttempts != 1 {
		t.Fatalf("accepted operation attempts = %d/%d, want 1/1", accepted.Attempt, accepted.MaxAttempts)
	}
	if accepted.LeaseOwner != nil || accepted.LeaseExpiresAt != nil || accepted.Error != nil {
		t.Fatalf("accepted operation unexpectedly has active metadata: %+v", accepted)
	}

	deadline := time.Now().Add(time.Second)
	var running domain.Operation
	for time.Now().Before(deadline) {
		candidate, operationErr := service.Operation(accepted.ID)
		if operationErr != nil {
			t.Fatal(operationErr)
		}
		if candidate.Status == "running" {
			running = candidate
			break
		}
		time.Sleep(time.Millisecond)
	}
	if running.ID == "" {
		t.Fatal("operation did not publish a running state")
	}
	if running.Checkpoint != "applying-power-state" {
		t.Fatalf("running checkpoint = %q, want applying-power-state", running.Checkpoint)
	}
	if running.LeaseOwner == nil || *running.LeaseOwner != memoryOperationLeaseOwner || running.LeaseExpiresAt == nil {
		t.Fatalf("running lease metadata = owner=%v expiresAt=%v", running.LeaseOwner, running.LeaseExpiresAt)
	}
	if running.Error != nil {
		t.Fatalf("running operation error = %+v, want nil", running.Error)
	}

	completed := waitForStoredOperation(t, service, accepted.ID)
	if completed.Status != "succeeded" || completed.Checkpoint != "completed" || completed.Progress != 100 {
		t.Fatalf("completed operation lifecycle = %+v", completed)
	}
	if completed.LeaseOwner != nil || completed.LeaseExpiresAt != nil || completed.Error != nil {
		t.Fatalf("completed operation unexpectedly retains transient metadata: %+v", completed)
	}
}

func TestStartRejectsMissingRequiredStartupValuesWithoutMutation(t *testing.T) {
	assertPowerRejectsMissingRequiredStartupValues(t, stoppedServerID, domain.PowerStart, "required-start-key-0001")
}

func TestRestartRejectsMissingRequiredStartupValuesWithoutMutation(t *testing.T) {
	assertPowerRejectsMissingRequiredStartupValues(t, runningServerID, domain.PowerRestart, "required-restart-key-01")
}

func assertPowerRejectsMissingRequiredStartupValues(t *testing.T, serverID string, action domain.PowerAction, idempotencyKey string) {
	t.Helper()
	service := newTestMemory(time.Millisecond)
	defer func() { _ = service.Close() }()
	actor := testActor("admin-1", "GuGu Admin")

	service.mu.Lock()
	serverBefore := service.servers[serverID]
	startup, err := service.startupTemplateForServerLocked(serverBefore)
	if err != nil {
		service.mu.Unlock()
		t.Fatalf("load trusted Startup template: %v", err)
	}
	missingKeys := make([]string, 0)
	removedValues := make([]string, 0)
	for _, variable := range startup.Variables {
		if !variable.Required {
			continue
		}
		missingKeys = append(missingKeys, variable.Key)
		if value, configured := service.startupValues[serverID][variable.Key]; configured {
			removedValues = append(removedValues, fmt.Sprint(value))
		}
		delete(service.startupValues[serverID], variable.Key)
	}
	operationsBefore := maps.Clone(service.operations)
	idempotencyBefore := maps.Clone(service.idempotency)
	auditBefore := append([]domain.AuditEvent(nil), service.audit...)
	service.mu.Unlock()
	if len(missingKeys) == 0 {
		t.Fatal("test fixture has no required Startup variables")
	}

	_, err = service.RequestPower(serverID, action, idempotencyKey, actor)
	problem := requireProblemCode(t, err, "VALIDATION_FAILED")
	for _, key := range missingKeys {
		if !strings.Contains(problem.Message, key) {
			t.Errorf("problem message %q does not identify missing key %q", problem.Message, key)
		}
	}
	for _, value := range removedValues {
		if strings.Contains(problem.Message, value) {
			t.Errorf("problem message %q leaked removed value %q", problem.Message, value)
		}
	}
	if len(problem.Details) != 0 {
		t.Fatalf("problem details = %+v, want no value-bearing details", problem.Details)
	}

	service.mu.RLock()
	serverAfter := service.servers[serverID]
	operationsAfter := maps.Clone(service.operations)
	idempotencyAfter := maps.Clone(service.idempotency)
	auditAfter := append([]domain.AuditEvent(nil), service.audit...)
	service.mu.RUnlock()
	if !reflect.DeepEqual(serverAfter, serverBefore) {
		t.Fatalf("server changed after rejected %s: before=%+v after=%+v", action, serverBefore, serverAfter)
	}
	if !reflect.DeepEqual(operationsAfter, operationsBefore) {
		t.Fatalf("operations changed after rejected %s: before=%+v after=%+v", action, operationsBefore, operationsAfter)
	}
	if !reflect.DeepEqual(idempotencyAfter, idempotencyBefore) {
		t.Fatalf("idempotency changed after rejected %s: before=%+v after=%+v", action, idempotencyBefore, idempotencyAfter)
	}
	if !reflect.DeepEqual(auditAfter, auditBefore) {
		t.Fatalf("audit changed after rejected %s: before=%+v after=%+v", action, auditBefore, auditAfter)
	}
}

func TestProvisioningServerRejectsPowerUntilReady(t *testing.T) {
	service := newTestMemory(time.Second)
	operation, err := service.CreateServer(validCreateServerInput(), "create-key-00002", testActor("admin-1", "GuGu Admin"))
	if err != nil {
		t.Fatalf("CreateServer failed: %v", err)
	}

	_, err = service.RequestPower(operation.ServerID, domain.PowerStart, "power-key-000004", testActor("admin-1", "GuGu Admin"))
	requireProblemCode(t, err, "OPERATION_IN_PROGRESS")
}

func TestFinishPowerDoesNotApplyAStaleGeneration(t *testing.T) {
	service := newTestMemory(0)
	service.mu.Lock()
	server := service.servers[stoppedServerID]
	server.Generation++
	server.DesiredPower = "stopped"
	server.ObservedPower = "stopping"
	service.servers[stoppedServerID] = server
	service.operations["stale-operation"] = domain.Operation{
		ID:         "stale-operation",
		ServerID:   stoppedServerID,
		NodeID:     server.NodeID,
		Type:       domain.PowerStart,
		Status:     "running",
		Generation: server.Generation - 1,
	}
	service.mu.Unlock()

	service.finishPower("stale-operation", stoppedServerID, domain.PowerStart)

	got, err := service.Server(stoppedServerID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ObservedPower != "stopping" {
		t.Fatalf("stale completion set observedPower=%q, want stopping", got.ObservedPower)
	}
	if got.ObservedGeneration == got.Generation {
		t.Fatalf("stale completion falsely converged generation %d", got.Generation)
	}
	completed, err := service.Operation("stale-operation")
	if err != nil {
		t.Fatal(err)
	}
	assertStaleOperationFailure(t, completed)
}

func TestFinishPowerDoesNotApplyAnOperationAssignedToAnotherNode(t *testing.T) {
	service := newTestMemory(0)
	service.mu.Lock()
	server := service.servers[stoppedServerID]
	acceptedNodeID := server.NodeID
	server.NodeID = availableNodeID
	server.DesiredPower = "stopped"
	server.ObservedPower = "stopping"
	service.servers[stoppedServerID] = server
	service.operations["stale-node-power"] = domain.Operation{
		ID:         "stale-node-power",
		ServerID:   stoppedServerID,
		NodeID:     acceptedNodeID,
		Type:       domain.PowerStart,
		Status:     "running",
		Generation: server.Generation,
	}
	service.mu.Unlock()

	service.finishPower("stale-node-power", stoppedServerID, domain.PowerStart)

	got, err := service.Server(stoppedServerID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ObservedPower != "stopping" || got.DesiredPower != "stopped" {
		t.Fatalf("stale-node completion applied power state: desired=%q observed=%q", got.DesiredPower, got.ObservedPower)
	}
	completed, err := service.Operation("stale-node-power")
	if err != nil {
		t.Fatal(err)
	}
	assertStaleOperationFailure(t, completed)
}

func TestFinishProvisionDoesNotSucceedWithAStaleGeneration(t *testing.T) {
	service := newTestMemory(0)
	service.mu.Lock()
	server := service.servers[stoppedServerID]
	server.LifecycleState = "provisioning"
	server.Generation++
	service.servers[stoppedServerID] = server
	service.operations["stale-provision"] = domain.Operation{
		ID:         "stale-provision",
		ServerID:   stoppedServerID,
		NodeID:     server.NodeID,
		Type:       domain.PowerAction("provision"),
		Status:     "running",
		Generation: server.Generation - 1,
	}
	service.mu.Unlock()

	service.finishProvision("stale-provision", stoppedServerID)

	got, err := service.Server(stoppedServerID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LifecycleState != "provisioning" {
		t.Fatalf("stale provision set lifecycleState=%q, want provisioning", got.LifecycleState)
	}
	completed, err := service.Operation("stale-provision")
	if err != nil {
		t.Fatal(err)
	}
	assertStaleOperationFailure(t, completed)
}

func TestFinishReconcileDoesNotSucceedWithAStaleGeneration(t *testing.T) {
	service := newTestMemory(0)
	service.mu.Lock()
	server := service.servers[stoppedServerID]
	server.Generation++
	service.servers[stoppedServerID] = server
	service.operations["stale-reconcile"] = domain.Operation{
		ID:         "stale-reconcile",
		ServerID:   stoppedServerID,
		NodeID:     server.NodeID,
		Type:       domain.PowerAction("reconcile"),
		Status:     "running",
		Generation: server.Generation - 1,
	}
	service.mu.Unlock()

	service.finishReconcile("stale-reconcile", stoppedServerID, "GuGu Admin", "server.network.update")

	got, err := service.Server(stoppedServerID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ObservedGeneration == got.Generation {
		t.Fatalf("stale reconcile falsely converged generation %d", got.Generation)
	}
	completed, err := service.Operation("stale-reconcile")
	if err != nil {
		t.Fatal(err)
	}
	assertStaleOperationFailure(t, completed)
}

func TestFinishBackupDoesNotSucceedWithAStaleGeneration(t *testing.T) {
	service := newTestMemory(0)
	before, err := service.Backups(stoppedServerID)
	if err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	server := service.servers[stoppedServerID]
	server.Generation++
	service.servers[stoppedServerID] = server
	service.operations["stale-backup"] = domain.Operation{
		ID:         "stale-backup",
		ServerID:   stoppedServerID,
		NodeID:     server.NodeID,
		Type:       domain.PowerAction("backup"),
		Status:     "running",
		Generation: server.Generation - 1,
	}
	service.mu.Unlock()

	service.finishBackup("stale-backup", stoppedServerID, "GuGu Admin")

	after, err := service.Backups(stoppedServerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("stale backup created a recovery point: before=%d after=%d", len(before), len(after))
	}
	completed, err := service.Operation("stale-backup")
	if err != nil {
		t.Fatal(err)
	}
	assertStaleOperationFailure(t, completed)
}

func TestCreateBackupPersistsCreatingMetadataAndFailureReason(t *testing.T) {
	service := newTestMemory(50 * time.Millisecond)
	operation, err := service.CreateBackup(stoppedServerID, "backup-state-machine-001", testActor("admin-1", "GuGu Admin"))
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	backups, err := service.Backups(stoppedServerID)
	if err != nil || len(backups) == 0 || backups[0].Status != "creating" {
		t.Fatalf("accepted backup metadata = %+v, err=%v; want creating", backups, err)
	}
	backupID := backups[0].ID
	service.mu.Lock()
	server := service.servers[stoppedServerID]
	server.Generation++
	service.servers[stoppedServerID] = server
	service.mu.Unlock()
	completed := waitForStoredOperation(t, service, operation.ID)
	if completed.Status != "failed" {
		t.Fatalf("operation status = %q, want failed", completed.Status)
	}
	backups, err = service.Backups(stoppedServerID)
	if err != nil || len(backups) == 0 || backups[0].ID != backupID {
		t.Fatalf("failed backup metadata missing: %+v, err=%v", backups, err)
	}
	if backups[0].Status != "failed" || backups[0].FailureCode == nil || *backups[0].FailureCode != "OPERATION_STALE" || backups[0].FailureMessage == nil {
		t.Fatalf("failed backup metadata = %+v", backups[0])
	}
}

func TestFailedBackupCanBeDeleted(t *testing.T) {
	service := newTestMemory(30 * time.Millisecond)
	operation, err := service.CreateBackup(stoppedServerID, "failed-backup-delete-0001", testActor("admin-1", "GuGu Admin"))
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	backups, err := service.Backups(stoppedServerID)
	if err != nil || len(backups) == 0 {
		t.Fatalf("creating backup = %+v, err=%v", backups, err)
	}
	backupID := backups[0].ID
	service.mu.Lock()
	server := service.servers[stoppedServerID]
	server.Generation++
	service.servers[stoppedServerID] = server
	service.mu.Unlock()
	if completed := waitForStoredOperation(t, service, operation.ID); completed.Status != "failed" {
		t.Fatalf("backup operation status = %q, want failed", completed.Status)
	}

	deleteOperation, err := service.DeleteBackup(stoppedServerID, backupID, "failed-backup-delete-0002", testActor("admin-1", "GuGu Admin"))
	if err != nil {
		t.Fatalf("delete failed backup: %v", err)
	}
	if completed := waitForStoredOperation(t, service, deleteOperation.ID); completed.Status != "succeeded" {
		t.Fatalf("delete operation = %+v, want succeeded", completed)
	}
	backups, err = service.Backups(stoppedServerID)
	if err != nil {
		t.Fatalf("backups after deleting failed recovery point = %+v, err=%v", backups, err)
	}
	for _, backup := range backups {
		if backup.ID == backupID {
			t.Fatalf("deleted failed recovery point remains visible: %+v", backup)
		}
	}
}

func TestFailedBackupDeleteCompensationReturnsToFailed(t *testing.T) {
	service := newTestMemory(40 * time.Millisecond)
	operation, err := service.CreateBackup(stoppedServerID, "failed-backup-compensate-0001", testActor("admin-1", "GuGu Admin"))
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	backups, _ := service.Backups(stoppedServerID)
	backupID := backups[0].ID
	service.mu.Lock()
	server := service.servers[stoppedServerID]
	server.Generation++
	service.servers[stoppedServerID] = server
	service.mu.Unlock()
	_ = waitForStoredOperation(t, service, operation.ID)

	deleteOperation, err := service.DeleteBackup(stoppedServerID, backupID, "failed-backup-compensate-0002", testActor("admin-1", "GuGu Admin"))
	if err != nil {
		t.Fatalf("delete failed backup: %v", err)
	}
	service.mu.Lock()
	server = service.servers[stoppedServerID]
	server.Generation++
	service.servers[stoppedServerID] = server
	service.mu.Unlock()
	if completed := waitForStoredOperation(t, service, deleteOperation.ID); completed.Status != "failed" {
		t.Fatalf("delete operation = %+v, want failed", completed)
	}
	backups, err = service.Backups(stoppedServerID)
	if err != nil {
		t.Fatalf("compensated failed backup = %+v, err=%v", backups, err)
	}
	found := false
	for _, backup := range backups {
		if backup.ID == backupID {
			found = true
			if backup.Status != "failed" {
				t.Fatalf("compensated backup status = %q, want failed", backup.Status)
			}
		}
	}
	if !found {
		t.Fatalf("compensated failed backup %q is missing: %+v", backupID, backups)
	}
}

func assertStaleOperationFailure(t *testing.T, operation domain.Operation) {
	t.Helper()
	if operation.Status != "failed" || operation.Checkpoint != "failed" {
		t.Fatalf("stale operation terminal state = status=%q checkpoint=%q, want failed/failed", operation.Status, operation.Checkpoint)
	}
	if operation.Error == nil || operation.Error.Code != "OPERATION_STALE" || operation.Error.Retryable {
		t.Fatalf("stale operation error = %+v, want non-retryable OPERATION_STALE", operation.Error)
	}
	if operation.LeaseOwner != nil || operation.LeaseExpiresAt != nil {
		t.Fatalf("stale operation retains lease metadata: %+v", operation)
	}
}

func TestCreateServerValidatesApprovalLimitsAndCapacity(t *testing.T) {
	tests := []struct {
		name  string
		input domain.CreateServerInput
		code  string
	}{
		{
			name:  "pending game definition",
			input: domain.CreateServerInput{Name: "Pending", GameDefinitionID: "io.gugumanager.vintagestory", GameBundleDigest: "sha256:c2c2cdb82e9ba2cc69e17b9acc99ddd4e75a40dd091e39a19c987927273e7779", NodeID: availableNodeID, MemoryMB: 1024, DiskGB: 5},
			code:  "GAME_DEFINITION_NOT_APPROVED",
		},
		{
			name:  "memory above API maximum",
			input: domain.CreateServerInput{Name: "Too large", GameDefinitionID: "io.gugumanager.papermc", GameBundleDigest: "sha256:412759ce8b7832b3762d1a6f34d076ecceebd4ecd7cd7d04f16ac0cff063285b", NodeID: availableNodeID, MemoryMB: 131073, DiskGB: 5},
			code:  "VALIDATION_FAILED",
		},
		{
			name:  "node capacity exceeded",
			input: domain.CreateServerInput{Name: "No capacity", GameDefinitionID: "io.gugumanager.papermc", GameBundleDigest: "sha256:412759ce8b7832b3762d1a6f34d076ecceebd4ecd7cd7d04f16ac0cff063285b", NodeID: availableNodeID, MemoryMB: 65536, DiskGB: 5},
			code:  "INSUFFICIENT_RESOURCE",
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newTestMemory(time.Second)
			_, err := service.CreateServer(test.input, "create-key-1000"+string(rune('0'+index)), testActor("admin-1", "GuGu Admin"))
			requireProblemCode(t, err, test.code)
		})
	}
}

func TestCreateServerRejectsInvalidIdempotencyKeys(t *testing.T) {
	for _, key := range []string{"short", "contains\nnewline", "非 ASCII 的幂等键 000000"} {
		t.Run(key, func(t *testing.T) {
			service := newTestMemory(time.Second)
			_, err := service.CreateServer(validCreateServerInput(), key, testActor("admin-1", "GuGu Admin"))
			requireProblemCode(t, err, "VALIDATION_FAILED")
		})
	}
}

func TestCreateServerUsesTheCatalogGameVersion(t *testing.T) {
	service := newTestMemory(time.Second)
	operation, err := service.CreateServer(validCreateServerInput(), "create-version-key-01", testActor("admin-1", "GuGu Admin"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := service.Server(operation.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	if server.GameVersion != "1.21.8" {
		t.Fatalf("server game version = %q, want upstream version 1.21.8", server.GameVersion)
	}
	entries, err := service.Files(operation.ServerID, "")
	if err != nil {
		t.Fatalf("new server file root is unavailable: %v", err)
	}
	if entries == nil || len(entries) != 0 {
		t.Fatalf("new server file root = %+v, want an empty non-nil list", entries)
	}
}

func TestAsyncRequestsAreAuditedAsAccepted(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Memory) error
	}{
		{
			name: "server create",
			run: func(service *Memory) error {
				_, err := service.CreateServer(validCreateServerInput(), "create-audit-key-0001", testActor("admin-1", "GuGu Admin"))
				return err
			},
		},
		{
			name: "power request",
			run: func(service *Memory) error {
				_, err := service.RequestPower(stoppedServerID, domain.PowerStart, "power-audit-key-00001", testActor("admin-1", "GuGu Admin"))
				return err
			},
		},
		{
			name: "backup request",
			run: func(service *Memory) error {
				_, err := service.CreateBackup(stoppedServerID, "backup-audit-key-0001", testActor("admin-1", "GuGu Admin"))
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newTestMemory(time.Second)
			if err := test.run(service); err != nil {
				t.Fatal(err)
			}
			events := service.AuditEvents()
			if len(events) == 0 {
				t.Fatal("async request did not append an audit event")
			}
			if events[0].Result != "accepted" {
				t.Fatalf("audit result = %q, want accepted", events[0].Result)
			}
		})
	}
}

func TestRestoreBackupRequiresStoppedServerAndValidRecordedChecksum(t *testing.T) {
	service := newTestMemory(time.Second)
	actor := testActor("admin-1", "GuGu Admin")

	_, err := service.RestoreBackup(runningServerID, "d1111111-1111-4111-8111-111111111111", "restore-running-key-01", actor)
	requireProblemCode(t, err, "RESTORE_LOCKED")

	service.mu.Lock()
	backup := service.backups[stoppedServerID][0]
	backup.Checksum = valuePointer("sha256:" + strings.Repeat("0", 64))
	service.backups[stoppedServerID][0] = backup
	service.mu.Unlock()
	_, err = service.RestoreBackup(stoppedServerID, backup.ID, "restore-corrupt-key-01", actor)
	requireProblemCode(t, err, "BACKUP_INTEGRITY_FAILED")
}

func TestRestoreBackupHoldsExclusiveLockAndConvergesGeneration(t *testing.T) {
	service := newTestMemory(10 * time.Millisecond)
	actor := testActor("admin-1", "GuGu Admin")
	backupID := "d3333333-3333-4333-8333-333333333333"

	operation, err := service.RestoreBackup(stoppedServerID, backupID, "restore-backup-key-01", actor)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := service.RestoreBackup(stoppedServerID, backupID, "restore-backup-key-01", actor)
	if err != nil || duplicate.ID != operation.ID {
		t.Fatalf("idempotent restore = %+v, %v; want operation %s", duplicate, err, operation.ID)
	}
	backups, err := service.Backups(stoppedServerID)
	if err != nil {
		t.Fatal(err)
	}
	if backups[0].Status != "restoring" {
		t.Fatalf("backup status = %q, want restoring", backups[0].Status)
	}
	_, err = service.CreateBackup(stoppedServerID, "blocked-backup-key-01", actor)
	problem := requireProblemCode(t, err, "OPERATION_IN_PROGRESS")
	if problem.Details["operationId"] != operation.ID {
		t.Fatalf("lock points to %v, want %s", problem.Details["operationId"], operation.ID)
	}

	completed := waitForStoredOperation(t, service, operation.ID)
	if completed.Status != "succeeded" {
		t.Fatalf("restore status = %q", completed.Status)
	}
	server, err := service.Server(stoppedServerID)
	if err != nil {
		t.Fatal(err)
	}
	if server.ObservedPower != "stopped" || server.ObservedGeneration != completed.Generation || server.Generation != completed.Generation {
		t.Fatalf("restored server did not converge: %+v operation=%+v", server, completed)
	}
	backups, _ = service.Backups(stoppedServerID)
	if backups[0].Status != "ready" {
		t.Fatalf("backup status after restore = %q, want ready", backups[0].Status)
	}
	if backups[0].FailureCode != nil || backups[0].FailureMessage != nil {
		t.Fatalf("successful restore retained failure metadata: %+v", backups[0])
	}
}

func TestRestoreBackupBlocksFileMutationsUntilCompletion(t *testing.T) {
	service := newTestMemory(250 * time.Millisecond)
	defer func() { _ = service.Close() }()
	actor := testActor("admin-1", "GuGu Admin")
	backupID := "d3333333-3333-4333-8333-333333333333"

	operation, err := service.RestoreBackup(stoppedServerID, backupID, "restore-file-lock-key-01", actor)
	if err != nil {
		t.Fatal(err)
	}

	mutations := []struct {
		name string
		run  func() error
	}{
		{name: "write", run: func() error {
			return service.WriteFile(stoppedServerID, "blocked-write.txt", []byte("blocked"), actor)
		}},
		{name: "create directory", run: func() error {
			return service.CreateDirectory(stoppedServerID, "blocked-directory", actor)
		}},
		{name: "move", run: func() error {
			return service.MoveFile(stoppedServerID, "server-settings.json", "blocked-server-settings.json", false, actor)
		}},
		{name: "delete", run: func() error {
			return service.DeleteFile(stoppedServerID, "server-settings.json", false, actor)
		}},
	}

	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			problem := requireProblemCode(t, mutation.run(), "OPERATION_IN_PROGRESS")
			if problem.Details["operationId"] != operation.ID {
				t.Fatalf("lock points to %v, want %s", problem.Details["operationId"], operation.ID)
			}
		})
	}

	completed := waitForStoredOperation(t, service, operation.ID)
	if completed.Status != "succeeded" {
		t.Fatalf("restore status = %q", completed.Status)
	}
	if err := service.WriteFile(stoppedServerID, "after-restore.txt", []byte("allowed"), actor); err != nil {
		t.Fatalf("write after restore completion: %v", err)
	}
}

func TestStaleRestoreDoesNotInvalidateRecoveryPoint(t *testing.T) {
	service := newTestMemory(0)
	const operationID = "stale-restore-operation"
	const backupID = "d3333333-3333-4333-8333-333333333333"

	service.mu.Lock()
	server := service.servers[stoppedServerID]
	backupIndex, backup, ok := service.backupLocked(stoppedServerID, backupID)
	if !ok {
		service.mu.Unlock()
		t.Fatal("seeded backup is missing")
	}
	backup.Status = "restoring"
	service.backups[stoppedServerID][backupIndex] = backup
	service.operations[operationID] = domain.Operation{
		ID:         operationID,
		ServerID:   stoppedServerID,
		NodeID:     server.NodeID,
		Type:       domain.PowerAction("restore"),
		Status:     "running",
		Generation: server.Generation,
	}
	server.Generation++
	service.servers[stoppedServerID] = server
	service.mu.Unlock()

	service.finishRestoreBackup(operationID, stoppedServerID, backupID, "GuGu Admin")

	completed, err := service.Operation(operationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "failed" {
		t.Fatalf("stale restore status = %q, want failed", completed.Status)
	}
	if completed.Checkpoint != "failed" {
		t.Fatalf("stale restore checkpoint = %q, want failed", completed.Checkpoint)
	}
	if completed.Error == nil || completed.Error.Code != "OPERATION_STALE" || completed.Error.Retryable {
		t.Fatalf("stale restore structured error = %+v, want non-retryable OPERATION_STALE", completed.Error)
	}
	if completed.LeaseOwner != nil || completed.LeaseExpiresAt != nil {
		t.Fatalf("failed restore unexpectedly retains lease metadata: %+v", completed)
	}
	backups, err := service.Backups(stoppedServerID)
	if err != nil {
		t.Fatal(err)
	}
	if backups[0].Status != "ready" {
		t.Fatalf("backup status after stale restore = %q, want ready", backups[0].Status)
	}
}

func TestRestoreDoesNotApplyAnOperationAssignedToAnotherNode(t *testing.T) {
	service := newTestMemory(0)
	const operationID = "stale-node-restore"
	const backupID = "d3333333-3333-4333-8333-333333333333"

	service.mu.Lock()
	server := service.servers[stoppedServerID]
	acceptedNodeID := server.NodeID
	server.NodeID = availableNodeID
	server.DesiredPower = "running"
	server.ObservedPower = "stopping"
	server.ObservedGeneration = server.Generation - 1
	service.servers[stoppedServerID] = server
	backupIndex, backup, ok := service.backupLocked(stoppedServerID, backupID)
	if !ok {
		service.mu.Unlock()
		t.Fatal("seeded backup is missing")
	}
	backup.Status = "restoring"
	service.backups[stoppedServerID][backupIndex] = backup
	service.operations[operationID] = domain.Operation{
		ID:         operationID,
		ServerID:   stoppedServerID,
		NodeID:     acceptedNodeID,
		Type:       domain.PowerAction("restore"),
		Status:     "running",
		Generation: server.Generation,
	}
	service.mu.Unlock()

	service.finishRestoreBackup(operationID, stoppedServerID, backupID, "GuGu Admin")

	completed, err := service.Operation(operationID)
	if err != nil {
		t.Fatal(err)
	}
	assertStaleOperationFailure(t, completed)
	got, err := service.Server(stoppedServerID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DesiredPower != "running" || got.ObservedPower != "stopping" || got.ObservedGeneration == got.Generation {
		t.Fatalf("stale-node restore mutated server state: %+v", got)
	}
	backups, err := service.Backups(stoppedServerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 || backups[0].Status != "ready" {
		t.Fatalf("backup after stale-node restore = %+v, want retained ready recovery point", backups)
	}
}

func TestRestoreStaleTargetTakesPrecedenceOverIntegrityFailure(t *testing.T) {
	service := newTestMemory(0)
	const operationID = "stale-corrupt-restore"
	const backupID = "d3333333-3333-4333-8333-333333333333"

	service.mu.Lock()
	server := service.servers[stoppedServerID]
	acceptedNodeID := server.NodeID
	server.NodeID = availableNodeID
	if server.NodeID == acceptedNodeID {
		server.NodeID = "22222222-2222-4222-8222-222222222222"
	}
	service.servers[stoppedServerID] = server
	backupIndex, backup, ok := service.backupLocked(stoppedServerID, backupID)
	if !ok {
		service.mu.Unlock()
		t.Fatal("seeded backup is missing")
	}
	backup.Status = "restoring"
	backup.Checksum = valuePointer("sha256:" + strings.Repeat("0", 64))
	service.backups[stoppedServerID][backupIndex] = backup
	service.operations[operationID] = domain.Operation{
		ID:         operationID,
		ServerID:   stoppedServerID,
		NodeID:     acceptedNodeID,
		Type:       domain.PowerAction("restore"),
		Status:     "running",
		Generation: server.Generation,
	}
	service.mu.Unlock()

	service.finishRestoreBackup(operationID, stoppedServerID, backupID, "GuGu Admin")

	completed, err := service.Operation(operationID)
	if err != nil {
		t.Fatal(err)
	}
	assertStaleOperationFailure(t, completed)
	service.mu.RLock()
	_, recovered, ok := service.backupLocked(stoppedServerID, backupID)
	service.mu.RUnlock()
	if !ok || recovered.Status != "ready" {
		t.Fatalf("backup after stale corrupt restore = %+v, present=%v; want retained ready recovery point", recovered, ok)
	}
}

func TestDeleteBackupIsIdempotentAndRemovesTheRecoveryPoint(t *testing.T) {
	service := newTestMemory(10 * time.Millisecond)
	actor := testActor("admin-1", "GuGu Admin")
	backupID := "d3333333-3333-4333-8333-333333333333"

	operation, err := service.DeleteBackup(stoppedServerID, backupID, "delete-backup-key-001", actor)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := service.DeleteBackup(stoppedServerID, backupID, "delete-backup-key-001", actor)
	if err != nil || duplicate.ID != operation.ID {
		t.Fatalf("idempotent delete = %+v, %v; want operation %s", duplicate, err, operation.ID)
	}
	backups, _ := service.Backups(stoppedServerID)
	if len(backups) != 1 || backups[0].Status != "deleting" {
		t.Fatalf("backups while deleting = %+v", backups)
	}
	if completed := waitForStoredOperation(t, service, operation.ID); completed.Status != "succeeded" {
		t.Fatalf("delete status = %q", completed.Status)
	}
	backups, _ = service.Backups(stoppedServerID)
	if len(backups) != 0 {
		t.Fatalf("deleted backup remains in list: %+v", backups)
	}
	_, err = service.DeleteBackup(stoppedServerID, backupID, "delete-backup-key-002", actor)
	requireProblemCode(t, err, "NOT_FOUND")
}

func TestDeleteBackupDoesNotApplyAnOperationAssignedToAnotherNode(t *testing.T) {
	service := newTestMemory(0)
	const operationID = "stale-node-backup-delete"
	const backupID = "d3333333-3333-4333-8333-333333333333"

	service.mu.Lock()
	server := service.servers[stoppedServerID]
	acceptedNodeID := server.NodeID
	server.NodeID = availableNodeID
	service.servers[stoppedServerID] = server
	backupIndex, backup, ok := service.backupLocked(stoppedServerID, backupID)
	if !ok {
		service.mu.Unlock()
		t.Fatal("seeded backup is missing")
	}
	backup.Status = "deleting"
	service.backups[stoppedServerID][backupIndex] = backup
	service.operations[operationID] = domain.Operation{
		ID:         operationID,
		ServerID:   stoppedServerID,
		NodeID:     acceptedNodeID,
		Type:       domain.PowerAction("backup-delete"),
		Status:     "running",
		Generation: server.Generation,
	}
	service.mu.Unlock()

	service.finishDeleteBackup(operationID, stoppedServerID, backupID, "GuGu Admin")

	completed, err := service.Operation(operationID)
	if err != nil {
		t.Fatal(err)
	}
	assertStaleOperationFailure(t, completed)
	backups, err := service.Backups(stoppedServerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 || backups[0].ID != backupID || backups[0].Status != "ready" {
		t.Fatalf("backup after stale-node delete = %+v, want retained ready recovery point", backups)
	}
}

func TestDeleteBackupStaleTargetTakesPrecedenceOverMissingBackup(t *testing.T) {
	service := newTestMemory(0)
	const operationID = "stale-missing-backup-delete"
	const backupID = "d3333333-3333-4333-8333-333333333333"

	service.mu.Lock()
	server := service.servers[stoppedServerID]
	service.operations[operationID] = domain.Operation{
		ID:         operationID,
		ServerID:   stoppedServerID,
		NodeID:     server.NodeID,
		Type:       domain.PowerAction("backup-delete"),
		Status:     "running",
		Generation: server.Generation,
	}
	server.Generation++
	service.servers[stoppedServerID] = server
	backupIndex, _, ok := service.backupLocked(stoppedServerID, backupID)
	if !ok {
		service.mu.Unlock()
		t.Fatal("seeded backup is missing")
	}
	stored := service.backups[stoppedServerID]
	service.backups[stoppedServerID] = append(stored[:backupIndex], stored[backupIndex+1:]...)
	delete(service.backupChecksums, backupID)
	service.mu.Unlock()

	service.finishDeleteBackup(operationID, stoppedServerID, backupID, "GuGu Admin")

	completed, err := service.Operation(operationID)
	if err != nil {
		t.Fatal(err)
	}
	assertStaleOperationFailure(t, completed)
}

func TestMissingServerTerminalFailuresAreAudited(t *testing.T) {
	const backupID = "d3333333-3333-4333-8333-333333333333"
	tests := []struct {
		name          string
		operationType domain.PowerAction
		backupStatus  string
		auditAction   string
		finish        func(*Memory, string)
	}{
		{
			name:          "backup",
			operationType: "backup",
			auditAction:   "backup.create",
			finish: func(service *Memory, operationID string) {
				service.finishBackup(operationID, stoppedServerID, "GuGu Admin")
			},
		},
		{
			name:          "restore",
			operationType: "restore",
			backupStatus:  "restoring",
			auditAction:   "backup.restore",
			finish: func(service *Memory, operationID string) {
				service.finishRestoreBackup(operationID, stoppedServerID, backupID, "GuGu Admin")
			},
		},
		{
			name:          "backup delete",
			operationType: "backup-delete",
			backupStatus:  "deleting",
			auditAction:   "backup.delete",
			finish: func(service *Memory, operationID string) {
				service.finishDeleteBackup(operationID, stoppedServerID, backupID, "GuGu Admin")
			},
		},
		{
			name:          "reconcile",
			operationType: "reconcile",
			auditAction:   "server.network.update",
			finish: func(service *Memory, operationID string) {
				service.finishReconcile(operationID, stoppedServerID, "GuGu Admin", "server.network.update")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newTestMemory(0)
			operationID := "missing-server-" + strings.ReplaceAll(test.name, " ", "-")
			service.mu.Lock()
			server := service.servers[stoppedServerID]
			service.operations[operationID] = domain.Operation{
				ID:         operationID,
				ServerID:   stoppedServerID,
				NodeID:     server.NodeID,
				Type:       test.operationType,
				Status:     "running",
				Generation: server.Generation,
			}
			if test.backupStatus != "" {
				backupIndex, backup, ok := service.backupLocked(stoppedServerID, backupID)
				if !ok {
					service.mu.Unlock()
					t.Fatal("seeded backup is missing")
				}
				backup.Status = test.backupStatus
				service.backups[stoppedServerID][backupIndex] = backup
			}
			delete(service.servers, stoppedServerID)
			service.mu.Unlock()
			auditCount := len(service.AuditEvents())

			test.finish(service, operationID)

			completed, err := service.Operation(operationID)
			if err != nil {
				t.Fatal(err)
			}
			assertStaleOperationFailure(t, completed)
			events := service.AuditEvents()
			if len(events) != auditCount+1 {
				t.Fatalf("terminal audit count = %d, want %d", len(events), auditCount+1)
			}
			event := events[0]
			if event.Action != test.auditAction || event.TargetName != stoppedServerID || event.Result != "failure" || event.OperationID != operationID {
				t.Fatalf("terminal audit = %+v, want %s failure for server ID %s and operation %s", event, test.auditAction, stoppedServerID, operationID)
			}
		})
	}
}

func TestFilesRejectAbsolutePathsAtTheStoreBoundary(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	for _, requested := range []string{"/config", `C:\Windows\win.ini`, `\\server\share\file.txt`} {
		t.Run(requested, func(t *testing.T) {
			_, err := service.Files("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", requested)
			requireProblemCode(t, err, "PATH_ESCAPE_BLOCKED")
		})
	}
}

func TestConsoleCommandLimitCountsCharactersNotUTF8Bytes(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	if err := service.SendConsoleCommand("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", strings.Repeat("界", 300), testActor("00000000-0000-4000-8000-000000000001", "GuGu Admin")); err != nil {
		t.Fatalf("300-character command was rejected: %v", err)
	}
	if err := service.SendConsoleCommand("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", strings.Repeat("界", 513), testActor("00000000-0000-4000-8000-000000000001", "GuGu Admin")); err == nil {
		t.Fatal("513-character command was accepted")
	} else {
		requireProblemCode(t, err, "VALIDATION_FAILED")
	}
}

func TestAllPersistedServerTaskTypesUseTheExclusiveOperationGate(t *testing.T) {
	for _, operationType := range []domain.PowerAction{
		"provision", "start", "stop", "restart", "kill", "backup", "restore", "backup-delete", "delete", "reconcile",
	} {
		if !isExclusiveOperation(operationType) {
			t.Errorf("operation type %q bypasses the exclusive gate", operationType)
		}
	}
}

func validCreateServerInput() domain.CreateServerInput {
	return domain.CreateServerInput{
		Name:             "Regression world",
		GameDefinitionID: "io.gugumanager.papermc",
		GameBundleDigest: "sha256:412759ce8b7832b3762d1a6f34d076ecceebd4ecd7cd7d04f16ac0cff063285b",
		NodeID:           availableNodeID,
		MemoryMB:         1024,
		DiskGB:           5,
	}
}

func TestNextAllocationPortReportsExhaustion(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	nodeID := "11111111-1111-4111-8111-111111111111"
	bindIP := "10.0.10.21"
	service.allocations["occupied-last-port"] = domain.Allocation{
		ID: "occupied-last-port", NodeID: nodeID, BindIP: bindIP, Port: 65535, Protocol: "tcp",
	}

	if port, ok := service.nextAllocationPortLocked(nodeID, bindIP, "tcp", 65535); ok || port != 0 {
		t.Fatalf("exhausted allocation port = %d, ok=%v; want 0, false", port, ok)
	}
}

func newTestMemory(latency time.Duration) *Memory {
	service := NewMemory("development", "admin@gugu.local", "gugu-dev-2026", "agent-token", latency)
	// Existing lifecycle tests exercise behavior after a runtime target has
	// been admitted. Production constructors remain fail-closed; this test-only
	// fixture supplies that evidence explicitly.
	for gameID, game := range service.games {
		game.Runnable = true
		service.games[gameID] = game
	}
	return service
}

func testActor(id string, name string) domain.User {
	if id == "admin-1" {
		id = "00000000-0000-4000-8000-000000000001"
	}
	return domain.User{ID: id, DisplayName: name}
}

func requireProblemCode(t *testing.T, err error, want string) *domain.Problem {
	t.Helper()
	var problem *domain.Problem
	if !errors.As(err, &problem) {
		t.Fatalf("error = %v, want domain problem %q", err, want)
	}
	if problem.Code != want {
		t.Fatalf("problem code = %q, want %q", problem.Code, want)
	}
	return problem
}

func waitForStoredOperation(t *testing.T, service *Memory, operationID string) domain.Operation {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		operation, err := service.Operation(operationID)
		if err != nil {
			t.Fatal(err)
		}
		if isTerminalOperation(operation.Status) {
			return operation
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("operation %s did not reach a terminal state", operationID)
	return domain.Operation{}
}
