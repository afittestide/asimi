package utils

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// GCloudProject resolves the GCP project used by Vertex AI. GOOGLE_CLOUD_PROJECT
// wins; otherwise it reads the configuration files the gcloud CLI writes, so
// `gcloud auth application-default login` followed by
// `gcloud config set project` is enough to authenticate Vertex AI without
// exporting the variable. CLOUDSDK_CONFIG overrides the configuration
// directory, mirroring gcloud's own behavior.
func GCloudProject() string {
	if project := os.Getenv("GOOGLE_CLOUD_PROJECT"); project != "" {
		return project
	}
	if dir := os.Getenv("CLOUDSDK_CONFIG"); dir != "" {
		return readGCloudProject(dir)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	dirs := []string{
		filepath.Join(home, ".config", "gcloud"),
		filepath.Join(home, ".gcloud"),
	}
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			dirs = append([]string{filepath.Join(appData, "gcloud")}, dirs...)
		}
	}

	for _, dir := range dirs {
		if project := readGCloudProject(dir); project != "" {
			return project
		}
	}
	return ""
}

// readGCloudProject reads the project from the active gcloud configuration
// under configDir. Missing or unreadable files are treated as "not configured".
func readGCloudProject(configDir string) string {
	name := "default"
	if active, err := os.ReadFile(filepath.Join(configDir, "active_config")); err == nil {
		if trimmed := strings.TrimSpace(string(active)); trimmed != "" {
			name = trimmed
		}
	}
	// active_config is user-writable; refuse path separators and traversal.
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return ""
	}

	data, err := os.ReadFile(filepath.Join(configDir, "configurations", "config_"+name))
	if err != nil {
		return ""
	}
	return parseGCloudProject(data)
}

// parseGCloudProject extracts the `project = ...` value from the [core]
// section of a gcloud INI-style configuration file. Comments are ignored and
// surrounding quotes are tolerated.
func parseGCloudProject(data []byte) string {
	section := ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		if section != "" && section != "core" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "project" {
			continue
		}
		if project := strings.Trim(strings.TrimSpace(value), `"'`); project != "" {
			return project
		}
	}
	return ""
}
