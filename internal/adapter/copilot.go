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
	"strings"
	"time"

	copilotauth "github.com/yottadynamics/yottacode/internal/auth/copilot"
)

const (
	copilotLoginHint = "run `yottacode copilot-auth login`"
)

// copilotAdapter speaks to the GitHub Copilot Chat Completions endpoint
// at api.githubcopilot.com. Auth comes from a two-layer token system:
// a long-lived GitHub OAuth token exchanges for a short-lived Copilot
// API token via the TokenSource.
type copilotAdapter struct {
	tokens   *copilotauth.TokenSource
	client   *http.Client
	model    string
	cfg      Config
	profile  ProviderProfile
}

func newCopilotAdapter(cfg Config) Client {
	storePath, err := copilotauth.DefaultStorePath()
	if err != nil {
		return newErroredAdapter(cfg, ProviderCopilot,
			fmt.Errorf("copilot: resolve token store path: %w", err))
	}
	_, err = copilotauth.Load(storePath)
	if err != nil {
		return newErroredAdapter(cfg, ProviderCopilot,
			fmt.Errorf("copilot: not logged in — %s", copilotLoginHint))
	}
	return newCopilotAdapterFor(cfg, copilotauth.NewTokenSource(storePath))
}

func newCopilotAdapterFor(cfg Config, src *copilotauth.TokenSource) Client {
	return &copilotAdapter{
		tokens:  src,
		client:  &http.Client{},
		model:   cfg.Model,
		cfg:     cfg,
		profile: buildCopilotProfile(cfg),
	}
}

func buildCopilotProfile(cfg Config) ProviderProfile {
	return buildProfile(cfg, false)
}

func (a *copilotAdapter) Profile() ProviderProfile { return a.profile }

func (a *copilotAdapter) ChatStream(ctx context.Context, messages []Message, tools []Tool) <-chan StreamEvent {
	out := make(chan StreamEvent, 16)
	go func() {
		defer close(out)
		a.runOnce(ctx, messages, tools, out)
	}()
	return out
}

func (a *copilotAdapter) runOnce(ctx context.Context, messages []Message, tools []Tool, out chan<- StreamEvent) {
	body, err := buildCopilotRequest(a.model, messages, tools)
	if err != nil {
		out <- StreamEvent{Kind: EventErr, Err: fmt.Errorf("copilot: build request: %w", err)}
		return
	}
	resp, err := a.sendWithRetry(ctx, body)
	if err != nil {
		out <- StreamEvent{Kind: EventErr, Err: err}
		return
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		if err := a.tokens.ForceRefresh(ctx); err != nil {
			out <- StreamEvent{Kind: EventErr,
				Err: fmt.Errorf("copilot: session expired and refresh failed (%w) — %s", err, copilotLoginHint)}
			return
		}
		resp, err = a.sendWithRetry(ctx, body)
		if err != nil {
			out <- StreamEvent{Kind: EventErr, Err: err}
			return
		}
		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			out <- StreamEvent{Kind: EventErr,
				Err: errors.New("copilot: session expired — " + copilotLoginHint)}
			return
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := errorDetailFromBytes(raw)
		if resp.StatusCode == http.StatusTooManyRequests {
			if hint := formatRateLimitHint(resp.Header, raw); hint != "" {
				out <- StreamEvent{Kind: EventErr,
					Err: fmt.Errorf("copilot: %s\n%s", msg, hint)}
				return
			}
		}
		out <- StreamEvent{Kind: EventErr,
			Err: fmt.Errorf("copilot: HTTP %d: %s", resp.StatusCode, msg)}
		return
	}
	origNames := make([]string, len(tools))
	for i, t := range tools {
		origNames[i] = t.Name
	}
	a.consumeSSE(ctx, resp.Body, origNames, out)
}

var copilot5xxBackoffs = []time.Duration{
	500 * time.Millisecond,
	1500 * time.Millisecond,
}

func (a *copilotAdapter) sendWithRetry(ctx context.Context, body []byte) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= len(copilot5xxBackoffs); attempt++ {
		resp, err := a.send(ctx, body)
		if err != nil {
			lastErr = err
			if attempt == len(copilot5xxBackoffs) {
				return nil, err
			}
			if waitErr := sleepCtx(ctx, copilot5xxBackoffs[attempt]); waitErr != nil {
				return nil, waitErr
			}
			continue
		}
		if resp.StatusCode < 500 {
			return resp, nil
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		detail := errorDetailFromBytes(raw)
		if attempt == len(copilot5xxBackoffs) {
			return nil, fmt.Errorf("copilot: HTTP %d after %d attempts: %s",
				resp.StatusCode, attempt+1, detail)
		}
		lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, detail)
		if waitErr := sleepCtx(ctx, copilot5xxBackoffs[attempt]); waitErr != nil {
			return nil, waitErr
		}
	}
	return nil, lastErr
}

