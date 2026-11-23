package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestCourtConfigHasNoUsernameField enforces edict 1006's Ruler ruling: the
// config-file username key was to be removed entirely — "fail closed, no inert
// knob survives to regress later". Court scope identity is resolved from the OS
// (OSUsername), never from committed config, so CourtConfig must not carry a
// username field at all: a surviving key is parseable from a tracked
// .agents/asimi.conf and would re-open the cross-user forgery vector the moment
// any code path read it again.
func TestCourtConfigHasNoUsernameField(t *testing.T) {
	if f, ok := reflect.TypeOf(CourtConfig{}).FieldByName("Username"); ok {
		t.Errorf("CourtConfig still declares field %q (koanf tag %q): the config-file username key must be deleted entirely, not left as an inert knob (edict 1006)", f.Name, f.Tag.Get("koanf"))
	}
}

func TestDefaultIgnoreCopyGlobs_IsSafeAllowlist(t *testing.T) {
	globs := DefaultIgnoreCopyGlobs()
	if len(globs) == 0 {
		t.Fatal("DefaultIgnoreCopyGlobs must not be empty (local config must travel)")
	}
	// The allowlist must never include caches, binaries, logs, coverage,
	// dist/tmp, or jobs/ — the artifacts the unbounded copy used to drag in.
	for _, forbidden := range []string{".gocache", ".gocache/", "asimi", "*.log", "coverage.*", "dist", "dist/", "tmp", "tmp/", "jobs", "jobs/", "notes"} {
		for _, g := range globs {
			if g == forbidden {
				t.Errorf("DefaultIgnoreCopyGlobs must not contain %q", g)
			}
		}
	}
	// Common local-config names must be present.
	want := map[string]bool{".env": false, ".envrc": false, "*.local": false}
	for _, g := range globs {
		if _, ok := want[g]; ok {
			want[g] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("DefaultIgnoreCopyGlobs missing %q", name)
		}
	}
}

func TestDefaultConfig_IgnoreCopyGlobsIsSafeSet(t *testing.T) {
	cfg := DefaultConfig()
	if len(cfg.Sandbox.IgnoreCopyGlobs) == 0 {
		t.Fatal("DefaultConfig must set Sandbox.IgnoreCopyGlobs")
	}
	for _, g := range cfg.Sandbox.IgnoreCopyGlobs {
		switch g {
		case ".gocache", "asimi", "jobs", "jobs/", "*.log":
			t.Errorf("DefaultConfig IgnoreCopyGlobs must not include %q", g)
		}
	}
}

func TestDefaultCacheVolumes_ModuleCacheOnly(t *testing.T) {
	vols := DefaultCacheVolumes()
	if len(vols) != 1 {
		t.Fatalf("DefaultCacheVolumes must be exactly one entry, got %d", len(vols))
	}
	got := vols[0]
	if got.Source != "asimi-gomodcache-<slug>" {
		t.Errorf("source = %q, want %q", got.Source, "asimi-gomodcache-<slug>")
	}
	if got.Destination != "/go/pkg/mod" {
		t.Errorf("destination = %q, want %q", got.Destination, "/go/pkg/mod")
	}
	// The destination must be absolute (not project-relative like overlays).
	if !strings.HasPrefix(got.Destination, "/") {
		t.Errorf("destination %q must be absolute", got.Destination)
	}
	// GOCACHE is pinned by e946 to <projectRoot>/.gocache; a cache volume over
	// the same path would fight that pin, so it must not be registered.
	if strings.Contains(got.Source, "gocache") && !strings.Contains(got.Source, "gomodcache") {
		t.Errorf("DefaultCacheVolumes must not register a GOCACHE volume: %q", got.Source)
	}
}

func TestDefaultConfig_CacheVolumesIsLive(t *testing.T) {
	cfg := DefaultConfig()
	if len(cfg.Sandbox.CacheVolumes) == 0 {
		t.Fatal("DefaultConfig must set Sandbox.CacheVolumes (field must be live)")
	}
}

func TestDefaultSessionConfig(t *testing.T) {
	got := DefaultSessionConfig()

	want := &SessionConfig{
		MaxSessions: 50,
		MaxAgeDays:  30,
	}

	if got.MaxSessions != want.MaxSessions {
		t.Errorf("MaxSessions = %d, want %d", got.MaxSessions, want.MaxSessions)
	}
	if got.MaxAgeDays != want.MaxAgeDays {
		t.Errorf("MaxAgeDays = %d, want %d", got.MaxAgeDays, want.MaxAgeDays)
	}
}

func TestDefaultSessionConfig_Table(t *testing.T) {
	got := DefaultSessionConfig()

	tests := []struct {
		name string
		got  int
		want int
	}{
		{"MaxSessions", got.MaxSessions, 50},
		{"MaxAgeDays", got.MaxAgeDays, 30},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %d, want %d", tt.got, tt.want)
			}
		})
	}
}

