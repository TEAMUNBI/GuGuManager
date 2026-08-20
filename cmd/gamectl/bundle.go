package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"github.com/gugumanager/gugumanager/internal/domain"
	gamedefinition "github.com/gugumanager/gugumanager/spec/game-definition"
)

// Bundle is a fixed, immutable, verifiable snapshot of a game release. It
// carries every field the Agent needs to run and maintain a server, together
// with the identity fields the control plane persists in game_bundles.
type Bundle struct {
	GameDefinitionID  string            `json:"gameDefinitionId"`
	DefinitionVersion string            `json:"definitionVersion"`
	GameVersion       string            `json:"gameVersion"`
	Digest            string            `json:"digest"` // sha256:<hex>
	SchemaVersion     string            `json:"schemaVersion"`
	License           string            `json:"license"`
	Compatibility     map[string]string `json:"compatibility"` // panel/agent/platforms 等
	Capabilities      []string          `json:"capabilities"`
	Image             string            `json:"image"`   // 运行时镜像（含 digest）
	Command           json.RawMessage   `json:"command"` // 启动命令模板
	Adapter           string            `json:"adapter"`
	User              string            `json:"user"`
	WorkingDir        string            `json:"workingDir"`
	Environment       map[string]string `json:"environment,omitempty"`
	DataMounts        json.RawMessage   `json:"dataMounts"`
	Console           json.RawMessage   `json:"console,omitempty"`
	Variables         json.RawMessage   `json:"variables"` // schema + secrets + bindings
	Stop              json.RawMessage   `json:"stop"`
	Health            json.RawMessage   `json:"health"`
	Ports             json.RawMessage   `json:"ports"`
	Install           json.RawMessage   `json:"install"`
	Lifecycle         json.RawMessage   `json:"lifecycle"`
	Revision          json.RawMessage   `json:"revision,omitempty"`
	Signature         *BundleSignature  `json:"signature,omitempty"`
}

// bundleSourceDefinition decodes the parts of a GameDefinition a Bundle is
// built from, keeping the runtime payloads as raw JSON so the snapshot never
// re-shapes what the Agent consumes.
type bundleSourceDefinition struct {
	APIVersion string `json:"apiVersion"`
	Metadata   struct {
		ID      string `json:"id"`
		Version string `json:"version"`
		License string `json:"license"`
	} `json:"metadata"`
	Spec struct {
		Release struct {
			Version string `json:"version"`
		} `json:"release"`
		Compatibility map[string]json.RawMessage `json:"compatibility"`
		Capabilities  []string                   `json:"capabilities"`
		Variables     json.RawMessage            `json:"variables"`
		Runtime       struct {
			Adapter     string            `json:"adapter"`
			Image       string            `json:"image"`
			User        string            `json:"user"`
			WorkingDir  string            `json:"workingDir"`
			Command     json.RawMessage   `json:"command"`
			Environment map[string]string `json:"environment"`
			DataMounts  json.RawMessage   `json:"dataMounts"`
			Console     json.RawMessage   `json:"console"`
			Ports       json.RawMessage   `json:"ports"`
			Stop        json.RawMessage   `json:"stop"`
			Health      json.RawMessage   `json:"health"`
		} `json:"runtime"`
		Install   json.RawMessage `json:"install"`
		Lifecycle json.RawMessage `json:"lifecycle"`
		Bundle    json.RawMessage `json:"bundle"`
	} `json:"spec"`
}

