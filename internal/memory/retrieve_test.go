package memory

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/config"
)

func entry(name, body string) MemoryEntry {
	return MemoryEntry{Name: name, Body: body, Type: "reference", Scope: "user"}
}

// keywordCfg returns a config that forces the legacy keyword strategy.
func keywordCfg(topK int, minScore float64) config.RetrievalConfig {
	return config.RetrievalConfig{Enabled: true, TopK: topK, MinScore: minScore, Strategy: "keyword"}
}

// bm25Cfg returns a config that forces the BM25 strategy.
func bm25Cfg(topK int, minScore float64) config.RetrievalConfig {
	return config.RetrievalConfig{Enabled: true, TopK: topK, MinScore: minScore, Strategy: "bm25"}
}

// --- Legacy keyword scorer tests (strategy="keyword") ---

func TestScore_HeadlineHitOutweighsBodyHit(t *testing.T) {
	a := entry("ripgrep", "User prefers ripgrep over GNU grep.")
	b := entry("tooling", "Author favors ripgrep when searching.")
	q := "should I use ripgrep here"

	sa := Score(a, q)
	sb := Score(b, q)
	if sa <= sb {
		t.Errorf("headline-match score should beat body-match score: a=%.3f b=%.3f", sa, sb)
	}
}

func TestScore_NoOverlapZero(t *testing.T) {
	e := entry("kubernetes", "Cluster lives in europe-west1.")
	if got := Score(e, "go testing patterns"); got != 0 {
		t.Errorf("zero overlap should score 0; got %.3f", got)
	}
}

func TestScore_EmptyQueryZero(t *testing.T) {
	e := entry("anything", "anything")
	if got := Score(e, ""); got != 0 {
		t.Errorf("empty query should score 0; got %.3f", got)
	}
	if got := Score(e, "    "); got != 0 {
		t.Errorf("whitespace-only query should score 0; got %.3f", got)
	}
}

func TestScore_StopwordsAndShortTokensIgnored(t *testing.T) {
	e := entry("code", "the and for go")
	if got := Score(e, "the and you of it"); got != 0 {
		t.Errorf("stopword-only query should score 0; got %.3f", got)
	}
}

func TestScore_CaseFolded(t *testing.T) {
	e := MemoryEntry{Name: "Tooling", Body: "User prefers RipGrep.", Type: "reference"}
	if got := Score(e, "should I use ripgrep"); got == 0 {
		t.Errorf("case-folded match expected, got 0")
	}
}

func TestScore_NormalizedAtMostOne(t *testing.T) {
	e := entry("ripgrep", "ripgrep ripgrep ripgrep")
	q := "ripgrep ripgrep ripgrep ripgrep ripgrep"
	if got := Score(e, q); got < 0 || got > 1 {
		t.Errorf("score should be in [0,1]; got %.3f", got)
	}
}

// --- Select tests (keyword strategy) ---

func TestSelect_DisabledReturnsAll(t *testing.T) {
	in := []MemoryEntry{entry("z", "alpha"), entry("a", "beta")}
	cfg := config.RetrievalConfig{Enabled: false, TopK: 1, Strategy: "keyword"}
	out := Select(in, "alpha", cfg)
	if len(out) != 2 {
		t.Errorf("disabled retrieval should return all entries; got %d", len(out))
	}
}

func TestSelect_TopKBound(t *testing.T) {
	in := []MemoryEntry{
		entry("a", "needle here"),
		entry("b", "needle here"),
		entry("c", "needle here"),
		entry("d", "no match"),
	}
	cfg := keywordCfg(2, 0)
	out := Select(in, "needle", cfg)
	if len(out) != 2 {
		t.Errorf("top_k=2 should bound result; got %d", len(out))
	}
}

func TestSelect_MinScoreFloor(t *testing.T) {
	in := []MemoryEntry{
		entry("match", "needle"),
		entry("weak", "tangentially"),
	}
	cfg := keywordCfg(10, 0.1)
	out := Select(in, "needle search", cfg)
	if len(out) != 1 || out[0].Name != "match" {
		t.Errorf("min_score floor should drop the zero-scorer; got %+v", out)
	}
}

