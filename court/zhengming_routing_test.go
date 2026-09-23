package court

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestZhengmingRouting_Chancellor verifies that when Chancellor's ask_ruler tool
// is invoked, the ZhengmingPendingMsg carries MinisterID="chancellor" — not "secretary".
//
// Before the fix (edict 489), the shared AskRulerTool in the registry
// omitted MinisterID, and the Requester was always the Chancellor, so all
// zhengming notifications routed to MinisterID="secretary" regardless of
// which minister actually asked the question.
func TestZhengmingRouting_Chancellor(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())
	require.NotNil(t, s)

	// Capture ZhengmingPendingMsg notifications
	var mu sync.Mutex
	var pendingMsgs []ZhengmingPendingMsg
	s.SetNotify(func(msg any) {
		mu.Lock()
		defer mu.Unlock()
		if m, ok := msg.(ZhengmingPendingMsg); ok {
			pendingMsgs = append(pendingMsgs, m)
		}
	})

	// Build tool registry — ask_ruler is now in commonTools,
	// resolved per-minister via ExtraTools(ministerID, commonTools).
	registry := tools.NewToolRegistry()
	tools.RegisterBuiltinTools(registry, tools.ToolRegistrationOpts{
		Ctx: tools.ToolContext{
			RepoInfo: &repo.RepoInfo{ProjectRoot: "/tmp"},
			Username: "testuser",
			Project:  "testproject",
			DB:       db,
		},
		ZhengmingRequester: s,
		WaitForZhengming:   nil, // no WaitForAnswer — Call returns immediately with "pending" status
	})

	// Get Sage's tools — ask_ruler is now a common tool
	sageExtras := registry.ExtraTools("chancellor", commonTools)

	// Find the ask_ruler tool
	var zhengmingTool tools.Tool
	for _, tool := range sageExtras {
		if tool.Name() == "ask_ruler" {
			zhengmingTool = tool
			break
		}
	}
	require.NotNil(t, zhengmingTool, "sage should have ask_ruler tool")

	// Call the tool
	input := `{"edict_id": 1, "questions": [{"text": "Approve?", "options": ["Yes", "No"]}]}`
	_, err := zhengmingTool.Call(context.Background(), input)
	require.NoError(t, err, "ask_ruler Call should not error")

	// Verify the ZhengmingPendingMsg has MinisterID="chancellor"
	mu.Lock()
	defer mu.Unlock()

	require.Len(t, pendingMsgs, 1, "should have received one ZhengmingPendingMsg")
	assert.Equal(t, "chancellor", pendingMsgs[0].MinisterID,
		"ZhengmingPendingMsg.MinisterID should be 'chancellor', not 'secretary' — the tool's MinisterID must flow through to the pending message")
}

// TestZhengmingRouting_War verifies that when War's ask_ruler
// tool is invoked, the ZhengmingPendingMsg carries MinisterID="war".
func TestZhengmingRouting_War(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())
	require.NotNil(t, s)

	// Capture ZhengmingPendingMsg notifications
	var mu sync.Mutex
	var pendingMsgs []ZhengmingPendingMsg
	s.SetNotify(func(msg any) {
		mu.Lock()
		defer mu.Unlock()
		if m, ok := msg.(ZhengmingPendingMsg); ok {
			pendingMsgs = append(pendingMsgs, m)
		}
	})

	// Build tool registry — ask_ruler is now a common tool
	registry := tools.NewToolRegistry()
	tools.RegisterBuiltinTools(registry, tools.ToolRegistrationOpts{
		Ctx: tools.ToolContext{
			RepoInfo: &repo.RepoInfo{ProjectRoot: "/tmp"},
			Username: "testuser",
			Project:  "testproject",
			DB:       db,
		},
		ZhengmingRequester: s,
		WaitForZhengming:   nil, // no WaitForAnswer — Call returns immediately
	})

	// Get War's tools — ask_ruler is now a common tool
	strategistExtras := registry.ExtraTools("war", commonTools)

	// Find the ask_ruler tool
	var zhengmingTool tools.Tool
	for _, tool := range strategistExtras {
		if tool.Name() == "ask_ruler" {
			zhengmingTool = tool
			break
		}
	}
	require.NotNil(t, zhengmingTool, "war should have ask_ruler tool")

	// Call the tool
	input := `{"edict_id": 2, "questions": [{"text": "Which approach?", "options": ["A", "B"]}]}`
	_, err := zhengmingTool.Call(context.Background(), input)
	require.NoError(t, err, "ask_ruler Call should not error")

	// Verify the ZhengmingPendingMsg has MinisterID="war"
	mu.Lock()
	defer mu.Unlock()

	require.Len(t, pendingMsgs, 1, "should have received one ZhengmingPendingMsg")
	assert.Equal(t, "war", pendingMsgs[0].MinisterID,
		"ZhengmingPendingMsg.MinisterID should be 'war', not 'secretary'")
}

