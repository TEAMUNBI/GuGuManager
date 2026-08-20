package bundletrust

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/install"
	gamedefinition "github.com/gugumanager/gugumanager/spec/game-definition"
)

type Signature struct {
	Algorithm     string `json:"algorithm"`
	KeyID         string `json:"keyId"`
	PayloadDigest string `json:"payloadDigest"`
	Value         string `json:"value"`
}

// Document is the signed gamectl Bundle revision. It intentionally mirrors the
// wire artifact rather than a mutable catalog row.
type Document struct {
	GameDefinitionID  string            `json:"gameDefinitionId"`
	DefinitionVersion string            `json:"definitionVersion"`
	GameVersion       string            `json:"gameVersion"`
	Digest            string            `json:"digest"`
	SchemaVersion     string            `json:"schemaVersion"`
	License           string            `json:"license"`
	Compatibility     map[string]string `json:"compatibility"`
	Capabilities      []string          `json:"capabilities"`
	Image             string            `json:"image"`
	Command           json.RawMessage   `json:"command"`
	Adapter           string            `json:"adapter"`
	User              string            `json:"user"`
	WorkingDir        string            `json:"workingDir"`
	Environment       map[string]string `json:"environment,omitempty"`
	DataMounts        json.RawMessage   `json:"dataMounts"`
	Console           json.RawMessage   `json:"console,omitempty"`
	Variables         json.RawMessage   `json:"variables"`
	Stop              json.RawMessage   `json:"stop"`
	Health            json.RawMessage   `json:"health"`
	Ports             json.RawMessage   `json:"ports"`
	Install           json.RawMessage   `json:"install"`
	Lifecycle         json.RawMessage   `json:"lifecycle"`
	Revision          json.RawMessage   `json:"revision,omitempty"`
	Signature         *Signature        `json:"signature,omitempty"`
}

type Verified struct {
	Document      Document
	RuntimeTarget domain.GameRuntimeTarget
	KeyID         string
}

// Installation is the immutable, signature-covered install plan an Agent may
// execute. Artifact sizes come from spec.bundle rather than the transport so a
// compromised origin cannot swap a same-digest declaration with unbounded
// content metadata.
type Installation struct {
	Artifacts  []install.Artifact
	Allowlist  []string
	Lifecycle  string
	Extensions []gamedefinition.ExtensionDescriptor
}

// InstallPlan converts the signed wire fields to the narrow builtin installer
// contract. Verify must have succeeded before this function is called.
func InstallPlan(document Document) (Installation, error) {
	var declared struct {
		Artifacts []struct {
			URL         string `json:"url"`
			Destination string `json:"destination"`
			SHA256      string `json:"sha256"`
		} `json:"artifacts"`
		NetworkAllowlist []string `json:"networkAllowlist"`
	}
	decoder := json.NewDecoder(bytes.NewReader(document.Install))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&declared); err != nil {
		return Installation{}, fmt.Errorf("decode Bundle install plan: %w", err)
	}
	var lifecycle struct {
		Install string `json:"install"`
	}
	if err := json.Unmarshal(document.Lifecycle, &lifecycle); err != nil {
		return Installation{}, fmt.Errorf("decode Bundle lifecycle: %w", err)
	}
	if lifecycle.Install != "builtin.artifacts" && len(declared.Artifacts) > 0 {
		return Installation{}, fmt.Errorf("unsupported artifact install lifecycle %q", lifecycle.Install)
	}
	var revision gamedefinition.BundleRevisionMetadata
	if err := json.Unmarshal(document.Revision, &revision); err != nil {
		return Installation{}, fmt.Errorf("decode Bundle revision: %w", err)
	}
	manifest := make(map[string]gamedefinition.ArtifactManifestEntry, len(revision.Artifacts))
	for _, entry := range revision.Artifacts {
		manifest[entry.Destination] = entry
	}
	artifacts := make([]install.Artifact, 0, len(declared.Artifacts))
	for index, artifact := range declared.Artifacts {
		entry, ok := manifest[artifact.Destination]
		if !ok || entry.URL != artifact.URL || entry.SHA256 != artifact.SHA256 {
			return Installation{}, fmt.Errorf("install artifact %d does not match signed revision manifest", index)
		}
		artifacts = append(artifacts, install.Artifact{
			URL: artifact.URL, Destination: artifact.Destination, SHA256: artifact.SHA256, SizeBytes: entry.SizeBytes,
		})
	}
	if err := install.ValidateArtifacts(artifacts); err != nil {
		return Installation{}, err
	}
	if _, err := install.ValidateAllowlist(declared.NetworkAllowlist); err != nil {
		return Installation{}, err
	}
	return Installation{
		Artifacts: artifacts, Allowlist: append([]string(nil), declared.NetworkAllowlist...),
		Lifecycle: lifecycle.Install, Extensions: append([]gamedefinition.ExtensionDescriptor(nil), revision.Extensions...),
	}, nil
}