func TestSelect_DeterministicTieBreak(t *testing.T) {
	in := []MemoryEntry{
		entry("charlie", "needle"),
		entry("alpha", "needle"),
		entry("bravo", "needle"),
	}
	cfg := keywordCfg(3, 0)
	out := Select(in, "needle", cfg)
	got := []string{out[0].Name, out[1].Name, out[2].Name}
	want := []string{"alpha", "bravo", "charlie"}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("tie-break order = %v, want %v", got, want)
			break
		}
	}
}

func TestSelect_EmptyQueryReturnsAlphabeticalTopK(t *testing.T) {
	in := []MemoryEntry{
		entry("delta", "x"),
		entry("alpha", "x"),
		entry("charlie", "x"),
		entry("bravo", "x"),
	}
	cfg := keywordCfg(2, 0)
	out := Select(in, "", cfg)
	if len(out) != 2 {
		t.Fatalf("expected 2 entries with empty query, got %d", len(out))
	}
	if out[0].Name != "alpha" || out[1].Name != "bravo" {
		t.Errorf("expected alphabetical fallback; got %s, %s", out[0].Name, out[1].Name)
	}
}

func TestSelect_TopKZeroNoBound(t *testing.T) {
	in := []MemoryEntry{
		entry("a", "needle"),
		entry("b", "needle"),
		entry("c", "needle"),
	}
	cfg := keywordCfg(0, 0)
	out := Select(in, "needle", cfg)
	if len(out) != 3 {
		t.Errorf("top_k=0 should mean unlimited; got %d", len(out))
	}
}

func TestSelect_EmptyIsNoop(t *testing.T) {
	cfg := keywordCfg(5, 0)
	if got := Select(nil, "anything", cfg); got != nil {
		t.Errorf("empty input should return nil; got %+v", got)
	}
}

// --- SystemPromptFor tests ---

func TestSystemPromptFor_AlwaysIncludesTrustAnchors(t *testing.T) {
	l := Loaded{
		UserText:    "be terse",
		ProjectText: "this is a Go project",
		ProjectMemories: []MemoryEntry{
			{Scope: "project", Name: "kubernetes", Type: "reference", Body: "Cluster details"},
		},
	}
	cfg := config.RetrievalConfig{Enabled: true, TopK: 0, MinScore: 0.99, Strategy: "keyword"}
	got := SystemPromptFor("base", l, "totally unrelated query", cfg)
	if !strings.Contains(got, "be terse") {
		t.Errorf("USER section dropped: %q", got)
	}
	if !strings.Contains(got, "Go project") {
		t.Errorf("YOTTACODE section dropped: %q", got)
	}
	if strings.Contains(got, "Cluster details") {
		t.Errorf("project memory body should be filtered by min_score; got: %q", got)
	}
}

func TestSystemPromptFor_RespectsTopKOnMemoriesOnly(t *testing.T) {
	l := Loaded{
		ProjectMemories: []MemoryEntry{
			{Scope: "project", Name: "ripgrep", Type: "reference", Body: "User prefers ripgrep over GNU grep."},
			{Scope: "project", Name: "tabs", Type: "user", Body: "User prefers tabs."},
			{Scope: "project", Name: "kubernetes", Type: "reference", Body: "Cluster lives in europe-west1."},
		},
	}
	cfg := keywordCfg(1, 0)
	got := SystemPromptFor("base", l, "I want to use ripgrep", cfg)
	if !strings.Contains(got, "ripgrep") {
		t.Errorf("relevant entry should be included: %q", got)
	}
	if strings.Contains(got, "Cluster lives in") {
		t.Errorf("irrelevant entry body should be filtered out: %q", got)
	}
}

func TestSystemPromptFor_DisabledMatchesSystemPrompt(t *testing.T) {
	l := Loaded{
		UserText: "u",
		UserMemories: []MemoryEntry{
			{Scope: "user", Name: "a", Type: "user", Body: "alpha"},
			{Scope: "user", Name: "b", Type: "user", Body: "beta"},
		},
	}
	cfg := config.RetrievalConfig{Enabled: false, Strategy: "keyword"}
	want := SystemPrompt("base", l)
	got := SystemPromptFor("base", l, "anything", cfg)
	if got != want {
		t.Errorf("disabled retrieval should equal SystemPrompt:\n got %q\nwant %q", got, want)
	}
}

