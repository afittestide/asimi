package types

import "fmt"

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

// IsEmpty reports whether no permission bits are set ("---------", sealed deny).
func (p Permissions) IsEmpty() bool {
	return p == (Permissions{})
}

func accessAllows(minister, required Access) bool {
	return (!required.Read || minister.Read) &&
		(!required.Write || minister.Write) &&
		(!required.Execute || minister.Execute)
}

// Allows reports whether the minister's permissions strictly cover every bit
// required by tool (non-empty subset: toolBits != 0 && toolBits subset of
// ministerBits). Used for MCP tools so multi-bit classifications such as
// "rw-------" never leak to read-only ministers, and "---------" (sealed deny)
// never matches any minister.
func (p Permissions) Allows(tool Permissions) bool {
	if tool.IsEmpty() {
		return false
	}
	return accessAllows(p.Earth, tool.Earth) &&
		accessAllows(p.Heaven, tool.Heaven) &&
		accessAllows(p.Intent, tool.Intent)
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
