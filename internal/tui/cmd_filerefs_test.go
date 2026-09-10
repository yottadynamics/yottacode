package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/filerefs"
)

// TestInjectFileRefs_RewritesSystemPrompt is the contract test for the
// @file pipeline as it appears in the TUI: a model with a system
// message, a Load result with one good ref and one bad ref, and the
// expectation that injectFileRefs rewrites the system prompt with the
// auto-injected block (good content present, bad ref annotated as a
// load failure) without losing the original base prompt content.
func TestInjectFileRefs_RewritesSystemPrompt(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = []adapter.Message{
		{Role: adapter.RoleSystem, Content: "BASE PROMPT"},
	}
	refs := []filerefs.Ref{
		{Token: "@hello.go", Path: "hello.go", Loaded: true, Content: "package main\n"},
		{Token: "@nope.go", Path: "nope.go", Error: "no such file or directory"},
	}

	m.injectFileRefs(refs)

	sys := m.sess.Messages[0].Content
	if !strings.HasPrefix(sys, "BASE PROMPT") {
		t.Fatalf("base prompt not preserved: %q", sys)
	}
	if !strings.Contains(sys, filerefs.Marker) {
		t.Fatalf("expected marker %q in prompt", filerefs.Marker)
	}
	if !strings.Contains(sys, "package main") {
		t.Fatalf("expected loaded file content in prompt")
	}
	if !strings.Contains(sys, "could not load") {
		t.Fatalf("expected failed-load notice in prompt")
	}
}

// TestClearFileRefs_StripsPriorBlock makes sure a turn with no @-refs
// scrubs the previous turn's block from the system prompt — we don't
// want stale content lingering across turns.
func TestClearFileRefs_StripsPriorBlock(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = []adapter.Message{
		{Role: adapter.RoleSystem, Content: "BASE"},
	}
	m.injectFileRefs([]filerefs.Ref{
		{Token: "@x", Path: "x", Loaded: true, Content: "abc"},
	})
	if !strings.Contains(m.sess.Messages[0].Content, filerefs.Marker) {
		t.Fatal("setup: marker should be present")
	}

	m.clearFileRefs()

	if strings.Contains(m.sess.Messages[0].Content, filerefs.Marker) {
		t.Fatalf("marker should have been stripped: %q", m.sess.Messages[0].Content)
	}
	if !strings.HasPrefix(m.sess.Messages[0].Content, "BASE") {
		t.Fatalf("base prompt lost: %q", m.sess.Messages[0].Content)
	}
}

// TestInjectFileRefs_EndToEndWithRealFile drives the full pipeline:
// write a real file, parse the @-token from input, load it, and check
// the system prompt picked up the file body. Catches breaks at any of
// the three layers (parse → load → inject) in one go.
func TestInjectFileRefs_EndToEndWithRealFile(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "demo.go"), "package demo\n\nfunc Hi() {}\n")

	m := newTestModel(t)
	m.cwd = cwd
	m.sess.Messages = []adapter.Message{
		{Role: adapter.RoleSystem, Content: "SYS"},
	}

	refs := filerefs.Parse("explain @demo.go please")
	if len(refs) != 1 {
		t.Fatalf("Parse refs = %d, want 1", len(refs))
	}
	refs = filerefs.Load(refs, m.cwd)
	if !refs[0].Loaded {
		t.Fatalf("expected file to load: %s", refs[0].Error)
	}
	m.injectFileRefs(refs)

	if !strings.Contains(m.sess.Messages[0].Content, "package demo") {
		t.Fatalf("expected file body in system prompt; got %q", m.sess.Messages[0].Content)
	}
}

// TestInjectFileRefs_SnapshotsActiveRefs verifies /context attribution owns its
// slice and that each injection replaces, rather than appends to, the prior turn.
func TestInjectFileRefs_SnapshotsActiveRefs(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = []adapter.Message{{Role: adapter.RoleSystem, Content: "SYS"}}
	refs := []filerefs.Ref{{Token: "@first.go", Path: "first.go", Loaded: true, Size: 10}}

	m.injectFileRefs(refs)
	refs[0].Path = "mutated.go"
	refs = append(refs, filerefs.Ref{Path: "also-mutated.go"})
	if got := len(m.activeFileRefs); got != 1 {
		t.Fatalf("active refs length = %d, want defensive snapshot of 1", got)
	}
	if got := m.activeFileRefs[0].Path; got != "first.go" {
		t.Fatalf("active ref path = %q, want original first.go", got)
	}

	m.injectFileRefs([]filerefs.Ref{{Token: "@second.go", Path: "second.go", Loaded: true}})
	if got := len(m.activeFileRefs); got != 1 || m.activeFileRefs[0].Path != "second.go" {
		t.Fatalf("second injection should replace active refs, got %#v", m.activeFileRefs)
	}
}

// TestClearFileRefs_ClearsActiveSnapshot pins the no-ref turn behavior: prompt
// cleanup and /context attribution must advance together.
func TestClearFileRefs_ClearsActiveSnapshot(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = []adapter.Message{{Role: adapter.RoleSystem, Content: "SYS"}}
	m.injectFileRefs([]filerefs.Ref{{Token: "@x", Path: "x", Loaded: true}})

	m.clearFileRefs()

	if m.activeFileRefs != nil {
		t.Fatalf("active refs should clear on a no-ref turn, got %#v", m.activeFileRefs)
	}
}

func TestClearCommandDropsFileRefs(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = []adapter.Message{{Role: adapter.RoleSystem, Content: "SYS"}}
	m.injectFileRefs([]filerefs.Ref{{Token: "@x", Path: "x", Loaded: true, Content: "body"}})

	m, _ = cmdClear(m, nil)

	if m.activeFileRefs != nil {
		t.Fatalf("active refs survived /clear: %#v", m.activeFileRefs)
	}
	if len(m.sess.Messages) != 1 || strings.Contains(m.sess.Messages[0].Content, filerefs.Marker) {
		t.Fatalf("file-ref block survived /clear: %#v", m.sess.Messages)
	}
	if m.sess.Messages[0].Content != "SYS" {
		t.Fatalf("base system prompt changed: %q", m.sess.Messages[0].Content)
	}
}

// TestDirEntryCount validates the helper used by the muted attached-
// directory notice; off-by-one bugs here would mis-report counts in
// the user-visible status line.
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
