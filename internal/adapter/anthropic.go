package adapter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// anthropicAdapter speaks Anthropic's native Messages API. Selected by
// the router for api.anthropic.com hosts and claude-* model prefixes.
//
// Mapping into the neutral Streamer surface:
//
//   - content_block_delta.text_delta       -> EventTokenDelta
//   - content_block_delta.thinking_delta   -> EventReasoning
//   - content_block_start (tool_use) +
//     accumulated input_json_delta         -> ToolCall in EventDone.Final
//
// System messages from the agent are lifted into the top-level `system`
// param (Anthropic does not carry system as a role on the messages
// array). Tool descriptors translate one-to-one — yottacode tools
// already advertise JSON-Schema input shapes, which is what
// input_schema expects.
type anthropicAdapter struct {
	client    anthropic.Client
	model     string
	maxTokens int64
	cfg       Config
	profile   ProviderProfile
}

// AnthropicDefaultMaxTokens is the upper bound on a single completion.
// Anthropic requires this — there is no "no limit" sentinel. 8192 is
// generous for code edits and within every claude-* model's per-turn
// cap; bigger documents land via tool_use round-trips, not single
// turns.
const AnthropicDefaultMaxTokens int64 = 8192

func newAnthropicAdapter(cfg Config) *anthropicAdapter {
	profile := buildProfile(cfg, false)
	opts := []option.RequestOption{
		// Snapshot rate-limit headers off every response so /usage can
		// show live per-minute token/request headroom.
		option.WithMiddleware(recordRateLimitMiddleware(profile.Provider)),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	c := anthropic.NewClient(opts...)
	return &anthropicAdapter{
		client:    c,
		model:     cfg.Model,
		maxTokens: AnthropicDefaultMaxTokens,
		cfg:       cfg,
		profile:   profile,
	}
}

func (a *anthropicAdapter) Profile() ProviderProfile { return a.profile }

// ChatStream streams a single assistant turn from Anthropic. Reasoning
// (extended thinking) tokens arrive as EventReasoning, body text as
// EventTokenDelta, and the final assembled Message — including any
// tool_use calls — lands in EventDone.
func (a *anthropicAdapter) ChatStream(ctx context.Context, messages []Message, tools []Tool) <-chan StreamEvent {
	out := make(chan StreamEvent, 16)
	go func() {
		defer close(out)

		system, msgs := splitForAnthropic(messages)
		applyAnthropicCacheControl(system, msgs)
		params := anthropic.MessageNewParams{
			Model:     anthropic.Model(a.model),
			MaxTokens: a.maxTokens,
			Messages:  msgs,
		}
		if len(system) > 0 {
			params.System = system
		}
		if anthropicTools := toAnthropicTools(tools, a.cfg, a.profile); len(anthropicTools) > 0 {
			params.Tools = anthropicTools
		}
		// Extended thinking is opt-in: enabled only when the user set an
		// effort level and the model is known to support it. budget is a
		// fraction of the model's max-output tokens (from the catalog);
		// max_tokens covers thinking + the visible answer together, so
		// raise it to the model's real cap to leave the answer room.
		if budget := anthropicThinkingBudget(a.cfg.ReasoningEffort, a.cfg.ModelMaxOutput, a.cfg.ModelSupportsThinking); budget > 0 {
			params.Thinking = anthropic.ThinkingConfigParamOfEnabled(budget)
			if int64(a.cfg.ModelMaxOutput) > params.MaxTokens {
				params.MaxTokens = int64(a.cfg.ModelMaxOutput)
			}
		}

		stream := a.client.Messages.NewStreaming(ctx, params)
		defer stream.Close()

		// Per-content-block state. Anthropic streams blocks one index
		// at a time: `content_block_start` carries metadata (type,
		// id, name for tool_use), then a series of deltas, then
		// `content_block_stop`. We accumulate text and tool input
		// JSON keyed on the integer index field.
		type pendingBlock struct {
			kind     string // "text" | "thinking" | "tool_use" | other
			toolID   string
			toolName string
			toolJSON strings.Builder
		}
		blocks := map[int64]*pendingBlock{}

		var content strings.Builder
		var toolCalls []ToolCall
		var stopReason string
		// usage carries the cumulative token counts as Anthropic
		// streams them: message_start populates the input + cache
		// fields (authoritative — they don't change over the turn),
		// message_delta carries the cumulative output_tokens. The
		// input/cache fields are NOT re-sent on every message_delta;
		// when absent the SDK surfaces them as 0, so they must not be
		// overwritten unconditionally here. Final snapshot goes onto
		// Message.Usage at EventDone.
		var usage *Usage

		for stream.Next() {
			evt := stream.Current()
			switch evt.Type {

			case "message_start":
				start := evt.AsMessageStart()
				u := start.Message.Usage
				usage = &Usage{
					InputTokens:         u.InputTokens,
					OutputTokens:        u.OutputTokens,
					CacheCreationTokens: u.CacheCreationInputTokens,
					CacheReadTokens:     u.CacheReadInputTokens,
				}

			case "content_block_start":
				start := evt.AsContentBlockStart()
				pb := &pendingBlock{kind: start.ContentBlock.Type}
				if start.ContentBlock.Type == "tool_use" {
					pb.toolID = start.ContentBlock.ID
					pb.toolName = start.ContentBlock.Name
				}
				blocks[start.Index] = pb

			case "content_block_delta":
				delta := evt.AsContentBlockDelta()
				pb := blocks[delta.Index]
				if pb == nil {
					continue
				}
				switch delta.Delta.Type {
				case "text_delta":
					text := delta.Delta.Text
					if text == "" {
						continue
					}
					content.WriteString(text)
					select {
					case <-ctx.Done():
						out <- StreamEvent{Kind: EventErr, Err: ctx.Err()}
						return
					case out <- StreamEvent{Kind: EventTokenDelta, Token: text}:
					}
				case "thinking_delta":
					text := delta.Delta.Thinking
					if text == "" {
						continue
					}
					select {
					case <-ctx.Done():
						out <- StreamEvent{Kind: EventErr, Err: ctx.Err()}
						return
					case out <- StreamEvent{Kind: EventReasoning, Token: text}:
					}
				case "input_json_delta":
					pb.toolJSON.WriteString(delta.Delta.PartialJSON)
					// Emit a heartbeat so the TUI's live tok/s
					// indicator keeps moving on tool-call-only
					// turns. Without it, a Claude turn that goes
					// straight to a tool call (no text, no
					// thinking) reads "0.0 tok/s" the entire
					// time the args stream — see the Responses
					// adapter for the matching fix.
					select {
					case <-ctx.Done():
						out <- StreamEvent{Kind: EventErr, Err: ctx.Err()}
						return
					case out <- StreamEvent{Kind: EventStreamProgress}:
					}
				}

			case "content_block_stop":
				stop := evt.AsContentBlockStop()
				pb := blocks[stop.Index]
				if pb == nil {
					continue
				}
				if pb.kind == "tool_use" {
					args := pb.toolJSON.String()
					if args == "" {
						// Anthropic emits tool_use blocks with empty
						// input as zero deltas. Normalize to "{}" so
						// downstream JSON parsers don't choke.
						args = "{}"
					}
					toolCalls = append(toolCalls, ToolCall{
						ID:       pb.toolID,
						Name:     pb.toolName,
						ArgsJSON: args,
					})
				}
				delete(blocks, stop.Index)

			case "message_delta":
				md := evt.AsMessageDelta()
				if md.Delta.StopReason != "" {
					stopReason = string(md.Delta.StopReason)
				}
				// message_delta authoritatively carries the cumulative
				// output_tokens — take it unconditionally. The
				// input/cache counts were captured at message_start and
				// are not re-sent on every message_delta; the SDK
				// surfaces the missing fields as 0, so overwriting
				// unconditionally would zero the real figures (and with
				// them every Anthropic turn's input-token cost and cache
				// accounting). Only adopt a delta's input/cache value
				// when it is actually populated (>0) — covering the rare
				// retried-turn case where the cache numbers genuinely
				// arrive late — otherwise keep message_start's values.
				if usage == nil {
					usage = &Usage{}
				}
				usage.OutputTokens = md.Usage.OutputTokens
				if md.Usage.InputTokens > 0 {
					usage.InputTokens = md.Usage.InputTokens
				}
				if md.Usage.CacheCreationInputTokens > 0 {
					usage.CacheCreationTokens = md.Usage.CacheCreationInputTokens
				}
				if md.Usage.CacheReadInputTokens > 0 {
					usage.CacheReadTokens = md.Usage.CacheReadInputTokens
				}

			case "message_stop":
				// terminal; loop will exit naturally
			}
		}

		if err := stream.Err(); err != nil {
			out <- StreamEvent{Kind: EventErr, Err: err}
			return
		}

		// A tool_use block is appended to toolCalls only on its
		// content_block_stop. Any tool_use still open in `blocks` means
		// the stream ended mid-call (a clean end with no content_block_stop
		// / message_stop). Emitting EventDone would silently drop that call
		// and commit an assistant turn missing a tool_use the user saw
		// streaming — a corrupted history. Surface an error instead.
		for _, pb := range blocks {
			if pb.kind == "tool_use" {
				out <- StreamEvent{Kind: EventErr, Err: fmt.Errorf("anthropic: stream ended with incomplete tool call %q — response truncated", pb.toolName)}
				return
			}
		}

		out <- StreamEvent{Kind: EventDone, Final: &Message{
			Role:       RoleAssistant,
			Content:    content.String(),
			ToolCalls:  toolCalls,
			StopReason: stopReason,
			Usage:      usage,
		}}
	}()
	return out
}

// splitForAnthropic separates system messages from the conversation
// stream. Anthropic carries the system prompt outside `messages` (as a
// list of TextBlockParams on the top-level System field); every other
// message becomes a MessageParam built from typed content blocks.
//
// Tool calls and tool results need block-level translation: assistant
// turns with tool_calls produce a tool_use block per call, and tool-
// role messages map to a user-role MessageParam carrying a tool_result
// block keyed on the call ID.
func splitForAnthropic(ms []Message) (system []anthropic.TextBlockParam, out []anthropic.MessageParam) {
	for _, m := range ms {
		switch m.Role {
		case RoleSystem:
			if m.Content == "" {
				continue
			}
			// When the composer marks a stable cache prefix
			// (CacheHeadBytes = the static base prompt, ahead of the
			// per-turn memory tail), emit the head as its own system
			// block with a cache breakpoint. The head + tools then
			// cache-hit across user turns even though the memory tail
			// churns; the tail block is covered by the end-of-system
			// breakpoint applyAnthropicCacheControl adds. Splitting the
			// text into two blocks is transparent to the model —
			// Anthropic concatenates system blocks.
			// utf8.RuneStart guards against a byte offset that lands
			// mid-rune: splitting there would leave each half with a
			// partial rune, which json.Marshal rewrites to U+FFFD —
			// silently corrupting the prompt. All current callers set
			// CacheHeadBytes to len(a complete prefix) so this never
			// trips; the guard just degrades to a single block if the
			// invariant is ever violated, rather than mangling text.
			if hb := m.CacheHeadBytes; hb > 0 && hb < len(m.Content) && utf8.RuneStart(m.Content[hb]) {
				head := anthropic.TextBlockParam{Text: m.Content[:hb]}
				head.CacheControl = anthropic.NewCacheControlEphemeralParam()
				system = append(system, head, anthropic.TextBlockParam{Text: m.Content[hb:]})
				continue
			}
			system = append(system, anthropic.TextBlockParam{Text: m.Content})

		case RoleUser:
			blocks := []anthropic.ContentBlockParamUnion{
				{OfText: &anthropic.TextBlockParam{Text: m.Content}},
			}
			for _, img := range m.Images {
				blocks = append(blocks, anthropic.ContentBlockParamUnion{
					OfImage: &anthropic.ImageBlockParam{
						Source: anthropic.ImageBlockParamSourceUnion{
							OfBase64: &anthropic.Base64ImageSourceParam{
								Data:      base64.StdEncoding.EncodeToString(img.Data),
								MediaType: anthropic.Base64ImageSourceMediaType(img.MediaType),
							},
						},
					},
				})
			}
			out = append(out, anthropic.NewUserMessage(blocks...))

		case RoleAssistant:
			var blocks []anthropic.ContentBlockParamUnion
			if m.Content != "" {
				blocks = append(blocks, anthropic.ContentBlockParamUnion{
					OfText: &anthropic.TextBlockParam{Text: m.Content},
				})
			}
			for _, tc := range m.ToolCalls {
				input := unmarshalToolArgs(tc.ArgsJSON)
				blocks = append(blocks, anthropic.ContentBlockParamUnion{
					OfToolUse: &anthropic.ToolUseBlockParam{
						ID:    tc.ID,
						Name:  tc.Name,
						Input: input,
					},
				})
			}
			if len(blocks) == 0 {
				continue
			}
			out = append(out, anthropic.NewAssistantMessage(blocks...))

		case RoleTool:
			content := []anthropic.ToolResultBlockParamContentUnion{
				{OfText: &anthropic.TextBlockParam{Text: m.Content}},
			}
			for _, img := range m.Images {
				content = append(content, anthropic.ToolResultBlockParamContentUnion{
					OfImage: &anthropic.ImageBlockParam{
						Source: anthropic.ImageBlockParamSourceUnion{
							OfBase64: &anthropic.Base64ImageSourceParam{
								Data:      base64.StdEncoding.EncodeToString(img.Data),
								MediaType: anthropic.Base64ImageSourceMediaType(img.MediaType),
							},
						},
					},
				})
			}
			result := anthropic.ContentBlockParamUnion{
				OfToolResult: &anthropic.ToolResultBlockParam{
					ToolUseID: m.ToolCallID,
					Content:   content,
				},
			}
			out = append(out, anthropic.NewUserMessage(result))
		}
	}
	return
}

// toAnthropicTools maps neutral Tool descriptors onto the SDK's tool
// union and appends provider-native server tools enabled by the
// resolved profile (today: web_search_20250305 only).
//
// yottacode advertises JSON-Schema-shaped Tool.Schema, which is
// exactly what input_schema expects — the only translation work is
// pulling out the top-level `properties` and `required` keys (the
// rest of the schema lives under ExtraFields so producer-side schemas
// with e.g. `oneOf` or `additionalProperties` survive intact).
func toAnthropicTools(tools []Tool, cfg Config, profile ProviderProfile) []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, 0, len(tools)+len(profile.EnabledBuiltinTools))
	for _, t := range tools {
		schema := anthropic.ToolInputSchemaParam{}
		if props, ok := t.Schema["properties"]; ok {
			schema.Properties = props
		}
		if req, ok := t.Schema["required"].([]string); ok {
			schema.Required = req
		} else if reqAny, ok := t.Schema["required"].([]any); ok {
			required := make([]string, 0, len(reqAny))
			for _, r := range reqAny {
				if s, ok := r.(string); ok {
					required = append(required, s)
				}
			}
			schema.Required = required
		}
		schema.ExtraFields = pickToolSchemaExtras(t.Schema)
		tool := anthropic.ToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: schema,
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &tool})
	}
	for _, kind := range profile.EnabledBuiltinTools {
		switch kind {
		case BuiltinToolWebSearch:
			out = append(out, buildAnthropicWebSearchTool(cfg))
		}
	}
	return out
}

