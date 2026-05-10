package adapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	openaiauth "github.com/yottadynamics/yottacode/internal/auth/openai"
)

// supportedModels reads the per-user model allow-list from the file
// written after each successful login (~/.yottacode/auth/openai-auth-
// models.json). Returns nil when the file is absent — the caller
// branches on that to render the "no models discovered yet" hint
// rather than masking it with a hardcoded fallback.
//
// Same source the catalog package consults — guarantees adapter and
// picker stay in lockstep.
func supportedModels() []string {
	path, err := openaiauth.DefaultModelsPath()
	if err != nil {
		return nil
	}
	mf, err := openaiauth.LoadModels(path)
	if err != nil {
		return nil
	}
	return mf.Models
}

const (
	// OpenAIAuthEndpoint is the ChatGPT-subscription Responses-API
	// endpoint reachable via OAuth bearer auth. Distinct from
	// api.openai.com/v1/responses both in URL and request contract.
	OpenAIAuthEndpoint = "https://chatgpt.com/backend-api/codex/responses"

	// openAIAuthBetaHeader values the required `OpenAI-Beta` header.
	openAIAuthBetaHeader = "responses=experimental"

	// openAIAuthOriginator matches the OAuth client_id we authed as.
	// The auth server pairs them on the authorize call; the API
	// server checks the originator on every request.
	openAIAuthOriginator = "codex_cli_rs"

	// OpenAIAuthDefaultInstructions is sent when the caller's message
	// slate has no system role. The Codex backend rejects requests
	// with empty/missing instructions ("Instructions are required"),
	// so we always send something.
	OpenAIAuthDefaultInstructions = "You are a helpful assistant."

	// openAIAuthLoginHint is the user-facing reminder for how to
	// (re-)log in. Centralized so any change to the cobra command
	// shape is a one-line edit here, not a hunt across error
	// messages.
	openAIAuthLoginHint = "run `yottacode openai-auth login`"
)

// openAIAuthAdapter speaks to chatgpt.com/backend-api/codex/responses,
// the ChatGPT-subscription backend reachable via OpenAI's "Sign in
// with ChatGPT" OAuth flow.
//
// Distinct from responsesAdapter (which talks to api.openai.com/v1):
// the request contract differs (forced instructions / store:false /
// stream:true), the model allow-list is narrower, and auth comes
// from a token store with refresh-on-expiry rather than a static
// API key. Raw net/http with a small SSE reader so the package has
// no SDK dependency to maintain.
type openAIAuthAdapter struct {
	tokens   *openaiauth.TokenSource
	client   *http.Client
	endpoint string
	model    string
	cfg      Config
	profile  ProviderProfile
}

func newOpenAIAuthAdapter(cfg Config) Client {
	allow := supportedModels()
	if len(allow) == 0 {
		return newErroredAdapter(cfg, ProviderOpenAIAuth,
			fmt.Errorf("openai-auth: no models discovered yet — %s to populate the model list",
				openAIAuthLoginHint))
	}
	if !inModelAllowList(cfg.Model, allow) {
		return newErroredAdapter(cfg, ProviderOpenAIAuth,
			fmt.Errorf("openai-auth: model %q not in your account's discovered set (available: %s) — re-run %s to refresh",
				cfg.Model, strings.Join(allow, ", "), openAIAuthLoginHint))
	}
	storePath, err := openaiauth.DefaultStorePath()
	if err != nil {
		return newErroredAdapter(cfg, ProviderOpenAIAuth,
			fmt.Errorf("openai-auth: resolve token store path: %w", err))
	}
	return newOpenAIAuthAdapterFor(cfg, openaiauth.NewTokenSource(storePath), OpenAIAuthEndpoint)
}

// newOpenAIAuthAdapterFor is the test-friendly inner constructor. It
// skips the model-allow-list check (the public constructor already
// enforced it) and takes the token source + endpoint as inputs so
// tests can wire an httptest server. Production callers go through
// newOpenAIAuthAdapter.
func newOpenAIAuthAdapterFor(cfg Config, src *openaiauth.TokenSource, endpoint string) Client {
	return &openAIAuthAdapter{
		tokens:   src,
		client:   &http.Client{}, // no timeout — SSE responses stream until the model finishes
		endpoint: endpoint,
		model:    cfg.Model,
		cfg:      cfg,
		profile:  buildOpenAIAuthProfile(cfg),
	}
}

