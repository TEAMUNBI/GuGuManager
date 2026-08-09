package gamedefinition

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// MaxSafeStartupInteger is the largest integer that can cross the JSON/JavaScript
// control-plane boundary without losing precision.
const MaxSafeStartupInteger int64 = 1<<53 - 1

const MinSafeStartupInteger int64 = -MaxSafeStartupInteger

var startupVariableIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// StartupVariableSchema is the closed, executable JSON-Schema subset supported
// by both gamectl and the Control Plane Startup store.
type StartupVariableSchema struct {
	Type                 string                             `json:"type"`
	AdditionalProperties *bool                              `json:"additionalProperties,omitempty"`
	Required             []string                           `json:"required,omitempty"`
	Properties           map[string]StartupVariableProperty `json:"properties"`
}

// StartupVariableProperty intentionally contains only constraints implemented
// by the Startup store. json.Decoder.DisallowUnknownFields rejects every other
// JSON-Schema keyword instead of silently weakening it.
type StartupVariableProperty struct {
	Type      string          `json:"type"`
	Default   json.RawMessage `json:"default,omitempty"`
	Const     json.RawMessage `json:"const,omitempty"`
	Enum      []string        `json:"enum,omitempty"`
	Minimum   *int64          `json:"minimum,omitempty"`
	Maximum   *int64          `json:"maximum,omitempty"`
	MinLength *int            `json:"minLength,omitempty"`
	MaxLength *int            `json:"maxLength,omitempty"`
}

func (property *StartupVariableProperty) UnmarshalJSON(content []byte) error {
	var wire struct {
		Type      string          `json:"type"`
		Default   json.RawMessage `json:"default"`
		Const     json.RawMessage `json:"const"`
		Enum      []string        `json:"enum"`
		Minimum   json.RawMessage `json:"minimum"`
		Maximum   json.RawMessage `json:"maximum"`
		MinLength json.RawMessage `json:"minLength"`
		MaxLength json.RawMessage `json:"maxLength"`
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	property.Type = wire.Type
	property.Default = wire.Default
	property.Const = wire.Const
	property.Enum = wire.Enum
	if len(wire.Minimum) != 0 {
		value, err := decodeSchemaIntegerKeyword(wire.Minimum)
		if err != nil {
			return fmt.Errorf("minimum: %w", err)
		}
		property.Minimum = &value
	}
	if len(wire.Maximum) != 0 {
		value, err := decodeSchemaIntegerKeyword(wire.Maximum)
		if err != nil {
			return fmt.Errorf("maximum: %w", err)
		}
		property.Maximum = &value
	}
	if len(wire.MinLength) != 0 {
		value, err := decodeSchemaLengthKeyword(wire.MinLength)
		if err != nil {
			return fmt.Errorf("minLength: %w", err)
		}
		property.MinLength = &value
	}
	if len(wire.MaxLength) != 0 {
		value, err := decodeSchemaLengthKeyword(wire.MaxLength)
		if err != nil {
			return fmt.Errorf("maxLength: %w", err)
		}
		property.MaxLength = &value
	}
	return nil
}

// DecodeStartupVariableSchema strictly decodes one variables.schema value.
// Unknown fields are errors because the runtime would otherwise ignore them.
func DecodeStartupVariableSchema(content []byte) (StartupVariableSchema, error) {
	if err := rejectNullStructuralKeywords(content); err != nil {
		return StartupVariableSchema{}, err
	}
	var schema StartupVariableSchema
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&schema); err != nil {
		return StartupVariableSchema{}, fmt.Errorf("decode variables.schema: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return StartupVariableSchema{}, fmt.Errorf("decode variables.schema: trailing JSON value")
		}
		return StartupVariableSchema{}, fmt.Errorf("decode variables.schema trailing data: %w", err)
	}
	return schema, nil
}

