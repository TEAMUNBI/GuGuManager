package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/gugumanager/gugumanager/internal/domain"
)

// desiredRuntimeSpecStorage is the immutable input persisted with a reconcile
// task. SecretKeys is control-plane-only metadata removed when the task is
// leased; the Agent only receives one-time secret handles in Variables.
type desiredRuntimeSpecStorage struct {
	GameDefinitionID string                    `json:"gameDefinitionId"`
	BundleDigest     string                    `json:"bundleDigest"`
	RuntimeTarget    string                    `json:"runtimeTargetJson"`
	ResourceLimits   provisionResourceLimits   `json:"resourceLimits"`
	Allocations      []provisionTaskAllocation `json:"allocations"`
	Variables        map[string]string         `json:"variables,omitempty"`
	SecretKeys       []string                  `json:"secretKeys,omitempty"`
	DesiredRunning   bool                      `json:"desiredRunning"`
	Digest           string                    `json:"digest"`
	Generation       uint64                    `json:"generation"`
	BundleRevision   []byte                    `json:"bundleRevisionJson,omitempty"`
	TrustRootPEM     []byte                    `json:"trustRootPem,omitempty"`
}

type reconcileTaskPayloadStorage struct {
	Desired desiredRuntimeSpecStorage `json:"desired"`
}

