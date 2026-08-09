package gamedefinition

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	serverfiles "github.com/gugumanager/gugumanager/internal/files"
)

// ValuePlaceholder is the only substitution a binding template may contain. It
// is matched literally, with exactly one space on each side, so that template
// rendering stays a total function over the declared variable set.
const ValuePlaceholder = "{{ value }}"

var environmentVariableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var anyPlaceholder = regexp.MustCompile(`\{\{[^{}]*\}\}`)

// ArgumentPlaceholder is the token a definition puts in runtime.command.args to
// mark where an argument binding is substituted.
func ArgumentPlaceholder(variable string) string {
	return "{{ " + variable + " }}"
}

// Binding is one resolved variables.bindings entry. Name applies to the
// environment target and Path to the file target; the schema requires each.
type Binding struct {
	Variable string
	Target   string
	Name     string
	Path     string
	Template string
}

// EnvironmentVariable is one materialized environment entry.
type EnvironmentVariable struct {
	Name   string
	Value  string
	Secret bool
}

// ConfigurationFile is one materialized file binding. Path is a canonical
// relative path below the server data root.
type ConfigurationFile struct {
	Path    string
	Content string
	Secret  bool
}

// RenderedStartup is the materialized startup configuration an Agent applies:
// a command with every placeholder resolved, plus the environment entries and
// configuration files the bindings produce.
type RenderedStartup struct {
	Executable  string
	Args        []string
	Environment []EnvironmentVariable
	Files       []ConfigurationFile
}

// RenderStartup materializes command arguments, environment entries, and
// configuration files from the declared bindings and resolved variable values.
//
// Values holds resolved scalars keyed by variable name; string, int64, and bool
// are the only supported types, matching the closed variable schema subset.
// A bound variable that has no value is an error for the argument target,
// because the command would otherwise keep an unresolved placeholder, and is
// skipped for the environment and file targets, where an unset optional
// variable simply materializes nothing.
//
// Secret variables must never reach a command argument; the schema and gamectl
// forbid it and this function enforces it again at the materialization
// boundary. Rendered values may not contain control characters, so a value can
// never inject an extra line into a configuration file or environment entry.
func RenderStartup(executable string, args []string, bindings []Binding, values map[string]any, secrets []string) (RenderedStartup, error) {
	secretSet := make(map[string]struct{}, len(secrets))
	for _, key := range secrets {
		secretSet[key] = struct{}{}
	}

	rendered := RenderedStartup{Executable: executable}
	arguments := make(map[string]string, len(bindings))
	environmentNames := make(map[string]int, len(bindings))
	filePaths := make(map[string]int, len(bindings))

	for index, binding := range bindings {
		field := fmt.Sprintf("variables.bindings[%d]", index)
		if !startupVariableIdentifier.MatchString(binding.Variable) {
			return RenderedStartup{}, fmt.Errorf("%s.variable %q is not a valid variable identifier", field, binding.Variable)
		}
		if err := validateBindingTemplate(field, binding.Template); err != nil {
			return RenderedStartup{}, err
		}
		_, isSecret := secretSet[binding.Variable]
		value, hasValue := values[binding.Variable]

		if binding.Target == "argument" {
			if isSecret {
				return RenderedStartup{}, fmt.Errorf("%s binds secret variable %q into a command argument", field, binding.Variable)
			}
			if !hasValue {
				return RenderedStartup{}, fmt.Errorf("%s binds variable %q into a command argument but it has no value", field, binding.Variable)
			}
		}
		if !hasValue {
			continue
		}
		text, err := renderBinding(field, binding.Template, value)
		if err != nil {
			return RenderedStartup{}, err
		}

		switch binding.Target {
		case "argument":
			if _, exists := arguments[binding.Variable]; exists {
				return RenderedStartup{}, fmt.Errorf("%s duplicates the argument binding for variable %q", field, binding.Variable)
			}
			arguments[binding.Variable] = text
		case "environment":
			if !environmentVariableName.MatchString(binding.Name) {
				return RenderedStartup{}, fmt.Errorf("%s.name %q is not a valid environment variable name", field, binding.Name)
			}
			if previous, exists := environmentNames[binding.Name]; exists {
				return RenderedStartup{}, fmt.Errorf("%s.name %q duplicates variables.bindings[%d].name", field, binding.Name, previous)
			}
			environmentNames[binding.Name] = index
			rendered.Environment = append(rendered.Environment, EnvironmentVariable{Name: binding.Name, Value: text, Secret: isSecret})
		case "file":
			path, err := NormalizeBindingPath(field+".path", binding.Path)
			if err != nil {
				return RenderedStartup{}, err
			}
			if previous, exists := filePaths[path]; exists {
				return RenderedStartup{}, fmt.Errorf("%s.path %q duplicates variables.bindings[%d].path", field, binding.Path, previous)
			}
			filePaths[path] = index
			rendered.Files = append(rendered.Files, ConfigurationFile{Path: path, Content: text, Secret: isSecret})
		default:
			return RenderedStartup{}, fmt.Errorf("%s.target %q is not supported", field, binding.Target)
		}
	}

	resolvedArgs, err := renderArguments(args, arguments)
	if err != nil {
		return RenderedStartup{}, err
	}
	rendered.Args = resolvedArgs
	return rendered, nil
}

