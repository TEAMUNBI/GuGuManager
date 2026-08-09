package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	serverfiles "github.com/gugumanager/gugumanager/internal/files"
)

const testMaxBytes = int64(1 << 20)

func newTestDestination(t *testing.T) (*serverfiles.ServerFS, string) {
	t.Helper()
	root := t.TempDir()
	filesystem, err := serverfiles.NewServerFS(root, serverfiles.Limits{
		MaxReadBytes:  testMaxBytes,
		MaxWriteBytes: testMaxBytes,
	})
	if err != nil {
		t.Fatalf("NewServerFS returned error: %v", err)
	}
	return filesystem, root
}

func digestOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// serveArtifacts starts a TLS server returning body per request path and yields
// options wired to its client, so tests exercise the fetch path without the
// guarded dialer rejecting loopback.
func serveArtifacts(t *testing.T, bodies map[string]string) (*httptest.Server, Options) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, ok := bodies[request.URL.Path]
		if !ok {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = writer.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "https://")
	if index := strings.LastIndex(host, ":"); index != -1 {
		host = host[:index]
	}
	return server, Options{
		Allowlist:        []string{host},
		MaxArtifactBytes: testMaxBytes,
		Client:           server.Client(),
	}
}

func TestInstallFetchesVerifiesAndPlacesArtifacts(t *testing.T) {
	const jar = "PK\x03\x04 fake paper jar"
	const config = "motd=hello\n"
	server, options := serveArtifacts(t, map[string]string{
		"/paper.jar":         jar,
		"/nested/config.yml": config,
	})
	destination, root := newTestDestination(t)

	results, err := Install(context.Background(), destination, []Artifact{
		{URL: server.URL + "/paper.jar", Destination: "paper.jar", SHA256: digestOf(jar)},
		{URL: server.URL + "/nested/config.yml", Destination: "config/server.yml", SHA256: digestOf(config)},
	}, options)
	if err != nil {
		t.Fatalf("Install returned error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Install returned %d results, want 2", len(results))
	}
	if got, want := results[0].SizeBytes, int64(len(jar)); got != want {
		t.Fatalf("results[0].SizeBytes = %d, want %d", got, want)
	}
	if got, want := results[1].Destination, "config/server.yml"; got != want {
		t.Fatalf("results[1].Destination = %q, want %q", got, want)
	}

	content, err := destination.ReadFile("paper.jar")
	if err != nil {
		t.Fatalf("ReadFile(paper.jar) returned error: %v", err)
	}
	if got := string(content); got != jar {
		t.Fatalf("paper.jar content = %q, want %q", got, jar)
	}
	// The nested destination directory is created on demand.
	nested, err := destination.ReadFile("config/server.yml")
	if err != nil {
		t.Fatalf("ReadFile(config/server.yml) returned error: %v", err)
	}
	if got := string(nested); got != config {
		t.Fatalf("config/server.yml content = %q, want %q", got, config)
	}
	assertNoTemporaryFiles(t, root)
}

func TestInstallRejectsDigestMismatchWithoutWritingDestination(t *testing.T) {
	const body = "real bytes"
	server, options := serveArtifacts(t, map[string]string{"/paper.jar": body})
	destination, root := newTestDestination(t)

	_, err := Install(context.Background(), destination, []Artifact{
		{URL: server.URL + "/paper.jar", Destination: "paper.jar", SHA256: digestOf("different bytes")},
	}, options)
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("Install error = %v, want ErrDigestMismatch", err)
	}
	if _, err := destination.ReadFile("paper.jar"); err == nil {
		t.Fatal("digest mismatch left paper.jar in place, want no destination file")
	}
	assertNoTemporaryFiles(t, root)
}