// buildOpenAIAuthProfile delegates to the generic buildProfile now
// that usesResponsesAPI + the SupportsReasoning expression both
// know about openai-auth. Keeping it as a thin alias means callers
// don't have to remember which builder to use — and StaticDiagnostics
// (which calls the generic builder) lines up with the runtime
// adapter's profile automatically.
//
// Provider-native built-in tools (web_search, code_interpreter) are
// not available on the Codex backend's ChatGPT-account auth path.
// The generic builder leaves them off because openai-auth isn't in
// the SupportsWebSearch / SupportsCodeInterpreter expressions.
func buildOpenAIAuthProfile(cfg Config) ProviderProfile {
	return buildProfile(cfg, true)
}

func (a *openAIAuthAdapter) Profile() ProviderProfile { return a.profile }

func (a *openAIAuthAdapter) ChatStream(ctx context.Context, messages []Message, tools []Tool) <-chan StreamEvent {
	out := make(chan StreamEvent, 16)
	go func() {
		defer close(out)
		a.runOnce(ctx, messages, tools, out)
	}()
	return out
}

func (a *openAIAuthAdapter) runOnce(ctx context.Context, messages []Message, tools []Tool, out chan<- StreamEvent) {
	body, err := buildOpenAIAuthRequest(a.model, messages, tools)
	if err != nil {
		out <- StreamEvent{Kind: EventErr, Err: fmt.Errorf("openai-auth: build request: %w", err)}
		return
	}
	resp, err := a.sendWithRetry(ctx, body)
	if err != nil {
		out <- StreamEvent{Kind: EventErr, Err: err}
		return
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		// One refresh + retry. If the second attempt also 401s, the
		// session is genuinely expired (refresh_token revoked or
		// rotated past us) and the user has to log in again.
		if err := a.tokens.ForceRefresh(ctx); err != nil {
			out <- StreamEvent{Kind: EventErr,
				Err: fmt.Errorf("openai-auth: session expired and refresh failed (%w) — %s", err, openAIAuthLoginHint)}
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
				Err: errors.New("openai-auth: session expired — " + openAIAuthLoginHint)}
			return
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := errorDetailFromBytes(raw)
		// Format the 429 quota error as a two-line message: a one-sentence
		// summary on top, the timing/plan hint on the bottom. Renderers
		// (TUI + oneshot) split on `\n` and re-prefix each line, so this
		// reads cleanly in scrollback. Other 4xx/5xx stay one-line.
		if resp.StatusCode == http.StatusTooManyRequests {
			if hint := formatRateLimitHint(resp.Header, raw); hint != "" {
				out <- StreamEvent{Kind: EventErr,
					Err: fmt.Errorf("openai-auth: %s\n%s", msg, hint)}
				return
			}
		}
		out <- StreamEvent{Kind: EventErr,
			Err: fmt.Errorf("openai-auth: HTTP %d: %s", resp.StatusCode, msg)}
		return
	}
	a.consumeSSE(ctx, resp.Body, out)
}

// openAIAuth5xxBackoffs is the per-attempt sleep schedule for the
// 5xx auto-retry. Total worst-case wall time = sum + per-attempt
// network latency, so the user sees an error within ~2s of the
// first failure when the backend is genuinely down. Cloudflare's
// "upstream connect error" 503 on the Codex backend is the
// motivating case — it clears within a single beat the vast
// majority of the time.
var openAIAuth5xxBackoffs = []time.Duration{
	500 * time.Millisecond,
	1500 * time.Millisecond,
}

