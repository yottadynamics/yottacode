package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/filerefs"
)

// lastUserMessage returns the most recent user message in the session.
func lastUserMessage(t *testing.T, m Model) adapter.Message {
	t.Helper()
	for i := len(m.sess.Messages) - 1; i >= 0; i-- {
		if m.sess.Messages[i].Role == adapter.RoleUser {
			return m.sess.Messages[i]
		}
	}
	t.Fatal("no user message in session")
	return adapter.Message{}
}

// The @file pipeline as the TUI drives it: contents ride the USER message
// (data, not system authority), the system prompt is never edited (so the
// provider prompt cache stays valid), the transcript and history keep what
// the user typed, and a failed ref is annotated rather than blocking.
func TestStartTurn_AttachesFilesToUserMessageNotSystemPrompt(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "demo.go"), "package demo\n\nfunc Hi() {}\n")

	m := newTestModel(t)
	m.cfg.Adapter = stubAdapterNoStream{}
	m.cwd = cwd
	m.sess.Messages = []adapter.Message{{Role: adapter.RoleSystem, Content: "BASE PROMPT"}}
	sysBefore := m.sess.Messages[0].Content

	out, _ := m.startTurn("explain @demo.go and @nope.go")
	m2 := out.(Model)
	defer m2.turnCancel()

	user := lastUserMessage(t, m2)
	if !strings.Contains(user.Content, "package demo") || !strings.Contains(user.Content, filerefs.Marker) {
		t.Fatalf("file body not attached to the user message: %q", user.Content)
	}
	if !strings.Contains(user.Content, "could not load") {
		t.Fatalf("failed ref not annotated: %q", user.Content)
	}
	if !strings.HasPrefix(user.Content, "explain demo.go and @nope.go") {
		t.Fatalf("rewritten text wrong: %q", user.Content)
	}
	if strings.Contains(m2.sess.Messages[0].Content, "package demo") || strings.Contains(m2.sess.Messages[0].Content, filerefs.Marker) {
		t.Fatalf("file contents leaked into the system prompt")
	}
	if m2.sess.Messages[0].Content != sysBefore {
		t.Fatalf("system prompt was edited by an @ turn")
	}
	if got := m2.activeFileRefs; len(got) != 2 {
		t.Fatalf("working set = %+v", got)
	}
}

// Regression: history used to store the rewritten text, so recalling an
// entry and resending it attached nothing.
func TestStartTurn_HistoryKeepsAtTokens(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "a.go"), "package a")
	m := newTestModel(t)
	m.cfg.Adapter = stubAdapterNoStream{}
	m.cwd = cwd

	out, _ := m.startTurn("explain @a.go")
	m2 := out.(Model)
	defer m2.turnCancel()

	if n := len(m2.inputHistory); n == 0 || m2.inputHistory[n-1] != "explain @a.go" {
		t.Fatalf("history = %q, want the text as typed", m2.inputHistory)
	}
	if got := m2.transcript.String(); !strings.Contains(got, "@a.go") {
		t.Fatalf("transcript should show the typed @token: %q", got)
	}
}

// Attachments persist in history, so later turns still see the file; a
// turn with no refs adds no block and clears the working set.
func TestStartTurn_NoRefsTurnAddsNoBlockAndClearsWorkingSet(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "a.go"), "package a")
	m := newTestModel(t)
	m.cfg.Adapter = stubAdapterNoStream{}
	m.cwd = cwd
	m.activeFileRefs = []filerefs.Ref{{Path: "old.go"}}

	out, _ := m.startTurn("plain question")
	m2 := out.(Model)
	defer m2.turnCancel()

	if got := lastUserMessage(t, m2).Content; got != "plain question" {
		t.Fatalf("content = %q", got)
	}
	if m2.activeFileRefs != nil {
		t.Fatalf("working set should clear, got %+v", m2.activeFileRefs)
	}
}

