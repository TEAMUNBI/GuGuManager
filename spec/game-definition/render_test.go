package gamedefinition

import (
	"strings"
	"testing"
)

func TestRenderStartupMaterializesArgumentsFilesAndEnvironment(t *testing.T) {
	bindings := []Binding{
		{Variable: "memory_mb", Target: "argument", Template: "-Xmx{{ value }}M"},
		{Variable: "accept_eula", Target: "file", Path: "eula.txt", Template: "eula={{ value }}\n"},
		{Variable: "rcon_password", Target: "environment", Name: "RCON_PASSWORD", Template: "{{ value }}"},
	}
	values := map[string]any{
		"memory_mb":     int64(2048),
		"accept_eula":   true,
		"rcon_password": "correct-horse",
	}
	rendered, err := RenderStartup(
		"java",
		[]string{"{{ memory_mb }}", "-jar", "paper.jar", "--nogui"},
		bindings, values, []string{"rcon_password"},
	)
	if err != nil {
		t.Fatalf("RenderStartup() error = %v", err)
	}
	wantArgs := []string{"-Xmx2048M", "-jar", "paper.jar", "--nogui"}
	if len(rendered.Args) != len(wantArgs) {
		t.Fatalf("Args = %q, want %q", rendered.Args, wantArgs)
	}
	for index, want := range wantArgs {
		if rendered.Args[index] != want {
			t.Fatalf("Args[%d] = %q, want %q", index, rendered.Args[index], want)
		}
	}
	if len(rendered.Files) != 1 || rendered.Files[0].Path != "eula.txt" || rendered.Files[0].Content != "eula=true\n" {
		t.Fatalf("Files = %+v, want eula.txt containing eula=true", rendered.Files)
	}
	if rendered.Files[0].Secret {
		t.Fatal("Files[0].Secret = true, want false for a non-secret variable")
	}
	if len(rendered.Environment) != 1 || rendered.Environment[0].Name != "RCON_PASSWORD" || rendered.Environment[0].Value != "correct-horse" {
		t.Fatalf("Environment = %+v, want RCON_PASSWORD", rendered.Environment)
	}
	if !rendered.Environment[0].Secret {
		t.Fatal("Environment[0].Secret = false, want true for a declared secret")
	}
}

func TestRenderStartupSubstitutesWholeArgumentElements(t *testing.T) {
	rendered, err := RenderStartup(
		"/srv/game/server",
		[]string{"--name", "{{ server_name }}"},
		[]Binding{{Variable: "server_name", Target: "argument", Template: "{{ value }}"}},
		map[string]any{"server_name": "Friday Factory --rcon-password leak"},
		nil,
	)
	if err != nil {
		t.Fatalf("RenderStartup() error = %v", err)
	}
	if len(rendered.Args) != 2 {
		t.Fatalf("Args = %q, want a value to stay one argument element", rendered.Args)
	}
	if rendered.Args[1] != "Friday Factory --rcon-password leak" {
		t.Fatalf("Args[1] = %q, want the whole value in a single element", rendered.Args[1])
	}
}

func TestRenderStartupRejectsSecretInCommandArgument(t *testing.T) {
	_, err := RenderStartup(
		"java",
		[]string{"{{ token }}"},
		[]Binding{{Variable: "token", Target: "argument", Template: "--token={{ value }}"}},
		map[string]any{"token": "s3cret"},
		[]string{"token"},
	)
	if err == nil {
		t.Fatal("RenderStartup() accepted a secret bound into a command argument")
	}
	if !strings.Contains(err.Error(), "secret") {
		t.Fatalf("error = %q, want it to name the secret boundary", err)
	}
}

func TestRenderStartupRejectsControlCharactersInRenderedValues(t *testing.T) {
	_, err := RenderStartup(
		"/srv/game/server",
		nil,
		[]Binding{{Variable: "server_name", Target: "file", Path: "server.properties", Template: "server-name={{ value }}\n"}},
		map[string]any{"server_name": "Aurora\nrcon.password=injected"},
		nil,
	)
	if err == nil {
		t.Fatal("RenderStartup() accepted a newline that rewrites the configuration file")
	}
	if !strings.Contains(err.Error(), "control character") {
		t.Fatalf("error = %q, want a control-character diagnostic", err)
	}
}

func TestRenderStartupSkipsUnsetOptionalMaterializations(t *testing.T) {
	rendered, err := RenderStartup(
		"/srv/game/server",
		nil,
		[]Binding{
			{Variable: "token", Target: "environment", Name: "TOKEN", Template: "{{ value }}"},
			{Variable: "motd", Target: "file", Path: "motd.txt", Template: "{{ value }}"},
		},
		map[string]any{},
		[]string{"token"},
	)
	if err != nil {
		t.Fatalf("RenderStartup() error = %v", err)
	}
	if len(rendered.Environment) != 0 || len(rendered.Files) != 0 {
		t.Fatalf("rendered = %+v, want unset optional bindings to materialize nothing", rendered)
	}
}

