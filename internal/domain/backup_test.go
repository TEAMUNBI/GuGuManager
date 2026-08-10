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
}

func TestBackupReadyMetadataSerializesValues(t *testing.T) {
	size := int64(2048)
	checksum := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	encoded, err := json.Marshal(Backup{ID: "backup-2", Name: "ready", Status: "ready", SizeBytes: &size, Checksum: &checksum})
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
}
