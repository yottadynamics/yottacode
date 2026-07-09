package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/subagents"
	"github.com/yottadynamics/yottacode/internal/worktree"
)

// --- pure-helper unit tests ------------------------------------------------

func TestValidateWritePartition(t *testing.T) {
	writer := &subagents.AgentConfig{Name: "writer"} // nil Tools → write-capable
	reader := &subagents.AgentConfig{Name: "reader", Tools: []string{"read_file"}}

	mk := func(cfg *subagents.AgentConfig, files ...string) *dispatchChild {
		return &dispatchChild{cfg: cfg, isWrite: !agentIsReadOnly(cfg), spec: dispatchTaskSpec{Files: files, Description: cfg.Name}}
	}

	t.Run("disjoint ok", func(t *testing.T) {
		got := validateWritePartition([]*dispatchChild{mk(writer, "a.go"), mk(writer, "b.go")})
		if got != "" {
			t.Errorf("expected ok, got %q", got)
		}
	})
	t.Run("overlap rejected", func(t *testing.T) {
		got := validateWritePartition([]*dispatchChild{mk(writer, "a.go", "shared.go"), mk(writer, "shared.go")})
		if !strings.Contains(got, "non-overlapping") {
			t.Errorf("expected overlap rejection, got %q", got)
		}
	})
	t.Run("missing files rejected", func(t *testing.T) {
		got := validateWritePartition([]*dispatchChild{mk(writer), mk(writer, "b.go")})
		if !strings.Contains(got, "must declare their `files`") {
			t.Errorf("expected missing-files rejection, got %q", got)
		}
	})
	t.Run("read-only tasks need no files", func(t *testing.T) {
		got := validateWritePartition([]*dispatchChild{mk(reader), mk(writer, "b.go")})
		if got != "" {
			t.Errorf("read-only task should not need files, got %q", got)
		}
	})
	t.Run("path normalization catches ./ overlap", func(t *testing.T) {
		got := validateWritePartition([]*dispatchChild{mk(writer, "./a.go"), mk(writer, "a.go")})
		if !strings.Contains(got, "non-overlapping") {
			t.Errorf("expected normalized overlap rejection, got %q", got)
		}
	})
}

func TestCommitSubject(t *testing.T) {
	if got := commitSubject("writer", "add the parser"); got != "writer: add the parser" {
		t.Errorf("got %q", got)
	}
	if got := commitSubject("writer", ""); got != "writer: dispatched changes" {
		t.Errorf("empty desc: got %q", got)
	}
	long := commitSubject("writer", strings.Repeat("x", 200))
	if len(long) > 72 {
		t.Errorf("subject not capped: len=%d", len(long))
	}
	if strings.HasSuffix(long, ".") {
		t.Errorf("subject must not end with a period: %q", long)
	}
}

// --- Execute gating tests --------------------------------------------------

func newDispatchToolForGating() *DispatchTool {
	at := &AgentTool{
		Configs: []subagents.AgentConfig{
			{Name: "writer"},
			{Name: "reader", Tools: []string{"read_file"}},
		},
		Tasks:    subagents.NewRegistry(),
		PlanMode: &PlanModeState{},
	}
	return &DispatchTool{Agent: at, Enabled: true}
}

