package subagents

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yottadynamics/yottacode/internal/memory"
)

// TranscriptDirFor must resolve to the subagents/ subdir INSIDE the
// project's memory dir — the layout contract that keeps all per-project
// agent state discoverable from one `ls ~/.yottacode/memory` tree.
func TestTranscriptDirFor_NestsInsideProjectMemoryDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YOTTACODE_HOME", "")
	cwd := filepath.Join(t.TempDir(), "demo")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := TranscriptDirFor(cwd)
	if err != nil {
		t.Fatalf("TranscriptDirFor: %v", err)
	}
	memDir, err := memory.ProjectMemoryDir(cwd)
	if err != nil {
		t.Fatalf("ProjectMemoryDir: %v", err)
	}
	if want := filepath.Join(memDir, "subagents"); got != want {
		t.Errorf("TranscriptDirFor = %q, want %q", got, want)
	}
}

func TestTranscriptDirFor_HonorsYottacodeHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YOTTACODE_HOME", "")
	override := t.TempDir()
	t.Setenv("YOTTACODE_HOME", override)
	cwd := filepath.Join(t.TempDir(), "demo")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := TranscriptDirFor(cwd)
	if err != nil {
		t.Fatalf("TranscriptDirFor: %v", err)
	}
	want := filepath.Join(override, "memory", "projects", memory.ProjectSlug(cwd), "subagents")
	if got != want {
		t.Errorf("TranscriptDirFor = %q, want %q", got, want)
	}
}
