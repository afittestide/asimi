package court

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSession_HasUserContent_EmptySession(t *testing.T) {
	t.Parallel()
	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system", "")
	require.NoError(t, err)

	assert.False(t, sess.HasUserContent(), "Empty session should have no user content")
}

func TestSession_HasUserContent_SystemOnly(t *testing.T) {
	t.Parallel()
	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system only prompt", "")
	require.NoError(t, err)

	assert.False(t, sess.HasUserContent(), "System-only session should have no user content")
}

func TestSession_HasUserContent_WithHumanMessage(t *testing.T) {
	t.Parallel()
	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system", "")
	require.NoError(t, err)
	sess.messages = append(sess.messages, schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleUser,
		Content: textContent("Hello"),
	})

	assert.True(t, sess.HasUserContent(), "Session with Human message should have user content")
}

func TestSession_HasUserContent_WithAIMessage(t *testing.T) {
	t.Parallel()
	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system", "")
	require.NoError(t, err)
	sess.messages = append(sess.messages, schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleAssistant,
		Content: textContent("Hello"),
	})

	assert.True(t, sess.HasUserContent(), "Session with AI message should have user content")
}

func TestSession_HasUserContent_WithToolMessageOnly(t *testing.T) {
	t.Parallel()
	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system", "")
	require.NoError(t, err)
	sess.messages = append(sess.messages, schemas.ChatMessage{
		Role:            schemas.ChatMessageRoleTool,
		Content:         textContent("result"),
		ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: strPtr("tc1")},
	})

	assert.False(t, sess.HasUserContent(), "Tool-only session should have no user content")
}

func TestSession_ExtractFirstPrompt_EmptySession(t *testing.T) {
	t.Parallel()
	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system", "")
	require.NoError(t, err)

	assert.Equal(t, "", sess.ExtractFirstPrompt(), "Empty session should return empty string")
}

func TestSession_ExtractFirstPrompt_SystemOnly(t *testing.T) {
	t.Parallel()
	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system only prompt", "")
	require.NoError(t, err)

	assert.Equal(t, "", sess.ExtractFirstPrompt(), "System-only session should return empty string")
}

func TestSession_ExtractFirstPrompt_WithHumanMessage(t *testing.T) {
	t.Parallel()
	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system", "")
	require.NoError(t, err)
	sess.messages = append(sess.messages, schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleUser,
		Content: textContent("Hello, world!"),
	})

	assert.Equal(t, "Hello, world!", sess.ExtractFirstPrompt())
}

func TestSession_ExtractFirstPrompt_SkipsNonHumanMessages(t *testing.T) {
	t.Parallel()
	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system", "")
	require.NoError(t, err)
	sess.messages = append(sess.messages, schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleAssistant,
		Content: textContent("AI response"),
	})
	sess.messages = append(sess.messages, schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleUser,
		Content: textContent("Human prompt"),
	})

	assert.Equal(t, "Human prompt", sess.ExtractFirstPrompt())
}

func TestSession_ExtractFirstPrompt_ReturnsFullText(t *testing.T) {
	t.Parallel()
	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system", "")
	require.NoError(t, err)
	longText := strings.Repeat("x", 150)
	sess.messages = append(sess.messages, schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleUser,
		Content: textContent(longText),
	})

	prompt := sess.ExtractFirstPrompt()
	assert.Equal(t, 150, len(prompt), "Full prompt should be returned without truncation")
	assert.Equal(t, longText, prompt)
}

func TestSession_ExtractFirstPrompt_UserMessageWithContent(t *testing.T) {
	t.Parallel()
	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system", "")
	require.NoError(t, err)
	// A user message with text content
	sess.messages = append(sess.messages, schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleUser,
		Content: textContent("The actual prompt"),
	})

	assert.Equal(t, "The actual prompt", sess.ExtractFirstPrompt())
}

// --- Transient scratchpad buffer (e723) ---
//
// The scratchpad is a one-shot buffer on Session: the Court loads court
// history into it before a ritual step, prepareUserMessage prepends it to the
// next user message, and it is cleared in the same breath. The system prompt
// (messages[0]) never changes, so the provider cache stays valid and the
// minister sees the history on every turn including tool calls.

func TestSession_ScratchpadSetGet(t *testing.T) {
	t.Parallel()
	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system", "")
	require.NoError(t, err)

	assert.Equal(t, "", sess.GetScratchpad(), "a fresh session must have an empty scratchpad")

	sess.SetScratchpad("## Court Precedents\n- rejected: no wrappers")
	assert.Contains(t, sess.GetScratchpad(), "Court Precedents")
}

func TestSession_PrepareUserMessage_InjectsScratchpadOnce(t *testing.T) {
	t.Parallel()
	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system prompt", "")
	require.NoError(t, err)

	sess.SetScratchpad("## Ritual Script\n▶ step 1")
	sess.prepareUserMessage("Implement the edict", nil)

	// The first user message carries the scratchpad as a prefix.
	last := sess.messages[len(sess.messages)-1]
	content := msgContentStr(last)
	assert.True(t, strings.HasPrefix(content, "## Ritual Script"),
		"scratchpad must be prepended to the user message, got: %q", content)
	assert.Contains(t, content, "Implement the edict")

	// The buffer is cleared after one use.
	assert.Equal(t, "", sess.GetScratchpad(), "scratchpad must be cleared after injection")

	// The next message is untouched — the scratchpad is one-shot.
	sess.prepareUserMessage("second prompt", nil)
	second := msgContentStr(sess.messages[len(sess.messages)-1])
	assert.Equal(t, "second prompt", second,
		"a cleared scratchpad must not leak into later messages")
}

func TestSession_PrepareUserMessage_EmptyScratchpadIsNoOp(t *testing.T) {
	t.Parallel()
	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system", "")
	require.NoError(t, err)

	sess.prepareUserMessage("plain prompt", nil)

	last := sess.messages[len(sess.messages)-1]
	assert.Equal(t, "plain prompt", msgContentStr(last),
		"an empty scratchpad must not alter the user message")
}

// TestSession_ScratchpadNeverTouchesSystemPrompt pins the cache-validity
// property: the scratchpad rides on the user message, never messages[0].
func TestSession_ScratchpadNeverTouchesSystemPrompt(t *testing.T) {
	t.Parallel()
	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "immutable system", "")
	require.NoError(t, err)

	sess.SetScratchpad("transient court history")
	sess.prepareUserMessage("work", nil)

	system := msgContentStr(sess.messages[0])
	assert.Equal(t, "immutable system", system,
		"the system prompt must never carry scratchpad content")
}
