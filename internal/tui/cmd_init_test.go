package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildInitPrompt_DraftMode covers the fresh-repo path: YOTTACODE.md
// does not exist, so the prompt instructs the agent to create it from
// scratch. The wording matters here — the agent's behavior pivots on
// "Create" vs "Refresh," so we lock the verb.
func TestBuildInitPrompt_DraftMode(t *testing.T) {
	cwd := t.TempDir()
	prompt := buildInitPrompt(cwd)

	if !strings.HasPrefix(prompt, "Create the per-repo context file") {
		t.Errorf("prompt should start with 'Create' when YOTTACODE.md is absent; got: %q", firstLine(prompt))
	}
	if !strings.Contains(prompt, ".yottacode/YOTTACODE.md") {
		t.Errorf("prompt should name the target path; got: %q", prompt)
	}
	if !strings.Contains(prompt, "list_dir") || !strings.Contains(prompt, "write_file") {
		t.Errorf("prompt should reference the tools the agent will use; got: %q", prompt)
	}
	if strings.Contains(prompt, "already exists") {
		t.Errorf("prompt should not mention 'already exists' when file is absent; got: %q", prompt)
	}
	if !strings.Contains(prompt, "Do not stop after creating a todo card") {
		t.Errorf("prompt should prevent todo-card approval handshakes; got: %q", prompt)
	}
	if strings.Contains(prompt, "Proceed with implementation?") {
		t.Errorf("prompt should not trigger a separate implementation confirmation; got: %q", prompt)
	}
}

// TestBuildInitPrompt_ScopesToCwd guards against the regression that
// produced `list_dir(path="/")` — the model interpreted "scan the
// repo" as "scan the filesystem root." The prompt must explicitly pin
// scope to the project root and warn off "/" so future prompt edits
// don't quietly drop the guardrail.
func TestBuildInitPrompt_ScopesToCwd(t *testing.T) {
	prompt := buildInitPrompt(t.TempDir())
	if !strings.Contains(prompt, `never call
list_dir with path "/"`) {
		t.Errorf("prompt must warn against list_dir path=\"/\"; got: %q", prompt)
	}
	if !strings.Contains(prompt, `project root is "."`) {
		t.Errorf("prompt must pin scope to '.'; got: %q", prompt)
	}
}

// TestBuildInitPrompt_RefreshMode covers the file-exists path: the
// prompt must pivot to a refresh stance so the agent doesn't flatten
// hand-edited content.
func TestBuildInitPrompt_RefreshMode(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".yottacode"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".yottacode", "YOTTACODE.md"), []byte("# Existing\n"), 0o644); err != nil {
		t.Fatalf("seed YOTTACODE.md: %v", err)
	}

	prompt := buildInitPrompt(cwd)
	if !strings.HasPrefix(prompt, "Refresh the per-repo context file") {
		t.Errorf("prompt should start with 'Refresh' when YOTTACODE.md is present; got: %q", firstLine(prompt))
	}
	if !strings.Contains(prompt, "already exists") {
		t.Errorf("refresh prompt should warn about preserving the existing file; got: %q", prompt)
	}
	if !strings.Contains(prompt, "Do not flatten") {
		t.Errorf("refresh prompt should explicitly forbid flattening; got: %q", prompt)
	}
}

// TestSlash_InitCommandRegistered verifies the slash-command registry
// carries the new /init handler. A stale registration was the bug
// behind the original /init→/setup rename, so we lock both names.
func TestSlash_InitCommandRegistered(t *testing.T) {
	if findSlash("init") == nil {
		t.Errorf("expected /init in the slash registry")
	}
	if findSlash("setup") == nil {
		t.Errorf("expected /setup in the slash registry")
	}
}

// TestSlash_InitBailsWhenTurnActive guards the early-return path. We
// can't drive a full /init run in unit tests (needs a live adapter),
// but the turnActive guard is pure UI logic and worth pinning so a
// future refactor doesn't quietly drop it.
func TestSlash_InitBailsWhenTurnActive(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	out, cmd := cmdInit(m, nil)
	if cmd != nil {
		t.Errorf("cmdInit should not start a new turn while one is active")
	}
	if !strings.Contains(out.transcript.String(), "a turn is already running") {
		t.Errorf("expected 'a turn is already running' notice; got: %q", out.transcript.String())
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
