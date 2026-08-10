package install

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultPaperMCBaseURL is the PaperMC API v2 root.
const DefaultPaperMCBaseURL = "https://api.papermc.io/v2"

const maxPaperMCResponseBytes = 4 << 20

type paperMCConfig struct {
	baseURL string
	client  *http.Client
}

// PaperMCOption configures ResolvePaperMCArtifact. Tests inject a fake API
// server and its client here; production paths keep the defaults.
type PaperMCOption func(*paperMCConfig)

// WithPaperMCClient injects the HTTP client used to query the PaperMC API.
func WithPaperMCClient(client *http.Client) PaperMCOption {
	return func(config *paperMCConfig) { config.client = client }
}

// WithPaperMCBaseURL overrides the API root, so tests can point resolution at
// an httptest server without touching the network.
func WithPaperMCBaseURL(base string) PaperMCOption {
	return func(config *paperMCConfig) { config.baseURL = base }
}

type paperMCBuildsPayload struct {
	Builds []paperMCBuild `json:"builds"`
}

type paperMCBuild struct {
	Build     int              `json:"build"`
	Downloads paperMCDownloads `json:"downloads"`
}

type paperMCDownloads struct {
	Application paperMCApplication `json:"application"`
}

type paperMCApplication struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// ResolvePaperMCArtifact resolves the newest published PaperMC build for
// version and returns its immutable download URL and content sha256, both read
// from the PaperMC API. The digest is validated to be a full lowercase sha256,
// matching the Artifact contract, so an upstream that stops publishing digests
// fails resolution instead of producing an unverifiable artifact.
//
// The download URL is only resolved, never fetched; installers verify the
// digest against the bytes they download.
func ResolvePaperMCArtifact(ctx context.Context, version string, options ...PaperMCOption) (downloadURL string, sha256 string, err error) {
	if version == "" {
		return "", "", fmt.Errorf("papermc: version is required")
	}
	if strings.ContainsAny(version, "/%\\") {
		return "", "", fmt.Errorf("papermc: version %q is not a valid PaperMC version", version)
	}
	config := paperMCConfig{baseURL: DefaultPaperMCBaseURL, client: http.DefaultClient}
	for _, option := range options {
		option(&config)
	}

	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	buildsURL, err := url.JoinPath(config.baseURL, "projects", "paper", "versions", version, "builds")
	if err != nil {
		return "", "", fmt.Errorf("papermc: build API url: %w", err)
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, buildsURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("papermc: build API request: %w", err)
	}
	response, err := config.client.Do(request)
	if err != nil {
		return "", "", fmt.Errorf("papermc: query %q: %w", buildsURL, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("papermc: query %q: %w: %d", buildsURL, ErrUnexpectedStatus, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxPaperMCResponseBytes))
	if err != nil {
		return "", "", fmt.Errorf("papermc: read builds response: %w", err)
	}
	var payload paperMCBuildsPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", "", fmt.Errorf("papermc: decode builds response: %w", err)
	}

	latest, ok := latestPaperMCBuild(payload.Builds)
	if !ok {
		return "", "", fmt.Errorf("papermc: version %q has no published builds", version)
	}
	application := latest.Downloads.Application
	if application.Name == "" {
		return "", "", fmt.Errorf("papermc: build %d has no application download", latest.Build)
	}
	if !isFullHexDigest(application.SHA256) {
		return "", "", fmt.Errorf("papermc: build %d sha256 %q: %w", latest.Build, application.SHA256, ErrInvalidDigest)
	}
	downloadURL, err = url.JoinPath(config.baseURL, "projects", "paper", "versions", version,
		"builds", strconv.Itoa(latest.Build), "downloads", application.Name)
	if err != nil {
		return "", "", fmt.Errorf("papermc: download URL: %w", err)
	}
	return downloadURL, application.SHA256, nil
}

// latestPaperMCBuild picks the highest build number regardless of the order
// the API returned the builds in.
func latestPaperMCBuild(builds []paperMCBuild) (paperMCBuild, bool) {
	var latest paperMCBuild
	found := false
	for _, build := range builds {
		if !found || build.Build > latest.Build {
			latest = build
			found = true
		}
	}
	return latest, found
}
