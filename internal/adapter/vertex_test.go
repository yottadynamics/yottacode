package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// --- shared test doubles ---------------------------------------------

// stubVertexTokens is a vertexTokenSource that hands back a canned token
// (or a canned failure) without touching Application Default Credentials.
type stubVertexTokens struct {
	mu    sync.Mutex
	token string
	err   error
	calls int
}

func (s *stubVertexTokens) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return "", s.err
	}
	return s.token, nil
}

func (s *stubVertexTokens) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type vertexCapture struct {
	mu     sync.Mutex
	body   []byte
	path   string
	header http.Header
}

func (c *vertexCapture) snapshot() (path string, body []byte, header http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.path, c.body, c.header
}

// vertexCapturingMockServer records the outbound request and replays a
// canned SSE body. Both Vertex kinds are tested through the real SDK
// clients, so what lands here is exactly what Google would receive.
func vertexCapturingMockServer(t *testing.T, body string) (*httptest.Server, *vertexCapture) {
	t.Helper()
	cap := &vertexCapture{}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cap.mu.Lock()
		cap.body = b
		// RequestURI is the raw, on-the-wire path — the point is to
		// catch escaping of "@" and ":" in the model segment, which
		// r.URL.Path would have already decoded away.
		cap.path = r.RequestURI
		cap.header = r.Header.Clone()
		cap.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		fmt.Fprint(w, body)
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, cap
}

func vertexGeminiBaseURL(origin string) string {
	return origin + "/v1/projects/test-proj/locations/us-central1/endpoints/openapi"
}

// --- Gemini-on-Vertex (kind: vertex) ---------------------------------

func TestVertex_SendsADCBearerAndKeepsShimPath(t *testing.T) {
	body := "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n" +
		"data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	srv, cap := vertexCapturingMockServer(t, body)
	tokens := &stubVertexTokens{token: "ya29.test-token"}

	ad := newVertexAdapterFor(Config{
		BaseURL: vertexGeminiBaseURL(srv.URL),
		Model:   "google/gemini-2.5-pro",
	}, tokens)

	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	_, _, final, errs := drainEvents(ch)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if final == nil || final.Content != "ok" {
		t.Fatalf("final = %+v, want content %q", final, "ok")
	}

	path, _, header := cap.snapshot()
	if got := header.Get("Authorization"); got != "Bearer ya29.test-token" {
		t.Errorf("Authorization = %q, want the ADC bearer", got)
	}
	// The shim is a plain OpenAI-compatible endpoint: base_url + the
	// SDK's own /chat/completions. No rewriting should happen here.
	want := "/v1/projects/test-proj/locations/us-central1/endpoints/openapi/chat/completions"
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if tokens.callCount() == 0 {
		t.Error("token source was never consulted")
	}
}

func TestVertex_NormalizesBareGeminiModelID(t *testing.T) {
	body := "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	srv, cap := vertexCapturingMockServer(t, body)

	ad := newVertexAdapterFor(Config{
		BaseURL: vertexGeminiBaseURL(srv.URL),
		Model:   "gemini-2.5-pro",
	}, &stubVertexTokens{token: "t"})

	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if _, _, _, errs := drainEvents(ch); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	_, raw, _ := cap.snapshot()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if got["model"] != "google/gemini-2.5-pro" {
		t.Errorf("model = %v, want google/gemini-2.5-pro", got["model"])
	}
}

func TestVertex_FinalMessageStampsModelAndProvider(t *testing.T) {
	body := "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n" +
		"data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	srv, _ := vertexCapturingMockServer(t, body)

	ad := newVertexAdapterFor(Config{
		BaseURL: vertexGeminiBaseURL(srv.URL),
		Model:   "gemini-2.5-pro",
	}, &stubVertexTokens{token: "t"})

	_, _, final, errs := drainEvents(ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil))
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if final == nil {
		t.Fatal("no final message")
	}
	if final.Model != "google/gemini-2.5-pro" {
		t.Errorf("final.Model = %q, want normalized Vertex Gemini model", final.Model)
	}
	if final.Provider != string(ProviderVertex) {
		t.Errorf("final.Provider = %q, want %q", final.Provider, ProviderVertex)
	}
}

