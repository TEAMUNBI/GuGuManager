package store

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gugumanager/gugumanager/internal/domain"
)

// ReencryptStartupSecrets upgrades all persisted startup Secret values to the
// active key in the configured keyring. The operation is idempotent and keeps
// non-secret values untouched. It is intended for an operator-triggered
// rotation job or a bounded startup migration window.
func (s *Postgres) ReencryptStartupSecrets(ctx context.Context) (int, error) {
	s.mu.RLock()
	keyring := s.secretKeyring
	legacy := s.secretCipher
	s.mu.RUnlock()
	if keyring == nil {
		if legacy == nil {
			return 0, errors.New("secret keyring is not configured")
		}
		return 0, errors.New("secret keyring is required for rotation")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin secret rotation: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT sv.server_id::text, gb.game_definition_id, gb.digest, sv.values
		FROM startup_values sv
		JOIN servers s ON s.id = sv.server_id AND s.deleted_at IS NULL
		JOIN game_bundles gb ON gb.id = s.game_bundle_id
		FOR UPDATE OF sv`)
	if err != nil {
		return 0, fmt.Errorf("list startup values for rotation: %w", err)
	}
	type startupDocument struct {
		serverID string
		gameID   string
		digest   string
		raw      []byte
	}
	var documents []startupDocument
	for rows.Next() {
		var document startupDocument
		if err := rows.Scan(&document.serverID, &document.gameID, &document.digest, &document.raw); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan startup values for rotation: %w", err)
		}
		documents = append(documents, document)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate startup values for rotation: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close startup values rotation query: %w", err)
	}
	processed := 0
	for _, document := range documents {
		revision, err := s.trustedCatalogRevision(ctx, tx, document.gameID, document.digest)
		if err != nil {
			return processed, fmt.Errorf("resolve game %s for server %s: %w", document.gameID, document.serverID, err)
		}
		game := revision.Game
		values, err := decodeSecretJSON(document.raw)
		if err != nil {
			return processed, fmt.Errorf("decode startup values for server %s: %w", document.serverID, err)
		}
		server := domain.Server{ID: document.serverID, GameID: document.gameID, GameBundleDigest: document.digest, GameDefinitionVersion: game.Version}
		startup, _, err := startupFromFixedBundle(server, game, nil)
		if err != nil {
			return processed, fmt.Errorf("resolve startup schema for server %s: %w", document.serverID, err)
		}
		if err := reencryptSecretValues(keyring, startup.Variables, values); err != nil {
			return processed, fmt.Errorf("reencrypt startup values for server %s: %w", document.serverID, err)
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			return processed, fmt.Errorf("encode startup values for server %s: %w", document.serverID, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE startup_values SET values = $2::jsonb, updated_at = now() WHERE server_id = $1`, document.serverID, string(encoded)); err != nil {
			return processed, fmt.Errorf("write startup values for server %s: %w", document.serverID, err)
		}
		processed++
	}
	if err := tx.Commit(); err != nil {
		return processed, fmt.Errorf("commit secret rotation: %w", err)
	}
	return processed, nil
}

func decodeSecretJSON(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var values map[string]any
	if err := decoder.Decode(&values); err != nil {
		return nil, err
	}
	return normalizeJSONNumbers(values).(map[string]any), nil
}

func reencryptSecretValues(keyring *secretKeyring, definitions []domain.StartupVariable, values map[string]any) error {
	for _, definition := range definitions {
		if !definition.Secret {
			continue
		}
		value, ok := values[definition.Key]
		if !ok {
			continue
		}
		text, ok := value.(string)
		if !ok || !IsSecretCiphertext(text) {
			return fmt.Errorf("secret %q is not encrypted", definition.Key)
		}
		if strings.HasPrefix(text, secretKeyringPrefix+keyring.activeID+":") {
			continue
		}
		decrypted, err := keyring.DecryptValue(text)
		if err != nil {
			return fmt.Errorf("decrypt %q: %w", definition.Key, err)
		}
		sealed, err := keyring.EncryptValue(decrypted)
		if err != nil {
			return fmt.Errorf("encrypt %q: %w", definition.Key, err)
		}
		values[definition.Key] = sealed
	}
	return nil
}

