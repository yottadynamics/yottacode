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
//   - TestMemorySaveTool_ScopeSteeringPinned pins the schema-level
//     default-to-user scope steering the same way.
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
//     The eval also measures WHERE saves land: each fixture carries a
//     wantScope ground truth (the saved file's path prefix —
//     memory/user/ vs memory/projects/ — is the scope), and a second
//     loose gate fails only on the unambiguous regression signature:
//     saves on ≥2 distinct user-truth fixtures with zero fixtures
//     choosing scope=user, i.e. the steering is not reaching the model.
//     Scope accuracy is logged either way; the project-truth fixture
//     doubles as the over-rotation canary (everything-becomes-user
//     shows up in its log line).
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
		// Recall-bias steering: memory should act like a knowledge base,
		// not only a sparse preference store.
		"When unsure whether something is worth saving, save it",
		"decision",
		"gotcha",
		// Scope steering: the prompt-side twin of the schema description
		// pinned by TestMemorySaveTool_ScopeSteeringPinned — both copies
		// must survive edits or they drift apart.
		"Default to user-scope",
		// Content-quality steering — the fix for "vague, few" memories.
		// The body-echo failure mode and durable-vs-work-log boundary must
		// stay in the prompt or the model drifts back to one-line
		// restatements and task-log junk.
		"What makes a good memory",
		"must ADD substance beyond the one-line description",
		"State durable facts declaratively",
		"stale within days",
	} {
		if !strings.Contains(DefaultSystemPrompt, want) {
			t.Errorf("DefaultSystemPrompt lost proactive-memory steering: missing %q", want)
		}
	}
}

// TestMemorySaveTool_ContentQualityPinned is the deterministic pin for
// the content parameter's substance steering. Like the scope pin, the
// schema description is the strongest lever (read at tool-call time, the
// moment the body is written), so a copy-edit that re-introduces
// "concise" or drops the no-echo rule would silently regress body
// quality back to vague one-line restatements — the exact failure this
// guidance exists to stop.
func TestMemorySaveTool_ContentQualityPinned(t *testing.T) {
	schema := (&MemorySaveTool{}).Schema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties map: %v", schema)
	}
	content, ok := props["content"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no content property: %v", props)
	}
	desc, _ := content["description"].(string)
	for _, want := range []string{
		"Be specific and self-contained",
		"add detail beyond the one-line description",
		"restates the description is worthless",
		"Knowledge, decisions, gotchas, how-it-works notes, and rationale are in scope",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("memory_save content description lost substance steering: missing %q in %q", want, desc)
		}
	}
	if strings.Contains(desc, "concise") {
		t.Errorf("memory_save content description re-introduced \"concise\" — it produces terse, vague echoes: %q", desc)
	}
}

// TestMemorySaveTool_ScopeSteeringPinned is the deterministic pin for
// the scope parameter's default-to-user steering. The schema
// description is the strongest lever on scope choice (it's read at
// tool-call time, where the prompt's scope section is ~80 lines
// upstream); a copy-edit that drops the DEFAULT marker or the
// portability test would silently regress user-scope saving.
func TestMemorySaveTool_ScopeSteeringPinned(t *testing.T) {
	schema := (&MemorySaveTool{}).Schema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties map: %v", schema)
	}
	scope, ok := props["scope"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no scope property: %v", props)
	}
	desc, _ := scope["description"].(string)
	for _, want := range []string{
		"DEFAULT",
		"ONLY for facts meaningless outside this repo",
		"completely different repo",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("memory_save scope description lost default-to-user steering: missing %q in %q", want, desc)
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
//
// wantScope is the ground-truth scope a well-steered model should
// choose. The first two are portable (about the person), the third is
// repo-bound, and the fourth is the trap case this eval exists to
// catch: a portable preference stated mid-repo-work, where repo
// gravity pulls the save toward scope=project.
var proactivityFixtures = []struct {
	name      string
	turn      string
	wantScope string
}{
	{"stated preference", "By the way, I always want Go tests written as table-driven cases with subtests — keep that in mind going forward. Now, what does the acronym CLI stand for? One sentence.", "user"},
	{"correction", "Stop pasting full stack traces into your answers — from now on just summarize the root cause in a sentence. Anyway: what's 17 + 25? Reply with just the number.", "user"},
	{"project fact", "For context you'll need in every future session: this project must build with Go 1.22 and CI runs `make ci`, never plain `go build`. Now name one common HTTP status code for success. One sentence.", "project"},
	{"portable preference amid repo work", "I'm deep in this repo's Makefile today, but here's something that applies to every project we ever work on: never use emojis in anything you write for me. Now, what is 9 × 6? Just the number.", "user"},
}

// evalTrunc clips s for one-line eval logs.
func evalTrunc(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// countSavedMemories walks the agent-managed memory tree under home
// (both scopes live under ~/.yottacode/memory/) and returns the saved
// memory files (MEMORY.md indexes, .archive copies, and subagent
// transcripts excluded).
func countSavedMemories(t *testing.T, home string) []string {
	t.Helper()
	var saved []string
	dir := filepath.Join(home, ".yottacode", "memory")
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
		// Subagent run transcripts nest inside the project memory dir
		// (memory/projects/<slug>/subagents/) and are not memories.
		if strings.Contains(path, string(filepath.Separator)+memory.SubagentsDirName+string(filepath.Separator)) {
			return nil
		}
		saved = append(saved, path)
		return nil
	})
	return saved
}

