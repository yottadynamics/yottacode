package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLiveOpenAICompatible_CapturesContextWindow verifies that the live
// /models probe reads whichever context-window field the provider
// volunteers (context_length / max_model_len / context_window) and
// leaves it zero when the provider reports none.
func TestLiveOpenAICompatible_CapturesContextWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"openrouter-style","context_length":131072},
			{"id":"vllm-style","max_model_len":65536},
			{"id":"proxy-style","context_window":200000},
			{"id":"no-window"}
		]}`))
	}))
	defer srv.Close()

	models, err := liveOpenAICompatible(context.Background(), "openai-compatible", srv.URL, "key")
	if err != nil {
		t.Fatalf("liveOpenAICompatible: %v", err)
	}

	want := map[string]int{
		"openrouter-style": 131072,
		"vllm-style":       65536,
		"proxy-style":      200000,
		"no-window":        0,
	}
	if len(models) != len(want) {
		t.Fatalf("got %d models, want %d", len(models), len(want))
	}
	for _, m := range models {
		w, ok := want[m.ID]
		if !ok {
			t.Errorf("unexpected model id %q", m.ID)
			continue
		}
		if m.ContextWindow != w {
			t.Errorf("model %q: ContextWindow = %d, want %d", m.ID, m.ContextWindow, w)
		}
	}
}

// TestOllamaContextWindow verifies /api/show resolution: a pinned num_ctx
// (Modelfile parameter) wins over the architectural context_length, the
// architectural value is the fallback, and a missing base URL or absent
// context length yields 0. Also confirms a trailing /v1 on the base URL is
// stripped (the OpenAI shim path) so /api/show resolves at the root.
func TestOllamaContextWindow(t *testing.T) {
	respond := func(body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/show" {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(body))
		}))
	}

	t.Run("pinned_num_ctx_wins", func(t *testing.T) {
		srv := respond(`{"parameters":"num_ctx 8192\nstop \"x\"","model_info":{"general.architecture":"llama","llama.context_length":131072}}`)
		defer srv.Close()
		if got := ollamaContextWindow(context.Background(), srv.URL+"/v1", "llama3.1"); got != 8192 {
			t.Errorf("ollamaContextWindow = %d, want 8192 (pinned num_ctx)", got)
		}
	})

	t.Run("architectural_fallback", func(t *testing.T) {
		srv := respond(`{"parameters":"stop \"x\"","model_info":{"general.architecture":"qwen2","qwen2.context_length":32768}}`)
		defer srv.Close()
		if got := ollamaContextWindow(context.Background(), srv.URL, "qwen2.5"); got != 32768 {
			t.Errorf("ollamaContextWindow = %d, want 32768 (architectural context_length)", got)
		}
	})

	t.Run("suffix_scan_fallback", func(t *testing.T) {
		// general.architecture missing → scan for any *.context_length.
		srv := respond(`{"model_info":{"gemma.context_length":8192}}`)
		defer srv.Close()
		if got := ollamaContextWindow(context.Background(), srv.URL, "gemma"); got != 8192 {
			t.Errorf("ollamaContextWindow = %d, want 8192 (suffix scan)", got)
		}
	})

	t.Run("none_returns_zero", func(t *testing.T) {
		srv := respond(`{"model_info":{"general.architecture":"llama"}}`)
		defer srv.Close()
		if got := ollamaContextWindow(context.Background(), srv.URL, "llama3.1"); got != 0 {
			t.Errorf("ollamaContextWindow = %d, want 0", got)
		}
	})

	t.Run("empty_inputs", func(t *testing.T) {
		if got := ollamaContextWindow(context.Background(), "", "m"); got != 0 {
			t.Errorf("empty base URL = %d, want 0", got)
		}
		if got := ollamaContextWindow(context.Background(), "http://x", ""); got != 0 {
			t.Errorf("empty model = %d, want 0", got)
		}
	})
}

func TestParseOllamaNumCtx(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"num_ctx 4096", 4096},
		{"stop \"<|eot|>\"\nnum_ctx 16384\ntemperature 0.7", 16384},
		{"temperature 0.7", 0},
		{"num_ctx notanumber", 0},
		{"num_ctx 0", 0},
		{"", 0},
	}
	for _, c := range cases {
		if got := parseOllamaNumCtx(c.in); got != c.want {
			t.Errorf("parseOllamaNumCtx(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestFirstPositive(t *testing.T) {
	cases := []struct {
		in   []int
		want int
	}{
		{[]int{0, 0, 5}, 5},
		{[]int{7, 9}, 7},
		{[]int{0, 0, 0}, 0},
		{[]int{-3, 0, 4}, 4},
		{nil, 0},
	}
	for _, c := range cases {
		if got := firstPositive(c.in...); got != c.want {
			t.Errorf("firstPositive(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}