func TestVertexAnthropic_FinalMessageStampsModelAndProvider(t *testing.T) {
	body := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-sonnet-4-5@20250929\"}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	srv, _ := vertexCapturingMockServer(t, body)

	ad := newVertexAnthropicAdapterFor(Config{
		BaseURL: vertexAnthropicBaseURL(srv.URL),
		Model:   "claude-sonnet-4-5@20250929",
	}, &stubVertexTokens{token: "t"})

	_, _, final, errs := drainEvents(ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil))
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if final == nil {
		t.Fatal("no final message")
	}
	if final.Model != "claude-sonnet-4-5@20250929" {
		t.Errorf("final.Model = %q, want configured Vertex Anthropic model", final.Model)
	}
	if final.Provider != string(ProviderVertexAnthropic) {
		t.Errorf("final.Provider = %q, want %q", final.Provider, ProviderVertexAnthropic)
	}
}

// The whole reason vertex is a kind rather than an openai-compatible
// profile: the credential must be minted per request, not once at
// construction, because ADC tokens expire in ~1h.
func TestVertex_MintsTokenOnEveryRequest(t *testing.T) {
	body := "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	srv, _ := vertexCapturingMockServer(t, body)
	tokens := &stubVertexTokens{token: "ya29.test-token"}

	ad := newVertexAdapterFor(Config{
		BaseURL: vertexGeminiBaseURL(srv.URL),
		Model:   "google/gemini-2.5-pro",
	}, tokens)

	for i := 0; i < 3; i++ {
		ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
		if _, _, _, errs := drainEvents(ch); len(errs) > 0 {
			t.Fatalf("turn %d: unexpected errors: %v", i, errs)
		}
	}
	if got := tokens.callCount(); got != 3 {
		t.Errorf("token minted %d times across 3 turns, want 3 — a cached-at-construction token would expire mid-session", got)
	}
}

func TestVertex_TokenFailureSurfacesAsStreamError(t *testing.T) {
	srv, _ := vertexCapturingMockServer(t, "")
	tokens := &stubVertexTokens{err: errors.New("no Application Default Credentials found")}

	ad := newVertexAdapterFor(Config{
		BaseURL: vertexGeminiBaseURL(srv.URL),
		Model:   "google/gemini-2.5-pro",
	}, tokens)

	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	_, _, _, errs := drainEvents(ch)
	if len(errs) == 0 {
		t.Fatal("want an error when ADC is unavailable, got none")
	}
}

func TestVertex_EmptyBaseURLIsAConfigError(t *testing.T) {
	ad := newVertexAdapterFor(Config{Model: "google/gemini-2.5-pro"}, &stubVertexTokens{token: "t"})
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	_, _, _, errs := drainEvents(ch)
	if len(errs) == 0 {
		t.Fatal("want a config error for an empty base_url, got none")
	}
}

// Both kinds delegate to a constructor that rebuilds the profile from
// cfg, and buildProfile sniffs base_url when nothing pins the provider.
// A Vertex adapter serving a host that isn't literally
// aiplatform.googleapis.com — a private endpoint, a test server — would
// otherwise report itself as openai-compatible, which silently
// mislabels the status bar and drops reasoning effort.
func TestVertex_ProfileIsPinnedRegardlessOfHost(t *testing.T) {
	srv, _ := vertexCapturingMockServer(t, "")
	tokens := &stubVertexTokens{token: "t"}

	gemini := newVertexAdapterFor(Config{
		BaseURL: vertexGeminiBaseURL(srv.URL),
		Model:   "google/gemini-2.5-pro",
	}, tokens)
	if got := gemini.Profile().Provider; got != ProviderVertex {
		t.Errorf("vertex adapter reports Provider=%q on a non-Google host, want %q", got, ProviderVertex)
	}

	claude := newVertexAnthropicAdapterFor(Config{
		BaseURL: vertexAnthropicBaseURL(srv.URL),
		Model:   "claude-sonnet-4-5@20250929",
	}, tokens)
	if got := claude.Profile().Provider; got != ProviderVertexAnthropic {
		t.Errorf("vertex-anthropic adapter reports Provider=%q on a non-Google host, want %q", got, ProviderVertexAnthropic)
	}
}

// --- reasoning effort ------------------------------------------------

