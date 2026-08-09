package files

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestServerFSWriteReadAndAtomicallyReplaceFile(t *testing.T) {
	serverFS, root := newTestServerFS(t, Limits{MaxReadBytes: 1024, MaxWriteBytes: 1024})

	if err := serverFS.Mkdir("config"); err != nil {
		t.Fatalf("Mkdir returned error: %v", err)
	}
	if err := serverFS.WriteFile("config/server.properties", []byte("motd=old\n")); err != nil {
		t.Fatalf("initial WriteFile returned error: %v", err)
	}
	if err := serverFS.WriteFile("config/server.properties", []byte("motd=new\n")); err != nil {
		t.Fatalf("replacement WriteFile returned error: %v", err)
	}

	content, err := serverFS.ReadFile("config/server.properties")
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if got, want := string(content), "motd=new\n"; got != want {
		t.Fatalf("ReadFile content = %q, want %q", got, want)
	}

	temporaryFiles, err := filepath.Glob(filepath.Join(root, "config", ".gugu-write-*"))
	if err != nil {
		t.Fatalf("Glob returned error: %v", err)
	}
	if len(temporaryFiles) != 0 {
		t.Fatalf("atomic write left temporary files: %v", temporaryFiles)
	}
}

func TestServerFSListReturnsValidatedImmediateChildren(t *testing.T) {
	serverFS, _ := newTestServerFS(t, testLimits())
	if err := serverFS.Mkdir("config"); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, serverFS, "config/server.properties", "motd=gugu")
	mustWriteFile(t, serverFS, "readme.txt", "hello")

	entries, err := serverFS.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || !entries[0].Directory || entries[0].Path != "config" || entries[1].Directory || entries[1].Path != "readme.txt" {
		t.Fatalf("root entries = %+v, want directory-first immediate children", entries)
	}

	entries, err = serverFS.List("config")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Path != "config/server.properties" || entries[0].SizeBytes != int64(len("motd=gugu")) {
		t.Fatalf("config entries = %+v", entries)
	}
}

func TestServerFSListRejectsSymlinkChild(t *testing.T) {
	serverFS, root := newTestServerFS(t, testLimits())
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, err := serverFS.List(""); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("List with symlink child error = %v, want ErrUnsafePath", err)
	}
}

func TestServerFSMkdirRequiresSafeExistingParent(t *testing.T) {
	serverFS, root := newTestServerFS(t, testLimits())

	if err := serverFS.Mkdir("world"); err != nil {
		t.Fatalf("Mkdir returned error: %v", err)
	}
	info, err := os.Stat(filepath.Join(root, "world"))
	if err != nil {
		t.Fatalf("Stat created directory: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("Mkdir target is not a directory")
	}

	if err := serverFS.Mkdir("missing/child"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Mkdir with missing parent error = %v, want fs.ErrNotExist", err)
	}
	if err := serverFS.Mkdir("world"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("duplicate Mkdir error = %v, want fs.ErrExist", err)
	}
}

func TestServerFSMoveDoesNotOverwriteUnlessRequested(t *testing.T) {
	serverFS, _ := newTestServerFS(t, testLimits())
	mustWriteFile(t, serverFS, "source.txt", "source")
	mustWriteFile(t, serverFS, "destination.txt", "destination")

	if err := serverFS.Move("source.txt", "destination.txt", false); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("Move without replace error = %v, want fs.ErrExist", err)
	}
	assertFileContent(t, serverFS, "source.txt", "source")
	assertFileContent(t, serverFS, "destination.txt", "destination")

	if err := serverFS.Move("source.txt", "destination.txt", true); err != nil {
		t.Fatalf("Move with replace returned error: %v", err)
	}
	if _, err := serverFS.ReadFile("source.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("source after Move error = %v, want fs.ErrNotExist", err)
	}
	assertFileContent(t, serverFS, "destination.txt", "source")
}

func TestServerFSMoveDirectoryAndRejectMoveIntoItself(t *testing.T) {
	serverFS, _ := newTestServerFS(t, testLimits())
	if err := serverFS.Mkdir("world"); err != nil {
		t.Fatalf("Mkdir world: %v", err)
	}
	mustWriteFile(t, serverFS, "world/level.dat", "level")
	if err := serverFS.Move("world", "save", false); err != nil {
		t.Fatalf("Move directory returned error: %v", err)
	}
	assertFileContent(t, serverFS, "save/level.dat", "level")

	if err := serverFS.Mkdir("save/region"); err != nil {
		t.Fatalf("Mkdir region: %v", err)
	}
	if err := serverFS.Move("save", "save/region/nested", false); !errors.Is(err, ErrInvalidMove) {
		t.Fatalf("Move directory into itself error = %v, want ErrInvalidMove", err)
	}
}

