package gamedefinition

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestNormalizeV1Alpha1ToV1Beta1IsDeterministic(t *testing.T) {
	content, err := schemaFiles.ReadFile("examples/papermc.json")
	if err != nil {
		t.Fatal(err)
	}
	first, err := NormalizeToV1Beta1JSON(content)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	second, err := NormalizeToV1Beta1JSON(content)
	if err != nil {
		t.Fatalf("normalize again: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("v1alpha1 conversion is not deterministic")
	}
	if err := ValidateV1Beta1JSON(first); err != nil {
		t.Fatalf("converted v1beta1: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(first, &document); err != nil {
		t.Fatal(err)
	}
	if document["apiVersion"] != APIVersionV1Beta1 {
		t.Fatalf("apiVersion = %v", document["apiVersion"])
	}
}

func TestV1Beta1RequiresArtifactManifestCoverage(t *testing.T) {
	content, err := schemaFiles.ReadFile("examples/papermc.json")
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := NormalizeToV1Beta1JSON(content)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(normalized, &document); err != nil {
		t.Fatal(err)
	}
	spec := document["spec"].(map[string]any)
	spec["install"] = map[string]any{"artifacts": []any{map[string]any{
		"url": "https://downloads.example.invalid/server.jar", "destination": "server.jar",
		"sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}}
	spec["bundle"].(map[string]any)["artifacts"] = []any{}
	tampered, _ := json.Marshal(document)
	if err := ValidateV1Beta1JSON(tampered); err == nil {
		t.Fatal("v1beta1 accepted an install artifact missing from its manifest")
	}
}
