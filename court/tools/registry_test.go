package tools

import (
	"context"
	"testing"
)

// mockTool is a minimal Tool implementation for testing.
type mockTool struct {
	name string
}

func (m mockTool) Name() string                                     { return m.name }
func (m mockTool) Description() string                              { return "" }
func (m mockTool) Call(_ context.Context, _ string) (string, error) { return "", nil }
func (m mockTool) Format(_, _ string, _ error) string               { return "" }
func (m mockTool) ParameterSchema() map[string]any                  { return nil }

// ---------------------------------------------------------------------------
// ParsePermissions — valid strings
// ---------------------------------------------------------------------------

func TestParsePermissionsValid(t *testing.T) {
	tests := []struct {
		input  string
		earth  Access
		heaven Access
		intent Access
	}{
		{
			input:  "rwxrwxrwx",
			earth:  Access{true, true, true},
			heaven: Access{true, true, true},
			intent: Access{true, true, true},
		},
		{
			input:  "r-----rw-",
			earth:  Access{true, false, false},
			heaven: Access{false, false, false},
			intent: Access{true, true, false},
		},
		{
			input:  "r-----rwx",
			earth:  Access{true, false, false},
			heaven: Access{false, false, false},
			intent: Access{true, true, true},
		},
		{
			input:  "r--r--rwx",
			earth:  Access{true, false, false},
			heaven: Access{true, false, false},
			intent: Access{true, true, true},
		},
		{
			input:  "r-xr--rw-",
			earth:  Access{true, false, true},
			heaven: Access{true, false, false},
			intent: Access{true, true, false},
		},
		{
			input:  "rwxr---w-",
			earth:  Access{true, true, true},
			heaven: Access{true, false, false},
			intent: Access{false, true, false},
		},
		{
			input:  "rwxrwxr--",
			earth:  Access{true, true, true},
			heaven: Access{true, true, true},
			intent: Access{true, false, false},
		},
		{
			input:  "rwxr--rwx",
			earth:  Access{true, true, true},
			heaven: Access{true, false, false},
			intent: Access{true, true, true},
		},
		{
			input:  "---------",
			earth:  Access{},
			heaven: Access{},
			intent: Access{},
		},
		{
			// Only read in every realm
			input:  "r--r--r--",
			earth:  Access{Read: true},
			heaven: Access{Read: true},
			intent: Access{Read: true},
		},
		{
			// Only write in every realm
			input:  "-w--w--w-",
			earth:  Access{Write: true},
			heaven: Access{Write: true},
			intent: Access{Write: true},
		},
		{
			// Only execute in every realm
			input:  "--x--x--x",
			earth:  Access{Execute: true},
			heaven: Access{Execute: true},
			intent: Access{Execute: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			p, err := ParsePermissions(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p.Earth != tt.earth {
				t.Errorf("earth = %+v, want %+v", p.Earth, tt.earth)
			}
			if p.Heaven != tt.heaven {
				t.Errorf("heaven = %+v, want %+v", p.Heaven, tt.heaven)
			}
			if p.Intent != tt.intent {
				t.Errorf("intent = %+v, want %+v", p.Intent, tt.intent)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ParsePermissions — error cases
// ---------------------------------------------------------------------------

func TestParsePermissionsErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"too_short", "rwx"},
		{"too_short_6", "rwxrwx"},
		{"too_long", "rwxrwxrwxrwx"},
		{"invalid_char_in_earth_read", "awxrwxrwx"},
		{"invalid_char_in_earth_write", "rawrwxrwx"},
		{"invalid_char_in_heaven_write", "rwxrawrwx"},
		{"invalid_char_in_intent_execute", "rwxrwxrwa"},
		{"numeric_char", "rwx1wxrwx"},
		{"space_char", "rwxrwxrw "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsePermissions(tt.input)
			if err == nil {
				t.Fatalf("expected error for input %q, got nil", tt.input)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ParsePermissions — String round-trip
// ---------------------------------------------------------------------------

func TestPermissionsStringRoundTrip(t *testing.T) {
	strings := []string{
		"rwxrwxrwx",
		"r-----rw-",
		"r-----rwx",
		"r--r--rwx",
		"r-xr--rw-",
		"rwxr---w-",
		"rwxrwxr--",
		"rwxr--rwx",
		"---------",
		"r--r--r--",
		"-w--w--w-",
		"--x--x--x",
	}
	for _, s := range strings {
		t.Run(s, func(t *testing.T) {
			p, err := ParsePermissions(s)
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			got := p.String()
			if got != s {
				t.Errorf("String() = %q, want %q", got, s)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Permissions.Match — per-flag matching logic
// ---------------------------------------------------------------------------

func TestPermissionsMatchReadRead(t *testing.T) {
	minister := Permissions{Earth: Access{Read: true}}
	tool := Permissions{Earth: Access{Read: true}}

	if !minister.Match(tool) {
		t.Error("minister with earth Read should match tool with earth Read")
	}
}

func TestPermissionsMatchWriteWrite(t *testing.T) {
	minister := Permissions{Intent: Access{Write: true}}
	tool := Permissions{Intent: Access{Write: true}}

	if !minister.Match(tool) {
		t.Error("minister with intent Write should match tool with intent Write")
	}
}

func TestPermissionsMatchExecuteExecute(t *testing.T) {
	minister := Permissions{Heaven: Access{Execute: true}}
	tool := Permissions{Heaven: Access{Execute: true}}

	if !minister.Match(tool) {
		t.Error("minister with heaven Execute should match tool with heaven Execute")
	}
}

func TestPermissionsMatchReadVsWriteNoMatch(t *testing.T) {
	// Minister has Read, tool has only Write in same realm — no match
	minister := Permissions{Earth: Access{Read: true}}
	tool := Permissions{Earth: Access{Write: true}}

	if minister.Match(tool) {
		t.Error("minister with earth Read should NOT match tool with only earth Write")
	}
}

func TestPermissionsMatchWriteVsExecuteNoMatch(t *testing.T) {
	// Minister has Write, tool has only Execute in same realm — no match
	minister := Permissions{Earth: Access{Write: true}}
	tool := Permissions{Earth: Access{Execute: true}}

	if minister.Match(tool) {
		t.Error("minister with earth Write should NOT match tool with only earth Execute")
	}
}

func TestPermissionsMatchCrossRealmNoMatch(t *testing.T) {
	// Minister has Read in earth, tool has Read in heaven — no match
	minister := Permissions{Earth: Access{Read: true}}
	tool := Permissions{Heaven: Access{Read: true}}

	if minister.Match(tool) {
		t.Error("minister with earth Read should NOT match tool with heaven Read (different realm)")
	}
}

func TestPermissionsMatchMultiFlagOverlap(t *testing.T) {
	// Minister has Read+Write, tool has only Write — match on Write
	minister := Permissions{Earth: Access{Read: true, Write: true}}
	tool := Permissions{Earth: Access{Write: true}}

	if !minister.Match(tool) {
		t.Error("minister with earth Read+Write should match tool with earth Write (shared Write)")
	}
}

func TestPermissionsMatchAnyRealm(t *testing.T) {
	// Minister matches in Intent realm even though Earth and Heaven don't match
	minister := Permissions{
		Earth:  Access{Write: true},   // no overlap with tool's earth
		Heaven: Access{},              // no access
		Intent: Access{Execute: true}, // overlap with tool's intent
	}
	tool := Permissions{
		Earth:  Access{Read: true},
		Intent: Access{Execute: true},
	}

	if !minister.Match(tool) {
		t.Error("should match on intent Execute even though earth doesn't match")
	}
}

func TestPermissionsMatchNoOverlap(t *testing.T) {
	minister := Permissions{Earth: Access{Read: true}}
	tool := Permissions{Heaven: Access{Write: true}, Intent: Access{Execute: true}}

	if minister.Match(tool) {
		t.Error("no overlapping flags in any realm — should not match")
	}
}

// ---------------------------------------------------------------------------
// ToolRegistry — NewToolRegistry
// ---------------------------------------------------------------------------

func TestNewToolRegistry(t *testing.T) {
	r := NewToolRegistry()
	if r == nil {
		t.Fatal("NewToolRegistry returned nil")
	}
	if len(r.Tools()) != 0 {
		t.Error("new registry should have no public tools")
	}
	if len(r.ExtraToolNames()) != 0 {
		t.Error("new registry should have no extra tools")
	}
}

// ---------------------------------------------------------------------------
// ToolRegistry — Register
// ---------------------------------------------------------------------------

func TestRegister(t *testing.T) {
	r := NewToolRegistry()
	perm, _ := ParsePermissions("rwxr---w-")
	r.Register(mockTool{name: "read_file"}, perm)

	tools := r.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Tool.Name() != "read_file" {
		t.Errorf("tool name = %q, want %q", tools[0].Tool.Name(), "read_file")
	}
	if tools[0].Permissions != perm {
		t.Errorf("permissions = %+v, want %+v", tools[0].Permissions, perm)
	}
}

func TestRegisterDuplicateMergesPermissions(t *testing.T) {
	r := NewToolRegistry()
	perm, _ := ParsePermissions("r-----rw-")
	r.Register(mockTool{name: "dup"}, perm)

	// Re-registering the same tool name with different permissions merges the
	// classifications instead of panicking (this is how create_manifest is
	// dual-classified under heaven Write and intent Write).
	other, _ := ParsePermissions("r--r----")
	r.Register(mockTool{name: "dup"}, other)

	tools := r.ForPermissions(perm)
	assertHas(t, toolNames(tools), "dup")
	// The merged entry matches through the second classification too.
	perm2, _ := ParsePermissions("r--------")
	assertHas(t, toolNames(r.ForPermissions(perm2)), "dup")
}

// ---------------------------------------------------------------------------
// ToolRegistry — Update
// ---------------------------------------------------------------------------

func TestUpdate(t *testing.T) {
	r := NewToolRegistry()
	perm, _ := ParsePermissions("r-----rw-")
	r.Register(mockTool{name: "read_file"}, perm)

	// Update with new instance (same name, different behavior)
	updatedTool := mockTool{name: "read_file"}
	r.Update(updatedTool)

	// Verify the tool is still registered
	tools := r.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool after update, got %d", len(tools))
	}
	if tools[0].Tool.Name() != "read_file" {
		t.Errorf("tool name = %q, want %q", tools[0].Tool.Name(), "read_file")
	}
	// Permissions should be preserved
	if tools[0].Permissions != perm {
		t.Errorf("permissions changed after update")
	}
}

func TestUpdateNonExistentNoOp(t *testing.T) {
	r := NewToolRegistry()
	perm, _ := ParsePermissions("r-----rw-")
	r.Register(mockTool{name: "existing"}, perm)

	// Update on non-existent tool should be no-op (no panic)
	r.Update(mockTool{name: "nonexistent"})

	tools := r.Tools()
	if len(tools) != 1 {
		t.Errorf("expected 1 tool after no-op update, got %d", len(tools))
	}
}

// ---------------------------------------------------------------------------
// ToolRegistry — RegisterExtra / RegisterExtraFactory / ExtraTools
// ---------------------------------------------------------------------------

func TestRegisterExtra(t *testing.T) {
	r := NewToolRegistry()
	r.RegisterExtra("consult_minister", mockTool{name: "consult_minister"})
	r.RegisterExtra("enact_ritual", mockTool{name: "enact_ritual"})

	// Any minister that lists these names gets the same static tool instances
	chancellorExtras := r.ExtraTools("secretary", []string{"consult_minister", "enact_ritual"})
	if len(chancellorExtras) != 2 {
		t.Fatalf("expected 2 extra tools, got %d", len(chancellorExtras))
	}
	names := toolNames(chancellorExtras)
	assertHas(t, names, "consult_minister")
	assertHas(t, names, "enact_ritual")

	// Forge also gets them if its def lists them
	forgeExtras := r.ExtraTools("forge", []string{"consult_minister", "enact_ritual"})
	if len(forgeExtras) != 2 {
		t.Fatalf("expected 2 extra tools for forge, got %d", len(forgeExtras))
	}

	// A minister with no extra_tools gets nothing
	noExtras := r.ExtraTools("forge", nil)
	if len(noExtras) != 0 {
		t.Errorf("forge with no extra_tools should get 0 tools, got %d", len(noExtras))
	}

	// Unknown names are silently skipped
	unknownExtras := r.ExtraTools("secretary", []string{"nonexistent"})
	if len(unknownExtras) != 0 {
		t.Errorf("unknown extra tool name should be skipped, got %d", len(unknownExtras))
	}
}

func TestRegisterExtraFactory(t *testing.T) {
	r := NewToolRegistry()
	r.RegisterExtraFactory("ask_ruler", func(mid string) Tool {
		return mockTool{name: "ask_ruler_" + mid}
	})

	// Each minister gets its own instance via the factory
	chancellorExtras := r.ExtraTools("chancellor", []string{"ask_ruler"})
	require := len(chancellorExtras)
	if require != 1 {
		t.Fatalf("expected 1 factory extra tool, got %d", require)
	}
	if chancellorExtras[0].Name() != "ask_ruler_chancellor" {
		t.Errorf("factory should produce per-minister tool, got %q", chancellorExtras[0].Name())
	}

	warExtras := r.ExtraTools("war", []string{"ask_ruler"})
	if len(warExtras) != 1 || warExtras[0].Name() != "ask_ruler_war" {
		t.Errorf("factory should produce per-minister tool for war, got %v", warExtras)
	}
}

func TestExtraToolsMixedStaticAndFactory(t *testing.T) {
	r := NewToolRegistry()
	r.RegisterExtra("consult_minister", mockTool{name: "consult_minister"})
	r.RegisterExtraFactory("ask_ruler", func(mid string) Tool {
		return mockTool{name: "ask_ruler_" + mid}
	})

	// A minister listing both gets both
	all := r.ExtraTools("secretary", []string{"consult_minister", "ask_ruler"})
	names := toolNames(all)
	assertHas(t, names, "consult_minister")
	assertHas(t, names, "ask_ruler_secretary")
}

func TestExtraToolsNotInPublicEntries(t *testing.T) {
	// Extra tools are separate from public entries
	r := NewToolRegistry()
	r.RegisterExtra("secret", mockTool{name: "secret"})

	// Public tools should be empty
	if len(r.Tools()) != 0 {
		t.Error("extra tools should not appear in public Tools()")
	}
}

// ---------------------------------------------------------------------------
// ToolRegistry — ForPermissions with empty registry
// ---------------------------------------------------------------------------

func TestForPermissionsEmptyRegistry(t *testing.T) {
	r := NewToolRegistry()
	perm, _ := ParsePermissions("rwxrwxrwx")

	tools := r.ForPermissions(perm)
	if len(tools) != 0 {
		t.Errorf("empty registry should return no tools, got %d", len(tools))
	}
}

// ---------------------------------------------------------------------------
// ToolRegistry — ForPermissions matching logic
// ---------------------------------------------------------------------------

func TestForPermissionsMatchByRead(t *testing.T) {
	r := NewToolRegistry()
	r.Register(mockTool{name: "read_file"}, Permissions{Earth: Access{Read: true}})

	// Minister with earth Read should see read_file
	perm := Permissions{Earth: Access{Read: true}}
	tools := r.ForPermissions(perm)
	names := toolNames(tools)
	assertHas(t, names, "read_file")
}

func TestForPermissionsMatchByWrite(t *testing.T) {
	r := NewToolRegistry()
	r.Register(mockTool{name: "create_manifest"}, Permissions{Intent: Access{Write: true}})

	// Minister with intent Write should see create_manifest
	perm := Permissions{Intent: Access{Write: true}}
	tools := r.ForPermissions(perm)
	names := toolNames(tools)
	assertHas(t, names, "create_manifest")
}

func TestForPermissionsMatchByExecute(t *testing.T) {
	r := NewToolRegistry()
	r.Register(mockTool{name: "run_shell_command"}, Permissions{Earth: Access{Execute: true}})

	// Minister with earth Execute should see run_shell_command
	perm := Permissions{Earth: Access{Execute: true}}
	tools := r.ForPermissions(perm)
	names := toolNames(tools)
	assertHas(t, names, "run_shell_command")
}

func TestForPermissionsNoMatch(t *testing.T) {
	r := NewToolRegistry()
	r.Register(mockTool{name: "run_shell_command"}, Permissions{Earth: Access{Execute: true}})

	// Strategist: r-----rw- (no earth Execute)
	strategistPerm, _ := ParsePermissions("r-----rw-")
	tools := r.ForPermissions(strategistPerm)
	names := toolNames(tools)
	assertNotHas(t, names, "run_shell_command")
}

func TestForPermissionsMultiToolFiltering(t *testing.T) {
	r := NewToolRegistry()

	readFile := mockTool{name: "read_file"}
	writeFile := mockTool{name: "write_file"}
	shellCmd := mockTool{name: "run_shell_command"}
	createManifest := mockTool{name: "create_manifest"}

	r.Register(readFile, Permissions{Earth: Access{Read: true}})
	r.Register(writeFile, Permissions{Earth: Access{Read: true, Write: true}})
	r.Register(shellCmd, Permissions{Earth: Access{Execute: true}})
	r.Register(createManifest, Permissions{Intent: Access{Write: true}})

	// Strategist: r-----rw- (earth: r--, intent: rw-)
	strategistPerm, _ := ParsePermissions("r-----rw-")
	strategistTools := r.ForPermissions(strategistPerm)
	strategistNames := toolNames(strategistTools)
	assertHas(t, strategistNames, "read_file")            // earth Read matches
	assertHas(t, strategistNames, "write_file")           // earth Read matches (shared Read)
	assertHas(t, strategistNames, "create_manifest")      // intent Write matches
	assertNotHas(t, strategistNames, "run_shell_command") // strategist has no earth Execute

	// Forge: rwxr---w- (earth: rwx, intent: -w-)
	forgePerm, _ := ParsePermissions("rwxr---w-")
	forgeTools := r.ForPermissions(forgePerm)
	forgeNames := toolNames(forgeTools)
	assertHas(t, forgeNames, "read_file")
	assertHas(t, forgeNames, "write_file")
	assertHas(t, forgeNames, "run_shell_command")
	assertHas(t, forgeNames, "create_manifest")
}

// ---------------------------------------------------------------------------
// ToolRegistry — ForPermissions does not return extra tools
// ---------------------------------------------------------------------------

func TestForPermissionsDoesNotReturnExtraTools(t *testing.T) {
	r := NewToolRegistry()
	r.Register(mockTool{name: "read_file"}, Permissions{Earth: Access{Read: true}})
	r.RegisterExtra("consult_minister", mockTool{name: "consult_minister"})

	chancellorPerm, _ := ParsePermissions("rwxr--rwx")
	tools := r.ForPermissions(chancellorPerm)
	names := toolNames(tools)
	assertHas(t, names, "read_file")
	// Extra tools are NOT returned by ForPermissions — only by ExtraTools
	assertNotHas(t, names, "consult_minister")

	// Extra tools are resolved separately
	extras := r.ExtraTools("secretary", []string{"consult_minister"})
	extraNames := toolNames(extras)
	assertHas(t, extraNames, "consult_minister")
}

func TestForPermissionsPureFunction(t *testing.T) {
	// ForPermissions is now a pure function of Permissions → []Tool.
	// The same permissions always yield the same tools regardless of minister ID.
	r := NewToolRegistry()
	r.Register(mockTool{name: "read_file"}, Permissions{Earth: Access{Read: true}})
	r.RegisterExtra("consult_minister", mockTool{name: "consult_minister"})

	perm := Permissions{Earth: Access{Read: true}}
	chancellorTools := r.ForPermissions(perm)
	forgeTools := r.ForPermissions(perm)

	if len(chancellorTools) != len(forgeTools) {
		t.Errorf("ForPermissions should return same tools for same perm regardless of minister")
	}
	for i := range chancellorTools {
		if chancellorTools[i].Name() != forgeTools[i].Name() {
			t.Errorf("tool mismatch at index %d: %q vs %q", i, chancellorTools[i].Name(), forgeTools[i].Name())
		}
	}
}

// ---------------------------------------------------------------------------
// Minister permission string verification — all six ministers
// ---------------------------------------------------------------------------

func TestChancellorPermissions(t *testing.T) {
	p, err := ParsePermissions("rwxr--rwx")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	// Earth: rwx — full access to source code
	if !p.Earth.Read || !p.Earth.Write || !p.Earth.Execute {
		t.Error("chancellor should have full earth access (rwx)")
	}
	// Heaven: r-- — read test results only
	if !p.Heaven.Read {
		t.Error("chancellor should have heaven Read")
	}
	if p.Heaven.Write || p.Heaven.Execute {
		t.Error("chancellor should NOT have heaven Write or Execute")
	}
	// Intent: rwx — full control of edicts, zhengming, rituals
	if !p.Intent.Read || !p.Intent.Write || !p.Intent.Execute {
		t.Error("chancellor should have full intent access (rwx)")
	}
}

func TestForgePermissions(t *testing.T) {
	p, err := ParsePermissions("rwxr---w-")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	// Earth: rwx — full access to source code
	if !p.Earth.Read || !p.Earth.Write || !p.Earth.Execute {
		t.Error("forge should have full earth access (rwx)")
	}
	// Heaven: r-- — read test results only
	if !p.Heaven.Read {
		t.Error("forge should have heaven Read")
	}
	if p.Heaven.Write || p.Heaven.Execute {
		t.Error("forge should NOT have heaven Write or Execute")
	}
	// Intent: -w- — write manifests only (no read/execute of intent)
	if p.Intent.Read || p.Intent.Execute {
		t.Error("forge should NOT have intent Read or Execute")
	}
	if !p.Intent.Write {
		t.Error("forge should have intent Write (create manifests)")
	}
}

func TestJudgePermissions(t *testing.T) {
	p, err := ParsePermissions("rwxrwxr--")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	// Earth: rwx — read, write, execute code
	if !p.Earth.Read || !p.Earth.Write || !p.Earth.Execute {
		t.Error("judge should have full earth access (rwx)")
	}
	// Heaven: rwx — full access to test results, run tests, write verdicts
	if !p.Heaven.Read || !p.Heaven.Write || !p.Heaven.Execute {
		t.Error("judge should have full heaven access (rwx)")
	}
	// Intent: r-- — read edicts, zhengming (no write/execute)
	if !p.Intent.Read {
		t.Error("judge should have intent Read")
	}
	if p.Intent.Write || p.Intent.Execute {
		t.Error("judge should NOT have intent Write or Execute")
	}
}

func TestSagePermissions(t *testing.T) {
	p, err := ParsePermissions("r--r--rwx")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	// Earth: r-- — read-only source code access
	if !p.Earth.Read {
		t.Error("sage should have earth Read")
	}
	if p.Earth.Write || p.Earth.Execute {
		t.Error("sage should NOT have earth Write or Execute")
	}
	// Heaven: r-- — read test results only
	if !p.Heaven.Read {
		t.Error("sage should have heaven Read")
	}
	if p.Heaven.Write || p.Heaven.Execute {
		t.Error("sage should NOT have heaven Write or Execute")
	}
	// Intent: rwx — suggest edicts, zhengming, record precedents
	if !p.Intent.Read || !p.Intent.Write || !p.Intent.Execute {
		t.Error("sage should have full intent access (rwx)")
	}
}

func TestStrategistPermissions(t *testing.T) {
	p, err := ParsePermissions("r-----rwx")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	// Earth: r-- — read-only source code access
	if !p.Earth.Read {
		t.Error("strategist should have earth Read")
	}
	if p.Earth.Write || p.Earth.Execute {
		t.Error("strategist should NOT have earth Write or Execute")
	}
	// Heaven: --- — no access to test results
	if p.Heaven.Read || p.Heaven.Write || p.Heaven.Execute {
		t.Error("strategist should have NO heaven access")
	}
	// Intent: rwx — read/write lings + execute zhengming
	if !p.Intent.Read || !p.Intent.Write || !p.Intent.Execute {
		t.Error("strategist should have intent Read, Write, and Execute (ask_ruler)")
	}
}

// ---------------------------------------------------------------------------
// ToolRegistry.String — smoke test
// ---------------------------------------------------------------------------

func TestToolRegistryString(t *testing.T) {
	r := NewToolRegistry()
	r.Register(mockTool{name: "read_file"}, Permissions{Earth: Access{Read: true}})
	r.RegisterExtra("consult_minister", mockTool{name: "consult_minister"})

	s := r.String()
	if s == "" {
		t.Error("String() should not be empty")
	}
	// Should mention the public tool with its permission string
	if !contains(s, "r--") || !contains(s, "read_file") {
		t.Errorf("String() should mention read_file with permission, got:\n%s", s)
	}
	// Should mention the extra tool
	if !contains(s, "consult_minister") {
		t.Errorf("String() should mention consult_minister extra tool, got:\n%s", s)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func toolNames(tools []Tool) map[string]bool {
	m := make(map[string]bool)
	for _, t := range tools {
		m[t.Name()] = true
	}
	return m
}

func assertHas(t *testing.T, names map[string]bool, name string) {
	t.Helper()
	if !names[name] {
		t.Errorf("expected tool %q to be present", name)
	}
}

func assertNotHas(t *testing.T, names map[string]bool, name string) {
	t.Helper()
	if names[name] {
		t.Errorf("expected tool %q to NOT be present", name)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && len(sub) > 0 && stringContains(s, sub)))
}

func stringContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