func Verify(documentJSON, trustRootPEM []byte) (Verified, error) {
	var document Document
	decoder := json.NewDecoder(bytes.NewReader(documentJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return Verified{}, fmt.Errorf("decode signed Bundle: %w", err)
	}
	if document.SchemaVersion != gamedefinition.APIVersionV1Beta1 || !gamedefinition.ValidPrefixedSHA256(document.Digest) {
		return Verified{}, errors.New("Bundle uses an unsupported schema or digest")
	}
	if document.Signature == nil || document.Signature.Algorithm != "ed25519" {
		return Verified{}, errors.New("Bundle has no supported signature")
	}
	public, err := parsePublicKey(trustRootPEM)
	if err != nil {
		return Verified{}, err
	}
	keyDigest := sha256.Sum256(public)
	keyID := "sha256:" + hex.EncodeToString(keyDigest[:])
	if document.Signature.KeyID != keyID {
		return Verified{}, errors.New("Bundle signature key does not match trust root")
	}
	signature := document.Signature
	document.Signature = nil
	canonical, err := json.Marshal(document)
	if err != nil {
		return Verified{}, err
	}
	payloadDigest := sha256.Sum256(canonical)
	if signature.PayloadDigest != "sha256:"+hex.EncodeToString(payloadDigest[:]) {
		return Verified{}, errors.New("Bundle signed payload digest mismatch")
	}
	value, err := base64.RawStdEncoding.DecodeString(signature.Value)
	if err != nil || len(value) != ed25519.SignatureSize || !ed25519.Verify(public, canonical, value) {
		return Verified{}, errors.New("Bundle signature verification failed")
	}
	document.Signature = signature
	definitionJSON, err := GameDefinitionJSON(document)
	if err != nil {
		return Verified{}, err
	}
	computedDigest, err := gamedefinition.CanonicalBundleDigest(definitionJSON)
	if err != nil {
		return Verified{}, fmt.Errorf("validate reconstructed GameDefinition: %w", err)
	}
	if document.Digest != computedDigest {
		return Verified{}, errors.New("Bundle canonical content digest mismatch")
	}
	target, err := runtimeTarget(document)
	if err != nil {
		return Verified{}, err
	}
	if err := verifyRevisionMetadata(document.Revision); err != nil {
		return Verified{}, err
	}
	return Verified{Document: document, RuntimeTarget: target, KeyID: keyID}, nil
}

// GameDefinitionJSON reconstructs the canonical v1beta1 source definition
// represented by a signed Bundle. Bundle snapshots intentionally omit only
// presentation fields such as metadata.name; those fields are not part of the
// canonical digest, so the stable definition id is used as the display name.
// Keeping this conversion here gives the control plane and Agent one verifier
// for Startup declarations as well as runtime targets.
func GameDefinitionJSON(document Document) ([]byte, error) {
	platforms := make([]string, 0, 2)
	for _, platform := range strings.Split(document.Compatibility["platforms"], ",") {
		if platform = strings.TrimSpace(platform); platform != "" {
			platforms = append(platforms, platform)
		}
	}
	compatibility := map[string]any{
		"panel": document.Compatibility["panel"], "agent": document.Compatibility["agent"], "platforms": platforms,
	}
	runtime := map[string]any{
		"adapter": document.Adapter, "image": document.Image, "user": document.User,
		"workingDir": document.WorkingDir, "command": document.Command,
		"environment": document.Environment, "dataMounts": document.DataMounts,
		"ports": document.Ports, "stop": document.Stop, "health": document.Health,
	}
	if len(document.Console) > 0 && string(bytes.TrimSpace(document.Console)) != "null" {
		runtime["console"] = document.Console
	}
	spec := map[string]any{
		"release":       map[string]any{"version": document.GameVersion},
		"compatibility": compatibility, "capabilities": document.Capabilities,
		"variables": document.Variables, "runtime": runtime, "install": document.Install,
		"lifecycle": document.Lifecycle, "bundle": document.Revision,
	}
	source := map[string]any{
		"apiVersion": document.SchemaVersion, "kind": "GameDefinition",
		"metadata": map[string]any{"id": document.GameDefinitionID, "name": document.GameDefinitionID, "version": document.DefinitionVersion, "license": document.License},
		"spec":     spec,
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		return nil, fmt.Errorf("reconstruct GameDefinition: %w", err)
	}
	return encoded, nil
}

func SupportsPlatform(compatibility map[string]string, platform string) bool {
	for _, candidate := range strings.Split(compatibility["platforms"], ",") {
		if strings.TrimSpace(candidate) == platform {
			return true
		}
	}
	return false
}

func parsePublicKey(content []byte) (ed25519.PublicKey, error) {
	block, rest := pem.Decode(content)
	if block == nil || block.Type != "PUBLIC KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("trust root must contain exactly one PKIX PUBLIC KEY")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	public, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("trust root is not Ed25519")
	}
	return public, nil
}