// buildBundle turns one validated GameDefinition file into a fixed Bundle with
// a stable digest. The digest is sha256 over the canonical JSON of every
// content field with the digest field itself blanked, so two builds of the
// same definition always agree and a verifier can recompute it from the
// document alone.
func buildBundle(inputPath string) (*Bundle, error) {
	content, err := os.ReadFile(inputPath)
	if err != nil {
		return nil, err
	}
	if err := gamedefinition.ValidateJSON(content); err != nil {
		return nil, fmt.Errorf("%s is not a valid GameDefinition: %w", inputPath, err)
	}
	var definition bundleSourceDefinition
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&definition); err != nil {
		return nil, fmt.Errorf("decode %s: %w", inputPath, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode %s: trailing JSON value", inputPath)
		}
		return nil, fmt.Errorf("decode %s: trailing data: %w", inputPath, err)
	}

	compatibility, err := compatibilityStrings(definition.Spec.Compatibility)
	if err != nil {
		return nil, err
	}
	bundle := &Bundle{
		GameDefinitionID:  definition.Metadata.ID,
		DefinitionVersion: definition.Metadata.Version,
		GameVersion:       definition.Spec.Release.Version,
		SchemaVersion:     definition.APIVersion,
		License:           definition.Metadata.License,
		Compatibility:     compatibility,
		Capabilities:      definition.Spec.Capabilities,
		Adapter:           definition.Spec.Runtime.Adapter,
		Image:             definition.Spec.Runtime.Image,
		User:              definition.Spec.Runtime.User,
		WorkingDir:        definition.Spec.Runtime.WorkingDir,
		Command:           definition.Spec.Runtime.Command,
		Environment:       definition.Spec.Runtime.Environment,
		DataMounts:        definition.Spec.Runtime.DataMounts,
		Console:           definition.Spec.Runtime.Console,
		Variables:         definition.Spec.Variables,
		Stop:              definition.Spec.Runtime.Stop,
		Health:            definition.Spec.Runtime.Health,
		Ports:             definition.Spec.Runtime.Ports,
		Install:           definition.Spec.Install,
		Lifecycle:         definition.Spec.Lifecycle,
		Revision:          definition.Spec.Bundle,
	}
	bundle.Digest, err = gamedefinition.CanonicalBundleDigest(content)
	if err != nil {
		return nil, fmt.Errorf("canonicalize bundle: %w", err)
	}
	return bundle, nil
}

// compatibilityStrings flattens spec.compatibility into the string map the
// Bundle carries. String values pass through; platform lists join into a
// comma-separated value so the map stays the closed panel/agent/platforms
// shape the control plane records in game_bundles.compatibility.
func compatibilityStrings(raw map[string]json.RawMessage) (map[string]string, error) {
	compatibility := make(map[string]string, len(raw))
	for key, value := range raw {
		decoder := json.NewDecoder(bytes.NewReader(value))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return nil, fmt.Errorf("decode spec.compatibility.%s: %w", key, err)
		}
		switch typed := decoded.(type) {
		case string:
			compatibility[key] = typed
		case []any:
			parts := make([]string, 0, len(typed))
			for _, item := range typed {
				parts = append(parts, fmt.Sprint(item))
			}
			compatibility[key] = strings.Join(parts, ",")
		default:
			return nil, fmt.Errorf("spec.compatibility.%s has unsupported type %T", key, decoded)
		}
	}
	return compatibility, nil
}

func bundleCommand(args []string) {
	if len(args) == 0 {
		bundleUsage()
		os.Exit(2)
	}
	switch args[0] {
	case "build":
		buildCommand(args[1:])
	case "convert":
		convertCommand(args[1:])
	case "sign":
		signCommand(args[1:])
	case "verify":
		verifyCommand(args[1:])
	case "index-build":
		indexBuildCommand(args[1:])
	case "index-sign":
		indexSignCommand(args[1:])
	case "publish":
		publishCommand(args[1:])
	default:
		bundleUsage()
		os.Exit(2)
	}
}