// TestZhengmingRouting_DBRecord verifies that the storage.Zhengming DB record
// also carries the correct MinisterID from the calling tool, not the requester's ID.
func TestZhengmingRouting_DBRecord(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())
	require.NotNil(t, s)
	s.SetNotify(func(msg any) {}) // discard notifications

	// Build tool registry — ask_ruler is now a common tool
	registry := tools.NewToolRegistry()
	tools.RegisterBuiltinTools(registry, tools.ToolRegistrationOpts{
		Ctx: tools.ToolContext{
			RepoInfo: &repo.RepoInfo{ProjectRoot: "/tmp"},
			Username: "testuser",
			Project:  "testproject",
			DB:       db,
		},
		ZhengmingRequester: s,
		WaitForZhengming:   nil, // no WaitForAnswer — Call returns immediately
	})

	// Get Chancellor's tools and find ask_ruler
	chancellorExtras := registry.ExtraTools("chancellor", commonTools)
	var zhengmingTool tools.Tool
	for _, tool := range chancellorExtras {
		if tool.Name() == "ask_ruler" {
			zhengmingTool = tool
			break
		}
	}
	require.NotNil(t, zhengmingTool, "chancellor should have ask_ruler tool")

	// Call the tool
	input := `{"edict_id": 3, "questions": [{"text": "OK?", "options": ["Yes", "No"]}]}`
	_, err := zhengmingTool.Call(context.Background(), input)
	require.NoError(t, err, "ask_ruler Call should not error")

	// Verify the DB record has MinisterID="chancellor"
	var zhengming storage.Zhengming
	err = db.Where("edict_id = ? AND username = ? AND project = ?", 3, "testuser", "testproject").First(&zhengming).Error
	require.NoError(t, err, "should find the zhengming DB record")
	assert.Equal(t, "chancellor", zhengming.MinisterID,
		"storage.Zhengming.MinisterID should be 'chancellor', not 'secretary'")
}

// TestZhengmingRouting_ChannelIDFromContext verifies that when ask_ruler is
// invoked with a channel-carrying context (as ritual execution threads via
// tools.ChannelIDKey), the emitted ZhengmingPendingMsg carries that channel so
// the TUI routes the prompt to the ritual tab instead of the active tab.
func TestZhengmingRouting_ChannelIDFromContext(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())
	require.NotNil(t, s)

	var mu sync.Mutex
	var pendingMsgs []ZhengmingPendingMsg
	s.SetNotify(func(msg any) {
		mu.Lock()
		defer mu.Unlock()
		if m, ok := msg.(ZhengmingPendingMsg); ok {
			pendingMsgs = append(pendingMsgs, m)
		}
	})

	registry := tools.NewToolRegistry()
	tools.RegisterBuiltinTools(registry, tools.ToolRegistrationOpts{
		Ctx: tools.ToolContext{
			RepoInfo: &repo.RepoInfo{ProjectRoot: "/tmp"},
			Username: "testuser",
			Project:  "testproject",
			DB:       db,
		},
		ZhengmingRequester: s,
		WaitForZhengming:   nil,
	})

	judgeExtras := registry.ExtraTools("judge", commonTools)
	var zhengmingTool tools.Tool
	for _, tool := range judgeExtras {
		if tool.Name() == "ask_ruler" {
			zhengmingTool = tool
			break
		}
	}
	require.NotNil(t, zhengmingTool, "judge should have ask_ruler tool")

	ctx := context.WithValue(context.Background(), tools.ChannelIDKey{}, "e42")
	input := `{"edict_id": 42, "questions": [{"text": "Proceed?", "options": ["Yes", "No"]}]}`
	_, err := zhengmingTool.Call(ctx, input)
	require.NoError(t, err, "ask_ruler Call should not error")

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, pendingMsgs, 1, "should have received one ZhengmingPendingMsg")
	assert.Equal(t, "e42", pendingMsgs[0].ChannelID,
		"ZhengmingPendingMsg.ChannelID should be resolved from tools.ChannelIDKey{} in ctx")
	assert.Equal(t, "judge", pendingMsgs[0].MinisterID,
		"ZhengmingPendingMsg.MinisterID should still carry the calling minister")
}
