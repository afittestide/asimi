package config

import "testing"

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