// Outside-project paths are allowed interactively (the user typed them)
// and say so in the notice; the deny-list still applies.
func TestStartTurn_OutsideProjectFlaggedInNotice(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Adapter = stubAdapterNoStream{}
	m.cwd = t.TempDir()
	other := filepath.Join(t.TempDir(), "notes.txt")
	mustWrite(t, other, "hello")

	out, _ := m.startTurn(`read @"` + other + `"`)
	m2 := out.(Model)
	defer m2.turnCancel()

	if got := m2.transcript.String(); !strings.Contains(got, "outside project") {
		t.Fatalf("notice should flag an outside-project attachment: %q", got)
	}
	if !strings.Contains(lastUserMessage(t, m2).Content, "hello") {
		t.Fatal("outside file not attached")
	}
}

// Rebuilding the transcript (resume) shows what the user typed, not the
// attachment block.
func TestRebuildTranscript_HidesAttachmentBlock(t *testing.T) {
	m := newTestModel(t)
	content := filerefs.Attach("look at a.go", []filerefs.Ref{{Token: "@a.go", Path: "a.go", Loaded: true, Content: "SECRET-BODY-XYZ"}})
	m.sess.Messages = append(m.sess.Messages, adapter.Message{Role: adapter.RoleUser, Content: content})
	m.transcript.Reset()

	rebuildTranscript(&m)

	got := m.transcript.String()
	if !strings.Contains(got, "look at a.go") || strings.Contains(got, "SECRET-BODY-XYZ") {
		t.Fatalf("transcript = %q", got)
	}
}

func TestPrepareQueuedMessage_ResolvesRefsAtDelivery(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "a.go"), "package a")
	m := newTestModel(t)
	m.cwd = cwd

	prep := m.prepareQueuedMessage()(agent.UserMessage{Content: "also check @a.go and @zzz.go"})

	if !strings.HasPrefix(prep.Content, "also check a.go and @zzz.go") || !strings.Contains(prep.Content, "package a") {
		t.Fatalf("content = %q", prep.Content)
	}
	if len(prep.Notices) != 2 || prep.Notices[0].Error || !prep.Notices[1].Error {
		t.Fatalf("notices = %+v", prep.Notices)
	}
}

func TestUserMessageAppended_RendersAttachNotices(t *testing.T) {
	m := newTestModel(t)
	m.renderAttachNotices([]agent.AttachNotice{
		{Text: "attached @a.go (9 B)"},
		{Text: "could not attach @b.go: no such file or directory", Error: true},
	})
	got := m.transcript.String()
	if !strings.Contains(got, "attached @a.go") || !strings.Contains(got, "could not attach @b.go") {
		t.Fatalf("transcript = %q", got)
	}
}

func TestClearCommandDropsFileRefs(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = []adapter.Message{{Role: adapter.RoleSystem, Content: "SYS"}}
	m.activeFileRefs = []filerefs.Ref{{Token: "@x", Path: "x", Loaded: true}}

	m, _ = cmdClear(m, nil)

	if m.activeFileRefs != nil {
		t.Fatalf("active refs survived /clear: %#v", m.activeFileRefs)
	}
	if len(m.sess.Messages) != 1 || m.sess.Messages[0].Content != "SYS" {
		t.Fatalf("system prompt changed by /clear: %#v", m.sess.Messages)
	}
}

// Sessions saved by older versions carried the block in the system prompt;
// it is stripped on /clear (and resume) so it cannot linger.
func TestClearCommandStripsLegacySystemBlock(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = []adapter.Message{{Role: adapter.RoleSystem, Content: "SYS\n\n" + filerefs.Marker + "\n\n### @x\nbody"}}

	m, _ = cmdClear(m, nil)

	if strings.Contains(m.sess.Messages[0].Content, filerefs.Marker) {
		t.Fatalf("legacy block survived: %q", m.sess.Messages[0].Content)
	}
}

// TestDirEntryCount validates the helper used by the /context working-set
// line; off-by-one bugs here would mis-report counts.
func TestDirEntryCount(t *testing.T) {
	listing := "(directory listing)\nf a.go\nd sub\nf b.txt"
	if got := dirEntryCount(listing); got != 3 {
		t.Fatalf("dirEntryCount = %d, want 3", got)
	}
	if got := dirEntryCount(""); got != 0 {
		t.Fatalf("empty listing should be 0, got %d", got)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
