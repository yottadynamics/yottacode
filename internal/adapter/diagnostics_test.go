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
