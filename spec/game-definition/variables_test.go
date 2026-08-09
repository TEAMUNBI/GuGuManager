package gamedefinition

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStartupVariableSchemaAcceptsExecutableSubset(t *testing.T) {
	raw := []byte(`{
		"type":"object",
		"additionalProperties":false,
		"required":["name","slots","enabled"],
		"properties":{
			"name":{"type":"string","default":"Aurora","minLength":1,"maxLength":64,"enum":["Aurora","Borealis"]},
			"slots":{"type":"integer","default":8,"minimum":1,"maximum":64},
			"enabled":{"type":"boolean","default":true,"const":true},
			"token":{"type":"string","minLength":8,"maxLength":128}
		}
	}`)
	schema, err := DecodeStartupVariableSchema(raw)
	if err != nil {
		t.Fatalf("DecodeStartupVariableSchema() error = %v", err)
	}
	if err := schema.Validate([]string{"token"}); err != nil {
		t.Fatalf("Validate() rejected the executable subset: %v", err)
	}
}

func TestStartupVariableSchemaRejectsUnknownKeywords(t *testing.T) {
	_, err := DecodeStartupVariableSchema([]byte(`{
		"type":"object",
		"properties":{"name":{"type":"string","pattern":"^safe$"}}
	}`))
	if err == nil {
		t.Fatal("DecodeStartupVariableSchema() accepted an unsupported property keyword")
	}
	if !strings.Contains(err.Error(), "pattern") {
		t.Fatalf("error = %q, want the unsupported keyword path", err)
	}
}

func TestStartupVariableSchemaRejectsCaseVariantKeywords(t *testing.T) {
	tests := []string{
		`{"Type":"object","properties":{}}`,
		`{"type":"object","Properties":{}}`,
		`{"type":"object","properties":{"name":{"Type":"string"}}}`,
		`{"type":"object","properties":{"name":{"type":"string","MinLength":1}}}`,
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if _, err := DecodeStartupVariableSchema([]byte(raw)); err == nil {
				t.Fatal("case-variant JSON-Schema keyword bypassed exact-key decoding")
			}
		})
	}
}

func TestStartupVariableSchemaRejectsNullStructuralKeywords(t *testing.T) {
	tests := []string{
		`{"type":"object","additionalProperties":null,"properties":{}}`,
		`{"type":"object","required":null,"properties":{}}`,
		`{"type":"object","properties":{"name":{"type":"string","enum":null}}}`,
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			schema, err := DecodeStartupVariableSchema([]byte(raw))
			if err == nil {
				err = schema.Validate(nil)
			}
			if err == nil {
				t.Fatal("null structural keyword was treated as if it were absent")
			}
		})
	}
}

func TestStartupVariableSchemaRejectsCrossKeywordContradictions(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		secret []string
		want   string
	}{
		{
			name: "integer range",
			raw:  `{"type":"object","properties":{"probe":{"type":"integer","minimum":2,"maximum":1}}}`,
			want: "minimum",
		},
		{
			name: "string range",
			raw:  `{"type":"object","properties":{"probe":{"type":"string","minLength":2,"maxLength":1}}}`,
			want: "minLength",
		},
		{
			name: "default violates enum",
			raw:  `{"type":"object","properties":{"probe":{"type":"string","default":"hard","enum":["normal"]}}}`,
			want: "default",
		},
		{
			name: "default differs from const",
			raw:  `{"type":"object","properties":{"probe":{"type":"boolean","default":true,"const":false}}}`,
			want: "default",
		},
		{
			name: "unsafe integer",
			raw:  `{"type":"object","properties":{"probe":{"type":"integer","default":9007199254740992}}}`,
			want: "safe integer",
		},
		{
			name: "unsafe minLength",
			raw:  `{"type":"object","properties":{"probe":{"type":"string","minLength":9007199254740992}}}`,
			want: "safe integer",
		},
		{
			name: "unsafe maxLength",
			raw:  `{"type":"object","properties":{"probe":{"type":"string","maxLength":9007199254740992}}}`,
			want: "safe integer",
		},
		{
			name: "high precision non-integer",
			raw:  `{"type":"object","properties":{"probe":{"type":"integer","default":1.0000000000000000000000000000000000001}}}`,
			want: "integer",
		},
		{
			name:   "secret const",
			raw:    `{"type":"object","properties":{"token":{"type":"string","const":"embedded-secret"}}}`,
			secret: []string{"token"},
			want:   "const",
		},
		{
			name:   "secret enum",
			raw:    `{"type":"object","properties":{"token":{"type":"string","enum":["candidate"]}}}`,
			secret: []string{"token"},
			want:   "enum",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schema, err := DecodeStartupVariableSchema([]byte(test.raw))
			if err != nil {
				t.Fatalf("DecodeStartupVariableSchema() error = %v", err)
			}
			err = schema.Validate(test.secret)
			if err == nil {
				t.Fatal("Validate() accepted a contradictory Startup variable schema")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %q, want %q context", err, test.want)
			}
		})
	}
}

