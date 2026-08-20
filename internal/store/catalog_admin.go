package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/gugumanager/gugumanager/internal/bundletrust"
	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/id"
	gamedefinition "github.com/gugumanager/gugumanager/spec/game-definition"
)

func (s *Postgres) BundleSources() ([]domain.BundleSource, error) {
	rows, err := s.db.Query(`SELECT id::text,name,index_url,official,enabled,created_at,updated_at FROM bundle_sources ORDER BY official DESC,created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.BundleSource{}
	for rows.Next() {
		var item domain.BundleSource
		if err := rows.Scan(&item.ID, &item.Name, &item.IndexURL, &item.Official, &item.Enabled, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Postgres) CreateBundleSource(input domain.BundleSourceInput, actor domain.User) (domain.BundleSource, error) {
	if !hasRole(actor, "platform_admin") {
		return domain.BundleSource{}, domain.NewProblem("FORBIDDEN", "需要平台管理员权限", false)
	}
	input.Name, input.IndexURL = strings.TrimSpace(input.Name), strings.TrimSpace(input.IndexURL)
	parsed, err := url.Parse(input.IndexURL)
	if len([]rune(input.Name)) < 1 || len([]rune(input.Name)) > 128 || err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return domain.BundleSource{}, domain.NewProblem("VALIDATION_FAILED", "Bundle source 名称或 HTTPS index URL 无效", false)
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	now := time.Now().UTC()
	item := domain.BundleSource{ID: id.New(), Name: input.Name, IndexURL: input.IndexURL, Official: input.Official, Enabled: enabled, CreatedAt: now, UpdatedAt: now}
	if _, err := s.db.Exec(`INSERT INTO bundle_sources(id,name,index_url,official,enabled,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$6)`, item.ID, item.Name, item.IndexURL, item.Official, item.Enabled, now); err != nil {
		return domain.BundleSource{}, domain.NewProblem("OPERATION_CONFLICT", "Bundle source 已存在", false)
	}
	return item, nil
}

func (s *Postgres) BundleTrustRoots() ([]domain.BundleTrustRoot, error) {
	rows, err := s.db.Query(`SELECT id::text,key_id,name,source,created_at,revoked_at FROM bundle_trust_roots ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.BundleTrustRoot{}
	for rows.Next() {
		var item domain.BundleTrustRoot
		var revoked sql.NullTime
		if err := rows.Scan(&item.ID, &item.KeyID, &item.Name, &item.Source, &item.CreatedAt, &revoked); err != nil {
			return nil, err
		}
		if revoked.Valid {
			item.RevokedAt = &revoked.Time
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Postgres) CreateBundleTrustRoot(input domain.BundleTrustRootInput, actor domain.User) (domain.BundleTrustRoot, error) {
	if !hasRole(actor, "platform_admin") {
		return domain.BundleTrustRoot{}, domain.NewProblem("FORBIDDEN", "需要平台管理员权限", false)
	}
	input.Name, input.Source = strings.TrimSpace(input.Name), strings.TrimSpace(input.Source)
	if input.Source == "" {
		input.Source = "private"
	}
	if !map[string]bool{"official": true, "private": true, "operator": true}[input.Source] || len([]rune(input.Name)) < 1 || len([]rune(input.Name)) > 128 {
		return domain.BundleTrustRoot{}, domain.NewProblem("VALIDATION_FAILED", "信任根元数据无效", false)
	}
	keyID, err := bundletrust.TrustRootKeyID([]byte(input.PublicKeyPEM))
	if err != nil {
		return domain.BundleTrustRoot{}, domain.NewProblem("VALIDATION_FAILED", "信任根必须是单个 Ed25519 PKIX 公钥", false)
	}
	now := time.Now().UTC()
	item := domain.BundleTrustRoot{ID: id.New(), KeyID: keyID, Name: input.Name, Source: input.Source, CreatedAt: now}
	if _, err := s.db.Exec(`INSERT INTO bundle_trust_roots(id,key_id,name,public_key_pem,source,created_at) VALUES($1,$2,$3,$4,$5,$6)`, item.ID, item.KeyID, item.Name, input.PublicKeyPEM, item.Source, now); err != nil {
		return domain.BundleTrustRoot{}, domain.NewProblem("OPERATION_CONFLICT", "该信任根已存在", false)
	}
	return item, nil
}

func (s *Postgres) RevokeBundleTrustRoot(keyID string, actor domain.User) error {
	if !hasRole(actor, "platform_admin") {
		return domain.NewProblem("FORBIDDEN", "需要平台管理员权限", false)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE bundle_trust_roots SET revoked_at=COALESCE(revoked_at,now()) WHERE key_id=$1`, keyID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return domain.NewProblem("NOT_FOUND", "信任根不存在", false)
	}
	if _, err := tx.Exec(`UPDATE game_bundles SET review_status='revoked',revoked_at=COALESCE(revoked_at,now()),updated_at=now() WHERE signature_key_id=$1 AND revoked_at IS NULL`, keyID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Postgres) BundleRevisions() ([]domain.BundleRevision, error) {
	rows, err := s.db.Query(`
		SELECT id::text,game_definition_id,definition_version,game_version,digest,schema_version,license,
		       COALESCE(signature_key_id,''),signature_verified_at IS NOT NULL,review_status,published_at,revoked_at
		FROM game_bundles ORDER BY published_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.BundleRevision{}
	for rows.Next() {
		var item domain.BundleRevision
		var revoked sql.NullTime
		if err := rows.Scan(&item.ID, &item.GameDefinitionID, &item.DefinitionVersion, &item.GameVersion, &item.Digest,
			&item.SchemaVersion, &item.License, &item.SignatureKeyID, &item.SignatureVerified, &item.ReviewStatus, &item.PublishedAt, &revoked); err != nil {
			return nil, err
		}
		if revoked.Valid {
			item.RevokedAt = &revoked.Time
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Postgres) ImportBundle(input domain.BundleImportInput, actor domain.User) (domain.BundleRevision, error) {
	if !hasRole(actor, "platform_admin") {
		return domain.BundleRevision{}, domain.NewProblem("FORBIDDEN", "需要平台管理员权限", false)
	}
	var document bundletrust.Document
	decoder := json.NewDecoder(strings.NewReader(string(input.Document)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || document.Signature == nil {
		return domain.BundleRevision{}, domain.NewProblem("VALIDATION_FAILED", "签名 Bundle 文档无效", false)
	}
	var trustRoot string
	if err := s.db.QueryRow(`SELECT public_key_pem FROM bundle_trust_roots WHERE key_id=$1 AND revoked_at IS NULL`, document.Signature.KeyID).Scan(&trustRoot); err != nil {
		return domain.BundleRevision{}, domain.NewProblem("PACKAGE_INCOMPATIBLE", "Bundle 签名密钥不受信任", false)
	}
	verified, err := bundletrust.Verify(input.Document, []byte(trustRoot))
	if err != nil {
		return domain.BundleRevision{}, domain.NewProblem("PACKAGE_INCOMPATIBLE", "Bundle 签名或规范摘要校验失败", false)
	}
	runtimeTarget, _ := json.Marshal(verified.RuntimeTarget)
	var revision gamedefinition.BundleRevisionMetadata
	if err := json.Unmarshal(verified.Document.Revision, &revision); err != nil {
		return domain.BundleRevision{}, domain.NewProblem("PACKAGE_INCOMPATIBLE", "Bundle revision 元数据无效", false)
	}
	manifest, _ := json.Marshal(revision.Artifacts)
	extensions, _ := json.Marshal(revision.Extensions)
	compatibility, _ := json.Marshal(verified.Document.Compatibility)
	signature, _ := json.Marshal(verified.Document.Signature)
	tx, err := s.db.Begin()
	if err != nil {
		return domain.BundleRevision{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		INSERT INTO game_definitions(id,name,source_url,review_status)
		VALUES($1,$1,'private://rest-import','pending')
		ON CONFLICT(id) DO UPDATE SET updated_at=now()
	`, document.GameDefinitionID); err != nil {
		return domain.BundleRevision{}, err
	}
	bundleID := id.New()
	err = tx.QueryRow(`
		INSERT INTO game_bundles(id,game_definition_id,definition_version,game_version,digest,schema_version,license,
		 compatibility,published_at,document,runtime_target,artifact_manifest,extensions,signature,signature_key_id,
		 signature_verified_at,review_status,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,now(),$9::jsonb,$10::jsonb,$11::jsonb,$12::jsonb,$13::jsonb,$14,now(),'pending',now())
		ON CONFLICT(digest) DO UPDATE SET updated_at=game_bundles.updated_at
		RETURNING id::text
	`, bundleID, document.GameDefinitionID, document.DefinitionVersion, document.GameVersion, document.Digest,
		document.SchemaVersion, document.License, string(compatibility), string(input.Document), string(runtimeTarget), string(manifest), string(extensions), string(signature), verified.KeyID).Scan(&bundleID)
	if err != nil {
		return domain.BundleRevision{}, domain.NewProblem("OPERATION_CONFLICT", "Bundle revision 与既有不可变版本冲突", false)
	}
	if _, err := tx.Exec(`INSERT INTO bundle_reviews(bundle_id,reviewer_id,decision,reason) VALUES($1,$2,'submitted','REST import verified')`, bundleID, actor.ID); err != nil {
		return domain.BundleRevision{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.BundleRevision{}, err
	}
	return s.bundleRevisionByID(bundleID)
}

func (s *Postgres) ReviewBundle(bundleID string, input domain.BundleReviewInput, actor domain.User) (domain.BundleReview, error) {
	if !hasRole(actor, "platform_admin") {
		return domain.BundleReview{}, domain.NewProblem("FORBIDDEN", "需要平台管理员权限", false)
	}
	input.Decision, input.Reason = strings.TrimSpace(input.Decision), strings.TrimSpace(input.Reason)
	if !map[string]bool{"approved": true, "rejected": true, "revoked": true}[input.Decision] || len([]rune(input.Reason)) > 2000 {
		return domain.BundleReview{}, domain.NewProblem("VALIDATION_FAILED", "审核结论或说明无效", false)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return domain.BundleReview{}, err
	}
	defer tx.Rollback()
	var gameID string
	var document []byte
	var trustRoot sql.NullString
	if err := tx.QueryRow(`
		SELECT gb.game_definition_id,gb.document,tr.public_key_pem FROM game_bundles gb
		LEFT JOIN bundle_trust_roots tr ON tr.key_id=gb.signature_key_id AND tr.revoked_at IS NULL
		WHERE gb.id=$1 FOR UPDATE OF gb
	`, bundleID).Scan(&gameID, &document, &trustRoot); err == sql.ErrNoRows {
		return domain.BundleReview{}, domain.NewProblem("NOT_FOUND", "Bundle revision 不存在", false)
	} else if err != nil {
		return domain.BundleReview{}, err
	}
	if input.Decision == "approved" {
		if !trustRoot.Valid {
			return domain.BundleReview{}, domain.NewProblem("PACKAGE_INCOMPATIBLE", "Bundle 信任根已撤销", false)
		}
		if _, err := bundletrust.Verify(document, []byte(trustRoot.String)); err != nil {
			return domain.BundleReview{}, domain.NewProblem("PACKAGE_INCOMPATIBLE", "Bundle 复核失败", false)
		}
	}
	review := domain.BundleReview{ID: id.New(), BundleID: bundleID, ReviewerID: actor.ID, Decision: input.Decision, Reason: input.Reason, CreatedAt: time.Now().UTC()}
	if _, err := tx.Exec(`INSERT INTO bundle_reviews(id,bundle_id,reviewer_id,decision,reason,created_at) VALUES($1,$2,$3,$4,$5,$6)`, review.ID, bundleID, actor.ID, input.Decision, input.Reason, review.CreatedAt); err != nil {
		return domain.BundleReview{}, err
	}
	if _, err := tx.Exec(`UPDATE game_bundles SET review_status=$2,revoked_at=CASE WHEN $2='revoked' THEN COALESCE(revoked_at,now()) ELSE NULL END,updated_at=now() WHERE id=$1`, bundleID, input.Decision); err != nil {
		return domain.BundleReview{}, err
	}
	if input.Decision == "approved" {
		_, err = tx.Exec(`UPDATE game_definitions SET review_status='approved',updated_at=now() WHERE id=$1`, gameID)
	}
	if err != nil {
		return domain.BundleReview{}, err
	}
	return review, tx.Commit()
}

func (s *Postgres) bundleRevisionByID(bundleID string) (domain.BundleRevision, error) {
	var item domain.BundleRevision
	var revoked sql.NullTime
	err := s.db.QueryRow(`SELECT id::text,game_definition_id,definition_version,game_version,digest,schema_version,license,COALESCE(signature_key_id,''),signature_verified_at IS NOT NULL,review_status,published_at,revoked_at FROM game_bundles WHERE id=$1`, bundleID).
		Scan(&item.ID, &item.GameDefinitionID, &item.DefinitionVersion, &item.GameVersion, &item.Digest, &item.SchemaVersion, &item.License, &item.SignatureKeyID, &item.SignatureVerified, &item.ReviewStatus, &item.PublishedAt, &revoked)
	if revoked.Valid {
		item.RevokedAt = &revoked.Time
	}
	return item, err
}

func (s *Postgres) UpgradeServer(serverID, targetDigest string, expectedGeneration int64, idempotencyKey string, actor domain.User) (domain.Operation, error) {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return domain.Operation{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	currentActor, err := s.authorizeServer(ctx, actor.ID, serverID, "servers.startup.write")
	if err != nil {
		return domain.Operation{}, err
	}
	digest := requestDigest(struct {
		Target     string `json:"target"`
		Generation int64  `json:"generation"`
	}{targetDigest, expectedGeneration})
	scope := taskIdempotencyScope("reconcile", actor.ID, idempotencyKey)
	if existing, ok, err := s.lookupIdempotentTask(ctx, scope, idempotencyKey, digest); err != nil || ok {
		return existing, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Operation{}, err
	}
	defer tx.Rollback()
	row, err := s.lockServerRow(ctx, tx, serverID)
	if err != nil {
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	if err := generationFence(row, expectedGeneration); err != nil {
		return domain.Operation{}, err
	}
	if err := s.requireServerReconcileCapabilityTx(ctx, tx, row.NodeID); err != nil {
		return domain.Operation{}, err
	}
	if active, ok, err := s.activeTaskInTx(ctx, tx, serverID); err != nil {
		return domain.Operation{}, err
	} else if ok {
		return domain.Operation{}, operationInProgress(active)
	}
	var fromBundleID, toBundleID, targetGameID, targetVersion, targetGameVersion string
	if err := tx.QueryRowContext(ctx, `SELECT game_bundle_id::text FROM servers WHERE id=$1`, serverID).Scan(&fromBundleID); err != nil {
		return domain.Operation{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT id::text,game_definition_id,definition_version,game_version FROM game_bundles WHERE digest=$1`, targetDigest).
		Scan(&toBundleID, &targetGameID, &targetVersion, &targetGameVersion); err != nil {
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "目标 Bundle revision 不存在", false)
	}
	if targetGameID != row.GameID || fromBundleID == toBundleID {
		return domain.Operation{}, domain.NewProblem("OPERATION_CONFLICT", "目标 revision 必须属于同一游戏且不同于当前版本", false)
	}
	if _, err := s.trustedCatalogRevision(ctx, tx, row.GameID, targetDigest); err != nil {
		return domain.Operation{}, err
	}
	nextGeneration := row.Generation + 1
	if _, err := tx.ExecContext(ctx, `UPDATE servers SET game_bundle_id=$2,game_version=$3,generation=$4,updated_at=now() WHERE id=$1`, serverID, toBundleID, targetGameVersion, nextGeneration); err != nil {
		return domain.Operation{}, err
	}
	row.GameBundleDigest, row.GameDefinitionVersion = targetDigest, targetVersion
	taskInput, err := s.materializeDesiredRuntimeTx(ctx, tx, row, nextGeneration)
	if err != nil {
		return domain.Operation{}, err
	}
	operationID := id.New()
	inserted, err := s.enqueueTaskTx(ctx, tx, operationID, serverID, row.NodeID, "reconcile", nextGeneration, actor.ID, idempotencyKey, digest[:], taskInput)
	if err != nil || inserted == "" {
		return domain.Operation{}, domain.NewProblem("INTERNAL_ERROR", "无法创建升级任务", true)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO server_bundle_history(server_id,from_bundle_id,to_bundle_id,operation_id,generation,status,created_by) VALUES($1,$2,$3,$4,$5,'reconciling',$6)`, serverID, fromBundleID, toBundleID, operationID, nextGeneration, actor.ID); err != nil {
		return domain.Operation{}, err
	}
	if err := s.recordAuditTx(ctx, tx, currentActor, "server.bundle.upgrade", "server", serverID, "accepted", operationID); err != nil {
		return domain.Operation{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Operation{}, err
	}
	return domain.NewQueuedOperation(operationID, serverID, row.NodeID, domain.PowerAction("reconcile"), nextGeneration, idempotencyKey, time.Now().UTC()), nil
}

func (s *Postgres) RollbackServer(serverID string, expectedGeneration int64, idempotencyKey string, actor domain.User) (domain.Operation, error) {
	var digest string
	err := s.db.QueryRow(`
		SELECT gb.digest FROM server_bundle_history h JOIN game_bundles gb ON gb.id=h.from_bundle_id
		WHERE h.server_id=$1 AND h.status IN ('applied','rolled-back') ORDER BY h.created_at DESC LIMIT 1
	`, serverID).Scan(&digest)
	if err == sql.ErrNoRows {
		return domain.Operation{}, domain.NewProblem("NOT_FOUND", "没有可回滚的 Bundle revision", false)
	}
	if err != nil {
		return domain.Operation{}, err
	}
	return s.UpgradeServer(serverID, digest, expectedGeneration, idempotencyKey, actor)
}

var _ = errors.Is
