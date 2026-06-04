package adapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// geminiAdapter speaks Google's Gemini Generative Language API directly
// over net/http — no vendor SDK. This is the template for the
// "thin-HTTP" migration path: every other adapter still wraps a vendor
// SDK and is scheduled to follow this shape once Gemini lands cleanly.
//
// Why no SDK:
//
//   - We translate vendor types into the neutral Streamer / Event /
//     Message / Tool surface anyway. Adding the Google SDK would
//     introduce a layer we'd immediately unwind.
//   - We use ~5% of any Gemini SDK's surface (streaming +
//     functionCall). The dependency cost (Google's genai pulls a fat
//     transitive graph) is not justified by the ergonomics savings.
//   - New API features arrive as JSON fields; with raw HTTP they're
//     immediately accessible, with an SDK we'd wait for a release.
//
// Mapping into the neutral Streamer surface:
//
//   - candidates[].content.parts[].text  (thought=false) → EventTokenDelta
//   - candidates[].content.parts[].text  (thought=true)  → EventReasoning
//   - candidates[].content.parts[].functionCall          → ToolCall in
//     EventDone.Final
//   - candidates[].finishReason                          → Final.StopReason
//
// Gemini does not stream tool-call arguments incrementally — function
// calls arrive as complete `functionCall` parts in a single SSE chunk
// (unlike Anthropic which streams input_json_delta fragments). That
// keeps the accumulator simple.
type geminiAdapter struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	model      string
	cfg        Config
	profile    ProviderProfile
}

const (
	geminiDefaultBaseURL = "https://generativelanguage.googleapis.com"
	// geminiAPIKeyHeader is the recommended way to pass the API key to
	// the Gemini REST API; the older ?key= query-string form is also
	// supported but pollutes URLs in proxy logs.
	geminiAPIKeyHeader = "x-goog-api-key"
)

func newGeminiAdapter(cfg Config) *geminiAdapter {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = geminiDefaultBaseURL
	}
	return &geminiAdapter{
		httpClient: http.DefaultClient,
		baseURL:    base,
		apiKey:     cfg.APIKey,
		model:      cfg.Model,
		cfg:        cfg,
		profile:    buildProfile(cfg, false),
	}
}

func (g *geminiAdapter) Profile() ProviderProfile { return g.profile }

