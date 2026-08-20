package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/gugumanager/gugumanager/internal/bundletrust"
	"github.com/gugumanager/gugumanager/internal/domain"
)

const (
	gameTrustLevelSigned        = "L2_SIGNED"
	gameSourceSignedBundle      = "signed-bundle"
	gameReasonBundleRevoked     = "BUNDLE_REVOKED"
	gameReasonBundleNotApproved = "BUNDLE_NOT_APPROVED"
)

type catalogQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type trustedCatalogRevision struct {
	Game      domain.GameDefinition
	Document  []byte
	TrustRoot []byte
}

// trustedCatalogRevision resolves either the previous embedded development
// bundle or a persisted signed v1beta1 revision. Persisted runtime_target is
// deliberately ignored: it is a cache only, while the signed document is the
// source of truth and is re-verified on every materialization boundary.
func (s *Postgres) trustedCatalogRevision(ctx context.Context, q catalogQueryRower, gameID, digest string) (trustedCatalogRevision, error) {
	if fixed, err := fixedCatalogGame(gameID); err == nil && fixed.BundleDigest == digest {
		return trustedCatalogRevision{Game: fixed}, nil
	}

	var name, definitionStatus, bundleStatus string
	var document, trustRoot sql.NullString
	var revoked bool
	err := q.QueryRowContext(ctx, `
		SELECT gd.name, gd.review_status, gb.review_status,
		       gb.revoked_at IS NOT NULL, gb.document::text, tr.public_key_pem
		FROM game_definitions gd
		JOIN game_bundles gb ON gb.game_definition_id = gd.id
		LEFT JOIN bundle_trust_roots tr
		  ON tr.key_id = gb.signature_key_id AND tr.revoked_at IS NULL
		WHERE gd.id = $1 AND gb.digest = $2
	`, gameID, digest).Scan(&name, &definitionStatus, &bundleStatus, &revoked, &document, &trustRoot)
	if err == sql.ErrNoRows {
		return trustedCatalogRevision{}, domain.NewProblem("VALIDATION_FAILED", "游戏包 revision 不存在", false)
	}
	if err != nil {
		return trustedCatalogRevision{}, domain.NewProblem("INTERNAL_ERROR", "无法读取签名游戏包", true)
	}
	game := domain.GameDefinition{ID: gameID, BundleDigest: digest, Name: name, Status: definitionStatus}
	markCatalogBundleUntrusted(&game, gameSourceDatabaseMetadata)
	if revoked || definitionStatus == "revoked" || bundleStatus == "revoked" {
		game.SupportReasons = []string{gameReasonBundleRevoked}
		return trustedCatalogRevision{}, packageRuntimeTargetUnavailable(game)
	}
	if definitionStatus != "approved" || bundleStatus != "approved" {
		game.SupportReasons = []string{gameReasonBundleNotApproved}
		return trustedCatalogRevision{}, packageRuntimeTargetUnavailable(game)
	}
	if !document.Valid || !trustRoot.Valid {
		return trustedCatalogRevision{}, packageRuntimeTargetUnavailable(game)
	}
	verified, err := bundletrust.Verify([]byte(document.String), []byte(trustRoot.String))
	if err != nil {
		problem := packageRuntimeTargetUnavailable(game)
		problem.Details["verificationError"] = err.Error()
		return trustedCatalogRevision{}, problem
	}
	if verified.Document.GameDefinitionID != gameID || verified.Document.Digest != digest {
		return trustedCatalogRevision{}, domain.NewProblem("PACKAGE_INCOMPATIBLE", "Bundle 标识与目录记录不一致", false)
	}
	source, err := bundletrust.GameDefinitionJSON(verified.Document)
	if err != nil {
		return trustedCatalogRevision{}, domain.NewProblem("PACKAGE_INCOMPATIBLE", "无法重建 Bundle 规范", false)
	}
	game.Version = verified.Document.DefinitionVersion
	game.GameVersion = verified.Document.GameVersion
	game.Capabilities = append([]string(nil), verified.Document.Capabilities...)
	for _, platform := range strings.Split(verified.Document.Compatibility["platforms"], ",") {
		if platform = strings.TrimSpace(platform); platform != "" {
			game.Platforms = append(game.Platforms, platform)
		}
	}
	game.Signed, game.Verified, game.Runnable, game.Supported = true, true, true, true
	game.TrustLevel, game.Source = gameTrustLevelSigned, gameSourceSignedBundle
	game.SupportReasons = []string{}
	game.RuntimeTarget = cloneRuntimeTarget(&verified.RuntimeTarget)
	game.BundleDocument = string(source)
	if presentation, ok := developmentGamePresentations[gameID]; ok {
		game.Summary, game.Icon = presentation.Summary, presentation.Icon
		game.DefaultMemory, game.DefaultDisk = presentation.DefaultMemory, presentation.DefaultDisk
	} else {
		game.Summary = fmt.Sprintf("Signed game bundle %s", verified.Document.DefinitionVersion)
		game.Icon, game.DefaultMemory, game.DefaultDisk = "cube", 2048, 20
	}
	return trustedCatalogRevision{Game: game, Document: []byte(document.String), TrustRoot: []byte(trustRoot.String)}, nil
}
