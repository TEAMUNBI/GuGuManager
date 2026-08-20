package objectstore

import (
	"bytes"
	"context"
	"os"
	"testing"
)

func TestLocalEncryptedRoundTripAndTamperDetection(t *testing.T) {
	store, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keyring, err := NewKeyring("current", map[string][]byte{"current": []byte("test-master-key")})
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("gugu-backup\x00"), 500000)
	manifest, err := EncryptUpload(context.Background(), store, "sha256/aa/object.enc", bytes.NewReader(payload), int64(len(payload)), keyring)
	if err != nil {
		t.Fatal(err)
	}
	var restored bytes.Buffer
	if err := DecryptDownload(context.Background(), store, manifest, &restored, keyring); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored.Bytes(), payload) {
		t.Fatal("restored payload differs")
	}

	path, _ := store.objectPath(manifest.ObjectKey)
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte{0xff}, 32); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if err := DecryptDownload(context.Background(), store, manifest, &bytes.Buffer{}, keyring); err == nil {
		t.Fatal("tampered object was accepted")
	}
}

func TestLocalRejectsEscapingObjectKeys(t *testing.T) {
	store, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"../escape", "/absolute", `folder\\escape`, "a/../b"} {
		if _, err := store.Put(context.Background(), key, bytes.NewReader(nil), 0, PutOptions{}); err == nil {
			t.Errorf("key %q was accepted", key)
		}
	}
}