// sendWithRetry wraps send() with a transient-error retry policy:
// 5xx responses (and connection-level errors before any response
// is received) are retried with the backoffs above. 4xx responses
// pass through unchanged — those are caller errors that won't be
// fixed by retrying.
//
// Mid-stream failures are NOT retried by this wrapper — once
// consumeSSE has flushed any byte to the user, a silent restart
// would duplicate already-rendered output. The caller's
// responsibility to treat errors after the stream starts as
// terminal.
func (a *openAIAuthAdapter) sendWithRetry(ctx context.Context, body []byte) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= len(openAIAuth5xxBackoffs); attempt++ {
		resp, err := a.send(ctx, body)
		if err != nil {
			// Treat connection-level failures (DNS, dialer, abrupt
			// reset before any response) the same as 5xx — same
			// transient class, same retry justification.
			lastErr = err
			if attempt == len(openAIAuth5xxBackoffs) {
				return nil, err
			}
			if waitErr := sleepCtx(ctx, openAIAuth5xxBackoffs[attempt]); waitErr != nil {
				return nil, waitErr
			}
			continue
		}
		if resp.StatusCode < 500 {
			return resp, nil
		}
		// 5xx — drain the body so the connection can be reused, then
		// decide whether to retry.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		detail := errorDetailFromBytes(raw)
		if attempt == len(openAIAuth5xxBackoffs) {
			return nil, fmt.Errorf("openai-auth: HTTP %d after %d attempts: %s",
				resp.StatusCode, attempt+1, detail)
		}
		lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, detail)
		if waitErr := sleepCtx(ctx, openAIAuth5xxBackoffs[attempt]); waitErr != nil {
			return nil, waitErr
		}
	}
	return nil, lastErr
}

// sleepCtx is a context-aware time.Sleep — returns ctx.Err() if the
// context cancels before the timer fires. Avoids leaving a goroutine
// blocked in a backoff sleep when the user hits Ctrl-C.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (a *openAIAuthAdapter) send(ctx context.Context, body []byte) (*http.Response, error) {
	token, err := a.tokens.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("openai-auth: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", openAIAuthBetaHeader)
	req.Header.Set("originator", openAIAuthOriginator)
	return a.client.Do(req)
}

// errorDetailFromBytes pulls the human-readable error string out of
// already-buffered JSON. Codex returns `{"detail":"..."}`; OpenAI's
// canonical Responses API returns `{"error":{"message":"..."}}`.
// Falls back to the raw body string when neither shape parses.
func errorDetailFromBytes(raw []byte) string {
	var p struct {
		Detail string `json:"detail"`
		Error  struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &p); err == nil {
		if p.Detail != "" {
			return p.Detail
		}
		if p.Error.Message != "" {
			return p.Error.Message
		}
	}
	return strings.TrimSpace(string(raw))
}