func TestRenderStartupRequiresValueForArgumentBinding(t *testing.T) {
	_, err := RenderStartup(
		"java",
		[]string{"{{ memory_mb }}"},
		[]Binding{{Variable: "memory_mb", Target: "argument", Template: "-Xmx{{ value }}M"}},
		map[string]any{},
		nil,
	)
	if err == nil {
		t.Fatal("RenderStartup() accepted an argument binding with no value")
	}
	if !strings.Contains(err.Error(), "no value") {
		t.Fatalf("error = %q, want it to report the missing value", err)
	}
}

func TestRenderStartupRejectsUnresolvedAndUnsupportedPlaceholders(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		bindings []Binding
		values   map[string]any
		want     string
	}{
		{
			name:     "template missing the value placeholder",
			bindings: []Binding{{Variable: "memory_mb", Target: "argument", Template: "-Xmx2048M"}},
			values:   map[string]any{"memory_mb": int64(2048)},
			want:     ValuePlaceholder,
		},
		{
			name:     "template with a misspelled placeholder",
			bindings: []Binding{{Variable: "memory_mb", Target: "file", Path: "memory.txt", Template: "{{ value }}{{ valu }}"}},
			values:   map[string]any{"memory_mb": int64(2048)},
			want:     "unsupported placeholder",
		},
		{
			name: "argument left unresolved by any binding",
			args: []string{"{{ memory_mb }}"},
			want: "without an argument binding",
		},
		{
			name: "argument containing a stray placeholder",
			args: []string{"-Xmx{{ memory_mb }}M"},
			want: "unresolved placeholder",
		},
		{
			name:     "argument binding with no placeholder in args",
			args:     []string{"-jar", "paper.jar"},
			bindings: []Binding{{Variable: "memory_mb", Target: "argument", Template: "-Xmx{{ value }}M"}},
			values:   map[string]any{"memory_mb": int64(2048)},
			want:     "has no",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := RenderStartup("java", test.args, test.bindings, test.values, nil)
			if err == nil {
				t.Fatalf("RenderStartup() accepted %s", test.name)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %q, want it to contain %q", err, test.want)
			}
		})
	}
}

func TestRenderStartupRejectsInvalidBindingTargets(t *testing.T) {
	tests := []struct {
		name     string
		bindings []Binding
		want     string
	}{
		{
			name:     "unsupported target",
			bindings: []Binding{{Variable: "motd", Target: "registry", Template: "{{ value }}"}},
			want:     "not supported",
		},
		{
			name:     "invalid environment name",
			bindings: []Binding{{Variable: "motd", Target: "environment", Name: "not-a-name", Template: "{{ value }}"}},
			want:     "environment variable name",
		},
		{
			name: "duplicate environment name",
			bindings: []Binding{
				{Variable: "motd", Target: "environment", Name: "MOTD", Template: "{{ value }}"},
				{Variable: "slots", Target: "environment", Name: "MOTD", Template: "{{ value }}"},
			},
			want: "duplicates",
		},
		{
			name: "duplicate file path",
			bindings: []Binding{
				{Variable: "motd", Target: "file", Path: "motd.txt", Template: "{{ value }}"},
				{Variable: "slots", Target: "file", Path: "motd.txt", Template: "{{ value }}"},
			},
			want: "duplicates",
		},
		{
			name:     "file path escaping the data root",
			bindings: []Binding{{Variable: "motd", Target: "file", Path: "../motd.txt", Template: "{{ value }}"}},
			want:     "safe relative Bundle target",
		},
		{
			name:     "non-canonical file path",
			bindings: []Binding{{Variable: "motd", Target: "file", Path: "./motd.txt", Template: "{{ value }}"}},
			want:     "not canonical",
		},
		{
			name:     "invalid variable identifier",
			bindings: []Binding{{Variable: "not-a-variable", Target: "file", Path: "motd.txt", Template: "{{ value }}"}},
			want:     "variable identifier",
		},
	}
	values := map[string]any{"motd": "hello", "slots": int64(8), "not-a-variable": "x"}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := RenderStartup("java", nil, test.bindings, values, nil)
			if err == nil {
				t.Fatalf("RenderStartup() accepted %s", test.name)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %q, want it to contain %q", err, test.want)
			}
		})
	}
}

func TestRenderScalarFormatsSupportedTypes(t *testing.T) {
	tests := []struct {
		value any
		want  string
	}{
		{value: "text", want: "text"},
		{value: int64(2048), want: "2048"},
		{value: int64(-1), want: "-1"},
		{value: MaxSafeStartupInteger, want: "9007199254740991"},
		{value: true, want: "true"},
		{value: false, want: "false"},
	}
	for _, test := range tests {
		got, err := RenderScalar(test.value)
		if err != nil {
			t.Fatalf("RenderScalar(%v) error = %v", test.value, err)
		}
		if got != test.want {
			t.Fatalf("RenderScalar(%v) = %q, want %q", test.value, got, test.want)
		}
	}
	if _, err := RenderScalar(3.5); err == nil {
		t.Fatal("RenderScalar() accepted a float, which the closed variable subset excludes")
	}
}
