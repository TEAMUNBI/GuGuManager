package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLintRejectsSchemaInvalidDefinitions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "missing required runtime adapter",
			mutate: func(definition map[string]any) {
				delete(runtimeObject(t, definition), "adapter")
			},
		},
		{
			name: "unknown root property",
			mutate: func(definition map[string]any) {
				definition["unexpected"] = true
			},
		},
		{
			name: "unsupported health check type",
			mutate: func(definition map[string]any) {
				runtimeObject(t, definition)["health"].(map[string]any)["type"] = "http"
			},
		},
		{
			name: "unknown nested command property",
			mutate: func(definition map[string]any) {
				runtimeObject(t, definition)["command"].(map[string]any)["shell"] = true
			},
		},
		{
			name: "unsupported variable binding target",
			mutate: func(definition map[string]any) {
				specObject(t, definition)["variables"].(map[string]any)["bindings"] = []any{map[string]any{
					"variable": "memory_mb",
					"target":   "hostPath",
					"template": "{{ value }}",
				}}
			},
		},
		{
			name: "artifact without immutable digest",
			mutate: func(definition map[string]any) {
				specObject(t, definition)["install"] = map[string]any{
					"artifacts": []any{map[string]any{
						"url":         "https://downloads.example.invalid/server.jar",
						"destination": "server.jar",
					}},
				}
			},
		},
		{
			name: "semver core number with a leading zero",
			mutate: func(definition map[string]any) {
				definition["metadata"].(map[string]any)["version"] = "01.0.0"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := exampleDefinition(t)
			test.mutate(definition)

			err := lint(writeDefinition(t, definition))
			if err == nil {
				t.Fatal("lint accepted a definition that violates the GameDefinition schema")
			}
			if !strings.Contains(err.Error(), "schema validation failed") {
				t.Fatalf("lint error = %q, want a schema validation error", err)
			}
		})
	}
}

func TestLintAcceptsSemVerPrereleaseAndBuildMetadata(t *testing.T) {
	definition := exampleDefinition(t)
	definition["metadata"].(map[string]any)["version"] = "1.0.0-rc.1+build.2"
	if err := lint(writeDefinition(t, definition)); err != nil {
		t.Fatalf("lint rejected a valid SemVer 2.0 version: %v", err)
	}
}

func TestLintAcceptsProcessHealthWithoutPortReference(t *testing.T) {
	definition := exampleDefinition(t)
	health := runtimeObject(t, definition)["health"].(map[string]any)
	health["type"] = "process"
	delete(health, "portRef")
	if err := lint(writeDefinition(t, definition)); err != nil {
		t.Fatalf("lint rejected a process health check without a port: %v", err)
	}
}

func TestLintAcceptsAnExplicitUpstreamGameVersion(t *testing.T) {
	definition := exampleDefinition(t)
	specObject(t, definition)["release"] = map[string]any{"version": "1.21.8-build.42"}
	if err := lint(writeDefinition(t, definition)); err != nil {
		t.Fatalf("lint rejected an explicit upstream game version: %v", err)
	}
}

func TestLintRejectsMissingOrFloatingUpstreamGameVersion(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing", mutate: func(spec map[string]any) { delete(spec, "release") }},
		{name: "floating latest", mutate: func(spec map[string]any) {
			spec["release"] = map[string]any{"version": "latest"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := exampleDefinition(t)
			test.mutate(specObject(t, definition))
			if err := lint(writeDefinition(t, definition)); err == nil {
				t.Fatal("lint accepted an unresolved upstream game version")
			}
		})
	}
}

func TestLintRejectsTrailingJSONValue(t *testing.T) {
	content, err := os.ReadFile(examplePath())
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "definition.json")
	content = append(content, []byte("\n{}\n")...)
	if err := os.WriteFile(filename, content, 0o600); err != nil {
		t.Fatal(err)
	}

	err = lint(filename)
	if err == nil {
		t.Fatal("lint accepted a trailing JSON value")
	}
	if !strings.Contains(err.Error(), "trailing JSON") {
		t.Fatalf("lint error = %q, want a trailing JSON error", err)
	}
}