func TestDispatch_GatingErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("disabled", func(t *testing.T) {
		d := newDispatchToolForGating()
		d.Enabled = false
		out, _ := d.Execute(ctx, `{"goal":"x","tasks":[{"subagent_type":"reader","prompt":"a"},{"subagent_type":"reader","prompt":"b"}]}`)
		if !strings.Contains(out, "experimental") {
			t.Errorf("expected experimental gate message, got %q", out)
		}
	})
	t.Run("too few tasks", func(t *testing.T) {
		d := newDispatchToolForGating()
		out, _ := d.Execute(ctx, `{"goal":"x","tasks":[{"subagent_type":"reader","prompt":"a"}]}`)
		if !strings.Contains(out, "at least 2 tasks") {
			t.Errorf("got %q", out)
		}
	})
	t.Run("unknown subagent", func(t *testing.T) {
		d := newDispatchToolForGating()
		out, _ := d.Execute(ctx, `{"goal":"x","tasks":[{"subagent_type":"nope","prompt":"a"},{"subagent_type":"reader","prompt":"b"}]}`)
		if !strings.Contains(out, "unknown subagent_type") {
			t.Errorf("got %q", out)
		}
	})
	t.Run("write overlap rejected before any work", func(t *testing.T) {
		d := newDispatchToolForGating()
		out, _ := d.Execute(ctx, `{"goal":"x","tasks":[{"subagent_type":"writer","prompt":"a","files":["x.go"]},{"subagent_type":"writer","prompt":"b","files":["x.go"]}]}`)
		if !strings.Contains(out, "non-overlapping") {
			t.Errorf("got %q", out)
		}
	})
	t.Run("write in plan mode rejected", func(t *testing.T) {
		d := newDispatchToolForGating()
		d.Agent.PlanMode.Active.Store(true)
		out, _ := d.Execute(ctx, `{"goal":"x","tasks":[{"subagent_type":"writer","prompt":"a","files":["x.go"]},{"subagent_type":"writer","prompt":"b","files":["y.go"]}]}`)
		if !strings.Contains(out, "plan mode") {
			t.Errorf("got %q", out)
		}
	})
}

// --- end-to-end fan-out ----------------------------------------------------

// dispatchWriteStreamer is a stateless test adapter: on the first call for a
// child it emits a write_file tool call to the path named after "TESTWRITE:"
// in the user prompt; once a tool result is in the history it emits a final
// reply. Stateless so concurrent children don't race.
type dispatchWriteStreamer struct{}

func (dispatchWriteStreamer) ChatStream(_ context.Context, msgs []adapter.Message, _ []adapter.Tool) <-chan adapter.StreamEvent {
	out := make(chan adapter.StreamEvent, 4)
	hasToolResult := false
	userPrompt := ""
	for _, m := range msgs {
		if m.Role == adapter.RoleTool {
			hasToolResult = true
		}
		if m.Role == adapter.RoleUser {
			userPrompt = m.Content
		}
	}
	go func() {
		defer close(out)
		if hasToolResult {
			out <- sseDone("done — file written")
			return
		}
		path := extractTestWritePath(userPrompt)
		args := fmt.Sprintf(`{"path":%q,"content":%q}`, path, "hello from "+path)
		out <- sseDone("", adapter.ToolCall{ID: "c1", Name: "write_file", ArgsJSON: args})
	}()
	return out
}

func extractTestWritePath(prompt string) string {
	const marker = "TESTWRITE:"
	i := strings.Index(prompt, marker)
	if i < 0 {
		return "out.txt"
	}
	rest := prompt[i+len(marker):]
	return strings.Fields(rest)[0]
}