// secretCipherPrefix 标记静态加密的启动变量值（Secret）。
// 存储格式：enc:v1:<base64(nonce || AES-256-GCM ciphertext)>。
// 无此前缀的值按明文处理，兼容升级前已落库的旧数据。
const secretCipherPrefix = "enc:v1:"

// secretKeyringPrefix identifies a ciphertext that carries the key id used
// for encryption: enc:v2:<key-id>:<base64(nonce || ciphertext)>.
const secretKeyringPrefix = "enc:v2:"

// secretCipher 用主密钥对启动变量的 Secret 值做 AES-256-GCM 静态加密。
// 密钥由 GUGU_ENCRYPTION_KEY_FILE 提供（生产）；development 不设置时
// 加密禁用，行为与升级前一致（明文存储、不回显）。
type secretCipher struct {
	aead cipher.AEAD
}

// secretKeyring keeps the active key for new writes and previous keys for
// decrypting data during a rotation window. The key material is retained only
// in memory; callers are responsible for loading it from a protected source.
type secretKeyring struct {
	activeID string
	keys     map[string]*secretCipher
}

func newSecretKeyring(activeID string, keys map[string][]byte) (*secretKeyring, error) {
	activeID = strings.TrimSpace(activeID)
	if activeID == "" || len(activeID) > 64 || strings.ContainsAny(activeID, ":\r\n") {
		return nil, errors.New("secret keyring requires a valid active key id")
	}
	if len(keys) == 0 {
		return nil, errors.New("secret keyring requires at least one key")
	}
	result := &secretKeyring{activeID: activeID, keys: make(map[string]*secretCipher, len(keys))}
	for keyID, material := range keys {
		keyID = strings.TrimSpace(keyID)
		if keyID == "" || len(keyID) > 64 || strings.ContainsAny(keyID, ":\r\n") {
			return nil, errors.New("secret keyring contains an invalid key id")
		}
		sealer, err := newSecretCipher(material)
		if err != nil {
			return nil, fmt.Errorf("initialize key %q: %w", keyID, err)
		}
		result.keys[keyID] = sealer
	}
	if _, ok := result.keys[activeID]; !ok {
		return nil, fmt.Errorf("active secret key %q is not configured", activeID)
	}
	return result, nil
}

func (k *secretKeyring) EncryptValue(value any) (string, error) {
	sealer := k.keys[k.activeID]
	encoded, err := sealer.EncryptValue(value)
	if err != nil {
		return "", err
	}
	return secretKeyringPrefix + k.activeID + ":" + strings.TrimPrefix(encoded, secretCipherPrefix), nil
}

func (k *secretKeyring) DecryptValue(encoded string) (any, error) {
	if !strings.HasPrefix(encoded, secretKeyringPrefix) {
		if strings.HasPrefix(encoded, secretCipherPrefix) {
			var lastErr error
			for _, sealer := range k.keys {
				value, err := sealer.DecryptValue(encoded)
				if err == nil {
					return value, nil
				}
				lastErr = err
			}
			return nil, lastErr
		}
		return encoded, nil
	}
	rest := strings.TrimPrefix(encoded, secretKeyringPrefix)
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, errors.New("malformed secret keyring ciphertext")
	}
	sealer, ok := k.keys[parts[0]]
	if !ok {
		return nil, fmt.Errorf("secret key %q is not available", parts[0])
	}
	return sealer.DecryptValue(secretCipherPrefix + parts[1])
}

