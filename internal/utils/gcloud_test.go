package utils

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func isolateGCloud(t *testing.T) {
	t.Helper()
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("HOME", t.TempDir())
}

func TestGCloudProjectReadsActiveConfig(t *testing.T) {
	isolateGCloud(t)
	dir := t.TempDir()
	t.Setenv("CLOUDSDK_CONFIG", dir)

	writeFile(t, dir, "active_config", "work\n")
	writeFile(t, dir, "configurations/config_work", "[core]\naccount = someone@example.com\nproject = my-project\n")

	assert.Equal(t, "my-project", GCloudProject())
}

func TestGCloudProjectFallsBackToDefaultConfig(t *testing.T) {
	isolateGCloud(t)
	dir := t.TempDir()
	t.Setenv("CLOUDSDK_CONFIG", dir)

	// No active_config file: gcloud uses "default".
	writeFile(t, dir, "configurations/config_default", "[core]\nproject = default-project\n")

	assert.Equal(t, "default-project", GCloudProject())
}

func TestGCloudProjectEmptyWhenUnset(t *testing.T) {
	isolateGCloud(t)
	dir := t.TempDir()
	t.Setenv("CLOUDSDK_CONFIG", dir)

	assert.Equal(t, "", GCloudProject())
}

func TestGCloudProjectRejectsTraversal(t *testing.T) {
	isolateGCloud(t)
	dir := t.TempDir()
	t.Setenv("CLOUDSDK_CONFIG", dir)

	writeFile(t, dir, "active_config", "../outside\n")
	assert.Equal(t, "", GCloudProject())
}

func TestParseGCloudProject(t *testing.T) {
	tests := []struct {
		name string
		ini  string
		want string
	}{
		{
			name: "core section",
			ini:  "[core]\naccount = someone@example.com\nproject = asimi-505106\n",
			want: "asimi-505106",
		},
		{
			name: "quoted and whitespace",
			ini:  "[core]\n  project  =  \"quoted-project\"  \n",
			want: "quoted-project",
		},
		{
			name: "comments ignored",
			ini:  "# project = commented\n[core]\n; project = also-commented\nproject = real-project\n",
			want: "real-project",
		},
		{
			name: "empty value",
			ini:  "[core]\nproject =\n",
			want: "",
		},
		{
			name: "missing key",
			ini:  "[core]\naccount = someone@example.com\n",
			want: "",
		},
		{
			name: "project outside core ignored",
			ini:  "[compute]\nproject = wrong-project\n[core]\nproject = right-project\n",
			want: "right-project",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseGCloudProject([]byte(tt.ini)))
		})
	}
}

// TestGCloudProjectIgnoresHomeConfigWhenCloudSDKConfigSet ensures tests (and
// users) can isolate the lookup by pointing CLOUDSDK_CONFIG at an empty dir.
func TestGCloudProjectIgnoresHomeConfigWhenCloudSDKConfigSet(t *testing.T) {
	isolateGCloud(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(home, ".config", "gcloud"), "active_config", "default\n")
	writeFile(t, filepath.Join(home, ".config", "gcloud"), "configurations/config_default", "[core]\nproject = home-project\n")

	isolated := t.TempDir()
	t.Setenv("CLOUDSDK_CONFIG", isolated)

	assert.Equal(t, "", GCloudProject())
}

func TestGCloudProjectReadsHomeConfig(t *testing.T) {
	isolateGCloud(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLOUDSDK_CONFIG", "")

	writeFile(t, filepath.Join(home, ".config", "gcloud"), "active_config", "default\n")
	writeFile(t, filepath.Join(home, ".config", "gcloud"), "configurations/config_default", "[core]\nproject = home-project\n")

	assert.Equal(t, "home-project", GCloudProject())
}

func TestGCloudProjectMissingNamedConfig(t *testing.T) {
	isolateGCloud(t)
	dir := t.TempDir()
	t.Setenv("CLOUDSDK_CONFIG", dir)

	writeFile(t, dir, "active_config", "missing\n")
	writeFile(t, dir, "configurations/config_default", "[core]\nproject = fallback-project\n")

	assert.Equal(t, "", GCloudProject())
}

func TestGCloudProjectEmptyActiveConfig(t *testing.T) {
	isolateGCloud(t)
	dir := t.TempDir()
	t.Setenv("CLOUDSDK_CONFIG", dir)

	writeFile(t, dir, "active_config", "  \n")
	writeFile(t, dir, "configurations/config_default", "[core]\nproject = default-project\n")

	assert.Equal(t, "default-project", GCloudProject())
}

func TestGCloudProjectPrefersEnvVar(t *testing.T) {
	isolateGCloud(t)
	dir := t.TempDir()
	t.Setenv("CLOUDSDK_CONFIG", dir)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "env-project")

	writeFile(t, dir, "configurations/config_default", "[core]\nproject = config-project\n")

	assert.Equal(t, "env-project", GCloudProject())
}
