package agent

import (
	"context"
	"errors"
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

// dispatchNoopStreamer is a child that immediately returns a final reply
// without touching any file — its worktree ends clean with no commits.
type dispatchNoopStreamer struct{}

func (dispatchNoopStreamer) ChatStream(_ context.Context, _ []adapter.Message, _ []adapter.Tool) <-chan adapter.StreamEvent {
	out := make(chan adapter.StreamEvent, 1)
	out <- sseDone("nothing to do")
	close(out)
	return out
}

// dispatchErrStreamer is a child whose stream fails immediately — the worker
// errors with a clean worktree and an empty branch.
type dispatchErrStreamer struct{}

func (dispatchErrStreamer) ChatStream(_ context.Context, _ []adapter.Message, _ []adapter.Tool) <-chan adapter.StreamEvent {
	out := make(chan adapter.StreamEvent, 1)
	out <- adapter.StreamEvent{Kind: adapter.EventErr, Err: errors.New("provider exploded")}
	close(out)
	return out
}

// dispatchWriteThenErrStreamer writes its owned file on the first call, then
// fails the stream — the worker errors with a DIRTY worktree (uncommitted
// work that must be kept for recovery, never auto-reclaimed).
type dispatchWriteThenErrStreamer struct{}

func (dispatchWriteThenErrStreamer) ChatStream(_ context.Context, msgs []adapter.Message, _ []adapter.Tool) <-chan adapter.StreamEvent {
	out := make(chan adapter.StreamEvent, 1)
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
	if hasToolResult {
		out <- adapter.StreamEvent{Kind: adapter.EventErr, Err: errors.New("provider exploded mid-task")}
	} else {
		path := extractTestWritePath(userPrompt)
		args := fmt.Sprintf(`{"path":%q,"content":%q}`, path, "partial work")
		out <- sseDone("", adapter.ToolCall{ID: "c1", Name: "write_file", ArgsJSON: args})
	}
	close(out)
	return out
}

// dispatchWorktreeDirs returns the Worktree paths of every registry task.
func dispatchWorktreeDirs(reg *subagents.Registry) []string {
	var dirs []string
	for _, tk := range reg.List() {
		if tk.Worktree != "" {
			dirs = append(dirs, tk.Worktree)
		}
	}
	return dirs
}

// eventuallyNoDispatchWorktrees waits for git's linked-worktree cleanup to
// become visible before asserting. macOS CI can report a just-removed
// worktree branch briefly after the worker's cleanup returned.
func eventuallyNoDispatchWorktrees(t *testing.T, repoRoot string, dirs []string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var branches []string
	var existing []string
	for {
		branches = gitListBranches(t, repoRoot, "worktree-dispatch-*")
		existing = existing[:0]
		for _, dir := range dirs {
			if _, err := os.Stat(dir); err == nil {
				existing = append(existing, dir)
			}
		}
		if len(branches) == 0 && len(existing) == 0 {
			return
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(branches) != 0 {
		t.Errorf("empty dispatch branches should be deleted, still have %v", branches)
	}
	for _, dir := range existing {
		t.Errorf("worktree %s should be removed", dir)
	}
}

// TestDispatch_Foreground_EmptyWorktreesReclaimed is the foreground-leak
// regression: a FOREGROUND write batch whose workers produce nothing must
// reclaim their worktrees + branches at the end of the run. Before the fix,
// only background workers cleaned up, so every foreground no-op batch leaked
// a worktree+branch pair per task.
func TestDispatch_Foreground_EmptyWorktreesReclaimed(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)
	d.Agent.Adapter = dispatchNoopStreamer{}

	out, err := d.Execute(context.Background(), `{"goal":"x","background":false,"tasks":[
		{"subagent_type":"writer","description":"a","prompt":"do nothing","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"do nothing","files":["beta.txt"]}
	]}`)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if !strings.Contains(out, "empty worktree and branch reclaimed") {
		t.Errorf("result should report the reclaim, got:\n%s", out)
	}
	if !strings.Contains(out, "nothing to integrate") {
		t.Errorf("expected 'nothing to integrate', got:\n%s", out)
	}
	eventuallyNoDispatchWorktrees(t, repoRoot, dispatchWorktreeDirs(d.Agent.Tasks))
}

// TestDispatch_Foreground_ErroredCleanWorktreeReclaimed: a FAILED worker that
// left nothing behind (clean tree, empty branch) has nothing to recover — its
// worktree + branch are reclaimed too. Before the fix, any errored worker
// leaked its worktree even when there was nothing in it.
func TestDispatch_Foreground_ErroredCleanWorktreeReclaimed(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)
	d.Agent.Adapter = dispatchErrStreamer{}

	out, err := d.Execute(context.Background(), `{"goal":"x","background":false,"tasks":[
		{"subagent_type":"writer","description":"a","prompt":"p","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"p","files":["beta.txt"]}
	]}`)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if !strings.Contains(out, "2 failed") {
		t.Errorf("expected both workers to fail, got:\n%s", out)
	}
	if !strings.Contains(out, "empty worktree and branch reclaimed") {
		t.Errorf("result should report the reclaim, got:\n%s", out)
	}
	eventuallyNoDispatchWorktrees(t, repoRoot, dispatchWorktreeDirs(d.Agent.Tasks))
}

// TestDispatch_Foreground_ErroredDirtyWorktreeKept guards the recovery
// posture: a failed worker that left UNCOMMITTED work keeps its worktree (and
// branch) so the partial output can be recovered — reclaim must never eat it.
func TestDispatch_Foreground_ErroredDirtyWorktreeKept(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)
	d.Agent.Adapter = dispatchWriteThenErrStreamer{}

	out, err := d.Execute(context.Background(), `{"goal":"x","background":false,"tasks":[
		{"subagent_type":"writer","description":"a","prompt":"TESTWRITE:alpha.txt","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"TESTWRITE:beta.txt","files":["beta.txt"]}
	]}`)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if !strings.Contains(out, "ended without committing") {
		t.Errorf("expected the kept-worktree reason, got:\n%s", out)
	}
	if branches := gitListBranches(t, repoRoot, "worktree-dispatch-*"); len(branches) != 2 {
		t.Errorf("dirty errored branches must be kept, got %v", branches)
	}
	dirs := dispatchWorktreeDirs(d.Agent.Tasks)
	if len(dirs) != 2 {
		t.Fatalf("expected 2 worktree tasks, got %d", len(dirs))
	}
	for _, dir := range dirs {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("dirty worktree %s must be kept for recovery: %v", dir, err)
		}
	}
}

