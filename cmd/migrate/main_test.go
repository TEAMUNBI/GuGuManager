package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gugumanager/gugumanager/internal/migrations"
)

func TestPlanValidatesAndListsMigrationsWithoutExecutingSQL(t *testing.T) {
	directory := t.TempDir()
	writeMigration(t, directory, "000001_core.up.sql", "BEGIN;\nCREATE TABLE example (id bigint);\nCOMMIT;\n")
	writeMigration(t, directory, "000001_core.down.sql", "BEGIN;\nDROP TABLE example;\nCOMMIT;\n")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"-dir", directory, "plan"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	for _, expected := range []string{
		"000001_core",
		"up_sha256=",
		"down_sha256=",
		"no database connection was attempted",
		"no SQL was executed",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("stdout = %q, missing %q", stdout.String(), expected)
		}
	}
	if strings.Contains(strings.ToLower(stdout.String()), "applied") {
		t.Fatalf("dry-run output falsely claims application: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestPlanRejectsIncompleteMigrationPair(t *testing.T) {
	directory := t.TempDir()
	writeMigration(t, directory, "000001_core.up.sql", "BEGIN;\nSELECT 1;\nCOMMIT;\n")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"-dir", directory, "plan"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit code = 0, stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "missing down migration") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestPlanRejectsDuplicateOrGappedVersions(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  string
	}{
		{
			name: "duplicate version",
			files: []string{
				"000001_core.up.sql", "000001_core.down.sql",
				"000001_other.up.sql", "000001_other.down.sql",
			},
			want: "duplicate migration version 000001",
		},
		{
			name: "gapped version",
			files: []string{
				"000001_core.up.sql", "000001_core.down.sql",
				"000003_other.up.sql", "000003_other.down.sql",
			},
			want: "expected migration version 000002",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			for _, name := range test.files {
				writeMigration(t, directory, name, "BEGIN;\nSELECT 1;\nCOMMIT;\n")
			}
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := run([]string{"-dir", directory, "plan"}, &stdout, &stderr); code == 0 {
				t.Fatalf("exit code = 0, stdout = %q", stdout.String())
			}
			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), test.want)
			}
		})
	}
}

func TestPlanRejectsNonCanonicalSQLFilenameAndEmptyDirectory(t *testing.T) {
	tests := []struct {
		name  string
		setup func(string)
		want  string
	}{
		{
			name: "noncanonical SQL filename",
			setup: func(directory string) {
				writeMigration(t, directory, "1_core.up.sql", "SELECT 1;")
			},
			want: "non-canonical migration filename",
		},
		{
			name:  "empty directory",
			setup: func(string) {},
			want:  "no migrations found",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			test.setup(directory)
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := run([]string{"-dir", directory, "plan"}, &stdout, &stderr); code == 0 {
				t.Fatalf("exit code = 0, stdout = %q", stdout.String())
			}
			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), test.want)
			}
		})
	}
}

func TestRunRequiresExplicitPlanCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code == 0 {
		t.Fatal("run without a command succeeded")
	}
	if !strings.Contains(stderr.String(), "usage:") || !strings.Contains(stderr.String(), "plan") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestProjectPlanIncludesMembershipPermissionMigration(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate migration command test source")
	}
	plan, err := migrations.LoadMigrations(filepath.Join(filepath.Dir(currentFile), "..", "..", "migrations"))
	if err != nil {
		t.Fatalf("build project migration plan: %v", err)
	}
	if len(plan) != 11 {
		t.Fatalf("project migration count = %d, want 11", len(plan))
	}
	if plan[2].VersionKey != "000003" || plan[2].Name != "membership_permissions" {
		t.Fatalf("third migration = %+v, want 000003_membership_permissions", plan[2])
	}
	if plan[3].VersionKey != "000004" || plan[3].Name != "password_reset" {
		t.Fatalf("fourth migration = %+v, want 000004_password_reset", plan[3])
	}
	if plan[4].VersionKey != "000005" || plan[4].Name != "controlplane_stage1" {
		t.Fatalf("fifth migration = %+v, want 000005_controlplane_stage1", plan[4])
	}
	if plan[5].VersionKey != "000006" || plan[5].Name != "telemetry_persistence" {
		t.Fatalf("sixth migration = %+v, want 000006_telemetry_persistence", plan[5])
	}
	if plan[6].VersionKey != "000007" || plan[6].Name != "secret_handles" {
		t.Fatalf("seventh migration = %+v, want 000007_secret_handles", plan[6])
	}
	if plan[7].VersionKey != "000008" || plan[7].Name != "backup_failure_metadata" {
		t.Fatalf("eighth migration = %+v, want 000008_backup_failure_metadata", plan[7])
	}
	if plan[8].VersionKey != "000009" || plan[8].Name != "task_fencing" {
		t.Fatalf("ninth migration = %+v, want 000009_task_fencing", plan[8])
	}
	if plan[9].VersionKey != "000010" || plan[9].Name != "agent_enrollment" {
		t.Fatalf("tenth migration = %+v, want 000010_agent_enrollment", plan[9])
	}
	if plan[10].VersionKey != "000011" || plan[10].Name != "network_port_roles" {
		t.Fatalf("eleventh migration = %+v, want 000011_network_port_roles", plan[10])
	}
}

func writeMigration(t *testing.T, directory string, name string, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