func TestSelect_SymmetricDifferentQuery(t *testing.T) {
	in := []MemoryEntry{
		entry("ripgrep", "ripgrep is the search tool"),
		entry("kubernetes", "cluster runs in europe-west1"),
	}
	cfg := keywordCfg(1, 0)

	rg := Select(in, "do you have ripgrep installed", cfg)
	k8s := Select(in, "redeploy the cluster", cfg)

	if len(rg) != 1 || rg[0].Name != "ripgrep" {
		t.Errorf("ripgrep query should pick ripgrep; got %+v", rg)
	}
	if len(k8s) != 1 || k8s[0].Name != "kubernetes" {
		t.Errorf("cluster query should pick kubernetes; got %+v", k8s)
	}
}

func TestSelectAcrossScopes_SharedBudget(t *testing.T) {
	user := []MemoryEntry{
		{Scope: "user", Name: "u-prefs", Type: "user", Body: "prefer table-driven tests"},
	}
	project := []MemoryEntry{
		{Scope: "project", Name: "p-tests", Type: "project", Body: "tests live in test/"},
		{Scope: "project", Name: "p-other", Type: "reference", Body: "kubernetes notes"},
	}
	cfg := keywordCfg(2, 0)
	gotUser, gotProject := selectAcrossScopes(user, project, "tests", cfg, nil)
	if len(gotUser) != 1 || gotUser[0].Name != "u-prefs" {
		t.Errorf("user-scope test entry should win one of the two slots; got %+v", gotUser)
	}
	if len(gotProject) != 1 || gotProject[0].Name != "p-tests" {
		t.Errorf("project-scope test entry should win one slot; got %+v", gotProject)
	}
}

// --- BM25 strategy integration tests ---

func TestSelect_BM25_StemmedRecall(t *testing.T) {
	in := []MemoryEntry{
		entry("test-prefs", "prefer integration tests over mocks"),
		entry("unrelated", "kubernetes cluster configuration"),
	}
	cfg := bm25Cfg(1, 0)
	// "testing" stems to "test", should match "tests" in the entry
	out := Select(in, "testing patterns", cfg)
	if len(out) != 1 || out[0].Name != "test-prefs" {
		t.Errorf("BM25 should match via stemming; got %+v", out)
	}
}

func TestSelect_BM25_SynonymRecall(t *testing.T) {
	in := []MemoryEntry{
		entry("test-prefs", "prefer integration tests over mocks"),
		entry("unrelated", "kubernetes cluster configuration"),
	}
	cfg := bm25Cfg(1, 0)
	// "fakes" stems to "fake", which is a synonym of "mock"
	out := Select(in, "should I use fakes in tests", cfg)
	if len(out) != 1 || out[0].Name != "test-prefs" {
		t.Errorf("BM25 should match via synonyms (fakes→mock); got %+v", out)
	}
}

func TestSelect_BM25_DatabaseSynonym(t *testing.T) {
	in := []MemoryEntry{
		entry("db-config", "database connection settings and configuration"),
		entry("unrelated", "user interface styling preferences"),
	}
	cfg := bm25Cfg(1, 0)
	out := Select(in, "how do I configure the db", cfg)
	if len(out) != 1 || out[0].Name != "db-config" {
		t.Errorf("BM25 should match db↔database synonym; got %+v", out)
	}
}

func TestSelect_BM25_DeploySynonym(t *testing.T) {
	in := []MemoryEntry{
		entry("release-process", "release commits stay manual"),
		entry("unrelated", "user interface styling preferences"),
	}
	cfg := bm25Cfg(1, 0)
	out := Select(in, "how do we deploy", cfg)
	if len(out) != 1 || out[0].Name != "release-process" {
		t.Errorf("BM25 should match deploy↔release synonym; got %+v", out)
	}
}

