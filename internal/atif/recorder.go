package atif

import (
	"fmt"
	"os"
	"time"
)

// TrajectoryRecorder implements AtifRecorder, writing ATIF events as JSONL
// to aggregated and per-session files.
type TrajectoryRecorder struct {
	writer    *AtifWriter
	sessionID string
	agentName string
	eventID   int
	turnOpen  bool
}

// NewTrajectoryRecorder creates a new TrajectoryRecorder for the given agent name.
// The agent/ directory is created on Start(), not here.
func NewTrajectoryRecorder(agentName, sessionID string) *TrajectoryRecorder {
	return &TrajectoryRecorder{
		writer:    NewAtifWriter(agentName, sessionID),
		sessionID: sessionID,
		agentName: agentName,
	}
}

// Start opens the writer and writes the session + agent_start events.
func (r *TrajectoryRecorder) Start() {
	if r == nil {
		return
	}
	r.writer.Open()
	if !r.writer.IsOpen() {
		return
	}

	cwd, _ := os.Getwd()

	// Session event
	r.writer.WriteEvent(SessionEvent{
		Type:      TypeSession,
		Version:   Version,
		ID:        r.sessionID,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Cwd:       cwd,
	})

	// Agent start event
	r.writer.WriteEvent(AgentStartEvent{
		Type: TypeAgentStart,
	})
}

// Close flushes and closes the writer.
func (r *TrajectoryRecorder) Close() {
	if r == nil {
		return
	}
	r.writer.Close()
}

// nextEventID generates a short unique event ID.
func (r *TrajectoryRecorder) nextEventID() string {
	r.eventID++
	return fmt.Sprintf("%s-%d", r.sessionID[:8], r.eventID)
}

// ts returns current unix timestamp in milliseconds.
func ts() int64 {
	return time.Now().UnixMilli()
}

// --- AtifRecorder implementation ---

// TurnStarted writes a turn_start event.
func (r *TrajectoryRecorder) TurnStarted() {
	if r == nil || !r.writer.IsOpen() {
		return
	}
	r.turnOpen = true
	r.writer.WriteEvent(TurnStartEvent{
		Type: TypeTurnStart,
	})
}

// TurnEnded writes a turn_end event with the final message and all tool results.
func (r *TrajectoryRecorder) TurnEnded(msg RecorderMessage, toolResults []RecorderToolResult) {
	if r == nil || !r.writer.IsOpen() || !r.turnOpen {
		return
	}
	r.turnOpen = false
	r.writer.WriteEvent(TurnEndEvent{
		Type:        TypeTurnEnd,
		Message:     recorderMsgToSchema(msg),
		ToolResults: recorderToolResultsToSchema(toolResults),
	})
}

// MessageStarted writes a message_start event.
func (r *TrajectoryRecorder) MessageStarted(msg RecorderMessage) {
	if r == nil || !r.writer.IsOpen() {
		return
	}
	r.writer.WriteEvent(MessageStartEvent{
		Type:      TypeMessageStart,
		Message:   recorderMsgToSchema(msg),
		Timestamp: ts(),
	})
}

// MessageEnded writes a message_end event.
func (r *TrajectoryRecorder) MessageEnded(msg RecorderMessage) {
	if r == nil || !r.writer.IsOpen() {
		return
	}
	r.writer.WriteEvent(MessageEndEvent{
		Type:      TypeMessageEnd,
		Message:   recorderMsgToSchema(msg),
		Timestamp: ts(),
	})
}

// ToolExecutionStarted writes a tool_execution_start event.
func (r *TrajectoryRecorder) ToolExecutionStarted(toolCallID, toolName string, args any) {
	if r == nil || !r.writer.IsOpen() {
		return
	}
	r.writer.WriteEvent(ToolExecutionStartEvent{
		Type:       TypeToolExecutionStart,
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Args:       args,
	})
}

// ToolExecutionEnded writes a tool_execution_end event.
func (r *TrajectoryRecorder) ToolExecutionEnded(toolCallID, toolName string, result string, isError bool) {
	if r == nil || !r.writer.IsOpen() {
		return
	}
	r.writer.WriteEvent(ToolExecutionEndEvent{
		Type:       TypeToolExecutionEnd,
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Result: ResultData{
			Content: []SchemaContentBlock{
				{Type: "text", Text: result},
			},
		},
		IsError: isError,
	})
}