// formatRateLimitHint extracts a "when can I retry" hint from a 429
// response. The Codex backend's actual shape (verified by probe):
//
//	headers:
//	  x-codex-primary-reset-after-seconds: 513093
//	  x-codex-primary-reset-at: 1778598168       (unix seconds)
//	  x-codex-plan-type: free
//	  x-codex-primary-used-percent: 100
//	body:
//	  {"error":{"type":"usage_limit_reached",
//	            "message":"The usage limit has been reached",
//	            "plan_type":"free",
//	            "resets_at":1778598167,
//	            "resets_in_seconds":513092}}
//
// Note: no `Retry-After`, no `x-ratelimit-*`. Probe order below
// reflects observed reality first, RFC fallbacks second, verbatim
// header dump last so any future shape change still surfaces.
//
// Returns "" when nothing useful is found, in which case the caller
// emits the bare detail message unchanged.
func formatRateLimitHint(h http.Header, body []byte) string {
	var bodyParse struct {
		Error struct {
			Type            string  `json:"type"`
			PlanType        string  `json:"plan_type"`
			ResetsAt        int64   `json:"resets_at"`
			ResetsInSeconds float64 `json:"resets_in_seconds"`
		} `json:"error"`
		// RFC-ish top-level fallbacks — kept for forward-compat in case
		// the backend ever flattens the shape.
		ResetAt           string  `json:"reset_at"`
		RetryAfterSeconds float64 `json:"retry_after_seconds"`
		ResetAfter        float64 `json:"reset_after"`
	}
	_ = json.Unmarshal(body, &bodyParse)

	var d time.Duration
	switch {
	case bodyParse.Error.ResetsInSeconds > 0:
		d = time.Duration(bodyParse.Error.ResetsInSeconds * float64(time.Second))
	case bodyParse.Error.ResetsAt > 0:
		d = time.Until(time.Unix(bodyParse.Error.ResetsAt, 0))
	case bodyParse.RetryAfterSeconds > 0:
		d = time.Duration(bodyParse.RetryAfterSeconds * float64(time.Second))
	case bodyParse.ResetAfter > 0:
		d = time.Duration(bodyParse.ResetAfter * float64(time.Second))
	}
	if d <= 0 && bodyParse.ResetAt != "" {
		if t, err := time.Parse(time.RFC3339, bodyParse.ResetAt); err == nil {
			d = time.Until(t)
		}
	}

	if d <= 0 {
		if v := strings.TrimSpace(h.Get("x-codex-primary-reset-after-seconds")); v != "" {
			if secs, err := strconv.ParseFloat(v, 64); err == nil && secs > 0 {
				d = time.Duration(secs * float64(time.Second))
			}
		}
	}
	if d <= 0 {
		if v := strings.TrimSpace(h.Get("x-codex-primary-reset-at")); v != "" {
			if ts, err := strconv.ParseInt(v, 10, 64); err == nil && ts > 0 {
				d = time.Until(time.Unix(ts, 0))
			}
		}
	}
	if d <= 0 {
		if ra := strings.TrimSpace(h.Get("Retry-After")); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
				d = time.Duration(secs) * time.Second
			} else if t, err := http.ParseTime(ra); err == nil {
				d = time.Until(t)
			}
		}
	}

	if d > 0 {
		hint := "retry after " + humanizeDuration(d)
		if plan := strings.TrimSpace(bodyParse.Error.PlanType); plan != "" {
			hint += " (" + plan + " plan)"
		} else if plan := strings.TrimSpace(h.Get("x-codex-plan-type")); plan != "" {
			hint += " (" + plan + " plan)"
		}
		return hint
	}

	// Nothing parseable — dump any rate-limit-shaped headers verbatim
	// so the next 429 reveals the real fields if the contract drifts.
	var parts []string
	for k, v := range h {
		lk := strings.ToLower(k)
		if (strings.HasPrefix(lk, "x-codex-") || strings.HasPrefix(lk, "x-ratelimit-")) && len(v) > 0 {
			parts = append(parts, k+"="+v[0])
		}
	}
	if len(parts) > 0 {
		sort.Strings(parts)
		return strings.Join(parts, ", ")
	}
	return ""
}

// humanizeDuration renders a positive duration in the most readable
// unit for an error message. Sub-minute → seconds, sub-hour → minutes,
// sub-day → hours+minutes, otherwise days+hours. Codex weekly windows
// produce 5–7 day values, so the day bucket matters in practice.
// Non-positive durations collapse to "now".
func humanizeDuration(d time.Duration) string {
	if d <= 0 {
		return "now"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Round(time.Minute).Minutes()))
	}
	if d < 24*time.Hour {
		d = d.Round(time.Minute)
		h := int(d / time.Hour)
		m := int((d % time.Hour) / time.Minute)
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	}
	d = d.Round(time.Hour)
	days := int(d / (24 * time.Hour))
	h := int((d % (24 * time.Hour)) / time.Hour)
	if h == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd %dh", days, h)
}

// buildOpenAIAuthRequest constructs the JSON body for the Codex
// backend. instructions / store / stream are set at build time
// (always, regardless of caller intent) — the backend rejects any
// other shape with HTTP 400. See project memory
// `project_chatgpt_codex_api_contract.md` for the full contract.
func buildOpenAIAuthRequest(model string, messages []Message, tools []Tool) ([]byte, error) {
	instructions, items := splitForOpenAIAuth(messages)
	if strings.TrimSpace(instructions) == "" {
		instructions = OpenAIAuthDefaultInstructions
	}
	body := map[string]any{
		"model":        model,
		"instructions": instructions,
		"input":        items,
		"stream":       true,
		"store":        false,
	}
	if functionTools := openAIAuthFunctionTools(tools); len(functionTools) > 0 {
		body["tools"] = functionTools
	}
	return json.Marshal(body)
}

