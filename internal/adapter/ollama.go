package adapter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
	"github.com/openai/openai-go/shared/constant"
)

// chatAdapter speaks to any OpenAI-compatible Chat Completions endpoint.
// Wires Ollama, Llama Stack, vLLM, xAI, OpenAI (gpt-4o / gpt-4.1 / etc.),
// Together, OpenRouter, and so on. For OpenAI's reasoning models (o-series,
// gpt-5*) on api.openai.com the router prefers the Responses API instead —
// see responses.go.
type chatAdapter struct {
	client  openai.Client
	model   string
	cfg     Config
	profile ProviderProfile
}

// ChatDefaultMaxTokens is the per-turn output cap sent on every Chat
// Completions request. Some OpenAI-compatible providers (NVIDIA NIM in
// particular) treat a missing max_tokens as zero and include that in
// the input+output ≤ window budget check — so a request with a full
// transcript and no max_tokens is rejected with 400 even when the
// input alone fits. Setting a real ceiling avoids that and reserves
// output headroom on every other provider too. 8192 matches the
// Anthropic adapter default — generous for code edits, within every
// vendor's per-turn cap, and not so large that a runaway response
// drains the wallet.
//
// Exported so callers that need to plan around the reserved output
// (e.g. the auto-summarize input budget) can subtract the same value
// the adapter actually sends.
const ChatDefaultMaxTokens int64 = 8192

// newChatAdapter builds a chatAdapter. apiKey is sent as Authorization:
// Bearer; Ollama and Llama Stack ignore it, but the SDK requires a
// non-empty value.
func newChatAdapter(cfg Config) *chatAdapter {
	apiKey := cfg.APIKey
	if apiKey == "" {
		apiKey = "local-no-auth"
	}
	profile := buildProfile(cfg, false)
	c := openai.NewClient(
		option.WithBaseURL(cfg.BaseURL),
		option.WithAPIKey(apiKey),
		// Snapshot rate-limit headers off every response so /usage can
		// show live per-minute token/request headroom. Local providers
		// (Ollama) don't emit these; recordRateLimit no-ops there.
		option.WithMiddleware(recordRateLimitMiddleware(profile.Provider)),
	)
	return &chatAdapter{
		client:  c,
		model:   cfg.Model,
		cfg:     cfg,
		profile: profile,
	}
}

func (a *chatAdapter) Model() string            { return a.model }
func (a *chatAdapter) Profile() ProviderProfile { return a.profile }

