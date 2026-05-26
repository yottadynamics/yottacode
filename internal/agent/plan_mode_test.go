package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

func TestSlugFromPrompt_KebabAndHash(t *testing.T) {
	cases := []struct {
		name      string
		prompt    string
		salt      string
		wantPfx   string
		wantHexLn int
	}{
		{"plain", "Add caching to fetch_url", "sess1", "add-caching-to-fetch-url-", 16},
		{"empty", "", "sess1", "untitled-", 16},
		{"all-punct", "!!! ??? ...", "sess1", "untitled-", 16},
		{"unicode-strip", "Refactor the **HTTP** layer (v2)", "sess1", "refactor-the-http-layer-v2-", 16},
		{"long-truncates", strings.Repeat("a", 200), "sess1", "", 16},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SlugFromPrompt(tc.prompt, tc.salt)
			if tc.wantPfx != "" && !strings.HasPrefix(got, tc.wantPfx) {
				t.Errorf("slug %q does not start with %q", got, tc.wantPfx)
			}
			// Suffix is always exactly 16 hex chars after the last `-`.
			lastDash := strings.LastIndex(got, "-")
			if lastDash < 0 || len(got[lastDash+1:]) != tc.wantHexLn {
				t.Errorf("slug %q missing 16-hex suffix after final '-'", got)
			}
			// Kebab portion never exceeds 60 chars.
			kebab := got[:lastDash]
			if len(kebab) > 60 {
				t.Errorf("kebab portion %q exceeds 60 chars (got %d)", kebab, len(kebab))
			}
		})
	}
}

func TestSlugFromPrompt_StableForSameInput(t *testing.T) {
	a := SlugFromPrompt("hello world", "salt")
	b := SlugFromPrompt("hello world", "salt")
	if a != b {
		t.Errorf("slug not deterministic: %q vs %q", a, b)
	}
	c := SlugFromPrompt("hello world", "different-salt")
	if a == c {
		t.Errorf("slug should differ with different salt; got %q both times", a)
	}
}

func TestPlanFilePath_HonorsHomeOverride(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("YOTTACODE_HOME", tmp)
	got, err := PlanFilePath("my-plan-abcd")
	if err != nil {
		t.Fatalf("PlanFilePath: %v", err)
	}
	want := filepath.Join(tmp, "plans", "my-plan-abcd.md")
	if got != want {
		t.Errorf("PlanFilePath = %q, want %q", got, want)
	}
}

func TestPlanFilePath_EmptySlugErrors(t *testing.T) {
	if _, err := PlanFilePath(""); err == nil {
		t.Errorf("expected error on empty slug")
	}
}

func TestPlanModeGate_AllowsReadOnlyTools(t *testing.T) {
	for _, name := range []string{"read_file", "grep", "list_dir", "glob", "git_log_file", "fetch_url"} {
		tool := &mockTool{name: name, requiresApproval: false}
		if msg, blocked := PlanModeGate(tool, `{}`, "/tmp/plans/foo.md"); blocked {
			t.Errorf("expected %s to be allowed in plan mode; blocked with %q", name, msg)
		}
	}
}

func TestPlanModeGate_BlocksMutatingTools(t *testing.T) {
	for _, name := range []string{"run_bash", "git_commit", "git_stage_files", "mkdir", "delete_file"} {
		tool := &mockTool{name: name, requiresApproval: true}
		msg, blocked := PlanModeGate(tool, `{}`, "/tmp/plans/foo.md")
		if !blocked {
			t.Errorf("expected %s to be blocked in plan mode", name)
		}
		if !strings.Contains(msg, "unavailable in plan mode") {
			t.Errorf("block message for %s should mention plan mode; got %q", name, msg)
		}
	}
}

