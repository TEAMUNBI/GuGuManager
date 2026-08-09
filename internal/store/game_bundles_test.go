package store

import (
	"encoding/json"
	"testing"
)

func TestStartupFromFixedBundleRejectsVariableSchemaOutsideExecutableSubset(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "unsupported pattern is not silently ignored",
			mutate: func(properties map[string]any) {
				properties["server_name"].(map[string]any)["pattern"] = "^safe$"
			},
		},
		{
			name: "Secret const is rejected before materialization",
			mutate: func(properties map[string]any) {
				properties["server_token"].(map[string]any)["const"] = "bundle-embedded-secret"
			},
		},
		{
			name: "Secret enum candidates are rejected",
			mutate: func(properties map[string]any) {
				properties["server_token"].(map[string]any)["enum"] = []any{"candidate-one", "candidate-two"}
			},
		},
		{
			name: "unsafe integer default is rejected",
			mutate: func(properties map[string]any) {
				properties["autosave_interval"] = map[string]any{
					"type":    "integer",
					"default": json.Number("9007199254740992"),
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newTestMemory(0)
			server := service.servers[stoppedServerID]
			game := service.games[server.GameID]

			var document map[string]any
			decoderContent := []byte(game.BundleDocument)
			if err := json.Unmarshal(decoderContent, &document); err != nil {
				t.Fatal(err)
			}
			spec := document["spec"].(map[string]any)
			variables := spec["variables"].(map[string]any)
			properties := variables["schema"].(map[string]any)["properties"].(map[string]any)
			test.mutate(properties)

			content, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			game.BundleDocument = string(content)
			game.BundleDigest = bundleDigest(content)
			server.GameBundleDigest = game.BundleDigest

			_, _, err = startupFromFixedBundle(server, game, nil)
			requireProblemCode(t, err, "PACKAGE_INCOMPATIBLE")
		})
	}
}

func TestStartupFromFixedBundleRejectsMalformedVariablesContainer(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "variables is null", mutate: func(document map[string]any) {
			document["spec"].(map[string]any)["variables"] = nil
		}},
		{name: "variables object omits schema", mutate: func(document map[string]any) {
			variables := document["spec"].(map[string]any)["variables"].(map[string]any)
			delete(variables, "schema")
			delete(variables, "secrets")
		}},
		{name: "secrets is null", mutate: func(document map[string]any) {
			document["spec"].(map[string]any)["variables"].(map[string]any)["secrets"] = nil
		}},
		{name: "bindings is null", mutate: func(document map[string]any) {
			document["spec"].(map[string]any)["variables"].(map[string]any)["bindings"] = nil
		}},
		{name: "spec variables keyword has wrong case", mutate: func(document map[string]any) {
			spec := document["spec"].(map[string]any)
			spec["Variables"] = spec["variables"]
			delete(spec, "variables")
		}},
		{name: "spec variables keyword typo is not treated as absent", mutate: func(document map[string]any) {
			spec := document["spec"].(map[string]any)
			spec["variaables"] = spec["variables"]
			delete(spec, "variables")
		}},
		{name: "variables keyword has wrong case", mutate: func(document map[string]any) {
			variables := document["spec"].(map[string]any)["variables"].(map[string]any)
			variables["Schema"] = variables["schema"]
			delete(variables, "schema")
		}},
		{name: "variables object has unknown field", mutate: func(document map[string]any) {
			document["spec"].(map[string]any)["variables"].(map[string]any)["unexpected"] = true
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newTestMemory(0)
			server := service.servers[stoppedServerID]
			game := service.games[server.GameID]

			var document map[string]any
			if err := json.Unmarshal([]byte(game.BundleDocument), &document); err != nil {
				t.Fatal(err)
			}
			test.mutate(document)
			content, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			game.BundleDocument = string(content)
			game.BundleDigest = bundleDigest(content)
			server.GameBundleDigest = game.BundleDigest

			_, _, err = startupFromFixedBundle(server, game, nil)
			requireProblemCode(t, err, "PACKAGE_INCOMPATIBLE")
		})
	}
}

func TestStartupFromFixedBundleAcceptsOmittedVariablesContainer(t *testing.T) {
	service := newTestMemory(0)
	server := service.servers[stoppedServerID]
	game := service.games[server.GameID]

	var document map[string]any
	if err := json.Unmarshal([]byte(game.BundleDocument), &document); err != nil {
		t.Fatal(err)
	}
	delete(document["spec"].(map[string]any), "variables")
	content, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	game.BundleDocument = string(content)
	game.BundleDigest = bundleDigest(content)
	server.GameBundleDigest = game.BundleDigest

	startup, values, err := startupFromFixedBundle(server, game, nil)
	if err != nil {
		t.Fatalf("omitted optional variables container was rejected: %v", err)
	}
	if len(startup.Variables) != 0 || len(startup.Bindings) != 0 || len(values) != 0 {
		t.Fatalf("omitted variables container materialized declarations: startup=%+v values=%v", startup, values)
	}
}

func TestStartupFromFixedBundleRejectsMalformedVariableBindings(t *testing.T) {
	tests := []struct {
		name    string
		binding map[string]any
	}{
		{name: "unknown field", binding: map[string]any{"variable": "memory_mb", "target": "argument", "template": "-Xmx{{ value }}M", "unexpected": true}},
		{name: "invalid target", binding: map[string]any{"variable": "memory_mb", "target": "hostPath", "template": "{{ value }}"}},
		{name: "environment missing name", binding: map[string]any{"variable": "memory_mb", "target": "environment", "template": "{{ value }}"}},
		{name: "file missing path", binding: map[string]any{"variable": "accept_eula", "target": "file", "template": "eula={{ value }}\n"}},
		{name: "unsafe file path", binding: map[string]any{"variable": "accept_eula", "target": "file", "path": "../eula.txt", "template": "eula={{ value }}\n"}},
		{name: "null template", binding: map[string]any{"variable": "memory_mb", "target": "environment", "name": "MEMORY_MB", "template": nil}},
		{name: "wrong member case", binding: map[string]any{"Variable": "memory_mb", "target": "argument", "template": "-Xmx{{ value }}M"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newTestMemory(0)
			server := service.servers[runningServerID]
			game := service.games[server.GameID]

			var document map[string]any
			if err := json.Unmarshal([]byte(game.BundleDocument), &document); err != nil {
				t.Fatal(err)
			}
			variables := document["spec"].(map[string]any)["variables"].(map[string]any)
			variables["bindings"] = []any{test.binding}
			content, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			game.BundleDocument = string(content)
			game.BundleDigest = bundleDigest(content)
			server.GameBundleDigest = game.BundleDigest

			_, _, err = startupFromFixedBundle(server, game, nil)
			requireProblemCode(t, err, "PACKAGE_INCOMPATIBLE")
		})
	}
}