// ChatStream streams a single assistant turn. The returned channel emits
// TokenDelta events for every text chunk, then exactly one terminal Done or
// Err, then closes. Cancel ctx to abort; the goroutine unwinds cleanly.
func (a *chatAdapter) ChatStream(ctx context.Context, messages []Message, tools []Tool) <-chan StreamEvent {
	out := make(chan StreamEvent, 16)
	go func() {
		defer close(out)

		params := openai.ChatCompletionNewParams{
			Model:     a.model,
			Messages:  toOpenAIMessages(messages),
			MaxTokens: openai.Int(ChatDefaultMaxTokens),
		}
		// IncludeUsage gives us a final chunk with token counts
		// (choices empty, usage populated), but only request it from
		// providers where yottacode can surface meaningful usage. Some
		// free/local OpenAI-compatible endpoints — notably NVIDIA NIM's
		// hosted integrate.api.nvidia.com pool — have returned empty
		// completions when this optional flag is present, so omit it for
		// those endpoints instead of spending compatibility on data the UI
		// intentionally ignores.
		if a.profile.SupportsUsageReporting {
			params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
				IncludeUsage: openai.Bool(true),
			}
		}
		if len(tools) > 0 {
			params.Tools = toOpenAITools(tools)
		}
		// reasoning_effort is xAI-only on this chat-completions path: it's
		// honored by the grok-*-mini family (low/high) and rejected by
		// grok-4, so xaiEffort gates on both. Other OpenAI-compatible
		// endpoints (Ollama, vLLM, …) leave the field unset.
		if a.profile.Provider == ProviderXAI {
			if effort, ok := xaiEffort(a.cfg.ReasoningEffort, a.model); ok {
				params.ReasoningEffort = effort
			}
		}

		stream := a.client.Chat.Completions.NewStreaming(ctx, params)
		defer stream.Close()

		// We do all accumulation ourselves rather than via the SDK's
		// ChatCompletionAccumulator: openai-go v1.12.0 panics in
		// streamaccumulator.go:172 whenever a delta carries
		// `"tool_calls": []` (the JSON field is present but empty),
		// which NVIDIA NIM's reasoning models emit on some chunks.
		// Tracking content + tool-call args directly here sidesteps the
		// upstream bug without losing any behavior — we already walk
		// every chunk for streaming events anyway.
		var (
			content       strings.Builder
			finishReason  string
			toolCallOrder []int64
			toolCallByIdx = map[int64]*ToolCall{}
			sawChoice     bool
			usage         *Usage
		)
		for stream.Next() {
			chunk := stream.Current()
			// Usage rides in on the last chunk (or, for some
			// providers, sprinkled across all chunks with the same
			// cumulative totals). Read whenever the JSON field is
			// valid; the last assignment wins.
			if chunk.JSON.Usage.Valid() {
				cu := chunk.Usage
				cached := cu.PromptTokensDetails.CachedTokens
				input := cu.PromptTokens - cached
				if input < 0 {
					input = 0
				}
				usage = &Usage{
					InputTokens:     input,
					OutputTokens:    cu.CompletionTokens,
					CacheReadTokens: cached,
					ReasoningTokens: cu.CompletionTokensDetails.ReasoningTokens,
				}
			}
			if len(chunk.Choices) == 0 {
				continue
			}
			sawChoice = true
			choice := chunk.Choices[0]
			if r := choice.FinishReason; r != "" {
				finishReason = r
			}
			delta := choice.Delta
			if reasoning := extractReasoning(delta); reasoning != "" {
				select {
				case <-ctx.Done():
					out <- StreamEvent{Kind: EventErr, Err: ctx.Err()}
					return
				case out <- StreamEvent{Kind: EventReasoning, Token: reasoning}:
				}
			}
			if delta.Content != "" {
				content.WriteString(delta.Content)
				select {
				case <-ctx.Done():
					out <- StreamEvent{Kind: EventErr, Err: ctx.Err()}
					return
				case out <- StreamEvent{Kind: EventTokenDelta, Token: delta.Content}:
				}
			}
			// Tool-call argument deltas don't surface as
			// Content/reasoning, so accumulate them by index and
			// emit a heartbeat per non-empty delta. Without the
			// heartbeat a chat-completions provider that goes
			// straight to a tool call reads "0.0 tok/s" for the
			// whole turn — same fix pattern as the Responses and
			// Anthropic adapters.
			for _, tc := range delta.ToolCalls {
				if tc.ID == "" && tc.Function.Name == "" && tc.Function.Arguments == "" {
					continue
				}
				acc, ok := toolCallByIdx[tc.Index]
				if !ok {
					acc = &ToolCall{}
					toolCallByIdx[tc.Index] = acc
					toolCallOrder = append(toolCallOrder, tc.Index)
				}
				if tc.ID != "" {
					acc.ID = tc.ID
				}
				acc.Name += tc.Function.Name
				acc.ArgsJSON += tc.Function.Arguments
				select {
				case <-ctx.Done():
					out <- StreamEvent{Kind: EventErr, Err: ctx.Err()}
					return
				case out <- StreamEvent{Kind: EventStreamProgress}:
				}
			}
		}
		if err := stream.Err(); err != nil {
			out <- StreamEvent{Kind: EventErr, Err: err}
			return
		}
		if !sawChoice {
			out <- StreamEvent{Kind: EventErr, Err: errors.New("adapter: empty completion")}
			return
		}
		final := Message{
			Role:       RoleAssistant,
			Content:    content.String(),
			StopReason: finishReason,
			Usage:      usage,
		}
		for _, idx := range toolCallOrder {
			final.ToolCalls = append(final.ToolCalls, *toolCallByIdx[idx])
		}
		// A tool call that arrives with finish_reason "length"/"content_filter"
		// was cut off mid-emission: even when the partial arguments happen to
		// parse, they're incomplete. Reject rather than run a tool against a
		// truncated payload or replay it. Content-only truncation is fine — the
		// loop continues it — so this gates on a tool call being present.
		if len(final.ToolCalls) > 0 && (finishReason == "length" || finishReason == "content_filter") {
			out <- StreamEvent{Kind: EventErr, Err: fmt.Errorf("adapter: stream ended with an incomplete tool call (finish_reason=%q) — response truncated", finishReason)}
			return
		}
		if err := normalizeChatToolCalls(final.ToolCalls); err != nil {
			out <- StreamEvent{Kind: EventErr, Err: err}
			return
		}
		out <- StreamEvent{Kind: EventDone, Final: &final}
	}()
	return out
}