func TestSelect_BM25_TopKBound(t *testing.T) {
	in := []MemoryEntry{
		entry("a", "needle here"),
		entry("b", "needle here"),
		entry("c", "needle here"),
	}
	cfg := bm25Cfg(2, 0)
	out := Select(in, "needle", cfg)
	if len(out) != 2 {
		t.Errorf("BM25 top_k=2 should bound result; got %d", len(out))
	}
}

func TestSelect_BM25_EmptyIsNoop(t *testing.T) {
	cfg := bm25Cfg(5, 0)
	if got := Select(nil, "anything", cfg); got != nil {
		t.Errorf("empty input should return nil; got %+v", got)
	}
}

func TestSelect_BM25_DisabledReturnsAll(t *testing.T) {
	in := []MemoryEntry{entry("z", "alpha"), entry("a", "beta")}
	cfg := config.RetrievalConfig{Enabled: false, Strategy: "bm25"}
	out := Select(in, "alpha", cfg)
	if len(out) != 2 {
		t.Errorf("disabled retrieval should return all entries; got %d", len(out))
	}
}

func TestResolveStrategy(t *testing.T) {
	cases := []struct {
		strategy string
		hasEmbed bool
		want     string
	}{
		{"", false, "bm25"},
		{"", true, "bm25"},
		{"keyword", false, "keyword"},
		{"keyword", true, "keyword"},
		{"bm25", false, "bm25"},
		{"bm25", true, "bm25"},
		{"semantic", false, "bm25"},
		{"semantic", true, "semantic"},
		{"auto", false, "bm25"},
		{"auto", true, "semantic"},
	}
	for _, tc := range cases {
		var client *EmbedClient
		if tc.hasEmbed {
			client = &EmbedClient{BaseURL: "http://localhost:11434", Model: "test"}
		}
		got := resolveStrategy(tc.strategy, client)
		if got != tc.want {
			t.Errorf("resolveStrategy(%q, hasEmbed=%v) = %q, want %q",
				tc.strategy, tc.hasEmbed, got, tc.want)
		}
	}
}

// --- #2: BM25 score normalization ---

func TestSelect_BM25_ScoresNormalizedToUnitRange(t *testing.T) {
	in := []MemoryEntry{
		entry("strong", "needle needle needle search index ranking"),
		entry("weak", "needle once"),
		entry("none", "kubernetes cluster configuration"),
	}
	cfg := bm25Cfg(0, 0)
	scored := SelectWithEmbeddingsScored(in, "needle search", cfg, nil)
	if len(scored) == 0 {
		t.Fatal("expected scored results")
	}
	for _, s := range scored {
		if s.Score < 0 || s.Score > 1 {
			t.Errorf("BM25 score out of [0,1]: %s=%.4f", s.Entry.Name, s.Score)
		}
	}
	// The top match is normalized to exactly 1.0; a weaker match scores
	// strictly between 0 and the top.
	if scored[0].Score != 1.0 {
		t.Errorf("top BM25 score should normalize to 1.0; got %.4f", scored[0].Score)
	}
}

func TestSelect_BM25_MinScoreFiltersOnNormalizedScale(t *testing.T) {
	in := []MemoryEntry{
		entry("strong", "needle needle needle search ranking index lookup"),
		entry("weak", "needle mentioned once among unrelated words here"),
	}
	// On the normalized scale the weak entry sits well below 0.9 while
	// the top entry is pinned at 1.0, so a high floor keeps only the top.
	cfg := bm25Cfg(0, 0.9)
	out := Select(in, "needle search ranking", cfg)
	if len(out) != 1 || out[0].Name != "strong" {
		t.Errorf("min_score=0.9 on normalized BM25 should keep only the top match; got %+v", out)
	}
}

// --- #3: byte-bounded injection ---