func TestLintRejectsHealthPortReferenceToUndeclaredPort(t *testing.T) {
	definition := exampleDefinition(t)
	runtimeObject(t, definition)["health"].(map[string]any)["portRef"] = "query"

	err := lint(writeDefinition(t, definition))
	if err == nil {
		t.Fatal("lint accepted health.portRef referencing an undeclared port")
	}
	if !strings.Contains(err.Error(), "does not reference a declared port") {
		t.Fatalf("lint error = %q, want an undeclared port reference error", err)
	}
}

func TestLintRejectsUnsafeArtifactDestination(t *testing.T) {
	unsafePaths := []string{
		"../server.jar",
		"/etc/passwd",
		`C:\Windows\win.ini`,
		`\\server\share\server.jar`,
		"mods/../../server.jar",
		"bad\x00name",
		".",
		`mods\plugin.jar`,
		"./mods/plugin.jar",
		"mods//plugin.jar",
		strings.Repeat("a", 256) + "/server.jar",
		strings.Repeat("a/", 513) + "server.jar",
	}
	for _, destination := range unsafePaths {
		t.Run(destination, func(t *testing.T) {
			definition := exampleDefinition(t)
			specObject(t, definition)["install"] = map[string]any{
				"artifacts": []any{map[string]any{
					"url":         "https://downloads.example.invalid/server.jar",
					"destination": destination,
					"sha256":      strings.Repeat("a", 64),
				}},
			}

			err := lint(writeDefinition(t, definition))
			if err == nil {
				t.Fatalf("lint accepted unsafe install artifact destination %q", destination)
			}
			if !strings.Contains(err.Error(), "install.artifacts[0].destination") {
				t.Fatalf("lint error = %q, want indexed artifact destination context", err)
			}
		})
	}
}

func TestLintRejectsUnsafeFileBindingPath(t *testing.T) {
	unsafePaths := []string{
		"../eula.txt",
		"/etc/passwd",
		`C:\Windows\win.ini`,
		`\\server\share\eula.txt`,
		"config/../../eula.txt",
		"bad\x00name",
		".",
		`config\server.properties`,
		"./config/server.properties",
		"config//server.properties",
		strings.Repeat("a", 256) + "/eula.txt",
	}
	for _, bindingPath := range unsafePaths {
		t.Run(bindingPath, func(t *testing.T) {
			definition := exampleDefinition(t)
			variables := specObject(t, definition)["variables"].(map[string]any)
			variables["bindings"] = []any{map[string]any{
				"variable": "accept_eula",
				"target":   "file",
				"path":     bindingPath,
				"template": "eula={{ value }}\n",
			}}

			err := lint(writeDefinition(t, definition))
			if err == nil {
				t.Fatalf("lint accepted unsafe file binding path %q", bindingPath)
			}
			if !strings.Contains(err.Error(), "variables.bindings[0].path") {
				t.Fatalf("lint error = %q, want indexed file binding path context", err)
			}
		})
	}
}

func TestLintRejectsDuplicateArtifactDestination(t *testing.T) {
	definition := exampleDefinition(t)
	specObject(t, definition)["install"] = map[string]any{
		"artifacts": []any{
			map[string]any{"url": "https://downloads.example.invalid/server.jar", "destination": "mods/server.jar", "sha256": strings.Repeat("a", 64)},
			map[string]any{"url": "https://mirror.example.invalid/server.jar", "destination": "mods/server.jar", "sha256": strings.Repeat("b", 64)},
		},
	}

	err := lint(writeDefinition(t, definition))
	if err == nil {
		t.Fatal("lint accepted duplicate install artifact destinations")
	}
	if !strings.Contains(err.Error(), "install.artifacts[1].destination") || !strings.Contains(err.Error(), "duplicates") {
		t.Fatalf("lint error = %q, want duplicate destination with indexed context", err)
	}
}