// Measured against the real shim: on a hard prompt gemini-2.5-pro spends
// ~800 thinking tokens at low and ~7,600 at high, so the enum is a real
// knob. The shim also validates the field, so an unmapped level must not
// be invented.
func TestVertex_SendsReasoningEffort(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name             string
		effort           string
		supportsThinking *bool
		want             string // "" means the field must be absent
	}{
		{name: "low", effort: "low", want: "low"},
		{name: "medium", effort: "medium", want: "medium"},
		{name: "high", effort: "high", want: "high"},
		// Default means "inject nothing" — the provider's own default is
		// not the same as any level we could pick.
		{name: "default omits the field", effort: "", want: ""},
		{name: "unknown level omits the field", effort: "banana", want: ""},
		// nil (unknown capability) tries anyway; only an explicit no from
		// the catalog suppresses. Matches the native Gemini path.
		{name: "unknown capability still sends", effort: "high", supportsThinking: nil, want: "high"},
		{name: "catalog says thinking works", effort: "high", supportsThinking: &yes, want: "high"},
		{name: "catalog says it cannot think", effort: "high", supportsThinking: &no, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
			srv, cap := vertexCapturingMockServer(t, body)

			ad := newVertexAdapterFor(Config{
				BaseURL:               vertexGeminiBaseURL(srv.URL),
				Model:                 "google/gemini-2.5-pro",
				ReasoningEffort:       tc.effort,
				ModelSupportsThinking: tc.supportsThinking,
			}, &stubVertexTokens{token: "t"})

			ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
			if _, _, _, errs := drainEvents(ch); len(errs) > 0 {
				t.Fatalf("unexpected errors: %v", errs)
			}

			_, raw, _ := cap.snapshot()
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			if tc.want == "" {
				if v, ok := got["reasoning_effort"]; ok {
					t.Errorf("reasoning_effort = %v, want the field absent", v)
				}
				return
			}
			if got["reasoning_effort"] != tc.want {
				t.Errorf("reasoning_effort = %v, want %q", got["reasoning_effort"], tc.want)
			}
		})
	}
}

// Effort must not leak to the other OpenAI-compatible providers sharing
// chatAdapter — Ollama and vLLM don't take the field.
func TestChatAdapter_EffortStaysScopedToXAIAndVertex(t *testing.T) {
	body := "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	srv, cap := vertexCapturingMockServer(t, body)

	ad := newChatAdapter(Config{
		BaseURL:         srv.URL,
		APIKey:          "k",
		Model:           "llama3.1:8b",
		ReasoningEffort: "high",
	})
	ch := ad.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if _, _, _, errs := drainEvents(ch); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	_, raw, _ := cap.snapshot()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if v, ok := got["reasoning_effort"]; ok {
		t.Errorf("reasoning_effort = %v sent to a generic openai-compatible endpoint; want absent", v)
	}
}

func TestEffortInapplicableReason_Vertex(t *testing.T) {
	no := false
	tests := []struct {
		name     string
		cfg      Config
		provider Provider
		wantWarn bool
	}{
		{
			name:     "vertex with effort set warns about nothing",
			cfg:      Config{ReasoningEffort: "high", Model: "google/gemini-2.5-pro"},
			provider: ProviderVertex,
		},
		{
			name:     "vertex-anthropic with effort set warns about nothing",
			cfg:      Config{ReasoningEffort: "high", Model: "claude-sonnet-4-5@20250929"},
			provider: ProviderVertexAnthropic,
		},
		{
			name:     "vertex model that cannot think is flagged",
			cfg:      Config{ReasoningEffort: "high", Model: "m", ModelSupportsThinking: &no},
			provider: ProviderVertex,
			wantWarn: true,
		},
		{
			name:     "vertex-anthropic model that cannot think is flagged",
			cfg:      Config{ReasoningEffort: "high", Model: "m", ModelSupportsThinking: &no},
			provider: ProviderVertexAnthropic,
			wantWarn: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EffortInapplicableReason(tc.cfg, tc.provider)
			if tc.wantWarn && got == "" {
				t.Error("want a reason explaining the effort is a no-op, got none")
			}
			if !tc.wantWarn && got != "" {
				t.Errorf("effort applies here, but got warning %q", got)
			}
		})
	}
}