func TestServerFSDeleteFileEmptyDirectoryAndRecursiveTree(t *testing.T) {
	serverFS, _ := newTestServerFS(t, testLimits())
	mustWriteFile(t, serverFS, "eula.txt", "eula=true")
	if err := serverFS.Delete("eula.txt", false); err != nil {
		t.Fatalf("Delete file returned error: %v", err)
	}
	if _, err := serverFS.ReadFile("eula.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Read deleted file error = %v, want fs.ErrNotExist", err)
	}

	if err := serverFS.Mkdir("empty"); err != nil {
		t.Fatalf("Mkdir empty: %v", err)
	}
	if err := serverFS.Delete("empty", false); err != nil {
		t.Fatalf("Delete empty directory returned error: %v", err)
	}

	if err := serverFS.Mkdir("tree"); err != nil {
		t.Fatalf("Mkdir tree: %v", err)
	}
	if err := serverFS.Mkdir("tree/nested"); err != nil {
		t.Fatalf("Mkdir nested: %v", err)
	}
	mustWriteFile(t, serverFS, "tree/nested/data.txt", "data")
	if err := serverFS.Delete("tree", false); err == nil {
		t.Fatal("non-recursive Delete of non-empty directory succeeded")
	}
	if err := serverFS.Delete("tree", true); err != nil {
		t.Fatalf("recursive Delete returned error: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(serverFS.root, "tree")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("deleted tree Lstat error = %v, want fs.ErrNotExist", err)
	}
}

func TestServerFSRejectsTraversalAbsoluteAndRootMutation(t *testing.T) {
	serverFS, _ := newTestServerFS(t, testLimits())
	mustWriteFile(t, serverFS, "inside.txt", "inside")

	for _, requested := range []string{"../outside.txt", "config/../outside.txt", `..\\outside.txt`, "/etc/passwd", `C:\\Windows\\win.ini`, `\\\\server\share\file.txt`, "bad\x00name"} {
		t.Run(requested, func(t *testing.T) {
			if _, err := serverFS.ReadFile(requested); !errors.Is(err, ErrPathEscape) {
				t.Fatalf("ReadFile(%q) error = %v, want ErrPathEscape", requested, err)
			}
			if err := serverFS.WriteFile(requested, []byte("x")); !errors.Is(err, ErrPathEscape) {
				t.Fatalf("WriteFile(%q) error = %v, want ErrPathEscape", requested, err)
			}
			if err := serverFS.Mkdir(requested); !errors.Is(err, ErrPathEscape) {
				t.Fatalf("Mkdir(%q) error = %v, want ErrPathEscape", requested, err)
			}
			if err := serverFS.Delete(requested, true); !errors.Is(err, ErrPathEscape) {
				t.Fatalf("Delete(%q) error = %v, want ErrPathEscape", requested, err)
			}
			if err := serverFS.Move("inside.txt", requested, false); !errors.Is(err, ErrPathEscape) {
				t.Fatalf("Move destination %q error = %v, want ErrPathEscape", requested, err)
			}
		})
	}

	for _, requested := range []string{"", "."} {
		if err := serverFS.WriteFile(requested, []byte("x")); !errors.Is(err, ErrRootMutation) {
			t.Fatalf("WriteFile root error = %v, want ErrRootMutation", err)
		}
		if err := serverFS.Delete(requested, true); !errors.Is(err, ErrRootMutation) {
			t.Fatalf("Delete root error = %v, want ErrRootMutation", err)
		}
	}
}

func TestServerFSRejectsSymlinkAndLinkEscapeForEveryOperation(t *testing.T) {
	serverFS, root := newTestServerFS(t, testLimits())
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside secret: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "secret-link")); err != nil {
		t.Fatalf("create file symlink: %v", err)
	}
	mustWriteFile(t, serverFS, "inside.txt", "inside")

	checks := []struct {
		name string
		run  func() error
	}{
		{name: "read through directory link", run: func() error { _, err := serverFS.ReadFile("escape/secret.txt"); return err }},
		{name: "read file link", run: func() error { _, err := serverFS.ReadFile("secret-link"); return err }},
		{name: "write through directory link", run: func() error { return serverFS.WriteFile("escape/new.txt", []byte("new")) }},
		{name: "mkdir through directory link", run: func() error { return serverFS.Mkdir("escape/new") }},
		{name: "move through directory link", run: func() error { return serverFS.Move("inside.txt", "escape/moved.txt", false) }},
		{name: "delete directory link", run: func() error { return serverFS.Delete("escape", true) }},
		{name: "delete file link", run: func() error { return serverFS.Delete("secret-link", false) }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.run(); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("error = %v, want ErrUnsafePath", err)
			}
		})
	}

	content, err := os.ReadFile(filepath.Join(outside, "secret.txt"))
	if err != nil {
		t.Fatalf("read outside secret: %v", err)
	}
	if string(content) != "secret" {
		t.Fatalf("outside secret changed to %q", content)
	}
	if _, err := os.Lstat(filepath.Join(outside, "new.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("outside new file exists or Lstat failed: %v", err)
	}
}

