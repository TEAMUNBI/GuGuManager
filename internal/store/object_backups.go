package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/id"
	"github.com/gugumanager/gugumanager/internal/objectstore"
)

type remoteBackupClaim struct {
	backupID, serverID, nodeID, digest string
	size                               int64
	leaseOwner                         string
}

// ArchivePendingBackups moves completed node-local archives into the
// configured encrypted ObjectStore. The short PostgreSQL lease is recoverable
// after a replica crash; the digest advisory lock deduplicates equal content
// without holding a transaction open during network I/O.
func (s *Postgres) ArchivePendingBackups(ctx context.Context, limit int) (int, error) {
	s.mu.RLock()
	storage, keyring, dispatcher := s.objectStore, s.objectKeyring, s.fileDispatcher
	s.mu.RUnlock()
	if storage == nil || keyring == nil || dispatcher == nil {
		return 0, nil
	}
	if limit < 1 || limit > 100 {
		limit = 10
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE backups SET remote_status = 'failed', remote_lease_owner = NULL, remote_lease_until = NULL,
		       remote_last_error = COALESCE(remote_last_error, 'upload lease expired')
		WHERE remote_status = 'uploading' AND remote_lease_until < now()
	`); err != nil {
		return 0, err
	}
	processed := 0
	for processed < limit && ctx.Err() == nil {
		claim, ok, err := s.claimRemoteBackup(ctx)
		if err != nil {
			return processed, err
		}
		if !ok {
			break
		}
		if err := s.archiveRemoteBackup(ctx, storage, keyring, dispatcher, claim); err != nil {
			s.failRemoteBackup(context.Background(), claim, err)
			continue
		}
		processed++
	}
	return processed, ctx.Err()
}

func (s *Postgres) claimRemoteBackup(ctx context.Context) (remoteBackupClaim, bool, error) {
	claim := remoteBackupClaim{leaseOwner: id.New()}
	err := s.db.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT b.id FROM backups b
			JOIN servers s ON s.id = b.server_id AND s.deleted_at IS NULL
			JOIN nodes n ON n.id = s.node_id AND n.revoked_at IS NULL
			WHERE b.status = 'ready' AND b.remote_status IN ('pending', 'failed')
			  AND b.content_digest IS NOT NULL AND b.size_bytes IS NOT NULL
			ORDER BY b.created_at
			FOR UPDATE OF b SKIP LOCKED LIMIT 1
		)
		UPDATE backups b
		SET remote_status = 'uploading', remote_attempt = remote_attempt + 1,
		    remote_lease_owner = $1, remote_lease_until = now() + interval '10 minutes', remote_last_error = NULL
		FROM candidate c, servers s
		WHERE b.id = c.id AND s.id = b.server_id
		RETURNING b.id::text, b.server_id::text, s.node_id::text, b.content_digest, b.size_bytes
	`, claim.leaseOwner).Scan(&claim.backupID, &claim.serverID, &claim.nodeID, &claim.digest, &claim.size)
	if err == sql.ErrNoRows {
		return remoteBackupClaim{}, false, nil
	}
	return claim, err == nil, err
}

func (s *Postgres) archiveRemoteBackup(ctx context.Context, storage objectstore.Store, keyring *objectstore.Keyring, dispatcher FileDispatcher, claim remoteBackupClaim) error {
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, claim.digest); err != nil {
		return err
	}
	defer connection.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, claim.digest)

	var manifestID string
	if err := connection.QueryRowContext(ctx, `
		SELECT id::text FROM object_manifests WHERE plaintext_digest = $1 AND status = 'active'
	`, claim.digest).Scan(&manifestID); err == nil {
		return s.attachObjectManifest(ctx, claim, manifestID)
	} else if err != sql.ErrNoRows {
		return err
	}

	content, err := dispatcher.DownloadBackup(ctx, claim.nodeID, claim.serverID, claim.backupID)
	if err != nil {
		return fmt.Errorf("download node backup: %w", err)
	}
	payload := content.Content
	if content.Base64 {
		payload, err = base64.StdEncoding.DecodeString(string(payload))
		if err != nil {
			return errors.New("node backup is not valid base64")
		}
	}
	actual := sha256.Sum256(payload)
	if int64(len(payload)) != claim.size || "sha256:"+hex.EncodeToString(actual[:]) != claim.digest {
		return errors.New("node backup does not match persisted digest and size")
	}
	objectKey := "backups/sha256/" + strings.TrimPrefix(claim.digest, "sha256:") + ".enc"
	manifest, err := objectstore.EncryptUpload(ctx, storage, objectKey, bytes.NewReader(payload), claim.size, keyring)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	manifestID = id.New()
	_, err = connection.ExecContext(ctx, `
		INSERT INTO object_manifests (
			id, object_key, format_version, plaintext_digest, ciphertext_digest,
			plaintext_size, ciphertext_size, key_id, wrapped_data_key, manifest, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, 'active')
	`, manifestID, manifest.ObjectKey, manifest.Version, manifest.PlaintextDigest, manifest.CiphertextDigest,
		manifest.PlaintextSize, manifest.CiphertextSize, manifest.KeyID, manifest.WrappedDataKey, string(encoded))
	if err != nil {
		return err
	}
	return s.attachObjectManifest(ctx, claim, manifestID)
}