func TestSelect_MaxBytesCapsCombinedBodies(t *testing.T) {
	body := strings.Repeat("needle ", 100) // ~700 bytes each
	in := []MemoryEntry{
		entry("a", body),
		entry("b", body),
		entry("c", body),
		entry("d", body),
	}
	// Budget fits ~2 bodies. TopK is generous so the byte cap is the
	// binding constraint.
	cfg := config.RetrievalConfig{Enabled: true, TopK: 10, Strategy: "keyword", MaxBytes: 1500}
	out := Select(in, "needle", cfg)
	if len(out) != 2 {
		t.Fatalf("max_bytes=1500 with ~700-byte bodies should admit 2 entries; got %d", len(out))
	}
	total := 0
	for _, e := range out {
		total += len(e.Body)
	}
	if total > cfg.MaxBytes {
		t.Errorf("combined bodies %d exceed max_bytes %d", total, cfg.MaxBytes)
	}
}

func TestSelect_MaxBytesAlwaysAdmitsTopEntry(t *testing.T) {
	huge := strings.Repeat("needle ", 1000) // ~7000 bytes, alone over budget
	in := []MemoryEntry{
		// "needle" in the name gives this entry a headline match, so it
		// ranks #1 ahead of the body-only match below — deterministically
		// making the oversized entry the top-ranked one.
		entry("needle-primary", huge),
		entry("other", "needle"),
	}
	cfg := config.RetrievalConfig{Enabled: true, TopK: 10, Strategy: "keyword", MaxBytes: 1000}
	out := Select(in, "needle", cfg)
	// The top-ranked entry is admitted even though it alone exceeds the
	// budget; the next entry is then dropped because the budget is blown.
	if len(out) != 1 || out[0].Name != "needle-primary" {
		t.Fatalf("top entry must always be admitted despite exceeding budget; got %+v", out)
	}
}

func TestSelect_MaxBytesZeroIsUnlimited(t *testing.T) {
	body := strings.Repeat("needle ", 100)
	in := []MemoryEntry{entry("a", body), entry("b", body), entry("c", body)}
	cfg := config.RetrievalConfig{Enabled: true, TopK: 10, Strategy: "keyword", MaxBytes: 0}
	out := Select(in, "needle", cfg)
	if len(out) != 3 {
		t.Errorf("max_bytes=0 should impose no byte limit; got %d", len(out))
	}
}

// --- Synonym down-weighting (exact-term precedence) ---

func TestSelect_BM25_SynonymsDownweightedVsExact(t *testing.T) {
	// "exact-runs" uses the searched term "test" repeatedly; "syn-soup"
	// uses two distinct synonyms of test (spec, assert) once each. Under
	// equal weight the synonym pile-up outranks the exact term — the
	// down-weight must restore exact-term precedence.
	in := []MemoryEntry{
		entry("exact-runs", "test test test"),
		entry("syn-soup", "spec assert"),
	}
	resetCorpusCache()
	out := Select(in, "test", bm25Cfg(0, 0))
	if len(out) < 2 || out[0].Name != "exact-runs" {
		t.Fatalf("down-weighted synonyms should rank the exact-term doc first; got %s, %s",
			out[0].Name, out[len(out)-1].Name)
	}

	// Equal-weight (the public Rank, used by CLI/TUI preview) ranks the
	// synonym doc first — confirming the down-weight is what changed the
	// agent's ranking, not the corpus stats.
	eq := BuildCorpus(in).Rank(StemExpandTokenize("test"))
	if eq[0].Entry.Name != "syn-soup" {
		t.Errorf("equal-weight should rank the synonym pile-up first (the bug); got %s", eq[0].Entry.Name)
	}
}

// --- Project-shadows-user scope precedence ---

func TestShadowUserByProject(t *testing.T) {
	user := []MemoryEntry{{Name: "a", Scope: "user"}, {Name: "dup", Scope: "user"}}
	project := []MemoryEntry{{Name: "dup", Scope: "project"}, {Name: "c", Scope: "project"}}
	out := shadowUserByProject(user, project)
	if len(out) != 1 || out[0].Name != "a" {
		t.Errorf("project should shadow user 'dup'; got %+v", out)
	}
	if len(shadowUserByProject(user, nil)) != 2 {
		t.Error("no project memories should leave the user set untouched")
	}
}

