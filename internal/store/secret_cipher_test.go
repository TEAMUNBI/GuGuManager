package store

import (
	"strings"
	"testing"

	"github.com/gugumanager/gugumanager/internal/domain"
)

func TestSecretCipherRoundTrip(t *testing.T) {
	cipher, err := newSecretCipher([]byte("master-key-任意长度-1234567890"))
	if err != nil {
		t.Fatalf("new secret cipher: %v", err)
	}

	cases := []any{
		"plain secret text",
		"含中文与特殊字符 RCON_密码#@!",
		int64(42),
		true,
	}
	for _, value := range cases {
		sealed, err := cipher.EncryptValue(value)
		if err != nil {
			t.Fatalf("encrypt %v: %v", value, err)
		}
		if !strings.HasPrefix(sealed, secretCipherPrefix) {
			t.Fatalf("encrypted value %q lacks prefix", sealed)
		}
		if strings.Contains(sealed, "plain") && value == "plain secret text" {
			t.Fatalf("encrypted value leaks plaintext: %q", sealed)
		}
		decrypted, err := cipher.DecryptValue(sealed)
		if err != nil {
			t.Fatalf("decrypt %q: %v", sealed, err)
		}
		if decrypted != value {
			t.Fatalf("round trip mismatch: got %v (%T), want %v (%T)", decrypted, decrypted, value, value)
		}
	}
}

func TestSecretCipherRandomizedNonce(t *testing.T) {
	cipher, err := newSecretCipher([]byte("same-master-key"))
	if err != nil {
		t.Fatalf("new secret cipher: %v", err)
	}
	first, _ := cipher.EncryptValue("identical value")
	second, _ := cipher.EncryptValue("identical value")
	if first == second {
		t.Fatal("same plaintext produced identical ciphertext; nonce must be random")
	}
}

func TestSecretCipherPlaintextCompatAndErrors(t *testing.T) {
	cipher, err := newSecretCipher([]byte("master-key"))
	if err != nil {
		t.Fatalf("new secret cipher: %v", err)
	}

	// 无前缀的旧明文数据按原样返回（升级兼容）。
	if got, err := cipher.DecryptValue("legacy-plain-text"); err != nil || got != "legacy-plain-text" {
		t.Fatalf("plaintext compat: got %v, %v", got, err)
	}

	// 截断/篡改的密文必须报错，不能静默返回。
	if _, err := cipher.DecryptValue(secretCipherPrefix + "not-base64!"); err == nil {
		t.Fatal("expected decode error for invalid ciphertext")
	}
	sealed, err := cipher.EncryptValue("secret")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	tampered := sealed[:len(sealed)-2] + "AA"
	if _, err := cipher.DecryptValue(tampered); err == nil {
		t.Fatal("expected auth failure for tampered ciphertext")
	}

	// 不同密钥解不开。
	other, _ := newSecretCipher([]byte("different-master-key"))
	if _, err := other.DecryptValue(sealed); err == nil {
		t.Fatal("expected decryption failure with wrong key")
	}
}

func TestSecretCipherHelpers(t *testing.T) {
	s := &Postgres{}
	if err := s.SetSecretCipher([]byte("master-key-for-helpers")); err != nil {
		t.Fatalf("set secret cipher: %v", err)
	}
	definitions := []domain.StartupVariable{
		{Key: "rcon_password", Secret: true},
		{Key: "memory_mb", Secret: false},
	}
	values := map[string]any{
		"rcon_password": "hunter2",
		"memory_mb":     int64(1024),
	}

	if err := s.encryptSecretValues(definitions, values); err != nil {
		t.Fatalf("encrypt secret values: %v", err)
	}
	if got := values["rcon_password"].(string); !strings.HasPrefix(got, secretCipherPrefix) {
		t.Fatalf("rcon_password not encrypted: %q", got)
	}
	if values["memory_mb"] != int64(1024) {
		t.Fatalf("non-secret value must be untouched: %v", values["memory_mb"])
	}

	// 幂等：已加密的值再加密保持不变。
	if err := s.encryptSecretValues(definitions, values); err != nil {
		t.Fatalf("re-encrypt: %v", err)
	}

	if err := s.decryptSecretValues(definitions, values); err != nil {
		t.Fatalf("decrypt secret values: %v", err)
	}
	if values["rcon_password"] != "hunter2" {
		t.Fatalf("decrypted secret mismatch: %v", values["rcon_password"])
	}
}