// --- routing ---------------------------------------------------------

// The sharpest trap in this integration: resolveProvider's model-tag
// fallback (isAnthropicModel / isGeminiModel) used to fire
// unconditionally, which would send a Vertex config to api.anthropic.com
// or generativelanguage.googleapis.com — hosts that reject a Google
// credential. Vertex serves exactly the model names that trigger the
// fallback, so this must keep working.
func TestResolveProvider_VertexBeatsModelTagFallback(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want Provider
	}{
		{
			name: "explicit kind wins over claude- model tag",
			cfg: Config{
				ProviderOverride: ProviderVertexAnthropic,
				BaseURL:          "https://aiplatform.googleapis.com/v1/projects/p/locations/global",
				Model:            "claude-sonnet-4-5@20250929",
			},
			want: ProviderVertexAnthropic,
		},
		{
			name: "explicit kind wins over gemini- model tag",
			cfg: Config{
				ProviderOverride: ProviderVertex,
				BaseURL:          "https://us-central1-aiplatform.googleapis.com/v1/projects/p/locations/us-central1/endpoints/openapi",
				Model:            "gemini-2.5-pro",
			},
			want: ProviderVertex,
		},
		{
			name: "aiplatform host without the shim path is claude",
			cfg: Config{
				BaseURL: "https://us-east5-aiplatform.googleapis.com/v1/projects/p/locations/us-east5",
				Model:   "claude-opus-4-8@default",
			},
			want: ProviderVertexAnthropic,
		},
		{
			name: "aiplatform host with the shim path is gemini",
			cfg: Config{
				BaseURL: "https://us-central1-aiplatform.googleapis.com/v1/projects/p/locations/us-central1/endpoints/openapi",
				Model:   "google/gemini-2.5-pro",
			},
			want: ProviderVertex,
		},
		// The fallback must still work for everyone else.
		{
			name: "non-vertex proxy still resolves by model tag",
			cfg:  Config{BaseURL: "https://corp-gateway.example.com", Model: "claude-opus-4-7"},
			want: ProviderAnthropic,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveProvider(tc.cfg); got != tc.want {
				t.Errorf("resolveProvider = %q, want %q", got, tc.want)
			}
		})
	}
}

// Vertex has no API key by design — the credential is an ADC token — so
// the static "no API key configured" guard must not reject it.
func TestNewWithConfig_VertexNeedsNoAPIKey(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "vertex",
			cfg: Config{
				ProviderOverride: ProviderVertex,
				BaseURL:          "https://us-central1-aiplatform.googleapis.com/v1/projects/p/locations/us-central1/endpoints/openapi",
				Model:            "google/gemini-2.5-pro",
			},
		},
		{
			name: "vertex-anthropic",
			cfg: Config{
				ProviderOverride: ProviderVertexAnthropic,
				BaseURL:          "https://aiplatform.googleapis.com/v1/projects/p/locations/global",
				Model:            "claude-sonnet-4-5@20250929",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if configRequiresAPIKey(tc.cfg, tc.cfg.ProviderOverride) {
				t.Fatal("configRequiresAPIKey = true; a Vertex session would die at construction with `no API key configured`")
			}
			// NewWithConfig resolves real ADC here, so only assert it did
			// not short-circuit into the missing-key errored adapter.
			c := NewWithConfig(tc.cfg)
			if ea, ok := c.(*erroredAdapter); ok && strings.Contains(ea.err.Error(), "no API key configured") {
				t.Fatalf("routed to the missing-API-key errored adapter: %v", ea.err)
			}
		})
	}
}

func TestBuildProfile_VertexAdvertisesReasoningAndImages(t *testing.T) {
	for _, provider := range []Provider{ProviderVertex, ProviderVertexAnthropic} {
		t.Run(string(provider), func(t *testing.T) {
			p := buildProfile(Config{ProviderOverride: provider, Model: "m"}, false)
			if p.Provider != provider {
				t.Fatalf("Provider = %q, want %q", p.Provider, provider)
			}
			if !p.SupportsReasoning {
				t.Error("SupportsReasoning = false; both Vertex families think")
			}
			if !p.SupportsImages {
				t.Error("SupportsImages = false; both Vertex families take images")
			}
			if !p.SupportsUsageReporting {
				t.Error("SupportsUsageReporting = false; Vertex bills per token and reports usage")
			}
		})
	}
}