func TestInstallReturnsCompletedResultsWhenALaterArtifactFails(t *testing.T) {
	const first = "first artifact"
	server, options := serveArtifacts(t, map[string]string{"/first.bin": first})
	destination, _ := newTestDestination(t)

	results, err := Install(context.Background(), destination, []Artifact{
		{URL: server.URL + "/first.bin", Destination: "first.bin", SHA256: digestOf(first)},
		{URL: server.URL + "/missing.bin", Destination: "second.bin", SHA256: digestOf("whatever")},
	}, options)
	if !errors.Is(err, ErrUnexpectedStatus) {
		t.Fatalf("Install error = %v, want ErrUnexpectedStatus", err)
	}
	if len(results) != 1 {
		t.Fatalf("Install returned %d results, want 1 completed before the failure", len(results))
	}
	if got, want := results[0].Destination, "first.bin"; got != want {
		t.Fatalf("results[0].Destination = %q, want %q", got, want)
	}
	if _, err := destination.ReadFile("first.bin"); err != nil {
		t.Fatalf("earlier artifact should remain installed: %v", err)
	}
}

func TestInstallEnforcesTransportAndManifestRules(t *testing.T) {
	valid := digestOf("x")
	testCases := []struct {
		name      string
		artifacts []Artifact
		allowlist []string
		wantErr   error
	}{
		{
			name:      "plain http is refused",
			artifacts: []Artifact{{URL: "http://cdn.example.com/a.jar", Destination: "a.jar", SHA256: valid}},
			allowlist: []string{"cdn.example.com"},
			wantErr:   ErrNotHTTPS,
		},
		{
			name:      "host outside the allowlist is refused",
			artifacts: []Artifact{{URL: "https://elsewhere.example.com/a.jar", Destination: "a.jar", SHA256: valid}},
			allowlist: []string{"cdn.example.com"},
			wantErr:   ErrHostNotAllowed,
		},
		{
			name:      "an empty allowlist grants nothing",
			artifacts: []Artifact{{URL: "https://cdn.example.com/a.jar", Destination: "a.jar", SHA256: valid}},
			allowlist: nil,
			wantErr:   ErrHostNotAllowed,
		},
		{
			name:      "short digest is refused",
			artifacts: []Artifact{{URL: "https://cdn.example.com/a.jar", Destination: "a.jar", SHA256: "abc123"}},
			allowlist: []string{"cdn.example.com"},
			wantErr:   ErrInvalidDigest,
		},
		{
			name:      "uppercase digest is refused",
			artifacts: []Artifact{{URL: "https://cdn.example.com/a.jar", Destination: "a.jar", SHA256: strings.ToUpper(valid)}},
			allowlist: []string{"cdn.example.com"},
			wantErr:   ErrInvalidDigest,
		},
		{
			name: "two artifacts cannot claim one destination",
			artifacts: []Artifact{
				{URL: "https://cdn.example.com/a.jar", Destination: "server.jar", SHA256: valid},
				{URL: "https://cdn.example.com/b.jar", Destination: "./server.jar", SHA256: valid},
			},
			allowlist: []string{"cdn.example.com"},
			wantErr:   ErrDuplicateTarget,
		},
		{
			name:      "traversal destination is refused",
			artifacts: []Artifact{{URL: "https://cdn.example.com/a.jar", Destination: "../escape.jar", SHA256: valid}},
			allowlist: []string{"cdn.example.com"},
			wantErr:   serverfiles.ErrPathEscape,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			destination, _ := newTestDestination(t)
			_, err := Install(context.Background(), destination, testCase.artifacts, Options{
				Allowlist:        testCase.allowlist,
				MaxArtifactBytes: testMaxBytes,
			})
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("Install error = %v, want %v", err, testCase.wantErr)
			}
		})
	}
}

func TestInstallRefusesDestinationWhoseWriteCeilingIsTooLow(t *testing.T) {
	root := t.TempDir()
	editorFS, err := serverfiles.NewServerFS(root, serverfiles.Limits{
		MaxReadBytes:  8 << 20,
		MaxWriteBytes: 8 << 20,
	})
	if err != nil {
		t.Fatalf("NewServerFS returned error: %v", err)
	}

	_, err = Install(context.Background(), editorFS, []Artifact{
		{URL: "https://cdn.example.com/a.jar", Destination: "a.jar", SHA256: digestOf("x")},
	}, Options{Allowlist: []string{"cdn.example.com"}})
	if !errors.Is(err, serverfiles.ErrSizeLimit) {
		t.Fatalf("Install error = %v, want ErrSizeLimit", err)
	}
	if !strings.Contains(err.Error(), "per file") {
		t.Fatalf("Install error %q should explain the destination ceiling", err)
	}
}

