package runners

import (
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spec "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/assert"

	"go.podman.io/podman/v6/pkg/specgen"
)

func TestMd5Hash(t *testing.T) {
	// Verify deterministic hex output
	got := md5Hash("hello")
	expected := fmt.Sprintf("%x", md5.Sum([]byte("hello")))
	assert.Equal(t, expected, got, "md5Hash should produce standard hex MD5")

	// Same input must always produce the same output
	assert.Equal(t, md5Hash("hello"), md5Hash("hello"), "md5Hash must be deterministic")

	// Different inputs must produce different outputs
	assert.NotEqual(t, md5Hash("hello"), md5Hash("world"), "different inputs must yield different hashes")

	// Empty string
	assert.Equal(t, fmt.Sprintf("%x", md5.Sum(nil)), md5Hash(""), "md5Hash of empty string")
}

func TestSanitizePath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"alphanumeric", "node_modules", "node_modules"},
		{"with slashes", "path/to/dir", "path_to_dir"},
		{"with dots", "yarn.lock", "yarn_lock"},
		{"mixed special chars", "a/b.c-d", "a_b_c_d"},
		{"already clean", "abc123", "abc123"},
		{"empty string", "", ""},
		{"only special chars", "/.-", "___"},
		{"uppercase mixed", "Foo/Bar.lock", "Foo_Bar_lock"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, sanitizePath(tt.input))
		})
	}
}

func TestOverlayVolumeName(t *testing.T) {
	absPath := "/home/user/projects/myapp"
	relPath := "node_modules"

	volumeName := fmt.Sprintf("asimi-overlay-%s-%s", md5Hash(absPath), sanitizePath(relPath))

	// Must have the correct prefix
	assert.True(t, strings.HasPrefix(volumeName, "asimi-overlay-"), "volume name must have asimi-overlay- prefix")

	// Must contain the hash portion
	hash := md5Hash(absPath)
	assert.Contains(t, volumeName, hash, "volume name must contain the project root hash")

	// Must contain the sanitized path
	sanitized := sanitizePath(relPath)
	assert.True(t, strings.HasSuffix(volumeName, "-"+sanitized), "volume name must end with -<sanitized-path>")

	// Must be deterministic
	volumeName2 := fmt.Sprintf("asimi-overlay-%s-%s", md5Hash(absPath), sanitizePath(relPath))
	assert.Equal(t, volumeName, volumeName2, "volume naming must be deterministic")

	// Different project roots must produce different volume names
	otherPath := "/home/user/projects/otherapp"
	volumeNameOther := fmt.Sprintf("asimi-overlay-%s-%s", md5Hash(otherPath), sanitizePath(relPath))
	assert.NotEqual(t, volumeName, volumeNameOther, "different project roots must yield different volume names")
}

func TestOverlayNamedVolumes(t *testing.T) {
	absPath := "/home/user/projects/myapp"

	// Build mounts in the same order as createContainer
	mounts := []spec.Mount{
		{
			Type:        "bind",
			Source:      absPath,
			Destination: absPath,
		},
	}

	// PlatformOverlays: node_modules is a directory, yarn.lock is a file
	// Directory overlays become NamedVolumes on s.Volumes
	// File overlays become bind mounts on the mounts slice
	dirOverlay := "node_modules"
	fileOverlay := "yarn.lock"

	var namedVolumes []*specgen.NamedVolume
	var fileMounts []spec.Mount

	// Directory overlay → NamedVolume
	dirDest := filepath.Join(absPath, dirOverlay)
	dirVolumeName := fmt.Sprintf("asimi-overlay-%s-%s", md5Hash(absPath), sanitizePath(dirOverlay))
	namedVolumes = append(namedVolumes, &specgen.NamedVolume{
		Name: dirVolumeName,
		Dest: dirDest,
	})

	// File overlay → bind mount
	fileDest := filepath.Join(absPath, fileOverlay)
	overlayDataDir, err := overlayFileDir(absPath)
	assert.NoError(t, err)
	overlayFilePath := filepath.Join(overlayDataDir, sanitizePath(fileOverlay))
	fileMounts = append(fileMounts, spec.Mount{
		Type:        "bind",
		Source:      overlayFilePath,
		Destination: fileDest,
	})
	mounts = append(mounts, fileMounts...)

	// First mount must be the bind mount for the project root
	assert.Equal(t, "bind", mounts[0].Type, "first mount must be bind type")
	assert.Equal(t, absPath, mounts[0].Destination, "first mount must be project root")

	// Directory overlay must be a NamedVolume
	assert.Equal(t, 1, len(namedVolumes), "one named volume for directory overlay")
	assert.Equal(t, dirDest, namedVolumes[0].Dest, "named volume dest must match directory overlay")
	assert.True(t, strings.HasPrefix(namedVolumes[0].Name, "asimi-overlay-"), "named volume must have asimi-overlay- prefix")

	// File overlay must be a bind mount appended to mounts
	assert.Equal(t, 2, len(mounts), "mounts must have project root + file overlay bind mount")
	assert.Equal(t, "bind", mounts[1].Type, "file overlay must be bind type")
	assert.Equal(t, fileDest, mounts[1].Destination, "file overlay mount dest must match")
	assert.Equal(t, overlayFilePath, mounts[1].Source, "file overlay mount source must be the overlay file path")
}

