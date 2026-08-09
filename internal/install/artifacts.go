// Package install implements the builtin.artifacts lifecycle handler: it
// fetches the artifacts a GameDefinition declares under spec.install and
// installs them into a server data directory.
//
// The package enforces the transport rules the Bundle contract requires rather
// than trusting the manifest: HTTPS only, every request host present in the
// declared network allowlist, every resolved address publicly routable, a byte
// ceiling on each response, and a full SHA-256 content digest verified before
// anything is committed. Destination paths are resolved through
// files.ServerFS, so an artifact cannot escape the server data root.
package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	serverfiles "github.com/gugumanager/gugumanager/internal/files"
)

var (
	ErrNotHTTPS         = errors.New("artifact URL must use https")
	ErrHostNotAllowed   = errors.New("artifact host is not in the network allowlist")
	ErrPrivateAddress   = errors.New("artifact host resolves to a non-routable address")
	ErrDigestMismatch   = errors.New("artifact content does not match the declared sha256")
	ErrInvalidDigest    = errors.New("artifact sha256 must be 64 lowercase hex characters")
	ErrDuplicateTarget  = errors.New("artifact destination duplicates another artifact")
	ErrUnexpectedStatus = errors.New("artifact host returned an unexpected status")
)

// Artifact mirrors one entry of spec.install.artifacts. The zero value is not
// installable; every field is required by the schema.
type Artifact struct {
	URL         string
	Destination string
	SHA256      string
}

// Options bounds a single install run. A zero field takes the documented
// default rather than an unbounded value.
type Options struct {
	// Allowlist holds the hostnames from spec.install.networkAllowlist. An
	// empty allowlist installs nothing: absent an explicit grant there is no
	// host an artifact may be fetched from.
	Allowlist []string
	// MaxArtifactBytes caps each response body. Defaults to
	// DefaultMaxArtifactBytes.
	MaxArtifactBytes int64
	// RequestTimeout bounds each individual fetch. Defaults to
	// DefaultRequestTimeout.
	RequestTimeout time.Duration
	// MaxRedirects bounds redirect hops. Every hop is revalidated against the
	// allowlist and the address guard. Defaults to DefaultMaxRedirects.
	MaxRedirects int
	// Client fetches artifacts. When nil a client with an SSRF-guarded dialer
	// is built from Allowlist. Tests inject a client here; production paths
	// should leave it nil so the guard applies.
	Client *http.Client
}

const (
	DefaultMaxArtifactBytes = int64(2 << 30)
	DefaultRequestTimeout   = 10 * time.Minute
	DefaultMaxRedirects     = 5
)

// Result reports what one artifact installed.
type Result struct {
	Destination string
	SizeBytes   int64
	SHA256      string
}

// Install fetches and installs every artifact into destination, which must be
// the server data directory. Artifacts install in declaration order; the first
// failure aborts the run and returns the results completed so far, so a caller
// can report partial progress on a failed install operation.
//
// Install is not atomic across artifacts: each file commits atomically on its
// own, but a mid-run failure leaves earlier artifacts in place. Callers drive
// this from an install operation that owns the whole data directory and can
// discard it on failure.
func Install(ctx context.Context, destination *serverfiles.ServerFS, artifacts []Artifact, options Options) ([]Result, error) {
	if destination == nil {
		return nil, fmt.Errorf("install: destination filesystem is required")
	}
	if err := validateArtifacts(artifacts); err != nil {
		return nil, err
	}
	options = options.withDefaults()
	// The destination enforces its own write ceiling. A handle sized for the
	// browser file editor would abort a real artifact partway through the
	// download, so refuse up front instead.
	if ceiling := destination.Limits().MaxWriteBytes; ceiling < options.MaxArtifactBytes {
		return nil, fmt.Errorf("install: destination accepts at most %d bytes per file, need %d: %w",
			ceiling, options.MaxArtifactBytes, serverfiles.ErrSizeLimit)
	}
	allowed, err := normalizeAllowlist(options.Allowlist)
	if err != nil {
		return nil, err
	}
	// Check every host before fetching anything. The guarded dialer enforces
	// this too, but doing it here means the rule holds even when a caller
	// injects its own client, and one unlisted host fails the run before it
	// downloads bytes it would have to discard.
	for index, artifact := range artifacts {
		host := hostOf(artifact.URL)
		if !hostAllowed(allowed, host) {
			return nil, fmt.Errorf("install.artifacts[%d].url %q: %w: %s",
				index, artifact.URL, ErrHostNotAllowed, host)
		}
	}
	client, err := options.clientWith(allowed)
	if err != nil {
		return nil, err
	}

	results := make([]Result, 0, len(artifacts))
	for index, artifact := range artifacts {
		result, err := installOne(ctx, destination, artifact, options, client)
		if err != nil {
			return results, fmt.Errorf("install.artifacts[%d]: %w", index, err)
		}
		results = append(results, result)
	}
	return results, nil
}

