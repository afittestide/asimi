package atif

// AtifRecorder is the interface for recording ATIF trajectory events.
// Implementations observe session lifecycle and write JSONL events.
type AtifRecorder interface {
	// Start opens the recording and writes session + agent_start events.
	Start()
	// Close flushes and closes all output files.
	Close()
	// TurnStarted records the start of a turn (one LLM request-response cycle).
	TurnStarted()
	// TurnEnded records the end of a turn with the final message and tool results.
	TurnEnded(msg RecorderMessage, toolResults []RecorderToolResult)
	// MessageStarted records when a message begins.
	MessageStarted(msg RecorderMessage)
	// MessageEnded records when a message completes.
	MessageEnded(msg RecorderMessage)
	// ToolExecutionStarted records when a tool call begins executing.
	ToolExecutionStarted(toolCallID, toolName string, args any)
	// ToolExecutionEnded records when a tool call completes.
	ToolExecutionEnded(toolCallID, toolName string, result string, isError bool)
	// ToolExecutionUpdated records partial/streaming tool output.
	ToolExecutionUpdated(toolCallID, toolName string, partialResult string, args any)
	// ModelChanged records a provider/model switch on the session. It lets
	// downstream consumers observe model selection changes mid-session.
	ModelChanged(provider, modelID string)
	// ThinkingLevelChanged records a change in the reasoning-effort level.
	ThinkingLevelChanged(level string)
}

// RecorderMessage carries message content and metadata for ATIF events.
// This is the in-memory representation used by the recorder interface;
// it maps to the ATIF JSON schema on serialization.
type RecorderMessage struct {
	Role          string
	Content       []RecorderContentBlock
	API           string
	Provider      string
	Model         string
	Usage         *RecorderUsage
	StopReason    string
	ToolCallID    string
	ToolName      string
	IsError       bool
	ResponseID    string
	RawStopReason string
	ErrorMessage  *string
}

// RecorderContentBlock represents a content block within a message.
type RecorderContentBlock struct {
	Type              string
	Text              *string
	Thinking          *string
	ThinkingSignature *string
	ToolCallID        string
	ToolName          string
	Arguments         any
	Content           []RecorderContentBlock
}

// RecorderUsage represents token usage breakdown.
type RecorderUsage struct {
	Input       int
	Output      int
	CacheRead   int
	CacheWrite  int
	Reasoning   int
	TotalTokens int
	Cost        *RecorderCost
}

// RecorderCost represents cost breakdown per component.
type RecorderCost struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
	Total      float64
}

// RecorderToolResult carries tool execution results for turn_end events.
type RecorderToolResult struct {
	Role       string
	ToolCallID string
	ToolName   string
	Content    []RecorderContentBlock
	IsError    bool
	Timestamp  int64
}
