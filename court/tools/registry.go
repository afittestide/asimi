package tools

import (
	"fmt"
	"strings"
	"sync"
)

// Access represents read/write/execute permissions within a single realm.
type Access struct {
	Read    bool
	Write   bool
	Execute bool
}

// Permissions holds per-realm access for a minister or a tool classification.
type Permissions struct {
	Earth  Access
	Heaven Access
	Intent Access
}

// ToolPermission pairs a tool with its permission classification.
type ToolPermission struct {
	Tool        Tool
	Permissions Permissions
}

// ParsePermissions parses a 9-character permission string of the form
// "rwxrwxrwx" where positions [0-2] = earth, [3-5] = heaven, [6-8] = intent.
// Each triple is (Read, Write, Execute); '-' means denied.
func ParsePermissions(s string) (Permissions, error) {
	if len(s) != 9 {
		return Permissions{}, fmt.Errorf("invalid permission string %q: must be 9 characters", s)
	}

	parse := func(rwx string) (Access, error) {
		if len(rwx) != 3 {
			return Access{}, fmt.Errorf("invalid rwx triple %q", rwx)
		}
		read := rwx[0] == 'r'
		write := rwx[1] == 'w'
		exec := rwx[2] == 'x'
		// Validate: each position is either the expected letter or '-'
		for i, ch := range rwx {
			expected := []byte{'r', 'w', 'x'}[i]
			if ch != '-' && ch != rune(expected) {
				return Access{}, fmt.Errorf("invalid character %q in position %d of %q", ch, i, rwx)
			}
		}
		return Access{Read: read, Write: write, Execute: exec}, nil
	}

	earth, err := parse(s[0:3])
	if err != nil {
		return Permissions{}, err
	}
	heaven, err := parse(s[3:6])
	if err != nil {
		return Permissions{}, err
	}
	intent, err := parse(s[6:9])
	if err != nil {
		return Permissions{}, err
	}

	return Permissions{Earth: earth, Heaven: heaven, Intent: intent}, nil
}

// accessMatch returns true when both accesses share at least one true flag.
func accessMatch(minister, tool Access) bool {
	return (minister.Read && tool.Read) ||
		(minister.Write && tool.Write) ||
		(minister.Execute && tool.Execute)
}

// Match returns true when the minister's permissions match the tool's
// permissions in at least one realm. A realm matches when both the
// minister and the tool have true for at least one shared access flag.
func (p Permissions) Match(tool Permissions) bool {
	return accessMatch(p.Earth, tool.Earth) ||
		accessMatch(p.Heaven, tool.Heaven) ||
		accessMatch(p.Intent, tool.Intent)
}

// String returns the 9-character permission string representation.
func (p Permissions) String() string {
	formatAccess := func(a Access) string {
		r, w, x := '-', '-', '-'
		if a.Read {
			r = 'r'
		}
		if a.Write {
			w = 'w'
		}
		if a.Execute {
			x = 'x'
		}
		return string([]byte{byte(r), byte(w), byte(x)})
	}
	return formatAccess(p.Earth) + formatAccess(p.Heaven) + formatAccess(p.Intent)
}

// ToolRegistry holds tool registrations with permission classifications
// and extra tool mappings for per-minister tool resolution.
type ToolRegistry struct {
	mu        sync.RWMutex
	entries   map[string]ToolPermission    // key = tool name
	extras    map[string]Tool              // static extra tools (key = name)
	factories map[string]func(string) Tool // factory extra tools (key = name)
}

// NewToolRegistry creates an empty tool registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		entries:   make(map[string]ToolPermission),
		extras:    make(map[string]Tool),
		factories: make(map[string]func(string) Tool),
	}
}

// Register adds a tool with its permission classification to the registry.
// If a tool with the same name is already registered, the permissions are
// merged (union of realm access flags) and the existing tool instance kept —
// this allows a tool to be visible through multiple permission classes
// (e.g. create_manifest under both heaven Write and intent Write) without
// panicking.
func (r *ToolRegistry) Register(tool Tool, perm Permissions) {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := tool.Name()
	if existing, exists := r.entries[name]; exists {
		r.entries[name] = ToolPermission{Tool: existing.Tool, Permissions: mergePermissions(existing.Permissions, perm)}
		return
	}
	r.entries[name] = ToolPermission{Tool: tool, Permissions: perm}
}