// ToolExecutionUpdated writes a tool_execution_update event.
func (r *TrajectoryRecorder) ToolExecutionUpdated(toolCallID, toolName string, partialResult string, args any) {
	if r == nil || !r.writer.IsOpen() {
		return
	}
	r.writer.WriteEvent(ToolExecutionUpdateEvent{
		Type:       TypeToolExecutionUpdate,
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Args:       args,
		PartialResult: PartialResultData{
			Content: []SchemaContentBlock{
				{Type: "text", Text: partialResult},
			},
		},
	})
}

// ModelChanged writes a model_change event.
func (r *TrajectoryRecorder) ModelChanged(provider, modelID string) {
	if r == nil || !r.writer.IsOpen() {
		return
	}
	r.writer.WriteEvent(ModelChangeEvent{
		Type:      TypeModelChange,
		ID:        r.nextEventID(),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Provider:  provider,
		ModelID:   modelID,
	})
}

// ThinkingLevelChanged writes a thinking_level_change event.
func (r *TrajectoryRecorder) ThinkingLevelChanged(level string) {
	if r == nil || !r.writer.IsOpen() {
		return
	}
	r.writer.WriteEvent(ThinkingLevelChangeEvent{
		Type:          TypeThinkingLevelChange,
		ID:            r.nextEventID(),
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
		ThinkingLevel: level,
	})
}

// --- Conversion helpers: types.go -> schema.go ---

func recorderMsgToSchema(m RecorderMessage) SchemaMessageData {
	content := make([]SchemaContentBlock, 0, len(m.Content))
	for _, c := range m.Content {
		block := SchemaContentBlock{Type: c.Type}
		switch c.Type {
		case "text":
			if c.Text != nil {
				block.Text = *c.Text
			}
		case "thinking":
			if c.Thinking != nil {
				block.Thinking = *c.Thinking
			}
			if c.ThinkingSignature != nil {
				block.ThinkingSignature = *c.ThinkingSignature
			}
		case "toolCall":
			block.ID = c.ToolCallID
			block.Name = c.ToolName
			block.Arguments = c.Arguments
		case "toolResult":
			block.ToolCallID = c.ToolCallID
			block.ToolName = c.ToolName
		}
		content = append(content, block)
	}

	msg := SchemaMessageData{
		Role:          m.Role,
		Content:       content,
		API:           m.API,
		Provider:      m.Provider,
		Model:         m.Model,
		StopReason:    m.StopReason,
		ToolCallID:    m.ToolCallID,
		ToolName:      m.ToolName,
		IsError:       m.IsError,
		ResponseID:    m.ResponseID,
		RawStopReason: m.RawStopReason,
		ErrorMessage:  m.ErrorMessage,
	}

	if m.Usage != nil {
		msg.Usage = &SchemaUsage{
			Input:       m.Usage.Input,
			Output:      m.Usage.Output,
			CacheRead:   m.Usage.CacheRead,
			CacheWrite:  m.Usage.CacheWrite,
			Reasoning:   m.Usage.Reasoning,
			TotalTokens: m.Usage.TotalTokens,
		}
		if m.Usage.Cost != nil {
			msg.Usage.Cost = &SchemaCost{
				Input:      m.Usage.Cost.Input,
				Output:     m.Usage.Cost.Output,
				CacheRead:  m.Usage.Cost.CacheRead,
				CacheWrite: m.Usage.Cost.CacheWrite,
				Total:      m.Usage.Cost.Total,
			}
		}
	}

	return msg
}

func recorderToolResultsToSchema(trs []RecorderToolResult) []ToolResultSchemaData {
	if trs == nil {
		return []ToolResultSchemaData{}
	}
	result := make([]ToolResultSchemaData, 0, len(trs))
	for _, tr := range trs {
		content := make([]SchemaContentBlock, 0, len(tr.Content))
		for _, c := range tr.Content {
			text := ""
			if c.Text != nil {
				text = *c.Text
			}
			content = append(content, SchemaContentBlock{
				Type: c.Type,
				Text: text,
			})
		}
		result = append(result, ToolResultSchemaData{
			Role:       tr.Role,
			ToolCallID: tr.ToolCallID,
			ToolName:   tr.ToolName,
			Content:    content,
			IsError:    tr.IsError,
			Timestamp:  tr.Timestamp,
		})
	}
	return result
}
