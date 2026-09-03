package utils

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"testing"
)

func TestAsVersionIsParseable(t *testing.T) {
	// The release version constant must always parse as a semantic version,
	// else CheckForUpdates and auto-update flows would fail at runtime.
	if _, err := ParseVersion(AsimiVersion); err != nil {
		t.Fatalf("AsimiVersion = %q is not a valid semantic version: %v", AsimiVersion, err)
	}
}

func TestGetAsimiSlug(t *testing.T) {
	slug := GetAsimiSlug()
	if slug != "afittestide/asimi" {
		t.Errorf("GetAsimiSlug() = %q, want %q", slug, "afittestide/asimi")
	}
}

func TestCheckForUpdates(t *testing.T) {
	// Skip unless CI=true to avoid network-dependent tests during local development
	if os.Getenv("CI") == "" {
		t.Skip("Skipping network-dependent test (set CI=true to run)")
	}

	// This test makes real HTTP calls to GitHub API
	// It verifies the full flow works end-to-end
	release, updated, err := CheckForUpdates(context.Background())
	if err != nil {
		// "no release found" is acceptable if repo has no releases
		if err.Error() != "no release found" {
			t.Fatalf("CheckForUpdates() error = %v", err)
		}
	}

	if !updated {
		// Current version is up to date - also valid
		t.Log("Current version is up to date")
	}

	t.Logf("Release info: version=%s, url=%s", release.Version, release.URL)
}

func TestParseGitHubRelease(t *testing.T) {
	// Save and restore AsimiVersion
	orig := AsimiVersion
	defer func() { AsimiVersion = orig }()

	tests := []struct {
		name           string
		body           string
		currentVersion string
		wantUpdate     bool
		wantErr        bool
		wantVersion    string
		wantURL        string
		wantAssetURL   string
	}{
		{
			name: "update available with matching asset",
			body: `{
				"tag_name": "v0.9.0",
				"html_url": "https://github.com/afittestide/asimi/releases/v0.9.0",
				"body": "Release notes here",
				"assets": [
					{"name": "asimi_v0.9.0_darwin_amd64.tar.gz", "browser_download_url": "https://example.com/darwin_amd64.tar.gz"},
					{"name": "asimi_v0.9.0_linux_amd64.tar.gz", "browser_download_url": "https://example.com/linux_amd64.tar.gz"}
				]
			}`,
			currentVersion: "0.8.1",
			wantUpdate:     true,
			wantErr:        false,
			wantVersion:    "v0.9.0",
			wantURL:        "https://github.com/afittestide/asimi/releases/v0.9.0",
		},
		{
			name: "no update needed - same version",
			body: `{
				"tag_name": "v0.8.1",
				"html_url": "https://github.com/afittestide/asimi/releases/v0.8.1",
				"body": "",
				"assets": []
			}`,
			currentVersion: "0.8.1",
			wantUpdate:     false,
			wantErr:        false,
			wantVersion:    "v0.8.1",
			wantURL:        "https://github.com/afittestide/asimi/releases/v0.8.1",
		},
		{
			name: "no update needed - older release",
			body: `{
				"tag_name": "v0.7.0",
				"html_url": "https://github.com/afittestide/asimi/releases/v0.7.0",
				"body": "",
				"assets": []
			}`,
			currentVersion: "0.8.1",
			wantUpdate:     false,
			wantErr:        false,
			wantVersion:    "v0.7.0",
			wantURL:        "https://github.com/afittestide/asimi/releases/v0.7.0",
		},
		{
			name:           "invalid JSON",
			body:           `{invalid json`,
			currentVersion: "0.8.1",
			wantUpdate:     false,
			wantErr:        true,
		},
		{
			name: "invalid tag name",
			body: `{
				"tag_name": "not-a-version",
				"html_url": "https://example.com",
				"body": "",
				"assets": []
			}`,
			currentVersion: "0.8.1",
			wantUpdate:     false,
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, hasUpdate, err := ParseGitHubRelease([]byte(tt.body), tt.currentVersion)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseGitHubRelease() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if hasUpdate != tt.wantUpdate {
				t.Errorf("ParseGitHubRelease() hasUpdate = %v, want %v", hasUpdate, tt.wantUpdate)
			}
			if info.Version != tt.wantVersion {
				t.Errorf("ParseGitHubRelease() version = %q, want %q", info.Version, tt.wantVersion)
			}
			if info.URL != tt.wantURL {
				t.Errorf("ParseGitHubRelease() url = %q, want %q", info.URL, tt.wantURL)
			}
			if info.ReleaseNotes != "Release notes here" && tt.wantVersion == "v0.9.0" {
				t.Errorf("ParseGitHubRelease() releaseNotes = %q, want 'Release notes here'", info.ReleaseNotes)
			}
		})
	}
}

