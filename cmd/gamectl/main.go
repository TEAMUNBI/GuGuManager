package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	serverfiles "github.com/gugumanager/gugumanager/internal/files"
	"github.com/gugumanager/gugumanager/internal/install"
	gamedefinition "github.com/gugumanager/gugumanager/spec/game-definition"
)

type gameDefinition struct {
	Spec struct {
		Variables struct {
			Secrets  []string        `json:"secrets"`
			Schema   json.RawMessage `json:"schema"`
			Bindings []struct {
				Variable string `json:"variable"`
				Target   string `json:"target"`
				Path     string `json:"path"`
				Template string `json:"template"`
			} `json:"bindings"`
		} `json:"variables"`
		Install struct {
			Artifacts []struct {
				URL         string `json:"url"`
				Destination string `json:"destination"`
				SHA256      string `json:"sha256"`
			} `json:"artifacts"`
			NetworkAllowlist []string `json:"networkAllowlist"`
		} `json:"install"`
		Lifecycle struct {
			Install string `json:"install"`
		} `json:"lifecycle"`
		Runtime struct {
			Command struct {
				Args []string `json:"args"`
			} `json:"command"`
			Ports []struct {
				Name string `json:"name"`
			} `json:"ports"`
			Health struct {
				Type    string `json:"type"`
				PortRef string `json:"portRef"`
			} `json:"health"`
		} `json:"runtime"`
	} `json:"spec"`
}

const initialGameDefinition = `{
  "apiVersion": "gugumanager.io/games/v1alpha1",
  "kind": "GameDefinition",
  "metadata": {
    "id": "io.example.game",
    "name": "Example Game",
    "version": "0.1.0",
    "license": "Apache-2.0"
  },
  "spec": {
    "release": {"version": "1.0.0"},
    "compatibility": {
      "panel": ">=0.1 <1.0",
      "agent": ">=0.1 <1.0",
      "platforms": ["linux/amd64"]
    },
    "capabilities": ["console"],
    "runtime": {
      "adapter": "container/v1",
      "image": "registry.invalid/example@sha256:0000000000000000000000000000000000000000000000000000000000000000",
      "user": "1000:1000",
      "workingDir": "/srv/game",
      "command": {"executable": "/srv/game/server", "args": []},
      "dataMounts": [{"name": "server-data", "target": "/srv/game", "backup": true}],
      "ports": [{"name": "game", "protocol": "tcp", "containerPort": 25565, "role": "primary"}],
      "stop": {"method": "signal", "value": "SIGTERM", "timeoutSeconds": 30},
      "health": {"type": "tcp", "portRef": "game", "intervalSeconds": 10, "timeoutSeconds": 5, "failureThreshold": 6}
    },
    "lifecycle": {
      "install": "builtin.artifacts",
      "configure": "builtin.bindings"
    }
  }
}
`

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "lint":
		lintCommand(os.Args[2:])
	case "init":
		initCommand(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func lintCommand(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: gamectl lint <definition.json>")
		os.Exit(2)
	}
	if err := lint(args[0]); err != nil {
		fmt.Fprintf(os.Stderr, "lint failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("lint ok:", args[0])
}

func lint(filename string) error {
	if strings.ToLower(filepath.Ext(filename)) != ".json" {
		return errors.New("MVP lint accepts JSON definitions; YAML support is planned for the schema tool")
	}
	content, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("invalid JSON: trailing JSON value")
		}
		return fmt.Errorf("invalid JSON: trailing data: %w", err)
	}

	if err := gamedefinition.ValidateV1Alpha1(document); err != nil {
		return fmt.Errorf("schema validation failed: %w", err)
	}

	var definition gameDefinition
	if err := json.Unmarshal(content, &definition); err != nil {
		return fmt.Errorf("decode validated GameDefinition: %w", err)
	}
	ports := map[string]bool{}
	for _, port := range definition.Spec.Runtime.Ports {
		if ports[port.Name] {
			return fmt.Errorf("duplicate runtime port %q", port.Name)
		}
		ports[port.Name] = true
	}
	if definition.Spec.Runtime.Health.Type != "process" && (definition.Spec.Runtime.Health.PortRef == "" || !ports[definition.Spec.Runtime.Health.PortRef]) {
		return fmt.Errorf("runtime.health.portRef %q does not reference a declared port", definition.Spec.Runtime.Health.PortRef)
	}
	properties := map[string]gamedefinition.StartupVariableProperty{}
	if len(definition.Spec.Variables.Schema) != 0 {
		startupSchema, err := gamedefinition.DecodeStartupVariableSchema(definition.Spec.Variables.Schema)
		if err != nil {
			return err
		}
		if err := startupSchema.Validate(definition.Spec.Variables.Secrets); err != nil {
			return err
		}
		properties = startupSchema.Properties
	}
	secretSet := make(map[string]bool, len(definition.Spec.Variables.Secrets))
	for _, secret := range definition.Spec.Variables.Secrets {
		secretSet[secret] = true
	}
	for index, binding := range definition.Spec.Variables.Bindings {
		if _, declared := properties[binding.Variable]; !declared {
			return fmt.Errorf("variable binding %q does not reference a declared variable", binding.Variable)
		}
		if binding.Target == "file" {
			field := fmt.Sprintf("variables.bindings[%d].path", index)
			if _, err := validateBundleTargetPath(field, binding.Path); err != nil {
				return err
			}
		}
		if binding.Target == "argument" {
			if secretSet[binding.Variable] {
				return fmt.Errorf("secret variable %q must not bind into a returned command argument", binding.Variable)
			}
			if !strings.Contains(binding.Template, "{{ value }}") {
				return fmt.Errorf("variables.bindings[%d].template must contain the exact {{ value }} placeholder", index)
			}
			placeholder := "{{ " + binding.Variable + " }}"
			if !containsExactString(definition.Spec.Runtime.Command.Args, placeholder) {
				return fmt.Errorf("argument binding %q has no %q placeholder in runtime.command.args", binding.Variable, placeholder)
			}
		}
	}
	return validateInstall(definition)
}