// renderArguments replaces each whole argument element that is an argument
// placeholder with its rendered binding. Substitution is element-for-element so
// a rendered value can never split into additional arguments.
func renderArguments(args []string, arguments map[string]string) ([]string, error) {
	resolved := make([]string, 0, len(args))
	used := make(map[string]struct{}, len(arguments))
	for index, argument := range args {
		if variable, ok := parseArgumentPlaceholder(argument); ok {
			text, bound := arguments[variable]
			if !bound {
				return nil, fmt.Errorf("runtime.command.args[%d] references variable %q without an argument binding", index, variable)
			}
			used[variable] = struct{}{}
			resolved = append(resolved, text)
			continue
		}
		if match := anyPlaceholder.FindString(argument); match != "" {
			return nil, fmt.Errorf("runtime.command.args[%d] contains unresolved placeholder %q", index, match)
		}
		resolved = append(resolved, argument)
	}
	for variable := range arguments {
		if _, ok := used[variable]; !ok {
			return nil, fmt.Errorf("argument binding for variable %q has no %q placeholder in runtime.command.args", variable, ArgumentPlaceholder(variable))
		}
	}
	return resolved, nil
}

func parseArgumentPlaceholder(argument string) (string, bool) {
	if !strings.HasPrefix(argument, "{{ ") || !strings.HasSuffix(argument, " }}") {
		return "", false
	}
	variable := argument[len("{{ ") : len(argument)-len(" }}")]
	if !startupVariableIdentifier.MatchString(variable) {
		return "", false
	}
	return variable, true
}

// validateBindingTemplate requires the exact value placeholder and rejects
// every other placeholder shape, so a typo fails the definition instead of
// reaching a game server as literal template text.
func validateBindingTemplate(field string, template string) error {
	if !strings.Contains(template, ValuePlaceholder) {
		return fmt.Errorf("%s.template must contain the exact %s placeholder", field, ValuePlaceholder)
	}
	for _, match := range anyPlaceholder.FindAllString(template, -1) {
		if match != ValuePlaceholder {
			return fmt.Errorf("%s.template contains unsupported placeholder %q", field, match)
		}
	}
	return nil
}

func renderBinding(field string, template string, value any) (string, error) {
	text, err := RenderScalar(value)
	if err != nil {
		return "", fmt.Errorf("%s: %w", field, err)
	}
	if index := strings.IndexFunc(text, isRejectedControlRune); index >= 0 {
		return "", fmt.Errorf("%s rendered value contains a control character", field)
	}
	return strings.ReplaceAll(template, ValuePlaceholder, text), nil
}

// RenderScalar formats a resolved variable value using the same lexical form
// the control plane and Agent both expect.
func RenderScalar(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case bool:
		return strconv.FormatBool(typed), nil
	default:
		return "", fmt.Errorf("value of type %T is not a supported startup scalar", value)
	}
}

func isRejectedControlRune(value rune) bool {
	return value < 0x20 || value == 0x7f || value >= 0x80 && value <= 0x9f
}

// NormalizeBindingPath validates a Bundle-declared relative target path and
// returns its canonical form. Bundle target paths name a file below the server
// data root using forward slashes; anything else is a definition bug rather
// than a runtime condition, so the caller fails the definition.
func NormalizeBindingPath(field string, value string) (string, error) {
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
