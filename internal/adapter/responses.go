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
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
)

// responsesAdapter speaks to OpenAI's Responses API (`/v1/responses`). The
// router selects this adapter for OpenAI's reasoning models (o-series,
// gpt-5*) — Chat Completions hides their chain-of-thought, so this is the
// only path that exposes a streaming reasoning summary the TUI can show.
//
// For anything else (gpt-4o, vLLM, Ollama, xAI, etc.) the chatAdapter in
// ollama.go is used.
type responsesAdapter struct {
	client  openai.Client
	model   string
	cfg     Config
	profile ProviderProfile
}

// newResponsesAdapter builds an adapter wired to the Responses endpoint at
// baseURL. apiKey is sent as Authorization: Bearer.
func newResponsesAdapter(cfg Config) *responsesAdapter {
	apiKey := cfg.APIKey
	if apiKey == "" {
		apiKey = "local-no-auth"
	}
	profile := buildProfile(cfg, true)
	c := openai.NewClient(
		option.WithBaseURL(cfg.BaseURL),
		option.WithAPIKey(apiKey),
		// Snapshot rate-limit headers off every response so /usage can
		// show live per-minute token/request headroom.
		option.WithMiddleware(recordRateLimitMiddleware(profile.Provider)),
	)
	return &responsesAdapter{
		client:  c,
		model:   cfg.Model,
		cfg:     cfg,
		profile: profile,
	}
}

func (a *responsesAdapter) Profile() ProviderProfile { return a.profile }