func buildCommand(args []string) {
	flags := flag.NewFlagSet("bundle build", flag.ContinueOnError)
	outputPath := flags.String("out", "", "bundle output path (default stdout)")
	if err := flags.Parse(args); err != nil || flags.NArg() != 1 {
		bundleUsage()
		os.Exit(2)
	}
	bundle, err := buildBundle(flags.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "bundle build failed: %v\n", err)
		os.Exit(1)
	}
	output, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "bundle build failed: %v\n", err)
		os.Exit(1)
	}
	if *outputPath == "" {
		fmt.Println(string(output))
		return
	}
	if err := writeBundle(*outputPath, *bundle); err != nil {
		fmt.Fprintf(os.Stderr, "bundle build failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("built", bundle.Digest, "to", *outputPath)
}

func convertCommand(args []string) {
	flags := flag.NewFlagSet("bundle convert", flag.ContinueOnError)
	outputPath := flags.String("out", "", "v1beta1 GameDefinition output path")
	if err := flags.Parse(args); err != nil || flags.NArg() != 1 || *outputPath == "" {
		bundleUsage()
		os.Exit(2)
	}
	content, err := os.ReadFile(flags.Arg(0))
	if err == nil {
		content, err = gamedefinition.NormalizeToV1Beta1JSON(content)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "bundle convert failed: %v\n", err)
		os.Exit(1)
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, content, "", "  "); err != nil {
		fmt.Fprintf(os.Stderr, "bundle convert failed: %v\n", err)
		os.Exit(1)
	}
	formatted.WriteByte('\n')
	if err := atomicWriteBytes(*outputPath, formatted.Bytes()); err != nil {
		fmt.Fprintf(os.Stderr, "bundle convert failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("converted", flags.Arg(0), "to", *outputPath)
}

func publishCommand(args []string) {
	flags := flag.NewFlagSet("bundle publish", flag.ContinueOnError)
	trustRoot := flags.String("trust-root", os.Getenv("GUGU_BUNDLE_TRUST_ROOT"), "Ed25519 public trust root PEM")
	if err := flags.Parse(args); err != nil || flags.NArg() != 1 || *trustRoot == "" {
		bundleUsage()
		os.Exit(2)
	}
	bundlePath := flags.Arg(0)
	bundle, err := readBundle(bundlePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bundle publish failed: %v\n", err)
		os.Exit(1)
	}
	public, err := loadEd25519PublicKey(*trustRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bundle publish failed: %v\n", err)
		os.Exit(1)
	}
	if err := verifyBundleSignature(bundle, public); err != nil {
		fmt.Fprintf(os.Stderr, "bundle publish failed: %v\n", err)
		os.Exit(1)
	}
	if bundle.SchemaVersion != gamedefinition.APIVersionV1Beta1 {
		fmt.Fprintf(os.Stderr, "bundle publish failed: schemaVersion must be %s\n", gamedefinition.APIVersionV1Beta1)
		os.Exit(1)
	}
	dsn := os.Getenv("GUGU_DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "bundle publish failed: GUGU_DATABASE_URL is not set")
		os.Exit(1)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bundle publish failed: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()
	publicPEM, err := marshalEd25519PublicKeyPEM(public)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bundle publish failed: %v\n", err)
		os.Exit(1)
	}
	if err := registerBundleTrustRoot(db, bundle.Signature.KeyID, publicPEM, filepath.Base(*trustRoot)); err != nil {
		fmt.Fprintf(os.Stderr, "bundle publish failed: %v\n", err)
		os.Exit(1)
	}
	source := "file://" + filepath.ToSlash(bundlePath)
	if err := publishBundle(db, bundle, source); err != nil {
		fmt.Fprintf(os.Stderr, "bundle publish failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("published", bundle.Digest, "as", bundle.GameDefinitionID, bundle.DefinitionVersion)
}

func signCommand(args []string) {
	flags := flag.NewFlagSet("bundle sign", flag.ContinueOnError)
	keyPath := flags.String("key", "", "Ed25519 PKCS#8 private key PEM")
	output := flags.String("out", "", "signed bundle output path")
	if err := flags.Parse(args); err != nil || flags.NArg() != 1 || *keyPath == "" || *output == "" {
		bundleUsage()
		os.Exit(2)
	}
	bundle, err := readBundle(flags.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "bundle sign failed: %v\n", err)
		os.Exit(1)
	}
	private, err := loadEd25519PrivateKey(*keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bundle sign failed: %v\n", err)
		os.Exit(1)
	}
	signed, err := signBundle(bundle, private)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bundle sign failed: %v\n", err)
		os.Exit(1)
	}
	if err := writeBundle(*output, signed); err != nil {
		fmt.Fprintf(os.Stderr, "bundle sign failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("signed", signed.Digest, "with", signed.Signature.KeyID)
}

func verifyCommand(args []string) {
	flags := flag.NewFlagSet("bundle verify", flag.ContinueOnError)
	keyPath := flags.String("key", "", "Ed25519 public trust root PEM")
	if err := flags.Parse(args); err != nil || flags.NArg() != 1 || *keyPath == "" {
		bundleUsage()
		os.Exit(2)
	}
	bundle, err := readBundle(flags.Arg(0))
	if err == nil {
		var public ed25519.PublicKey
		public, err = loadEd25519PublicKey(*keyPath)
		if err == nil {
			err = verifyBundleSignature(bundle, public)
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "bundle verify failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("verified", bundle.Digest, "as", bundle.Signature.KeyID)
}

func isBundleDigest(digest string) bool {
	if !strings.HasPrefix(digest, "sha256:") {
		return false
	}
	hexPart := strings.TrimPrefix(digest, "sha256:")
	if len(hexPart) != 64 {
		return false
	}
	for _, character := range hexPart {
		switch {
		case character >= '0' && character <= '9':
		case character >= 'a' && character <= 'f':
		default:
			return false
		}
	}
	return true
}

// publishBundle upserts the approved game definition and its fixed bundle into
// the control plane schema. It is the only path that writes game_bundles, so
// the digest, definition version and review status stay consistent with what
// CreateServer looks up.
func publishBundle(db *sql.DB, bundle Bundle, source string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// The Bundle carries no display name or source URL, so the definition id
	// doubles as the name and the publish command's own file is the source.
	// Review status is approved: a published bundle is one the control plane
	// may create servers from.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO game_definitions (id, name, source_url, review_status)
		VALUES ($1, $1, $2, 'approved')
		ON CONFLICT (id) DO UPDATE
		SET review_status = 'approved', updated_at = now()
	`, bundle.GameDefinitionID, source); err != nil {
		return fmt.Errorf("upsert game_definitions: %w", err)
	}

	compatibility, err := json.Marshal(bundle.Compatibility)
	if err != nil {
		return fmt.Errorf("encode compatibility: %w", err)
	}
	document, err := json.Marshal(bundle)
	if err != nil {
		return fmt.Errorf("encode bundle document: %w", err)
	}
	runtimeTarget, err := runtimeTargetFromBundle(bundle)
	if err != nil {
		return err
	}
	runtimeTargetJSON, err := json.Marshal(runtimeTarget)
	if err != nil {
		return fmt.Errorf("encode runtime target: %w", err)
	}
	manifestJSON := []byte("[]")
	extensionsJSON := []byte("[]")
	var sbomJSON any
	if len(bundle.Revision) > 0 {
		var revision gamedefinition.BundleRevisionMetadata
		if err := json.Unmarshal(bundle.Revision, &revision); err != nil {
			return fmt.Errorf("decode bundle revision metadata: %w", err)
		}
		manifestJSON, _ = json.Marshal(revision.Artifacts)
		extensionsJSON, _ = json.Marshal(revision.Extensions)
		if revision.SBOM != nil {
			encoded, _ := json.Marshal(revision.SBOM)
			sbomJSON = string(encoded)
		}
	}
	signatureJSON, err := json.Marshal(bundle.Signature)
	if err != nil || bundle.Signature == nil {
		return errors.New("bundle signature is required")
	}
	var insertedID string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO game_bundles (
			game_definition_id, definition_version, game_version, digest,
			schema_version, license, compatibility, published_at, document,
			runtime_target, artifact_manifest, extensions, sbom, signature,
			signature_key_id, signature_verified_at, review_status, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, now(), $8::jsonb,
		        $9::jsonb, $10::jsonb, $11::jsonb, $12::jsonb, $13::jsonb,
		        $14, now(), 'approved', now())
		ON CONFLICT (game_definition_id, definition_version) DO NOTHING
		RETURNING id::text
	`, bundle.GameDefinitionID, bundle.DefinitionVersion, bundle.GameVersion, bundle.Digest,
		bundle.SchemaVersion, bundle.License, string(compatibility), string(document), string(runtimeTargetJSON),
		string(manifestJSON), string(extensionsJSON), sbomJSON, string(signatureJSON), bundle.Signature.KeyID).Scan(&insertedID)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("upsert game_bundles: %w", err)
	}
	if err == sql.ErrNoRows {
		var existingDigest string
		if err := tx.QueryRowContext(ctx, `
			SELECT digest FROM game_bundles WHERE game_definition_id = $1 AND definition_version = $2
		`, bundle.GameDefinitionID, bundle.DefinitionVersion).Scan(&existingDigest); err != nil {
			return fmt.Errorf("read immutable bundle revision: %w", err)
		}
		if existingDigest != bundle.Digest {
			return fmt.Errorf("immutable revision %s@%s is already bound to %s", bundle.GameDefinitionID, bundle.DefinitionVersion, existingDigest)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE game_bundles SET review_status = 'approved', revoked_at = NULL, updated_at = now()
			WHERE game_definition_id = $1 AND definition_version = $2 AND digest = $3
		`, bundle.GameDefinitionID, bundle.DefinitionVersion, bundle.Digest); err != nil {
			return fmt.Errorf("approve existing bundle revision: %w", err)
		}
	}

	return tx.Commit()
}

func registerBundleTrustRoot(db *sql.DB, keyID, publicPEM, name string) error {
	_, err := db.Exec(`
		INSERT INTO bundle_trust_roots (key_id, name, public_key_pem, source)
		VALUES ($1, $2, $3, 'operator')
		ON CONFLICT (key_id) DO UPDATE SET name = EXCLUDED.name, public_key_pem = EXCLUDED.public_key_pem, revoked_at = NULL
	`, keyID, name, publicPEM)
	if err != nil {
		return fmt.Errorf("register bundle trust root: %w", err)
	}
	return nil
}

func runtimeTargetFromBundle(bundle Bundle) (domain.GameRuntimeTarget, error) {
	var command domain.StartupCommand
	var mounts []domain.RuntimeDataMount
	var ports []domain.RuntimePort
	var stop domain.RuntimeStop
	var health domain.RuntimeHealth
	var console *domain.RuntimeConsoleAdapter
	for name, target := range map[string]any{
		"command": &command, "dataMounts": &mounts, "ports": &ports, "stop": &stop, "health": &health, "console": &console,
	} {
		var source json.RawMessage
		switch name {
		case "command":
			source = bundle.Command
		case "dataMounts":
			source = bundle.DataMounts
		case "ports":
			source = bundle.Ports
		case "stop":
			source = bundle.Stop
		case "health":
			source = bundle.Health
		case "console":
			source = bundle.Console
		}
		if len(source) == 0 && name == "console" {
			continue
		}
		if err := json.Unmarshal(source, target); err != nil {
			return domain.GameRuntimeTarget{}, fmt.Errorf("decode runtime %s: %w", name, err)
		}
	}
	target := domain.GameRuntimeTarget{
		Adapter: bundle.Adapter, Image: bundle.Image, User: bundle.User, WorkingDir: bundle.WorkingDir,
		Command: command, Environment: bundle.Environment, DataMounts: mounts, Ports: ports, Stop: stop, Health: health, Console: console,
	}
	canonical, err := json.Marshal(target)
	if err != nil {
		return domain.GameRuntimeTarget{}, err
	}
	digest := sha256.Sum256(canonical)
	target.Digest = "sha256:" + hex.EncodeToString(digest[:])
	return target, nil
}

func bundleUsage() {
	fmt.Println("gamectl bundle build <definition.json>")
	fmt.Println("gamectl bundle convert --out <definition.v1beta1.json> <definition.json>")
	fmt.Println("gamectl bundle sign --key <private.pem> --out <signed.json> <bundle.json>")
	fmt.Println("gamectl bundle verify --key <public.pem> <signed.json>")
	fmt.Println("gamectl bundle publish --trust-root <public.pem> <signed.json>")
}