func TrustRootKeyID(content []byte) (string, error) {
	public, err := parsePublicKey(content)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(public)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func runtimeTarget(document Document) (domain.GameRuntimeTarget, error) {
	target := domain.GameRuntimeTarget{
		Adapter: document.Adapter, Image: document.Image, User: document.User, WorkingDir: document.WorkingDir,
		Environment: document.Environment,
	}
	for name, pair := range map[string]struct {
		raw    json.RawMessage
		target any
	}{
		"command": {document.Command, &target.Command}, "dataMounts": {document.DataMounts, &target.DataMounts},
		"ports": {document.Ports, &target.Ports}, "stop": {document.Stop, &target.Stop},
		"health": {document.Health, &target.Health}, "console": {document.Console, &target.Console},
	} {
		if len(pair.raw) == 0 && name == "console" {
			continue
		}
		if err := json.Unmarshal(pair.raw, pair.target); err != nil {
			return domain.GameRuntimeTarget{}, fmt.Errorf("decode Bundle runtime %s: %w", name, err)
		}
	}
	if target.Adapter != "container/v1" || !strings.Contains(target.Image, "@sha256:") || target.Command.Executable == "" || len(target.DataMounts) == 0 {
		return domain.GameRuntimeTarget{}, errors.New("Bundle runtime target is incomplete")
	}
	canonical, err := json.Marshal(target)
	if err != nil {
		return domain.GameRuntimeTarget{}, err
	}
	digest := sha256.Sum256(canonical)
	target.Digest = "sha256:" + hex.EncodeToString(digest[:])
	return target, nil
}

func verifyRevisionMetadata(raw json.RawMessage) error {
	if len(raw) == 0 {
		return errors.New("v1beta1 Bundle revision metadata is missing")
	}
	var revision gamedefinition.BundleRevisionMetadata
	if err := json.Unmarshal(raw, &revision); err != nil {
		return fmt.Errorf("decode Bundle revision metadata: %w", err)
	}
	for index, artifact := range revision.Artifacts {
		if len(artifact.SHA256) != 64 || artifact.URL == "" || artifact.Destination == "" || artifact.SizeBytes < 0 {
			return fmt.Errorf("Bundle artifact %d is invalid", index)
		}
	}
	for index, extension := range revision.Extensions {
		if extension.ABI != "wasi-component/v1" || len(extension.ArtifactSHA256) != 64 {
			return fmt.Errorf("Bundle extension %d is invalid", index)
		}
	}
	return nil
}
