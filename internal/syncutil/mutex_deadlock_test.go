//go:build deadlock

package syncutil

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestDeadlockDetectorCatchesRecursiveLock proves that the deadlock build tag
// does more than compile: the child must terminate through go-deadlock's
// recursive-lock path and print both acquisition sites.
func TestDeadlockDetectorCatchesRecursiveLock(t *testing.T) {
	if os.Getenv("YOTTACODE_DEADLOCK_PROBE") == "1" {
		var mu Mutex
		mu.Lock()
		mu.Lock()
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDeadlockDetectorCatchesRecursiveLock$")
	cmd.Env = append(os.Environ(), "YOTTACODE_DEADLOCK_PROBE=1")
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("recursive-lock child timed out; detector did not terminate it:\n%s", output)
	}
	if err == nil {
		t.Fatalf("recursive-lock child exited successfully; detector output:\n%s", output)
	}
	text := string(output)
	for _, want := range []string{"POTENTIAL DEADLOCK", "Recursive locking", "Previous place where the lock was grabbed"} {
		if !strings.Contains(text, want) {
			t.Fatalf("detector output missing %q:\n%s", want, text)
		}
	}
}