func TestStartupValuesForStorageEncryptsCopyWithoutMutatingRuntimeValues(t *testing.T) {
	s := &Postgres{}
	if err := s.SetSecretCipher([]byte("master-key-for-storage-copy")); err != nil {
		t.Fatalf("set secret cipher: %v", err)
	}
	definitions := []domain.StartupVariable{
		{Key: "rcon_password", Secret: true},
		{Key: "memory_mb", Secret: false},
	}
	runtimeValues := map[string]any{
		"rcon_password": "agent-needs-plaintext",
		"memory_mb":     int64(2048),
	}

	storedValues, err := s.startupValuesForStorage(definitions, runtimeValues)
	if err != nil {
		t.Fatalf("prepare startup values for storage: %v", err)
	}
	if runtimeValues["rcon_password"] != "agent-needs-plaintext" {
		t.Fatalf("storage preparation mutated runtime secret: %v", runtimeValues["rcon_password"])
	}
	storedSecret, ok := storedValues["rcon_password"].(string)
	if !ok || !strings.HasPrefix(storedSecret, secretCipherPrefix) {
		t.Fatalf("stored secret is not encrypted: %v", storedValues["rcon_password"])
	}
	if storedValues["memory_mb"] != int64(2048) {
		t.Fatalf("stored non-secret value changed: %v", storedValues["memory_mb"])
	}
}

func TestSecretKeyringUsesActiveKeyAndDecryptsPreviousKeys(t *testing.T) {
	keyring, err := newSecretKeyring("current", map[string][]byte{
		"current":  []byte("current-master-key"),
		"previous": []byte("previous-master-key"),
	})
	if err != nil {
		t.Fatalf("new keyring: %v", err)
	}

	sealed, err := keyring.EncryptValue("rcon-password")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !strings.HasPrefix(sealed, secretKeyringPrefix+"current:") {
		t.Fatalf("ciphertext = %q, want active key id", sealed)
	}
	value, err := keyring.DecryptValue(sealed)
	if err != nil || value != "rcon-password" {
		t.Fatalf("decrypt current value = %v, %v", value, err)
	}

	previous, err := newSecretKeyring("previous", map[string][]byte{
		"current":  []byte("current-master-key"),
		"previous": []byte("previous-master-key"),
	})
	if err != nil {
		t.Fatalf("new previous keyring: %v", err)
	}
	oldCiphertext, err := previous.EncryptValue("old-password")
	if err != nil {
		t.Fatalf("encrypt with previous key: %v", err)
	}
	value, err = keyring.DecryptValue(oldCiphertext)
	if err != nil || value != "old-password" {
		t.Fatalf("decrypt previous value = %v, %v", value, err)
	}
}

func TestSecretKeyringRejectsUnknownKeyAndMalformedCiphertext(t *testing.T) {
	keyring, err := newSecretKeyring("current", map[string][]byte{"current": []byte("current-master-key")})
	if err != nil {
		t.Fatalf("new keyring: %v", err)
	}
	if _, err := keyring.DecryptValue(secretKeyringPrefix + "missing:invalid"); err == nil {
		t.Fatal("expected unknown key id to fail closed")
	}
	if _, err := keyring.DecryptValue(secretKeyringPrefix + "current:not-base64!"); err == nil {
		t.Fatal("expected malformed ciphertext to fail closed")
	}
}

func TestReencryptSecretValuesRotatesLegacyCiphertext(t *testing.T) {
	legacy, err := newSecretCipher([]byte("old-material"))
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := newSecretKeyring("current", map[string][]byte{
		"current":  []byte("new-material"),
		"previous": []byte("old-material"),
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := legacy.EncryptValue("rcon-secret")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]any{"rcon": sealed, "memory_mb": int64(1024)}
	definitions := []domain.StartupVariable{{Key: "rcon", Secret: true}, {Key: "memory_mb", Secret: false}}
	if err := reencryptSecretValues(rotated, definitions, values); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(values["rcon"].(string), secretKeyringPrefix+"current:") {
		t.Fatalf("rotated value = %q, want active key id", values["rcon"])
	}
	decoded, err := rotated.DecryptValue(values["rcon"].(string))
	if err != nil || decoded != "rcon-secret" {
		t.Fatalf("rotated value decrypt = %#v, %v", decoded, err)
	}
	if values["memory_mb"] != int64(1024) {
		t.Fatalf("non-secret value changed: %#v", values["memory_mb"])
	}
}

func TestReencryptSecretValuesFailsClosedOnPlaintextSecret(t *testing.T) {
	keyring, err := newSecretKeyring("current", map[string][]byte{"current": []byte("material")})
	if err != nil {
		t.Fatal(err)
	}
	err = reencryptSecretValues(keyring, []domain.StartupVariable{{Key: "password", Secret: true}}, map[string]any{"password": "plaintext"})
	if err == nil {
		t.Fatal("expected plaintext secret to fail closed")
	}
}
