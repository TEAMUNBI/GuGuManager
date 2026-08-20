package domain

import (
	"encoding/json"
	"time"
)

type BundleSource struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	IndexURL  string    `json:"indexUrl"`
	Official  bool      `json:"official"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type BundleSourceInput struct {
	Name     string `json:"name"`
	IndexURL string `json:"indexUrl"`
	Official bool   `json:"official"`
	Enabled  *bool  `json:"enabled,omitempty"`
}

type BundleTrustRoot struct {
	ID        string     `json:"id"`
	KeyID     string     `json:"keyId"`
	Name      string     `json:"name"`
	Source    string     `json:"source"`
	CreatedAt time.Time  `json:"createdAt"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
}

type BundleTrustRootInput struct {
	Name         string `json:"name"`
	PublicKeyPEM string `json:"publicKeyPem"`
	Source       string `json:"source"`
}

type BundleRevision struct {
	ID                string     `json:"id"`
	GameDefinitionID  string     `json:"gameDefinitionId"`
	DefinitionVersion string     `json:"definitionVersion"`
	GameVersion       string     `json:"gameVersion"`
	Digest            string     `json:"digest"`
	SchemaVersion     string     `json:"schemaVersion"`
	License           string     `json:"license"`
	SignatureKeyID    string     `json:"signatureKeyId"`
	SignatureVerified bool       `json:"signatureVerified"`
	ReviewStatus      string     `json:"reviewStatus"`
	PublishedAt       time.Time  `json:"publishedAt"`
	RevokedAt         *time.Time `json:"revokedAt,omitempty"`
}

type BundleImportInput struct {
	Document json.RawMessage `json:"document"`
}

type BundleReviewInput struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

type BundleReview struct {
	ID         string    `json:"id"`
	BundleID   string    `json:"bundleId"`
	ReviewerID string    `json:"reviewerId,omitempty"`
	Decision   string    `json:"decision"`
	Reason     string    `json:"reason"`
	CreatedAt  time.Time `json:"createdAt"`
}

type ServerUpgradeInput struct {
	BundleDigest string `json:"bundleDigest"`
}
