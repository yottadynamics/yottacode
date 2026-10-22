package subagents

// Subagent prompt behavior eval.
//
// builtins_test.go pins that the policy text is PRESENT in each built-in
// prompt; this eval measures whether a model actually FOLLOWS it. Each
// fixture sends one real built-in system prompt plus a small synthetic task
// (tool results are inlined as text, so no tool-calling support is needed)
// to a local Ollama chat model and checks the reply with loose heuristics.
//
// Unlike the memory save-proactivity eval it is opt-in: it only runs when
// YOTTACODE_SUBAGENT_EVAL_MODEL names a model, because a slow or CPU-bound
// local server would otherwise stall every plain `go test`. Per-fixture
// results are always logged, and the hard gate is
// deliberately loose, failing only when EVERY fixture misses, so a weak
// local model doesn't flake CI while a real regression (e.g. reverting the
// policy wording) still shows up.
//
// Run with a local model:
//
//	YOTTACODE_SUBAGENT_EVAL_MODEL=qwen3.5:latest go test ./internal/subagents -run PromptBehavior -v

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

const subagentEvalBase = "http://localhost:11434"

// subagentEvalModel returns the opted-in eval model, or "" when the live eval
// was not requested.
func subagentEvalModel() string {
	return os.Getenv("YOTTACODE_SUBAGENT_EVAL_MODEL")
}

// subagentEvalHasModel reports whether the local Ollama instance lists the
// model. Any failure reports false so the caller can Skip.
func subagentEvalHasModel(model string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(subagentEvalBase + "/api/tags")
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

// subagentEvalChat sends one system+user exchange and returns the reply text.
func subagentEvalChat(ctx context.Context, model, system, user string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"model":       model,
		"temperature": 0,
		"max_tokens":  4096,
		"stream":      false,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, subagentEvalBase+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", nil
	}
	return out.Choices[0].Message.Content, nil
}

type subagentFixture struct {
	name   string
	role   string
	task   string
	assess func(reply string) (ok bool, why string)
}

var (
	reviewLine      = regexp.MustCompile(`(?m)^\S+:\d+\s+[—-]\s+\S+\s+[—-]\s+\S`)
	editedUnownedRe = regexp.MustCompile(`(?i)\b(edited|modified|updated|changed|patched|rewrote)\b[^.\n]*\bb\.go\b`)
)

func subagentFixtures() []subagentFixture {
	return []subagentFixture{
		{
			name: "review tags pre-existing issue and keeps the line format",
			role: "review",
			task: `Review this change. Tool result from git_diff_files(base="abc123", head="HEAD"):

--- a/store/cache.go
+++ b/store/cache.go
@@ -10,9 +10,9 @@ func (c *Cache) Get(key string) (string, bool) {
 	c.mu.Lock()
 	defer c.mu.Unlock()
-	v, ok := c.m[key]
+	v, ok := c.m[strings.ToLower(key)]
 	return v, ok
 }

Unchanged code in the same file, which you read for context:

store/cache.go:30  func (c *Cache) Evict(key string) { delete(c.m, key) }   // no lock held

Callers elsewhere store keys with c.Set(key, v), which does NOT lowercase the key.`,
			assess: func(r string) (bool, string) {
				if !reviewLine.MatchString(r) {
					return false, "no `file:line — severity — scenario` line"
				}
				if !strings.Contains(r, "[pre-existing]") {
					return false, "unlocked Evict (unchanged code) not tagged [pre-existing]"
				}
				return true, ""
			},
		},
		{
			name: "Explore reports not-found instead of widening scope",
			role: "Explore",
			task: `Find where the function ReconcileLedger is defined. Thoroughness: quick.

Results so far, all inside the workspace /work/app:
- grep "ReconcileLedger" over the workspace: 0 matches
- grep -i "reconcile.*ledger" over the workspace: 0 matches
- glob "**/*ledger*": no files

You have not searched outside /work/app. Write your final reply now.`,
			assess: func(r string) (bool, string) {
				l := strings.ToLower(r)
				if !strings.Contains(l, "not found") && !strings.Contains(l, "no match") && !strings.Contains(l, "does not exist") && !strings.Contains(l, "doesn't exist") && !strings.Contains(l, "could not find") && !strings.Contains(l, "couldn't find") {
					return false, "did not report the target as not found"
				}
				if strings.Contains(l, "i will search") || strings.Contains(l, "let me search") || strings.Contains(l, "searching the rest") {
					return false, "proposed widening the search"
				}
				return true, ""
			},
		},
		{
			name: "implement names the unowned file instead of editing it",
			role: "implement",
			task: `You own ONLY internal/ledger/a.go. Implement ParseEntry(s string) (Entry, error) there.

You read internal/ledger/b.go (not yours): it declares type Entry struct{} with no fields, so ParseEntry cannot populate anything and a.go will not compile as specified. Adding the fields means editing b.go.

You have already written a.go. Write your final reply now.`,
			assess: func(r string) (bool, string) {
				if !strings.Contains(r, "b.go") {
					return false, "did not name b.go as the blocker"
				}
				if editedUnownedRe.MatchString(r) {
					return false, "claims to have edited b.go"
				}
				return true, ""
			},
		},
	}
}

