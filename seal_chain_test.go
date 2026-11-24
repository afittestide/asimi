package main

import (
	"testing"
	"time"

	"github.com/afittestide/asimi/storage"
	"github.com/stretchr/testify/assert"
)

func TestRenderSealChain_AllSealsGranted(t *testing.T) {
	seals := []storage.Seal{
		{MinisterID: "judge", Realm: "earth", SealID: "j1"},
		{MinisterID: "chancellor", Realm: "earth", SealID: "c2"},
		{MinisterID: "ruler", Realm: "earth", SealID: "r3"},
	}
	output := renderSealChain(seals, 80)
	assert.Contains(t, output, "Judge")
	assert.Contains(t, output, "Chancellor")
	assert.Contains(t, output, "Ruler")
	// All sealed → all show ✓
	assert.Contains(t, output, "✓")
	assert.NotContains(t, output, "○")
}

func TestRenderSealChain_NoSealsGranted(t *testing.T) {
	output := renderSealChain(nil, 80)
	// None sealed → all show ○
	assert.Contains(t, output, "○")
	assert.Contains(t, output, "Judge")
	assert.Contains(t, output, "Chancellor")
	assert.Contains(t, output, "Ruler")
}

func TestRenderSealChain_PartialSeals(t *testing.T) {
	seals := []storage.Seal{
		{MinisterID: "judge", Realm: "earth", SealID: "j1"},
	}
	output := renderSealChain(seals, 80)
	// Judge sealed, Chancellor and Ruler not
	assert.Contains(t, output, "✓")
	assert.Contains(t, output, "○")
	assert.Contains(t, output, "Judge")
	assert.Contains(t, output, "Chancellor")
	assert.Contains(t, output, "Ruler")
}

func TestRenderSealChain_StaleSealRendersUngranted(t *testing.T) {
	staleAt := time.Now()
	seals := []storage.Seal{
		{MinisterID: "judge", Realm: "earth", SealID: "j1"},
		{MinisterID: "chancellor", Realm: "earth", SealID: "c2"},
		{MinisterID: "ruler", Realm: "earth", SealID: "r3", StaleAt: &staleAt},
	}
	output := renderSealChain(seals, 80)
	assert.Contains(t, output, "[✓ Judge]")
	assert.Contains(t, output, "[✓ Chancellor]")
	assert.Contains(t, output, "[○ Ruler]")
	// The tombstone must not be badged as live.
	assert.NotContains(t, output, "[✓ Ruler]")
}

func TestRenderSealChain_IgnoresIntentSeals(t *testing.T) {
	seals := []storage.Seal{
		{MinisterID: "chancellor", Realm: "intent", SealID: "ci1"},
		{MinisterID: "ruler", Realm: "intent", SealID: "ri1"},
	}
	output := renderSealChain(seals, 80)
	assert.Contains(t, output, "[○ Judge]")
	assert.Contains(t, output, "[○ Chancellor]")
	assert.Contains(t, output, "[○ Ruler]")
	assert.NotContains(t, output, "✓")
}
