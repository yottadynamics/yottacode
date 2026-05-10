package adapter

import (
	"context"
	"encoding/json"
	"errors"

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
	profile ProviderProfile
}

// newChatAdapter builds a chatAdapter. apiKey is sent as Authorization:
// Bearer; Ollama and Llama Stack ignore it, but the SDK requires a
// non-empty value.
func newChatAdapter(cfg Config) *chatAdapter {
	apiKey := cfg.APIKey
	if apiKey == "" {
		apiKey = "local-no-auth"
	}
	c := openai.NewClient(
		option.WithBaseURL(cfg.BaseURL),
		option.WithAPIKey(apiKey),
	)
	return &chatAdapter{
		client:  c,
		model:   cfg.Model,
		profile: buildProfile(cfg, false),
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
			Model:    a.model,
			Messages: toOpenAIMessages(messages),
		}
		if len(tools) > 0 {
			params.Tools = toOpenAITools(tools)
		}

		stream := a.client.Chat.Completions.NewStreaming(ctx, params)
		defer stream.Close()

		acc := openai.ChatCompletionAccumulator{}
		var finishReason string
		for stream.Next() {
			chunk := stream.Current()
			acc.AddChunk(chunk)
			if len(chunk.Choices) == 0 {
				continue
			}
			if r := chunk.Choices[0].FinishReason; r != "" {
				finishReason = r
			}
			delta := chunk.Choices[0].Delta
			if reasoning := extractReasoning(delta); reasoning != "" {
				select {
				case <-ctx.Done():
					out <- StreamEvent{Kind: EventErr, Err: ctx.Err()}
					return
				case out <- StreamEvent{Kind: EventReasoning, Token: reasoning}:
				}
			}
			if delta.Content != "" {
				select {
				case <-ctx.Done():
					out <- StreamEvent{Kind: EventErr, Err: ctx.Err()}
					return
				case out <- StreamEvent{Kind: EventTokenDelta, Token: delta.Content}:
				}
			}
			// Tool-call argument deltas are accumulated invisibly by
			// the SDK's ChatCompletionAccumulator, so they don't
			// surface as Content/reasoning. Emit a heartbeat per
			// active tool-call delta so the TUI's tok/s indicator
			// keeps moving on tool-call-only turns. Without this, a
			// chat-completions provider (Ollama / vLLM / xAI / etc.)
			// that goes straight to a tool call reads "0.0 tok/s"
			// the entire time the args stream — same fix pattern as
			// the Responses and Anthropic adapters.
			for _, tc := range delta.ToolCalls {
				if tc.ID == "" && tc.Function.Name == "" && tc.Function.Arguments == "" {
					continue
				}
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
		if len(acc.Choices) == 0 {
			out <- StreamEvent{Kind: EventErr, Err: errors.New("adapter: empty completion")}
			return
		}
		final := fromOpenAIAssistant(acc.Choices[0].Message)
		final.StopReason = finishReason
		out <- StreamEvent{Kind: EventDone, Final: &final}
	}()
	return out
}

func toOpenAIMessages(ms []Message) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(ms))
	for _, m := range ms {
		switch m.Role {
		case RoleSystem:
			out = append(out, openai.SystemMessage(m.Content))
		case RoleUser:
			out = append(out, openai.UserMessage(m.Content))
		case RoleAssistant:
			asst := openai.ChatCompletionAssistantMessageParam{
				Content: openai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: openai.String(m.Content),
				},
			}
			if len(m.ToolCalls) > 0 {
				tcs := make([]openai.ChatCompletionMessageToolCallParam, 0, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					tcs = append(tcs, openai.ChatCompletionMessageToolCallParam{
						ID: tc.ID,
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      tc.Name,
							Arguments: tc.ArgsJSON,
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

func fromOpenAIAssistant(m openai.ChatCompletionMessage) Message {
	out := Message{
		Role:    RoleAssistant,
		Content: m.Content,
	}
	for _, tc := range m.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:       tc.ID,
			Name:     tc.Function.Name,
			ArgsJSON: tc.Function.Arguments,
		})
	}
	return out
}
