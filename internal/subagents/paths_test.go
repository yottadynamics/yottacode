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

// TestTranscriptDirFor_DoesNotCreateDir is the regression guard for the
// phantom-project-dir bug: startup wiring resolves the transcript dir
// once per session via TranscriptDirFor, so resolution MUST be
// side-effect-free. If it created the dir (the old EnsureTranscriptDir
// behavior), every project ever opened would get an empty
// ~/.yottacode/memory/projects/<slug>/ even with no subagent run.
// openTranscript MkdirAlls lazily on the first dispatch instead.
func TestTranscriptDirFor_DoesNotCreateDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YOTTACODE_HOME", "")
	cwd := filepath.Join(t.TempDir(), "demo")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	dir, err := TranscriptDirFor(cwd)
	if err != nil {
		t.Fatalf("TranscriptDirFor: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("TranscriptDirFor created %q (stat err = %v); resolution must not touch disk", dir, err)
	}
	// The project memory parent must also stay absent — that is the dir
	// whose phantom creation polluted the memory tree.
	projDir, err := memory.ProjectMemoryDir(cwd)
	if err != nil {
		t.Fatalf("ProjectMemoryDir: %v", err)
	}
	if _, err := os.Stat(projDir); !os.IsNotExist(err) {
		t.Errorf("project memory dir %q exists after mere resolution (stat err = %v)", projDir, err)
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
