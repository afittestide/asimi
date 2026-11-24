package tools

import (
	"fmt"
	"strings"
	"sync"

	"github.com/afittestide/asimi/internal/types"
)

// Access represents read/write/execute permissions within a single realm.
type Access = types.Access

// Permissions holds per-realm access for a minister or a tool classification.
type Permissions = types.Permissions

// ToolPermission pairs a tool with its permission classification.
type ToolPermission struct {
	Tool        Tool
	Permissions Permissions
	// SubsetMatch requires a minister's permissions to strictly cover every bit
	// in Permissions (perm.Allows) rather than matching any single bit
	// (perm.Match). Set for MCP tools to enforce least privilege while
	// preserving dual-realm OR matching for built-ins like create_manifest.
	SubsetMatch bool
}

// ParsePermissions parses a 9-character permission string of the form
// "rwxrwxrwx" where positions [0-2] = earth, [3-5] = heaven, [6-8] = intent.
// Each triple is (Read, Write, Execute); '-' means denied.
func ParsePermissions(s string) (Permissions, error) {
	return types.ParsePermissions(s)
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
		r.entries[name] = ToolPermission{Tool: tool, Permissions: existing.Permissions, SubsetMatch: existing.SubsetMatch}
	}
}

// Set registers a tool with the given permissions, replacing any existing
// tool instance and permissions for the same name rather than unioning them.
func (r *ToolRegistry) Set(tool Tool, perm Permissions) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.entries[tool.Name()] = ToolPermission{Tool: tool, Permissions: perm}
}

// SetMCP registers or replaces a dynamic MCP tool using strict subset
// permission matching. It rejects nil tools, empty names, sealed-deny
// ("---------") permissions, and any name that collides with a built-in or
// extra tool.
func (r *ToolRegistry) SetMCP(tool Tool, perm Permissions) error {
	if tool == nil || tool.Name() == "" {
		return fmt.Errorf("cannot register MCP tool: tool name is required")
	}
	if perm.IsEmpty() {
		return fmt.Errorf("cannot register MCP tool %q: permissions %s represent sealed deny", tool.Name(), perm.String())
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	name := tool.Name()
	if _, isExtra := r.extras[name]; isExtra {
		return fmt.Errorf("cannot register MCP tool %q: collides with extra tool", name)
	}
	if _, isFactory := r.factories[name]; isFactory {
		return fmt.Errorf("cannot register MCP tool %q: collides with extra tool factory", name)
	}
	if existing, exists := r.entries[name]; exists && !existing.SubsetMatch {
		return fmt.Errorf("cannot register MCP tool %q: collides with built-in tool", name)
	}

	r.entries[name] = ToolPermission{
		Tool:        tool,
		Permissions: perm,
		SubsetMatch: true,
	}
	return nil
}

// HasNonMCPTool reports whether name is already occupied by a built-in public
// tool or an extra tool / factory (i.e., anything other than a dynamic MCP
// registration).
func (r *ToolRegistry) HasNonMCPTool(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if _, isExtra := r.extras[name]; isExtra {
		return true
	}
	if _, isFactory := r.factories[name]; isFactory {
		return true
	}
	if existing, exists := r.entries[name]; exists && !existing.SubsetMatch {
		return true
	}
	return false
}

// Unregister removes a tool from the registry by name.
// If the tool is not registered, this is a no-op.
func (r *ToolRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.entries, name)
}

// Get returns the ToolPermission entry for the given tool name and true,
// or the zero value and false if not registered.
func (r *ToolRegistry) Get(name string) (ToolPermission, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tp, ok := r.entries[name]
	return tp, ok
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
		allowed := false
		if tp.SubsetMatch {
			allowed = perm.Allows(tp.Permissions)
		} else {
			allowed = perm.Match(tp.Permissions)
		}
		if allowed {
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