func TestInstallRejectsOversizeArtifact(t *testing.T) {
	body := strings.Repeat("a", 4096)
	server, options := serveArtifacts(t, map[string]string{"/big.bin": body})
	options.MaxArtifactBytes = 1024
	destination, root := newTestDestination(t)

	_, err := Install(context.Background(), destination, []Artifact{
		{URL: server.URL + "/big.bin", Destination: "big.bin", SHA256: digestOf(body)},
	}, options)
	if !errors.Is(err, serverfiles.ErrSizeLimit) {
		t.Fatalf("Install error = %v, want ErrSizeLimit", err)
	}
	if _, err := destination.ReadFile("big.bin"); err == nil {
		t.Fatal("oversize artifact was installed, want no destination file")
	}
	assertNoTemporaryFiles(t, root)
}

func TestInstallRequiresDestinationFilesystem(t *testing.T) {
	_, err := Install(context.Background(), nil, nil, Options{})
	if err == nil {
		t.Fatal("Install with nil destination returned no error")
	}
}

func TestInstallRejectsRootAsDestination(t *testing.T) {
	const body = "bytes"
	server, options := serveArtifacts(t, map[string]string{"/a.jar": body})
	destination, _ := newTestDestination(t)

	_, err := Install(context.Background(), destination, []Artifact{
		{URL: server.URL + "/a.jar", Destination: ".", SHA256: digestOf(body)},
	}, options)
	if !errors.Is(err, serverfiles.ErrRootMutation) {
		t.Fatalf("Install error = %v, want ErrRootMutation", err)
	}
}

func TestInstallRevalidatesRedirectTargets(t *testing.T) {
	const body = "redirected bytes"
	var origin *httptest.Server
	origin = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/allowed.bin":
			http.Redirect(writer, request, origin.URL+"/final.bin", http.StatusFound)
		case "/final.bin":
			_, _ = writer.Write([]byte(body))
		case "/offsite.bin":
			http.Redirect(writer, request, "https://elsewhere.example.com/final.bin", http.StatusFound)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(origin.Close)

	host := strings.TrimPrefix(origin.URL, "https://")
	if index := strings.LastIndex(host, ":"); index != -1 {
		host = host[:index]
	}
	options := Options{
		Allowlist:        []string{host},
		MaxArtifactBytes: testMaxBytes,
		Client:           origin.Client(),
	}

	destination, _ := newTestDestination(t)
	if _, err := Install(context.Background(), destination, []Artifact{
		{URL: origin.URL + "/allowed.bin", Destination: "ok.bin", SHA256: digestOf(body)},
	}, options); err != nil {
		t.Fatalf("Install following an in-allowlist redirect returned error: %v", err)
	}

	offsiteFS, _ := newTestDestination(t)
	_, err := Install(context.Background(), offsiteFS, []Artifact{
		{URL: origin.URL + "/offsite.bin", Destination: "bad.bin", SHA256: digestOf(body)},
	}, options)
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("Install error for offsite redirect = %v, want ErrHostNotAllowed", err)
	}
}

func TestInstallStopsWhenContextIsCancelled(t *testing.T) {
	const body = "bytes"
	server, options := serveArtifacts(t, map[string]string{"/a.jar": body})
	destination, _ := newTestDestination(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Install(ctx, destination, []Artifact{
		{URL: server.URL + "/a.jar", Destination: "a.jar", SHA256: digestOf(body)},
	}, options)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Install error = %v, want context.Canceled", err)
	}
}

// assertNoTemporaryFiles fails when an aborted or completed write left a
// staging file anywhere under the data root.
func assertNoTemporaryFiles(t *testing.T, root string) {
	t.Helper()
	var leftovers []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".gugu-write-") {
			leftovers = append(leftovers, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir returned error: %v", err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("install left temporary files behind: %v", leftovers)
	}
}