// TestDispatch_Background_EmptyWorktreesReclaimed: same reclaim on the
// detached path, and the async done event explains it (Reclaimed=true) so
// the dock banner doesn't name a branch that no longer exists.
func TestDispatch_Background_EmptyWorktreesReclaimed(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	d := newDispatchToolE2E(t, repoRoot)
	d.Agent.Adapter = dispatchNoopStreamer{}
	d.SupportsBackground = true

	done := make(chan SubagentBackgroundDone, 8)
	d.Agent.SetBackgroundDoneCallback(func(e SubagentBackgroundDone) { done <- e })

	if _, err := d.Execute(context.Background(), `{"goal":"x","tasks":[
		{"subagent_type":"writer","description":"a","prompt":"do nothing","files":["alpha.txt"]},
		{"subagent_type":"writer","description":"b","prompt":"do nothing","files":["beta.txt"]}
	]}`); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	timeout := time.After(5 * time.Second)
	for got := 0; got < 2; got++ {
		select {
		case e := <-done:
			if !e.Reclaimed {
				t.Errorf("no-op worker %s should report Reclaimed, got %+v", e.TaskID[:8], e)
			}
			if e.Committed {
				t.Errorf("no-op worker %s should not report Committed", e.TaskID[:8])
			}
		case <-timeout:
			t.Fatalf("only %d/2 background-done callbacks fired", got)
		}
	}
	eventuallyNoDispatchWorktrees(t, repoRoot, dispatchWorktreeDirs(d.Agent.Tasks))
}

