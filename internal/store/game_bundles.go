package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/gugumanager/gugumanager/internal/domain"
	serverfiles "github.com/gugumanager/gugumanager/internal/files"
	gamedefinition "github.com/gugumanager/gugumanager/spec/game-definition"
)

type fixedBundleDocument struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"metadata"`
	Spec struct {
		Release struct {
			Version string `json:"version"`
		} `json:"release"`
		Compatibility struct {
			Platforms []string `json:"platforms"`
		} `json:"compatibility"`
		Capabilities []string        `json:"capabilities"`
		Variables    json.RawMessage `json:"variables"`
		Runtime      struct {
			Command domain.StartupCommand `json:"command"`
		} `json:"runtime"`
	} `json:"spec"`
}

type fixedBundleVariables struct {
	Schema   json.RawMessage
	Secrets  []string
	Bindings []domain.StartupBinding
}

type developmentGamePresentation struct {
	Summary       string
	Status        string
	Servers       int
	Icon          string
	DefaultMemory int
	DefaultDisk   int
}

var developmentGamePresentations = map[string]developmentGamePresentation{
	"io.gugumanager.papermc": {
		Summary: "高性能 Minecraft Java Dedicated Server", Status: "approved", Servers: 6,
		Icon: "cube", DefaultMemory: 4096, DefaultDisk: 25,
	},
	"io.gugumanager.factorio": {
		Summary: "稳定的工厂协作存档服务器", Status: "approved", Servers: 3,
		Icon: "factory", DefaultMemory: 4096, DefaultDisk: 20,
	},
	"io.gugumanager.vintagestory": {
		Summary: "强调探索与持久世界的独立服务器", Status: "pending", Servers: 1,
		Icon: "mountain", DefaultMemory: 3072, DefaultDisk: 18,
	},
}

var developmentGameOrder = []string{
	"io.gugumanager.papermc",
	"io.gugumanager.factorio",
	"io.gugumanager.vintagestory",
}

func loadFixedGameCatalog() ([]domain.GameDefinition, error) {
	bundles, err := gamedefinition.FixedBundles()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]domain.GameDefinition, len(bundles))
	for _, bundle := range bundles {
		document, err := decodeFixedBundle(bundle.Document)
		if err != nil {
			return nil, fmt.Errorf("parse embedded GameDefinition %s: %w", bundle.Filename, err)
		}
		presentation, known := developmentGamePresentations[document.Metadata.ID]
		if !known {
			return nil, fmt.Errorf("embedded GameDefinition %s has no development catalog metadata", document.Metadata.ID)
		}
		if _, duplicate := byID[document.Metadata.ID]; duplicate {
			return nil, fmt.Errorf("duplicate embedded GameDefinition id %s", document.Metadata.ID)
		}
		byID[document.Metadata.ID] = domain.GameDefinition{
			ID: document.Metadata.ID, BundleDigest: bundleDigest(bundle.Document), Name: document.Metadata.Name,
			Summary: presentation.Summary, Version: document.Metadata.Version, GameVersion: document.Spec.Release.Version,
			Status: presentation.Status, Capabilities: append([]string(nil), document.Spec.Capabilities...),
			Platforms: append([]string(nil), document.Spec.Compatibility.Platforms...), Servers: presentation.Servers,
			Icon: presentation.Icon, DefaultMemory: presentation.DefaultMemory, DefaultDisk: presentation.DefaultDisk,
			BundleDocument: string(bundle.Document),
		}
	}

	result := make([]domain.GameDefinition, 0, len(developmentGameOrder))
	for _, gameID := range developmentGameOrder {
		game, ok := byID[gameID]
		if !ok {
			return nil, fmt.Errorf("missing embedded GameDefinition %s", gameID)
		}
		result = append(result, game)
	}
	if len(byID) != len(result) {
		return nil, fmt.Errorf("embedded GameDefinition catalog contains an unordered entry")
	}
	return result, nil
}

func decodeFixedBundle(content []byte) (fixedBundleDocument, error) {
	if err := gamedefinition.ValidateV1Alpha1JSON(content); err != nil {
		return fixedBundleDocument{}, fmt.Errorf("canonical GameDefinition validation failed: %w", err)
	}
	if err := rejectCaseVariantVariablesContainer(content); err != nil {
		return fixedBundleDocument{}, err
	}
	var document fixedBundleDocument
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return fixedBundleDocument{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fixedBundleDocument{}, fmt.Errorf("trailing JSON value")
		}
		return fixedBundleDocument{}, fmt.Errorf("trailing JSON data: %w", err)
	}
	if document.APIVersion != "gugumanager.io/games/v1alpha1" || document.Kind != "GameDefinition" {
		return fixedBundleDocument{}, fmt.Errorf("unsupported GameDefinition identity")
	}
	if document.Metadata.ID == "" || document.Metadata.Version == "" || document.Spec.Release.Version == "" {
		return fixedBundleDocument{}, fmt.Errorf("incomplete GameDefinition metadata")
	}
	return document, nil
}

func rejectCaseVariantVariablesContainer(content []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(content, &root); err != nil {
		return err
	}
	for key := range root {
		if strings.EqualFold(key, "spec") && key != "spec" {
			return fmt.Errorf("Bundle field %q must use exact JSON member casing", key)
		}
	}
	specRaw, present := root["spec"]
	if !present {
		return nil
	}
	var spec map[string]json.RawMessage
	if err := json.Unmarshal(specRaw, &spec); err != nil {
		return nil
	}
	for key := range spec {
		if strings.EqualFold(key, "variables") && key != "variables" {
			return fmt.Errorf("spec field %q must use exact JSON member casing", key)
		}
	}
	return nil
}

func decodeFixedBundleVariables(content []byte) (fixedBundleVariables, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(content, &members); err != nil {
		return fixedBundleVariables{}, err
	}
	if members == nil {
		return fixedBundleVariables{}, fmt.Errorf("variables must be an object")
	}
	allowed := map[string]struct{}{"schema": {}, "secrets": {}, "bindings": {}}
	for key := range members {
		if _, ok := allowed[key]; !ok {
			return fixedBundleVariables{}, fmt.Errorf("variables contains unsupported field %q", key)
		}
	}
	schema, declared := members["schema"]
	if !declared || bytes.Equal(bytes.TrimSpace(schema), []byte("null")) {
		return fixedBundleVariables{}, fmt.Errorf("variables.schema is required")
	}
	result := fixedBundleVariables{Schema: append(json.RawMessage(nil), schema...)}
	if secrets, present := members["secrets"]; present {
		if bytes.Equal(bytes.TrimSpace(secrets), []byte("null")) {
			return fixedBundleVariables{}, fmt.Errorf("variables.secrets must be an array")
		}
		if err := json.Unmarshal(secrets, &result.Secrets); err != nil {
			return fixedBundleVariables{}, fmt.Errorf("variables.secrets: %w", err)
		}
	}
	if bindings, present := members["bindings"]; present {
		if bytes.Equal(bytes.TrimSpace(bindings), []byte("null")) {
			return fixedBundleVariables{}, fmt.Errorf("variables.bindings must be an array")
		}
		decoded, err := decodeFixedBundleBindings(bindings)
		if err != nil {
			return fixedBundleVariables{}, fmt.Errorf("variables.bindings: %w", err)
		}
		result.Bindings = decoded
	}
	return result, nil
}

func decodeFixedBundleBindings(content []byte) ([]domain.StartupBinding, error) {
	var entries []json.RawMessage
	if err := json.Unmarshal(content, &entries); err != nil {
		return nil, err
	}
	result := make([]domain.StartupBinding, 0, len(entries))
	for index, entry := range entries {
		var members map[string]json.RawMessage
		if err := json.Unmarshal(entry, &members); err != nil || members == nil {
			return nil, fmt.Errorf("entry %d must be an object", index)
		}
		allowed := map[string]struct{}{
			"variable": {}, "target": {}, "name": {}, "path": {}, "template": {},
		}
		for key := range members {
			if _, ok := allowed[key]; !ok {
				return nil, fmt.Errorf("entry %d contains unsupported field %q", index, key)
			}
		}
		variable, err := requiredBundleBindingString(members, "variable")
		if err != nil {
			return nil, fmt.Errorf("entry %d: %w", index, err)
		}
		target, err := requiredBundleBindingString(members, "target")
		if err != nil {
			return nil, fmt.Errorf("entry %d: %w", index, err)
		}
		template, err := requiredBundleBindingString(members, "template")
		if err != nil {
			return nil, fmt.Errorf("entry %d: %w", index, err)
		}
		name, hasName, err := optionalBundleBindingString(members, "name")
		if err != nil || hasName && name == "" {
			return nil, fmt.Errorf("entry %d name must be a non-empty string when present", index)
		}
		path, hasPath, err := optionalBundleBindingString(members, "path")
		if err != nil || hasPath && path == "" {
			return nil, fmt.Errorf("entry %d path must be a non-empty string when present", index)
		}
		switch target {
		case "argument":
		case "environment":
			if !hasName {
				return nil, fmt.Errorf("entry %d environment target requires name", index)
			}
		case "file":
			if !hasPath {
				return nil, fmt.Errorf("entry %d file target requires path", index)
			}
			if err := validateFixedBundleTargetPath(path); err != nil {
				return nil, fmt.Errorf("entry %d file path is invalid", index)
			}
		default:
			return nil, fmt.Errorf("entry %d target is unsupported", index)
		}
		result = append(result, domain.StartupBinding{Variable: variable, Target: target, Template: template})
	}
	return result, nil
}

func requiredBundleBindingString(members map[string]json.RawMessage, key string) (string, error) {
	value, present, err := optionalBundleBindingString(members, key)
	if err != nil {
		return "", fmt.Errorf("%s must be a string", key)
	}
	if !present {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

func optionalBundleBindingString(members map[string]json.RawMessage, key string) (string, bool, error) {
	raw, present := members[key]
	if !present {
		return "", false, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", true, fmt.Errorf("%s must not be null", key)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", true, err
	}
	return value, true, nil
}

func validateFixedBundleTargetPath(value string) error {
	if strings.Contains(value, "\\") {
		return fmt.Errorf("must use forward slashes")
	}
	normalized, err := serverfiles.NormalizeRelative(value)
	if err != nil || normalized == "" || normalized != value {
		return fmt.Errorf("must be a canonical relative path")
	}
	return nil
}

func bundleDigest(content []byte) string {
	digest := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func startupFromFixedBundle(server domain.Server, game domain.GameDefinition, overrides map[string]any) (domain.Startup, map[string]any, error) {
	if err := validateServerBundleIdentity(server, game); err != nil {
		return domain.Startup{}, nil, err
	}
	document, err := decodeFixedBundle([]byte(game.BundleDocument))
	if err != nil {
		return domain.Startup{}, nil, packageIncompatible("fixed Bundle document cannot be decoded")
	}
	if document.Metadata.ID != game.ID || document.Metadata.Version != game.Version {
		return domain.Startup{}, nil, packageIncompatible("fixed Bundle metadata does not match the catalog entry")
	}
	variables := fixedBundleVariables{}
	if len(document.Spec.Variables) != 0 {
		variables, err = decodeFixedBundleVariables(document.Spec.Variables)
		if err != nil {
			return domain.Startup{}, nil, packageIncompatible("fixed Bundle variables declaration cannot be decoded")
		}
	}
	variableSchema := gamedefinition.StartupVariableSchema{
		Type:       "object",
		Properties: map[string]gamedefinition.StartupVariableProperty{},
	}
	if len(variables.Schema) != 0 {
		variableSchema, err = gamedefinition.DecodeStartupVariableSchema(variables.Schema)
		if err != nil {
			return domain.Startup{}, nil, packageIncompatible("fixed Bundle variable schema cannot be decoded")
		}
	}
	if err := variableSchema.Validate(variables.Secrets); err != nil {
		return domain.Startup{}, nil, packageIncompatible("fixed Bundle variable schema is incompatible: " + err.Error())
	}

	required := make(map[string]bool, len(variableSchema.Required))
	for _, key := range variableSchema.Required {
		required[key] = true
	}
	secret := make(map[string]bool, len(variables.Secrets))
	for _, key := range variables.Secrets {
		secret[key] = true
	}

	keys := make([]string, 0, len(variableSchema.Properties))
	for key := range variableSchema.Properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	startup := domain.Startup{
		ServerID: server.ID, Generation: server.Generation,
		Command: domain.StartupCommand{
			Executable: document.Spec.Runtime.Command.Executable,
			Args:       append([]string(nil), document.Spec.Runtime.Command.Args...),
		},
		Variables: make([]domain.StartupVariable, 0, len(keys)),
		Bindings:  append([]domain.StartupBinding(nil), variables.Bindings...),
	}
	values := make(map[string]any, len(keys))
	for _, key := range keys {
		property := variableSchema.Properties[key]
		variable := domain.StartupVariable{
			Key: key, Type: property.Type, Secret: secret[key], Required: required[key],
			Minimum: property.Minimum, Maximum: property.Maximum, MinLength: property.MinLength, MaxLength: property.MaxLength,
			EnumValues: append([]string(nil), property.Enum...),
		}
		if variable.Type != "string" && variable.Type != "integer" && variable.Type != "boolean" {
			return domain.Startup{}, nil, packageIncompatible("fixed Bundle declares an unsupported Startup variable type")
		}
		if len(property.Const) != 0 {
			constant, err := decodeBundleValue(property.Const)
			if err != nil {
				return domain.Startup{}, nil, packageIncompatible("fixed Bundle variable const cannot be decoded")
			}
			normalized, err := normalizeStartupValue(variable, constant)
			if err != nil {
				return domain.Startup{}, nil, packageIncompatible("fixed Bundle variable const violates its schema")
			}
			variable.ConstValue = normalized
		}
		if len(property.Default) != 0 {
			defaultValue, err := decodeBundleValue(property.Default)
			if err != nil {
				return domain.Startup{}, nil, packageIncompatible("fixed Bundle variable default cannot be decoded")
			}
			normalized, err := normalizeStartupValue(variable, defaultValue)
			if err != nil {
				return domain.Startup{}, nil, packageIncompatible("fixed Bundle variable default violates its schema")
			}
			variable.Default = normalized
			values[key] = normalized
		}
		startup.Variables = append(startup.Variables, variable)
	}
	if err := validateStartupBindings(startup); err != nil {
		return domain.Startup{}, nil, err
	}
	if err := applyStartupOverrides(startup.Variables, values, overrides); err != nil {
		return domain.Startup{}, nil, err
	}
	return startup, values, nil
}

func validateServerBundleIdentity(server domain.Server, game domain.GameDefinition) error {
	if server.GameID != game.ID || server.GameDefinitionVersion != game.Version || server.GameBundleDigest != game.BundleDigest {
		return packageIncompatible("server Bundle identity does not match the catalog entry")
	}
	if game.BundleDocument == "" || bundleDigest([]byte(game.BundleDocument)) != game.BundleDigest {
		return packageIncompatible("catalog Bundle digest does not match its embedded document")
	}
	return nil
}

func validateStartupBindings(startup domain.Startup) error {
	definitions := make(map[string]domain.StartupVariable, len(startup.Variables))
	for _, variable := range startup.Variables {
		definitions[variable.Key] = variable
	}
	for _, binding := range startup.Bindings {
		variable, declared := definitions[binding.Variable]
		if !declared {
			return packageIncompatible("fixed Bundle binding does not reference a declared variable")
		}
		if binding.Target != "argument" {
			continue
		}
		if variable.Secret {
			return packageIncompatible("fixed Bundle attempts to bind a secret into a returned command")
		}
		placeholder := startupArgumentPlaceholder(binding.Variable)
		if !containsString(startup.Command.Args, placeholder) || !strings.Contains(binding.Template, "{{ value }}") {
			return packageIncompatible("fixed Bundle argument binding is incomplete")
		}
	}
	return nil
}

func applyStartupOverrides(definitions []domain.StartupVariable, values map[string]any, overrides map[string]any) error {
	byKey := make(map[string]domain.StartupVariable, len(definitions))
	for _, variable := range definitions {
		byKey[variable.Key] = variable
	}
	for key, value := range overrides {
		definition, declared := byKey[key]
		if !declared {
			return validationProblem("undeclared Startup variable: " + key)
		}
		if value == nil {
			if definition.Required {
				return validationProblem("required Startup variable cannot be cleared: " + key)
			}
			delete(values, key)
			continue
		}
		normalized, err := normalizeStartupValue(definition, value)
		if err != nil {
			return err
		}
		values[key] = normalized
	}
	return nil
}

func decodeBundleValue(raw json.RawMessage) (any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func startupTemplatesEqual(left domain.Startup, right domain.Startup) bool {
	left.Generation = 0
	right.Generation = 0
	return reflect.DeepEqual(left, right)
}

func startupArgumentPlaceholder(variable string) string {
	return "{{ " + variable + " }}"
}

func packageIncompatible(message string) *domain.Problem {
	return domain.NewProblem("PACKAGE_INCOMPATIBLE", message, false)
}