// inputItem is one entry in the Responses-API "input" array. Mirrors
// the JSON shape the api.openai.com Responses API accepts — the
// Codex backend speaks the same dialect for the body, just with
// a stricter validation policy.
//
// Output is *string (not string with omitempty) because the Codex
// backend requires the field to be present on every
// function_call_output item — including ones whose tool returned
// empty content. A bare `string` with omitempty drops the field for
// empty results and the backend 400s with `Missing required
// parameter: 'input[N].output'`. A nil pointer omits the field for
// non-output items; a non-nil pointer-to-empty-string keeps it.
type inputItem struct {
	Type      string  `json:"type,omitempty"`
	Role      string  `json:"role,omitempty"`
	Content   any     `json:"content,omitempty"`
	CallID    string  `json:"call_id,omitempty"`
	Name      string  `json:"name,omitempty"`
	Arguments string  `json:"arguments,omitempty"`
	Output    *string `json:"output,omitempty"`
}

func splitForOpenAIAuth(ms []Message) (instructions string, items []inputItem) {
	for _, m := range ms {
		switch m.Role {
		case RoleSystem:
			if instructions != "" {
				instructions += "\n\n"
			}
			instructions += m.Content
		case RoleUser:
			items = append(items, inputItem{Role: "user", Content: m.Content})
		case RoleAssistant:
			if m.Content != "" {
				items = append(items, inputItem{Role: "assistant", Content: m.Content})
			}
			for _, tc := range m.ToolCalls {
				items = append(items, inputItem{
					Type:      "function_call",
					CallID:    tc.ID,
					Name:      tc.Name,
					Arguments: tc.ArgsJSON,
				})
			}
		case RoleTool:
			out := m.Content
			items = append(items, inputItem{
				Type:   "function_call_output",
				CallID: m.ToolCallID,
				Output: &out,
			})
		}
	}
	return
}

type openAIAuthFunctionTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

func openAIAuthFunctionTools(tools []Tool) []openAIAuthFunctionTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]openAIAuthFunctionTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, openAIAuthFunctionTool{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Schema,
		})
	}
	return out
}

// consumeSSE reads server-sent events off resp.Body and dispatches
// them to out. Mirrors the event-handling switch in
// internal/adapter/responses.go but reads raw SSE rather than going
// through openai-go's SDK — the wire shape is identical, the SDK
// is just sugar.
func (a *openAIAuthAdapter) consumeSSE(ctx context.Context, body io.Reader, out chan<- StreamEvent) {
	type pendingCall struct {
		id   string // call_id (used by tool_result back-translation)
		name string
	}
	pending := map[string]*pendingCall{}

	var content strings.Builder
	var finalCalls []ToolCall
	var citations []Citation
	var messageStatus string

	reader := newSSEReader(body)
	for {
		ev, err := reader.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			out <- StreamEvent{Kind: EventErr, Err: fmt.Errorf("openai-auth: read sse: %w", err)}
			return
		}

		switch ev.Type {
		case "response.output_text.delta":
			var d struct {
				Delta string `json:"delta"`
			}
			_ = json.Unmarshal(ev.Data, &d)
			if d.Delta == "" {
				continue
			}
			content.WriteString(d.Delta)
			select {
			case <-ctx.Done():
				out <- StreamEvent{Kind: EventErr, Err: ctx.Err()}
				return
			case out <- StreamEvent{Kind: EventTokenDelta, Token: d.Delta}:
			}

		case "response.reasoning_summary_text.delta":
			var d struct {
				Delta string `json:"delta"`
			}
			_ = json.Unmarshal(ev.Data, &d)
			if d.Delta == "" {
				continue
			}
			select {
			case <-ctx.Done():
				out <- StreamEvent{Kind: EventErr, Err: ctx.Err()}
				return
			case out <- StreamEvent{Kind: EventReasoning, Token: d.Delta}:
			}

		case "response.output_item.added":
			var d struct {
				Item struct {
					Type   string `json:"type"`
					ID     string `json:"id"`
					CallID string `json:"call_id"`
					Name   string `json:"name"`
				} `json:"item"`
			}
			_ = json.Unmarshal(ev.Data, &d)
			if d.Item.Type == "function_call" {
				pending[d.Item.ID] = &pendingCall{id: d.Item.CallID, name: d.Item.Name}
			}

		case "response.output_item.done":
			var d struct {
				Item struct {
					Type    string `json:"type"`
					Status  string `json:"status"`
					Content []struct {
						Type        string `json:"type"`
						Annotations []struct {
							Type     string `json:"type"`
							Title    string `json:"title,omitempty"`
							URL      string `json:"url,omitempty"`
							FileID   string `json:"file_id,omitempty"`
							Filename string `json:"filename,omitempty"`
						} `json:"annotations,omitempty"`
					} `json:"content,omitempty"`
				} `json:"item"`
			}
			_ = json.Unmarshal(ev.Data, &d)
			if d.Item.Type == "message" {
				messageStatus = strings.TrimSpace(d.Item.Status)
				for _, part := range d.Item.Content {
					for _, ann := range part.Annotations {
						c := Citation{Type: ann.Type}
						switch ann.Type {
						case "url_citation":
							c.Title = ann.Title
							c.URL = ann.URL
						case "file_citation", "container_file_citation":
							c.FileID = ann.FileID
							c.Filename = ann.Filename
						}
						citations = append(citations, c)
					}
				}
			}

		case "response.function_call_arguments.delta":
			// Heartbeat for tok/s indicator on tool-call-only turns;
			// authoritative args land in .done. See responses.go's
			// matching case for the same reasoning.
			select {
			case <-ctx.Done():
				out <- StreamEvent{Kind: EventErr, Err: ctx.Err()}
				return
			case out <- StreamEvent{Kind: EventStreamProgress}:
			}

		case "response.function_call_arguments.done":
			var d struct {
				ItemID    string `json:"item_id"`
				Arguments string `json:"arguments"`
			}
			_ = json.Unmarshal(ev.Data, &d)
			if pc := pending[d.ItemID]; pc != nil {
				finalCalls = append(finalCalls, ToolCall{ID: pc.id, Name: pc.name, ArgsJSON: d.Arguments})
				delete(pending, d.ItemID)
			}

		case "error":
			var d struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(ev.Data, &d)
			msg := d.Message
			if msg == "" {
				msg = "stream error"
			}
			out <- StreamEvent{Kind: EventErr, Err: errors.New("openai-auth: " + msg)}
			return
		}
	}

	final := Message{
		Role:      RoleAssistant,
		Content:   content.String(),
		ToolCalls: finalCalls,
		Citations: dedupeCitations(citations),
	}
	if messageStatus == "incomplete" {
		final.StopReason = "incomplete"
	}
	out <- StreamEvent{Kind: EventDone, Final: &final}
}

