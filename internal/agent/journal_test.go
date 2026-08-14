package agent

import (
	"path/filepath"
	"testing"

	agentv1 "github.com/gugumanager/gugumanager/api/proto/gugumanager/agent/v1"
	"google.golang.org/protobuf/proto"
)

func TestOperationJournalRecordLookupComplete(t *testing.T) {
	journal, err := OpenOperationJournal(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	defer journal.Close()

	if err := journal.RecordRunning("op-1", "digest-a", 1); err != nil {
		t.Fatalf("record running: %v", err)
	}
	entry, ok, err := journal.Lookup("op-1")
	if err != nil || !ok {
		t.Fatalf("lookup running entry: ok=%t err=%v", ok, err)
	}
	if entry.Status != "running" || entry.Digest != "digest-a" || entry.Attempt != 1 {
		t.Fatalf("running entry = %+v", entry)
	}

	if err := journal.Complete("op-1", "digest-a", 1, true, "", false, "done", []byte(`{"r":1}`), []byte(`{"observed":true}`)); err != nil {
		t.Fatalf("complete: %v", err)
	}
	entry, ok, err = journal.Lookup("op-1")
	if err != nil || !ok {
		t.Fatalf("lookup terminal entry: ok=%t err=%v", ok, err)
	}
	if entry.Status != "succeeded" || string(entry.ResultJSON) != `{"r":1}` {
		t.Fatalf("terminal entry = %+v", entry)
	}

	if _, ok, err := journal.Lookup("missing"); err != nil || ok {
		t.Fatalf("missing lookup = ok:%t err:%v, want false/nil", ok, err)
	}
}

func TestOperationJournalRejectsDigestMismatch(t *testing.T) {
	journal, err := OpenOperationJournal(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	defer journal.Close()

	if err := journal.RecordRunning("op-1", "digest-a", 1); err != nil {
		t.Fatalf("record running: %v", err)
	}
	if err := journal.RecordRunning("op-1", "digest-b", 2); err != ErrJournalDigestMismatch {
		t.Fatalf("redelivery with different digest = %v, want ErrJournalDigestMismatch", err)
	}
	if err := journal.Complete("op-1", "digest-b", 2, true, "", false, "", nil, nil); err != ErrJournalDigestMismatch {
		t.Fatalf("complete with different digest = %v, want ErrJournalDigestMismatch", err)
	}
	entry, ok, err := journal.Lookup("op-1")
	if err != nil || !ok || entry.Digest != "digest-a" || entry.Status != "running" {
		t.Fatalf("entry after mismatched writes = %+v (ok=%t err=%v)", entry, ok, err)
	}
}

func TestOperationJournalPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	journal, err := OpenOperationJournal(path)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	if err := journal.RecordRunning("op-persist", "digest-p", 3); err != nil {
		t.Fatalf("record running: %v", err)
	}
	if err := journal.Complete("op-persist", "digest-p", 3, false, "X", false, "", []byte(`{"code":"X"}`), nil); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := OpenOperationJournal(path)
	if err != nil {
		t.Fatalf("reopen journal: %v", err)
	}
	defer reopened.Close()
	entry, ok, err := reopened.Lookup("op-persist")
	if err != nil || !ok {
		t.Fatalf("lookup after reopen: ok=%t err=%v", ok, err)
	}
	if entry.Status != "failed" || entry.Attempt != 3 {
		t.Fatalf("persisted entry = %+v", entry)
	}
}

func testOpID(i int) string {
	return "op-" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + "-" + itoa(i)
}

func TestOperationJournalEvictsOldestBeyondCap(t *testing.T) {
	journal, err := OpenOperationJournal(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	defer journal.Close()

	for i := 0; i < journalMaxEntries+16; i++ {
		if err := journal.RecordRunning(testOpID(i), "digest", 1); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	if _, ok, err := journal.Lookup(testOpID(0)); err != nil || ok {
		t.Fatalf("oldest entry should be evicted: ok=%t err=%v", ok, err)
	}
	if _, ok, err := journal.Lookup(testOpID(journalMaxEntries + 15)); err != nil || !ok {
		t.Fatalf("latest entry missing: ok=%t err=%v", ok, err)
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

func TestTaskPayloadDigestIgnoresFenceFields(t *testing.T) {
	original := fencedPowerTask("op-1", "server-1", agentv1.PowerAction_POWER_ACTION_START)
	redelivered := fencedPowerTask("op-1", "server-1", agentv1.PowerAction_POWER_ACTION_START)
	redelivered.LeaseToken = "lease-rotated"
	redelivered.ConnectionEpoch = 42
	redelivered.Attempt = 3

	if taskPayloadDigest(original) != taskPayloadDigest(redelivered) {
		t.Fatal("digest changed when only lease fence fields changed")
	}

	changed := proto.Clone(original).(*agentv1.Task)
	changed.Payload = &agentv1.Task_Power{Power: &agentv1.PowerTaskPayload{Action: agentv1.PowerAction_POWER_ACTION_STOP}}
	if taskPayloadDigest(original) == taskPayloadDigest(changed) {
		t.Fatal("digest must change when the task payload changes")
	}

	generationChanged := proto.Clone(original).(*agentv1.Task)
	generationChanged.Generation = 3
	if taskPayloadDigest(original) == taskPayloadDigest(generationChanged) {
		t.Fatal("digest must change when the task generation changes")
	}
}