func TestSystemPrompt_ProjectShadowsUser(t *testing.T) {
	l := Loaded{
		UserMemories: []MemoryEntry{
			{Scope: "user", Name: "dup", Type: "feedback", Body: "USER-BODY"},
			{Scope: "user", Name: "only-user", Type: "user", Body: "UONLY"},
		},
		ProjectMemories: []MemoryEntry{
			{Scope: "project", Name: "dup", Type: "project", Body: "PROJECT-BODY"},
		},
	}
	out := SystemPrompt("base", l)
	if strings.Contains(out, "USER-BODY") {
		t.Error("user-scope 'dup' body should be shadowed by the project twin")
	}
	if !strings.Contains(out, "PROJECT-BODY") {
		t.Error("project-scope 'dup' should inject")
	}
	if !strings.Contains(out, "UONLY") {
		t.Error("a non-shadowed user memory should still inject")
	}
}

func TestSelectAcrossScopes_ProjectShadowsUser(t *testing.T) {
	user := []MemoryEntry{{Scope: "user", Name: "dup", Body: "u"}, {Scope: "user", Name: "u-only", Body: "x"}}
	project := []MemoryEntry{{Scope: "project", Name: "dup", Body: "p"}}
	gotUser, _ := selectAcrossScopes(user, project, "dup", keywordCfg(10, 0), nil)
	for _, e := range gotUser {
		if e.Name == "dup" {
			t.Error("user 'dup' should be shadowed out by the project twin before ranking")
		}
	}
}

// --- Score normalization consistency across strategies ---

func TestSelect_Keyword_ScoresNormalizedToUnitRange(t *testing.T) {
	in := []MemoryEntry{
		entry("strong", "needle needle needle search index lookup"),
		entry("weak", "needle once"),
		entry("none", "kubernetes cluster configuration"),
	}
	cfg := keywordCfg(0, 0)
	scored := SelectWithEmbeddingsScored(in, "needle search", cfg, nil)
	if len(scored) == 0 {
		t.Fatal("expected scored results")
	}
	for _, s := range scored {
		if s.Score < 0 || s.Score > 1 {
			t.Errorf("keyword score out of [0,1]: %s=%.4f", s.Entry.Name, s.Score)
		}
	}
	if scored[0].Score != 1.0 {
		t.Errorf("top keyword score should normalize to 1.0; got %.4f", scored[0].Score)
	}
}

// TestScoreSemantic_BlendIsLive is the hermetic test for the BM25+cosine
// blend (the live-Ollama integration test silently exercises BM25 fallback
// because it writes model-less sidecars). A fake embedder returns a fixed
// query vector; entries get .vec sidecars written with WriteVecWithModel
// matching the client model. It asserts: (1) an entry with ZERO BM25 but
// high cosine still scores > 0 (so the cosine term is genuinely
// contributing, not fallback), (2) a sidecar from a DIFFERENT model is
// ignored (scores 0), and (3) the top blended score normalizes to 1.0.
func TestScoreSemantic_BlendIsLive(t *testing.T) {
	resetCorpusCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The query embeds to [1,0,0]; entry vectors below are set
		// relative to it.
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"embedding":[1,0,0]}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	mk := func(name, body string, vec []float32, model string) MemoryEntry {
		p := filepath.Join(dir, name+".md")
		if err := WriteVecWithModel(VecPath(p), vec, model); err != nil {
			t.Fatal(err)
		}
		return MemoryEntry{Scope: "user", Name: name, Type: "reference", Body: body, Path: p}
	}
	in := []MemoryEntry{
		// Strong BM25 ("needle"), cosine 0 (orthogonal vector).
		mk("lexical", "needle needle needle", []float32{0, 1, 0}, "test-model"),
		// Zero BM25 for "needle", cosine 1 (same vector as the query).
		mk("conceptual", "zzz qqq www", []float32{1, 0, 0}, "test-model"),
		// Zero BM25, cosine WOULD be 1 but the sidecar's model differs → ignored.
		mk("stale-model", "zzz qqq www", []float32{1, 0, 0}, "other-model"),
	}

	client := &EmbedClient{BaseURL: srv.URL, Model: "test-model", Timeout: 5 * time.Second}
	cfg := config.RetrievalConfig{Enabled: true, TopK: 0, MinScore: 0, Strategy: "semantic", SemanticWeight: 0.4}
	scored := SelectWithEmbeddingsScored(in, "needle", cfg, client)

	byName := map[string]float64{}
	for _, s := range scored {
		byName[s.Entry.Name] = s.Score
	}
	// (1) cosine genuinely contributes: conceptual has no BM25 match yet
	// scores > 0 purely from the cosine term.
	if byName["conceptual"] <= 0 {
		t.Errorf("conceptual (zero BM25, high cosine) should score > 0 via the blend; got %.4f — blend may not be live", byName["conceptual"])
	}
	// (2) model-mismatch sidecar is ignored → BM25-only → 0.
	if byName["stale-model"] != 0 {
		t.Errorf("stale-model sidecar (different model) should contribute no cosine; got %.4f", byName["stale-model"])
	}
	// (3) top normalizes to 1.0.
	if scored[0].Score != 1.0 {
		t.Errorf("top blended score should normalize to 1.0; got %.4f (top=%s)", scored[0].Score, scored[0].Entry.Name)
	}
}

