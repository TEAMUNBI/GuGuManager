package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBundleBuildStableDigest(t *testing.T) {
	first, err := buildBundle(examplePath())
	if err != nil {
		t.Fatalf("first buildBundle returned error: %v", err)
	}
	second, err := buildBundle(examplePath())
	if err != nil {
		t.Fatalf("second buildBundle returned error: %v", err)
	}
	if first.Digest == "" {
		t.Fatal("bundle digest is empty")
	}
	if first.Digest != second.Digest {
		t.Fatalf("digest is not stable: first %q, second %q", first.Digest, second.Digest)
	}
	if first.GameDefinitionID != "io.gugumanager.papermc" {
		t.Fatalf("gameDefinitionId = %q, want io.gugumanager.papermc", first.GameDefinitionID)
	}
	if first.GameVersion != "1.21.8" {
		t.Fatalf("gameVersion = %q, want 1.21.8", first.GameVersion)
	}
}

func TestBundleBuildContainsDigest(t *testing.T) {
	bundle, err := buildBundle(examplePath())
	if err != nil {
		t.Fatalf("buildBundle returned error: %v", err)
	}
	output, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("Marshal bundle returned error: %v", err)
	}
	if !strings.Contains(string(output), "sha256:") {
		t.Fatalf("bundle output %s does not contain a sha256: digest", output)
	}
	if !strings.HasPrefix(bundle.Digest, "sha256:") {
		t.Fatalf("bundle digest %q does not start with sha256:", bundle.Digest)
	}
	if len(bundle.Digest) != len("sha256:")+64 {
		t.Fatalf("bundle digest %q has length %d, want %d", bundle.Digest, len(bundle.Digest), len("sha256:")+64)
	}
}

func TestBundleBuildCommand(t *testing.T) {
	bundle, err := buildBundle(examplePath())
	if err != nil {
		t.Fatalf("buildBundle returned error: %v", err)
	}
	var command struct {
		Executable string   `json:"executable"`
		Args       []string `json:"args"`
	}
	if err := json.Unmarshal(bundle.Command, &command); err != nil {
		t.Fatalf("unmarshal bundle command: %v", err)
	}
	if command.Executable == "" {
		t.Fatal("bundle command executable is empty")
	}
	var ports []map[string]any
	if err := json.Unmarshal(bundle.Ports, &ports); err != nil {
		t.Fatalf("unmarshal bundle ports: %v", err)
	}
	if len(ports) == 0 {
		t.Fatal("bundle ports are empty")
	}
	if len(bundle.Variables) == 0 {
		t.Fatal("bundle variables are empty")
	}
}