func TestPlanModeGate_AllowsTodoWriteAndExitPlanMode(t *testing.T) {
	for _, name := range []string{"todo_write", "exit_plan_mode"} {
		tool := &mockTool{name: name, requiresApproval: true}
		if _, blocked := PlanModeGate(tool, `{}`, "/tmp/plans/foo.md"); blocked {
			t.Errorf("expected %s to bypass the plan-mode block", name)
		}
	}
}

func TestPlanModeGate_AllowsWritesOnlyToPlanFile(t *testing.T) {
	tmp := t.TempDir()
	planFile := filepath.Join(tmp, "plans", "foo.md")

	cases := []struct {
		name        string
		tool        string
		argsJSON    string
		wantBlocked bool
	}{
		{"write_file to plan file", "write_file", `{"path":"` + planFile + `"}`, false},
		{"edit_file to plan file", "edit_file", `{"path":"` + planFile + `"}`, false},
		{"apply_diff to plan file", "apply_diff", `{"file_path":"` + planFile + `"}`, false},
		{"write_file to source", "write_file", `{"path":"` + filepath.Join(tmp, "main.go") + `"}`, true},
		{"edit_file to source", "edit_file", `{"path":"` + filepath.Join(tmp, "main.go") + `"}`, true},
		{"apply_diff to source", "apply_diff", `{"file_path":"` + filepath.Join(tmp, "main.go") + `"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := &mockTool{name: tc.tool, requiresApproval: true}
			_, blocked := PlanModeGate(tool, tc.argsJSON, planFile)
			if blocked != tc.wantBlocked {
				t.Errorf("blocked=%v, want %v", blocked, tc.wantBlocked)
			}
		})
	}
}

func TestPlanModeGate_NoPlanFileBlocksAllWrites(t *testing.T) {
	tool := &mockTool{name: "write_file", requiresApproval: true}
	msg, blocked := PlanModeGate(tool, `{"path":"/tmp/anything.md"}`, "")
	if !blocked {
		t.Errorf("expected write to be blocked when no plan file is set")
	}
	if !strings.Contains(msg, "no plan file is set") {
		t.Errorf("expected explanation about missing plan file; got %q", msg)
	}
}

func TestListPlans_NewestFirst(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("YOTTACODE_HOME", tmp)
	plansDir := filepath.Join(tmp, "plans")
	if err := os.MkdirAll(plansDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Write three plan files with deliberately distinct mtimes.
	for i, name := range []string{"older.md", "newer.md", "newest.md"} {
		path := filepath.Join(plansDir, name)
		if err := os.WriteFile(path, []byte("# plan"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		// Older → newest. Older mtimes earlier.
		ts := time.Now().Add(-time.Duration(2-i) * time.Hour)
		if err := os.Chtimes(path, ts, ts); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}
	plans, err := ListPlans()
	if err != nil {
		t.Fatalf("ListPlans: %v", err)
	}
	if len(plans) != 3 {
		t.Fatalf("expected 3 plans, got %d", len(plans))
	}
	if plans[0].Slug != "newest" || plans[2].Slug != "older" {
		t.Errorf("expected newest-first ordering; got %q, %q, %q",
			plans[0].Slug, plans[1].Slug, plans[2].Slug)
	}
}

func TestListPlans_MissingDirReturnsNil(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("YOTTACODE_HOME", tmp)
	// No plans subdir.
	plans, err := ListPlans()
	if err != nil {
		t.Fatalf("ListPlans should not error on missing dir: %v", err)
	}
	if plans != nil {
		t.Errorf("expected nil slice on missing dir; got %+v", plans)
	}
}

func TestListPlans_IgnoresNonMarkdown(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("YOTTACODE_HOME", tmp)
	plansDir := filepath.Join(tmp, "plans")
	if err := os.MkdirAll(plansDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"keep.md", "stash.txt", "notes.org", ".hidden"} {
		path := filepath.Join(plansDir, name)
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	plans, err := ListPlans()
	if err != nil {
		t.Fatalf("ListPlans: %v", err)
	}
	if len(plans) != 1 || plans[0].Slug != "keep" {
		t.Errorf("expected only the .md file; got %+v", plans)
	}
}

func TestMatchPlan(t *testing.T) {
	plans := []PlanEntry{
		{Slug: "refactor-auth-abc123"},
		{Slug: "add-caching-def456"},
		{Slug: "refactor-billing-789"},
	}
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{"prefix", "refactor", "refactor-auth-abc123"}, // first match wins (newest-first sort)
		{"middle", "caching", "add-caching-def456"},
		{"case-insensitive", "CACHING", "add-caching-def456"},
		{"no match", "noop", ""},
		{"empty query", "", ""},
		{"whitespace", "   ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchPlan(plans, tc.query)
			if tc.want == "" {
				if got != nil {
					t.Errorf("expected nil match; got %q", got.Slug)
				}
				return
			}
			if got == nil {
				t.Errorf("expected match %q; got nil", tc.want)
				return
			}
			if got.Slug != tc.want {
				t.Errorf("expected %q; got %q", tc.want, got.Slug)
			}
		})
	}
}

func TestPlanModeState_IsActiveNilSafe(t *testing.T) {
	var p *PlanModeState
	if p.IsActive() {
		t.Errorf("nil PlanModeState should report inactive")
	}
	p = &PlanModeState{}
	if p.IsActive() {
		t.Errorf("zero-value PlanModeState should report inactive")
	}
	p.Active.Store(true)
	if !p.IsActive() {
		t.Errorf("Active=true should report active")
	}
}

// --- loop integration ------------------------------------------------------

func TestLoop_PlanMode_BlocksMutatingToolWithGateMessage(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "run_bash", ArgsJSON: `{"command":"echo hi"}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "run_bash", requiresApproval: true, output: "should-not-execute"})
	planMode := &PlanModeState{PlanFile: "/tmp/plans/foo.md"}
	planMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, PlanMode: planMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	// Should never have asked for approval — the gate intercepts first.
	if hasEvent[ApprovalNeeded](events) {
		t.Errorf("plan-mode block should not surface as ApprovalNeeded")
	}
	// The gate emits an ApprovalAuto with Source=plan-mode-block.
	var auto *ApprovalAuto
	for i := range events {
		if a, ok := events[i].(ApprovalAuto); ok && a.Source == "plan-mode-block" {
			auto = &a
		}
	}
	if auto == nil {
		t.Errorf("expected ApprovalAuto with Source=plan-mode-block; got %+v", events)
	}
	// The model should see the gate's error string in the tool result.
	var sawGateMsg bool
	for _, m := range hist {
		if m.Role == adapter.RoleTool && strings.Contains(m.Content, "unavailable in plan mode") {
			sawGateMsg = true
		}
	}
	if !sawGateMsg {
		t.Errorf("expected tool-result message to carry the gate's block string; history=%+v", hist)
	}
}