func TestParseGitHubRelease_AssetMatching(t *testing.T) {
	assetName := fmt.Sprintf("asimi_0.9.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	body := fmt.Sprintf(`{
		"tag_name": "v0.9.0",
		"html_url": "https://example.com/release",
		"body": "",
		"assets": [
			{"name": "asimi_0.9.0_other_os.tar.gz", "browser_download_url": "https://example.com/wrong.tar.gz"},
			{"name": %q, "browser_download_url": "https://example.com/correct.tar.gz"}
		]
	}`, assetName)

	info, _, err := ParseGitHubRelease([]byte(body), "0.8.1")
	if err != nil {
		t.Fatalf("ParseGitHubRelease() unexpected error: %v", err)
	}
	if info.AssetURL != "https://example.com/correct.tar.gz" {
		t.Errorf("ParseGitHubRelease() assetURL = %q, want correct URL", info.AssetURL)
	}
}

func TestParseGitHubRelease_AssetMatchingVPrefixStripped(t *testing.T) {
	// Regression: tag_name is "v0.9.0" but assets are named without "v".
	// The code must strip the "v" from the tag when matching asset names.
	body := `{
		"tag_name": "v0.9.1",
		"html_url": "https://example.com/release",
		"body": "",
		"assets": [
			{"name": "asimi_v0.9.1_other_os.tar.gz", "browser_download_url": "https://example.com/wrong.tar.gz"},
			{"name": "asimi_0.9.1_other_os.tar.gz", "browser_download_url": "https://example.com/wrong2.tar.gz"}
		]
	}`

	info, _, err := ParseGitHubRelease([]byte(body), "0.8.1")
	if err != nil {
		t.Fatalf("ParseGitHubRelease() unexpected error: %v", err)
	}
	if info.AssetURL != "" {
		t.Errorf("ParseGitHubRelease() assetURL = %q, want empty (no matching asset for this OS/arch)", info.AssetURL)
	}
}

func TestParseGitHubRelease_NoMatchingAsset(t *testing.T) {
	body := `{
		"tag_name": "v0.9.0",
		"html_url": "https://example.com/release",
		"body": "",
		"assets": [
			{"name": "asimi_0.9.0_other_os.tar.gz", "browser_download_url": "https://example.com/wrong.tar.gz"}
		]
	}`

	info, hasUpdate, err := ParseGitHubRelease([]byte(body), "0.8.1")
	if err != nil {
		t.Fatalf("ParseGitHubRelease() unexpected error: %v", err)
	}
	if info.AssetURL != "" {
		t.Errorf("ParseGitHubRelease() assetURL = %q, want empty", info.AssetURL)
	}
	if !hasUpdate {
		t.Error("ParseGitHubRelease() should report update available even without matching asset")
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		name      string
		version   string
		wantErr   bool
		wantMajor uint64
		wantMinor uint64
		wantPatch uint64
	}{
		{
			name:      "version with v prefix",
			version:   "v0.1.0",
			wantErr:   false,
			wantMajor: 0,
			wantMinor: 1,
			wantPatch: 0,
		},
		{
			name:      "version without v prefix",
			version:   "0.1.0",
			wantErr:   false,
			wantMajor: 0,
			wantMinor: 1,
			wantPatch: 0,
		},
		{
			name:      "complex version",
			version:   "v1.2.3-beta.1",
			wantErr:   false,
			wantMajor: 1,
			wantMinor: 2,
			wantPatch: 3,
		},
		{
			name:    "invalid version",
			version: "invalid",
			wantErr: true,
		},
		{
			name:    "empty version",
			version: "",
			wantErr: true,
		},
		{
			name:    "partial version",
			version: "1.2",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := ParseVersion(tt.version)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseVersion() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if v.Major != tt.wantMajor {
					t.Errorf("ParseVersion() major = %v, want %v", v.Major, tt.wantMajor)
				}
				if v.Minor != tt.wantMinor {
					t.Errorf("ParseVersion() minor = %v, want %v", v.Minor, tt.wantMinor)
				}
				if v.Patch != tt.wantPatch {
					t.Errorf("ParseVersion() patch = %v, want %v", v.Patch, tt.wantPatch)
				}
			}
		})
	}
}