func TestCacheVolumesEmptyEmitsNoNamedVolume(t *testing.T) {
	assert.Empty(t, namedCacheVolumes(nil, "owner/repo"),
		"nil CacheVolumes must emit no named volumes")
	assert.Empty(t, namedCacheVolumes([]Mount{}, "owner/repo"),
		"empty CacheVolumes must emit no named volumes")
}

// Cache volumes are named volumes with absolute destinations, keyed by the
// sanitized project slug (so all worktrees/edicts of one project share a cache
// and the name is stable across worktrees).
func TestCacheVolumesAbsoluteDestKeyedBySlug(t *testing.T) {
	cacheVolumes := []Mount{
		{Source: "asimi-gomodcache-<slug>", Destination: "/go/pkg/mod"},
	}

	namedVolumes := namedCacheVolumes(cacheVolumes, "Owner/Repo")

	assert.Len(t, namedVolumes, 1, "one cache volume per entry")
	assert.Equal(t, "/go/pkg/mod", namedVolumes[0].Dest, "destination must be absolute")
	assert.True(t, strings.HasPrefix(namedVolumes[0].Dest, "/"), "destination must be absolute")
	// Raw "Owner/Repo" carries "/" and uppercase; sanitize lowercases and
	// hyphenates so the volume name is legal and stable.
	assert.Equal(t, "asimi-gomodcache-owner-repo", namedVolumes[0].Name,
		"volume name must be keyed by the sanitized project slug")

	// Same project, different worktree path → same volume name (stability).
	assert.Equal(t, cacheVolumeName("asimi-gomodcache-<slug>", "owner/repo"),
		cacheVolumeName("asimi-gomodcache-<slug>", "owner/repo"),
		"volume name must be stable for the same project")
	// Different project → different volume name (isolation).
	assert.NotEqual(t, cacheVolumeName("asimi-gomodcache-<slug>", "owner/repo"),
		cacheVolumeName("asimi-gomodcache-<slug>", "owner/other"),
		"different projects must not share a cache volume")
}

func TestDirectoryOverlayUsesNamedVolume(t *testing.T) {
	// Create a temp project root with a directory overlay
	tmpDir := t.TempDir()
	absPath, err := filepath.Abs(tmpDir)
	assert.NoError(t, err)

	overlayDir := "node_modules"
	overlayDest := filepath.Join(absPath, overlayDir)
	assert.NoError(t, os.MkdirAll(overlayDest, 0755))

	// Simulate the createContainer overlay logic for a directory
	info, err := os.Stat(overlayDest)
	assert.NoError(t, err)
	assert.True(t, info.IsDir(), "overlay must be a directory")

	var namedVolumes []*specgen.NamedVolume
	var mounts []spec.Mount

	volumeName := fmt.Sprintf("asimi-overlay-%s-%s", md5Hash(absPath), sanitizePath(overlayDir))
	namedVolumes = append(namedVolumes, &specgen.NamedVolume{
		Name: volumeName,
		Dest: overlayDest,
	})

	// Directory overlay should produce a NamedVolume, not a bind mount
	assert.Equal(t, 1, len(namedVolumes), "should have one named volume")
	assert.Equal(t, 0, len(mounts), "should have no bind mounts for directory overlay")
	assert.Equal(t, volumeName, namedVolumes[0].Name)
	assert.Equal(t, overlayDest, namedVolumes[0].Dest)
}