// validateInstall gates the install block against what the builtin.artifacts
// handler will actually do at runtime, so a definition that cannot install
// fails here instead of on a server's first attempt.
func validateInstall(definition gameDefinition) error {
	artifacts := make([]install.Artifact, 0, len(definition.Spec.Install.Artifacts))
	for _, artifact := range definition.Spec.Install.Artifacts {
		artifacts = append(artifacts, install.Artifact{
			URL:         artifact.URL,
			Destination: artifact.Destination,
			SHA256:      artifact.SHA256,
		})
	}
	// The runtime's own rules: https, full lowercase digest, safe and unique
	// destinations. Shared rather than restated so the two cannot drift.
	if err := install.ValidateArtifacts(artifacts); err != nil {
		return err
	}
	for index, artifact := range definition.Spec.Install.Artifacts {
		field := fmt.Sprintf("install.artifacts[%d].destination", index)
		if _, err := validateBundleTargetPath(field, artifact.Destination); err != nil {
			return err
		}
	}

	// The CLI lint command is deliberately offline. It validates the artifact
	// URL shape and immutable digest above, but does not require a network
	// allowlist entry to be present or prove that an allowlist can authorize a
	// host; the installer rechecks those runtime-only controls immediately
	// before fetching. If an allowlist is supplied, still validate its syntax so
	// malformed manifests fail early without making lint depend on networking.
	if len(definition.Spec.Install.NetworkAllowlist) > 0 {
		if _, err := install.ValidateAllowlist(definition.Spec.Install.NetworkAllowlist); err != nil {
			return err
		}
	}

	// A declared handler and a declared payload have to agree: claiming
	// builtin.artifacts with nothing to fetch installs an empty data directory,
	// and listing artifacts under another handler never fetches them.
	usesArtifacts := definition.Spec.Lifecycle.Install == "builtin.artifacts"
	if !usesArtifacts && len(artifacts) > 0 {
		return fmt.Errorf("install.artifacts is declared but lifecycle.install %q never fetches it", definition.Spec.Lifecycle.Install)
	}
	return nil
}

func validateBundleTargetPath(field string, value string) (string, error) {
	if strings.Contains(value, "\\") {
		return "", fmt.Errorf("%s %q must use canonical forward-slash relative path syntax", field, value)
	}
	normalized, err := serverfiles.NormalizeRelative(value)
	if err != nil {
		return "", fmt.Errorf("%s %q is not a safe relative Bundle target: %w", field, value, err)
	}
	if normalized == "" {
		return "", fmt.Errorf("%s %q must name a file below the server data root", field, value)
	}
	if normalized != value {
		return "", fmt.Errorf("%s %q is not canonical; use %q", field, value, normalized)
	}
	return normalized, nil
}

func containsExactString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func initCommand(args []string) {
	flags := flag.NewFlagSet("init", flag.ExitOnError)
	destination := flags.String("dir", "game-definition", "directory to create")
	_ = flags.Parse(args)
	if err := os.MkdirAll(*destination, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	file := filepath.Join(*destination, "definition.json")
	if _, err := os.Stat(file); err == nil {
		fmt.Fprintln(os.Stderr, "refusing to overwrite", file)
		os.Exit(1)
	}
	content := []byte(initialGameDefinition)
	if err := os.WriteFile(file, content, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("created", file)
}

func usage() {
	fmt.Println("gamectl lint <definition.json>")
	fmt.Println("gamectl init --dir <directory>")
}
