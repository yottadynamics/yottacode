package adapter

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbe_SucceedsAndFindsModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("auth header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"gpt-5"},{"id":"gpt-4.1"}]}`)
	}))
	t.Cleanup(srv.Close)

	got := Probe(context.Background(), Config{
		BaseURL:          srv.URL,
		APIKey:           "sk-test",
		Model:            "gpt-5",
		ProviderOverride: ProviderOpenAI,
	})
	if !got.EndpointReachable || !got.AuthOK || !got.ModelVisible {
		t.Fatalf("unexpected probe result: %+v", got)
	}
	if len(got.Issues) != 0 {
		t.Fatalf("unexpected issues: %+v", got.Issues)
	}
}

func TestProbe_AuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	got := Probe(context.Background(), Config{
		BaseURL:          srv.URL,
		APIKey:           "bad-key",
		Model:            "gpt-5",
		ProviderOverride: ProviderOpenAI,
	})
	if !got.EndpointReachable {
		t.Fatalf("endpoint should be reachable: %+v", got)
	}
	if got.AuthOK {
		t.Fatalf("auth should fail: %+v", got)
	}
	if msg := strings.Join(got.Issues, "\n"); !strings.Contains(msg, "authentication failed (HTTP 401)") {
		t.Fatalf("issues missing auth failure: %s", msg)
	}
}

func TestProbe_FlagsInvisibleModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"gpt-4.1"}]}`)
	}))
	t.Cleanup(srv.Close)

	got := Probe(context.Background(), Config{
		BaseURL:          srv.URL,
		APIKey:           "sk-test",
		Model:            "gpt-5",
		ProviderOverride: ProviderOpenAI,
	})
	if got.ModelVisible {
		t.Fatalf("model should not be visible: %+v", got)
	}
	if msg := strings.Join(got.Issues, "\n"); !strings.Contains(msg, `model "gpt-5" not listed by /models`) {
		t.Fatalf("issues missing model visibility error: %s", msg)
	}
}

// Anthropic must get a real /v1/models probe — not the static-only
// short-circuit that left EndpointReachable/AuthOK false and pinned the
// connection dot to red for every working Anthropic config. The probe
// authenticates with x-api-key + anthropic-version, not a Bearer token.
func TestProbe_Anthropic_SucceedsAndFindsModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "sk-ant-test" {
			t.Fatalf("x-api-key = %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got == "" {
			t.Fatalf("missing anthropic-version header")
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("Anthropic probe must not send a Bearer token, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"claude-opus-4-8"},{"id":"claude-sonnet-4-6"}]}`)
	}))
	t.Cleanup(srv.Close)

	got := Probe(context.Background(), Config{
		BaseURL:          srv.URL,
		APIKey:           "sk-ant-test",
		Model:            "claude-opus-4-8",
		ProviderOverride: ProviderAnthropic,
	})
	if !got.EndpointReachable || !got.AuthOK || !got.ModelVisible {
		t.Fatalf("unexpected probe result: %+v", got)
	}
	if len(got.Issues) != 0 {
		t.Fatalf("unexpected issues: %+v", got.Issues)
	}
}

// A bad Anthropic key must surface as reachable-but-unauthed, the same
// shape the OpenAI path produces, so the dot goes red for a real reason.
func TestProbe_Anthropic_AuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	got := Probe(context.Background(), Config{
		BaseURL:          srv.URL,
		APIKey:           "bad-key",
		Model:            "claude-opus-4-8",
		ProviderOverride: ProviderAnthropic,
	})
	if !got.EndpointReachable {
		t.Fatalf("endpoint should be reachable: %+v", got)
	}
	if got.AuthOK {
		t.Fatalf("auth should fail: %+v", got)
	}
	if msg := strings.Join(got.Issues, "\n"); !strings.Contains(msg, "authentication failed (HTTP 401)") {
		t.Fatalf("issues missing auth failure: %s", msg)
	}
}