func TestLintAcceptsCanonicalBundleTargetPaths(t *testing.T) {
	definition := exampleDefinition(t)
	spec := specObject(t, definition)
	spec["install"] = map[string]any{
		"artifacts": []any{
			map[string]any{"url": "https://downloads.example.invalid/paper.jar", "destination": "paper.jar", "sha256": strings.Repeat("a", 64)},
			map[string]any{"url": "https://downloads.example.invalid/plugin.jar", "destination": "mods/plugin.jar", "sha256": strings.Repeat("b", 64)},
		},
	}
	variables := spec["variables"].(map[string]any)
	runtime := spec["runtime"].(map[string]any)
	runtime["command"].(map[string]any)["args"] = []any{"{{ memory_mb }}"}
	variables["bindings"] = []any{
		map[string]any{"variable": "memory_mb", "target": "argument", "template": "-Xmx{{ value }}M"},
		map[string]any{"variable": "accept_eula", "target": "file", "path": "config/server.properties", "template": "eula={{ value }}\n"},
	}

	if err := lint(writeDefinition(t, definition)); err != nil {
		t.Fatalf("lint rejected canonical Bundle target paths: %v", err)
	}
}

func TestLintRejectsArgumentBindingTemplateWithoutValuePlaceholder(t *testing.T) {
	templates := []string{
		"",
		"-Xmx{{ memory_mb }}M",
		"-Xmx{{value}}M",
	}
	for _, template := range templates {
		t.Run(template, func(t *testing.T) {
			definition := exampleDefinition(t)
			specObject(t, definition)["runtime"].(map[string]any)["command"].(map[string]any)["args"] = []any{"{{ memory_mb }}"}
			variables := specObject(t, definition)["variables"].(map[string]any)
			variables["bindings"] = []any{map[string]any{
				"variable": "memory_mb",
				"target":   "argument",
				"template": template,
			}}

			err := lint(writeDefinition(t, definition))
			if err == nil {
				t.Fatalf("lint accepted argument binding template %q without the exact {{ value }} placeholder", template)
			}
			if !strings.Contains(err.Error(), "variables.bindings[0].template") || !strings.Contains(err.Error(), "{{ value }}") {
				t.Fatalf("lint error = %q, want indexed template and exact placeholder context", err)
			}
		})
	}
}

func TestLintAcceptsArgumentBindingTemplateWithValuePlaceholder(t *testing.T) {
	definition := exampleDefinition(t)
	specObject(t, definition)["runtime"].(map[string]any)["command"].(map[string]any)["args"] = []any{"{{ memory_mb }}"}
	variables := specObject(t, definition)["variables"].(map[string]any)
	variables["bindings"] = []any{map[string]any{
		"variable": "memory_mb",
		"target":   "argument",
		"template": "memory={{ value }}",
	}}

	if err := lint(writeDefinition(t, definition)); err != nil {
		t.Fatalf("lint rejected argument binding template with the exact {{ value }} placeholder: %v", err)
	}
}

func TestLintRejectsSecretReferencingUndeclaredVariable(t *testing.T) {
	definition := exampleDefinition(t)
	variables := specObject(t, definition)["variables"].(map[string]any)
	variables["secrets"] = []any{"missing_secret"}

	err := lint(writeDefinition(t, definition))
	if err == nil {
		t.Fatal("lint accepted a secret that does not reference a declared variable")
	}
	if !strings.Contains(err.Error(), "does not reference a declared variable") {
		t.Fatalf("lint error = %q, want an undeclared secret reference error", err)
	}
}

func TestLintRejectsSecretPropertyWithDefault(t *testing.T) {
	definition := exampleDefinition(t)
	variables := specObject(t, definition)["variables"].(map[string]any)
	properties := variables["schema"].(map[string]any)["properties"].(map[string]any)
	properties["rcon_password"] = map[string]any{
		"type":      "string",
		"minLength": float64(8),
		"default":   "must-not-be-stored-in-the-bundle",
	}
	variables["secrets"] = []any{"rcon_password"}

	err := lint(writeDefinition(t, definition))
	if err == nil {
		t.Fatal("lint accepted a secret variable with a default")
	}
	if !strings.Contains(err.Error(), "must not declare a default") {
		t.Fatalf("lint error = %q, want a secret default error", err)
	}
}

