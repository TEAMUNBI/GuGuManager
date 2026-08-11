package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentv1 "github.com/gugumanager/gugumanager/api/proto/gugumanager/agent/v1"
	runtimeadapter "github.com/gugumanager/gugumanager/internal/runtime"
	"google.golang.org/protobuf/encoding/protojson"
)

// TestDockerBackupRestoreDeleteLifecycle exercises the real Docker copy,
// archive validation, host-side atomic restore and deletion path. CI enables
// it on a runner with Docker; developer machines opt in explicitly.
func TestDockerBackupRestoreDeleteLifecycle(t *testing.T) {
	if os.Getenv("GUGU_TEST_DOCKER") != "1" {
		t.Skip("set GUGU_TEST_DOCKER=1 to run the Docker lifecycle integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	docker, err := runtimeadapter.NewDockerRuntime()
	if err != nil {
		t.Fatalf("connect Docker: %v", err)
	}
	defer docker.Close()

	root := t.TempDir()
	serverID := "integration-backup"
	containerName := "gugu-server-" + serverID
	dataDir := filepath.Join(root, serverID)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("create data directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "before.txt"), []byte("before"), 0o644); err != nil {
		t.Fatalf("write initial data: %v", err)
	}
	containerID, err := docker.CreateContainer(ctx, runtimeadapter.ContainerConfig{
		Name:       containerName,
		Image:      "nginx:1.29-alpine",
		VolumePath: dataDir,
		MemoryMB:   256,
		CPUShares:  256,
	})
	if err != nil {
		t.Fatalf("create Docker container: %v", err)
	}
	defer docker.RemoveContainer(context.Background(), containerID, true)

	exec := &DockerExecutor{dataRoot: root, rt: docker}
	backupID := "integration-backup-1"
	createPayload, err := protojson.Marshal(&agentv1.BackupTaskPayload{Action: &agentv1.BackupTaskPayload_Create{
		Create: &agentv1.CreateBackupPayload{BackupId: backupID, StorageObjectKey: "backups/" + backupID + ".tar.gz"},
	}})
	if err != nil {
		t.Fatalf("marshal create payload: %v", err)
	}
	createOutcome, err := exec.ExecuteTask(ctx, &agentv1.Task{Type: "backup", ServerId: serverID, Payload: &agentv1.Task_PayloadJson{PayloadJson: createPayload}})
	if err != nil || !createOutcome.Succeeded {
		t.Fatalf("create backup outcome=%+v err=%v", createOutcome, err)
	}
	var result struct {
		BackupID        string `json:"backupId"`
		Checksum        string `json:"checksum"`
		ManifestDigest  string `json:"manifestDigest"`
		StorageLocation string `json:"storageLocation"`
	}
	if err := json.Unmarshal(createOutcome.ResultJSON, &result); err != nil {
		t.Fatalf("decode create result: %v", err)
	}
	if result.BackupID != backupID || result.Checksum == "" || result.ManifestDigest == "" || result.StorageLocation == "" {
		t.Fatalf("incomplete create result: %+v", result)
	}

	if err := os.WriteFile(filepath.Join(dataDir, "before.txt"), []byte("mutated"), 0o644); err != nil {
		t.Fatalf("mutate current data: %v", err)
	}
	if err := docker.StopContainer(ctx, containerID, 10); err != nil {
		t.Fatalf("stop container before restore: %v", err)
	}
	restorePayload, err := protojson.Marshal(&agentv1.BackupTaskPayload{Action: &agentv1.BackupTaskPayload_Restore{
		Restore: &agentv1.RestoreBackupPayload{
			BackupId: backupID, StorageObjectKey: result.StorageLocation,
			ExpectedContentDigest: result.Checksum, ExpectedManifestDigest: result.ManifestDigest,
		},
	}})
	if err != nil {
		t.Fatalf("marshal restore payload: %v", err)
	}
	restoreOutcome, err := exec.ExecuteTask(ctx, &agentv1.Task{Type: "backup", ServerId: serverID, Payload: &agentv1.Task_PayloadJson{PayloadJson: restorePayload}})
	if err != nil || !restoreOutcome.Succeeded {
		t.Fatalf("restore outcome=%+v err=%v", restoreOutcome, err)
	}
	restored, err := os.ReadFile(filepath.Join(dataDir, "before.txt"))
	if err != nil || string(restored) != "before" {
		t.Fatalf("restored file = %q, err=%v", restored, err)
	}
	if err := docker.StartContainer(ctx, containerID); err != nil {
		t.Fatalf("start container after restore: %v", err)
	}

	deletePayload, err := protojson.Marshal(&agentv1.BackupTaskPayload{Action: &agentv1.BackupTaskPayload_Delete{
		Delete: &agentv1.DeleteBackupPayload{BackupId: backupID, StorageObjectKey: result.StorageLocation, DeleteRemoteObject: true},
	}})
	if err != nil {
		t.Fatalf("marshal delete payload: %v", err)
	}
	deleteOutcome, err := exec.ExecuteTask(ctx, &agentv1.Task{Type: "backup", ServerId: serverID, Payload: &agentv1.Task_PayloadJson{PayloadJson: deletePayload}})
	if err != nil || !deleteOutcome.Succeeded {
		t.Fatalf("delete outcome=%+v err=%v", deleteOutcome, err)
	}
	if _, err := os.Stat(filepath.Join(root, result.StorageLocation)); !os.IsNotExist(err) {
		t.Fatalf("backup archive still exists, stat err=%v", err)
	}
}
