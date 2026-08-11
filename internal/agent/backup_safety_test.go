package agent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/gugumanager/gugumanager/api/proto/gugumanager/agent/v1"
	"github.com/gugumanager/gugumanager/internal/runtime"
	"google.golang.org/protobuf/encoding/protojson"
)

func writeTestBackupPayload(t *testing.T, filePath, entryName, content string, entryType byte) {
	t.Helper()
	file, err := os.Create(filePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	header := &tar.Header{Name: entryName, Mode: 0o600, Size: int64(len(content)), Typeflag: entryType}
	if entryType == tar.TypeSymlink {
		header.Size = 0
		header.Linkname = "../../outside"
	}
	if err := tarWriter.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if header.Size > 0 {
		if _, err := tarWriter.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func backupTask(t *testing.T, serverID string, payload *agentv1.BackupTaskPayload) *agentv1.Task {
	t.Helper()
	encoded, err := protojson.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return &agentv1.Task{
		OperationId: "op-backup-safety",
		ServerId:    serverID,
		Type:        "backup",
		Attempt:     1,
		Payload:     &agentv1.Task_PayloadJson{PayloadJson: encoded},
	}
}

func TestBackupCreatePublishesRealGzipAndReplaysImmutableResult(t *testing.T) {
	root := t.TempDir()
	docker := newFakeDocker()
	executor := &DockerExecutor{dataRoot: root, rt: docker}
	task := backupTask(t, "srv-1", &agentv1.BackupTaskPayload{Action: &agentv1.BackupTaskPayload_Create{
		Create: &agentv1.CreateBackupPayload{BackupId: "backup-1", StorageObjectKey: "backups/backup-1.tar.gz"},
	}})

	first, err := executor.ExecuteTask(context.Background(), task)
	if err != nil || !first.Succeeded {
		t.Fatalf("first create = %+v, err=%v", first, err)
	}
	archive := filepath.Join(root, "backups", "backup-1.tar.gz")
	content, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) < 2 || content[0] != 0x1f || content[1] != 0x8b {
		t.Fatalf("published backup is not gzip: %x", content[:min(8, len(content))])
	}
	second, err := executor.ExecuteTask(context.Background(), task)
	if err != nil || !second.Succeeded {
		t.Fatalf("replayed create = %+v, err=%v", second, err)
	}
	if string(first.ResultJSON) != string(second.ResultJSON) {
		t.Fatalf("replayed result changed: first=%s second=%s", first.ResultJSON, second.ResultJSON)
	}
	docker.mu.Lock()
	execCalls := len(docker.execArgv)
	docker.mu.Unlock()
	if execCalls != 2 {
		t.Fatalf("immutable replay executed container again: calls=%d", execCalls)
	}
	partials, err := filepath.Glob(filepath.Join(root, "backups", "*.partial"))
	if err != nil || len(partials) != 0 {
		t.Fatalf("partial archives remain: %v, err=%v", partials, err)
	}
}

func TestBackupCreateCopyFailureLeavesNoPublishedOrPartialArchive(t *testing.T) {
	root := t.TempDir()
	docker := newFakeDocker()
	docker.copyFromErr = errors.New("copy interrupted")
	executor := &DockerExecutor{dataRoot: root, rt: docker}
	task := backupTask(t, "srv-1", &agentv1.BackupTaskPayload{Action: &agentv1.BackupTaskPayload_Create{
		Create: &agentv1.CreateBackupPayload{BackupId: "backup-1", StorageObjectKey: "backups/backup-1.tar.gz"},
	}})
	outcome, err := executor.ExecuteTask(context.Background(), task)
	if err != nil || outcome.Succeeded || !outcome.Retryable {
		t.Fatalf("copy failure outcome=%+v err=%v", outcome, err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("copy failure left backup artifacts: %v", entries)
	}
}

func TestBackupPathsCannotEscapeDataRoot(t *testing.T) {
	root := t.TempDir()
	docker := newFakeDocker()
	docker.status = runtime.ContainerStatus{ID: "cont-abc", State: "exited", Status: "exited", Running: false}
	executor := &DockerExecutor{dataRoot: root, rt: docker}

	create := backupTask(t, "srv-1", &agentv1.BackupTaskPayload{Action: &agentv1.BackupTaskPayload_Create{
		Create: &agentv1.CreateBackupPayload{BackupId: "../escape", StorageObjectKey: "../escape.tar.gz"},
	}})
	createOutcome, err := executor.ExecuteTask(context.Background(), create)
	if err != nil || createOutcome.Succeeded || createOutcome.ErrorCode != "BACKUP_INTEGRITY_FAILED" {
		t.Fatalf("unsafe create outcome=%+v err=%v", createOutcome, err)
	}

	restore := backupTask(t, "srv-1", &agentv1.BackupTaskPayload{Action: &agentv1.BackupTaskPayload_Restore{
		Restore: &agentv1.RestoreBackupPayload{BackupId: "backup-1", StorageObjectKey: "backups/../../escape.tar.gz"},
	}})
	restoreOutcome, err := executor.ExecuteTask(context.Background(), restore)
	if err != nil || restoreOutcome.Succeeded || restoreOutcome.ErrorCode != "BACKUP_INTEGRITY_FAILED" {
		t.Fatalf("unsafe restore outcome=%+v err=%v", restoreOutcome, err)
	}

	deleteTask := backupTask(t, "srv-1", &agentv1.BackupTaskPayload{Action: &agentv1.BackupTaskPayload_Delete{
		Delete: &agentv1.DeleteBackupPayload{BackupId: "backup-1", StorageObjectKey: "../escape.tar.gz", DeleteRemoteObject: true},
	}})
	deleteOutcome, err := executor.ExecuteTask(context.Background(), deleteTask)
	if err != nil || deleteOutcome.Succeeded || deleteOutcome.ErrorCode != "BACKUP_INTEGRITY_FAILED" {
		t.Fatalf("unsafe delete outcome=%+v err=%v", deleteOutcome, err)
	}
	if _, err := executor.downloadBackup(context.Background(), "../escape"); err == nil {
		t.Fatal("unsafe backup download id was accepted")
	}
}

func TestRestoreRequiresActuallyStoppedContainer(t *testing.T) {
	docker := newFakeDocker()
	executor := &DockerExecutor{dataRoot: t.TempDir(), rt: docker}
	task := backupTask(t, "srv-1", &agentv1.BackupTaskPayload{Action: &agentv1.BackupTaskPayload_Restore{
		Restore: &agentv1.RestoreBackupPayload{BackupId: "backup-1", StorageObjectKey: "backups/backup-1.tar.gz"},
	}})
	outcome, err := executor.ExecuteTask(context.Background(), task)
	if err != nil || outcome.Succeeded || outcome.ErrorCode != "SERVER_MUST_BE_STOPPED" || outcome.Retryable {
		t.Fatalf("running restore outcome=%+v err=%v", outcome, err)
	}
}

func TestBackupManifestIncludesFileContent(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first.tar.gz")
	second := filepath.Join(root, "second.tar.gz")
	writeTestBackupPayload(t, first, "world/level.dat", "AAAA", tar.TypeReg)
	writeTestBackupPayload(t, second, "world/level.dat", "BBBB", tar.TypeReg)
	firstDigest, err := backupManifestDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := backupManifestDigest(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest == secondDigest {
		t.Fatalf("same-size content changes did not affect manifest digest: %s", firstDigest)
	}
}

func TestBackupManifestRejectsLinksAndTraversal(t *testing.T) {
	root := t.TempDir()
	linkArchive := filepath.Join(root, "link.tar.gz")
	writeTestBackupPayload(t, linkArchive, "world/link", "", tar.TypeSymlink)
	if _, err := backupManifestDigest(linkArchive); err == nil {
		t.Fatal("symlink backup entry was accepted")
	}
	traversalArchive := filepath.Join(root, "traversal.tar.gz")
	writeTestBackupPayload(t, traversalArchive, "../../outside", "data", tar.TypeReg)
	if _, err := backupManifestDigest(traversalArchive); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("traversal entry error=%v, want unsafe path rejection", err)
	}
}

func TestInterruptedRestoreRollsBackOnExecutorStartup(t *testing.T) {
	root := t.TempDir()
	serverID := "srv-1"
	stagingName := ".srv-1.restore-test"
	previousName := stagingName + ".previous"
	if err := os.Mkdir(filepath.Join(root, stagingName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, stagingName, "new.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, previousName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, previousName, "old.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := restoreMarker{Version: restoreMarkerVersion, ServerID: serverID, Staging: stagingName, Previous: previousName, Phase: "old-moved"}
	if err := writeRestoreMarker(root, marker); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDockerExecutor(root); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(root, serverID, "old.txt")); err != nil || string(content) != "old" {
		t.Fatalf("old data was not restored: content=%q err=%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(root, stagingName)); !os.IsNotExist(err) {
		t.Fatalf("staging directory remains: %v", err)
	}
	if _, err := os.Stat(restoreMarkerPath(root, serverID)); !os.IsNotExist(err) {
		t.Fatalf("restore marker remains: %v", err)
	}
}

func TestActivatedRestoreCleanupResumesOnExecutorStartup(t *testing.T) {
	root := t.TempDir()
	serverID := "srv-1"
	stagingName := ".srv-1.restore-test"
	previousName := stagingName + ".previous"
	if err := os.Mkdir(filepath.Join(root, serverID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, serverID, "new.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, previousName), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := restoreMarker{Version: restoreMarkerVersion, ServerID: serverID, Staging: stagingName, Previous: previousName, Phase: "activated"}
	if err := writeRestoreMarker(root, marker); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDockerExecutor(root); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(root, serverID, "new.txt")); err != nil || string(content) != "new" {
		t.Fatalf("activated data changed: content=%q err=%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(root, previousName)); !os.IsNotExist(err) {
		t.Fatalf("previous directory remains: %v", err)
	}
}