// savedMemoryRecord is one memory file a fixture turn saved. Scope is
// derived from the on-disk location — under the merged layout the path
// prefix IS the scope (memory/user/ vs memory/projects/<slug>/), so no
// extra plumbing through the tool layer is needed.
type savedMemoryRecord struct {
	Name  string
	Scope string // "user" | "project"
}

// scopeOfSavedPath classifies a saved memory file by its location in
// the memory tree.
func scopeOfSavedPath(path string) string {
	sep := string(filepath.Separator)
	if strings.Contains(path, sep+"memory"+sep+"user"+sep) {
		return "user"
	}
	if strings.Contains(path, sep+"memory"+sep+"projects"+sep) {
		return "project"
	}
	return "unknown"
}

// runProactivityFixture drives one user turn through agent.Turn with
// the REAL shipped steering (identity prompt + cold-start memory
// composition, which ends with the save nudge) and the real memory
// tools rooted in throwaway dirs. Returns a record per memory file the
// turn saved, with the scope it landed in.
func runProactivityFixture(t *testing.T, model, turn string) []savedMemoryRecord {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YOTTACODE_HOME", "")
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
	records := make([]savedMemoryRecord, len(saved))
	for i, p := range saved {
		records[i] = savedMemoryRecord{
			Name:  filepath.Base(p),
			Scope: scopeOfSavedPath(p),
		}
	}
	return records
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
	// Distinct user-truth FIXTURES, not saves: two saves from one turn
	// are a single model decision context, so only multiple independent
	// turns constitute the multi-fluke signal the scope gate wants.
	userTruthFixturesSaved, userTruthFixturesUserScoped := 0, 0
	for _, fx := range proactivityFixtures {
		t.Run(fx.name, func(t *testing.T) {
			saved := runProactivityFixture(t, model, fx.turn)
			totalSaves += len(saved)
			if fx.wantScope == "user" && len(saved) > 0 {
				userTruthFixturesSaved++
				for _, s := range saved {
					if s.Scope == "user" {
						userTruthFixturesUserScoped++
						break
					}
				}
			}
			t.Logf("model=%s fixture=%q wantScope=%s → %d save(s) %v", model, fx.name, fx.wantScope, len(saved), saved)
		})
	}

	// Loose hard gate: a model that handles explicit saves (control
	// passed) and is steered by the shipped prompts should save on at
	// least ONE blatant fixture. Zero across all of them means the
	// proactive steering is not reaching the model — the exact
	// regression this eval exists to catch.
	if totalSaves == 0 {
		t.Errorf("0 proactive saves across %d fixtures (control saved fine) — proactive-memory steering regressed (tool description, save nudge, or prompt section)", len(proactivityFixtures))
	} else {
		t.Logf("proactive saves: %d across %d fixtures", totalSaves, len(proactivityFixtures))
	}

	// Scope gate, same loose philosophy: fail only on the unambiguous
	// signature — saves landed on ≥2 DISTINCT portable-truth fixtures
	// (independent turns, so one flaky turn can't trip it) yet not one
	// fixture produced a scope=user save. That means the default-to-user
	// steering (scope schema description + prompt scope section) is not
	// reaching the model. Anything softer is logged, not failed, so weak
	// local models don't flake CI.
	if userTruthFixturesSaved >= 2 && userTruthFixturesUserScoped == 0 {
		t.Errorf("saves landed on %d user-truth fixtures, none chose scope=user — default-to-user scope steering regressed (memory_save scope description or prompt scope section)", userTruthFixturesSaved)
	} else if userTruthFixturesSaved > 0 {
		t.Logf("scope accuracy on user-truth fixtures: %d/%d fixtures chose scope=user", userTruthFixturesUserScoped, userTruthFixturesSaved)
	}
}