// TestScoreSemantic_WeightTunable verifies retrieval.semantic_weight
// actually shifts the BM25↔cosine balance: weight 0 → pure BM25 (the
// zero-BM25 / high-cosine entry scores 0), weight 1 → pure cosine (the
// zero-cosine / high-BM25 entry scores 0). Same hermetic fake embedder as
// the blend test (query embeds to [1,0,0]).
func TestScoreSemantic_WeightTunable(t *testing.T) {
	resetCorpusCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"embedding":[1,0,0]}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	mk := func(name, body string, vec []float32) MemoryEntry {
		p := filepath.Join(dir, name+".md")
		if err := WriteVecWithModel(VecPath(p), vec, "test-model"); err != nil {
			t.Fatal(err)
		}
		return MemoryEntry{Scope: "user", Name: name, Type: "reference", Body: body, Path: p}
	}
	in := []MemoryEntry{
		mk("lexical", "needle needle needle", []float32{0, 1, 0}), // strong BM25, cosine 0
		mk("conceptual", "zzz qqq www", []float32{1, 0, 0}),       // zero BM25, cosine 1
	}
	client := &EmbedClient{BaseURL: srv.URL, Model: "test-model", Timeout: 5 * time.Second}

	score := func(weight float64, name string) float64 {
		cfg := config.RetrievalConfig{Enabled: true, TopK: 0, MinScore: 0, Strategy: "semantic", SemanticWeight: weight}
		for _, s := range SelectWithEmbeddingsScored(in, "needle", cfg, client) {
			if s.Entry.Name == name {
				return s.Score
			}
		}
		return -1
	}

	// weight 0 = pure BM25: the cosine-only entry contributes nothing.
	if got := score(0, "conceptual"); got != 0 {
		t.Errorf("semantic_weight=0 should be pure BM25; conceptual (zero BM25) scored %.4f, want 0", got)
	}
	if got := score(0, "lexical"); got != 1.0 {
		t.Errorf("semantic_weight=0: BM25-strong entry should normalize to 1.0; got %.4f", got)
	}
	// weight 1 = pure cosine: the BM25-only entry (cosine 0) contributes nothing.
	if got := score(1, "lexical"); got != 0 {
		t.Errorf("semantic_weight=1 should be pure cosine; lexical (cosine 0) scored %.4f, want 0", got)
	}
	if got := score(1, "conceptual"); got != 1.0 {
		t.Errorf("semantic_weight=1: cosine-strong entry should normalize to 1.0; got %.4f", got)
	}
}