func TestLoop_PlanMode_AllowsReadToolWithoutApproval(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "read_file", ArgsJSON: `{"path":"main.go"}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "read_file", requiresApproval: false, output: "package main"})
	planMode := &PlanModeState{PlanFile: "/tmp/plans/foo.md"}
	planMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, PlanMode: planMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "look"}}

	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if hasEvent[ApprovalNeeded](events) {
		t.Errorf("read-only tool should not require approval in plan mode either")
	}
	if !hasEvent[ToolStart](events) {
		t.Errorf("expected ToolStart for the allowed tool")
	}
}

// Confirm the gate flips off once PlanMode.Active does — exercises the
// post-approve path. We start in plan mode, run one turn that gets
// blocked, then flip the flag and run a second turn that succeeds.
func TestLoop_PlanMode_FlipOffAllowsNextTurn(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "run_bash", ArgsJSON: `{"command":"echo hi"}`})},
		{sseToken("blocked"), sseDone("blocked")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "run_bash", requiresApproval: true, output: "executed"})
	planMode := &PlanModeState{PlanFile: "/tmp/plans/foo.md"}
	planMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, PlanMode: planMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}
	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if !hasEvent[ApprovalAuto](events) {
		t.Errorf("first turn should have emitted plan-mode-block")
	}

	// Now exit plan mode and run a second turn — the same tool should
	// now require approval (the gate stops short-circuiting).
	planMode.Active.Store(false)
	streamer2 := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c2", Name: "run_bash", ArgsJSON: `{"command":"echo hi"}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	cfg.Adapter = streamer2
	hist2 := []adapter.Message{{Role: adapter.RoleUser, Content: "go again"}}
	events2, _ := runTurnSync(t, context.Background(), cfg, &hist2, func(ApprovalNeeded) Decision {
		return AllowOnce
	})
	if !hasEvent[ApprovalNeeded](events2) {
		t.Errorf("after exiting plan mode the mutating tool should hit the approval modal")
	}
}

