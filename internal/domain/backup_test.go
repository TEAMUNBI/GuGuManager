package domain

import (
	"encoding/json"
	"testing"
)

func TestBackupUnknownMetadataSerializesAsNull(t *testing.T) {
	encoded, err := json.Marshal(Backup{ID: "backup-1", Name: "creating", Status: "creating"})
	if err != nil {
		t.Fatalf("marshal backup: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("decode backup: %v", err)
	}
	if object["sizeBytes"] != nil {
		t.Fatalf("sizeBytes = %v, want null", object["sizeBytes"])
	}
	if object["checksum"] != nil {
		t.Fatalf("checksum = %v, want null", object["checksum"])
	}
	if object["manifestDigest"] != nil {
		t.Fatalf("manifestDigest = %v, want null", object["manifestDigest"])
	}
	if object["failureCode"] != nil || object["failureMessage"] != nil || object["deletedAt"] != nil {
		t.Fatalf("failure/deletion metadata = %v/%v/%v, want null", object["failureCode"], object["failureMessage"], object["deletedAt"])
	}
}

func TestBackupReadyMetadataSerializesValues(t *testing.T) {
	size := int64(2048)
	checksum := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	manifestDigest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	encoded, err := json.Marshal(Backup{ID: "backup-2", Name: "ready", Status: "ready", SizeBytes: &size, Checksum: &checksum, ManifestDigest: &manifestDigest})
	if err != nil {
		t.Fatalf("marshal backup: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("decode backup: %v", err)
	}
	if object["sizeBytes"] != float64(size) {
		t.Fatalf("sizeBytes = %v, want %d", object["sizeBytes"], size)
	}
	if object["checksum"] != checksum {
		t.Fatalf("checksum = %v, want %q", object["checksum"], checksum)
	}
	if object["manifestDigest"] != manifestDigest {
		t.Fatalf("manifestDigest = %v, want %q", object["manifestDigest"], manifestDigest)
	}
}

func TestBackupFailedMetadataSerializesValues(t *testing.T) {
	code := "BACKUP_INTEGRITY_FAILED"
	message := "backup checksum does not match the recorded manifest"
	encoded, err := json.Marshal(Backup{ID: "backup-3", Name: "failed", Status: "failed", FailureCode: &code, FailureMessage: &message})
	if err != nil {
		t.Fatalf("marshal backup: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("decode backup: %v", err)
	}
	if object["failureCode"] != code || object["failureMessage"] != message {
		t.Fatalf("failure metadata = %v/%v, want %q/%q", object["failureCode"], object["failureMessage"], code, message)
	}
}

func TestBackupStatusTransitions(t *testing.T) {
	tests := []struct {
		from, to string
		allowed  bool
	}{
		{"creating", "ready", true},
		{"creating", "failed", true},
		{"failed", "creating", true},
		{"failed", "deleting", true},
		{"failed", "restoring", false},
		{"ready", "restoring", true},
		{"restoring", "ready", true},
		{"ready", "deleting", true},
		{"deleting", "ready", true},
		{"deleting", "failed", true},
		{"deleting", "deleted", true},
		{"creating", "restoring", false},
		{"deleted", "ready", false},
	}
	for _, test := range tests {
		if got := BackupStatusTransitionAllowed(test.from, test.to); got != test.allowed {
			t.Errorf("transition %s -> %s = %v, want %v", test.from, test.to, got, test.allowed)
		}
	}
}