func rejectNullStructuralKeywords(content []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(content, &root); err != nil {
		return fmt.Errorf("decode variables.schema: %w", err)
	}
	rootKeywords := map[string]struct{}{
		"type": {}, "additionalProperties": {}, "required": {}, "properties": {},
	}
	for keyword := range root {
		if _, allowed := rootKeywords[keyword]; !allowed {
			return fmt.Errorf("variables.schema contains unsupported keyword %q", keyword)
		}
	}
	for _, keyword := range []string{"additionalProperties", "required"} {
		if raw, present := root[keyword]; present && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("variables.schema.%s must not be null", keyword)
		}
	}
	propertiesRaw, present := root["properties"]
	if !present || bytes.Equal(bytes.TrimSpace(propertiesRaw), []byte("null")) {
		return nil
	}
	var properties map[string]json.RawMessage
	if err := json.Unmarshal(propertiesRaw, &properties); err != nil {
		return nil
	}
	for key, propertyRaw := range properties {
		var property map[string]json.RawMessage
		if err := json.Unmarshal(propertyRaw, &property); err != nil {
			continue
		}
		propertyKeywords := map[string]struct{}{
			"type": {}, "default": {}, "const": {}, "enum": {},
			"minimum": {}, "maximum": {}, "minLength": {}, "maxLength": {},
		}
		for keyword := range property {
			if _, allowed := propertyKeywords[keyword]; !allowed {
				return fmt.Errorf("variables.schema.properties[%q] contains unsupported keyword %q", key, keyword)
			}
		}
		for _, keyword := range []string{"enum", "minimum", "maximum", "minLength", "maxLength"} {
			if raw, present := property[keyword]; present && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return fmt.Errorf("variables.schema.properties[%q].%s must not be null", key, keyword)
			}
		}
	}
	return nil
}

// Validate checks cross-keyword and GuGuManager materialization semantics. It
// never includes default, const, or enum values in diagnostics.
func (schema StartupVariableSchema) Validate(secrets []string) error {
	if schema.Type != "object" {
		return fmt.Errorf("variables.schema.type must be %q", "object")
	}
	if schema.Properties == nil {
		return fmt.Errorf("variables.schema.properties is required")
	}
	if schema.AdditionalProperties != nil && *schema.AdditionalProperties {
		return fmt.Errorf("variables.schema.additionalProperties must be false")
	}

	required := make(map[string]struct{}, len(schema.Required))
	for _, key := range schema.Required {
		if !startupVariableIdentifier.MatchString(key) {
			return fmt.Errorf("variables.schema.required entry %q is not a valid variable identifier", key)
		}
		if _, duplicate := required[key]; duplicate {
			return fmt.Errorf("variables.schema.required contains duplicate entry %q", key)
		}
		if _, declared := schema.Properties[key]; !declared {
			return fmt.Errorf("variables.schema.required entry %q does not reference a declared variable", key)
		}
		required[key] = struct{}{}
	}

	for key, property := range schema.Properties {
		if !startupVariableIdentifier.MatchString(key) {
			return fmt.Errorf("variables.schema property %q is not a valid variable identifier", key)
		}
		if err := property.validate(key); err != nil {
			return err
		}
	}

	secretSet := make(map[string]struct{}, len(secrets))
	for _, key := range secrets {
		if !startupVariableIdentifier.MatchString(key) {
			return fmt.Errorf("variables.secrets entry %q is not a valid variable identifier", key)
		}
		if _, duplicate := secretSet[key]; duplicate {
			return fmt.Errorf("variables.secrets contains duplicate entry %q", key)
		}
		property, declared := schema.Properties[key]
		if !declared {
			return fmt.Errorf("variables.secrets entry %q does not reference a declared variable", key)
		}
		if len(property.Default) != 0 {
			return fmt.Errorf("secret variable %q must not declare a default", key)
		}
		if len(property.Const) != 0 {
			return fmt.Errorf("secret variable %q must not declare const", key)
		}
		if property.Enum != nil {
			return fmt.Errorf("secret variable %q must not declare enum", key)
		}
		secretSet[key] = struct{}{}
	}
	return nil
}

