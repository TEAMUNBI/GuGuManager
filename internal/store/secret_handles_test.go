package store

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

func TestSecretHandleRoundTripAndDigestDoesNotRevealToken(t *testing.T) {
	raw := []byte("01234567890123456789012345678901")
	handle := secretHandleValue(raw)
	parsed, err := parseSecretHandle(handle)
	if err != nil {
		t.Fatal(err)
	}
	if string(parsed) != string(raw) {
		t.Fatalf("parsed token = %q, want original", parsed)
	}
	digest := sha256.Sum256(parsed)
	if strings.Contains(base64.RawStdEncoding.EncodeToString(digest[:]), base64.RawStdEncoding.EncodeToString(raw)) {
		t.Fatal("digest unexpectedly contains raw token")
	}
}

func TestSecretHandleRejectsMalformedValues(t *testing.T) {
	for _, handle := range []string{"", "sh:v1:", "sh:v1:not-base64", "enc:v1:abc"} {
		if _, err := parseSecretHandle(handle); err == nil {
			t.Fatalf("handle %q was accepted", handle)
		}
	}
}

func TestSecretHandleSnapshotRequiresCiphertext(t *testing.T) {
	for _, ciphertext := range []string{"enc:v1:legacy", "enc:v2:current:sealed"} {
		got, ok, err := secretHandleSnapshot(map[string]any{"rcon_password": ciphertext}, "rcon_password")
		if err != nil || !ok || got != ciphertext {
			t.Fatalf("snapshot %q = %q, %v, %v", ciphertext, got, ok, err)
		}
	}
	if _, _, err := secretHandleSnapshot(map[string]any{"rcon_password": "plaintext"}, "rcon_password"); err == nil {
		t.Fatal("plaintext Secret was accepted for a handle snapshot")
	}
	if _, ok, err := secretHandleSnapshot(map[string]any{}, "rcon_password"); err != nil || ok {
		t.Fatalf("missing Secret snapshot = ok %v, err %v", ok, err)
	}
}