func installOne(ctx context.Context, destination *serverfiles.ServerFS, artifact Artifact, options Options, client *http.Client) (Result, error) {
	target, err := serverfiles.NormalizeRelative(artifact.Destination)
	if err != nil {
		return Result{}, fmt.Errorf("destination %q: %w", artifact.Destination, err)
	}
	if target == "" {
		return Result{}, fmt.Errorf("destination %q: %w", artifact.Destination, serverfiles.ErrRootMutation)
	}
	if parent := parentOf(target); parent != "" {
		if err := destination.MkdirAll(parent); err != nil {
			return Result{}, err
		}
	}

	requestCtx, cancel := context.WithTimeout(ctx, options.RequestTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		return Result{}, fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Accept", "*/*")

	response, err := client.Do(request)
	if err != nil {
		return Result{}, fmt.Errorf("fetch %q: %w", artifact.URL, unwrapGuardError(err))
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("fetch %q: %w: %d", artifact.URL, ErrUnexpectedStatus, response.StatusCode)
	}
	if response.ContentLength > 0 && response.ContentLength > options.MaxArtifactBytes {
		return Result{}, fmt.Errorf("fetch %q: %w", artifact.URL, serverfiles.ErrSizeLimit)
	}

	// The digest is checked inside the reader so a mismatch surfaces as a copy
	// failure. WriteStream then discards its temporary file and the declared
	// destination is never replaced with unverified bytes.
	verifier := &digestVerifier{
		reader:   response.Body,
		digest:   sha256.New(),
		expected: artifact.SHA256,
		limit:    options.MaxArtifactBytes,
	}
	written, err := destination.WriteStream(target, verifier)
	if err != nil {
		return Result{}, fmt.Errorf("install %q: %w", artifact.Destination, err)
	}
	return Result{Destination: target, SizeBytes: written, SHA256: artifact.SHA256}, nil
}

// ValidateArtifacts reports whether artifacts satisfy the manifest rules that
// can be decided without fetching anything: https-only URLs with a host, a
// full lowercase sha256, safe relative destinations, and no two artifacts
// claiming one destination. Install calls this before its first request;
// gamectl lint calls it so a definition fails at authoring time rather than on
// a server's first install.
func ValidateArtifacts(artifacts []Artifact) error {
	return validateArtifacts(artifacts)
}

// ValidateAllowlist reports whether every entry of spec.install.networkAllowlist
// is a bare hostname, and returns the normalized grant set. Callers pass a host
// to HostAllowed to test membership using the fetch path's own comparison.
func ValidateAllowlist(entries []string) (map[string]struct{}, error) {
	return normalizeAllowlist(entries)
}

// HostAllowed reports whether host is granted by a set from ValidateAllowlist,
// applying the case and trailing-dot handling the fetch path uses.
func HostAllowed(allowed map[string]struct{}, host string) bool {
	return hostAllowed(allowed, host)
}

func validateArtifacts(artifacts []Artifact) error {
	seen := make(map[string]int, len(artifacts))
	for index, artifact := range artifacts {
		field := fmt.Sprintf("install.artifacts[%d]", index)
		if !isFullHexDigest(artifact.SHA256) {
			return fmt.Errorf("%s.sha256 %q: %w", field, artifact.SHA256, ErrInvalidDigest)
		}
		parsed, err := url.Parse(artifact.URL)
		if err != nil {
			return fmt.Errorf("%s.url %q: %w", field, artifact.URL, err)
		}
		if parsed.Scheme != "https" {
			return fmt.Errorf("%s.url %q: %w", field, artifact.URL, ErrNotHTTPS)
		}
		if parsed.Hostname() == "" {
			return fmt.Errorf("%s.url %q: missing host", field, artifact.URL)
		}
		target, err := serverfiles.NormalizeRelative(artifact.Destination)
		if err != nil {
			return fmt.Errorf("%s.destination %q: %w", field, artifact.Destination, err)
		}
		if previous, exists := seen[target]; exists {
			return fmt.Errorf("%s.destination %q: %w by install.artifacts[%d]", field, artifact.Destination, ErrDuplicateTarget, previous)
		}
		seen[target] = index
	}
	return nil
}

func (o Options) withDefaults() Options {
	if o.MaxArtifactBytes <= 0 {
		o.MaxArtifactBytes = DefaultMaxArtifactBytes
	}
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = DefaultRequestTimeout
	}
	if o.MaxRedirects <= 0 {
		o.MaxRedirects = DefaultMaxRedirects
	}
	return o
}

func (o Options) clientWith(allowed map[string]struct{}) (*http.Client, error) {
	checkRedirect := func(request *http.Request, via []*http.Request) error {
		if len(via) >= o.MaxRedirects {
			return fmt.Errorf("redirect limit of %d exceeded", o.MaxRedirects)
		}
		if request.URL.Scheme != "https" {
			return fmt.Errorf("redirect to %q: %w", request.URL.Redacted(), ErrNotHTTPS)
		}
		if !hostAllowed(allowed, request.URL.Hostname()) {
			return fmt.Errorf("redirect to %q: %w", request.URL.Redacted(), ErrHostNotAllowed)
		}
		return nil
	}
	if o.Client != nil {
		injected := *o.Client
		injected.CheckRedirect = checkRedirect
		return &injected, nil
	}
	return &http.Client{
		CheckRedirect: checkRedirect,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           guardedDialer(allowed),
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          4,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}, nil
}

func parentOf(target string) string {
	index := strings.LastIndex(target, "/")
	if index <= 0 {
		return ""
	}
	return target[:index]
}

func isFullHexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		switch {
		case character >= '0' && character <= '9':
		case character >= 'a' && character <= 'f':
		default:
			return false
		}
	}
	return true
}

// digestVerifier enforces the two rules that must hold on the bytes themselves
// rather than on anything the remote host claims: the total length stays within
// limit, and the content hashes to expected. Both surface as read errors so the
// caller's atomic write discards its staging file instead of committing.
type digestVerifier struct {
	reader   io.Reader
	digest   hash.Hash
	expected string
	limit    int64
	read     int64
	verified bool
}

func (d *digestVerifier) Read(buffer []byte) (int, error) {
	read, err := d.reader.Read(buffer)
	if read > 0 {
		// Checked against the bytes received, because Content-Length is a
		// claim the host may omit (chunked) or understate.
		d.read += int64(read)
		if d.limit > 0 && d.read > d.limit {
			return read, fmt.Errorf("%w: artifact exceeds %d bytes", serverfiles.ErrSizeLimit, d.limit)
		}
		if _, hashErr := d.digest.Write(buffer[:read]); hashErr != nil {
			return read, hashErr
		}
	}
	if errors.Is(err, io.EOF) && !d.verified {
		actual := hex.EncodeToString(d.digest.Sum(nil))
		if actual != d.expected {
			return read, fmt.Errorf("%w: got %s, want %s", ErrDigestMismatch, actual, d.expected)
		}
		d.verified = true
	}
	return read, err
}

func normalizeAllowlist(entries []string) (map[string]struct{}, error) {
	allowed := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		host := strings.ToLower(strings.TrimSpace(entry))
		if host == "" {
			continue
		}
		if strings.ContainsAny(host, "/:*") {
			return nil, fmt.Errorf("networkAllowlist entry %q must be a bare hostname", entry)
		}
		allowed[host] = struct{}{}
	}
	return allowed, nil
}

func hostAllowed(allowed map[string]struct{}, host string) bool {
	_, ok := allowed[strings.ToLower(strings.TrimSuffix(host, "."))]
	return ok
}

func hostOf(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

// guardError marks a dial rejected by the guard so Install can report the
// cause rather than the wrapped url.Error text.
type guardError struct{ err error }

func (g *guardError) Error() string { return g.err.Error() }
func (g *guardError) Unwrap() error { return g.err }

func unwrapGuardError(err error) error {
	var guard *guardError
	if errors.As(err, &guard) {
		return guard.err
	}
	return err
}

// guardedDialer refuses to connect unless the host is allowlisted and every
// address it resolved to is publicly routable. Checking the address after
// resolution and dialing that exact address is what closes DNS rebinding: the
// name cannot resolve to a public address for the check and a private one for
// the connection.
func guardedDialer(allowed map[string]struct{}) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network string, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, &guardError{err: err}
		}
		if !hostAllowed(allowed, host) {
			return nil, &guardError{err: fmt.Errorf("%w: %s", ErrHostNotAllowed, host)}
		}
		addresses, err := (&net.Resolver{}).LookupIPAddr(ctx, host)
		if err != nil {
			return nil, &guardError{err: err}
		}
		if len(addresses) == 0 {
			return nil, &guardError{err: fmt.Errorf("%w: %s has no addresses", ErrPrivateAddress, host)}
		}
		for _, candidate := range addresses {
			if !routable(candidate.IP) {
				return nil, &guardError{err: fmt.Errorf("%w: %s resolves to %s", ErrPrivateAddress, host, candidate.IP)}
			}
		}
		var lastErr error
		for _, candidate := range addresses {
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
			if err == nil {
				return connection, nil
			}
			lastErr = err
		}
		return nil, &guardError{err: lastErr}
	}
}

// routable reports whether address is a public unicast address. Loopback,
// private, link-local, multicast, unspecified and IPv4-mapped ranges are all
// refused so an artifact URL cannot reach node-local or cloud metadata
// services.
func routable(address net.IP) bool {
	if address == nil || address.IsUnspecified() || address.IsLoopback() {
		return false
	}
	if address.IsPrivate() || address.IsMulticast() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() {
		return false
	}
	if address.IsInterfaceLocalMulticast() {
		return false
	}
	if v4 := address.To4(); v4 != nil {
		switch {
		case v4[0] == 0, v4[0] == 127:
			return false
		case v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127: // RFC 6598 carrier NAT
			return false
		case v4[0] == 169 && v4[1] == 254: // metadata service
			return false
		case v4[0] == 192 && v4[1] == 0 && v4[2] == 0: // RFC 6890 protocol assignments
			return false
		case v4[0] == 192 && v4[1] == 0 && v4[2] == 2: // documentation
			return false
		case v4[0] == 198 && (v4[1] == 18 || v4[1] == 19): // benchmarking
			return false
		case v4[0] == 198 && v4[1] == 51 && v4[2] == 100: // documentation
			return false
		case v4[0] == 203 && v4[1] == 0 && v4[2] == 113: // documentation
			return false
		case v4[0] >= 240: // reserved and broadcast
			return false
		}
		return true
	}
	if len(address) != net.IPv6len {
		return false
	}
	switch {
	case address[0] == 0xfc || address[0] == 0xfd: // unique local
		return false
	case address[0] == 0x20 && address[1] == 0x01 && address[2] == 0x0d && address[3] == 0xb8: // documentation
		return false
	case address[0] == 0x00 && address[1] == 0x64 && address[2] == 0xff && address[3] == 0x9b: // NAT64
		return false
	}
	if address.To4() != nil { // IPv4-mapped or -compatible
		return false
	}
	return true
}
