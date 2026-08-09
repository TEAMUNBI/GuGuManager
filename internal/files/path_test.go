package files

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveWithinRootAllowsRelativeServerPath(t *testing.T) {
	root := t.TempDir()
	resolved, err := ResolveWithinRoot(root, "config/server.properties")
	if err != nil {
		t.Fatalf("ResolveWithinRoot returned error: %v", err)
	}
	want := filepath.Join(root, "config", "server.properties")
	if resolved != want {
		t.Fatalf("resolved path = %q, want %q", resolved, want)
	}
}

func TestNormalizeRelativeRejectsOversizedPortablePaths(t *testing.T) {
	for _, requested := range []string{
		strings.Join([]string{strings.Repeat("a", 205), strings.Repeat("b", 204), strings.Repeat("c", 204), strings.Repeat("d", 204), strings.Repeat("e", 204)}, "/"),
		strings.Repeat("a", 256) + "/file.txt",
	} {
		if _, err := NormalizeRelative(requested); !errors.Is(err, ErrPathLimit) {
			t.Fatalf("NormalizeRelative path length error = %v, want ErrPathLimit", err)
		}
	}
}

func TestResolveWithinRootRejectsTraversalAndAbsolutePaths(t *testing.T) {
	root := t.TempDir()
	for _, requested := range []string{"../secrets.txt", `..\\secrets.txt`, "/etc/passwd", `C:\\Windows\\win.ini`, `\\server\share\file.txt`, "bad\x00name"} {
		t.Run(requested, func(t *testing.T) {
			if _, err := ResolveWithinRoot(root, requested); err == nil {
				t.Fatalf("ResolveWithinRoot(%q) succeeded, want rejection", requested)
			}
		})
	}
}

func TestNormalizeRelativeReturnsPortablePath(t *testing.T) {
	got, err := NormalizeRelative(`config\\paper-global.yml`)
	if err != nil {
		t.Fatalf("NormalizeRelative returned error: %v", err)
	}
	if got != "config/paper-global.yml" {
		t.Fatalf("normalized path = %q", got)
	}
}
