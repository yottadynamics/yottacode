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
	// Usage is the provider-reported token counts for the turn that
	// produced this message. Pointer so nil ≠ "zero tokens" — adapters
	// that didn't observe usage data leave it unset, and the /usage
	// command can tell the difference.
	Usage *Usage `json:"usage,omitempty"`
}

// Usage is the per-turn token breakdown reported by the provider.
// Fields are normalized across providers: cache fields are populated
// only by providers that expose prompt caching (Anthropic, OpenAI's
// cached_input); ReasoningTokens is populated by o-series and Gemini
// "thoughts". All fields are int64 to match the JSON wire types and
// to keep arithmetic on session totals overflow-safe.
type Usage struct {
	InputTokens         int64 `json:"input_tokens,omitempty"`
	OutputTokens        int64 `json:"output_tokens,omitempty"`
	CacheCreationTokens int64 `json:"cache_creation_tokens,omitempty"`
	CacheReadTokens     int64 `json:"cache_read_tokens,omitempty"`
	ReasoningTokens     int64 `json:"reasoning_tokens,omitempty"`
}

// Add accumulates other into u. Used by the session accumulator and
// the daily rollup. Treats nil receivers/args as zero so callers can
// chain without nil-guarding.
func (u *Usage) Add(other *Usage) {
	if u == nil || other == nil {
		return
	}
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.CacheCreationTokens += other.CacheCreationTokens
	u.CacheReadTokens += other.CacheReadTokens
	u.ReasoningTokens += other.ReasoningTokens
}

// IsZero reports whether all token counts are zero. A non-nil zero
// Usage is the "we tried but the provider returned no data" signal —
// distinct from nil ("we didn't try / never reached the parsing
// path"). The /usage renderer uses this to format empty sessions.
func (u *Usage) IsZero() bool {
	if u == nil {
		return true
	}
	return u.InputTokens == 0 && u.OutputTokens == 0 &&
		u.CacheCreationTokens == 0 && u.CacheReadTokens == 0 &&
		u.ReasoningTokens == 0
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