// normalizeChatToolCalls repairs and validates provider-emitted tool calls
// before they enter conversation history. An empty arguments payload is
// normalized to "{}" so no-argument tools (exit_plan_mode, git_push,
// worktree_status, …) and otherwise-recoverable calls survive: the model
// reaches the tool's own validation instead of a hard turn error, and the call
// serializes as valid JSON on replay. A missing function name, or a NON-EMPTY
// arguments payload that isn't a JSON object (a truncated stream), is still
// rejected — OpenAI-compatible providers replay assistant tool_call messages
// verbatim, so committing a truncated payload makes the NEXT request itself
// invalid JSON for providers such as NVIDIA NIM, which the model can no longer
// recover from.
func normalizeChatToolCalls(calls []ToolCall) error {
	return validateToolCallsForHistory(calls)
}

func toOpenAIMessages(ms []Message) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(ms))
	for _, m := range ms {
		switch m.Role {
		case RoleSystem:
			out = append(out, openai.SystemMessage(m.Content))
		case RoleUser:
			if len(m.Images) > 0 {
				parts := []openai.ChatCompletionContentPartUnionParam{
					openai.TextContentPart(m.Content),
				}
				for _, img := range m.Images {
					dataURL := fmt.Sprintf("data:%s;base64,%s", img.MediaType, base64.StdEncoding.EncodeToString(img.Data))
					parts = append(parts, openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{URL: dataURL}))
				}
				out = append(out, openai.UserMessage(parts))
			} else {
				out = append(out, openai.UserMessage(m.Content))
			}
		case RoleAssistant:
			asst := openai.ChatCompletionAssistantMessageParam{
				Content: openai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: openai.String(m.Content),
				},
			}
			if len(m.ToolCalls) > 0 {
				tcs := make([]openai.ChatCompletionMessageToolCallParam, 0, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					// Replay guard: a malformed historic tool_call should not poison the
					// next Chat Completions request. Providers expect arguments to be a
					// JSON object encoded as a string.
					args := safeToolArgsJSON(tc.ArgsJSON)
					tcs = append(tcs, openai.ChatCompletionMessageToolCallParam{
						ID: tc.ID,
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      tc.Name,
							Arguments: args,
						},
						Type: constant.ValueOf[constant.Function](),
					})
				}
				asst.ToolCalls = tcs
			}
			out = append(out, openai.ChatCompletionMessageParamUnion{OfAssistant: &asst})
		case RoleTool:
			out = append(out, openai.ToolMessage(m.Content, m.ToolCallID))
		}
	}
	return out
}

func toOpenAITools(tools []Tool) []openai.ChatCompletionToolParam {
	out := make([]openai.ChatCompletionToolParam, 0, len(tools))
	for _, t := range tools {
		out = append(out, openai.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        t.Name,
				Description: openai.String(t.Description),
				Parameters:  openai.FunctionParameters(t.Schema),
			},
			Type: constant.ValueOf[constant.Function](),
		})
	}
	return out
}

// extractReasoning pulls non-standard "thinking" tokens out of the SDK's
// ExtraFields bucket. Two field names cover the providers we care about:
//
//   - "reasoning" — Ollama's OpenAI shim for Qwen 3 / DeepSeek R1 / similar
//     thinking models hosted locally
//   - "reasoning_content" — xAI Grok (grok-4, grok-3-reasoning) extension
//     to the standard Chat Completions response shape
//
// Whichever is present (and non-null) is returned. Returns "" otherwise —
// vanilla GPT-4o, Claude-via-shim, etc. simply don't emit reasoning here
// and the caller treats it as a non-event.
func extractReasoning(delta openai.ChatCompletionChunkChoiceDelta) string {
	for _, key := range []string{"reasoning", "reasoning_content"} {
		f, ok := delta.JSON.ExtraFields[key]
		if !ok {
			continue
		}
		raw := f.Raw()
		if raw == "" || raw == "null" {
			continue
		}
		var s string
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			continue
		}
		if s != "" {
			return s
		}
	}
	return ""
}