// --- write-path allowance -------------------------------------------------

func TestValidateWritePath_PlanModeAllowedFile(t *testing.T) {
	tmp := t.TempDir()
	planDir := filepath.Join(tmp, "plans")
	if err := os.MkdirAll(planDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	planFile := filepath.Join(planDir, "p.md")
	cwd := filepath.Join(tmp, "project")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	opts := WritePathOptions{Cwd: NewCwdRef(cwd), PlanModeAllowedFile: planFile}

	// Plan file (outside cwd) — allowed because of the short-circuit.
	if err := ValidateWritePath(planFile, opts); err != nil {
		t.Errorf("plan file should be allowed: %v", err)
	}
	// A sibling under the plan dir — not allowed (only exact match).
	sibling := filepath.Join(planDir, "other.md")
	if err := ValidateWritePath(sibling, opts); err == nil {
		t.Errorf("sibling file under plan dir should not be allowed")
	}
	// Inside cwd — still allowed via the cwd path.
	insideCwd := filepath.Join(cwd, "main.go")
	if err := ValidateWritePath(insideCwd, opts); err != nil {
		t.Errorf("file inside cwd should be allowed: %v", err)
	}
	// Outside cwd and not the plan file — blocked.
	outside := filepath.Join(tmp, "elsewhere", "x.go")
	if err := ValidateWritePath(outside, opts); err == nil {
		t.Errorf("file outside cwd and not the plan file should be blocked")
	}
}

func TestValidateWritePath_PlanModeAllowedFile_RespectsDenyList(t *testing.T) {
	tmp := t.TempDir()
	cwd := filepath.Join(tmp, "project")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	denied := filepath.Join(cwd, ".yottacode", "permissions.json")
	opts := WritePathOptions{
		Cwd:                 NewCwdRef(cwd),
		DenyExact:           DefaultDenyPaths(cwd),
		PlanModeAllowedFile: denied,
	}
	// Even if the user (somehow) crafts a plan path that lands on the
	// deny list, the deny list still wins — checked before the plan-mode
	// short-circuit.
	if err := ValidateWritePath(denied, opts); err == nil {
		t.Errorf("deny-list path should win over plan-mode allowance")
	}
}

// --- exit_plan_mode tool --------------------------------------------------

func TestExitPlanMode_SchemaTakesNoArguments(t *testing.T) {
	tool := &ExitPlanModeTool{}
	if !tool.RequiresApproval("") {
		t.Errorf("exit_plan_mode must require approval (routes through the plan-approval card)")
	}
	schema := tool.Schema()
	props, _ := schema["properties"].(map[string]any)
	if len(props) != 0 {
		t.Errorf("exit_plan_mode should advertise NO properties (plan content comes from the file); got %+v", props)
	}
	if _, ok := schema["required"]; ok {
		t.Errorf("exit_plan_mode should have no `required` field; got %+v", schema)
	}
}

// Execute only runs on the approve path (loop short-circuits on Deny),
// so the only success path is the "approved" string. The rejection
// path is exercised in the loop integration tests below.
func TestExitPlanMode_ApproveReturnsApprovedString(t *testing.T) {
	tool := &ExitPlanModeTool{}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "plan approved") {
		t.Errorf("output should announce approval; got %q", out)
	}
}