func (s *Postgres) attachObjectManifest(ctx context.Context, claim remoteBackupClaim, manifestID string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE backups SET object_manifest_id = $1, remote_status = 'ready',
		       remote_lease_owner = NULL, remote_lease_until = NULL, remote_last_error = NULL
		WHERE id = $2 AND remote_status = 'uploading' AND remote_lease_owner = $3
	`, manifestID, claim.backupID, claim.leaseOwner)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return errors.New("backup upload lease was superseded")
	}
	return nil
}

func (s *Postgres) failRemoteBackup(ctx context.Context, claim remoteBackupClaim, cause error) {
	message := cause.Error()
	if len(message) > 1000 {
		message = message[:1000]
	}
	_, _ = s.db.ExecContext(ctx, `
		UPDATE backups SET remote_status = 'failed', remote_lease_owner = NULL,
		       remote_lease_until = NULL, remote_last_error = $1
		WHERE id = $2 AND remote_status = 'uploading' AND remote_lease_owner = $3
	`, message, claim.backupID, claim.leaseOwner)
}

func (s *Postgres) remoteBackupContent(ctx context.Context, backupID, expectedDigest string, expectedSize int64) (domain.BackupContent, bool, error) {
	s.mu.RLock()
	storage, keyring := s.objectStore, s.objectKeyring
	s.mu.RUnlock()
	if storage == nil || keyring == nil {
		return domain.BackupContent{}, false, nil
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT om.manifest FROM backups b
		JOIN object_manifests om ON om.id = b.object_manifest_id AND om.status = 'active'
		WHERE b.id = $1 AND b.remote_status = 'ready'
	`, backupID).Scan(&raw)
	if err == sql.ErrNoRows {
		return domain.BackupContent{}, false, nil
	}
	if err != nil {
		return domain.BackupContent{}, false, err
	}
	var manifest objectstore.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return domain.BackupContent{}, false, err
	}
	var destination bytes.Buffer
	if err := objectstore.DecryptDownload(ctx, storage, manifest, &destination, keyring); err != nil {
		return domain.BackupContent{}, false, err
	}
	payload := destination.Bytes()
	if manifest.PlaintextDigest != expectedDigest || manifest.PlaintextSize != expectedSize || int64(len(payload)) != expectedSize {
		return domain.BackupContent{}, false, errors.New("remote backup manifest does not match backup metadata")
	}
	return domain.BackupContent{Filename: backupID + ".tar.gz", Content: payload, SizeBytes: int64(len(payload))}, true, nil
}

// ReconcileObjectTombstones deletes remote objects only after every backup
// reference is gone. Tombstones make deletion recoverable and idempotent.
func (s *Postgres) ReconcileObjectTombstones(ctx context.Context, limit int) (int, error) {
	s.mu.RLock()
	storage := s.objectStore
	s.mu.RUnlock()
	if storage == nil {
		return 0, nil
	}
	if limit < 1 || limit > 100 {
		limit = 10
	}
	processed := 0
	for processed < limit {
		lease := id.New()
		var tombstoneID, manifestID, objectKey string
		err := s.db.QueryRowContext(ctx, `
			WITH candidate AS (
				SELECT t.id FROM object_tombstones t
				WHERE t.status IN ('pending', 'failed') OR (t.status = 'leased' AND t.lease_until < now())
				ORDER BY t.created_at FOR UPDATE SKIP LOCKED LIMIT 1
			)
			UPDATE object_tombstones t SET status = 'leased', attempt = attempt + 1,
			       lease_owner = $1, lease_until = now() + interval '5 minutes', updated_at = now()
			FROM candidate c, object_manifests om
			WHERE t.id = c.id AND om.id = t.object_manifest_id
			RETURNING t.id::text, om.id::text, om.object_key
		`, lease).Scan(&tombstoneID, &manifestID, &objectKey)
		if err == sql.ErrNoRows {
			break
		}
		if err != nil {
			return processed, err
		}
		var references int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM backups WHERE object_manifest_id = $1 AND status <> 'deleted'`, manifestID).Scan(&references); err != nil {
			return processed, err
		}
		if references > 0 {
			_, _ = s.db.ExecContext(ctx, `DELETE FROM object_tombstones WHERE id = $1 AND lease_owner = $2`, tombstoneID, lease)
			continue
		}
		if err := storage.Delete(ctx, objectKey); err != nil {
			_, _ = s.db.ExecContext(context.Background(), `UPDATE object_tombstones SET status = 'failed', last_error = $3, lease_owner = NULL, lease_until = NULL, updated_at = now() WHERE id = $1 AND lease_owner = $2`, tombstoneID, lease, err.Error())
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return processed, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE object_manifests SET status = 'deleted', deleted_at = now(), updated_at = now() WHERE id = $1`, manifestID); err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE object_tombstones SET status = 'completed', completed_at = now(), lease_owner = NULL, lease_until = NULL, updated_at = now() WHERE id = $1 AND lease_owner = $2`, tombstoneID, lease)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE backups SET remote_status = 'deleted' WHERE object_manifest_id = $1 AND status = 'deleted'`, manifestID)
		}
		if err != nil {
			tx.Rollback()
			return processed, err
		}
		if err := tx.Commit(); err != nil {
			return processed, err
		}
		processed++
	}
	return processed, ctx.Err()
}
