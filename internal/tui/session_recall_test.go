package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/filerefs"
	"github.com/yottadynamics/yottacode/internal/memory"
	"github.com/yottadynamics/yottacode/internal/recall"
	"github.com/yottadynamics/yottacode/internal/session"
)

func TestRenderTurnStatus_RecalledIndicator(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.turnStart = time.Now()

	// No recall this turn → no "recalled" segment.
	if s := m.renderTurnStatus(); strings.Contains(s, "recalled") {
		t.Errorf("no-recall status should omit indicator: %q", s)
	}

	// Plural.
	m.recalledCount.Store(2)
	if s := m.renderTurnStatus(); !strings.Contains(s, "recalled 2 conversations") {
		t.Errorf("status = %q, want 'recalled 2 conversations'", s)
	}

	// Singular.
	m.recalledCount.Store(1)
	s := m.renderTurnStatus()
	if !strings.Contains(s, "recalled 1 conversation") || strings.Contains(s, "1 conversations") {
		t.Errorf("singular status = %q, want 'recalled 1 conversation'", s)
	}
}

func hit(id, name, snippet string, score float64) recall.ScoredHit {
	return recall.ScoredHit{
		Hit: recall.Hit{
			SessionID:   id,
			SessionName: name,
			Role:        adapter.RoleUser,
			Created:     time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC),
			Snippet:     snippet,
		},
		Score: score,
	}
}

func TestRenderPriorConversations_Empty(t *testing.T) {
	if got := renderPriorConversations(nil, 2000); got != "" {
		t.Errorf("empty hits rendered %q, want empty", got)
	}
}

func TestRenderPriorConversations_FormatsLines(t *testing.T) {
	block := renderPriorConversations([]recall.ScoredHit{
		hit("sess-001", "auth-work", "we decided to use jwt", 0.91),
	}, 2000)
	if !strings.Contains(block, "auth-work") {
		t.Errorf("block missing session name: %q", block)
	}
	if !strings.Contains(block, "2026-07-10") {
		t.Errorf("block missing date: %q", block)
	}
	if !strings.Contains(block, "we decided to use jwt") {
		t.Errorf("block missing excerpt: %q", block)
	}
}

func TestRenderPriorConversations_BudgetKeepsTopHit(t *testing.T) {
	hits := []recall.ScoredHit{
		hit("s1", "first", strings.Repeat("x", 300), 0.9),
		hit("s2", "second", strings.Repeat("y", 300), 0.8),
		hit("s3", "third", strings.Repeat("z", 300), 0.7),
	}
	// A tiny budget must still admit the single top hit, and must drop the rest.
	block := renderPriorConversations(hits, 50)
	if !strings.Contains(block, "first") {
		t.Errorf("top hit dropped under tight budget: %q", block)
	}
	if strings.Contains(block, "second") || strings.Contains(block, "third") {
		t.Errorf("over-budget hits not dropped: %q", block)
	}
}

// The injected block must never contain the multi-line section markers that
// extractSummarySection / extractFileRefsBlock scan for, or a later turn would
// mis-bound the preserved summary and file-refs blocks. Excerpts are collapsed
// to single lines upstream, so the body should carry no blank-line boundaries.
func TestRenderPriorConversations_NoSectionMarkerCollision(t *testing.T) {
	block := renderPriorConversations([]recall.ScoredHit{
		hit("s1", "x", "line one line two still one excerpt", 0.9),
	}, 2000)
	if strings.Contains(block, "\n\n") {
		t.Errorf("block contains a blank-line boundary: %q", block)
	}
	if strings.Contains(block, strings.TrimSpace(summaryHeading)) {
		t.Errorf("block contains summary heading marker: %q", block)
	}
	if strings.Contains(block, filerefs.Marker) {
		t.Errorf("block contains file-refs marker: %q", block)
	}
	if strings.TrimSpace(priorConvosHeading) == strings.TrimSpace(summaryHeading) {
		t.Error("priorConvosHeading collides with summaryHeading")
	}
}

// recallTestVocab drives the fake embedder: a text's vector is the per-keyword
// occurrence count, so texts sharing keywords have a meaningful cosine and
// ranking is reproducible without Ollama.
var recallTestVocab = []string{"auth", "jwt", "docker", "kubernetes"}