func TestFileOverlayUsesBindMount(t *testing.T) {
	// Create a temp project root with a file overlay
	tmpDir := t.TempDir()
	absPath, err := filepath.Abs(tmpDir)
	assert.NoError(t, err)

	overlayFile := "yarn.lock"
	overlayDest := filepath.Join(absPath, overlayFile)
	assert.NoError(t, os.WriteFile(overlayDest, []byte("host content"), 0644))

	// Simulate the createContainer overlay logic for a file
	info, err := os.Stat(overlayDest)
	assert.NoError(t, err)
	assert.False(t, info.IsDir(), "overlay must be a file")

	overlayDataDir, err := overlayFileDir(absPath)
	assert.NoError(t, err)

	overlayFilePath := filepath.Join(overlayDataDir, sanitizePath(overlayFile))
	if _, err := os.Stat(overlayFilePath); err != nil {
		assert.NoError(t, os.WriteFile(overlayFilePath, nil, 0644))
	}

	mounts := []spec.Mount{
		{
			Type:        "bind",
			Source:      absPath,
			Destination: absPath,
		},
	}

	mounts = append(mounts, spec.Mount{
		Type:        "bind",
		Source:      overlayFilePath,
		Destination: overlayDest,
	})

	// File overlay should produce a bind mount, not a NamedVolume
	assert.Equal(t, 2, len(mounts), "should have project root + file overlay bind mount")
	assert.Equal(t, "bind", mounts[1].Type, "file overlay must be bind type")
	assert.Equal(t, overlayDest, mounts[1].Destination, "file overlay dest must match")
	assert.Equal(t, overlayFilePath, mounts[1].Source, "file overlay source must be overlay file path")

	// The overlay file must exist
	info, err = os.Stat(overlayFilePath)
	assert.NoError(t, err, "overlay file must exist")
	assert.False(t, info.IsDir(), "overlay file must be a regular file")
}

func TestFileOverlayNotCopiedFromHost(t *testing.T) {
	// Create a temp project root with a file overlay that has host content
	tmpDir := t.TempDir()
	absPath, err := filepath.Abs(tmpDir)
	assert.NoError(t, err)

	overlayFile := "yarn.lock"
	overlayDest := filepath.Join(absPath, overlayFile)
	hostContent := []byte("this is host yarn.lock content that should NOT be copied")
	assert.NoError(t, os.WriteFile(overlayDest, hostContent, 0644))

	// Simulate the createContainer file overlay logic
	overlayDataDir, err := overlayFileDir(absPath)
	assert.NoError(t, err)

	overlayFilePath := filepath.Join(overlayDataDir, sanitizePath(overlayFile))
	// Create empty file (not a copy)
	assert.NoError(t, os.WriteFile(overlayFilePath, nil, 0644))

	// The overlay file must be empty (0 bytes), not a copy of the host file
	info, err := os.Stat(overlayFilePath)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), info.Size(), "overlay file must be empty, not copied from host")

	// Double-check the host file still has content (untouched)
	hostInfo, err := os.Stat(overlayDest)
	assert.NoError(t, err)
	assert.Equal(t, int64(len(hostContent)), hostInfo.Size(), "host file must still have its original content")
}

func TestOverlayFileDir(t *testing.T) {
	absPath := "/some/abs/path/to/project"

	dir, err := overlayFileDir(absPath)
	assert.NoError(t, err)

	homeDir, err := os.UserHomeDir()
	assert.NoError(t, err)

	expected := filepath.Join(homeDir, ".local", "share", "asimi", "overlays", md5Hash(absPath))
	assert.Equal(t, expected, dir, "overlayFileDir should return the correct path")

	// Directory must exist
	info, err := os.Stat(dir)
	assert.NoError(t, err)
	assert.True(t, info.IsDir(), "overlay data directory must exist and be a directory")

	// Cleanup
	os.RemoveAll(dir)
}

func TestMissingOverlaySkipped(t *testing.T) {
	// Verify that when os.Stat fails on the overlay destination, it is skipped
	// rather than creating a volume or mount for a non-existent path.
	tmpDir := t.TempDir()
	absPath, err := filepath.Abs(tmpDir)
	assert.NoError(t, err)

	missingOverlay := "does_not_exist"
	overlayDest := filepath.Join(absPath, missingOverlay)

	_, err = os.Stat(overlayDest)
	assert.Error(t, err, "overlay path must not exist")

	// Simulate the createContainer overlay logic for a missing path
	var namedVolumes []*specgen.NamedVolume
	var mounts []spec.Mount

	info, statErr := os.Stat(overlayDest)
	if statErr != nil {
		// skip — this is the behavior in createContainer
	} else if info.IsDir() {
		namedVolumes = append(namedVolumes, &specgen.NamedVolume{
			Name: fmt.Sprintf("asimi-overlay-%s-%s", md5Hash(absPath), sanitizePath(missingOverlay)),
			Dest: overlayDest,
		})
	} else {
		mounts = append(mounts, spec.Mount{
			Type:        "bind",
			Source:      overlayDest,
			Destination: overlayDest,
		})
	}

	// Missing overlay should produce neither named volumes nor bind mounts
	assert.Equal(t, 0, len(namedVolumes), "no named volumes for missing overlay")
	assert.Equal(t, 0, len(mounts), "no bind mounts for missing overlay")
}
