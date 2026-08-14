package gamedefinition

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

type canonicalBundle struct {
	GameDefinitionID  string            `json:"gameDefinitionId"`
	DefinitionVersion string            `json:"definitionVersion"`
	GameVersion       string            `json:"gameVersion"`
	Digest            string            `json:"digest"`
	SchemaVersion     string            `json:"schemaVersion"`
	License           string            `json:"license"`
	Compatibility     map[string]string `json:"compatibility"`
	Capabilities      []string          `json:"capabilities"`
	Adapter           string            `json:"adapter"`
	Image             string            `json:"image"`
	User              string            `json:"user"`
	WorkingDir        string            `json:"workingDir"`
	Command           json.RawMessage   `json:"command"`
	Environment       json.RawMessage   `json:"environment"`
	DataMounts        json.RawMessage   `json:"dataMounts"`
	Console           json.RawMessage   `json:"console"`
	Variables         json.RawMessage   `json:"variables"`
	Stop              json.RawMessage   `json:"stop"`
	Health            json.RawMessage   `json:"health"`
	Ports             json.RawMessage   `json:"ports"`
	Install           json.RawMessage   `json:"install"`
	Lifecycle         json.RawMessage   `json:"lifecycle"`
}

type canonicalBundleSource struct {
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
			Adapter     string          `json:"adapter"`
			Image       string          `json:"image"`
			User        string          `json:"user"`
			WorkingDir  string          `json:"workingDir"`
			Command     json.RawMessage `json:"command"`
			Environment json.RawMessage `json:"environment"`
			DataMounts  json.RawMessage `json:"dataMounts"`
			Console     json.RawMessage `json:"console"`
			Ports       json.RawMessage `json:"ports"`
			Stop        json.RawMessage `json:"stop"`
			Health      json.RawMessage `json:"health"`
		} `json:"runtime"`
		Install   json.RawMessage `json:"install"`
		Lifecycle json.RawMessage `json:"lifecycle"`
	} `json:"spec"`
}

// CanonicalBundleDigest computes the digest written by gamectl bundle build.
// It intentionally covers executable Bundle content and ignores source-file
// whitespace, so the embedded catalog and a published bundle share identity.
func CanonicalBundleDigest(content []byte) (string, error) {
	if err := ValidateV1Alpha1JSON(content); err != nil {
		return "", err
	}
	var source canonicalBundleSource
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&source); err != nil {
		return "", err
	}
	compatibility, err := canonicalCompatibilityStrings(source.Spec.Compatibility)
	if err != nil {
		return "", err
	}
	bundle := canonicalBundle{
		GameDefinitionID: source.Metadata.ID, DefinitionVersion: source.Metadata.Version,
		GameVersion: source.Spec.Release.Version, SchemaVersion: source.APIVersion,
		License: source.Metadata.License, Compatibility: compatibility,
		Capabilities: source.Spec.Capabilities, Adapter: source.Spec.Runtime.Adapter,
		Image: source.Spec.Runtime.Image, User: source.Spec.Runtime.User,
		WorkingDir: source.Spec.Runtime.WorkingDir, Command: source.Spec.Runtime.Command,
		Environment: source.Spec.Runtime.Environment, DataMounts: source.Spec.Runtime.DataMounts,
		Console: source.Spec.Runtime.Console, Variables: source.Spec.Variables,
		Stop: source.Spec.Runtime.Stop, Health: source.Spec.Runtime.Health,
		Ports: source.Spec.Runtime.Ports, Install: source.Spec.Install,
		Lifecycle: source.Spec.Lifecycle,
	}
	canonical, err := json.Marshal(bundle)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func canonicalCompatibilityStrings(raw map[string]json.RawMessage) (map[string]string, error) {
	result := make(map[string]string, len(raw))
	for key, value := range raw {
		var decoded any
		decoder := json.NewDecoder(bytes.NewReader(value))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil {
			return nil, fmt.Errorf("decode spec.compatibility.%s: %w", key, err)
		}
		switch typed := decoded.(type) {
		case string:
			result[key] = typed
		case []any:
			parts := make([]string, 0, len(typed))
			for _, item := range typed {
				parts = append(parts, fmt.Sprint(item))
			}
			result[key] = joinComma(parts)
		default:
			return nil, fmt.Errorf("spec.compatibility.%s has unsupported type %T", key, decoded)
		}
	}
	return result, nil
}

func joinComma(values []string) string {
	var buffer bytes.Buffer
	for index, value := range values {
		if index > 0 {
			buffer.WriteByte(',')
		}
		buffer.WriteString(value)
	}
	return buffer.String()
}
