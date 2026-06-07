package agent

// Save-proactivity eval.
//
// The retrieval side of the memory layer has a measured quality gate
// (internal/memory/eval_test.go); the SAVE side historically had none,
// which is how the original under-saving regression — models persisting
// memories only when explicitly asked — shipped invisibly. Two layers
// gate it now:
//
//   - TestDefaultSystemPrompt_ProactiveMemorySteering is the
//     deterministic, dependency-free pin: the prompt sections that
//     steer proactive saving must survive prompt edits.
//   - TestMemorySaveProactivity_LiveEval drives the REAL composed
//     system prompt + memory_save tool through agent.Turn against a
//     local Ollama chat model (skipped when unavailable, mirroring
//     TestRetrievalRelevance_Semantic). Fixtures state blatant durable
//     facts mid-task; a model steered by the shipped prompts should
//     save unprompted. Per-fixture results are logged; the hard gate is
//     deliberately loose — zero saves across ALL fixtures — so a weak
//     local model doesn't flake CI while a real steering regression
//     (e.g. reverting the proactive wording) still fails.
//
// Run with a local model:
//
//	YOTTACODE_MEMORY_EVAL_MODEL=qwen3.5:latest go test ./internal/agent -run Proactivity -v

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/memory"
)

func TestDefaultSystemPrompt_ProactiveMemorySteering(t *testing.T) {
	for _, want := range []string{
		"Don't wait for explicit \"remember this\"",
		"Self-improvement:",
		"memory_save",
	} {
		if !strings.Contains(DefaultSystemPrompt, want) {
			t.Errorf("DefaultSystemPrompt lost proactive-memory steering: missing %q", want)
		}
	}
}

// ollamaEvalModel returns the chat model to eval against, defaulting to
// a small instruct model commonly pulled for local use.
func ollamaEvalModel() string {
	if m := os.Getenv("YOTTACODE_MEMORY_EVAL_MODEL"); m != "" {
		return m
	}
	return "qwen3.5:latest"
}

const ollamaEvalBase = "http://localhost:11434"

// ollamaHasModel probes the local Ollama instance for the named chat
// model. Any failure (no server, no model) reports false so the caller
// can Skip.
func ollamaHasModel(model string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(ollamaEvalBase + "/api/tags")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return false
	}
	for _, m := range tags.Models {
		if m.Name == model || m.Name == model+":latest" || model == m.Name+":latest" {
			return true
		}
	}
	return false
}

// proactivityControl asks for a save EXPLICITLY. It distinguishes "this
// model won't save unprompted" (the behavior under eval) from "this
// model can't execute tool calls through this path at all" (an
// environment limitation that makes proactivity unmeasurable — the eval
// Skips rather than fails).
const proactivityControl = "Use the memory_save tool right now to save this fact: I prefer dark mode in every UI. scope=user, type=user, name=dark-mode-preference."

// proactivityFixtures are user turns that state something durable
// WITHOUT asking for it to be remembered, each alongside an ordinary
// task so the model has a primary request to satisfy. They map to the
// prompt's "When to save" triggers: a stated preference, a correction,
// and a project fact.
var proactivityFixtures = []struct {
	name string
	turn string
}{
	{"stated preference", "By the way, I always want Go tests written as table-driven cases with subtests — keep that in mind going forward. Now, what does the acronym CLI stand for? One sentence."},
	{"correction", "Stop pasting full stack traces into your answers — from now on just summarize the root cause in a sentence. Anyway: what's 17 + 25? Reply with just the number."},
	{"project fact", "For context you'll need in every future session: this project must build with Go 1.22 and CI runs `make ci`, never plain `go build`. Now name one common HTTP status code for success. One sentence."},
}

// evalTrunc clips s for one-line eval logs.
func evalTrunc(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// countSavedMemories walks every agent-managed memory location under
// home and returns the saved memory files (MEMORY.md indexes and
// .archive copies excluded).
func countSavedMemories(t *testing.T, home string) []string {
	t.Helper()
	var saved []string
	for _, dir := range []string{
		filepath.Join(home, ".yottacode", "memory"),
		filepath.Join(home, ".yottacode", "projects"),
	} {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if filepath.Ext(path) != ".md" || filepath.Base(path) == "MEMORY.md" {
				return nil
			}
			if strings.Contains(path, ".archive") {
				return nil
			}
			saved = append(saved, path)
			return nil
		})
	}
	return saved
}

