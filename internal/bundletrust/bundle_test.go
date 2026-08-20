package bundletrust

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestVerifyOfficialSignedBundle(t *testing.T) {
	_, current, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(current), "..", "..", "spec", "game-definition", "official")
	document, err := os.ReadFile(filepath.Join(root, "papermc.bundle.signed.json"))
	if err != nil {
		t.Fatal(err)
	}
	trustRoot, err := os.ReadFile(filepath.Join(root, "official-ed25519.pub.pem"))
	if err != nil {
		t.Fatal(err)
	}
	verified, err := Verify(document, trustRoot)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verified.Document.GameDefinitionID != "io.gugumanager.papermc" || verified.RuntimeTarget.Digest == "" {
		t.Fatalf("verified = %+v", verified)
	}
	document[len(document)/2] ^= 1
	if _, err := Verify(document, trustRoot); err == nil {
		t.Fatal("tampered signed Bundle was accepted")
	}
}
