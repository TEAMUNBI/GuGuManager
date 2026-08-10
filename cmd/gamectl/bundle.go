package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/lib/pq"

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
	Image             string            `json:"image"`     // 运行时镜像（含 digest）
	Command           json.RawMessage   `json:"command"`   // 启动命令模板
	Variables         json.RawMessage   `json:"variables"` // schema + secrets + bindings
	Stop              json.RawMessage   `json:"stop"`
	Health            json.RawMessage   `json:"health"`
	Ports             json.RawMessage   `json:"ports"`
	Install           json.RawMessage   `json:"install"`
	Lifecycle         json.RawMessage   `json:"lifecycle"`
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
			Image   string          `json:"image"`
			Command json.RawMessage `json:"command"`
			Ports   json.RawMessage `json:"ports"`
			Stop    json.RawMessage `json:"stop"`
			Health  json.RawMessage `json:"health"`
		} `json:"runtime"`
		Install   json.RawMessage `json:"install"`
		Lifecycle json.RawMessage `json:"lifecycle"`
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
	if err := gamedefinition.ValidateV1Alpha1JSON(content); err != nil {
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
		Image:             definition.Spec.Runtime.Image,
		Command:           definition.Spec.Runtime.Command,
		Variables:         definition.Spec.Variables,
		Stop:              definition.Spec.Runtime.Stop,
		Health:            definition.Spec.Runtime.Health,
		Ports:             definition.Spec.Runtime.Ports,
		Install:           definition.Spec.Install,
		Lifecycle:         definition.Spec.Lifecycle,
	}
	canonical, err := json.Marshal(bundle)
	if err != nil {
		return nil, fmt.Errorf("canonicalize bundle: %w", err)
	}
	sum := sha256.Sum256(canonical)
	bundle.Digest = "sha256:" + hex.EncodeToString(sum[:])
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
	flags := flag.NewFlagSet("bundle", flag.ExitOnError)
	_ = flags.Parse(args)
	remaining := flags.Args()
	if len(remaining) == 0 {
		bundleUsage()
		os.Exit(2)
	}
	switch remaining[0] {
	case "build":
		buildCommand(remaining[1:])
	case "publish":
		publishCommand(remaining[1:])
	default:
		bundleUsage()
		os.Exit(2)
	}
}

func buildCommand(args []string) {
	if len(args) != 1 {
		bundleUsage()
		os.Exit(2)
	}
	bundle, err := buildBundle(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "bundle build failed: %v\n", err)
		os.Exit(1)
	}
	output, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "bundle build failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(output))
}

func publishCommand(args []string) {
	if len(args) != 1 {
		bundleUsage()
		os.Exit(2)
	}
	content, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "bundle publish failed: %v\n", err)
		os.Exit(1)
	}
	var bundle Bundle
	if err := json.Unmarshal(content, &bundle); err != nil {
		fmt.Fprintf(os.Stderr, "bundle publish failed: %v\n", err)
		os.Exit(1)
	}
	if !isBundleDigest(bundle.Digest) {
		fmt.Fprintf(os.Stderr, "bundle publish failed: digest %q is not a sha256:<64 lowercase hex> value\n", bundle.Digest)
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
	source := "file://" + filepath.ToSlash(args[0])
	if err := publishBundle(db, bundle, source); err != nil {
		fmt.Fprintf(os.Stderr, "bundle publish failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("published", bundle.Digest, "as", bundle.GameDefinitionID, bundle.DefinitionVersion)
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
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO game_bundles (
			game_definition_id, definition_version, game_version, digest,
			schema_version, license, compatibility, published_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, now())
		ON CONFLICT (game_definition_id, definition_version) DO UPDATE
		SET game_version = EXCLUDED.game_version,
		    digest = EXCLUDED.digest,
		    license = EXCLUDED.license,
		    compatibility = EXCLUDED.compatibility,
		    published_at = now()
	`, bundle.GameDefinitionID, bundle.DefinitionVersion, bundle.GameVersion, bundle.Digest,
		bundle.SchemaVersion, bundle.License, string(compatibility)); err != nil {
		return fmt.Errorf("upsert game_bundles: %w", err)
	}

	return tx.Commit()
}

func bundleUsage() {
	fmt.Println("gamectl bundle build <definition.json>")
	fmt.Println("gamectl bundle publish <bundle.json>")
}
