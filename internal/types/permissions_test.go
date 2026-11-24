package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePermissionsAndString(t *testing.T) {
	valid := []struct {
		input string
		want  Permissions
	}{
		{
			input: "---------",
			want:  Permissions{},
		},
		{
			input: "rwxrwxrwx",
			want: Permissions{
				Earth:  Access{Read: true, Write: true, Execute: true},
				Heaven: Access{Read: true, Write: true, Execute: true},
				Intent: Access{Read: true, Write: true, Execute: true},
			},
		},
		{
			input: "rw-r--rw-",
			want: Permissions{
				Earth:  Access{Read: true, Write: true},
				Heaven: Access{Read: true},
				Intent: Access{Read: true, Write: true},
			},
		},
		{
			input: "--x-w---x",
			want: Permissions{
				Earth:  Access{Execute: true},
				Heaven: Access{Write: true},
				Intent: Access{Execute: true},
			},
		},
	}

	for _, tc := range valid {
		got, err := ParsePermissions(tc.input)
		require.NoError(t, err, "input %q", tc.input)
		assert.Equal(t, tc.want, got)
		assert.Equal(t, tc.input, got.String())
	}

	invalid := []string{
		"",
		"rwx",
		"rwxrwxrwx-",
		"rwx rwx r",
		"x--------", // wrong letter in earth pos 0
		"-r-------", // wrong letter in earth pos 1
		"--w------", // wrong letter in earth pos 2
		"---w-----", // wrong letter in heaven pos 0
		"----x----", // wrong letter in heaven pos 1
		"-----r---", // wrong letter in heaven pos 2
		"------x--", // wrong letter in intent pos 0
		"-------r-", // wrong letter in intent pos 1
		"--------w", // wrong letter in intent pos 2
	}
	for _, s := range invalid {
		_, err := ParsePermissions(s)
		assert.Error(t, err, "expected error for invalid permissions %q", s)
	}
}

func TestPermissions_IsEmpty(t *testing.T) {
	deny, err := ParsePermissions("---------")
	require.NoError(t, err)
	assert.True(t, deny.IsEmpty())

	earthRead, err := ParsePermissions("r--------")
	require.NoError(t, err)
	assert.False(t, earthRead.IsEmpty())
}

func TestPermissions_Match(t *testing.T) {
	chancellor, _ := ParsePermissions("r--r--rwx")
	earthRW, _ := ParsePermissions("rw-------")
	heavenW, _ := ParsePermissions("----w----")
	dualManifest, _ := ParsePermissions("----w--w-")
	forge, _ := ParsePermissions("rwx----w-")
	deny, _ := ParsePermissions("---------")

	// OR semantics preserved for built-ins (including create_manifest dual-realm).
	assert.True(t, chancellor.Match(earthRW))
	assert.False(t, chancellor.Match(heavenW))
	assert.True(t, forge.Match(dualManifest))
	assert.False(t, forge.Match(deny))
}

func TestPermissions_Allows_SubsetMatching(t *testing.T) {
	chancellor, _ := ParsePermissions("r--r--rwx")
	war, _ := ParsePermissions("r-----rw-")
	secretary, _ := ParsePermissions("r-xrwxrwx")
	forge, _ := ParsePermissions("rwx----w-")
	rites, _ := ParsePermissions("rw-r--rw-")
	all, _ := ParsePermissions("rwxrwxrwx")

	earthR, _ := ParsePermissions("r--------")
	earthRW, _ := ParsePermissions("rw-------")
	earthRWX, _ := ParsePermissions("rwx------")
	multiRealm, _ := ParsePermissions("r--r--r--")
	deny, _ := ParsePermissions("---------")

	// Sealed deny ("---------") never matches any minister, not even rwxrwxrwx.
	assert.False(t, all.Allows(deny))
	assert.False(t, forge.Allows(deny))

	// Read-only Earth ministers cannot access rw------- MCP tools.
	assert.True(t, chancellor.Allows(earthR))
	assert.False(t, chancellor.Allows(earthRW))
	assert.True(t, war.Allows(earthR))
	assert.False(t, war.Allows(earthRW))
	assert.True(t, secretary.Allows(earthR))
	assert.False(t, secretary.Allows(earthRW))

	// Ministers with Earth rw or rwx satisfy rw-------.
	assert.True(t, forge.Allows(earthRW))
	assert.True(t, rites.Allows(earthRW))
	assert.True(t, forge.Allows(earthRWX))
	assert.False(t, rites.Allows(earthRWX))

	// Multi-realm tool requires ALL bits across ALL realms.
	assert.True(t, chancellor.Allows(multiRealm))
	assert.True(t, rites.Allows(multiRealm))
	assert.False(t, war.Allows(multiRealm))   // missing heaven r
	assert.False(t, forge.Allows(multiRealm)) // missing heaven r and intent r
}