// mergePermissions returns the union of two permission classifications.
func mergePermissions(a, b Permissions) Permissions {
	return Permissions{
		Earth:  mergeAccess(a.Earth, b.Earth),
		Heaven: mergeAccess(a.Heaven, b.Heaven),
		Intent: mergeAccess(a.Intent, b.Intent),
	}
}

func mergeAccess(a, b Access) Access {
	return Access{
		Read:    a.Read || b.Read,
		Write:   a.Write || b.Write,
		Execute: a.Execute || b.Execute,
	}
}

// Update replaces an existing tool registration with a new instance.
// If the tool is not registered, this is a no-op.
// This is useful when a tool's configuration changes after initial registration.
func (r *ToolRegistry) Update(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := tool.Name()
	if existing, ok := r.entries[name]; ok {
		r.entries[name] = ToolPermission{Tool: tool, Permissions: existing.Permissions}
	}
}

// RegisterExtra registers a static extra tool by name. Extra tools are
// resolved per-minister via ExtraTools(ministerID, names) and are not
// subject to permission matching.
func (r *ToolRegistry) RegisterExtra(name string, tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.extras[name] = tool
}

// RegisterExtraFactory registers a factory that produces a tool instance
// for a given ministerID. Used for per-minister tools like ask_ruler
// that need the MinisterID embedded in the tool.
func (r *ToolRegistry) RegisterExtraFactory(name string, factory func(string) Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[name] = factory
}

// ExtraTools resolves the given extra tool names for the given minister.
// If a factory is registered for a name, it is invoked with ministerID to
// produce the tool instance. Otherwise, the static registration is used.
// Unknown names are silently skipped.
func (r *ToolRegistry) ExtraTools(ministerID string, names []string) []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Tool
	for _, name := range names {
		if factory, ok := r.factories[name]; ok {
			result = append(result, factory(ministerID))
			continue
		}
		if tool, ok := r.extras[name]; ok {
			result = append(result, tool)
		}
	}
	return result
}

// ForPermissions returns all public tools whose permissions match the
// given minister permissions. Extra tools are resolved separately via
// ExtraTools(ministerID, names).
func (r *ToolRegistry) ForPermissions(perm Permissions) []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]bool)
	var result []Tool

	for name, tp := range r.entries {
		if seen[name] {
			continue
		}
		if perm.Match(tp.Permissions) {
			seen[name] = true
			result = append(result, tp.Tool)
		}
	}

	return result
}

// Tools returns all publicly registered tools regardless of permissions.
// Useful for debugging and introspection.
func (r *ToolRegistry) Tools() []ToolPermission {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]ToolPermission, 0, len(r.entries))
	for _, tp := range r.entries {
		result = append(result, tp)
	}
	return result
}

// ExtraToolNames returns the names of all registered extra tools and
// factories. Useful for debugging and introspection.
func (r *ToolRegistry) ExtraToolNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]bool)
	var result []string
	for name := range r.extras {
		if !seen[name] {
			seen[name] = true
			result = append(result, name)
		}
	}
	for name := range r.factories {
		if !seen[name] {
			seen[name] = true
			result = append(result, name)
		}
	}
	return result
}

// String returns a human-readable summary of the registry contents.
func (r *ToolRegistry) String() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var b strings.Builder
	b.WriteString("ToolRegistry:\n")
	for name, tp := range r.entries {
		fmt.Fprintf(&b, "  %s  %s\n", tp.Permissions.String(), name)
	}
	if len(r.extras) > 0 || len(r.factories) > 0 {
		b.WriteString("  extra:\n")
		for name, t := range r.extras {
			fmt.Fprintf(&b, "    [static] %s\n", name)
			_ = t
		}
		for name := range r.factories {
			fmt.Fprintf(&b, "    [factory] %s\n", name)
		}
	}
	return b.String()
}
