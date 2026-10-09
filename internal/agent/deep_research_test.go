package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/subagents"
	"github.com/yottadynamics/yottacode/internal/syncutil"
)

func TestDecodeReplyJSON_ToleratesFenceAndChatter(t *testing.T) {
	var p struct {
		Questions []string `json:"questions"`
	}
	reply := "Sure!\n```json\n{\"questions\": [\"a\", \"b\"]}\n```\nHope that helps."
	if err := decodeReplyJSON(reply, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Questions) != 2 {
		t.Fatalf("got %v", p.Questions)
	}
	if err := decodeReplyJSON("no object here", &p); err == nil {
		t.Error("expected error for reply without JSON")
	}
	if err := decodeReplyJSON(`{"questions": [`, &p); err == nil {
		t.Error("expected error for truncated JSON")
	}
}

func TestClampBreadth(t *testing.T) {
	for in, want := range map[int]int{0: 4, 1: 2, 2: 2, 5: 5, 6: 6, 99: 6, -3: 2} {
		if got := clampBreadth(in); got != want {
			t.Errorf("clampBreadth(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestParsePlanReply_CapsDedupesAndRejectsEmpty(t *testing.T) {
	got, err := parsePlanReply(`{"questions":["One"," one ","","Two","Three"]}`, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "One" || got[1] != "Two" {
		t.Errorf("got %v, want [One Two]", got)
	}
	if _, err := parsePlanReply(`{"questions":["  "]}`, 4); err == nil {
		t.Error("blank-only plan must be rejected")
	}
}

func TestAcceptClaim(t *testing.T) {
	ok := researchClaim{Claim: " c ", Evidence: "e", SourceTitle: "T\nitle", SourceLocator: "https://x", SourceType: "PRIMARY", Confidence: "bogus"}
	got, accepted := acceptClaim(ok)
	if !accepted {
		t.Fatal("complete claim rejected")
	}
	if got.Claim != "c" || got.SourceTitle != "T itle" || got.SourceType != "primary" || got.Confidence != "low" {
		t.Errorf("not normalized: %+v", got)
	}
	for _, mutate := range []func(*researchClaim){
		func(c *researchClaim) { c.Claim = "" },
		func(c *researchClaim) { c.Evidence = " " },
		func(c *researchClaim) { c.SourceTitle = "" },
		func(c *researchClaim) { c.SourceLocator = "" },
	} {
		c := ok
		mutate(&c)
		if _, accepted := acceptClaim(c); accepted {
			t.Errorf("incomplete claim accepted: %+v", c)
		}
	}
}

func TestShardClaims_RoundRobin(t *testing.T) {
	claims := make([]researchClaim, 5)
	for i := range claims {
		claims[i].ID = fmt.Sprintf("claim-%d", i)
	}
	shards := shardClaims(claims, 2)
	if len(shards) != 2 || len(shards[0]) != 3 || len(shards[1]) != 2 {
		t.Fatalf("bad shard sizes: %v", shards)
	}
	if shards[0][1].ID != "claim-2" || shards[1][0].ID != "claim-1" {
		t.Errorf("not round-robin: %v", shards)
	}
	if got := shardClaims(claims[:1], 2); len(got) != 1 {
		t.Errorf("shard count must not exceed claim count, got %d", len(got))
	}
	if shardClaims(nil, 2) != nil {
		t.Error("no claims → no shards")
	}
}

func TestValidateVerdictReply(t *testing.T) {
	ids := []string{"claim-0", "claim-1"}
	good := `{"verdicts":[{"claim_id":"claim-0","supported":true},{"claim_id":"claim-1","supported":false}]}`
	if _, err := validateVerdictReply(good, ids); err != nil {
		t.Fatalf("good reply rejected: %v", err)
	}
	for name, reply := range map[string]string{
		"missing":   `{"verdicts":[{"claim_id":"claim-0"}]}`,
		"duplicate": `{"verdicts":[{"claim_id":"claim-0"},{"claim_id":"claim-0"}]}`,
		"foreign":   `{"verdicts":[{"claim_id":"claim-0"},{"claim_id":"claim-9"}]}`,
		"garbage":   `nope`,
	} {
		if _, err := validateVerdictReply(reply, ids); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

func TestAcceptClaim_RejectsControlOnlyFields(t *testing.T) {
	base := researchClaim{Claim: "claim", Evidence: "evidence", SourceTitle: "title", SourceLocator: "https://example.test"}
	for _, field := range []string{"claim", "evidence", "title", "locator"} {
		c := base
		switch field {
		case "claim":
			c.Claim = "\x1b\x07"
		case "evidence":
			c.Evidence = "\x1b\x07"
		case "title":
			c.SourceTitle = "\x1b\x07"
		case "locator":
			c.SourceLocator = "\x1b\x07"
		}
		if _, ok := acceptClaim(c); ok {
			t.Errorf("control-only %s was accepted", field)
		}
	}
}

func TestAcceptVerdict_RequiresIndependentEvidence(t *testing.T) {
	c := researchClaim{ID: "claim-0", Claim: "x"}
	if _, ok := acceptVerdict(c, rawVerdict{Supported: false, Evidence: "e", SourceTitle: "t", SourceLocator: "l"}); ok {
		t.Error("unsupported verdict accepted")
	}
	if _, ok := acceptVerdict(c, rawVerdict{Supported: true, Evidence: "e", SourceTitle: "t"}); ok {
		t.Error("supported verdict without locator accepted")
	}
	if v, ok := acceptVerdict(c, rawVerdict{Supported: true, Evidence: "e", SourceTitle: "t", SourceLocator: "l"}); !ok || v.VerifierSourceLocator != "l" {
		t.Errorf("complete verdict rejected: %+v", v)
	}
}

func TestValidateReportBody(t *testing.T) {
	if err := validateReportBody("Answer. [S1] More. [S2]", 2); err != nil {
		t.Errorf("valid body rejected: %v", err)
	}
	cases := map[string]string{
		"unused marker":  "Only one. [S1]",
		"invented":       "A [S1] B [S2] C [S3]",
		"own sources":    "A [S1] B [S2]\n\n## Sources\n- x",
		"own references": "A [S1] B [S2]\n### References\n- x",
	}
	for name, body := range cases {
		if err := validateReportBody(body, 2); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

func TestExtractReportBody(t *testing.T) {
	if b, ok := extractReportBody("noise <report-body>\nhi [S1]\n</report-body> tail"); !ok || b != "hi [S1]" {
		t.Errorf("got %q, %v", b, ok)
	}
	for _, in := range []string{"no tags", "<report-body>unterminated", "<report-body>  </report-body>"} {
		if _, ok := extractReportBody(in); ok {
			t.Errorf("%q should not extract", in)
		}
	}
}

func TestRenderSources_MergesSharedSources(t *testing.T) {
	mk := func(loc string) verifiedClaim {
		return verifiedClaim{
			researchClaim:         researchClaim{SourceTitle: "Doc", SourceLocator: loc},
			VerifierSourceTitle:   "Doc",
			VerifierSourceLocator: loc,
		}
	}
	out := renderSources([]verifiedClaim{mk("https://a"), mk("https://b"), mk("https://a")})
	if !strings.Contains(out, "[S1] [S3] Doc — https://a") {
		t.Errorf("shared source not merged:\n%s", out)
	}
	if strings.Count(out, "https://a") != 1 {
		t.Errorf("source listed twice:\n%s", out)
	}
	v := mk("https://a")
	v.VerifierSourceLocator = "https://other"
	if out := renderSources([]verifiedClaim{v}); !strings.Contains(out, "independently checked against Doc — https://other") {
		t.Errorf("missing cross-check note:\n%s", out)
	}
}

func TestResearchSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Bootc and chunking for patching!": "bootc-and-chunking-for-patching",
		"   ":                              "report",
		"../../etc/passwd":                 "etc-passwd",
	} {
		if got := researchSlug(in); got != want {
			t.Errorf("researchSlug(%q) = %q, want %q", in, got, want)
		}
	}
	if got := researchSlug(strings.Repeat("a", 200)); len(got) > 50 {
		t.Errorf("slug too long: %d", len(got))
	}
}

func TestWriteResearchReport_NeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	p1, err := writeResearchReport(dir, "x", "one")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := writeResearchReport(dir, "x", "two")
	if err != nil {
		t.Fatal(err)
	}
	if p1 == p2 || filepath.Base(p2) != "deep-research-x-2.md" {
		t.Errorf("second write should get a suffix: %s, %s", p1, p2)
	}
	if b, _ := os.ReadFile(p1); string(b) != "one" {
		t.Errorf("first report was overwritten: %q", b)
	}
}

// fakeResearch is a scripted sub-agent runner. Each agent type has a
// handler; calls are counted per type.
type fakeResearch struct {
	mu       syncutil.Mutex
	calls    map[string]int
	handlers map[string]func(call int, prompt string) (string, error)
}

func (f *fakeResearch) run(_ context.Context, agentType, prompt string) (string, error) {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[agentType]++
	n := f.calls[agentType]
	h := f.handlers[agentType]
	f.mu.Unlock()
	if h == nil {
		return "", fmt.Errorf("no handler for %s", agentType)
	}
	return h(n, prompt)
}

// verifyAll answers a verifier prompt with "supported" for every claim in
// its packet.
func verifyAll(_ int, prompt string) (string, error) {
	_, rest, _ := strings.Cut(prompt, "<candidate-claims-json>\n")
	packet, _, _ := strings.Cut(rest, "\n</candidate-claims-json>")
	var claims []researchClaim
	if err := json.Unmarshal([]byte(packet), &claims); err != nil {
		return "", err
	}
	var vs []rawVerdict
	for _, c := range claims {
		vs = append(vs, rawVerdict{ClaimID: c.ID, Supported: true, Reason: "ok", Evidence: "seen", SourceTitle: "Check " + c.ID, SourceLocator: "https://check/" + c.ID})
	}
	b, _ := json.Marshal(map[string]any{"verdicts": vs})
	return string(b), nil
}

func researchReply(n int) string {
	var cs []map[string]string
	for i := range n {
		cs = append(cs, map[string]string{
			"claim": fmt.Sprintf("fact %d", i), "evidence": "quote", "source_title": "Src",
			"source_locator": fmt.Sprintf("https://src/%d", i), "source_type": "primary", "confidence": "high",
		})
	}
	b, _ := json.Marshal(map[string]any{"claims": cs, "uncertainties": []string{}})
	return string(b)
}

func newTestResearchTool(f *fakeResearch, dir string) *DeepResearchTool {
	cwd := NewCwdRef(dir)
	return &DeepResearchTool{Cwd: cwd, run: f.run}
}

func TestDeepResearch_HappyPath(t *testing.T) {
	dir := t.TempDir()
	f := &fakeResearch{handlers: map[string]func(int, string) (string, error){
		"research-planner":  func(int, string) (string, error) { return `{"questions":["q1","q2","q3"]}`, nil },
		"researcher":        func(int, string) (string, error) { return researchReply(2), nil },
		"research-verifier": verifyAll,
		"research-synthesizer": func(int, string) (string, error) {
			return "<report-body>Answer [S1] [S2] [S3] [S4] [S5] [S6]</report-body>", nil
		},
	}}
	tool := newTestResearchTool(f, dir)
	out, err := tool.Execute(context.Background(), `{"query":"How does X work?"}`)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls["researcher"] != 3 || f.calls["research-verifier"] != 2 {
		t.Errorf("fan-out: researchers=%d verifiers=%d, want 3 and 2", f.calls["researcher"], f.calls["research-verifier"])
	}
	path := filepath.Join(dir, "deep-research-how-does-x-work.md")
	if !strings.Contains(out, path) {
		t.Errorf("result does not name the report file:\n%s", out)
	}
	if strings.Contains(out, "Partial") {
		t.Errorf("clean run marked partial:\n%s", out)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("report not written: %v", err)
	}
	for _, want := range []string{"**Status: Verified**", "Answer [S1]", "## Sources", "[S6]", "## Coverage and uncertainty"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("report missing %q:\n%s", want, b)
		}
	}
}

func TestDeepResearch_BreadthCapsResearchers(t *testing.T) {
	f := &fakeResearch{handlers: map[string]func(int, string) (string, error){
		"research-planner": func(int, string) (string, error) {
			return `{"questions":["a","b","c","d","e","f","g","h"]}`, nil
		},
		"researcher":           func(int, string) (string, error) { return researchReply(1), nil },
		"research-verifier":    verifyAll,
		"research-synthesizer": func(int, string) (string, error) { return "", errors.New("down") },
	}}
	tool := newTestResearchTool(f, t.TempDir())
	if _, err := tool.Execute(context.Background(), `{"query":"q","breadth":3}`); err != nil {
		t.Fatal(err)
	}
	if f.calls["researcher"] != 3 {
		t.Errorf("breadth=3 ran %d researchers", f.calls["researcher"])
	}
	f2 := &fakeResearch{handlers: f.handlers}
	if _, err := newTestResearchTool(f2, t.TempDir()).Execute(context.Background(), `{"query":"q"}`); err != nil {
		t.Fatal(err)
	}
	if f2.calls["researcher"] != 4 {
		t.Errorf("default breadth ran %d researchers, want 4", f2.calls["researcher"])
	}
}

func TestDeepResearch_RetriesMalformedResearcherJSON(t *testing.T) {
	var prompts []string
	var mu syncutil.Mutex
	f := &fakeResearch{handlers: map[string]func(int, string) (string, error){
		"research-planner": func(int, string) (string, error) { return `{"questions":["only"]}`, nil },
		"researcher": func(n int, p string) (string, error) {
			mu.Lock()
			prompts = append(prompts, p)
			mu.Unlock()
			if n == 1 {
				return "I found some things, here they are in prose.", nil
			}
			return researchReply(1), nil
		},
		"research-verifier":    verifyAll,
		"research-synthesizer": func(int, string) (string, error) { return "<report-body>A [S1]</report-body>", nil },
	}}
	out, err := newTestResearchTool(f, t.TempDir()).Execute(context.Background(), `{"query":"q"}`)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls["researcher"] != 2 {
		t.Errorf("researcher calls = %d, want 2 (one retry)", f.calls["researcher"])
	}
	if !strings.Contains(prompts[1], "previous reply was rejected") {
		t.Errorf("retry prompt does not explain the rejection:\n%s", prompts[1])
	}
	if strings.Contains(out, "Partial") {
		t.Errorf("recovered run should not be partial:\n%s", out)
	}
}

func TestDeepResearch_PlannerFailureFallsBackToOriginalQuery(t *testing.T) {
	var gotPrompt string
	f := &fakeResearch{handlers: map[string]func(int, string) (string, error){
		"research-planner": func(int, string) (string, error) { return "", errors.New("boom") },
		"researcher": func(_ int, p string) (string, error) {
			gotPrompt = p
			return researchReply(1), nil
		},
		"research-verifier":    verifyAll,
		"research-synthesizer": func(int, string) (string, error) { return "<report-body>A [S1]</report-body>", nil },
	}}
	out, err := newTestResearchTool(f, t.TempDir()).Execute(context.Background(), `{"query":"the original"}`)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls["researcher"] != 1 || !strings.Contains(gotPrompt, "the original") {
		t.Errorf("expected one researcher on the original query, got %d / %q", f.calls["researcher"], gotPrompt)
	}
	if !strings.Contains(out, "Partial") {
		t.Errorf("planner failure must mark the run partial:\n%s", out)
	}
}

func TestDeepResearch_BadSynthesisFallsBackToFindingList(t *testing.T) {
	dir := t.TempDir()
	f := &fakeResearch{handlers: map[string]func(int, string) (string, error){
		"research-planner":  func(int, string) (string, error) { return `{"questions":["q"]}`, nil },
		"researcher":        func(int, string) (string, error) { return researchReply(2), nil },
		"research-verifier": verifyAll,
		"research-synthesizer": func(int, string) (string, error) {
			return "<report-body>Invented [S9] and forgot the rest</report-body>", nil
		},
	}}
	out, err := newTestResearchTool(f, dir).Execute(context.Background(), `{"query":"q"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "## Findings") || strings.Contains(out, "[S9]") {
		t.Errorf("invalid synthesis must be replaced by the deterministic list:\n%s", out)
	}
	if !strings.Contains(out, "Partial") {
		t.Errorf("synthesis fallback must mark the run partial:\n%s", out)
	}
}

func TestDeepResearch_VerifierShardFailureExcludesItsClaims(t *testing.T) {
	var verifierCalls int
	var mu syncutil.Mutex
	f := &fakeResearch{handlers: map[string]func(int, string) (string, error){
		"research-planner": func(int, string) (string, error) { return `{"questions":["q"]}`, nil },
		"researcher":       func(int, string) (string, error) { return researchReply(4), nil },
		"research-verifier": func(n int, p string) (string, error) {
			mu.Lock()
			verifierCalls++
			mu.Unlock()
			// Shard holding claim-0 never produces valid output.
			if strings.Contains(p, `"id":"claim-0"`) {
				return "garbage", nil
			}
			return verifyAll(n, p)
		},
		"research-synthesizer": func(int, string) (string, error) {
			return "<report-body>A [S1] [S2]</report-body>", nil
		},
	}}
	dir := t.TempDir()
	out, err := newTestResearchTool(f, dir).Execute(context.Background(), `{"query":"q"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Partial") {
		t.Errorf("lost shard must mark the run partial:\n%s", out)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "deep-research-q.md"))
	if !strings.Contains(string(b), "Verifier shard 1 failed") {
		t.Errorf("coverage notes should name the failed shard:\n%s", b)
	}
	if strings.Contains(string(b), "[S3]") {
		t.Errorf("only the 2 claims from the healthy shard may be cited:\n%s", b)
	}
}

func TestDeepResearch_NoVerifiedClaimsYieldsPartialReportWithoutSynthesis(t *testing.T) {
	f := &fakeResearch{handlers: map[string]func(int, string) (string, error){
		"research-planner": func(int, string) (string, error) { return `{"questions":["q"]}`, nil },
		"researcher":       func(int, string) (string, error) { return researchReply(2), nil },
		"research-verifier": func(int, string) (string, error) {
			return `{"verdicts":[]}`, nil // wrong count → rejected
		},
	}}
	dir := t.TempDir()
	out, err := newTestResearchTool(f, dir).Execute(context.Background(), `{"query":"q"}`)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls["research-synthesizer"] != 0 {
		t.Error("synthesizer must not run with zero verified claims")
	}
	if !strings.Contains(out, "None of the candidate claims survived") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

func TestDeepResearch_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeResearch{handlers: map[string]func(int, string) (string, error){
		"research-planner": func(int, string) (string, error) {
			cancel()
			return `{"questions":["q"]}`, nil
		},
	}}
	if _, err := newTestResearchTool(f, t.TempDir()).Execute(ctx, `{"query":"q"}`); err == nil {
		t.Error("cancelled run must return an error")
	}
}

func TestDeepResearch_ArgValidation(t *testing.T) {
	tool := newTestResearchTool(&fakeResearch{}, t.TempDir())
	if _, err := tool.Execute(context.Background(), `{"query":"  "}`); err == nil {
		t.Error("blank query must be rejected")
	}
	if _, err := tool.Execute(context.Background(), `not json`); err == nil {
		t.Error("invalid JSON args must be rejected")
	}
	if tool.RequiresApproval("") || tool.ParallelSafe("") {
		t.Error("deep_research needs no approval and must not run inside a parallel batch")
	}
}

// The example JSON in each builtin agent definition must expose exactly the
// property paths of the schema the tool injects and the validators decode.
func TestResearchAgentExamplesMatchSchemas(t *testing.T) {
	prompts := map[string]string{}
	for _, c := range subagents.LoadBuiltins() {
		prompts[c.Name] = c.Prompt
	}
	cases := map[string]map[string]any{
		"research-planner":  researchPlanSchema,
		"researcher":        researchResultSchema,
		"research-verifier": researchVerdictsSchema,
	}
	for agentName, schema := range cases {
		_, after, ok := strings.Cut(prompts[agentName], "Reply with ONE JSON object")
		if !ok {
			t.Fatalf("%s: prompt has no JSON reply section", agentName)
		}
		var example any
		if err := decodeReplyJSON(after, &example); err != nil {
			t.Fatalf("%s: example JSON does not parse: %v", agentName, err)
		}
		want := schemaPaths(schema, "")
		got := examplePaths(example, "")
		sort.Strings(want)
		sort.Strings(got)
		if !slices.Equal(want, got) {
			t.Errorf("%s: example paths differ from schema\n schema:  %v\n example: %v", agentName, want, got)
		}
	}
}

func TestSchemaPromptIsInjectedIntoEveryJSONPrompt(t *testing.T) {
	var mu syncutil.Mutex
	seen := map[string]string{}
	rec := func(name string, reply func() string) func(int, string) (string, error) {
		return func(n int, p string) (string, error) {
			mu.Lock()
			seen[name] = p
			mu.Unlock()
			if name == "research-verifier" {
				return verifyAll(n, p)
			}
			return reply(), nil
		}
	}
	f := &fakeResearch{handlers: map[string]func(int, string) (string, error){
		"research-planner":     rec("research-planner", func() string { return `{"questions":["q"]}` }),
		"researcher":           rec("researcher", func() string { return researchReply(1) }),
		"research-verifier":    rec("research-verifier", nil),
		"research-synthesizer": func(int, string) (string, error) { return "<report-body>A [S1]</report-body>", nil },
	}}
	if _, err := newTestResearchTool(f, t.TempDir()).Execute(context.Background(), `{"query":"q"}`); err != nil {
		t.Fatal(err)
	}
	for name, marker := range map[string]string{
		"research-planner": `"questions"`, "researcher": `"source_locator"`, "research-verifier": `"claim_id"`,
	} {
		if p := seen[name]; !strings.Contains(p, "<json-schema>") || !strings.Contains(p, marker) {
			t.Errorf("%s prompt lacks the injected schema (%s):\n%s", name, marker, p)
		}
	}
}

func TestClipBoundsUntrustedFields(t *testing.T) {
	long := strings.Repeat("é", 5000)
	c, ok := acceptClaim(researchClaim{Claim: long, Evidence: long, SourceTitle: long, SourceLocator: long})
	if !ok {
		t.Fatal("claim rejected")
	}
	if n := len([]rune(c.Claim)); n > clipClaim+1 {
		t.Errorf("claim not clipped: %d runes", n)
	}
	if n := len([]rune(c.Evidence)); n > clipEvidence+1 {
		t.Errorf("evidence not clipped: %d runes", n)
	}
	if got := clip("short", 10); got != "short" {
		t.Errorf("clip altered a short string: %q", got)
	}
}

func TestWorkflowPhaseLine(t *testing.T) {
	e := WorkflowPhase{Workflow: "deep-research", Phases: deepResearchPhases, Current: 1, Detail: "4 question(s)"}
	want := "deep-research: Plan ✓ · Research ● · Verify ○ · Report ○ — 4 question(s)"
	if got := e.Line(); got != want {
		t.Errorf("Line() = %q, want %q", got, want)
	}
	e.Current, e.Detail = len(deepResearchPhases), ""
	if got := e.Line(); got != "deep-research: Plan ✓ · Research ✓ · Verify ✓ · Report ✓" {
		t.Errorf("finished line = %q", got)
	}
}

func TestDeepResearch_EmitsPhaseEventsInOrder(t *testing.T) {
	f := &fakeResearch{handlers: map[string]func(int, string) (string, error){
		"research-planner":     func(int, string) (string, error) { return `{"questions":["q"]}`, nil },
		"researcher":           func(int, string) (string, error) { return researchReply(1), nil },
		"research-verifier":    verifyAll,
		"research-synthesizer": func(int, string) (string, error) { return "<report-body>A [S1]</report-body>", nil },
	}}
	ch := make(chan Event, 64)
	ctx := WithParentEvents(context.Background(), ch)
	if _, err := newTestResearchTool(f, t.TempDir()).Execute(ctx, `{"query":"q"}`); err != nil {
		t.Fatal(err)
	}
	close(ch)
	var current []int
	for ev := range ch {
		if p, ok := ev.(WorkflowPhase); ok {
			current = append(current, p.Current)
		}
	}
	if !slices.Equal(current, []int{0, 1, 2, 3, 4}) {
		t.Errorf("phase sequence = %v, want [0 1 2 3 4]", current)
	}
}

// newBackgroundResearchTool builds a tool that runs detached, as in the
// interactive TUI, and returns the channel the completion callback feeds.
func newBackgroundResearchTool(t *testing.T, f *fakeResearch, dir string) (*DeepResearchTool, *subagents.Registry, <-chan SubagentBackgroundDone) {
	t.Helper()
	reg := subagents.NewRegistry()
	agentTool := &AgentTool{AllowBackground: true, Tasks: reg, TranscriptDir: t.TempDir()}
	done := make(chan SubagentBackgroundDone, 4)
	agentTool.SetBackgroundDoneCallback(func(ev SubagentBackgroundDone) { done <- ev })
	return &DeepResearchTool{Agent: agentTool, Cwd: NewCwdRef(dir), run: f.run}, reg, done
}

func happyHandlers() map[string]func(int, string) (string, error) {
	return map[string]func(int, string) (string, error){
		"research-planner":     func(int, string) (string, error) { return `{"questions":["a","b"]}`, nil },
		"researcher":           func(int, string) (string, error) { return researchReply(1), nil },
		"research-verifier":    verifyAll,
		"research-synthesizer": func(int, string) (string, error) { return "<report-body>Answer [S1] [S2]</report-body>", nil },
	}
}

// In the interactive TUI the tool must return at once and finish later
// through the same registry/callback path background subagents use.
func TestDeepResearch_BackgroundReturnsImmediatelyAndCompletes(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	h := happyHandlers()
	plan := h["research-planner"]
	h["research-planner"] = func(n int, p string) (string, error) { <-release; return plan(n, p) }
	tool, reg, done := newBackgroundResearchTool(t, &fakeResearch{handlers: h}, dir)

	out, err := tool.Execute(context.Background(), `{"query":"How does X work?"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "started as background task") {
		t.Errorf("expected an immediate 'started' result, got:\n%s", out)
	}
	tasks := reg.List()
	if len(tasks) != 1 || tasks[0].Status != subagents.TaskRunning || !tasks[0].Background || !tasks[0].NotifyOnDone || tasks[0].AgentType != "deep-research" {
		t.Fatalf("expected one running background deep-research task, got %+v", tasks)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "deep-research-*.md")); len(files) != 0 {
		t.Errorf("no report may exist before the run finishes, found %v", files)
	}

	close(release)
	var ev SubagentBackgroundDone
	select {
	case ev = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("completion callback never fired")
	}
	if ev.Errored || !ev.NotifyOnDone || ev.AgentType != "deep-research" || !strings.Contains(ev.Result, "Full report") {
		t.Errorf("unexpected completion event: %+v", ev)
	}
	got, _ := reg.Get(ev.TaskID)
	if got.Status != subagents.TaskCompleted || got.Result != ev.Result {
		t.Errorf("registry not updated before the callback: %+v", got)
	}
	var phases int
	for _, a := range got.Activities {
		if strings.HasPrefix(a, "deep-research: ") {
			phases++
		}
	}
	if phases != 5 {
		t.Errorf("dock activity should carry the 5 phase lines, got %d: %v", phases, got.Activities)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "deep-research-*.md")); len(files) != 1 {
		t.Errorf("expected one report file, found %v", files)
	}
}

// /subagents stop (registry.Cancel) must stop a detached run cleanly: a
// Canceled task, a callback, and no half-written report.
func TestDeepResearch_BackgroundCancel(t *testing.T) {
	dir := t.TempDir()
	started := make(chan struct{})
	release := make(chan struct{})
	h := happyHandlers()
	h["research-planner"] = func(int, string) (string, error) {
		close(started)
		<-release
		return `{"questions":["a"]}`, nil
	}
	tool, reg, done := newBackgroundResearchTool(t, &fakeResearch{handlers: h}, dir)
	if _, err := tool.Execute(context.Background(), `{"query":"q"}`); err != nil {
		t.Fatal(err)
	}
	<-started
	if !reg.Cancel(reg.List()[0].ID) {
		t.Fatal("Cancel found no running task")
	}
	close(release)
	select {
	case ev := <-done:
		if !strings.Contains(ev.Result, "stopped") {
			t.Errorf("result should say it was stopped: %q", ev.Result)
		}
		if got, _ := reg.Get(ev.TaskID); got.Status != subagents.TaskCanceled {
			t.Errorf("status = %v, want canceled", got.Status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("completion callback never fired after cancel")
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "deep-research-*.md")); len(files) != 0 {
		t.Errorf("a cancelled run must not leave a report, found %v", files)
	}
}

func TestDeepResearch_BackgroundCapRejection(t *testing.T) {
	tool, reg, _ := newBackgroundResearchTool(t, &fakeResearch{handlers: happyHandlers()}, t.TempDir())
	tool.Agent.MaxConcurrentSubagents = 1
	reg.Add(&subagents.Task{ID: "busy", AgentType: "x", Status: subagents.TaskRunning, Background: true})
	out, err := tool.Execute(context.Background(), `{"query":"q"}`)
	if err != nil || !strings.HasPrefix(out, "error: at most 1 background") {
		t.Errorf("want a cap rejection result, got %q, %v", out, err)
	}
	if len(reg.List()) != 1 {
		t.Errorf("a rejected run must not register a task, have %d", len(reg.List()))
	}
}

// Researchers' own stated uncertainties are scope notes, not failures: they
// must reach the report without turning a fully verified run Partial.
func TestDeepResearch_UncertaintiesDoNotMakeRunPartial(t *testing.T) {
	dir := t.TempDir()
	h := happyHandlers()
	h["researcher"] = func(int, string) (string, error) {
		return `{"claims":[{"claim":"c","evidence":"e","source_title":"T","source_locator":"https://x","source_type":"primary","confidence":"high"}],"uncertainties":["a page returned 403"]}`, nil
	}
	h["research-planner"] = func(int, string) (string, error) { return `{"questions":["a"]}`, nil }
	h["research-synthesizer"] = func(int, string) (string, error) { return "<report-body>A [S1]</report-body>", nil }
	out, err := newTestResearchTool(&fakeResearch{handlers: h}, dir).Execute(context.Background(), `{"query":"q"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Status: Partial") {
		t.Errorf("uncertainty alone must not mark the run Partial:\n%s", out)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "deep-research-q.md"))
	if !strings.Contains(string(b), "**Status: Verified**") || !strings.Contains(string(b), "a page returned 403") {
		t.Errorf("report should be Verified and still list the uncertainty:\n%s", b)
	}
}

func TestLeadSummary(t *testing.T) {
	body := "Direct answer. [S1]\n\nSecond para. [S2]\n\n### Details\n\nlots more [S3]"
	got := leadSummary(body, 1500)
	if !strings.Contains(got, "Second para") || strings.Contains(got, "Details") || strings.Contains(got, "lots more") {
		t.Errorf("lead should stop before the first heading:\n%s", got)
	}
	if !strings.Contains(got, "saved report") {
		t.Errorf("lead should point at the saved report:\n%s", got)
	}
	if got := leadSummary("## Findings\n- a [S1]\n- b [S2]", 1500); !strings.Contains(got, "- b [S2]") {
		t.Errorf("heading-first bodies (the plain-list fallback) must keep their list:\n%s", got)
	}
	if got := leadSummary("x\n\n"+strings.Repeat("y", 3000), 100); len([]rune(got)) > 250 {
		t.Errorf("lead not clipped: %d runes", len([]rune(got)))
	}
}

// Text from sub-agents comes from arbitrary web pages and lands in the saved
// report and the terminal: escape sequences must not survive, ordinary
// newlines and tabs must.
func TestStripControl(t *testing.T) {
	in := "a\x1b[2Jb\x1b]52;c;ZXZpbA==\x07c\td\ne\x00f\x7fg"
	got := stripControl(in)
	if strings.ContainsAny(got, "\x1b\x07\x00\x7f") {
		t.Errorf("control characters survived: %q", got)
	}
	if !strings.Contains(got, "\t") || !strings.Contains(got, "\n") {
		t.Errorf("tab and newline must be kept: %q", got)
	}
	if got := oneLine("T\x1b[31mitle\x1b[0m\nof   page"); got != "T[31mitle[0m of page" {
		t.Errorf("oneLine = %q", got)
	}
}

func TestSubAgentTextIsSanitizedAtTheBoundary(t *testing.T) {
	const esc = "\x1b[31m"
	c, ok := acceptClaim(researchClaim{Claim: "c" + esc, Evidence: "e" + esc, SourceTitle: "t" + esc, SourceLocator: "https://x" + esc})
	if !ok {
		t.Fatal("claim rejected")
	}
	for name, v := range map[string]string{"claim": c.Claim, "evidence": c.Evidence, "title": c.SourceTitle, "locator": c.SourceLocator} {
		if strings.Contains(v, "\x1b") {
			t.Errorf("%s kept an escape byte: %q", name, v)
		}
	}
	vc, ok := acceptVerdict(c, rawVerdict{Supported: true, Evidence: "e" + esc, SourceTitle: "t" + esc, SourceLocator: "l" + esc, Reason: "r" + esc})
	if !ok {
		t.Fatal("verdict rejected")
	}
	for name, v := range map[string]string{"evidence": vc.VerifierEvidence, "title": vc.VerifierSourceTitle, "locator": vc.VerifierSourceLocator, "note": vc.VerifierNote} {
		if strings.Contains(v, "\x1b") {
			t.Errorf("verifier %s kept an escape byte: %q", name, v)
		}
	}
	if body, ok := extractReportBody("<report-body>ok \x1b]0;pwn\x07 [S1]</report-body>"); !ok || strings.ContainsAny(body, "\x1b\x07") {
		t.Errorf("report body kept control characters: %q", body)
	}
}

// One run at a time per session: a second start while one is active is refused
// (not queued, not raced), and allowed again once the first has finished.
func TestDeepResearch_OnlyOneBackgroundRunAtATime(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	h := happyHandlers()
	plan := h["research-planner"]
	h["research-planner"] = func(n int, p string) (string, error) { <-release; return plan(n, p) }
	tool, reg, done := newBackgroundResearchTool(t, &fakeResearch{handlers: h}, dir)

	if out, _ := tool.Execute(context.Background(), `{"query":"first"}`); !strings.Contains(out, "started as background task") {
		t.Fatalf("first run should start, got %q", out)
	}
	out, err := tool.Execute(context.Background(), `{"query":"second"}`)
	if err != nil || !strings.HasPrefix(out, "error: a deep-research run is already in progress") {
		t.Errorf("second start should be refused, got %q, %v", out, err)
	}
	if n := len(reg.List()); n != 1 {
		t.Errorf("a refused start must not register a task, have %d", n)
	}

	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("first run never finished")
	}
	if out, _ := tool.Execute(context.Background(), `{"query":"third"}`); !strings.Contains(out, "started as background task") {
		t.Errorf("a new run should be allowed once the first finished, got %q", out)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("third run never finished")
	}
}

// web_search is read-only (it mutates nothing) but it is outbound network with
// a model-chosen query, exactly like fetch_url. Unattended standalone
// background subagents must not get either auto-approved.
func TestUnattendedAllowlistExcludesNetworkTools(t *testing.T) {
	for _, name := range []string{"web_search", "fetch_url"} {
		if !readOnlyChildTools[name] {
			t.Errorf("%s should be classified read-only", name)
		}
		if safeUnattendedReadOnlyTool(name) {
			t.Errorf("%s must not be on the unattended background allowlist", name)
		}
	}
	if !safeUnattendedReadOnlyTool("read_file") {
		t.Error("ordinary read-only tools must stay allowed unattended")
	}
}

func TestChildIterationBudget(t *testing.T) {
	for _, c := range []struct {
		name string
		cfg  *subagents.AgentConfig
		want int
	}{
		{"nil config", nil, childIterationCap},
		{"unset", &subagents.AgentConfig{}, childIterationCap},
		{"lower", &subagents.AgentConfig{MaxIterations: 30}, 30},
		{"cannot raise above the session cap", &subagents.AgentConfig{MaxIterations: 500}, childIterationCap},
		{"equal", &subagents.AgentConfig{MaxIterations: childIterationCap}, childIterationCap},
	} {
		if got := childIterationBudget(c.cfg); got != c.want {
			t.Errorf("%s: budget = %d, want %d", c.name, got, c.want)
		}
	}
}

// An iteration-capped agent must fail once, not be retried: a retry would
// burn a second full budget to hit the same wall.
func TestRunJSON_DoesNotRetryIterCap(t *testing.T) {
	calls := 0
	tool := &DeepResearchTool{run: func(context.Context, string, string) (string, error) {
		calls++
		return "", errResearchIterCap
	}}
	_, err := tool.runJSON(context.Background(), "researcher", "p", func(string) error { return nil })
	if !errors.Is(err, errResearchIterCap) || calls != 1 {
		t.Errorf("err = %v after %d call(s), want one call and errResearchIterCap", err, calls)
	}
}

func TestCompactCount(t *testing.T) {
	for in, want := range map[int]string{0: "0", 999: "999", 1000: "1K", 492_400: "492K", 1_611_000: "1.6M"} {
		if got := compactCount(in); got != want {
			t.Errorf("compactCount(%d) = %q, want %q", in, got, want)
		}
	}
}

// The result and the saved report both state what the run cost, the token
// total is the registry's usage for exactly this run's agents, and it is split
// by agent type so the user can see which phase spent it.
func TestDeepResearch_CostSummary(t *testing.T) {
	dir := t.TempDir()
	reg := subagents.NewRegistry()
	// A finished task from before the run must not be counted.
	reg.Add(&subagents.Task{ID: "old", AgentType: "researcher", Started: time.Now().Add(-time.Hour), TokensUsed: 9_000_000})
	h := happyHandlers()
	research := h["researcher"]
	h["researcher"] = func(n int, p string) (string, error) {
		reg.Add(&subagents.Task{ID: subagents.NewTaskID(), AgentType: "researcher", Started: time.Now(), TokensUsed: 100_000})
		return research(n, p)
	}
	verify := h["research-verifier"]
	h["research-verifier"] = func(n int, p string) (string, error) {
		// Exact provider usage wins over the estimate when both are present.
		reg.Add(&subagents.Task{ID: subagents.NewTaskID(), AgentType: "research-verifier", Started: time.Now(), TokensUsed: 1,
			Usage: adapter.Usage{InputTokens: 40_000, OutputTokens: 10_000}})
		return verify(n, p)
	}
	tool := newTestResearchTool(&fakeResearch{handlers: h}, dir)
	tool.Agent = &AgentTool{Tasks: reg} // AllowBackground unset → blocking
	out, err := tool.Execute(context.Background(), `{"query":"q"}`)
	if err != nil {
		t.Fatal(err)
	}
	// researcher 2×100K + verifier 2×50K (exact usage); planner and synthesizer
	// recorded nothing, so they are omitted from the split.
	// The peak depends on how the fake runner's goroutines interleave (1 or 2),
	// so match it as a range; the run count and the token split are exact.
	want := regexp.MustCompile(`Cost: 6 agent runs \(planner 1, researcher 2, verifier 2, synthesizer 1\), at most [12] at once · ~300K tokens \(researcher 200K, verifier 100K\)`)
	if !want.MatchString(out) {
		t.Errorf("result lacks the cost line:\n%s", out)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "deep-research-q.md"))
	if !strings.Contains(string(b), "## Run cost") || !want.Match(b) {
		t.Errorf("report lacks the run-cost section:\n%s", b)
	}
}

// "6 agent runs" is cumulative over the workflow; only some overlap. The cost
// line states the peak so it cannot read as 6 at once.
func TestResearchStats_TracksPeakConcurrency(t *testing.T) {
	s := &researchStats{calls: map[string]int{}, start: time.Now()}
	s.begin("research-planner")
	s.end() // planner finishes before research starts
	s.begin("researcher")
	s.begin("researcher")
	s.begin("researcher") // three in flight
	s.end()
	s.begin("research-verifier") // back to three; the peak must not grow
	s.end()
	s.end()
	s.end()

	tool := &DeepResearchTool{}
	line := tool.costLine(s)
	if !strings.Contains(line, "5 agent runs (planner 1, researcher 3, verifier 1), at most 3 at once") {
		t.Errorf("cost line = %q", line)
	}
	if s.running != 0 {
		t.Errorf("running = %d after every run ended, want 0", s.running)
	}
	// A nil stats (no run context) must be safe.
	var none *researchStats
	none.begin("researcher")
	none.end()
}

// Retries are real spend and must show in the agent-run count.
func TestDeepResearch_CostCountsRetries(t *testing.T) {
	h := happyHandlers()
	h["research-planner"] = func(int, string) (string, error) { return `{"questions":["only"]}`, nil }
	h["researcher"] = func(n int, p string) (string, error) {
		if n == 1 {
			return "prose, not JSON", nil
		}
		return researchReply(1), nil
	}
	h["research-synthesizer"] = func(int, string) (string, error) { return "<report-body>A [S1]</report-body>", nil }
	out, err := newTestResearchTool(&fakeResearch{handlers: h}, t.TempDir()).Execute(context.Background(), `{"query":"q"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "researcher 2") {
		t.Errorf("a retried researcher should count twice:\n%s", out)
	}
	if strings.Contains(out, "tokens") {
		t.Errorf("no registry → tokens must be omitted, not guessed:\n%s", out)
	}
}

// A subagent that inherits every parent tool must not receive deep_research:
// it would let a child start its own 4+2 agent fan-out.
func TestDeepResearch_IsADelegationTool(t *testing.T) {
	if !isDelegationTool(DeepResearchToolName) {
		t.Error("deep_research must be stripped from child registries like Agent/dispatch")
	}
}

func TestDeepResearch_SlotsRespectForegroundCap(t *testing.T) {
	capped := &DeepResearchTool{Agent: &AgentTool{MaxConcurrentSubagents: 2}}
	if got := cap(capped.slots(6)); got != 2 {
		t.Errorf("slots(6) with cap 2 = %d, want 2", got)
	}
	if got := cap(capped.slots(1)); got != 1 {
		t.Errorf("slots(1) = %d, want 1", got)
	}
	if got := cap((&DeepResearchTool{}).slots(0)); got != 1 {
		t.Errorf("slots(0) must still allow progress, got %d", got)
	}
}

// With fewer slots than questions, every researcher must still run (queued,
// not dropped) and never exceed the cap.
func TestDeepResearch_QueuesWhenCapIsLow(t *testing.T) {
	var mu syncutil.Mutex
	running, peak := 0, 0
	f := &fakeResearch{handlers: map[string]func(int, string) (string, error){
		"research-planner": func(int, string) (string, error) { return `{"questions":["a","b","c","d"]}`, nil },
		"researcher": func(int, string) (string, error) {
			mu.Lock()
			running++
			peak = max(peak, running)
			mu.Unlock()
			time.Sleep(10 * time.Millisecond)
			mu.Lock()
			running--
			mu.Unlock()
			return researchReply(1), nil
		},
		"research-verifier":    verifyAll,
		"research-synthesizer": func(int, string) (string, error) { return "", errors.New("skip") },
	}}
	tool := newTestResearchTool(f, t.TempDir())
	tool.Agent = &AgentTool{MaxConcurrentSubagents: 2}
	if _, err := tool.Execute(context.Background(), `{"query":"q"}`); err != nil {
		t.Fatal(err)
	}
	if f.calls["researcher"] != 4 {
		t.Errorf("researchers run = %d, want 4", f.calls["researcher"])
	}
	if peak > 2 {
		t.Errorf("peak concurrency %d exceeded cap 2", peak)
	}
}

// deep_research needs no approval but writes a report file, so plan mode
// must block it explicitly rather than classify it read-only.
func TestPlanModeGate_BlocksDeepResearch(t *testing.T) {
	tool := &DeepResearchTool{}
	if msg, blocked := PlanModeGate(tool, `{"query":"q"}`, "plan.md"); !blocked || msg == "" {
		t.Errorf("deep_research must be blocked in plan mode, got blocked=%v msg=%q", blocked, msg)
	}
}
