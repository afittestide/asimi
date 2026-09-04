package repo

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRefreshDiff_NilReceiver(t *testing.T) {
	// RefreshDiff must not panic on nil receiver
	var r *RepoInfo
	require.NotPanics(t, func() { r.RefreshDiff() })
}

func TestRefreshDiff_NilRepo(t *testing.T) {
	// RefreshDiff must not panic when repo field is nil
	r := &RepoInfo{}
	require.NotPanics(t, func() { r.RefreshDiff() })
}

func TestRefreshDiffWithTTL_NilReceiver(t *testing.T) {
	// RefreshDiffWithTTL must not panic on nil receiver
	var r *RepoInfo
	require.NotPanics(t, func() { r.RefreshDiffWithTTL(50 * time.Millisecond) })
}

func TestRefreshDiffWithTTL_NilRepo(t *testing.T) {
	// RefreshDiffWithTTL must not panic when repo field is nil
	r := &RepoInfo{}
	require.NotPanics(t, func() { r.RefreshDiffWithTTL(50 * time.Millisecond) })
	// diffInitialized must remain false — RefreshDiff could not have completed
	require.False(t, r.diffInitialized,
		"RefreshDiffWithTTL on nil repo must not mark diff as initialized")
}

func TestIsClean_NilReceiver(t *testing.T) {
	// IsClean must not panic on nil receiver and should report clean
	var r *RepoInfo
	require.True(t, r.IsClean(), "nil receiver should return true (clean)")
}

func TestIsClean_NilRepo(t *testing.T) {
	// IsClean must not panic when repo field is nil, reporting clean
	r := &RepoInfo{}
	require.NotPanics(t, func() { _ = r.IsClean() })
	require.True(t, r.IsClean(), "repo with nil backing should return true (clean)")
}