// dispatchTestRepo creates a git repo with committer identity configured (so
// ApplyCommit's `git commit` works) and one base commit.
func dispatchTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"config", "commit.gpgsign", "false"},
		// Auto-gc off: a commit otherwise spawns a detached `git gc
		// --auto` that keeps writing to .git after the command returns.
		// On a loaded runner it's still active when t.TempDir()'s
		// RemoveAll runs, and the concurrent writes make the final rmdir
		// fail with "directory not empty" (intermittent macOS CI flake).
		{"config", "gc.auto", "0"},
	} {
		if _, err := gitOutput(ctx, dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "base"}} {
		if _, err := gitOutput(ctx, dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	// Worktrees live under ~/.yottacode/worktrees/<repo-slug>/ (outside
	// t.TempDir), so remove that slug dir on cleanup to avoid leaving
	// cruft behind across test runs.
	t.Cleanup(func() {
		os.RemoveAll(filepath.Dir(worktree.Dir(dir, "x")))
	})
	return dir
}

func newDispatchToolE2E(t *testing.T, repoRoot string) *DispatchTool {
	t.Helper()
	auto := &AutoModeState{}
	auto.Active.Store(true) // so child write_file auto-approves without a decisions channel
	at := &AgentTool{
		Configs:        []subagents.AgentConfig{{Name: "writer", Description: "implements a file"}},
		Tasks:          subagents.NewRegistry(),
		Adapter:        dispatchWriteStreamer{},
		ParentRegistry: NewRegistry(),
		Permissions:    nil,
		AutoMode:       auto,
		PlanMode:       &PlanModeState{},
		YoloMode:       &YoloModeState{},
		Cwd:            NewCwdRef(repoRoot),
		TranscriptDir:  t.TempDir(),
	}
	return &DispatchTool{Agent: at, Enabled: true}
}

func TestDispatch_EndToEnd_TwoWriteTasks(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)

	args := `{"goal":"build two files","tasks":[
		{"subagent_type":"writer","description":"file a","prompt":"create the file. TESTWRITE:alpha.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"file b","prompt":"create the file. TESTWRITE:beta.txt","files":["beta.txt"]}
	]}`
	out, err := d.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("dispatch Execute: %v", err)
	}

	// Two worktree branches were created and committed. Use a clean ref
	// format: git marks worktree-checked-out branches with "+", not "*".
	branchOut, gErr := gitOutput(context.Background(), repoRoot, "branch", "--list", "worktree-dispatch-*", "--format=%(refname:short)")
	if gErr != nil {
		t.Fatalf("git branch: %v", gErr)
	}
	branchNames := nonEmptyLines(branchOut)
	if len(branchNames) != 2 {
		t.Fatalf("expected 2 dispatch branches, got %v", branchNames)
	}

	// Each branch carries its owned file with the expected content.
	for _, f := range []string{"alpha.txt", "beta.txt"} {
		found := false
		for _, br := range branchNames {
			if content, e := gitOutput(context.Background(), repoRoot, "show", br+":"+f); e == nil {
				if strings.TrimSpace(content) == "hello from "+f {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("no dispatch branch committed %s with expected content", f)
		}
	}

	// Result mentions integration next-step.
	if !strings.Contains(out, "call integrate") {
		t.Errorf("result missing integrate hint:\n%s", out)
	}
	// Base working tree is untouched (isolation): no alpha/beta in repoRoot.
	for _, f := range []string{"alpha.txt", "beta.txt"} {
		if _, err := os.Stat(filepath.Join(repoRoot, f)); err == nil {
			t.Errorf("%s leaked into the base working tree — worktree isolation broken", f)
		}
	}
}

func TestDispatch_Foreground_OutOfScopeWriteIsNotCommitted(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)

	out, err := d.Execute(context.Background(), `{"goal":"stay scoped","background":false,"tasks":[
		{"subagent_type":"writer","description":"a","prompt":"TESTWRITE:beta.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"TESTWRITE:gamma.txt","files":["gamma.txt"]}
	]}`)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if strings.Contains(out, "beta.txt") && strings.Contains(out, "committed") {
		t.Fatalf("out-of-scope beta.txt should not be committed, got:\n%s", out)
	}

	branchOut, gErr := gitOutput(context.Background(), repoRoot, "branch", "--list", "worktree-dispatch-*", "--format=%(refname:short)")
	if gErr != nil {
		t.Fatalf("git branch: %v", gErr)
	}
	for _, br := range nonEmptyLines(branchOut) {
		if _, e := gitOutput(context.Background(), repoRoot, "show", br+":beta.txt"); e == nil {
			t.Fatalf("out-of-scope beta.txt was committed on %s", br)
		}
	}
}