func TestExitPlanMode_RefusalMessageFirm(t *testing.T) {
	// The message must direct the model to END THE TURN and wait for
	// user feedback, NOT to immediately retry exit_plan_mode. Without
	// these directives the model loops the approval card forever.
	for _, want := range []string{
		"keep planning",
		"END THIS TURN NOW",
		"Do NOT call exit_plan_mode again",
		"Wait for the user's next message",
		"following turn",
	} {
		if !strings.Contains(ExitPlanModeRefusalMessage, want) {
			t.Errorf("refusal message missing %q; got %q", want, ExitPlanModeRefusalMessage)
		}
	}
}

func TestExitPlanMode_SavedForLaterMessageFirm(t *testing.T) {
	for _, want := range []string{
		"saving it for implementation later",
		"END THIS TURN NOW",
		"Do NOT call any more tools",
		"resume via /plan list",
	} {
		if !strings.Contains(ExitPlanModeSavedForLaterMessage, want) {
			t.Errorf("save-for-later message missing %q; got %q", want, ExitPlanModeSavedForLaterMessage)
		}
	}
}

// capturingStreamer is a Streamer test-double that records the
// messages it's handed on each ChatStream call so tests can assert
// what reached the adapter. Replays the same scripted turn for every
// call (sufficient for the tests below — none drive more than one
// turn through).
type capturingStreamerCall struct {
	messages []adapter.Message
	tools    []adapter.Tool
}

type capturingStreamer struct {
	turn  []adapter.StreamEvent
	calls []capturingStreamerCall
}

func (s *capturingStreamer) ChatStream(ctx context.Context, msgs []adapter.Message, tools []adapter.Tool) <-chan adapter.StreamEvent {
	// Copy the input slices so subsequent appends in the loop don't
	// retroactively change what the test inspects.
	msgsCopy := append([]adapter.Message(nil), msgs...)
	toolsCopy := append([]adapter.Tool(nil), tools...)
	s.calls = append(s.calls, capturingStreamerCall{messages: msgsCopy, tools: toolsCopy})
	out := make(chan adapter.StreamEvent, len(s.turn)+1)
	go func() {
		defer close(out)
		for _, ev := range s.turn {
			select {
			case <-ctx.Done():
				return
			case out <- ev:
			}
		}
	}()
	return out
}