// A gateway base URL that already includes /v1 must not become
// /v1/v1/models.
func TestProbe_Anthropic_TrimsTrailingV1(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q, want /v1/models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"claude-opus-4-8"}]}`)
	}))
	t.Cleanup(srv.Close)

	got := Probe(context.Background(), Config{
		BaseURL:          srv.URL + "/v1",
		APIKey:           "sk-ant-test",
		Model:            "claude-opus-4-8",
		ProviderOverride: ProviderAnthropic,
	})
	if !got.EndpointReachable || !got.AuthOK || !got.ModelVisible {
		t.Fatalf("unexpected probe result: %+v", got)
	}
}

// Gemini had the same class of bug Anthropic did: the generic probe
// sent Authorization: Bearer against <base>/models, but Gemini needs the
// x-goog-api-key header and a /v1beta/models path, so every working
// Gemini config went red. The probe must authenticate the Gemini way,
// hit the version-prefixed path, and strip the "models/" name prefix
// when matching the configured model.
func TestProbe_Gemini_SucceedsAndFindsModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models" {
			t.Fatalf("path = %q, want /v1beta/models", r.URL.Path)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "AIza-test" {
			t.Fatalf("x-goog-api-key = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("Gemini probe must not send a Bearer token, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"models":[{"name":"models/gemini-2.0-flash"},{"name":"models/gemini-1.5-pro"}]}`)
	}))
	t.Cleanup(srv.Close)

	got := Probe(context.Background(), Config{
		BaseURL:          srv.URL,
		APIKey:           "AIza-test",
		Model:            "gemini-2.0-flash",
		ProviderOverride: ProviderGemini,
	})
	if !got.EndpointReachable || !got.AuthOK || !got.ModelVisible {
		t.Fatalf("unexpected probe result: %+v", got)
	}
	if len(got.Issues) != 0 {
		t.Fatalf("unexpected issues: %+v", got.Issues)
	}
}

// A bad Gemini key must surface as reachable-but-unauthed, the same
// shape the OpenAI and Anthropic paths produce.
func TestProbe_Gemini_AuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	got := Probe(context.Background(), Config{
		BaseURL:          srv.URL,
		APIKey:           "bad-key",
		Model:            "gemini-2.0-flash",
		ProviderOverride: ProviderGemini,
	})
	if !got.EndpointReachable {
		t.Fatalf("endpoint should be reachable: %+v", got)
	}
	if got.AuthOK {
		t.Fatalf("auth should fail: %+v", got)
	}
	if msg := strings.Join(got.Issues, "\n"); !strings.Contains(msg, "authentication failed (HTTP 403)") {
		t.Fatalf("issues missing auth failure: %s", msg)
	}
}

// StaticDiagnostics for openai-auth must report the same shape the
// runtime adapter advertises: Responses API + reasoning. Earlier the
// generic buildProfile didn't list openai-auth in the SupportsReasoning
// expression and usesResponsesAPI early-returned false, so /doctor
// claimed "chat-completions / no reasoning" while the actual adapter
// streamed Responses-API events with reasoning summaries.
func TestStaticDiagnostics_OpenAIAuthShape(t *testing.T) {
	res := StaticDiagnostics(Config{
		Model:            "gpt-5.5",
		BaseURL:          "https://chatgpt.com/backend-api/codex",
		ProviderOverride: ProviderOpenAIAuth,
	})
	if res.Profile.Provider != ProviderOpenAIAuth {
		t.Errorf("Profile.Provider = %q, want openai-auth", res.Profile.Provider)
	}
	if !res.Profile.UsesResponsesAPI {
		t.Errorf("UsesResponsesAPI = false; openai-auth must report Responses API")
	}
	if !res.Profile.SupportsReasoning {
		t.Errorf("SupportsReasoning = false; openai-auth surfaces reasoning summaries")
	}
	// Built-in tools are NOT supported on the Codex backend's
	// ChatGPT-account auth path. Generic buildProfile leaves them off
	// because openai-auth isn't in the SupportsWebSearch /
	// SupportsCodeInterpreter expressions.
	if res.Profile.SupportsWebSearch {
		t.Errorf("SupportsWebSearch = true; not available on Codex backend")
	}
	if res.Profile.SupportsCodeInterpreter {
		t.Errorf("SupportsCodeInterpreter = true; not available on Codex backend")
	}
}