func (property StartupVariableProperty) validate(key string) error {
	path := fmt.Sprintf("variables.schema.properties[%q]", key)
	switch property.Type {
	case "string":
		if property.Minimum != nil || property.Maximum != nil {
			return fmt.Errorf("%s string variables must not declare minimum or maximum", path)
		}
		if property.MinLength != nil && *property.MinLength < 0 {
			return fmt.Errorf("%s.minLength must not be negative", path)
		}
		if property.MinLength != nil && int64(*property.MinLength) > MaxSafeStartupInteger {
			return fmt.Errorf("%s.minLength must be a safe integer", path)
		}
		if property.MaxLength != nil && *property.MaxLength < 0 {
			return fmt.Errorf("%s.maxLength must not be negative", path)
		}
		if property.MaxLength != nil && int64(*property.MaxLength) > MaxSafeStartupInteger {
			return fmt.Errorf("%s.maxLength must be a safe integer", path)
		}
		if property.MinLength != nil && property.MaxLength != nil && *property.MinLength > *property.MaxLength {
			return fmt.Errorf("%s.minLength must not exceed maxLength", path)
		}
		if property.Enum != nil {
			if len(property.Enum) == 0 {
				return fmt.Errorf("%s.enum must contain at least one value", path)
			}
			seen := make(map[string]struct{}, len(property.Enum))
			for _, value := range property.Enum {
				if _, duplicate := seen[value]; duplicate {
					return fmt.Errorf("%s.enum must contain unique values", path)
				}
				seen[value] = struct{}{}
				if err := property.validateStringConstraints(path+".enum", value); err != nil {
					return err
				}
			}
		}
	case "integer":
		if property.MinLength != nil || property.MaxLength != nil || property.Enum != nil {
			return fmt.Errorf("%s integer variables must not declare minLength, maxLength, or enum", path)
		}
		if property.Minimum != nil && !safeStartupInteger(*property.Minimum) {
			return fmt.Errorf("%s.minimum must be a safe integer", path)
		}
		if property.Maximum != nil && !safeStartupInteger(*property.Maximum) {
			return fmt.Errorf("%s.maximum must be a safe integer", path)
		}
		if property.Minimum != nil && property.Maximum != nil && *property.Minimum > *property.Maximum {
			return fmt.Errorf("%s.minimum must not exceed maximum", path)
		}
	case "boolean":
		if property.Minimum != nil || property.Maximum != nil || property.MinLength != nil || property.MaxLength != nil || property.Enum != nil {
			return fmt.Errorf("%s boolean variables must not declare range, length, or enum constraints", path)
		}
	default:
		return fmt.Errorf("%s.type %q is not supported", path, property.Type)
	}

	defaultValue, hasDefault, err := property.validateRawValue(path, "default", property.Default)
	if err != nil {
		return err
	}
	constValue, hasConst, err := property.validateRawValue(path, "const", property.Const)
	if err != nil {
		return err
	}
	if hasDefault && hasConst && !reflect.DeepEqual(defaultValue, constValue) {
		return fmt.Errorf("%s.default must equal const", path)
	}
	return nil
}

func (property StartupVariableProperty) validateRawValue(path string, keyword string, raw json.RawMessage) (any, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, true, fmt.Errorf("%s.%s cannot be decoded", path, keyword)
	}
	switch property.Type {
	case "string":
		text, ok := value.(string)
		if !ok {
			return nil, true, fmt.Errorf("%s.%s must be a string", path, keyword)
		}
		if err := property.validateStringConstraints(path+"."+keyword, text); err != nil {
			return nil, true, err
		}
		return text, true, nil
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return nil, true, fmt.Errorf("%s.%s must be an integer", path, keyword)
		}
		integer, integerOK := ParseStartupInteger(number)
		if !integerOK {
			return nil, true, fmt.Errorf("%s.%s must be an integer", path, keyword)
		}
		if !safeStartupInteger(integer) {
			return nil, true, fmt.Errorf("%s.%s must be a safe integer", path, keyword)
		}
		if property.Minimum != nil && integer < *property.Minimum {
			return nil, true, fmt.Errorf("%s.%s is below minimum", path, keyword)
		}
		if property.Maximum != nil && integer > *property.Maximum {
			return nil, true, fmt.Errorf("%s.%s is above maximum", path, keyword)
		}
		return integer, true, nil
	case "boolean":
		boolean, ok := value.(bool)
		if !ok {
			return nil, true, fmt.Errorf("%s.%s must be a boolean", path, keyword)
		}
		return boolean, true, nil
	default:
		return nil, true, fmt.Errorf("%s.%s uses an unsupported type", path, keyword)
	}
}