// TestScoreSemantic_HangingEmbedderBoundedByTimeout proves the fix for
// the per-turn-embed freeze: a hanging Ollama must not block retrieval
// for longer than the client timeout; it falls back to (normalized) BM25.
func TestScoreSemantic_HangingEmbedderBoundedByTimeout(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-done // never answers within the client timeout
	}))
	// Cleanup is LIFO: close(done) runs before srv.Close(), so the
	// blocked handler returns and Close() doesn't stall the test.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(done) })

	client := &EmbedClient{BaseURL: srv.URL, Model: "x", Timeout: 150 * time.Millisecond}
	in := []MemoryEntry{
		entry("strong", "needle needle search index lookup"),
		entry("weak", "needle once"),
	}
	cfg := config.RetrievalConfig{Enabled: true, TopK: 0, MinScore: 0, Strategy: "semantic"}

	start := time.Now()
	scored := SelectWithEmbeddingsScored(in, "needle search", cfg, client)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("semantic retrieval blocked %v on a hanging embedder — must bound at the client timeout and fall back to BM25", elapsed)
	}
	if len(scored) == 0 || scored[0].Score != 1.0 {
		t.Errorf("expected normalized BM25 fallback ranking; got %+v", scored)
	}
}

// TestSelect_Semantic_FallbackNormalized: when the embedder errors (here,
// an unreachable endpoint), scoreSemantic falls back to BM25 — which must
// still be normalized to top=1.0 so min_score behaves identically whether
// or not Ollama is reachable.
func TestSelect_Semantic_FallbackNormalized(t *testing.T) {
	in := []MemoryEntry{
		entry("strong", "needle needle needle search index lookup"),
		entry("weak", "needle once"),
	}
	cfg := config.RetrievalConfig{Enabled: true, TopK: 0, MinScore: 0, Strategy: "semantic"}
	dead := &EmbedClient{BaseURL: "http://127.0.0.1:1", Model: "x", Timeout: 200 * time.Millisecond}
	scored := SelectWithEmbeddingsScored(in, "needle search", cfg, dead)
	if len(scored) == 0 {
		t.Fatal("expected scored results")
	}
	if scored[0].Score != 1.0 {
		t.Errorf("semantic fallback should be BM25-normalized (top=1.0); got %.4f", scored[0].Score)
	}
}

// --- #1: semantic retrieval rejects vectors from a different model ---

func TestEntryCosine_SkipsModelMismatch(t *testing.T) {
	dir := t.TempDir()
	vec := []float32{1, 0, 0}
	query := []float32{1, 0, 0} // identical → cosine 1.0 when trusted

	match := filepath.Join(dir, "match.vec")
	if err := WriteVecWithModel(match, vec, "model-a"); err != nil {
		t.Fatalf("write match vec: %v", err)
	}
	if got := entryCosine(query, match, "model-a"); got <= 0.99 {
		t.Errorf("same-model vec should contribute full cosine; got %.4f", got)
	}
	if got := entryCosine(query, match, "model-b"); got != 0 {
		t.Errorf("different-model vec must contribute 0 cosine; got %.4f", got)
	}

	// A legacy sidecar (raw float32, no model header) records no model,
	// so it must not be trusted against the current model either.
	legacy := filepath.Join(dir, "legacy.vec")
	if err := WriteVec(legacy, vec); err != nil {
		t.Fatalf("write legacy vec: %v", err)
	}
	if got := entryCosine(query, legacy, "model-a"); got != 0 {
		t.Errorf("legacy (model-less) vec must contribute 0 cosine; got %.4f", got)
	}

	// A missing sidecar yields 0, not an error.
	if got := entryCosine(query, filepath.Join(dir, "absent.vec"), "model-a"); got != 0 {
		t.Errorf("missing vec should contribute 0 cosine; got %.4f", got)
	}
}

func TestSelect_BM25_DefaultStrategyIsBM25(t *testing.T) {
	in := []MemoryEntry{
		entry("test-prefs", "prefer integration tests over mocks"),
		entry("unrelated", "kubernetes cluster configuration"),
	}
	// Empty strategy should default to bm25 and thus support stemming
	cfg := config.RetrievalConfig{Enabled: true, TopK: 1, Strategy: ""}
	out := Select(in, "testing patterns", cfg)
	if len(out) != 1 || out[0].Name != "test-prefs" {
		t.Errorf("default strategy should behave like BM25; got %+v", out)
	}
}