// --- preflight probe -------------------------------------------------

// Probe's generic path does GET {base_url}/models with cfg.APIKey. For
// Vertex that means a 404 (the Gemini shim has no /models route) or a 401
// (there is no API key to send) — so both kinds were rejected before the
// first turn even though the credentials were fine. Found end-to-end, not
// by unit test; this keeps it fixed.
func TestProbe_VertexChecksCredentialsNotModelsRoute(t *testing.T) {
	// A server that 404s everything, standing in for the shim's missing
	// /models route. A passing probe here proves nothing was requested.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("probe made an HTTP request to %s; Vertex has no /models surface to probe", r.URL.Path)
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	prev := newVertexTokenSource
	t.Cleanup(func() { newVertexTokenSource = prev })

	t.Run("healthy credentials pass", func(t *testing.T) {
		newVertexTokenSource = func() vertexTokenSource { return &stubVertexTokens{token: "ya29.ok"} }
		for _, kind := range []Provider{ProviderVertex, ProviderVertexAnthropic} {
			res := Probe(context.Background(), Config{
				ProviderOverride: kind,
				BaseURL:          srv.URL + "/v1/projects/p/locations/global",
				Model:            "claude-sonnet-4-5@20250929",
			})
			if len(res.Issues) > 0 {
				t.Errorf("%s: Probe reported issues with valid credentials: %v", kind, res.Issues)
			}
			if !res.AuthOK {
				t.Errorf("%s: AuthOK = false, want true", kind)
			}
		}
	})

	t.Run("missing credentials surface the setup hint", func(t *testing.T) {
		newVertexTokenSource = func() vertexTokenSource {
			return &stubVertexTokens{err: errors.New("vertex: no Application Default Credentials found")}
		}
		res := Probe(context.Background(), Config{
			ProviderOverride: ProviderVertexAnthropic,
			BaseURL:          srv.URL + "/v1/projects/p/locations/global",
			Model:            "claude-sonnet-4-5@20250929",
		})
		if len(res.Issues) == 0 {
			t.Fatal("Probe reported no issues despite unusable credentials")
		}
		if res.AuthOK {
			t.Error("AuthOK = true despite unusable credentials")
		}
		if !strings.Contains(strings.Join(res.Issues, "; "), "Application Default Credentials") {
			t.Errorf("issues = %v, want the ADC failure surfaced", res.Issues)
		}
	})
}

// --- splitVertexBaseURL ----------------------------------------------

func TestSplitVertexBaseURL(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantOrigin string
		wantPath   string
		wantErr    bool
	}{
		{
			name:       "global endpoint",
			in:         "https://aiplatform.googleapis.com/v1/projects/p/locations/global",
			wantOrigin: "https://aiplatform.googleapis.com/",
			wantPath:   "/v1/projects/p/locations/global",
		},
		{
			name:       "regional endpoint",
			in:         "https://us-east5-aiplatform.googleapis.com/v1/projects/p/locations/us-east5",
			wantOrigin: "https://us-east5-aiplatform.googleapis.com/",
			wantPath:   "/v1/projects/p/locations/us-east5",
		},
		{
			name:       "trailing slash is trimmed",
			in:         "https://aiplatform.googleapis.com/v1/projects/p/locations/global/",
			wantOrigin: "https://aiplatform.googleapis.com/",
			wantPath:   "/v1/projects/p/locations/global",
		},
		{name: "empty", in: "", wantErr: true},
		{name: "no scheme or host", in: "/v1/projects/p/locations/global", wantErr: true},
		{name: "missing project and location", in: "https://aiplatform.googleapis.com/v1", wantErr: true},
		{name: "missing location", in: "https://aiplatform.googleapis.com/v1/projects/p", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			origin, path, err := splitVertexBaseURL(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error for %q, got origin=%q path=%q", tc.in, origin, path)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if origin != tc.wantOrigin {
				t.Errorf("origin = %q, want %q", origin, tc.wantOrigin)
			}
			if path != tc.wantPath {
				t.Errorf("path = %q, want %q", path, tc.wantPath)
			}
		})
	}
}