// ChatStream streams a single assistant turn via the Responses API.
// Reasoning summaries arrive as EventReasoning, output text as
// EventTokenDelta, and the final assembled Message (with any function
// tool calls) lands in EventDone.
func (a *responsesAdapter) ChatStream(ctx context.Context, messages []Message, tools []Tool) <-chan StreamEvent {
	out := make(chan StreamEvent, 16)
	go func() {
		defer close(out)

		instructions, items := splitForResponses(messages)

		params := responses.ResponseNewParams{
			Model: shared.ResponsesModel(a.model),
			Input: responses.ResponseNewParamsInputUnion{OfInputItemList: items},
			// Opt into reasoning-summary streaming. Without this the
			// Responses API computes reasoning silently and only emits
			// the final output_text — the live thinking row would show
			// "Thinking… (0 tok/s)" the entire time the model reasons.
			// "auto" lets the API pick a summary verbosity appropriate
			// to the model.
			Reasoning: shared.ReasoningParam{Summary: shared.ReasoningSummaryAuto},
		}
		if effort, ok := parseReasoningEffort(a.cfg.ReasoningEffort); ok && isReasoningModel(a.model) {
			params.Reasoning.Effort = effort
		}
		if instructions != "" {
			params.Instructions = openai.String(instructions)
		}
		if responseTools := toResponsesTools(tools, a.cfg, a.profile); len(responseTools) > 0 {
			params.Tools = responseTools
		}

		stream := a.client.Responses.NewStreaming(ctx, params)
		defer stream.Close()

		// Per-item state for in-flight function calls. The server emits
		// `output_item.added` (carrying call_id + name), then a series of
		// `function_call_arguments.delta`, then `function_call_arguments.done`
		// with the authoritative final argument string. We key on item_id.
		type pendingCall struct {
			id   string // call_id (used by tool_result back-translation)
			name string
		}
		pending := map[string]*pendingCall{}

		var content strings.Builder
		var finalCalls []ToolCall
		var citations []Citation
		var messageStatus string
		var usage *Usage

		for stream.Next() {
			evt := stream.Current()
			switch evt.Type {

			case "response.output_text.delta":
				text := evt.Delta.OfString
				content.WriteString(text)
				select {
				case <-ctx.Done():
					out <- StreamEvent{Kind: EventErr, Err: ctx.Err()}
					return
				case out <- StreamEvent{Kind: EventTokenDelta, Token: text}:
				}

			case "response.reasoning_summary_text.delta":
				text := evt.Delta.OfString
				if text == "" {
					continue
				}
				select {
				case <-ctx.Done():
					out <- StreamEvent{Kind: EventErr, Err: ctx.Err()}
					return
				case out <- StreamEvent{Kind: EventReasoning, Token: text}:
				}

			case "response.output_item.added":
				if evt.Item.Type == "function_call" {
					pending[evt.Item.ID] = &pendingCall{
						id:   evt.Item.CallID,
						name: evt.Item.Name,
					}
					continue
				}
				if evt.Item.Type == "x_search_call" {
					out <- StreamEvent{
						Kind:              EventProviderTool,
						ProviderToolName:  "x_search",
						ProviderToolPhase: toolPhase(evt.Item.Status, "in_progress"),
					}
					continue
				}
			case "response.output_item.done":
				if evt.Item.Type == "message" {
					messageStatus = strings.TrimSpace(evt.Item.Status)
					citations = append(citations, citationsFromOutputItem(evt.Item)...)
					continue
				}
				if evt.Item.Type == "x_search_call" {
					out <- StreamEvent{
						Kind:              EventProviderTool,
						ProviderToolName:  "x_search",
						ProviderToolPhase: toolPhase(evt.Item.Status, "completed"),
					}
				}

			case "response.web_search_call.in_progress":
				out <- StreamEvent{Kind: EventProviderTool, ProviderToolName: "web_search", ProviderToolPhase: "in_progress"}
			case "response.web_search_call.searching":
				out <- StreamEvent{Kind: EventProviderTool, ProviderToolName: "web_search", ProviderToolPhase: "searching"}
			case "response.web_search_call.completed":
				out <- StreamEvent{Kind: EventProviderTool, ProviderToolName: "web_search", ProviderToolPhase: "completed"}

			case "response.code_interpreter_call.in_progress":
				out <- StreamEvent{Kind: EventProviderTool, ProviderToolName: "code_interpreter", ProviderToolPhase: "in_progress"}
			case "response.code_interpreter_call.interpreting":
				out <- StreamEvent{Kind: EventProviderTool, ProviderToolName: "code_interpreter", ProviderToolPhase: "interpreting"}
			case "response.code_interpreter_call_code.done":
				out <- StreamEvent{
					Kind:               EventProviderTool,
					ProviderToolName:   "code_interpreter",
					ProviderToolPhase:  "code",
					ProviderToolDetail: summarizeProviderToolDetail(evt.Code),
				}
			case "response.code_interpreter_call.completed":
				out <- StreamEvent{Kind: EventProviderTool, ProviderToolName: "code_interpreter", ProviderToolPhase: "completed"}

			case "response.function_call_arguments.delta":
				// The authoritative args string lands in .done — we don't
				// accumulate deltas. But surfacing each delta as a
				// progress heartbeat keeps the TUI's "tok/s" indicator
				// alive on turns that produce only a tool call with no
				// reasoning summary or text output (notably gpt-5* with
				// effort=minimal on the Responses API). Without this
				// the row reads "0.0 tok/s" until the call completes.
				select {
				case <-ctx.Done():
					out <- StreamEvent{Kind: EventErr, Err: ctx.Err()}
					return
				case out <- StreamEvent{Kind: EventStreamProgress}:
				}

			case "response.function_call_arguments.done":
				// The .done event carries the full arguments string; we
				// don't need to accumulate the .delta events.
				if pc := pending[evt.ItemID]; pc != nil {
					finalCalls = append(finalCalls, ToolCall{
						ID:       pc.id,
						Name:     pc.name,
						ArgsJSON: evt.Arguments,
					})
					delete(pending, evt.ItemID)
				}

			case "response.completed":
				// Final usage tally lands here, on the Response field.
				// Responses API reports cached_tokens as a subset of
				// input_tokens; surface them split so the cost
				// calculator can price cache hits separately.
				u := evt.Response.Usage
				cached := u.InputTokensDetails.CachedTokens
				input := u.InputTokens - cached
				if input < 0 {
					input = 0
				}
				usage = &Usage{
					InputTokens:     input,
					OutputTokens:    u.OutputTokens,
					CacheReadTokens: cached,
					ReasoningTokens: u.OutputTokensDetails.ReasoningTokens,
				}

			case "error":
				if evt.Message != "" {
					out <- StreamEvent{Kind: EventErr, Err: errors.New(evt.Message)}
					return
				}
			}
		}

		if err := stream.Err(); err != nil {
			out <- StreamEvent{Kind: EventErr, Err: err}
			return
		}

		final := Message{
			Role:      RoleAssistant,
			Content:   content.String(),
			ToolCalls: finalCalls,
			Citations: dedupeCitations(citations),
			StopReason: func() string {
				if messageStatus == "incomplete" {
					return "incomplete"
				}
				return ""
			}(),
			Usage: usage,
		}
		out <- StreamEvent{Kind: EventDone, Final: &final}
	}()
	return out
}