// runProactivityFixture drives one user turn through agent.Turn with
// the REAL shipped steering (identity prompt + cold-start memory
// composition, which ends with the save nudge) and the real memory
// tools rooted in throwaway dirs. Returns the basenames of the memory
// files the turn saved.
func runProactivityFixture(t *testing.T, model, turn string) []string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir() // no git remote — slug falls back to basename

	reg := NewRegistry()
	cwdRef := NewCwdRef(cwd)
	reg.Register(&MemorySaveTool{Cwd: cwdRef})
	reg.Register(&MemorySearchTool{Cwd: cwdRef})

	sysPrompt := memory.SystemPrompt(DefaultSystemPrompt, memory.Loaded{})
	history := []adapter.Message{
		{Role: adapter.RoleSystem, Content: sysPrompt},
		{Role: adapter.RoleUser, Content: turn},
	}

	cfg := LoopConfig{
		Adapter:       adapter.New(ollamaEvalBase+"/v1", "", model),
		Registry:      reg,
		Cwd:           cwdRef,
		MaxIterations: 8,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	events, err := runTurnSync(t, ctx, cfg, &history, nil)
	if err != nil {
		t.Logf("turn error (counted as no-save): %v", err)
	}
	// Diagnostics: which tools ran and what the model said. This is the
	// difference between "steering regressed" and "model hallucinated
	// the save without calling the tool".
	for _, ev := range events {
		switch e := ev.(type) {
		case ToolStart:
			t.Logf("  tool call: %s(%s)", e.ToolName, evalTrunc(e.ArgsJSON, 120))
		case ErrorEvent:
			t.Logf("  error event: %v", e.Err)
		}
	}
	if n := len(history); n > 0 && history[n-1].Role == adapter.RoleAssistant {
		t.Logf("  final reply: %s", evalTrunc(history[n-1].Content, 200))
	}

	saved := countSavedMemories(t, home)
	names := make([]string, len(saved))
	for i, p := range saved {
		names[i] = filepath.Base(p)
	}
	return names
}

func TestMemorySaveProactivity_LiveEval(t *testing.T) {
	model := ollamaEvalModel()
	if !ollamaHasModel(model) {
		t.Skipf("save-proactivity eval needs a local Ollama chat model (%s); skipping (prompt-content pins still gate)", model)
	}

	// Control: an EXPLICIT save request. A model that can't execute
	// that tool call through this path can't have its proactivity
	// measured — Skip (environment limitation), don't fail (steering
	// regression).
	control := runProactivityFixture(t, model, proactivityControl)
	t.Logf("model=%s control (explicit save) → %d save(s) %v", model, len(control), control)
	if len(control) == 0 {
		t.Skipf("model %s did not execute an explicit memory_save tool call; proactivity is unmeasurable on this model", model)
	}

	totalSaves := 0
	for _, fx := range proactivityFixtures {
		t.Run(fx.name, func(t *testing.T) {
			saved := runProactivityFixture(t, model, fx.turn)
			totalSaves += len(saved)
			t.Logf("model=%s fixture=%q → %d save(s) %v", model, fx.name, len(saved), saved)
		})
	}

	// Loose hard gate: a model that handles explicit saves (control
	// passed) and is steered by the shipped prompts should save on at
	// least ONE blatant fixture. Zero across all three means the
	// proactive steering is not reaching the model — the exact
	// regression this eval exists to catch.
	if totalSaves == 0 {
		t.Errorf("0 proactive saves across %d fixtures (control saved fine) — proactive-memory steering regressed (tool description, save nudge, or prompt section)", len(proactivityFixtures))
	} else {
		t.Logf("proactive saves: %d across %d fixtures", totalSaves, len(proactivityFixtures))
	}
}