// TestDefaultConfig_DiskMinFreeMiB verifies edict 985: the disk-pressure guard
// ships enabled with a sensible floor (so a full podman VM prunes before it
// wedges) and a positive default is what a field left unset would lack.
func TestDefaultConfig_DiskMinFreeMiB(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Sandbox.DiskMinFreeMiB != DefaultDiskMinFreeMiB {
		t.Fatalf("DefaultConfig Sandbox.DiskMinFreeMiB = %d, want %d",
			cfg.Sandbox.DiskMinFreeMiB, DefaultDiskMinFreeMiB)
	}
	if DefaultDiskMinFreeMiB <= 0 {
		t.Fatalf("DefaultDiskMinFreeMiB must be positive to guard the store, got %d", DefaultDiskMinFreeMiB)
	}
}

func TestDefaultConfig_CourtDefaults(t *testing.T) {
	got := DefaultConfig().Court
	want := *DefaultCourtConfig()
	if got != want {
		t.Fatalf("DefaultConfig().Court = %+v, want %+v", got, want)
	}
	if got.MaxParallelLings != 0 {
		t.Errorf("default MaxParallelLings = %d, want 0", got.MaxParallelLings)
	}
}

func TestCourtConfigResolved(t *testing.T) {
	defaults := DefaultCourtConfig()

	t.Run("zero config fills positive-only defaults and preserves explicit zero sweep and ling gate", func(t *testing.T) {
		got := (CourtConfig{}).Resolved("user/repo", false)
		if got.StepIdleTimeout != defaults.StepIdleTimeout {
			t.Errorf("StepIdleTimeout = %v, want %v", got.StepIdleTimeout, defaults.StepIdleTimeout)
		}
		if got.OutputLimit != defaults.OutputLimit {
			t.Errorf("OutputLimit = %d, want %d", got.OutputLimit, defaults.OutputLimit)
		}
		if got.RunnerFlatlineTimeout != 0 {
			t.Errorf("RunnerFlatlineTimeout = %v, want 0 (sweep disabled)", got.RunnerFlatlineTimeout)
		}
		if got.MaxParallelLings != 0 {
			t.Errorf("MaxParallelLings = %d, want 0 (ignition disabled)", got.MaxParallelLings)
		}
		if got.Project != "user/repo" {
			t.Errorf("Project = %q, want %q", got.Project, "user/repo")
		}
		if got.IsolatedHost {
			t.Error("IsolatedHost = true, want false")
		}
	})

	t.Run("negative positive-only fields fall back to defaults", func(t *testing.T) {
		in := CourtConfig{
			StepIdleTimeout:  -1 * time.Minute,
			OutputLimit:      -10,
			MaxParallelLings: -2,
		}
		got := in.Resolved("user/repo", true)
		if got.StepIdleTimeout != defaults.StepIdleTimeout {
			t.Errorf("StepIdleTimeout = %v, want %v", got.StepIdleTimeout, defaults.StepIdleTimeout)
		}
		if got.OutputLimit != defaults.OutputLimit {
			t.Errorf("OutputLimit = %d, want %d", got.OutputLimit, defaults.OutputLimit)
		}
		if got.MaxParallelLings > 0 {
			t.Errorf("MaxParallelLings = %d, want <= 0", got.MaxParallelLings)
		}
		if !got.IsolatedHost {
			t.Error("IsolatedHost = false, want true")
		}
	})

	t.Run("custom fields flow through while caller authority overwrites Project and IsolatedHost", func(t *testing.T) {
		in := CourtConfig{
			Project:               "pinned/evil",
			IsolatedHost:          true,
			StepIdleTimeout:       2 * time.Minute,
			OutputLimit:           1200,
			RunnerFlatlineTimeout: 7 * time.Minute,
			MaxParallelLings:      4,
		}
		got := in.Resolved("caller/verified", false)
		if got.Project != "caller/verified" {
			t.Errorf("Project = %q, want %q", got.Project, "caller/verified")
		}
		if got.IsolatedHost {
			t.Error("IsolatedHost = true, want false")
		}
		if got.StepIdleTimeout != 2*time.Minute {
			t.Errorf("StepIdleTimeout = %v, want 2m", got.StepIdleTimeout)
		}
		if got.OutputLimit != 1200 {
			t.Errorf("OutputLimit = %d, want 1200", got.OutputLimit)
		}
		if got.RunnerFlatlineTimeout != 7*time.Minute {
			t.Errorf("RunnerFlatlineTimeout = %v, want 7m", got.RunnerFlatlineTimeout)
		}
		if got.MaxParallelLings != 4 {
			t.Errorf("MaxParallelLings = %d, want 4", got.MaxParallelLings)
		}
	})
}
