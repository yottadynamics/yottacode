package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestApplyGitPatchCancellationDoesNotRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stderr, err, retried := applyGitPatch(ctx, t.TempDir(), "/nonexistent.patch")
	if err == nil || retried {
		t.Fatalf("applyGitPatch cancellation = stderr %q, err %v, retried %v", stderr, err, retried)
	}
}

func TestApplyGitPatchTimeoutStopsAndDoesNotRetry(t *testing.T) {
	patch := filepath.Join(t.TempDir(), "missing.diff")
	if err := os.WriteFile(patch, []byte("not a patch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	// Ensure the deadline is observable before the subprocess is started.
	<-ctx.Done()
	_, err, retried := applyGitPatch(ctx, t.TempDir(), patch)
	if err == nil || retried {
		t.Fatalf("applyGitPatch timeout err=%v retried=%v", err, retried)
	}
}

func TestApplyDiffToolTimeoutIsConfigurable(t *testing.T) {
	if defaultApplyDiffToolTimeout <= 0 || applyDiffToolTimeout <= 0 {
		t.Fatal("apply diff timeout must be positive")
	}
	if strings.TrimSpace(applyPatchFailureHint("patch does not apply")) == "" {
		t.Fatal("patch failure hint unexpectedly empty")
	}
}

func TestApplyDiffToolTimeoutReturnsPromptly(t *testing.T) {
	old := applyDiffToolTimeout
	applyDiffToolTimeout = time.Nanosecond
	t.Cleanup(func() { applyDiffToolTimeout = old })

	tmp := t.TempDir()
	path := filepath.Join(tmp, "a.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ApplyDiffTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	args := `{"diff":"--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old\n+new\n"}`
	start := time.Now()
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want timeout", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
}
