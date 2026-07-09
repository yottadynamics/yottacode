package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateWritePath_OwnedPathsPinsDispatchWrites checks the dispatch
// ownership boundary: a worker's mutating filesystem tools may write inside
// its declared files/directories, but not elsewhere in the same worktree.
func TestValidateWritePath_OwnedPathsPinsDispatchWrites(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "owned-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := WritePathOptions{
		Cwd:        NewCwdRef(cwd),
		OwnedPaths: []string{"owned.txt", "owned-dir/"},
	}

	for _, rel := range []string{"owned.txt", "owned-dir/generated.go"} {
		if err := ValidateWritePath(filepath.Join(cwd, rel), opts); err != nil {
			t.Errorf("ValidateWritePath(%s) denied owned path: %v", rel, err)
		}
	}

	err := ValidateWritePath(filepath.Join(cwd, "other.txt"), opts)
	if err == nil || !strings.Contains(err.Error(), "owned files") {
		t.Fatalf("expected owned-files denial for out-of-scope write, got %v", err)
	}
}