// newSecretCipher 用任意长度主密钥构造 AES-256-GCM 加密器：
// 密钥经 SHA-256 派生为 32 字节，允许密钥文件内容为任意长度。
func newSecretCipher(masterKey []byte) (*secretCipher, error) {
	if len(masterKey) == 0 {
		return nil, errors.New("secret cipher requires a non-empty master key")
	}
	derived := sha256.Sum256(masterKey)
	block, err := aes.NewCipher(derived[:])
	if err != nil {
		return nil, fmt.Errorf("create aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}
	return &secretCipher{aead: aead}, nil
}

// EncryptValue 把任意类型的值序列化后加密，返回 enc:v1: 前缀的密文。
func (c *secretCipher) EncryptValue(value any) (string, error) {
	plaintext, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal secret value: %w", err)
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, plaintext, nil)
	return secretCipherPrefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// DecryptValue 解密 enc:v1: 前缀的密文并恢复原始值（数字规范化与内存态一致）；
// 无前缀的值按明文字符串原样返回（旧数据兼容）。
func (c *secretCipher) DecryptValue(encoded string) (any, error) {
	if !bytes.HasPrefix([]byte(encoded), []byte(secretCipherPrefix)) {
		return encoded, nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(encoded[len(secretCipherPrefix):])
	if err != nil {
		return nil, fmt.Errorf("decode secret ciphertext: %w", err)
	}
	if len(raw) < c.aead.NonceSize() {
		return nil, errors.New("secret ciphertext too short")
	}
	nonce, ciphertext := raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt secret value: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode decrypted secret value: %w", err)
	}
	return normalizeJSONNumbers(value), nil
}

// IsSecretCiphertext 判断给定值是否为已加密的 Secret 密文。
func IsSecretCiphertext(value any) bool {
	text, ok := value.(string)
	return ok && (bytes.HasPrefix([]byte(text), []byte(secretCipherPrefix)) || bytes.HasPrefix([]byte(text), []byte(secretKeyringPrefix)))
}

// encryptSecretValues 把 values 中声明为 Secret 的变量值加密为 enc:v1: 密文
// （静态存储前调用）。加密器未注入（development）或值已是密文时保持不变。
func (s *Postgres) encryptSecretValues(definitions []domain.StartupVariable, values map[string]any) error {
	if s.secretCipher == nil && s.secretKeyring == nil {
		return nil
	}
	for _, definition := range definitions {
		if !definition.Secret {
			continue
		}
		value, ok := values[definition.Key]
		if !ok || IsSecretCiphertext(value) {
			continue
		}
		var sealed string
		var err error
		if s.secretKeyring != nil {
			sealed, err = s.secretKeyring.EncryptValue(value)
		} else {
			sealed, err = s.secretCipher.EncryptValue(value)
		}
		if err != nil {
			return err
		}
		values[definition.Key] = sealed
	}
	return nil
}

// startupValuesForStorage returns a shallow copy of values with Secret
// entries encrypted. The caller can keep using the original map to materialize
// the plaintext payload sent to an Agent.
func (s *Postgres) startupValuesForStorage(definitions []domain.StartupVariable, values map[string]any) (map[string]any, error) {
	stored := make(map[string]any, len(values))
	for key, value := range values {
		stored[key] = value
	}
	if err := s.encryptSecretValues(definitions, stored); err != nil {
		return nil, err
	}
	return stored, nil
}

// decryptSecretValues 把 values 中 Secret 变量值解密为明文（生成启动命令与
// 展示前调用）。无加密器或值不是密文时原样保留；解不开的密文视为数据损坏。
func (s *Postgres) decryptSecretValues(definitions []domain.StartupVariable, values map[string]any) error {
	if s.secretCipher == nil && s.secretKeyring == nil {
		for _, definition := range definitions {
			if value, ok := values[definition.Key]; ok && IsSecretCiphertext(value) {
				return errors.New("secret ciphertext requires an encryption key")
			}
		}
		return nil
	}
	for _, definition := range definitions {
		if !definition.Secret {
			continue
		}
		value, ok := values[definition.Key]
		if !ok {
			continue
		}
		text, ok := value.(string)
		if !ok {
			continue
		}
		var decrypted any
		var err error
		if s.secretKeyring != nil {
			decrypted, err = s.secretKeyring.DecryptValue(text)
		} else {
			decrypted, err = s.secretCipher.DecryptValue(text)
		}
		if err != nil {
			return err
		}
		values[definition.Key] = decrypted
	}
	return nil
}