// TestDispatch_Background_DoneCallbackCarriesCommitStatus is the P1
// regression for the async path: the background-done callback must report
// whether the worker actually committed (Committed + CommitSHA), not fire a
// no-op. Before the fix the event carried no commit status at all, so a
// banner could imply integrate-ready work on an empty branch.
func TestDispatch_Background_DoneCallbackCarriesCommitStatus(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)
	d.SupportsBackground = true

	done := make(chan SubagentBackgroundDone, 8)
	d.Agent.SetBackgroundDoneCallback(func(e SubagentBackgroundDone) { done <- e })

	_, err := d.Execute(context.Background(), `{"goal":"two files","tasks":[
		{"subagent_type":"writer","description":"a","prompt":"TESTWRITE:alpha.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"TESTWRITE:beta.txt","files":["beta.txt"]}
	]}`)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	got := map[string]SubagentBackgroundDone{}
	timeout := time.After(5 * time.Second)
	for len(got) < 2 {
		select {
		case e := <-done:
			got[e.TaskID] = e
		case <-timeout:
			t.Fatalf("only %d/2 background-done callbacks fired", len(got))
		}
	}
	for _, e := range got {
		if !e.Committed || e.CommitSHA == "" {
			t.Errorf("worker %s should report Committed with a SHA, got %+v", e.TaskID[:8], e)
		}
		if e.CommitErr != "" {
			t.Errorf("clean worker %s should have no CommitErr, got %q", e.TaskID[:8], e.CommitErr)
		}
	}
}

