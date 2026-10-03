package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCwdRefRecoverUsesStableProjectRoot(t *testing.T) {
	root := t.TempDir()
	dead := filepath.Join(root, "worktree")
	if err := os.Mkdir(dead, 0o755); err != nil {
		t.Fatal(err)
	}
	ref := NewCwdRef(root)
	ref.Set(dead)
	if err := os.Remove(dead); err != nil {
		t.Fatal(err)
	}

	recovery, err := ref.Recover()
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if !recovery.Recovered || recovery.Requested != dead || recovery.Actual != root {
		t.Fatalf("unexpected recovery: %+v", recovery)
	}
	if got := ref.Get(); got != root {
		t.Fatalf("Get() = %q, want %q", got, root)
	}
}

func TestCwdRefRecoverWithoutStableRootLeavesPathUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	ref := NewCwdRef(path)
	if recovery, err := ref.Recover(); err != nil || recovery.Recovered {
		t.Fatalf("Recover = %+v, %v; want no recovery", recovery, err)
	}
	if ref.Get() != path {
		t.Fatalf("Get() changed an unrecoverable path: %q", ref.Get())
	}
}