// buildAnthropicWebSearchTool constructs the Anthropic-hosted
// web_search_20250305 tool param, mapping our generic search-domain
// config to the SDK's allowed_domains / blocked_domains fields. The
// SDK rejects setting both at once, but we already validate that in
// providerDiagnostics (and Resolve refuses to start when both are
// passed via flag).
func buildAnthropicWebSearchTool(cfg Config) anthropic.ToolUnionParam {
	t := anthropic.WebSearchTool20250305Param{}
	if len(cfg.SearchAllowedDomains) > 0 {
		t.AllowedDomains = cfg.SearchAllowedDomains
	} else if len(cfg.SearchExcludedDomains) > 0 {
		t.BlockedDomains = cfg.SearchExcludedDomains
	}
	return anthropic.ToolUnionParam{OfWebSearchTool20250305: &t}
}

// pickToolSchemaExtras passes through every schema field other than
// the ones we already mapped (properties, required, type). This keeps
// `additionalProperties: false`, `oneOf`, custom `description`s on the
// schema root, and so on intact when handed to the API.
func pickToolSchemaExtras(s map[string]any) map[string]any {
	if len(s) == 0 {
		return nil
	}
	skip := map[string]struct{}{
		"properties": {},
		"required":   {},
		"type":       {},
	}
	out := make(map[string]any, len(s))
	for k, v := range s {
		if _, drop := skip[k]; drop {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// unmarshalToolArgs decodes the agent's stored ArgsJSON back into the
// any-shape Anthropic wants on tool_use.Input. We accept a malformed
// or empty payload by passing through an empty object — the SDK will
// reject anything wrong on the next request and the user will see a
// clear API error, which is better than silently dropping the call.
func unmarshalToolArgs(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return map[string]any{}
	}
	if v == nil {
		return map[string]any{}
	}
	return v
}

// applyAnthropicCacheControl marks two Anthropic prompt-cache
// breakpoints on the outgoing request: one at the end of the system
// block and one at the end of the most recent message.
//
// Anthropic's cache prefix order is tools → system → messages. A
// breakpoint on a block caches that block AND every block before it,
// so:
//
//   - Marking the last system block caches the tools array + the
//     full system prompt. Hits on every subsequent turn within the
//     5-minute TTL.
//   - Marking the last message caches the entire conversation
//     history. Hits on the next turn after the assistant responds —
//     the agent loop's tool-call round-trips benefit directly.
//
// Cost model: Anthropic bills cache writes at 1.25× base, reads at
// 0.1×. Break-even is one hit per write; a multi-turn session
// reaches that on turn 2. yottacode uses 2 of the 4 available
// breakpoints — headroom reserved for future segmentation (e.g.
// splitting the system prompt into stable + retrieval-dynamic
// sections so the stable head still hits even when the dynamic tail
// changes per turn).
//
// No-op when there is neither a system block nor a message — defends
// against the otherwise-impossible empty request.
func applyAnthropicCacheControl(system []anthropic.TextBlockParam, msgs []anthropic.MessageParam) {
	if n := len(system); n > 0 {
		system[n-1].CacheControl = anthropic.NewCacheControlEphemeralParam()
	}
	if n := len(msgs); n > 0 {
		blocks := msgs[n-1].Content
		if b := len(blocks); b > 0 {
			if cc := blocks[b-1].GetCacheControl(); cc != nil {
				*cc = anthropic.NewCacheControlEphemeralParam()
			}
		}
	}
}

// AnthropicAdapterError makes "couldn't decode the SDK union into a
// content_block_start" surface as a real error rather than a silent
// drop. Currently unused — left here as a reminder if we add stricter
// runtime checks.
var _ = errors.New