// TestDispatch_Foreground_HookRejectionSurfacesReason is the P1 regression
// for silent commit failures: a pre-commit hook rejecting the auto-commit
// must be reported as a reason (NOT a clean "no changes"), and such a branch
// must be excluded from the integrate hint.
func TestDispatch_Foreground_HookRejectionSurfacesReason(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	// A pre-commit hook that always fails. Linked worktrees share the main
	// repo's hooks dir, so this fires for every worker's commit.
	hookDir := filepath.Join(repoRoot, ".git", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hookDir, "pre-commit"),
		[]byte("#!/bin/sh\necho 'lint failed: forbidden token'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := newDispatchToolE2E(t, repoRoot)

	out, err := d.Execute(context.Background(), `{"goal":"x","background":false,"tasks":[
		{"subagent_type":"writer","description":"a","prompt":"TESTWRITE:alpha.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"TESTWRITE:beta.txt","files":["beta.txt"]}
	]}`)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(out, "NOT committed") {
		t.Errorf("expected a NOT committed reason, got:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "hook") {
		t.Errorf("expected the hook rejection surfaced as the reason, got:\n%s", out)
	}
	if !strings.Contains(out, "nothing to integrate") {
		t.Errorf("expected 'nothing to integrate' when every commit was rejected, got:\n%s", out)
	}
}

// TestBranchTip is the P1 regression for commit-presence derivation: a
// branch with a commit beyond base reports its tip SHA (so a worker that
// committed its own work and left a clean tree is recognized as having
// produced commits, instead of being judged by end-of-run dirtiness); a
// branch even with base reports "".
func TestBranchTip(t *testing.T) {
	ctx := context.Background()
	repo := dispatchTestRepo(t)
	base, err := gitOutput(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	base = strings.TrimSpace(base)

	// A branch with no commits beyond base → "".
	if tip := branchTip(ctx, repo, base); tip != "" {
		t.Errorf("branch even with base should report no tip, got %q", tip)
	}

	// Add a commit (a "self-committed worker" leaving a clean tree), then the
	// tip is reported even though the worktree is clean.
	if err := os.WriteFile(filepath.Join(repo, "self.txt"), []byte("self\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "self-committed"}} {
		if _, err := gitOutput(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if gitWorktreeDirty(ctx, repo) {
		t.Fatal("tree should be clean after the worker's own commit")
	}
	tip := branchTip(ctx, repo, base)
	if tip == "" {
		t.Fatal("a committed-but-clean branch must still report its tip (P1 presence derivation)")
	}
	head, _ := gitOutput(ctx, repo, "rev-parse", "HEAD")
	if tip != strings.TrimSpace(head) {
		t.Errorf("branchTip = %q, want HEAD %q", tip, strings.TrimSpace(head))
	}

	// Empty base → "" (guards the no-base path).
	if tip := branchTip(ctx, repo, ""); tip != "" {
		t.Errorf("empty base should report no tip, got %q", tip)
	}
}

// TestDispatch_Background_EnforcesMaxConcurrent is the P3 regression: a
// background dispatch must respect MaxBackgroundSubagents (repeated calls
// would otherwise stack unbounded detached workers), and a rejected batch
// must leak no worktrees.
func TestDispatch_Background_EnforcesMaxConcurrent(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)
	d.SupportsBackground = true

	// Saturate the background cap with already-running tasks.
	for i := 0; i < MaxBackgroundSubagents; i++ {
		d.Agent.Tasks.Add(&subagents.Task{
			ID:         subagents.NewTaskID(),
			Status:     subagents.TaskRunning,
			Background: true,
		})
	}

	out, err := d.Execute(context.Background(), `{"goal":"x","tasks":[
		{"subagent_type":"writer","description":"a","prompt":"TESTWRITE:alpha.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"TESTWRITE:beta.txt","files":["beta.txt"]}
	]}`)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(out, "exceed") {
		t.Errorf("expected the background cap to reject the batch, got:\n%s", out)
	}
	// The rejection must happen before any worktree is created.
	if branches := gitListBranches(t, repoRoot, "worktree-dispatch-*"); len(branches) != 0 {
		t.Errorf("rejected batch leaked worktree branches: %v", branches)
	}
}

// TestDispatch_Background_OutOfWorktreeWriteDoesNotHang is the P3.8
// regression: a background worker that tries to write outside its worktree
// trips PathTrustElevationNeeded. With no human to prompt, the drain loop
// must auto-deny and FEED the decision so the worker finishes — before the
// fix it blocked forever on the unfed decisions channel (the iteration cap
// can't fire mid-tool), wedging the slot. The test would time out if the
// worker hung.
func TestDispatch_Background_OutOfWorktreeWriteDoesNotHang(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)
	d.SupportsBackground = true

	// Absolute path OUTSIDE any worktree — the write must be refused.
	escape := filepath.Join(t.TempDir(), "escape.txt")
	args := fmt.Sprintf(`{"goal":"escape attempt","tasks":[
		{"subagent_type":"writer","description":"in","prompt":"TESTWRITE:inside.txt","files":["inside.txt"]},
		{"subagent_type":"writer","description":"out","prompt":"TESTWRITE:%s","files":["out.txt"]}
	]}`, escape)
	if _, err := d.Execute(context.Background(), args); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// Joins the detached workers; fails the test if the escaping worker hung.
	waitForTasksDone(t, d.Agent.Tasks, 2, 5*time.Second)

	if _, err := os.Stat(escape); err == nil {
		t.Errorf("out-of-worktree write should have been denied, but %s exists", escape)
	}
}

// TestDispatch_Foreground_OutOfWorktreeWrite_NoDeadlock is the E2E regression
// for the approval-gate deadlock, through the real dispatch foreground path.
// Foreground dispatch installs its own approval gate; a write worker that
// targets a path outside its worktree trips PathTrustElevationNeeded, whose
// handler holds the gate across blocking on the decision. Before the fix the
// child's own loop and the drain-loop forwarder contended on that same gate and
// hung. With the fix it returns quickly (the escaping write denied).
func TestDispatch_Foreground_OutOfWorktreeWrite_NoDeadlock(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)
	d.SupportsBackground = false // force foreground

	escape := filepath.Join(t.TempDir(), "escape.txt")
	args := fmt.Sprintf(`{"goal":"fg escape","background":false,"tasks":[
		{"subagent_type":"writer","description":"in","prompt":"TESTWRITE:inside.txt","files":["inside.txt"]},
		{"subagent_type":"writer","description":"out","prompt":"TESTWRITE:%s","files":["out.txt"]}
	]}`, escape)

	// Wire parent events + decisions so the foreground forward path (which takes
	// the gate) is exercised; answer Deny to the path-trust modal like a real UI.
	events := make(chan Event, 64)
	decisions := make(chan Decision, 1)
	ctx := WithParentDecisions(WithParentEvents(context.Background(), events), decisions)
	go func() {
		for ev := range events {
			if _, ok := ev.(PathTrustElevationNeeded); ok {
				decisions <- Deny
			}
		}
	}()

	done := make(chan struct{})
	go func() {
		_, _ = d.Execute(ctx, args)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("DEADLOCK: foreground dispatch hung on the approval gate for an out-of-worktree write")
	}
}

func TestDispatch_ResolveBackground(t *testing.T) {
	bp := func(b bool) *bool { return &b }
	cases := []struct {
		name     string
		req      *bool
		hasWrite bool
		supports bool
		wantBg   bool
		wantNote bool
	}{
		{"write batch defaults to background", nil, true, true, true, false},
		{"read batch defaults to foreground", nil, false, true, false, false},
		{"explicit false overrides write→fg", bp(false), true, true, false, false},
		{"explicit true overrides read→bg", bp(true), false, true, true, false},
		{"background unsupported falls back + notes", bp(true), false, false, false, true},
		{"write default unsupported falls back + notes", nil, true, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &DispatchTool{SupportsBackground: tc.supports}
			bg, note := d.resolveBackground(tc.req, tc.hasWrite)
			if bg != tc.wantBg {
				t.Errorf("bg = %v, want %v", bg, tc.wantBg)
			}
			if (note != "") != tc.wantNote {
				t.Errorf("note = %q, wantNote = %v", note, tc.wantNote)
			}
		})
	}
}

// waitForTasksDone polls the registry until n tasks exist and none is still
// running, or the timeout elapses. Used to join detached background workers
// before asserting on their committed branches.
func waitForTasksDone(t *testing.T, reg *subagents.Registry, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		tasks := reg.List()
		if len(tasks) >= n {
			allDone := true
			for _, tk := range tasks {
				if tk.Status == subagents.TaskRunning {
					allDone = false
					break
				}
			}
			if allDone {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d subagent tasks to finish", n)
}

// TestDispatch_Background_EndToEnd runs a write batch in background mode:
// dispatch returns immediately (non-blocking) with a batch handle, the
// detached workers commit to their branches, and the base tree is untouched.
func TestDispatch_Background_EndToEnd(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)
	d.SupportsBackground = true // enable real background execution

	// Write batch → defaults to background (no explicit flag).
	out, err := d.Execute(context.Background(), `{"goal":"two files in bg","tasks":[
		{"subagent_type":"writer","description":"a","prompt":"TESTWRITE:alpha.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"TESTWRITE:beta.txt","files":["beta.txt"]}
	]}`)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// Returns immediately with the background handle (not per-task results).
	if !strings.Contains(out, "BACKGROUND") {
		t.Errorf("expected background result, got:\n%s", out)
	}
	if !strings.Contains(out, "call integrate") {
		t.Errorf("expected integrate hint, got:\n%s", out)
	}

	// Join the detached workers, then assert they committed their branches.
	waitForTasksDone(t, d.Agent.Tasks, 2, 5*time.Second)

	branches := gitListBranches(t, repoRoot, "worktree-dispatch-*")
	if len(branches) != 2 {
		t.Fatalf("expected 2 dispatch branches, got %v", branches)
	}
	for _, f := range []string{"alpha.txt", "beta.txt"} {
		found := false
		for _, br := range branches {
			if content, e := gitOutput(context.Background(), repoRoot, "show", br+":"+f); e == nil &&
				strings.TrimSpace(content) == "hello from "+f {
				found = true
			}
		}
		if !found {
			t.Errorf("no background dispatch branch committed %s", f)
		}
	}
	// Workers were marked background in the registry.
	for _, tk := range d.Agent.Tasks.List() {
		if !tk.Background {
			t.Errorf("task %s should be marked background", tk.ID[:8])
		}
	}
}