// splitForResponses lifts system messages out into the top-level
// Instructions field (the Responses API's preferred shape) and translates
// every other message into an input item. Tool results map to
// function_call_output items keyed by tool_call_id.
func splitForResponses(ms []Message) (instructions string, items []responses.ResponseInputItemUnionParam) {
	for _, m := range ms {
		switch m.Role {
		case RoleSystem:
			if instructions != "" {
				instructions += "\n\n"
			}
			instructions += m.Content
		case RoleUser:
			if len(m.Images) > 0 {
				parts := responses.ResponseInputMessageContentListParam{
					responses.ResponseInputContentParamOfInputText(m.Content),
				}
				for _, img := range m.Images {
					dataURL := fmt.Sprintf("data:%s;base64,%s", img.MediaType, base64.StdEncoding.EncodeToString(img.Data))
					parts = append(parts, responses.ResponseInputContentUnionParam{
						OfInputImage: &responses.ResponseInputImageParam{
							ImageURL: param.NewOpt(dataURL),
						},
					})
				}
				items = append(items, responses.ResponseInputItemParamOfMessage(parts, responses.EasyInputMessageRoleUser))
			} else {
				items = append(items, responses.ResponseInputItemParamOfMessage(m.Content, responses.EasyInputMessageRoleUser))
			}
		case RoleAssistant:
			if m.Content != "" {
				items = append(items, responses.ResponseInputItemParamOfMessage(m.Content, responses.EasyInputMessageRoleAssistant))
			}
			for _, tc := range m.ToolCalls {
				items = append(items, responses.ResponseInputItemParamOfFunctionCall(tc.ArgsJSON, tc.ID, tc.Name))
			}
		case RoleTool:
			items = append(items, responses.ResponseInputItemParamOfFunctionCallOutput(m.ToolCallID, m.Content))
		}
	}
	return
}

// toResponsesTools maps neutral Tool descriptors to the Responses API's
// FunctionToolParam (wrapped in the ToolUnionParam variant) plus any enabled
// provider-native tools supported by the resolved provider profile.
func toResponsesTools(tools []Tool, cfg Config, profile ProviderProfile) []responses.ToolUnionParam {
	out := make([]responses.ToolUnionParam, 0, len(tools)+len(profile.EnabledBuiltinTools))
	for _, t := range tools {
		ft := responses.FunctionToolParam{
			Name:        t.Name,
			Description: openai.String(t.Description),
			Parameters:  t.Schema,
			Strict:      openai.Bool(false),
		}
		out = append(out, responses.ToolUnionParam{OfFunction: &ft})
	}
	for _, tool := range profile.EnabledBuiltinTools {
		switch tool {
		case BuiltinToolWebSearch:
			out = append(out, buildWebSearchTool(cfg, profile.Provider))
		case BuiltinToolXSearch:
			out = append(out, buildXSearchTool(cfg))
		case BuiltinToolCodeInterpreter:
			out = append(out, responses.ToolParamOfCodeInterpreter("auto"))
		}
	}
	return out
}

