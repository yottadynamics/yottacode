package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/session"
)

// seedSessions plants three saved sessions with predictable shapes
// in $HOME/.yottacode/sessions/, returning them oldest → newest.
// Tests use this to drive the cobra subcommands against a known
// on-disk state without touching the user's real home dir.
func seedSessions(t *testing.T) []*session.Session {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	a, err := session.New("model-a", "/cwd")
	if err != nil {
		t.Fatalf("New a: %v", err)
	}
	a.Name = "alpha"
	a.Messages = []adapter.Message{
		{Role: adapter.RoleUser, Content: "hello from alpha"},
		{Role: adapter.RoleAssistant, Content: "hi alpha"},
	}
	if err := a.Save(); err != nil {
		t.Fatalf("Save a: %v", err)
	}

	b, err := session.New("model-b", "/cwd")
	if err != nil {
		t.Fatalf("New b: %v", err)
	}
	// Needs a real exchange like a: `sessions list` is sourced from
	// session.List, which skips system-only shells because there's
	// nothing in them to resume.
	b.Messages = []adapter.Message{
		{Role: adapter.RoleUser, Content: "hello from beta"},
		{Role: adapter.RoleAssistant, Content: "hi beta"},
	}
	if err := b.Save(); err != nil {
		t.Fatalf("Save b: %v", err)
	}

	return []*session.Session{a, b}
}

func TestSessionsList_PlainTextRendersOneRowPerSession(t *testing.T) {
	seeds := seedSessions(t)

	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"sessions", "list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	body := out.String()
	for _, s := range seeds {
		if !strings.Contains(body, s.ID) {
			t.Errorf("list missing session id %q:\n%s", s.ID, body)
		}
	}
	// Named session shows the name, unnamed shows the placeholder.
	if !strings.Contains(body, "alpha") {
		t.Errorf("named session should render its name:\n%s", body)
	}
	// One non-blank line per session.
	lines := 0
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			lines++
		}
	}
	if lines != len(seeds) {
		t.Errorf("expected %d rows, got %d:\n%s", len(seeds), lines, body)
	}
}

func TestSessionsList_JSONShape(t *testing.T) {
	seeds := seedSessions(t)

	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"sessions", "list", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	var got []session.SessionInfo
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("decode json: %v\n%s", err, out.String())
	}
	if len(got) != len(seeds) {
		t.Fatalf("expected %d entries, got %d", len(seeds), len(got))
	}
	// session.List returns newest-first; the second-seeded session
	// has the larger id and should land at index 0.
	if got[0].ID < got[1].ID {
		t.Errorf("list should be newest-first; got %q before %q", got[0].ID, got[1].ID)
	}
}

func TestSessionsList_EmptyDirReportsCleanly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"sessions", "list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out.String(), "no saved sessions") {
		t.Errorf("empty dir should report clean message; got %q", out.String())
	}
}

func TestSessionsRename_UpdatesNameOnDisk(t *testing.T) {
	seeds := seedSessions(t)
	target := seeds[1] // the unnamed one

	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"sessions", "rename", target.ID, "renamed-from-cli"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	loaded, err := session.Load(target.ID)
	if err != nil {
		t.Fatalf("Load after rename: %v", err)
	}
	if loaded.Name != "renamed-from-cli" {
		t.Errorf("rename should set Name on disk; got %q", loaded.Name)
	}
	// And the friendlier name now resolves the same session.
	byName, err := session.Load("renamed-from-cli")
	if err != nil {
		t.Fatalf("Load by name after rename: %v", err)
	}
	if byName.ID != target.ID {
		t.Errorf("Load(name) returned wrong session: %q vs %q", byName.ID, target.ID)
	}
}

func TestSessionsRename_UnknownIDErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"sessions", "rename", "does-not-exist", "newname"})
	// SilenceErrors=false would dump usage; tests just want the
	// error result.
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	if err := cmd.Execute(); err == nil {
		t.Errorf("rename of unknown session should error")
	}
}

func TestSessionsExport_WritesMarkdownToExplicitPath(t *testing.T) {
	seeds := seedSessions(t)
	tmp := t.TempDir()
	out := filepath.Join(tmp, "alpha.md")

	cmd := newCLI()
	var stdout strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"sessions", "export", "alpha", out})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("export file not written: %v", err)
	}
	for _, want := range []string{seeds[0].ID, "alpha", "hello from alpha", "hi alpha"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("export missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(stdout.String(), "wrote") {
		t.Errorf("export should print byte count to stdout; got %q", stdout.String())
	}
}

func TestSessionsExport_RefusesOverwriteWithoutForce(t *testing.T) {
	seedSessions(t)
	tmp := t.TempDir()
	dest := filepath.Join(tmp, "out.md")
	if err := os.WriteFile(dest, []byte("existing content"), 0o644); err != nil {
		t.Fatalf("seed dest: %v", err)
	}

	cmd := newCLI()
	var stdout strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"sessions", "export", "alpha", dest})
	if err := cmd.Execute(); err == nil {
		t.Errorf("export to existing path should error without --force")
	}
	body, _ := os.ReadFile(dest)
	if string(body) != "existing content" {
		t.Errorf("destination should be untouched without --force; got %q", body)
	}

	// With --force the write goes through.
	cmd2 := newCLI()
	var stdout2 strings.Builder
	cmd2.SetOut(&stdout2)
	cmd2.SetErr(&stdout2)
	cmd2.SetArgs([]string{"sessions", "export", "alpha", dest, "--force"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("export --force: %v", err)
	}
	body, _ = os.ReadFile(dest)
	if !strings.Contains(string(body), "alpha") {
		t.Errorf("--force should overwrite with the export markdown; got %q", body)
	}
}

func TestSessionsExport_WritesJSONLForJSONLPath(t *testing.T) {
	seedSessions(t)
	tmp := t.TempDir()
	out := filepath.Join(tmp, "alpha.jsonl")

	cmd := newCLI()
	var stdout strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"sessions", "export", "alpha", out})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("export file not written: %v", err)
	}
	if !strings.Contains(string(body), `"type":"session"`) || !strings.Contains(string(body), `"type":"user"`) {
		t.Fatalf("exported JSONL missing structured records:\n%s", body)
	}
	if !strings.Contains(stdout.String(), "wrote log") {
		t.Errorf("JSONL export should report log output; got %q", stdout.String())
	}
}

func TestSessionsExport_DefaultsToCwdWhenPathOmitted(t *testing.T) {
	seedSessions(t)
	tmp := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	cmd := newCLI()
	var stdout strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"sessions", "export", "alpha"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var found string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			found = e.Name()
			break
		}
	}
	if found == "" {
		t.Fatalf("expected a .md file in cwd, found none in %v", entries)
	}
	if !strings.HasPrefix(found, "alpha-") {
		t.Errorf("default filename should lead with the session name; got %q", found)
	}
}