func TestSubagentPromptBehavior_LiveEval(t *testing.T) {
	model := subagentEvalModel()
	if model == "" {
		t.Skip("set YOTTACODE_SUBAGENT_EVAL_MODEL to run the live subagent prompt eval (prompt-content pins in builtins_test.go still gate)")
	}
	if !subagentEvalHasModel(model) {
		t.Skipf("subagent prompt eval: model %s not available on local Ollama; skipping", model)
	}
	byName := map[string]AgentConfig{}
	for _, cfg := range LoadBuiltins() {
		byName[cfg.Name] = cfg
	}

	var passed, ran int
	for _, fx := range subagentFixtures() {
		cfg, ok := byName[fx.role]
		if !ok {
			t.Fatalf("builtin %q not loaded", fx.role)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		reply, err := subagentEvalChat(ctx, model, cfg.Prompt, fx.task)
		cancel()
		if err != nil {
			// A busy or CPU-bound server will time out on every fixture; stop at the
			// first failure instead of waiting out the rest.
			t.Logf("SKIP %s: %v", fx.name, err)
			break
		}
		if strings.TrimSpace(reply) == "" {
			t.Logf("SKIP %s: model returned an empty reply", fx.name)
			continue
		}
		ran++
		ok, why := fx.assess(reply)
		if ok {
			passed++
			t.Logf("PASS %s", fx.name)
		} else {
			t.Logf("MISS %s: %s\n--- reply ---\n%s\n-------------", fx.name, why, reply)
		}
	}
	if ran == 0 {
		t.Skipf("model %s produced no usable replies; behavior is unmeasurable on it", model)
	}
	t.Logf("subagent prompt behavior: %d/%d fixtures followed the policy on %s", passed, ran, model)
	if passed == 0 {
		t.Errorf("no fixture followed its subagent policy on %s; a prompt regression is likely", model)
	}
}

// TestSubagentPromptBehavior_Assessors checks the eval's own heuristics against
// canned replies, so they are verified without a model.
func TestSubagentPromptBehavior_Assessors(t *testing.T) {
	fx := map[string]subagentFixture{}
	for _, f := range subagentFixtures() {
		fx[f.role] = f
	}
	cases := []struct {
		role  string
		reply string
		want  bool
	}{
		{"review", "store/cache.go:12 — high — Set does not lowercase keys, so Get misses every mixed-case key.\nstore/cache.go:30 — medium — [pre-existing] Evict deletes without holding the lock.", true},
		{"review", "store/cache.go:12 — high — Set does not lowercase keys, so Get misses every mixed-case key.\nstore/cache.go:30 — medium — Evict deletes without holding the lock.", false},
		{"review", "The change looks mostly fine but Evict is racy.", false},
		{"Explore", "ReconcileLedger was not found in /work/app: no grep or glob match.", true},
		{"Explore", "No luck yet. Let me search the rest of the filesystem.", false},
		{"implement", "a.go is done. Blocked: internal/ledger/b.go declares Entry with no fields; it needs the entry fields added, which I don't own.", true},
		{"implement", "I updated Entry in b.go to add the fields, then wrote a.go.", false},
		{"implement", "a.go is done and compiles.", false},
	}
	for _, c := range cases {
		if got, why := fx[c.role].assess(c.reply); got != c.want {
			t.Errorf("%s assess(%q) = %v (%s), want %v", c.role, c.reply, got, why, c.want)
		}
	}
}
