package adapter

// Role enumerates the speakers in a conversation.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is a model's request to invoke a tool. ArgsJSON is the raw JSON
// string the model produced; validation happens at execution time.
type ToolCall struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ArgsJSON string `json:"args_json"`
}

// Citation is a source reference attached to an assistant message by a
// provider-native search or retrieval capability.
type Citation struct {
	Type     string `json:"type,omitempty"`
	Title    string `json:"title,omitempty"`
	URL      string `json:"url,omitempty"`
	FileID   string `json:"file_id,omitempty"`
	Filename string `json:"filename,omitempty"`
}

// ImageBlock carries a single image as raw bytes plus its MIME type.
// Used in tool-result messages so the model can see screenshots, photos,
// diagrams, etc. alongside the textual output.
type ImageBlock struct {
	Data      []byte `json:"data"`
	MediaType string `json:"media_type"` // e.g. "image/png", "image/jpeg"
}

// Message is the neutral conversation unit the agent persists and replays.
// Tool-role messages carry ToolCallID to bind them to the call they answer.
type Message struct {
	Role       Role         `json:"role"`
	Content    string       `json:"content,omitempty"`
	Images     []ImageBlock `json:"images,omitempty"`
	ToolCalls  []ToolCall   `json:"tool_calls,omitempty"`
	Citations  []Citation   `json:"citations,omitempty"`
	StopReason string       `json:"stop_reason,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

// Tool is the schema the adapter advertises to the model. Schema must be a
// JSON-schema object describing the function's parameters.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
}

// StreamEventKind discriminates StreamEvent variants.
type StreamEventKind int

const (
	EventTokenDelta StreamEventKind = iota
	// EventReasoning is emitted for "thinking" tokens produced by reasoning
	// models (Qwen 3, DeepSeek R1, etc.). Render these subtly so the user can
	// see the model is working without mistaking them for the final answer.
	EventReasoning
	EventProviderTool
	EventDone
	EventErr
	// EventFallback is emitted by MultiStreamer when one candidate fails
	// before producing any visible output and the active policy elects to
	// retry on a different candidate. Surfaced loudly in the TUI so a
	// silent provider-degradation never hides behind the abstraction.
	EventFallback
	// EventStreamProgress is a heartbeat for stream activity that has no
	// visible text — used by adapters to surface in-flight tool-call
	// argument generation so the live "tok/s" indicator keeps moving on
	// turns where the model produces a function call without any
	// reasoning summary or text output (notably gpt-5* on the Responses
	// API). No payload: the consumer just increments its activity counter.
	EventStreamProgress
)

// StreamEvent is one item emitted while a completion streams.
//
// TokenDelta carries a chunk of assistant text to render live.
// Done carries the fully-accumulated assistant Message (with tool_calls, if any).
// Err carries a terminal error; no further events will follow.
// Fallback carries router metadata when MultiStreamer falls through from one
// candidate to another; the From/To/Reason/Policy fields are populated.
type StreamEvent struct {
	Kind               StreamEventKind
	Token              string
	ProviderToolName   string
	ProviderToolPhase  string
	ProviderToolDetail string
	Final              *Message
	Err                error
	// Fallback metadata — populated only when Kind == EventFallback.
	FallbackFrom   string
	FallbackTo     string
	FallbackReason string
	FallbackPolicy string
}