func (property StartupVariableProperty) validateStringConstraints(path string, value string) error {
	length := len([]rune(value))
	if property.MinLength != nil && length < *property.MinLength {
		return fmt.Errorf("%s is shorter than minLength", path)
	}
	if property.MaxLength != nil && length > *property.MaxLength {
		return fmt.Errorf("%s is longer than maxLength", path)
	}
	if property.Enum != nil {
		for _, allowed := range property.Enum {
			if value == allowed {
				return nil
			}
		}
		return fmt.Errorf("%s is not in enum", path)
	}
	return nil
}

// ParseStartupInteger converts a JSON number to int64 without a float64
// round-trip. It performs a linear lexical normalization and never constructs
// an arbitrary-precision value, so absurd exponents cannot amplify work beyond
// the request size. Exponent and decimal representations are accepted only
// when the mathematical value is exactly integral.
func ParseStartupInteger(number json.Number) (int64, bool) {
	text := number.String()
	if text == "" {
		return 0, false
	}
	index := 0
	negative := false
	if text[index] == '-' {
		negative = true
		index++
		if index == len(text) {
			return 0, false
		}
	}

	integerStart := index
	switch {
	case text[index] == '0':
		index++
		if index < len(text) && isDecimalDigit(text[index]) {
			return 0, false
		}
	case text[index] >= '1' && text[index] <= '9':
		for index < len(text) && isDecimalDigit(text[index]) {
			index++
		}
	default:
		return 0, false
	}
	integerPart := text[integerStart:index]

	fractionPart := ""
	if index < len(text) && text[index] == '.' {
		index++
		fractionStart := index
		for index < len(text) && isDecimalDigit(text[index]) {
			index++
		}
		if index == fractionStart {
			return 0, false
		}
		fractionPart = text[fractionStart:index]
	}

	exponent := 0
	if index < len(text) && (text[index] == 'e' || text[index] == 'E') {
		index++
		exponentNegative := false
		if index < len(text) && (text[index] == '+' || text[index] == '-') {
			exponentNegative = text[index] == '-'
			index++
		}
		exponentStart := index
		limit := len(integerPart) + len(fractionPart) + 20
		for index < len(text) && isDecimalDigit(text[index]) {
			digit := int(text[index] - '0')
			if exponent <= limit {
				if exponent > (limit-digit)/10 {
					exponent = limit + 1
				} else {
					exponent = exponent*10 + digit
				}
			}
			index++
		}
		if index == exponentStart {
			return 0, false
		}
		if exponentNegative {
			exponent = -exponent
		}
	}
	if index != len(text) {
		return 0, false
	}

	digits := strings.TrimLeft(integerPart+fractionPart, "0")
	if digits == "" {
		return 0, true
	}
	scale := exponent - len(fractionPart)
	if scale < 0 {
		remove := -scale
		trailingZeros := 0
		for position := len(digits) - 1; position >= 0 && digits[position] == '0'; position-- {
			trailingZeros++
		}
		if remove > trailingZeros {
			return 0, false
		}
		digits = digits[:len(digits)-remove]
		scale = 0
	}
	if scale > 0 {
		if scale > 19 || len(digits) > 19-scale {
			return 0, false
		}
		digits += strings.Repeat("0", scale)
	}
	if len(digits) > 19 {
		return 0, false
	}
	if negative {
		digits = "-" + digits
	}
	integer, err := strconv.ParseInt(digits, 10, 64)
	return integer, err == nil
}

func isDecimalDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func decodeSchemaIntegerKeyword(raw json.RawMessage) (int64, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return 0, fmt.Errorf("must be an integer")
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, fmt.Errorf("must be an integer")
	}
	integer, ok := ParseStartupInteger(number)
	if !ok {
		return 0, fmt.Errorf("must be an integer")
	}
	return integer, nil
}

func decodeSchemaLengthKeyword(raw json.RawMessage) (int, error) {
	integer, err := decodeSchemaIntegerKeyword(raw)
	if err != nil {
		return 0, err
	}
	converted := int(integer)
	if int64(converted) != integer {
		return 0, fmt.Errorf("is outside the supported integer range")
	}
	return converted, nil
}

func safeStartupInteger(value int64) bool {
	return value >= MinSafeStartupInteger && value <= MaxSafeStartupInteger
}