// sseEvent is one parsed SSE record (event:/data: pair).
type sseEvent struct {
	Type string
	Data []byte
}

// sseReader is a minimal Server-Sent Events reader sufficient for
// the OpenAI Responses-API event stream. Per W3C spec it supports
// multiple data: lines per event (joined with \n), comments
// (starting with `:`), and ignores unknown fields. We don't care
// about `id:` or `retry:` here.
type sseReader struct {
	r *bufio.Reader
}

func newSSEReader(r io.Reader) *sseReader {
	return &sseReader{r: bufio.NewReaderSize(r, 64*1024)}
}

func (s *sseReader) next() (sseEvent, error) {
	var ev sseEvent
	var data bytes.Buffer
	for {
		line, err := s.r.ReadString('\n')
		if line == "" && errors.Is(err, io.EOF) {
			if ev.Type != "" || data.Len() > 0 {
				ev.Data = data.Bytes()
				return ev, nil
			}
			return sseEvent{}, io.EOF
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return sseEvent{}, err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			if ev.Type != "" || data.Len() > 0 {
				ev.Data = data.Bytes()
				return ev, nil
			}
			if errors.Is(err, io.EOF) {
				return sseEvent{}, io.EOF
			}
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, ":"):
			// comment, ignore
		case strings.HasPrefix(trimmed, "event:"):
			ev.Type = strings.TrimSpace(trimmed[len("event:"):])
		case strings.HasPrefix(trimmed, "data:"):
			d := strings.TrimPrefix(trimmed[len("data:"):], " ")
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(d)
		}
		if errors.Is(err, io.EOF) {
			if ev.Type != "" || data.Len() > 0 {
				ev.Data = data.Bytes()
				return ev, nil
			}
			return sseEvent{}, io.EOF
		}
	}
}

func inModelAllowList(model string, allow []string) bool {
	for _, m := range allow {
		if m == model {
			return true
		}
	}
	return false
}