// fakeEmbedServer stands in for Ollama's /api/embeddings. The Model holds a
// concrete *memory.EmbedClient rather than an interface, so this is how the tui
// side gets exercised end-to-end; the same client also satisfies
// recall.Embedder, so one server both seeds the index and embeds the query.
// When fail is true every call 500s, for the embed-error degradation path.
func fakeEmbedServer(t *testing.T, fail bool) *memory.EmbedClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var req struct {
			Prompt string `json:"prompt"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		vec := make([]float32, len(recallTestVocab))
		lower := strings.ToLower(req.Prompt)
		for i, w := range recallTestVocab {
			vec[i] = float32(strings.Count(lower, w))
		}
		// Never return an all-zero vector: Embed rejects an empty embedding,
		// and a zero vector would make cosine meaningless anyway.
		if vec[0] == 0 && len(vec) > 0 {
			vec[0] += 0.0001
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": vec})
	}))
	t.Cleanup(srv.Close)
	return &memory.EmbedClient{BaseURL: srv.URL, Model: "test-embed", Timeout: 5 * time.Second}
}

// seedRecall indexes a session at the given cwd and embeds it, so it is a
// candidate for auto-recall in the test model built alongside.
func seedRecall(t *testing.T, idx *recall.Index, ec *memory.EmbedClient, id, cwd, body string) {
	t.Helper()
	s := &session.Session{
		ID: id, Name: id, Model: "test-model", Created: time.Now(), Cwd: cwd,
		Messages: []adapter.Message{{Role: adapter.RoleUser, Content: body}},
	}
	if err := idx.IndexSession(s); err != nil {
		t.Fatalf("IndexSession %s: %v", id, err)
	}
	if err := idx.BackfillVectors(context.Background(), ec, ec.Model); err != nil {
		t.Fatalf("BackfillVectors: %v", err)
	}
}

// rootsOf builds a projectRoots slice from a single root, treating "" as
// "no root resolved" rather than an empty entry.
func rootsOf(root string) []string {
	if root == "" {
		return nil
	}
	return []string{root}
}

// recallTestModel wires a Model with a real on-disk index and the fake embed
// client, mirroring how run.go composes them.
func recallTestModel(t *testing.T, idx *recall.Index, ec *memory.EmbedClient, cwd, root string, sr config.SessionRecallConfig) Model {
	t.Helper()
	m := newTestModel(t)
	m.recall = idx
	m.embedClient = ec
	m.cwd = cwd
	m.projectRoots = rootsOf(root)
	m.fileCfg.Retrieval.SessionRecall = sr
	return m
}

// defaultSR mirrors the shipped defaults so tests exercise real settings.
func defaultSR() config.SessionRecallConfig {
	return config.SessionRecallConfig{Auto: true, Scope: recall.ScopeProject, TopK: 3, MinScore: 0.6, MaxBytes: 2000}
}

func TestPriorConversationsBlock_InjectsRelevantHit(t *testing.T) {
	idx := recall.MustOpenForTest(t)
	ec := fakeEmbedServer(t, false)
	seedRecall(t, idx, ec, "past-auth", "/proj", "we decided to use jwt auth everywhere")

	m := recallTestModel(t, idx, ec, "/proj", "/proj", defaultSR())
	block := m.priorConversationsBlock(context.Background(), "auth jwt")
	if block == "" {
		t.Fatal("expected an injected block for an on-topic query, got none")
	}
	if !strings.Contains(block, "jwt auth") {
		t.Errorf("block missing the recalled excerpt: %q", block)
	}
	if !strings.Contains(block, "past-auth") {
		t.Errorf("block missing the session name: %q", block)
	}
	if got := m.recalledCount.Load(); got != 1 {
		t.Errorf("recalledCount = %d, want 1", got)
	}
}

// Every degradation path must inject nothing *and* leave the turn indicator at
// zero. The counter is pre-seeded so a missing reset shows up as a stale count.
func TestPriorConversationsBlock_BailOuts(t *testing.T) {
	// Seeding always uses a working embedder; only the query-time client is
	// varied, since that is the call priorConversationsBlock actually makes.
	newCase := func(t *testing.T, failQuery bool) Model {
		idx := recall.MustOpenForTest(t)
		ec := fakeEmbedServer(t, false)
		seedRecall(t, idx, ec, "past-auth", "/proj", "we decided to use jwt auth everywhere")
		if failQuery {
			ec = fakeEmbedServer(t, true)
		}
		return recallTestModel(t, idx, ec, "/proj", "/proj", defaultSR())
	}

	cases := map[string]func(t *testing.T) (Model, string){
		"auto_off": func(t *testing.T) (Model, string) {
			m := newCase(t, false)
			m.fileCfg.Retrieval.SessionRecall.Auto = false
			return m, "auth jwt"
		},
		"nil_index": func(t *testing.T) (Model, string) {
			m := newCase(t, false)
			m.recall = nil
			return m, "auth jwt"
		},
		"nil_embed_client": func(t *testing.T) (Model, string) {
			m := newCase(t, false)
			m.embedClient = nil
			return m, "auth jwt"
		},
		"blank_query": func(t *testing.T) (Model, string) {
			m := newCase(t, false)
			return m, " \n\t "
		},
		"embed_error": func(t *testing.T) (Model, string) {
			m := newCase(t, true)
			return m, "auth jwt"
		},
		"nothing_clears_floor": func(t *testing.T) (Model, string) {
			m := newCase(t, false)
			m.fileCfg.Retrieval.SessionRecall.MinScore = 0.99
			return m, "docker kubernetes"
		},
		// top_k = 0 reads as "inject nothing" and must behave that way. It
		// previously fell through to SearchSemantic's default of 10.
		"top_k_zero": func(t *testing.T) (Model, string) {
			m := newCase(t, false)
			m.fileCfg.Retrieval.SessionRecall.TopK = 0
			return m, "auth jwt"
		},
		"only_candidate_is_live_session": func(t *testing.T) (Model, string) {
			m := newCase(t, false)
			m.sess.ID = "past-auth" // the sole indexed session is this one
			return m, "auth jwt"
		},
		"other_project_only": func(t *testing.T) (Model, string) {
			m := newCase(t, false)
			m.cwd, m.projectRoots = "/elsewhere", rootsOf("/elsewhere")
			return m, "auth jwt"
		},
	}

	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			m, query := build(t)
			m.recalledCount.Store(3)
			if got := m.priorConversationsBlock(context.Background(), query); got != "" {
				t.Errorf("expected no injection, got %q", got)
			}
			if got := m.recalledCount.Load(); got != 0 {
				t.Errorf("recalledCount = %d, want 0 (indicator must reset)", got)
			}
		})
	}
}

// Inbound half of the sensitivity gate: a quarantined project injects nothing,
// even though the history is present, on-topic, and well above the floor.
func TestPriorConversationsBlock_SensitiveProjectInjectsNothing(t *testing.T) {
	idx := recall.MustOpenForTest(t)
	ec := fakeEmbedServer(t, false)
	seedRecall(t, idx, ec, "past-auth", "/proj", "we decided to use jwt auth everywhere")

	m := recallTestModel(t, idx, ec, "/proj", "/proj", defaultSR())
	// Same fixture recalls fine when not marked — that is the control.
	if got := m.priorConversationsBlock(context.Background(), "auth jwt"); got == "" {
		t.Fatal("control failed: fixture should recall when not sensitive")
	}

	m.sensitiveProject = true
	m.recalledCount.Store(3)
	if got := m.priorConversationsBlock(context.Background(), "auth jwt"); got != "" {
		t.Errorf("sensitive project injected %q, want nothing", got)
	}
	if got := m.recalledCount.Load(); got != 0 {
		t.Errorf("recalledCount = %d, want 0", got)
	}
}

// Outbound half, end to end through the Model: a sensitive project's sessions
// must not surface here even with scope widened to "all".
func TestPriorConversationsBlock_SensitiveRootsExcludedFromOtherProject(t *testing.T) {
	idx := recall.MustOpenForTest(t)
	ec := fakeEmbedServer(t, false)
	seedRecall(t, idx, ec, "phi-session", "/repo/phi", "we decided to use jwt auth everywhere")
	seedRecall(t, idx, ec, "open-session", "/repo/open", "we decided to use jwt auth everywhere")

	sr := defaultSR()
	sr.Scope = recall.ScopeAll
	m := recallTestModel(t, idx, ec, "/repo/open", "/repo/open", sr)
	m.sensitiveRoots = []string{"/repo/phi"}

	block := m.priorConversationsBlock(context.Background(), "auth jwt")
	if strings.Contains(block, "phi-session") {
		t.Errorf("sensitive project leaked into another project's recall: %q", block)
	}
	if !strings.Contains(block, "open-session") {
		t.Errorf("non-sensitive history should still recall: %q", block)
	}
}

func TestPriorConversationsBlock_SensitiveWorktreeRootsExcludedFromOtherProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	idx := recall.MustOpenForTest(t)
	ec := fakeEmbedServer(t, false)
	phiRoot := filepath.Join(t.TempDir(), "phi")
	sensitiveRoots := sensitiveRecallRoots([]string{phiRoot})
	if len(sensitiveRoots) != 2 {
		t.Fatalf("sensitiveRecallRoots(%q) = %v, want root plus worktree container", phiRoot, sensitiveRoots)
	}
	phiWorktree := filepath.Join(sensitiveRoots[1], "branch-a")
	openRoot := filepath.Join(t.TempDir(), "open")
	seedRecall(t, idx, ec, "phi-worktree-session", phiWorktree, "we decided to use jwt auth everywhere")
	seedRecall(t, idx, ec, "open-session", openRoot, "we decided to use jwt auth everywhere")

	sr := defaultSR()
	sr.Scope = recall.ScopeAll
	m := recallTestModel(t, idx, ec, openRoot, openRoot, sr)
	m.sensitiveRoots = sensitiveRoots

	block := m.priorConversationsBlock(context.Background(), "auth jwt")
	if strings.Contains(block, "phi-worktree-session") {
		t.Errorf("sensitive worktree leaked into another project's recall: %q", block)
	}
	if !strings.Contains(block, "open-session") {
		t.Errorf("non-sensitive history should still recall: %q", block)
	}
}

// The thread-through test: fails if ProjectRoot never reaches the search opts.
func TestPriorConversationsBlock_ProjectScopeMatchesSubdirectory(t *testing.T) {
	idx := recall.MustOpenForTest(t)
	ec := fakeEmbedServer(t, false)
	seedRecall(t, idx, ec, "in-project", "/proj/sub", "we decided to use jwt auth everywhere")
	seedRecall(t, idx, ec, "sibling", "/proj-other", "we decided to use jwt auth everywhere")

	// Running from a different subdirectory of the same repo.
	m := recallTestModel(t, idx, ec, "/proj/other", "/proj", defaultSR())
	block := m.priorConversationsBlock(context.Background(), "auth jwt")
	if !strings.Contains(block, "in-project") {
		t.Errorf("subdirectory session not recalled from a sibling subdirectory: %q", block)
	}
	if strings.Contains(block, "sibling") {
		t.Errorf("prefix-sibling project leaked into project scope: %q", block)
	}
}

// readRecallLog returns the debug log lines written under the test's HOME.
func readRecallLog(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".yottacode", "recall-debug.log"))
	if err != nil {
		t.Fatalf("read recall-debug.log: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestPriorConversationsBlock_DebugLogsNearMisses(t *testing.T) {
	idx := recall.MustOpenForTest(t)
	ec := fakeEmbedServer(t, false)
	seedRecall(t, idx, ec, "on-topic", "/proj", "jwt auth jwt auth")
	seedRecall(t, idx, ec, "near-miss", "/proj", "auth notes plus docker and kubernetes")
	t.Setenv("YOTTACODE_RECALL_DEBUG", "1")

	m := recallTestModel(t, idx, ec, "/proj", "/proj", defaultSR())
	block := m.priorConversationsBlock(context.Background(), "auth jwt")
	if !strings.Contains(block, "on-topic") {
		t.Fatalf("expected the on-topic session injected: %q", block)
	}
	if strings.Contains(block, "near-miss") {
		t.Fatalf("sub-floor session must not be injected: %q", block)
	}

	lines := readRecallLog(t)
	if len(lines) != 1 {
		t.Fatalf("want exactly one log line, got %d: %v", len(lines), lines)
	}
	line := lines[0]
	// Both candidates recorded, only one injected — the near-miss is visible,
	// which is the entire point of the change. The raw query is not logged;
	// only a digest is persisted for correlation.
	for _, want := range []string{"query_sha256=", "candidates=2", "injected=1", "min_score=0.600", "on-topic", "near-miss", "injected]", "dropped]"} {
		if !strings.Contains(line, want) {
			t.Errorf("log line missing %q: %s", want, line)
		}
	}
	if strings.Contains(line, `query="auth jwt"`) || strings.Contains(line, "auth jwt") {
		t.Errorf("log line persisted raw query: %s", line)
	}
}

// Telemetry observes the decision; it must never change it.
func TestPriorConversationsBlock_DebugDoesNotChangeInjection(t *testing.T) {
	build := func(t *testing.T) Model {
		idx := recall.MustOpenForTest(t)
		ec := fakeEmbedServer(t, false)
		seedRecall(t, idx, ec, "on-topic", "/proj", "jwt auth jwt auth")
		seedRecall(t, idx, ec, "near-miss", "/proj", "auth notes plus docker and kubernetes")
		return recallTestModel(t, idx, ec, "/proj", "/proj", defaultSR())
	}

	off := build(t)
	blockOff := off.priorConversationsBlock(context.Background(), "auth jwt")
	countOff := off.recalledCount.Load()

	t.Setenv("YOTTACODE_RECALL_DEBUG", "1")
	on := build(t)
	blockOn := on.priorConversationsBlock(context.Background(), "auth jwt")
	countOn := on.recalledCount.Load()

	if blockOff != blockOn {
		t.Errorf("debug changed the injected block:\n off=%q\n  on=%q", blockOff, blockOn)
	}
	if countOff != countOn {
		t.Errorf("debug changed recalledCount: off=%d on=%d", countOff, countOn)
	}
}

func TestPriorConversationsBlock_DebugLogsZeroHits(t *testing.T) {
	idx := recall.MustOpenForTest(t)
	ec := fakeEmbedServer(t, false)
	seedRecall(t, idx, ec, "on-topic", "/proj", "jwt auth")
	t.Setenv("YOTTACODE_RECALL_DEBUG", "1")

	m := recallTestModel(t, idx, ec, "/proj", "/proj", defaultSR())
	// Off-topic query: the one candidate scores near zero, well under the 0.6
	// floor, so nothing is injected.
	if got := m.priorConversationsBlock(context.Background(), "docker kubernetes"); got != "" {
		t.Fatalf("off-topic query should inject nothing, got %q", got)
	}
	// A turn that injects nothing is exactly the turn worth tuning against, so
	// it must still be logged.
	lines := readRecallLog(t)
	if len(lines) != 1 || !strings.Contains(lines[0], "injected=0") {
		t.Errorf("zero-hit turn not logged with injected=0: %v", lines)
	}
}

func TestLogRecallCandidates_NoFileWhenEnvUnset(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YOTTACODE_RECALL_DEBUG", "")
	logRecallCandidates("auth jwt", []recall.ScoredHit{hit("s1", "n", "x", 0.9)}, 1, 0.6)
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".yottacode", "recall-debug.log")); !os.IsNotExist(err) {
		t.Errorf("telemetry wrote a file with the env var unset (err=%v)", err)
	}
}

func TestApplyRecallGate(t *testing.T) {
	hits := []recall.ScoredHit{
		hit("a", "a", "x", 0.90),
		hit("b", "b", "x", 0.70),
		hit("c", "c", "x", 0.55),
		hit("d", "d", "x", 0.10),
	}
	ids := func(in []recall.ScoredHit) []string {
		out := make([]string, len(in))
		for i, h := range in {
			out[i] = h.SessionID
		}
		return out
	}

	for _, tc := range []struct {
		name     string
		hits     []recall.ScoredHit
		minScore float64
		topK     int
		want     []string
	}{
		{"floor drops the tail", hits, 0.6, 10, []string{"a", "b"}},
		{"cap keeps the head", hits, 0.0, 2, []string{"a", "b"}},
		{"floor then cap", hits, 0.5, 2, []string{"a", "b"}},
		{"nothing clears floor", hits, 0.99, 10, nil},
		// top_k = 0 means none. It used to mean ten (SearchSemantic's
		// library default for an unspecified limit), so setting the knob to
		// 0 injected MORE than the default of 3.
		{"zero topK means none", hits, 0.0, 0, nil},
		{"negative topK means none", hits, 0.0, -1, nil},
		{"empty input", nil, 0.6, 3, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ids(applyRecallGate(tc.hits, tc.minScore, tc.topK)); !slices.Equal(got, tc.want) {
				t.Errorf("applyRecallGate = %v, want %v", got, tc.want)
			}
		})
	}
}
