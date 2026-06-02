package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/config"
)

// TestDiscoverContextWindow verifies the live, table-free resolution
// order: the list-models endpoint wins when it reports a window, and the
// overflow probe is the fallback when it doesn't.
func TestDiscoverContextWindow(t *testing.T) {
	const model = "deepseek-ai/deepseek-v4-pro"
	// Disable models.dev network for the subtests that don't seed it, so the
	// resolution order is exercised hermetically (no 2MB fetch).
	seedModelsDev(t, nil)

	t.Run("from_models_dev", func(t *testing.T) {
		// list-models reports no window → models.dev (matched by base-URL
		// host) supplies it, and the overflow probe never fires. This is the
		// NVIDIA NIM path: the deployment lists ids only, models.dev knows
		// the per-host window.
		probed := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/models" {
				_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-ai/deepseek-v4-pro"}]}`))
				return
			}
			probed = true
			w.WriteHeader(http.StatusBadRequest)
		}))
		defer srv.Close()
		seedModelsDev(t, modelsDevCatalog{
			"nim-host": {ID: "nim-host", API: srv.URL, Models: map[string]modelsDevModel{
				model: {Limit: modelsDevLimit{Context: 1_048_576}},
			}},
		})
		p := config.Provider{Name: "nim", Kind: "openai-compatible", BaseURL: srv.URL}
		got, detail := DiscoverContextWindow(context.Background(), p, "", model)
		if got != 1_048_576 {
			t.Errorf("DiscoverContextWindow = %d, want 1048576 (from models.dev)", got)
		}
		if probed {
			t.Error("probe must not fire when models.dev supplies the window")
		}
		if detail != "from models.dev" {
			t.Errorf("detail = %q, want \"from models.dev\"", detail)
		}
	})

	t.Run("from_list_models", func(t *testing.T) {
		// /v1/models reports max_model_len → used directly, no probe.
		probed := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/models" {
				_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-ai/deepseek-v4-pro","max_model_len":262144}]}`))
				return
			}
			probed = true
			w.WriteHeader(http.StatusBadRequest)
		}))
		defer srv.Close()
		p := config.Provider{Name: "nim", Kind: "openai-compatible", BaseURL: srv.URL}
		if got, _ := DiscoverContextWindow(context.Background(), p, "", model); got != 262144 {
			t.Errorf("DiscoverContextWindow = %d, want 262144 (from list-models)", got)
		}
		if probed {
			t.Error("probe should not fire when list-models already reports a window")
		}
	})

	t.Run("no_window_anywhere_returns_zero_without_probing", func(t *testing.T) {
		// list-models lists the model but no window, and models.dev has
		// nothing for this host → 0, and crucially NO /chat/completions
		// overflow request is ever made (the probe is gone).
		probed := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/models" {
				_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-ai/deepseek-v4-pro"}]}`))
				return
			}
			if strings.Contains(r.URL.Path, "chat/completions") {
				probed = true
			}
			w.WriteHeader(http.StatusBadRequest)
		}))
		defer srv.Close()
		p := config.Provider{Name: "nim", Kind: "openai-compatible", BaseURL: srv.URL}
		if got, _ := DiscoverContextWindow(context.Background(), p, "", model); got != 0 {
			t.Errorf("DiscoverContextWindow = %d, want 0 (no source has it)", got)
		}
		if probed {
			t.Error("no overflow probe should ever be sent")
		}
	})

	t.Run("empty_model", func(t *testing.T) {
		p := config.Provider{Name: "nim", Kind: "openai-compatible", BaseURL: "http://unused"}
		if got, _ := DiscoverContextWindow(context.Background(), p, "", ""); got != 0 {
			t.Errorf("DiscoverContextWindow(empty model) = %d, want 0", got)
		}
	})

	t.Run("non_openai_compatible_makes_no_chat_request", func(t *testing.T) {
		// A kind with no live discovery (openai-auth/copilot) resolves to 0
		// without ever POSTing /chat/completions to the wrong endpoint.
		probed := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "chat/completions") {
				probed = true
			}
			w.WriteHeader(http.StatusBadRequest)
		}))
		defer srv.Close()
		p := config.Provider{Name: "chatgpt", Kind: "openai-auth", BaseURL: srv.URL}
		if got, _ := DiscoverContextWindow(context.Background(), p, "", "gpt-5.4"); got != 0 {
			t.Errorf("DiscoverContextWindow = %d, want 0 for non-discoverable kind", got)
		}
		if probed {
			t.Error("no /chat/completions request should be sent")
		}
	})

	t.Run("ollama_uses_api_show_not_probe", func(t *testing.T) {
		// Ollama resolves via POST /api/show (preferring a pinned num_ctx,
		// else the architectural context_length) — never the overflow probe,
		// which Ollama doesn't reject on. The base URL ends in /v1 (the
		// OpenAI shim); /api/show lives at the root.
		probed := false
		var showHit bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.Contains(r.URL.Path, "chat/completions"):
				probed = true
				w.WriteHeader(http.StatusBadRequest)
			case r.URL.Path == "/api/show":
				showHit = true
				_, _ = w.Write([]byte(`{"parameters":"stop \"<|eot|>\"\nnum_ctx 16384","model_info":{"general.architecture":"llama","llama.context_length":131072}}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()
		p := config.Provider{Name: "ollama", Kind: "ollama", BaseURL: srv.URL + "/v1"}
		got, detail := DiscoverContextWindow(context.Background(), p, "", "llama3.1")
		if got != 16384 {
			t.Errorf("DiscoverContextWindow = %d, want 16384 (pinned num_ctx wins over architectural)", got)
		}
		if !showHit {
			t.Error("expected /api/show to be queried for ollama")
		}
		if probed {
			t.Error("ollama must not hit the overflow probe")
		}
		if !strings.Contains(detail, "api/show") {
			t.Errorf("diagnostic = %q, want it to mention /api/show", detail)
		}
	})

	t.Run("ollama_no_context_returns_zero", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/show" {
				_, _ = w.Write([]byte(`{"parameters":"stop \"x\"","model_info":{"general.architecture":"llama"}}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()
		p := config.Provider{Name: "ollama", Kind: "ollama", BaseURL: srv.URL}
		if got, _ := DiscoverContextWindow(context.Background(), p, "", "llama3.1"); got != 0 {
			t.Errorf("DiscoverContextWindow = %d, want 0 when /api/show reports no context length", got)
		}
	})
}
