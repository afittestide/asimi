// Package utils provides shared utility functions for message formatting.
package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"strings"

	"github.com/blang/semver"
)

const (
	githubOwner = "afittestide"
	githubRepo  = "asimi"
)

// Update this before each release
var AsimiVersion = "0.13.1-dev"

// ReleaseInfo holds information about a GitHub release.
type ReleaseInfo struct {
	Version      string
	URL          string
	AssetURL     string
	ReleaseNotes string
}

// githubRelease is the JSON structure returned by the GitHub releases API.
type githubRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// GetAsimiSlug gets the slug for asimi's repo
func GetAsimiSlug() string {
	return fmt.Sprintf("%s/%s", githubOwner, githubRepo)
}

// ParseVersion parses a version string, handling "v" prefix
func ParseVersion(v string) (semver.Version, error) {
	// Remove "v" prefix if present
	v = strings.TrimPrefix(v, "v")
	return semver.Parse(v)
}

// CheckForUpdates checks if a newer version is available on GitHub.
// The ctx controls the HTTP request timeout and cancellation.
func CheckForUpdates(ctx context.Context) (ReleaseInfo, bool, error) {
	slog.Debug("Checking updates", "project", GetAsimiSlug())

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", githubOwner, githubRepo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ReleaseInfo{}, false, fmt.Errorf("failed to create request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ReleaseInfo{}, false, fmt.Errorf("failed to fetch latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ReleaseInfo{}, false, fmt.Errorf("github API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ReleaseInfo{}, false, fmt.Errorf("failed to read release response: %w", err)
	}

	return ParseGitHubRelease(body, AsimiVersion)
}

// ParseGitHubRelease parses a GitHub releases API JSON body and determines
// whether an update is available relative to currentVersion.
func ParseGitHubRelease(body []byte, currentVersion string) (ReleaseInfo, bool, error) {
	var release githubRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return ReleaseInfo{}, false, fmt.Errorf("failed to parse release response: %w", err)
	}

	latestVersion, err := ParseVersion(release.TagName)
	if err != nil {
		return ReleaseInfo{}, false, fmt.Errorf("failed to parse latest version: %w", err)
	}

	current, err := ParseVersion(currentVersion)
	if err != nil {
		return ReleaseInfo{}, false, fmt.Errorf("invalid current version: %w", err)
	}

	// Find the matching asset for this OS/arch.
	// GitHub tags include a "v" prefix (e.g. "v0.9.1") but asset names don't.
	tag := strings.TrimPrefix(release.TagName, "v")
	assetName := fmt.Sprintf("asimi_%s_%s_%s.tar.gz", tag, runtime.GOOS, runtime.GOARCH)
	assetURL := ""
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			assetURL = asset.BrowserDownloadURL
			break
		}
	}

	info := ReleaseInfo{
		Version:      release.TagName,
		URL:          release.HTMLURL,
		AssetURL:     assetURL,
		ReleaseNotes: release.Body,
	}

	if latestVersion.LTE(current) {
		slog.Debug("current version is up to date", "current", currentVersion, "latest", info.Version)
		return info, false, nil
	}

	return info, true, nil
}
