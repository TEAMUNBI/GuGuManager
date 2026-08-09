// Package gamedefinition exposes the canonical GameDefinition schemas to Go tools.
package gamedefinition

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const v1Alpha1SchemaURL = "https://gugumanager.io/schemas/game-definition/v1alpha1.schema.json"

// schemaFiles keeps validation independent of the process working directory.
//
//go:embed v1alpha1.schema.json examples/*.json
var schemaFiles embed.FS

var compiledV1Alpha1Schema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	content, err := V1Alpha1Schema()
	if err != nil {
		return nil, fmt.Errorf("read embedded GameDefinition schema: %w", err)
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode embedded GameDefinition schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource(v1Alpha1SchemaURL, document); err != nil {
		return nil, fmt.Errorf("load GameDefinition schema: %w", err)
	}
	schema, err := compiler.Compile(v1Alpha1SchemaURL)
	if err != nil {
		return nil, fmt.Errorf("compile GameDefinition schema: %w", err)
	}
	return schema, nil
})

// FixedBundle is one immutable GameDefinition document bundled into the
// development control plane.
type FixedBundle struct {
	Filename string
	Document []byte
}

// V1Alpha1Schema returns the canonical v1alpha1 JSON Schema.
func V1Alpha1Schema() ([]byte, error) {
	return schemaFiles.ReadFile("v1alpha1.schema.json")
}

// ValidateV1Alpha1 validates an already decoded document against the canonical
// v1alpha1 JSON Schema. JSON numbers should be decoded with json.Decoder.UseNumber.
func ValidateV1Alpha1(document any) error {
	schema, err := compiledV1Alpha1Schema()
	if err != nil {
		return err
	}
	return schema.Validate(document)
}

// ValidateV1Alpha1JSON decodes exactly one JSON value without losing number
// precision, then validates the complete document against the canonical Schema.
func ValidateV1Alpha1JSON(content []byte) error {
	var document any
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return err
	}
	return ValidateV1Alpha1(document)
}

// FixedBundles returns defensive copies of the built-in development bundles.
func FixedBundles() ([]FixedBundle, error) {
	entries, err := schemaFiles.ReadDir("examples")
	if err != nil {
		return nil, fmt.Errorf("read embedded GameDefinition examples: %w", err)
	}
	bundles := make([]FixedBundle, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		document, err := schemaFiles.ReadFile("examples/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read embedded GameDefinition %s: %w", entry.Name(), err)
		}
		bundles = append(bundles, FixedBundle{Filename: entry.Name(), Document: append([]byte(nil), document...)})
	}
	return bundles, nil
}
