package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
)

const (
	secretHandlePrefix = "sh:v1:"
	secretHandleTTL    = 2 * time.Minute
)

// SecretHandleStore is intentionally separate from TaskStore so development
// fakes do not need to implement a production-only Secret resolution path.
type SecretHandleStore interface {
	ResolveSecretHandle(ctx context.Context, operationID, serverID, nodeID, handle string) (string, time.Time, error)
}

func secretHandleValue(token []byte) string {
	return secretHandlePrefix + base64.RawURLEncoding.EncodeToString(token)
}

func parseSecretHandle(handle string) ([]byte, error) {
	if !strings.HasPrefix(handle, secretHandlePrefix) {
		return nil, errors.New("invalid secret handle prefix")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(handle, secretHandlePrefix))
	if err != nil || len(raw) != 32 {
		return nil, errors.New("invalid secret handle encoding")
	}
	return raw, nil
}

func secretHandleSnapshot(values map[string]any, key string) (string, bool, error) {
	value, ok := values[key]
	if !ok {
		return "", false, nil
	}
	ciphertext, ok := value.(string)
	if !ok || !IsSecretCiphertext(ciphertext) {
		return "", false, errors.New("secret handle snapshot requires encrypted storage")
	}
	return ciphertext, true, nil
}

func (s *Postgres) materializeSecretHandlesTx(ctx context.Context, tx *sql.Tx, task *ClaimedTask) error {
	if task == nil || task.TaskType != "provision" || len(task.PayloadJSON) == 0 {
		return nil
	}
	var payload provisionTaskPayload
	if err := json.Unmarshal(task.PayloadJSON, &payload); err != nil {
		return fmt.Errorf("decode provision checkpoint: %w", err)
	}
	if len(payload.SecretKeys) == 0 {
		return nil
	}
	values, err := s.startupValuesTx(ctx, tx, task.ServerID)
	if err != nil {
		return err
	}
	for _, key := range payload.SecretKeys {
		ciphertext, ok, err := secretHandleSnapshot(values, key)
		if err != nil {
			return fmt.Errorf("snapshot startup secret %q: %w", key, err)
		}
		if !ok {
			continue
		}
		var token [32]byte
		if _, err := rand.Read(token[:]); err != nil {
			return fmt.Errorf("generate secret handle: %w", err)
		}
		digest := sha256.Sum256(token[:])
		expiresAt := time.Now().UTC().Add(secretHandleTTL)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO secret_handles (token_digest, operation_id, server_id, node_id, variable_key, encrypted_value, attempt, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, digest[:], task.OperationID, task.ServerID, task.NodeID, key, ciphertext, task.Attempt, expiresAt); err != nil {
			return fmt.Errorf("persist secret handle: %w", err)
		}
		// The raw value is intentionally replaced with an opaque reference in
		// the leased payload; only the resolver can exchange it for plaintext.
		if payload.Variables == nil {
			payload.Variables = make(map[string]string)
		}
		payload.Variables[key] = secretHandleValue(token[:])
	}
	// SecretKeys is a storage-only field and must never be sent to protojson.
	payload.SecretKeys = nil
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode leased provision payload: %w", err)
	}
	task.PayloadJSON = encoded
	return nil
}

func (s *Postgres) ResolveSecretHandle(ctx context.Context, operationID, serverID, nodeID, handle string) (string, time.Time, error) {
	raw, err := parseSecretHandle(handle)
	if err != nil {
		return "", time.Time{}, err
	}
	digest := sha256.Sum256(raw)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("begin secret handle resolution: %w", err)
	}
	defer tx.Rollback()
	var variableKey string
	var encryptedValue string
	var expiresAt time.Time
	var attempt int
	err = tx.QueryRowContext(ctx, `
		SELECT variable_key, encrypted_value, expires_at, attempt
		FROM secret_handles
		WHERE token_digest = $1 AND operation_id = $2 AND server_id = $3 AND node_id = $4
		  AND consumed_at IS NULL AND expires_at > now()
		FOR UPDATE
	`, digest[:], operationID, serverID, nodeID).Scan(&variableKey, &encryptedValue, &expiresAt, &attempt)
	if err == sql.ErrNoRows {
		return "", time.Time{}, errors.New("secret handle is expired, consumed, or not bound to this operation")
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("lookup secret handle: %w", err)
	}
	var taskStatus string
	var currentAttempt int
	if err := tx.QueryRowContext(ctx, `SELECT status, attempt FROM server_tasks WHERE id = $1 AND server_id = $2 AND node_id = $3`, operationID, serverID, nodeID).Scan(&taskStatus, &currentAttempt); err != nil {
		return "", time.Time{}, fmt.Errorf("verify secret handle task: %w", err)
	}
	if currentAttempt != attempt || (taskStatus != "leased" && taskStatus != "dispatched" && taskStatus != "running") {
		return "", time.Time{}, errors.New("secret handle is no longer valid for this task attempt")
	}
	values := map[string]any{variableKey: encryptedValue}
	if err := s.decryptSecretValues([]domain.StartupVariable{{Key: variableKey, Secret: true}}, values); err != nil {
		return "", time.Time{}, errors.New("secret value cannot be decrypted")
	}
	value, ok := values[variableKey]
	if !ok {
		return "", time.Time{}, errors.New("secret value is not configured")
	}
	stringValue := stringifiedStartupValues(map[string]any{variableKey: value})[variableKey]
	if _, err := tx.ExecContext(ctx, `UPDATE secret_handles SET consumed_at = now() WHERE token_digest = $1`, digest[:]); err != nil {
		return "", time.Time{}, fmt.Errorf("consume secret handle: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", time.Time{}, fmt.Errorf("commit secret handle resolution: %w", err)
	}
	return stringValue, expiresAt, nil
}