func TestLintRejectsVariableSchemasOutsideStartupSubset(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any, map[string]any)
	}{
		{name: "schema type is not object", mutate: func(schema, _ map[string]any) { schema["type"] = "array" }},
		{name: "schema omits properties", mutate: func(schema, _ map[string]any) { delete(schema, "properties") }},
		{name: "schema composition keyword", mutate: func(schema, _ map[string]any) { schema["oneOf"] = []any{} }},
		{name: "property omits type", mutate: func(_, properties map[string]any) {
			properties["custom_name"] = map[string]any{"default": "Friday"}
		}},
		{name: "unsupported number property", mutate: func(_, properties map[string]any) {
			properties["ratio"] = map[string]any{"type": "number"}
		}},
		{name: "unsupported array property", mutate: func(_, properties map[string]any) {
			properties["mods"] = map[string]any{"type": "array"}
		}},
		{name: "unsupported object property", mutate: func(_, properties map[string]any) {
			properties["settings"] = map[string]any{"type": "object"}
		}},
		{name: "unsupported union property", mutate: func(_, properties map[string]any) {
			properties["optional_name"] = map[string]any{"type": []any{"string", "null"}}
		}},
		{name: "unsupported string pattern", mutate: func(_, properties map[string]any) {
			properties["custom_name"] = map[string]any{"type": "string", "pattern": "^safe$"}
		}},
		{name: "unsupported string format", mutate: func(_, properties map[string]any) {
			properties["contact"] = map[string]any{"type": "string", "format": "email"}
		}},
		{name: "numeric enum", mutate: func(_, properties map[string]any) {
			properties["memory_mb"] = map[string]any{"type": "integer", "enum": []any{1024, 2048}}
		}},
		{name: "minimum on string", mutate: func(_, properties map[string]any) {
			properties["custom_name"] = map[string]any{"type": "string", "minimum": 1}
		}},
		{name: "minLength on integer", mutate: func(_, properties map[string]any) {
			properties["memory_mb"] = map[string]any{"type": "integer", "minLength": 1}
		}},
		{name: "enum on boolean", mutate: func(_, properties map[string]any) {
			properties["accept_eula"] = map[string]any{"type": "boolean", "enum": []any{true}}
		}},
		{name: "negative string length", mutate: func(_, properties map[string]any) {
			properties["custom_name"] = map[string]any{"type": "string", "minLength": -1}
		}},
		{name: "empty string enum", mutate: func(_, properties map[string]any) {
			properties["difficulty"] = map[string]any{"type": "string", "enum": []any{}}
		}},
		{name: "duplicate string enum", mutate: func(_, properties map[string]any) {
			properties["difficulty"] = map[string]any{"type": "string", "enum": []any{"normal", "normal"}}
		}},
		{name: "invalid property identifier", mutate: func(_, properties map[string]any) {
			properties["server-name"] = map[string]any{"type": "string"}
		}},
		{name: "duplicate required entry", mutate: func(schema, _ map[string]any) {
			schema["required"] = []any{"memory_mb", "memory_mb"}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := exampleDefinition(t)
			schema := variableSchemaObject(t, definition)
			properties := variablePropertiesObject(t, definition)
			test.mutate(schema, properties)

			if err := lint(writeDefinition(t, definition)); err == nil {
				t.Fatal("lint accepted a variable schema outside the executable Startup subset")
			}
		})
	}
}

func TestLintRejectsInvalidVariableDefaultsAndConstants(t *testing.T) {
	tests := []struct {
		name     string
		property map[string]any
	}{
		{name: "integer default has wrong type", property: map[string]any{"type": "integer", "default": "2048"}},
		{name: "integer default below minimum", property: map[string]any{"type": "integer", "default": 1, "minimum": 2}},
		{name: "integer range is contradictory", property: map[string]any{"type": "integer", "minimum": 4, "maximum": 3}},
		{name: "string default is shorter than minimum", property: map[string]any{"type": "string", "default": "a", "minLength": 2}},
		{name: "string range is contradictory", property: map[string]any{"type": "string", "minLength": 3, "maxLength": 2}},
		{name: "default is outside enum", property: map[string]any{"type": "string", "default": "hard", "enum": []any{"normal"}}},
		{name: "const is outside enum", property: map[string]any{"type": "string", "const": "hard", "enum": []any{"normal"}}},
		{name: "default differs from const", property: map[string]any{"type": "string", "default": "normal", "const": "hard"}},
		{name: "default exceeds safe integer domain", property: map[string]any{"type": "integer", "default": json.Number("9007199254740992")}},
		{name: "maximum exceeds safe integer domain", property: map[string]any{"type": "integer", "maximum": json.Number("9007199254740992")}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := exampleDefinition(t)
			variablePropertiesObject(t, definition)["contract_probe"] = test.property
			if err := lint(writeDefinition(t, definition)); err == nil {
				t.Fatal("lint accepted an invalid Startup default or const")
			}
		})
	}
}