// Plan-mode resume + manual edits both depend on the addendum carrying
// the *current* contents of the plan file each iteration. This test
// captures what the loop hands to the adapter and asserts the plan
// body shows up inside the addendum's delimiters.
func TestLoop_PlanMode_AddendumCarriesFileContents(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("YOTTACODE_HOME", tmp)
	planFile := filepath.Join(tmp, "plans", "resumed-plan-abc.md")
	if err := os.MkdirAll(filepath.Dir(planFile), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	planBody := "# Resumed Plan\n\n- step 1: investigate\n- step 2: refactor\n"
	if err := os.WriteFile(planFile, []byte(planBody), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cap := &capturingStreamer{turn: []adapter.StreamEvent{sseToken("ok"), sseDone("ok")}}
	planMode := &PlanModeState{PlanFile: planFile}
	planMode.Active.Store(true)
	cfg := LoopConfig{Adapter: cap, Registry: NewRegistry(), MaxIterations: 2, PlanMode: planMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "recap"}}
	if _, err := runTurnSync(t, context.Background(), cfg, &hist, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}

	if len(cap.calls) == 0 {
		t.Fatalf("adapter was never called")
	}
	// The addendum is prepended as the first system message in the
	// per-iteration slice. Confirm it carries both the path AND the
	// plan body inside the delimiters.
	first := cap.calls[0].messages
	if len(first) == 0 || first[0].Role != adapter.RoleSystem {
		t.Fatalf("expected addendum as first system message; got %+v", first)
	}
	addendum := first[0].Content
	if !strings.Contains(addendum, planFile) {
		t.Errorf("addendum missing plan file path; got %q", addendum)
	}
	if !strings.Contains(addendum, "---PLAN-FILE-START---") || !strings.Contains(addendum, "---PLAN-FILE-END---") {
		t.Errorf("addendum missing plan-file delimiters; got %q", addendum)
	}
	if !strings.Contains(addendum, planBody) {
		t.Errorf("addendum should embed the plan body; got %q", addendum)
	}
}

// Helper checks of the file-reader so failure modes produce useful
// markers in the addendum rather than empty / leaked errno strings.
func TestReadPlanFileForAddendum(t *testing.T) {
	tmp := t.TempDir()
	// Empty path → "no plan file resolved yet" marker.
	if got := readPlanFileForAddendum(""); !strings.Contains(got, "no plan file resolved") {
		t.Errorf("empty path; got %q", got)
	}
	// Missing file → "does not exist yet" marker.
	missing := filepath.Join(tmp, "missing.md")
	if got := readPlanFileForAddendum(missing); !strings.Contains(got, "does not exist yet") {
		t.Errorf("missing file; got %q", got)
	}
	// Empty file → "file is empty" marker.
	empty := filepath.Join(tmp, "empty.md")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	if got := readPlanFileForAddendum(empty); !strings.Contains(got, "file is empty") {
		t.Errorf("empty file; got %q", got)
	}
	// Populated file → content is returned verbatim.
	populated := filepath.Join(tmp, "plan.md")
	body := "# Plan\nhello"
	if err := os.WriteFile(populated, []byte(body), 0o644); err != nil {
		t.Fatalf("write populated: %v", err)
	}
	if got := readPlanFileForAddendum(populated); got != body {
		t.Errorf("populated file content mismatch; got %q want %q", got, body)
	}
	// Oversize file → truncated with a marker.
	big := filepath.Join(tmp, "big.md")
	huge := strings.Repeat("a", planAddendumMaxBytes+10_000)
	if err := os.WriteFile(big, []byte(huge), 0o644); err != nil {
		t.Fatalf("write big: %v", err)
	}
	got := readPlanFileForAddendum(big)
	if !strings.Contains(got, "truncated at") {
		t.Errorf("oversize file should be truncated with marker; got %q", got[:200])
	}
	if len(got) >= len(huge) {
		t.Errorf("oversize file should be shorter than the source; got %d, src %d", len(got), len(huge))
	}
}

// Loop integration: when the approval modal returns SaveForLater for
// exit_plan_mode, the loop must surface the firm "end this turn"
// message — NOT the refinement hint and NOT call Execute (which would
// otherwise return the "approved" string).
func TestLoop_PlanMode_SaveForLaterReturnsFirmMessage(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "exit_plan_mode", ArgsJSON: `{}`})},
		{sseToken("ok"), sseDone("acknowledged")},
	}}
	reg := NewRegistry()
	reg.Register(&ExitPlanModeTool{})
	planMode := &PlanModeState{PlanFile: "/tmp/plans/p.md"}
	planMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, PlanMode: planMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "plan it"}}

	events, err := runTurnSync(t, context.Background(), cfg, &hist, func(ApprovalNeeded) Decision {
		return SaveForLater
	})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if !hasEvent[ApprovalNeeded](events) {
		t.Errorf("exit_plan_mode should have requested approval")
	}
	// The tool-result message in history should be the SavedForLater
	// message, not the generic "approved" or the refusal text.
	var toolResult string
	for _, m := range hist {
		if m.Role == adapter.RoleTool {
			toolResult = m.Content
		}
	}
	if !strings.Contains(toolResult, "END THIS TURN NOW") {
		t.Errorf("expected SavedForLater message in tool result; got %q", toolResult)
	}
	if strings.Contains(toolResult, "plan approved") {
		t.Errorf("Execute should NOT have run; got the 'plan approved' string")
	}
}