func (a *copilotAdapter) send(ctx context.Context, body []byte) (*http.Response, error) {
	token, err := a.tokens.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("copilot: %w", err)
	}
	endpoint := a.tokens.APIEndpoint() + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Editor-Version", "vscode/1.95.0")
	req.Header.Set("Editor-Plugin-Version", "copilot/1.250.0")
	req.Header.Set("Copilot-Integration-Id", "vscode-chat")
	req.Header.Set("User-Agent", "GitHubCopilotChat/0.26.0")
	return a.client.Do(req)
}

// copilotSanitizeName replaces characters not in [a-zA-Z0-9_-] with
// underscores. The Copilot API enforces ^[a-zA-Z0-9_-]{1,128}$ on
// tool names; MCP tools use slashes (mcp/server/tool) which must be
// mapped. The reverse mapping in copilotUnsanitizeName restores
// slashes so the agent loop resolves the right tool.
func copilotSanitizeName(name string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, name)
}

func copilotUnsanitizeName(sanitized string, origNames []string) string {
	for _, orig := range origNames {
		if copilotSanitizeName(orig) == sanitized {
			return orig
		}
	}
	return sanitized
}

func buildCopilotRequest(model string, messages []Message, tools []Tool) ([]byte, error) {
	chatMessages := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		var content any = m.Content
		if m.Role == RoleUser && len(m.Images) > 0 {
			parts := []any{
				map[string]any{"type": "text", "text": m.Content},
			}
			for _, img := range m.Images {
				dataURL := fmt.Sprintf("data:%s;base64,%s", img.MediaType, base64.StdEncoding.EncodeToString(img.Data))
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": dataURL}})
			}
			content = parts
		}
		msg := map[string]any{
			"role":    string(m.Role),
			"content": content,
		}
		if m.Role == RoleAssistant && len(m.ToolCalls) > 0 {
			tcs := make([]map[string]any, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				tcs = append(tcs, map[string]any{
					"id":   tc.ID,
					"type": "function",
					"function": map[string]any{
						"name":      copilotSanitizeName(tc.Name),
						"arguments": tc.ArgsJSON,
					},
				})
			}
			msg["tool_calls"] = tcs
		}
		if m.Role == RoleTool {
			msg["tool_call_id"] = m.ToolCallID
		}
		chatMessages = append(chatMessages, msg)
	}

	body := map[string]any{
		"model":      model,
		"messages":   chatMessages,
		"stream":     true,
		"max_tokens": ChatDefaultMaxTokens,
	}
	if functionTools := copilotFunctionTools(tools); len(functionTools) > 0 {
		body["tools"] = functionTools
	}
	return json.Marshal(body)
}

func copilotFunctionTools(tools []Tool) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        copilotSanitizeName(t.Name),
				"description": t.Description,
				"parameters":  t.Schema,
			},
		})
	}
	return out
}

// consumeSSE reads Chat Completions SSE events from the response body.
func (a *copilotAdapter) consumeSSE(ctx context.Context, body io.Reader, origToolNames []string, out chan<- StreamEvent) {
	var content strings.Builder
	var finalCalls []ToolCall
	var finishReason string

	// Track in-progress tool calls by index
	toolCallsByIndex := map[int]*ToolCall{}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 256*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.FinishReason != "" {
			finishReason = choice.FinishReason
		}
		delta := choice.Delta

		if delta.Content != "" {
			content.WriteString(delta.Content)
			select {
			case <-ctx.Done():
				out <- StreamEvent{Kind: EventErr, Err: ctx.Err()}
				return
			case out <- StreamEvent{Kind: EventTokenDelta, Token: delta.Content}:
			}
		}

		for _, tc := range delta.ToolCalls {
			existing, ok := toolCallsByIndex[tc.Index]
			if !ok {
				existing = &ToolCall{}
				toolCallsByIndex[tc.Index] = existing
			}
			if tc.ID != "" {
				existing.ID = tc.ID
			}
			if tc.Function.Name != "" {
				existing.Name = tc.Function.Name
			}
			existing.ArgsJSON += tc.Function.Arguments

			select {
			case <-ctx.Done():
				out <- StreamEvent{Kind: EventErr, Err: ctx.Err()}
				return
			case out <- StreamEvent{Kind: EventStreamProgress}:
			}
		}
	}
	if err := scanner.Err(); err != nil {
		out <- StreamEvent{Kind: EventErr, Err: fmt.Errorf("copilot: read sse: %w", err)}
		return
	}

	for i := 0; i < len(toolCallsByIndex); i++ {
		if tc, ok := toolCallsByIndex[i]; ok {
			tc.Name = copilotUnsanitizeName(tc.Name, origToolNames)
			finalCalls = append(finalCalls, *tc)
		}
	}

	final := Message{
		Role:       RoleAssistant,
		Content:    content.String(),
		ToolCalls:  finalCalls,
		StopReason: finishReason,
	}
	out <- StreamEvent{Kind: EventDone, Final: &final}
}