func buildWebSearchTool(cfg Config, provider Provider) responses.ToolUnionParam {
	if provider == ProviderXAI {
		raw := map[string]any{"type": "web_search"}
		if filters := buildXAIWebSearchFilters(cfg); len(filters) > 0 {
			raw["filters"] = filters
		}
		return param.Override[responses.ToolUnionParam](json.RawMessage(mustJSON(raw)))
	}
	toolParam := responses.ToolParamOfWebSearchPreview(responses.WebSearchToolTypeWebSearchPreview2025_03_11)
	if ws := toolParam.OfWebSearchPreview; ws != nil {
		ws.UserLocation = responses.WebSearchToolUserLocationParam{
			Country:  param.NewOpt("US"),
			Timezone: param.NewOpt("America/New_York"),
		}
	}
	return toolParam
}

func buildXSearchTool(cfg Config) responses.ToolUnionParam {
	raw := map[string]any{"type": "x_search"}
	if len(cfg.XSearchAllowedHandles) > 0 {
		raw["allowed_x_handles"] = cfg.XSearchAllowedHandles
	} else if len(cfg.XSearchExcludedHandles) > 0 {
		raw["excluded_x_handles"] = cfg.XSearchExcludedHandles
	}
	if cfg.XSearchFromDate != "" {
		raw["from_date"] = cfg.XSearchFromDate
	}
	if cfg.XSearchToDate != "" {
		raw["to_date"] = cfg.XSearchToDate
	}
	return param.Override[responses.ToolUnionParam](json.RawMessage(mustJSON(raw)))
}

func buildXAIWebSearchFilters(cfg Config) map[string]any {
	filters := map[string]any{}
	if len(cfg.SearchAllowedDomains) > 0 {
		filters["allowed_domains"] = cfg.SearchAllowedDomains
	} else if len(cfg.SearchExcludedDomains) > 0 {
		filters["excluded_domains"] = cfg.SearchExcludedDomains
	}
	return filters
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func parseReasoningEffort(s string) (shared.ReasoningEffort, bool) {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case "low":
		return shared.ReasoningEffortLow, true
	case "medium":
		return shared.ReasoningEffortMedium, true
	case "high":
		return shared.ReasoningEffortHigh, true
	default:
		return "", false
	}
}

func summarizeProviderToolDetail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) > 80 {
		return s[:79] + "…"
	}
	return s
}

func toolPhase(status, fallback string) string {
	status = strings.TrimSpace(status)
	if status == "" {
		return fallback
	}
	return status
}

func citationsFromOutputItem(item responses.ResponseOutputItemUnion) []Citation {
	if item.Type != "message" {
		return nil
	}
	var out []Citation
	for _, part := range item.Content {
		if part.Type != "output_text" {
			continue
		}
		for _, ann := range part.Annotations {
			switch ann.Type {
			case "url_citation":
				c := ann.AsURLCitation()
				out = append(out, Citation{
					Type:  ann.Type,
					Title: c.Title,
					URL:   c.URL,
				})
			case "file_citation":
				c := ann.AsFileCitation()
				out = append(out, Citation{
					Type:     ann.Type,
					FileID:   c.FileID,
					Filename: c.Filename,
				})
			case "container_file_citation":
				c := ann.AsContainerFileCitation()
				out = append(out, Citation{
					Type:     ann.Type,
					FileID:   c.FileID,
					Filename: c.Filename,
				})
			}
		}
	}
	return out
}

func dedupeCitations(in []Citation) []Citation {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]Citation, 0, len(in))
	for _, c := range in {
		key := strings.Join([]string{c.Type, c.Title, c.URL, c.FileID, c.Filename}, "\x00")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, c)
	}
	return out
}