// ChatStream streams one assistant turn from Gemini.
func (g *geminiAdapter) ChatStream(ctx context.Context, messages []Message, tools []Tool) <-chan StreamEvent {
	out := make(chan StreamEvent, 16)
	go func() {
		defer close(out)

		budget := geminiThinkingBudget(g.cfg.ReasoningEffort, g.cfg.ModelSupportsThinking)
		reqBody, err := json.Marshal(buildGeminiRequest(messages, tools, budget))
		if err != nil {
			out <- StreamEvent{Kind: EventErr, Err: fmt.Errorf("gemini: marshal request: %w", err)}
			return
		}

		url := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?alt=sse",
			g.baseURL, g.model)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
		if err != nil {
			out <- StreamEvent{Kind: EventErr, Err: fmt.Errorf("gemini: build request: %w", err)}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		if g.apiKey != "" {
			req.Header.Set(geminiAPIKeyHeader, g.apiKey)
		}

		resp, err := g.httpClient.Do(req)
		if err != nil {
			out <- StreamEvent{Kind: EventErr, Err: fmt.Errorf("gemini: do request: %w", err)}
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			out <- StreamEvent{Kind: EventErr, Err: fmt.Errorf("gemini: HTTP %d: %s",
				resp.StatusCode, strings.TrimSpace(string(body)))}
			return
		}

		var content strings.Builder
		var toolCalls []ToolCall
		var stopReason string
		// Gemini has no native tool-call IDs, so we synthesize them. Seed
		// the counter ABOVE any call_N already in the history: a plain
		// per-turn `call_0` recurs every turn and makes lookupToolName
		// (first-ID-match across all history) bind a later turn's function
		// response to an earlier turn's call. Seeding from the history keeps
		// IDs unique across the whole conversation — and is robust even if
		// the adapter is recreated mid-session (e.g. a /model switch).
		toolCallSeq := maxGeminiToolCallSeq(messages)
		var usage *Usage

		err = streamGeminiSSE(ctx, resp.Body, func(chunk geminiResponseChunk) error {
			if chunk.UsageMetadata != nil {
				um := chunk.UsageMetadata
				cached := um.CachedContentTokenCount
				input := um.PromptTokenCount - cached
				if input < 0 {
					input = 0
				}
				usage = &Usage{
					InputTokens:     input,
					OutputTokens:    um.CandidatesTokenCount,
					CacheReadTokens: cached,
					ReasoningTokens: um.ThoughtsTokenCount,
				}
			}
			for _, cand := range chunk.Candidates {
				if cand.FinishReason != "" {
					stopReason = cand.FinishReason
				}
				for _, part := range cand.Content.Parts {
					switch {
					case part.FunctionCall != nil:
						argsJSON := "{}"
						if len(part.FunctionCall.Args) > 0 {
							b, mErr := json.Marshal(part.FunctionCall.Args)
							if mErr == nil {
								argsJSON = string(b)
							}
						}
						toolCallSeq++
						toolCalls = append(toolCalls, ToolCall{
							ID:       fmt.Sprintf("call_%d", toolCallSeq),
							Name:     part.FunctionCall.Name,
							ArgsJSON: argsJSON,
						})
					case part.Text != "":
						kind := EventTokenDelta
						if part.Thought {
							kind = EventReasoning
						} else {
							content.WriteString(part.Text)
						}
						select {
						case <-ctx.Done():
							return ctx.Err()
						case out <- StreamEvent{Kind: kind, Token: part.Text}:
						}
					}
				}
			}
			return nil
		})

		if err != nil && !errors.Is(err, io.EOF) {
			out <- StreamEvent{Kind: EventErr, Err: err}
			return
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

// --- request shape --------------------------------------------------

type geminiRequest struct {
	Contents          []geminiContent         `json:"contents"`
	SystemInstruction *geminiContent          `json:"systemInstruction,omitempty"`
	Tools             []geminiTool            `json:"tools,omitempty"`
	GenerationConfig  *geminiGenerationConfig `json:"generationConfig,omitempty"`
}

// geminiGenerationConfig carries optional generation knobs. Only
// thinkingConfig is populated today (when an effort level is set);
// omitempty keeps the field — and the whole object — out of the wire
// shape entirely when reasoning is left at the provider default.
type geminiGenerationConfig struct {
	ThinkingConfig *geminiThinkingConfig `json:"thinkingConfig,omitempty"`
}

// geminiThinkingConfig enables Gemini 2.5 "thinking". includeThoughts
// surfaces the thinking trace as thought=true parts (rendered as
// EventReasoning); thinkingBudget caps the thinking tokens.
type geminiThinkingConfig struct {
	IncludeThoughts bool  `json:"includeThoughts"`
	ThinkingBudget  int64 `json:"thinkingBudget"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"` // "user" or "model"
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	InlineData       *geminiInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type geminiFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDecl `json:"functionDeclarations"`
}

type geminiFunctionDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// --- response shape (also reuses geminiContent) ---------------------

type geminiResponseChunk struct {
	Candidates    []geminiCandidate    `json:"candidates"`
	UsageMetadata *geminiUsageMetadata `json:"usageMetadata,omitempty"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason,omitempty"`
}

// geminiUsageMetadata mirrors the Gemini v1beta usage shape. The
// streaming SSE may emit multiple chunks; the last one carries the
// final tally. promptTokenCount is the full input (incl. cached);
// cachedContentTokenCount is a subset, so subtract for non-cached.
type geminiUsageMetadata struct {
	PromptTokenCount        int64 `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount,omitempty"`
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount,omitempty"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount,omitempty"`
	TotalTokenCount         int64 `json:"totalTokenCount,omitempty"`
}

// --- conversion helpers ---------------------------------------------

// buildGeminiRequest converts the neutral []Message + []Tool surface
// into Gemini's wire shape. System messages lift to systemInstruction;
// assistant turns become role="model"; tool-result messages become
// role="user" with a functionResponse part.
func buildGeminiRequest(messages []Message, tools []Tool, thinkingBudget int64) geminiRequest {
	req := geminiRequest{}
	if thinkingBudget > 0 {
		req.GenerationConfig = &geminiGenerationConfig{
			ThinkingConfig: &geminiThinkingConfig{
				IncludeThoughts: true,
				ThinkingBudget:  thinkingBudget,
			},
		}
	}
	var sysParts []geminiPart
	for _, m := range messages {
		switch m.Role {
		case RoleSystem:
			if m.Content == "" {
				continue
			}
			sysParts = append(sysParts, geminiPart{Text: m.Content})
		case RoleUser:
			parts := []geminiPart{{Text: m.Content}}
			for _, img := range m.Images {
				parts = append(parts, geminiPart{
					InlineData: &geminiInlineData{
						MimeType: img.MediaType,
						Data:     base64.StdEncoding.EncodeToString(img.Data),
					},
				})
			}
			req.Contents = append(req.Contents, geminiContent{
				Role:  "user",
				Parts: parts,
			})
		case RoleAssistant:
			var parts []geminiPart
			if m.Content != "" {
				parts = append(parts, geminiPart{Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				parts = append(parts, geminiPart{
					FunctionCall: &geminiFunctionCall{
						Name: tc.Name,
						Args: unmarshalGeminiArgs(tc.ArgsJSON),
					},
				})
			}
			if len(parts) == 0 {
				continue
			}
			req.Contents = append(req.Contents, geminiContent{
				Role:  "model",
				Parts: parts,
			})
		case RoleTool:
			name := lookupToolName(messages, m.ToolCallID)
			req.Contents = append(req.Contents, geminiContent{
				Role: "user",
				Parts: []geminiPart{{
					FunctionResponse: &geminiFunctionResponse{
						Name:     name,
						Response: map[string]any{"result": m.Content},
					},
				}},
			})
		}
	}
	if len(sysParts) > 0 {
		req.SystemInstruction = &geminiContent{Parts: sysParts}
	}
	if len(tools) > 0 {
		decls := make([]geminiFunctionDecl, 0, len(tools))
		for _, t := range tools {
			decls = append(decls, geminiFunctionDecl{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Schema,
			})
		}
		req.Tools = []geminiTool{{FunctionDeclarations: decls}}
	}
	return req
}

// lookupToolName walks the message history to find the assistant turn
// whose ToolCalls contain the given ID, returning the tool's name.
// Gemini's functionResponse part requires the name in addition to the
// implicit pairing the OpenAI/Anthropic shapes carry through tool-call
// IDs alone. Returns "" if no match — Gemini will reject the request
// with a clear error in that case, which is better than silently
// dropping a tool result.
// maxGeminiToolCallSeq returns the highest N among the synthesized
// `call_N` IDs already present in the conversation, or 0 when there are
// none. ChatStream seeds its per-turn ID counter from this so newly
// synthesized IDs never collide with ones still referenced in history.
func maxGeminiToolCallSeq(messages []Message) int {
	max := 0
	for _, m := range messages {
		for _, tc := range m.ToolCalls {
			if n, ok := parseGeminiCallSeq(tc.ID); ok && n > max {
				max = n
			}
		}
	}
	return max
}

func parseGeminiCallSeq(id string) (int, bool) {
	const prefix = "call_"
	if !strings.HasPrefix(id, prefix) {
		return 0, false
	}
	n, err := strconv.Atoi(id[len(prefix):])
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func lookupToolName(messages []Message, callID string) string {
	for _, m := range messages {
		if m.Role != RoleAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.ID == callID {
				return tc.Name
			}
		}
	}
	return ""
}

// unmarshalGeminiArgs decodes a stored ArgsJSON string into the
// map-shape Gemini expects. Empty or malformed input becomes an empty
// map — safer than dropping the call, since Gemini will reject obvious
// schema violations on the next turn with a real error.
func unmarshalGeminiArgs(s string) map[string]any {
	s = strings.TrimSpace(s)
	if s == "" {
		return map[string]any{}
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil || v == nil {
		return map[string]any{}
	}
	return v
}

// --- SSE parsing ----------------------------------------------------

// streamGeminiSSE drives a Gemini SSE response body, decoding each
// `data: <json>` frame into a geminiResponseChunk and invoking onChunk.
// Returns context errors directly; ignores empty/keepalive lines and
// the optional `[DONE]` terminator (Gemini does not emit one in
// practice, but tolerating it makes the parser robust to proxies that
// add one).
func streamGeminiSSE(ctx context.Context, r io.Reader, onChunk func(geminiResponseChunk) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk geminiResponseChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return fmt.Errorf("gemini: parse SSE frame: %w", err)
		}
		if err := onChunk(chunk); err != nil {
			return err
		}
	}
	return scanner.Err()
}
