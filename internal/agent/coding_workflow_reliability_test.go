package agent

import (
	"context"
	"strings"
	"testing"
)

func TestApplyGitPatchCancellationDoesNotRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stderr, err, retried := applyGitPatch(ctx, t.TempDir(), "/nonexistent.patch")
	if err == nil || retried {
		t.Fatalf("applyGitPatch cancellation = stderr %q, err %v, retried %v", stderr, err, retried)
	}
}

func TestApplyDiffToolTimeoutIsConfigurable(t *testing.T) {
	if applyDiffToolTimeout <= 0 {
		t.Fatal("apply diff timeout must be positive")
	}
	if strings.TrimSpace(applyPatchFailureHint("patch does not apply")) == "" {
		t.Fatal("patch failure hint unexpectedly empty")
	}
}
