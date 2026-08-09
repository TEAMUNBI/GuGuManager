//go:build !windows

package files

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"
)

func TestServerFSRejectsNamedPipesAsFiles(t *testing.T) {
	serverFS, root := newTestServerFS(t, testLimits())
	pipe := filepath.Join(root, "server.pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Skipf("create named pipe: %v", err)
	}

	if _, err := serverFS.ReadFile("server.pipe"); !errors.Is(err, ErrUnsupportedFileType) {
		t.Fatalf("ReadFile(named pipe) error = %v, want ErrUnsupportedFileType", err)
	}
	if err := serverFS.WriteFile("server.pipe", []byte("data")); !errors.Is(err, ErrUnsupportedFileType) {
		t.Fatalf("WriteFile(named pipe) error = %v, want ErrUnsupportedFileType", err)
	}
	if err := serverFS.Delete("server.pipe", false); !errors.Is(err, ErrUnsupportedFileType) {
		t.Fatalf("Delete(named pipe) error = %v, want ErrUnsupportedFileType", err)
	}
}