func desiredRuntimeDigest(spec desiredRuntimeSpecStorage) (string, error) {
	spec.Digest = ""
	canonical, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// materializeDesiredRuntimeTx snapshots every mutable runtime input after the
// caller has applied its desired-state mutation but before the transaction is
// committed. This prevents a later read from silently changing an in-flight
// task and makes retries byte-for-byte deterministic.
func (s *Postgres) materializeDesiredRuntimeTx(ctx context.Context, tx *sql.Tx, row serverRow, generation int64) (string, error) {
	revision, err := s.trustedCatalogRevision(ctx, tx, row.GameID, row.GameBundleDigest)
	if err != nil {
		return "", err
	}
	game := revision.Game
	if game.RuntimeTarget == nil || game.BundleDigest != row.GameBundleDigest {
		return "", packageRuntimeTargetUnavailable(game)
	}
	runtimeTarget, err := json.Marshal(game.RuntimeTarget)
	if err != nil {
		return "", domain.NewProblem("INTERNAL_ERROR", "无法编码运行时目标", true)
	}

	var desiredPower string
	var memoryBytes, diskBytes int64
	if err := tx.QueryRowContext(ctx, `
		SELECT desired_power, memory_limit_bytes, disk_limit_bytes
		FROM servers WHERE id = $1
	`, row.ID).Scan(&desiredPower, &memoryBytes, &diskBytes); err != nil {
		return "", domain.NewProblem("INTERNAL_ERROR", "无法读取服务器运行时资源", true)
	}

	allocations := make([]provisionTaskAllocation, 0, 4)
	allocationRows, err := tx.QueryContext(ctx, `
		SELECT id::text, host(bind_ip), port, container_port, protocol,
		       COALESCE(port_ref, ''), role
		FROM allocations
		WHERE server_id = $1 AND released_at IS NULL
		ORDER BY is_primary DESC, created_at, id
	`, row.ID)
	if err != nil {
		return "", domain.NewProblem("INTERNAL_ERROR", "无法读取服务器端口分配", true)
	}
	defer allocationRows.Close()
	for allocationRows.Next() {
		var allocation provisionTaskAllocation
		var hostPort, containerPort int
		var protocol string
		if err := allocationRows.Scan(&allocation.AllocationID, &allocation.BindIP, &hostPort, &containerPort, &protocol, &allocation.PortRef, &allocation.Role); err != nil {
			return "", domain.NewProblem("INTERNAL_ERROR", "无法解析服务器端口分配", true)
		}
		allocation.HostPort = uint32(hostPort)
		allocation.ContainerPort = uint32(containerPort)
		allocation.Protocol = provisionProtocolName(protocol)
		allocations = append(allocations, allocation)
	}
	if err := allocationRows.Err(); err != nil {
		return "", domain.NewProblem("INTERNAL_ERROR", "无法读取服务器端口分配", true)
	}

	server := domain.Server{
		ID: row.ID, GameID: row.GameID, GameBundleDigest: row.GameBundleDigest,
		GameDefinitionVersion: row.GameDefinitionVersion, NodeID: row.NodeID, Generation: generation,
	}
	startup, _, err := startupFromFixedBundle(server, game, nil)
	if err != nil {
		return "", err
	}
	values, err := s.startupValuesTx(ctx, tx, row.ID)
	if err != nil {
		return "", err
	}
	if err := s.decryptSecretValues(startup.Variables, values); err != nil {
		return "", domain.NewProblem("INTERNAL_ERROR", "无法解密启动变量", true)
	}

	spec := desiredRuntimeSpecStorage{
		GameDefinitionID: row.GameID,
		BundleDigest:     row.GameBundleDigest,
		RuntimeTarget:    string(runtimeTarget),
		ResourceLimits: provisionResourceLimits{
			MemoryBytes: uint64(memoryBytes),
			DiskBytes:   uint64(diskBytes),
		},
		Allocations:    allocations,
		Variables:      stringifiedNonSecretStartupValues(startup.Variables, values),
		SecretKeys:     secretStartupKeys(startup.Variables),
		DesiredRunning: desiredPower == "running",
		Generation:     uint64(generation),
	}
	spec.BundleRevision, spec.TrustRootPEM = revision.Document, revision.TrustRoot
	spec.Digest, err = desiredRuntimeDigest(spec)
	if err != nil {
		return "", domain.NewProblem("INTERNAL_ERROR", "无法计算运行时规范摘要", true)
	}
	encoded, err := json.Marshal(reconcileTaskPayloadStorage{Desired: spec})
	if err != nil {
		return "", domain.NewProblem("INTERNAL_ERROR", "无法生成重新对账任务负载", true)
	}
	return string(encoded), nil
}

func provisionDesiredDigest(payload provisionTaskPayload, generation uint64) (string, error) {
	spec := desiredRuntimeSpecStorage{
		GameDefinitionID: payload.GameDefinitionID,
		BundleDigest:     payload.BundleDigest,
		RuntimeTarget:    payload.RuntimeTarget,
		ResourceLimits:   payload.ResourceLimits,
		Allocations:      payload.Allocations,
		Variables:        payload.Variables,
		SecretKeys:       payload.SecretKeys,
		DesiredRunning:   payload.StartAfterProvision,
		Generation:       generation,
		BundleRevision:   payload.BundleRevisionJSON,
		TrustRootPEM:     payload.TrustRootPEM,
	}
	digest, err := desiredRuntimeDigest(spec)
	if err != nil {
		return "", fmt.Errorf("digest desired runtime: %w", err)
	}
	return digest, nil
}

func (s *Postgres) bundleEvidenceTx(ctx context.Context, tx *sql.Tx, digest string) ([]byte, []byte, error) {
	var document, trustRoot sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT gb.document::text, tr.public_key_pem
		FROM game_bundles gb
		LEFT JOIN bundle_trust_roots tr ON tr.key_id = gb.signature_key_id AND tr.revoked_at IS NULL
		WHERE gb.digest = $1 AND gb.review_status = 'approved' AND gb.revoked_at IS NULL
	`, digest).Scan(&document, &trustRoot)
	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, domain.NewProblem("INTERNAL_ERROR", "无法读取 Bundle 信任证据", true)
	}
	if !document.Valid && !trustRoot.Valid {
		return nil, nil, nil
	}
	if !document.Valid || !trustRoot.Valid {
		return nil, nil, domain.NewProblem("PACKAGE_INCOMPATIBLE", "Bundle 签名或信任根不完整", false)
	}
	return []byte(document.String), []byte(trustRoot.String), nil
}
