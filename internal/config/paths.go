package config

import (
	"os"
	"path/filepath"
	"strings"
)

// ExpandHome expands a leading "~" or "~/" in path to the current user's
// home directory. Paths without a leading tilde, and "~user" forms, are
// returned unchanged. Returns an error when a leading tilde is present but
// the home directory cannot be resolved.
func ExpandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}