func TestLintRejectsSecretConstAndEnum(t *testing.T) {
	tests := []struct {
		name     string
		property map[string]any
		keyword  string
	}{
		{
			name:     "const",
			property: map[string]any{"type": "string", "const": "must-not-be-stored-in-the-bundle"},
			keyword:  "const",
		},
		{
			name:     "enum",
			property: map[string]any{"type": "string", "enum": []any{"candidate-one", "candidate-two"}},
			keyword:  "enum",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := exampleDefinition(t)
			variables := specObject(t, definition)["variables"].(map[string]any)
			variablePropertiesObject(t, definition)["rcon_password"] = test.property
			variables["secrets"] = []any{"rcon_password"}

			err := lint(writeDefinition(t, definition))
			if err == nil {
				t.Fatalf("lint accepted Secret %s material", test.keyword)
			}
			if !strings.Contains(err.Error(), "secret variable") || !strings.Contains(err.Error(), test.keyword) {
				t.Fatalf("lint error = %q, want Secret %s context", err, test.keyword)
			}
		})
	}
}

func TestLintRejectsRequiredVariableReferenceToUnknownProperty(t *testing.T) {
	definition := exampleDefinition(t)
	variables := specObject(t, definition)["variables"].(map[string]any)
	variables["schema"].(map[string]any)["required"] = []any{"unknown_required"}

	err := lint(writeDefinition(t, definition))
	if err == nil {
		t.Fatal("lint accepted a required variable that is not declared in properties")
	}
	if !strings.Contains(err.Error(), "required entry") {
		t.Fatalf("lint error = %q, want an undeclared required reference error", err)
	}
}

func TestInitTemplatePassesLint(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "new-game")
	command := exec.Command(os.Args[0], "-test.run=^TestGamectlInitHelperProcess$", "--", "--dir", destination)
	command.Env = append(os.Environ(), "GO_WANT_GAMECTL_INIT_HELPER=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("gamectl init failed: %v\n%s", err, output)
	}

	filename := filepath.Join(destination, "definition.json")
	if err := lint(filename); err != nil {
		t.Fatalf("gamectl init generated a definition that fails lint: %v", err)
	}
}

func TestGamectlInitHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_GAMECTL_INIT_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			initCommand(os.Args[i+1:])
			return
		}
	}
	t.Fatal("missing helper argument separator")
}

func exampleDefinition(t *testing.T) map[string]any {
	t.Helper()
	content, err := os.ReadFile(examplePath())
	if err != nil {
		t.Fatal(err)
	}
	var definition map[string]any
	if err := json.Unmarshal(content, &definition); err != nil {
		t.Fatal(err)
	}
	return definition
}

func runtimeObject(t *testing.T, definition map[string]any) map[string]any {
	t.Helper()
	spec := specObject(t, definition)
	runtime, ok := spec["runtime"].(map[string]any)
	if !ok {
		t.Fatal("example runtime is not an object")
	}
	return runtime
}

func specObject(t *testing.T, definition map[string]any) map[string]any {
	t.Helper()
	spec, ok := definition["spec"].(map[string]any)
	if !ok {
		t.Fatal("example spec is not an object")
	}
	return spec
}

func variableSchemaObject(t *testing.T, definition map[string]any) map[string]any {
	t.Helper()
	variables, ok := specObject(t, definition)["variables"].(map[string]any)
	if !ok {
		t.Fatal("example variables is not an object")
	}
	schema, ok := variables["schema"].(map[string]any)
	if !ok {
		t.Fatal("example variables.schema is not an object")
	}
	return schema
}

func variablePropertiesObject(t *testing.T, definition map[string]any) map[string]any {
	t.Helper()
	properties, ok := variableSchemaObject(t, definition)["properties"].(map[string]any)
	if !ok {
		t.Fatal("example variables.schema.properties is not an object")
	}
	return properties
}

func writeDefinition(t *testing.T, definition map[string]any) string {
	t.Helper()
	content, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "definition.json")
	if err := os.WriteFile(filename, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return filename
}

func examplePath() string {
	return filepath.Join("..", "..", "spec", "game-definition", "examples", "papermc.json")
}
