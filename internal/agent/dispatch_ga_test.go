package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDispatch_MaxWorkersLowersPerCallCap(t *testing.T) {
	for _, tc := range []struct {
		name       string
		maxWorkers int
		want       int
	}{
		{"unset uses ceiling", 0, MaxDispatchTasksPerCall},
		{"lower applies", 2, 2},
		{"above ceiling is clamped", MaxDispatchTasksPerCall + 5, MaxDispatchTasksPerCall},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &DispatchTool{MaxWorkers: tc.maxWorkers}
			if got := d.maxWorkers(); got != tc.want {
				t.Fatalf("maxWorkers() = %d, want %d", got, tc.want)
			}
		})
	}

	d := &DispatchTool{Enabled: true, MaxWorkers: 2}
	out, _ := d.Execute(context.Background(), `{"goal":"g","tasks":[
		{"subagent_type":"writer","description":"a","prompt":"p","files":["a"]},
		{"subagent_type":"writer","description":"b","prompt":"p","files":["b"]},
		{"subagent_type":"writer","description":"c","prompt":"p","files":["c"]}]}`)
	if !strings.Contains(out, "at most 2 concurrent subtasks (got 3)") {
		t.Fatalf("expected cap error, got %q", out)
	}
}

// TestIntegrate_LeavesDirtyUserCheckoutUntouched pins that integrate merges
// only inside its own integration worktree: uncommitted edits and the
// checked-out branch/HEAD in the user's checkout are unchanged.
func TestIntegrate_LeavesDirtyUserCheckoutUntouched(t *testing.T) {
	repoRoot := dispatchTestRepo(t)
	ctx := context.Background()
	gitCommitFileOnBranch(t, repoRoot, "feat-a", "a.txt", "a", "main")
	gitCommitFileOnBranch(t, repoRoot, "feat-b", "b.txt", "b", "main")

	dirty := filepath.Join(repoRoot, "wip.txt")
	if err := os.WriteFile(dirty, []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}
	headBefore, _ := gitOutput(ctx, repoRoot, "rev-parse", "HEAD")
	branchBefore, _ := gitOutput(ctx, repoRoot, "rev-parse", "--abbrev-ref", "HEAD")

	it := &IntegrateTool{Cwd: NewCwdRef(repoRoot), Enabled: true}
	out, err := it.Execute(ctx, `{"branches":["feat-a","feat-b"]}`)
	if err != nil || !strings.Contains(out, "Integrated 2 branch") {
		t.Fatalf("integrate: err=%v out=%s", err, out)
	}

	if got, _ := os.ReadFile(dirty); string(got) != "uncommitted" {
		t.Errorf("dirty file changed: %q", got)
	}
	for _, f := range []string{"a.txt", "b.txt"} {
		if _, e := os.Stat(filepath.Join(repoRoot, f)); e == nil {
			t.Errorf("%s leaked into the user's checkout", f)
		}
	}
	if h, _ := gitOutput(ctx, repoRoot, "rev-parse", "HEAD"); h != headBefore {
		t.Errorf("HEAD moved: %s -> %s", headBefore, h)
	}
	if b, _ := gitOutput(ctx, repoRoot, "rev-parse", "--abbrev-ref", "HEAD"); b != branchBefore {
		t.Errorf("branch changed: %s -> %s", branchBefore, b)
	}
}

// TestIntegrate_SerializesConcurrentCalls holds the integrate slot and checks
// a second call blocks until released, and that a cancelled waiter bails out.
func TestIntegrate_SerializesConcurrentCalls(t *testing.T) {
	it := &IntegrateTool{Cwd: NewCwdRef(t.TempDir()), Enabled: true}
	it.semOnce.Do(func() { it.sem = make(chan struct{}, 1) })
	it.sem <- struct{}{} // another integrate in flight

	cctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() {
		out, _ := it.Execute(cctx, `{"branches":["x"]}`)
		done <- out
	}()
	select {
	case out := <-done:
		t.Fatalf("second integrate ran concurrently: %q", out)
	case <-time.After(150 * time.Millisecond):
	}
	cancel()
	select {
	case out := <-done:
		if !strings.Contains(out, "waiting for another integrate") {
			t.Fatalf("got %q", out)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled waiter did not return")
	}
}
