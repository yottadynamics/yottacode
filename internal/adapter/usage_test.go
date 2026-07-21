package adapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAnthropicAdapter_PopulatesUsage runs a recorded SSE script
// whose message_start + message_delta carry the four Anthropic token
// classes (input, output, cache_creation_input, cache_read_input)
// and asserts the final Message.Usage reflects them. Regression
// guard for the "usage silently dropped" path that would otherwise
// make /usage return zero for every Anthropic turn.
func TestAnthropicAdapter_PopulatesUsage(t *testing.T) {
	srv := newAnthropicMockServer(t, anthropicScript{
		events: []anthropicSSE{
			{Event: "message_start", Data: `{"type":"message_start","message":{"id":"msg_u","role":"assistant","content":[],"model":"claude-sonnet-4-5","usage":{"input_tokens":1000,"output_tokens":0,"cache_creation_input_tokens":2000,"cache_read_input_tokens":3000}}}`},
			{Event: "content_block_start", Data: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`},
			{Event: "content_block_stop", Data: `{"type":"content_block_stop","index":0}`},
			{Event: "message_delta", Data: `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":1000,"output_tokens":42,"cache_creation_input_tokens":2000,"cache_read_input_tokens":3000}}`},
			{Event: "message_stop", Data: `{"type":"message_stop"}`},
		},
	})
	t.Cleanup(srv.Close)

	a := newAnthropicAdapter(Config{BaseURL: srv.URL, APIKey: "k", Model: "claude-sonnet-4-5"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var final *Message
	for ev := range a.ChatStream(ctx, []Message{{Role: RoleUser, Content: "hi"}}, nil) {
		if ev.Kind == EventDone {
			final = ev.Final
		}
		if ev.Kind == EventErr {
			t.Fatalf("stream errored: %v", ev.Err)
		}
	}
	if final == nil {
		t.Fatal("no EventDone")
	}
	if final.Usage == nil {
		t.Fatal("Message.Usage = nil; usage parsing skipped")
	}
	if got, want := final.Usage.InputTokens, int64(1000); got != want {
		t.Errorf("InputTokens = %d, want %d", got, want)
	}
	if got, want := final.Usage.OutputTokens, int64(42); got != want {
		t.Errorf("OutputTokens = %d, want %d", got, want)
	}
	if got, want := final.Usage.CacheCreationTokens, int64(2000); got != want {
		t.Errorf("CacheCreationTokens = %d, want %d", got, want)
	}
	if got, want := final.Usage.CacheReadTokens, int64(3000); got != want {
		t.Errorf("CacheReadTokens = %d, want %d", got, want)
	}
}

// TestGeminiAdapter_PopulatesUsage runs a recorded Gemini SSE
// response with usageMetadata on the final frame and asserts the
// neutral Usage tally maps every field correctly — including the
// cachedContent subtraction (Gemini's promptTokenCount includes
// cached tokens, so the adapter normalizes to non-cached input).
func TestGeminiAdapter_PopulatesUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		// Two frames: a text delta, then a final frame carrying usageMetadata.
		_, _ = w.Write([]byte("data: " + `{"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}` + "\n\n"))
		if fl != nil {
			fl.Flush()
		}
		_, _ = w.Write([]byte("data: " + `{"candidates":[{"content":{"parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1500,"candidatesTokenCount":200,"cachedContentTokenCount":500,"thoughtsTokenCount":30,"totalTokenCount":1730}}` + "\n\n"))
		if fl != nil {
			fl.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	a := newGeminiAdapter(Config{BaseURL: srv.URL, APIKey: "k", Model: "gemini-2.5-flash"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var final *Message
	for ev := range a.ChatStream(ctx, []Message{{Role: RoleUser, Content: "hi"}}, nil) {
		if ev.Kind == EventDone {
			final = ev.Final
		}
		if ev.Kind == EventErr {
			t.Fatalf("stream errored: %v", ev.Err)
		}
	}
	if final == nil || final.Usage == nil {
		t.Fatalf("no usage on final: %+v", final)
	}
	// promptTokenCount 1500 - cached 500 = 1000 non-cached input.
	if got, want := final.Usage.InputTokens, int64(1000); got != want {
		t.Errorf("InputTokens = %d, want %d (1500 prompt - 500 cached)", got, want)
	}
	if got, want := final.Usage.CacheReadTokens, int64(500); got != want {
		t.Errorf("CacheReadTokens = %d, want %d", got, want)
	}
	if got, want := final.Usage.OutputTokens, int64(200); got != want {
		t.Errorf("OutputTokens = %d, want %d", got, want)
	}
	if got, want := final.Usage.ReasoningTokens, int64(30); got != want {
		t.Errorf("ReasoningTokens = %d, want %d", got, want)
	}
}

// TestProviderProfile_SupportsUsageReporting locks the per-provider
// gate that /usage uses to decide whether to compute a cost. Ollama
// and NVIDIA NIM stay false (no per-token billing); everything else
// stays true.
func TestProviderProfile_SupportsUsageReporting(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		want    bool
		wantSub bool // optional: also assert provider matches
	}{
		{
			name: "anthropic",
			cfg:  Config{BaseURL: "https://api.anthropic.com", APIKey: "k", Model: "claude-sonnet-4-5"},
			want: true,
		},
		{
			name: "openai",
			cfg:  Config{BaseURL: "https://api.openai.com/v1", APIKey: "k", Model: "gpt-4o"},
			want: true,
		},
		{
			name: "gemini",
			cfg:  Config{BaseURL: "https://generativelanguage.googleapis.com", APIKey: "k", Model: "gemini-2.5-flash"},
			want: true,
		},
		{
			name: "openrouter (other openai-compatible)",
			cfg:  Config{BaseURL: "https://openrouter.ai/api/v1", APIKey: "k", Model: "meta/llama-3.1"},
			want: true,
		},
		{
			name: "ollama (local)",
			cfg:  Config{BaseURL: "http://localhost:11434/v1", APIKey: "", Model: "qwen3.5"},
			want: false,
		},
		{
			name: "nvidia nim (excluded)",
			cfg:  Config{BaseURL: "https://integrate.api.nvidia.com/v1", APIKey: "k", Model: "nvidia/llama-3.1-nemotron"},
			want: false,
		},
		{
			name: "self-hosted vLLM (localhost)",
			cfg:  Config{BaseURL: "http://127.0.0.1:8000/v1", APIKey: "", Model: "llama"},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := buildProfile(tc.cfg, false)
			if p.SupportsUsageReporting != tc.want {
				t.Errorf("SupportsUsageReporting = %v, want %v (provider=%s, baseURL=%s)",
					p.SupportsUsageReporting, tc.want, p.Provider, tc.cfg.BaseURL)
			}
		})
	}
}

// TestProbeOpenAIAuthAccount_HappyPath stands up a fake
// /backend-api/me, writes a valid OAuth token to the redirected
// HOME, points the probe at the fake server, and asserts the
// returned account carries email + plan.
func TestProbeOpenAIAuthAccount_HappyPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	tokenDir := filepath.Join(tmp, ".yottacode", "auth")
	if err := os.MkdirAll(tokenDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tokens := map[string]any{
		"access_token":  "test-access-token",
		"refresh_token": "rt",
		"expires_at":    time.Now().Add(1 * time.Hour).Format(time.RFC3339Nano),
	}
	b, _ := json.Marshal(tokens)
	if err := os.WriteFile(filepath.Join(tokenDir, "openai-auth.json"), b, 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-access-token" {
			t.Errorf("Authorization = %q, want Bearer test-access-token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"email":"alice@example.com","subscription_plan":"Plus","provider":"google"}`))
	}))
	t.Cleanup(srv.Close)

	// Point the probe at the fake server and clear the cache.
	orig := openAIAuthMeEndpoint
	openAIAuthMeEndpoint = srv.URL + "/backend-api/me"
	t.Cleanup(func() { openAIAuthMeEndpoint = orig; SetOpenAIAuthAccountForTest(nil) })
	SetOpenAIAuthAccountForTest(nil)

	got := ProbeOpenAIAuthAccount(context.Background())
	if got == nil {
		t.Fatal("ProbeOpenAIAuthAccount returned nil — probe failed unexpectedly")
	}
	if got.Email != "alice@example.com" {
		t.Errorf("Email = %q, want alice@example.com", got.Email)
	}
	if got.Plan != "Plus" {
		t.Errorf("Plan = %q, want Plus", got.Plan)
	}
}

// TestProbeOpenAIAuthAccount_404DegradesSilently confirms the probe
// returns nil (not an error) when the endpoint shape changed or the
// path 404s — callers fall back to the 429 memo without surfacing
// a confusing error to the user.
func TestProbeOpenAIAuthAccount_404DegradesSilently(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	tokenDir := filepath.Join(tmp, ".yottacode", "auth")
	_ = os.MkdirAll(tokenDir, 0o700)
	tokens := map[string]any{
		"access_token":  "t",
		"refresh_token": "rt",
		"expires_at":    time.Now().Add(1 * time.Hour).Format(time.RFC3339Nano),
	}
	b, _ := json.Marshal(tokens)
	_ = os.WriteFile(filepath.Join(tokenDir, "openai-auth.json"), b, 0o600)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	orig := openAIAuthMeEndpoint
	openAIAuthMeEndpoint = srv.URL + "/backend-api/me"
	t.Cleanup(func() { openAIAuthMeEndpoint = orig; SetOpenAIAuthAccountForTest(nil) })
	SetOpenAIAuthAccountForTest(nil)

	if got := ProbeOpenAIAuthAccount(context.Background()); got != nil {
		t.Errorf("expected nil on 404; got %+v", got)
	}
}

// TestProbeOpenAIAuthAccount_CacheReuse runs the probe twice with a
// counter on the server side; the second call must come from cache.
func TestProbeOpenAIAuthAccount_CacheReuse(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	tokenDir := filepath.Join(tmp, ".yottacode", "auth")
	_ = os.MkdirAll(tokenDir, 0o700)
	tokens := map[string]any{
		"access_token":  "t",
		"refresh_token": "rt",
		"expires_at":    time.Now().Add(1 * time.Hour).Format(time.RFC3339Nano),
	}
	b, _ := json.Marshal(tokens)
	_ = os.WriteFile(filepath.Join(tokenDir, "openai-auth.json"), b, 0o600)

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"email":"a@b","subscription_plan":"Pro"}`))
	}))
	t.Cleanup(srv.Close)
	orig := openAIAuthMeEndpoint
	openAIAuthMeEndpoint = srv.URL
	t.Cleanup(func() { openAIAuthMeEndpoint = orig; SetOpenAIAuthAccountForTest(nil) })
	SetOpenAIAuthAccountForTest(nil)

	_ = ProbeOpenAIAuthAccount(context.Background())
	_ = ProbeOpenAIAuthAccount(context.Background())
	_ = ProbeOpenAIAuthAccount(context.Background())
	if hits != 1 {
		t.Errorf("server hit %d times; want 1 (subsequent calls must come from cache)", hits)
	}
}

// TestRecordOpenAIAuthRateLimit_ExtractsPlanAndReset checks the 429
// memo path: a Codex-shape error body (or x-codex-* headers) must
// populate plan + resets_at so /usage can surface the quota window.
func TestRecordOpenAIAuthRateLimit_ExtractsPlanAndReset(t *testing.T) {
	// Reset state at the start so prior tests don't bleed in.
	openAIAuthRateLimitMu.Lock()
	openAIAuthRateLimit = nil
	openAIAuthRateLimitMu.Unlock()

	body := []byte(`{"error":{"type":"usage_limit_reached","message":"limit","plan_type":"pro","resets_in_seconds":3600}}`)
	hdr := http.Header{}
	recordOpenAIAuthRateLimit(hdr, body)

	got := LastOpenAIAuthRateLimit()
	if got == nil {
		t.Fatal("LastOpenAIAuthRateLimit() = nil; memo not recorded")
	}
	if got.PlanType != "pro" {
		t.Errorf("PlanType = %q, want pro", got.PlanType)
	}
	// The body names a reset without saying which window tripped, so it
	// is attributed to primary.
	if !got.Primary.Has {
		t.Fatal("Primary.Has = false; body reset not attributed to a window")
	}
	// resets_in_seconds = 3600 → reset is ~1h from now; allow 5s tolerance.
	diff := time.Until(got.Primary.ResetsAt) - time.Hour
	if diff > 5*time.Second || diff < -5*time.Second {
		t.Errorf("Primary.ResetsAt ≈ now+1h ± 5s; got %v (diff %v)", got.Primary.ResetsAt, diff)
	}
}

// TestRecordOpenAIAuthRateLimit_ParsesBothWindowsFromHeaders locks the
// live-capture path: a response carrying both x-codex-* header families
// (and no body, as on the success path) must populate both windows with
// percent, width, and reset. This is what lets /usage report standing
// headroom instead of only what the last 429 said.
func TestRecordOpenAIAuthRateLimit_ParsesBothWindowsFromHeaders(t *testing.T) {
	openAIAuthRateLimitMu.Lock()
	openAIAuthRateLimit = nil
	openAIAuthRateLimitMu.Unlock()

	hdr := http.Header{}
	hdr.Set("x-codex-plan-type", "prolite")
	hdr.Set("x-codex-primary-used-percent", "12")
	hdr.Set("x-codex-primary-window-minutes", "300")
	hdr.Set("x-codex-primary-reset-after-seconds", "3600")
	hdr.Set("x-codex-secondary-used-percent", "97.5")
	hdr.Set("x-codex-secondary-window-minutes", "10080")
	hdr.Set("x-codex-secondary-reset-after-seconds", "561600")

	recordOpenAIAuthRateLimit(hdr, nil)

	got := LastOpenAIAuthRateLimit()
	if got == nil {
		t.Fatal("LastOpenAIAuthRateLimit() = nil; header-only capture did not record")
	}
	if got.PlanType != "prolite" {
		t.Errorf("PlanType = %q, want prolite", got.PlanType)
	}
	if !got.Primary.HasPercent || got.Primary.UsedPercent != 12 {
		t.Errorf("Primary percent = %v (has=%v), want 12", got.Primary.UsedPercent, got.Primary.HasPercent)
	}
	if got.Primary.WindowMinutes != 300 {
		t.Errorf("Primary.WindowMinutes = %d, want 300", got.Primary.WindowMinutes)
	}
	if !got.Secondary.HasPercent || got.Secondary.UsedPercent != 97.5 {
		t.Errorf("Secondary percent = %v (has=%v), want 97.5", got.Secondary.UsedPercent, got.Secondary.HasPercent)
	}
	if got.Secondary.WindowMinutes != 10080 {
		t.Errorf("Secondary.WindowMinutes = %d, want 10080", got.Secondary.WindowMinutes)
	}
	// 561600s ≈ 6d12h out; just confirm it landed in the future.
	if !got.Secondary.ResetsAt.After(time.Now().Add(6 * 24 * time.Hour)) {
		t.Errorf("Secondary.ResetsAt = %v, want ~6d12h out", got.Secondary.ResetsAt)
	}
}

// TestRecordOpenAIAuthRateLimit_QuotaLessResponseKeepsMemo guards the
// don't-clobber rule. Capture now runs on every response, including ones
// from endpoints that emit no quota headers at all — those must leave the
// last real observation standing rather than blanking it.
func TestRecordOpenAIAuthRateLimit_QuotaLessResponseKeepsMemo(t *testing.T) {
	openAIAuthRateLimitMu.Lock()
	openAIAuthRateLimit = nil
	openAIAuthRateLimitMu.Unlock()

	hdr := http.Header{}
	hdr.Set("x-codex-primary-used-percent", "42")
	hdr.Set("x-codex-primary-window-minutes", "300")
	recordOpenAIAuthRateLimit(hdr, nil)

	// A response with nothing quota-shaped on it.
	recordOpenAIAuthRateLimit(http.Header{}, nil)

	got := LastOpenAIAuthRateLimit()
	if got == nil {
		t.Fatal("memo was cleared by a quota-less response")
	}
	if got.Primary.UsedPercent != 42 {
		t.Errorf("Primary.UsedPercent = %v, want the retained 42", got.Primary.UsedPercent)
	}
}

// TestFormatQuotaWindowLabel covers the width→name mapping the renderer
// uses so windows are never mislabelled from a hardcoded guess.
func TestFormatQuotaWindowLabel(t *testing.T) {
	for minutes, want := range map[int]string{
		300:   "5h window",
		60:    "1h window",
		10080: "weekly",
		1440:  "daily",
		90:    "90m window",
		0:     "",
		-5:    "",
	} {
		if got := FormatQuotaWindowLabel(minutes); got != want {
			t.Errorf("FormatQuotaWindowLabel(%d) = %q, want %q", minutes, got, want)
		}
	}
}