func TestStartupVariableSchemaAcceptsMathematicalIntegerConstraintSyntax(t *testing.T) {
	raw := []byte(`{
		"type":"object",
		"properties":{
			"slots":{"type":"integer","default":1e3,"minimum":1.0,"maximum":2e3},
			"name":{"type":"string","minLength":1.0,"maxLength":64e0}
		}
	}`)
	schema, err := DecodeStartupVariableSchema(raw)
	if err != nil {
		t.Fatalf("DecodeStartupVariableSchema() rejected mathematical integer syntax: %v", err)
	}
	if err := schema.Validate(nil); err != nil {
		t.Fatalf("Validate() rejected mathematical integer syntax: %v", err)
	}
}

func TestParseStartupIntegerRejectsHugeExponentsWithBoundedAllocations(t *testing.T) {
	for _, value := range []json.Number{"1e1000000", "1e-1000000"} {
		t.Run(value.String(), func(t *testing.T) {
			if integer, ok := ParseStartupInteger(value); ok {
				t.Fatalf("ParseStartupInteger(%q) = %d, true; want rejection", value, integer)
			}
			allocations := testing.AllocsPerRun(3, func() {
				ParseStartupInteger(value)
			})
			if allocations > 8 {
				t.Fatalf("ParseStartupInteger(%q) allocated %.0f objects; want bounded lexical rejection", value, allocations)
			}
		})
	}
}

func TestParseStartupIntegerNormalizesExactJSONIntegerSyntax(t *testing.T) {
	valid := map[json.Number]int64{
		"0":                      0,
		"-0":                     0,
		"15":                     15,
		"15.0":                   15,
		"1.5e1":                  15,
		"150e-1":                 15,
		"1.50e+1":                15,
		"1e00000000000000000002": 100,
		"9223372036854775807":    9223372036854775807,
		"-9223372036854775808":   -9223372036854775808,
		"0e1000000":              0,
	}
	for input, want := range valid {
		t.Run("valid_"+input.String(), func(t *testing.T) {
			got, ok := ParseStartupInteger(input)
			if !ok || got != want {
				t.Fatalf("ParseStartupInteger(%q) = %d, %t; want %d, true", input, got, ok, want)
			}
		})
	}

	invalid := []json.Number{
		"", "+1", "01", ".1", "1.", "1e", "1e+", "1 ",
		"0.1", "150e-2", "9007199254740990.5",
		"9223372036854775808", "-9223372036854775809",
		"1e1000000", "1e-1000000",
	}
	for _, input := range invalid {
		t.Run("invalid_"+input.String(), func(t *testing.T) {
			if got, ok := ParseStartupInteger(input); ok {
				t.Fatalf("ParseStartupInteger(%q) = %d, true; want rejection", input, got)
			}
		})
	}
}