func TestServerFSRecursiveDeletePreflightsLinksBeforeRemovingAnything(t *testing.T) {
	serverFS, root := newTestServerFS(t, testLimits())
	outside := t.TempDir()
	if err := serverFS.Mkdir("tree"); err != nil {
		t.Fatalf("Mkdir tree: %v", err)
	}
	mustWriteFile(t, serverFS, "tree/keep.txt", "keep")
	if err := os.Symlink(outside, filepath.Join(root, "tree", "unsafe")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	if err := serverFS.Delete("tree", true); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Delete tree with symlink error = %v, want ErrUnsafePath", err)
	}
	assertFileContent(t, serverFS, "tree/keep.txt", "keep")
}

func TestServerFSRejectsDirectoriesAsFilesAndPortableDeviceNames(t *testing.T) {
	serverFS, _ := newTestServerFS(t, testLimits())
	if err := serverFS.Mkdir("directory"); err != nil {
		t.Fatalf("Mkdir directory: %v", err)
	}
	if _, err := serverFS.ReadFile("directory"); !errors.Is(err, ErrNotRegularFile) {
		t.Fatalf("ReadFile(directory) error = %v, want ErrNotRegularFile", err)
	}
	if err := serverFS.WriteFile("directory", []byte("data")); !errors.Is(err, ErrNotRegularFile) {
		t.Fatalf("WriteFile(directory) error = %v, want ErrNotRegularFile", err)
	}

	for _, requested := range []string{"NUL", "con.txt", "config/COM1.log", "name:stream"} {
		if err := serverFS.WriteFile(requested, []byte("data")); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("WriteFile(%q) error = %v, want ErrUnsafePath", requested, err)
		}
	}
}

func TestServerFSEnforcesReadAndWriteSizeLimits(t *testing.T) {
	serverFS, root := newTestServerFS(t, Limits{MaxReadBytes: 4, MaxWriteBytes: 4})
	if err := serverFS.WriteFile("small.txt", []byte("1234")); err != nil {
		t.Fatalf("WriteFile at limit returned error: %v", err)
	}
	if err := serverFS.WriteFile("small.txt", []byte("12345")); !errors.Is(err, ErrSizeLimit) {
		t.Fatalf("oversized WriteFile error = %v, want ErrSizeLimit", err)
	}
	assertFileContent(t, serverFS, "small.txt", "1234")

	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte("12345"), 0o600); err != nil {
		t.Fatalf("write large fixture: %v", err)
	}
	if _, err := serverFS.ReadFile("large.txt"); !errors.Is(err, ErrSizeLimit) {
		t.Fatalf("oversized ReadFile error = %v, want ErrSizeLimit", err)
	}
}

func TestNewServerFSRejectsInvalidRootAndLimits(t *testing.T) {
	root := t.TempDir()
	if _, err := NewServerFS(root, Limits{}); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("NewServerFS with empty limits error = %v, want fs.ErrInvalid", err)
	}

	fileRoot := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(fileRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write root fixture: %v", err)
	}
	if _, err := NewServerFS(fileRoot, testLimits()); !errors.Is(err, ErrNotDirectory) {
		t.Fatalf("NewServerFS with file root error = %v, want ErrNotDirectory", err)
	}
}

func newTestServerFS(t *testing.T, limits Limits) (*ServerFS, string) {
	t.Helper()
	root := t.TempDir()
	serverFS, err := NewServerFS(root, limits)
	if err != nil {
		t.Fatalf("NewServerFS returned error: %v", err)
	}
	return serverFS, root
}

func testLimits() Limits {
	return Limits{MaxReadBytes: 1024 * 1024, MaxWriteBytes: 1024 * 1024}
}

func mustWriteFile(t *testing.T, serverFS *ServerFS, requested string, content string) {
	t.Helper()
	if err := serverFS.WriteFile(requested, []byte(content)); err != nil {
		t.Fatalf("WriteFile(%q) returned error: %v", requested, err)
	}
}

func assertFileContent(t *testing.T, serverFS *ServerFS, requested string, want string) {
	t.Helper()
	content, err := serverFS.ReadFile(requested)
	if err != nil {
		t.Fatalf("ReadFile(%q) returned error: %v", requested, err)
	}
	if got := string(content); got != want {
		t.Fatalf("ReadFile(%q) = %q, want %q", requested, got, want)
	}
}
