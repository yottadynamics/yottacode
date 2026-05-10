package memory

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/config"
)

func entry(name, body string) MemoryEntry {
	return MemoryEntry{Name: name, Body: body, Type: "reference", Scope: "user"}
}

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

func TestSelect_DisabledReturnsAll(t *testing.T) {
	in := []MemoryEntry{entry("z", "alpha"), entry("a", "beta")}
	cfg := config.RetrievalConfig{Enabled: false, TopK: 1}
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
	cfg := config.RetrievalConfig{Enabled: true, TopK: 2}
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
	cfg := config.RetrievalConfig{Enabled: true, TopK: 10, MinScore: 0.1}
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
	cfg := config.RetrievalConfig{Enabled: true, TopK: 3}
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
	cfg := config.RetrievalConfig{Enabled: true, TopK: 2}
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
	cfg := config.RetrievalConfig{Enabled: true, TopK: 0}
	out := Select(in, "needle", cfg)
	if len(out) != 3 {
		t.Errorf("top_k=0 should mean unlimited; got %d", len(out))
	}
}

func TestSelect_EmptyIsNoop(t *testing.T) {
	cfg := config.RetrievalConfig{Enabled: true, TopK: 5}
	if got := Select(nil, "anything", cfg); got != nil {
		t.Errorf("empty input should return nil; got %+v", got)
	}
}

func TestSystemPromptFor_AlwaysIncludesTrustAnchors(t *testing.T) {
	l := Loaded{
		UserText:    "be terse",
		ProjectText: "this is a Go project",
		ProjectMemories: []MemoryEntry{
			{Scope: "project", Name: "kubernetes", Type: "reference", Body: "Cluster details"},
		},
	}
	cfg := config.RetrievalConfig{Enabled: true, TopK: 0, MinScore: 0.99}
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
	cfg := config.RetrievalConfig{Enabled: true, TopK: 1}
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
	cfg := config.RetrievalConfig{Enabled: false}
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
	cfg := config.RetrievalConfig{Enabled: true, TopK: 1}

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
	cfg := config.RetrievalConfig{Enabled: true, TopK: 2}
	gotUser, gotProject := selectAcrossScopes(user, project, "tests", cfg)
	if len(gotUser) != 1 || gotUser[0].Name != "u-prefs" {
		t.Errorf("user-scope test entry should win one of the two slots; got %+v", gotUser)
	}
	if len(gotProject) != 1 || gotProject[0].Name != "p-tests" {
		t.Errorf("project-scope test entry should win one slot; got %+v", gotProject)
	}
}