// --- plan-file write auto-allow -------------------------------------------

func TestIsPlanFileWrite(t *testing.T) {
	planFile := "/tmp/plans/foo.md"
	cases := []struct {
		name     string
		tool     string
		args     string
		planFile string
		want     bool
	}{
		{"write_file to plan file", "write_file", `{"path":"` + planFile + `"}`, planFile, true},
		{"edit_file to plan file", "edit_file", `{"path":"` + planFile + `"}`, planFile, true},
		{"apply_diff to plan file", "apply_diff", `{"file_path":"` + planFile + `"}`, planFile, true},
		{"write_file elsewhere", "write_file", `{"path":"/tmp/other.go"}`, planFile, false},
		{"read_file ignored", "read_file", `{"path":"` + planFile + `"}`, planFile, false},
		{"empty plan file", "write_file", `{"path":"` + planFile + `"}`, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPlanFileWrite(tc.tool, tc.args, tc.planFile); got != tc.want {
				t.Errorf("IsPlanFileWrite(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestLoop_PlanMode_AutoApprovesPlanFileWrite(t *testing.T) {
	// In plan mode, write_file to the plan file should auto-allow
	// (Source=plan-mode-allow) without firing an approval modal.
	planFile := "/tmp/plan-test/plan.md"
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{
			ID:       "c1",
			Name:     "write_file",
			ArgsJSON: `{"path":"` + planFile + `"}`,
		})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	// mockTool stands in for write_file — RequiresApproval=true to
	// confirm the plan-mode auto-allow path bypasses the modal.
	reg.Register(&mockTool{name: "write_file", requiresApproval: true, output: "wrote"})
	planMode := &PlanModeState{PlanFile: planFile}
	planMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, PlanMode: planMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if hasEvent[ApprovalNeeded](events) {
		t.Errorf("plan-file write should auto-allow, not prompt")
	}
	var auto *ApprovalAuto
	for i := range events {
		if a, ok := events[i].(ApprovalAuto); ok && a.Source == "plan-mode-allow" {
			auto = &a
		}
	}
	if auto == nil {
		t.Errorf("expected ApprovalAuto with Source=plan-mode-allow; got %+v", events)
	}
}

func TestLoop_PlanMode_DenyRuleStillWinsOverPlanFileWrite(t *testing.T) {
	// A user who deny-rules write_file should be obeyed even when the
	// target is the plan file. Plan mode is "the model can write the
	// plan"; explicit deny is "I don't want this". The rule descriptor
	// is the relpath of the target from cwd — so we put the plan file
	// under the test cwd and write a deny rule that matches it
	// directly.
	perms, cwd := permsForTest(t, nil, nil, []string{"Write(plan.md)"})
	planFile := filepath.Join(cwd, "plan.md")
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{
			ID:       "c1",
			Name:     "write_file",
			ArgsJSON: `{"path":"` + planFile + `"}`,
		})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "write_file", requiresApproval: true, output: "wrote"})
	planMode := &PlanModeState{PlanFile: planFile}
	planMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, Permissions: perms, Cwd: NewCwdRef(cwd), MaxIterations: 5, PlanMode: planMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, _ := runTurnSync(t, context.Background(), cfg, &hist, nil)
	var sawDeny, sawPlanAllow bool
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok {
			if a.Source == "deny-rule" {
				sawDeny = true
			}
			if a.Source == "plan-mode-allow" {
				sawPlanAllow = true
			}
		}
	}
	if !sawDeny {
		t.Errorf("expected the explicit deny rule to fire; events=%+v", events)
	}
	if sawPlanAllow {
		t.Errorf("plan-mode-allow should NOT override an explicit deny rule")
	}
}

// Suppress the unused-import warning for `errors` if no other test in
// this file uses it after the trim. We still want it for future tests.
var _ = errors.New