// TestReclaimEmptyWorktree_Conservative: emptiness must be affirmative — a
// missing dir or unset base means "can't tell", and the worktree is kept.
func TestReclaimEmptyWorktree_Conservative(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	ctx := context.Background()

	if reclaimEmptyWorktree(ctx, repoRoot, filepath.Join(t.TempDir(), "nope"), "HEAD") {
		t.Error("missing worktree dir must not report reclaimed")
	}
	if reclaimEmptyWorktree(ctx, repoRoot, repoRoot, "") {
		t.Error("empty base must not reclaim")
	}
	if reclaimEmptyWorktree(ctx, "", repoRoot, "HEAD") {
		t.Error("empty repoRoot must not reclaim")
	}
}

// TestReclaimEmptyDispatchWorktrees_Sweep covers the session-exit sweep: of
// the session's recorded dispatch worktrees, only the provably-empty one is
// removed — dirty (recoverable) and committed (awaiting integrate) worktrees
// survive, and a task whose dir is already gone is skipped without error.
func TestReclaimEmptyDispatchWorktrees_Sweep(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	ctx := context.Background()
	base, err := gitOutput(ctx, repoRoot, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	base = strings.TrimSpace(base)

	mkWorktree := func(name string) string {
		t.Helper()
		dir := worktree.Dir(repoRoot, name)
		if _, err := gitOutput(ctx, repoRoot, "worktree", "add", "-b", worktree.Branch(name), dir, base); err != nil {
			t.Fatalf("worktree add %s: %v", name, err)
		}
		return dir
	}

	empty := mkWorktree("sweep-empty")
	dirty := mkWorktree("sweep-dirty")
	if err := os.WriteFile(filepath.Join(dirty, "partial.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	committed := mkWorktree("sweep-committed")
	if err := os.WriteFile(filepath.Join(committed, "done.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "work"}} {
		if _, err := gitOutput(ctx, committed, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	reg := subagents.NewRegistry()
	for i, wt := range []string{empty, dirty, committed, filepath.Join(t.TempDir(), "already-gone")} {
		reg.Add(&subagents.Task{
			ID:       fmt.Sprintf("task-%d", i),
			Status:   subagents.TaskCanceled,
			Worktree: wt,
			Base:     base,
			Started:  time.Now(),
		})
	}
	// A non-worktree subagent task must be ignored.
	reg.Add(&subagents.Task{ID: "plain", Status: subagents.TaskCompleted, Started: time.Now()})

	if n := ReclaimEmptyDispatchWorktrees(ctx, reg); n != 1 {
		t.Errorf("sweep removed %d worktrees, want 1", n)
	}
	if _, err := os.Stat(empty); err == nil {
		t.Errorf("empty worktree %s should be removed", empty)
	}
	if _, err := os.Stat(dirty); err != nil {
		t.Errorf("dirty worktree %s must be kept: %v", dirty, err)
	}
	if _, err := os.Stat(committed); err != nil {
		t.Errorf("committed worktree %s must be kept: %v", committed, err)
	}
	branches := gitListBranches(t, repoRoot, "worktree-sweep-*")
	if len(branches) != 2 {
		t.Errorf("want the empty branch deleted and the other two kept, got %v", branches)
	}
	for _, br := range branches {
		if br == "worktree-sweep-empty" {
			t.Errorf("branch %s should have been deleted with its worktree", br)
		}
	}

	if got := ReclaimEmptyDispatchWorktrees(ctx, nil); got != 0 {
		t.Errorf("nil registry: got %d, want 0", got)
	}
}
