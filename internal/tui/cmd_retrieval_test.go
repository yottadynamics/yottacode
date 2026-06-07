package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/filerefs"
	"github.com/yottadynamics/yottacode/internal/session"
)

func writeUnderDir(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestRebuildSystemPromptForTurn_AppliesRetrieval(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	writeUnderDir(t, filepath.Join(home, ".yottacode", "USER.md"), "be terse")
	memDir := filepath.Join(home, ".yottacode", "memory")
	writeUnderDir(t, filepath.Join(memDir, "ripgrep.md"),
		"---\nname: ripgrep\ntype: reference\ndescription: prefers ripgrep\n---\nProject prefers ripgrep for code search.\n")
	writeUnderDir(t, filepath.Join(memDir, "kubernetes.md"),
		"---\nname: kubernetes\ntype: reference\ndescription: cluster region\n---\nCluster runs in europe-west1.\n")

	m := Model{
		cwd:              cwd,
		baseSystemPrompt: "BASE",
		fileCfg: config.Config{
			Retrieval: config.RetrievalConfig{Enabled: true, TopK: 1, MinScore: 0.05},
		},
		sess: &session.Session{
			Messages: []adapter.Message{
				{Role: adapter.RoleSystem, Content: "stale content"},
			},
		},
	}
	m.rebuildSystemPromptForTurn(context.Background(), "should I use ripgrep")

	got := m.sess.Messages[0].Content
	if !strings.Contains(got, "be terse") {
		t.Errorf("USER section should always be present; got %q", got)
	}
	if !strings.Contains(got, "Project prefers ripgrep") {
		t.Errorf("relevant memory body should be selected; got %q", got)
	}
	if strings.Contains(got, "europe-west1") {
		t.Errorf("irrelevant memory body should be filtered; got %q", got)
	}
	if !strings.Contains(got, "BASE") {
		t.Errorf("base prompt should remain; got %q", got)
	}
}

func TestRebuildSystemPromptForTurn_PreservesSummarySection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	prior := "BASE\n\n## Prior session context (summarized)\nshipped feature X yesterday"
	m := Model{
		cwd:              cwd,
		baseSystemPrompt: "BASE",
		fileCfg: config.Config{
			Retrieval: config.RetrievalConfig{Enabled: true, TopK: 0},
		},
		sess: &session.Session{
			Messages: []adapter.Message{
				{Role: adapter.RoleSystem, Content: prior},
			},
		},
	}
	m.rebuildSystemPromptForTurn(context.Background(), "anything")

	got := m.sess.Messages[0].Content
	if !strings.Contains(got, "## Prior session context (summarized)") {
		t.Errorf("summary heading should survive rebuild; got %q", got)
	}
	if !strings.Contains(got, "shipped feature X yesterday") {
		t.Errorf("summary body should survive rebuild; got %q", got)
	}
}

// TestRebuildSystemPromptForTurn_PreservesFileRefsBlock — regression
// for the retrieval move into the turn goroutine: the rebuild now runs
// AFTER startTurn's fileref injection (it used to run before), so the
// recompose must carry the "Referenced files" block the same way it
// carries the summary block. Without the carry, every @-ref of the
// very turn being started would be silently dropped from the prompt.
// Also pins the canonical tail order: summary, then refs.
func TestRebuildSystemPromptForTurn_PreservesFileRefsBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	prior := "BASE\n\n## Prior session context (summarized)\nshipped feature X" +
		"\n\n" + filerefs.Marker + "\n\n### @docs/foo.md\n```\nfoo body\n```"
	m := Model{
		cwd:              cwd,
		baseSystemPrompt: "BASE",
		fileCfg: config.Config{
			Retrieval: config.RetrievalConfig{Enabled: true, TopK: 0},
		},
		sess: &session.Session{
			Messages: []adapter.Message{
				{Role: adapter.RoleSystem, Content: prior},
			},
		},
	}
	m.rebuildSystemPromptForTurn(context.Background(), "anything")

	got := m.sess.Messages[0].Content
	refsIdx := strings.Index(got, filerefs.Marker)
	if refsIdx < 0 {
		t.Fatalf("fileref block should survive rebuild; got %q", got)
	}
	if !strings.Contains(got, "foo body") {
		t.Errorf("fileref body should survive rebuild; got %q", got)
	}
	sumIdx := strings.Index(got, "## Prior session context (summarized)")
	if sumIdx < 0 {
		t.Fatalf("summary should survive rebuild alongside refs; got %q", got)
	}
	if sumIdx > refsIdx {
		t.Errorf("canonical order is summary before refs (extractSummarySection bounds on the refs marker); got summary@%d refs@%d", sumIdx, refsIdx)
	}
}

func TestExtractFileRefsBlock(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"absent", "BASE\n## User preferences\nfoo", ""},
		{
			"present",
			"BASE\n\n" + filerefs.Marker + "\n\n### @a.md\nbody",
			filerefs.Marker + "\n\n### @a.md\nbody",
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			if got := extractFileRefsBlock(c.in); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestRebuildSystemPrompt_TurnGoroutineWritesUnderLock — regression for
// the retrieval move: rebuildSystemPromptForTurn now runs in the TURN
// goroutine and rewrites sess.Messages[0].Content there. Update-thread
// paths that touch the system message mid-turn (reloadMemoryNow via
// /memory and /model — PreservesTurn=false cancel-then-run, so they
// execute while the canceled turn goroutine is still unwinding) must
// serialize with it via histMu. Run with -race.
func TestRebuildSystemPrompt_TurnGoroutineWritesUnderLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTestModel(t)
	m.sess.Messages = append([]adapter.Message{{Role: adapter.RoleSystem, Content: "sys"}}, m.sess.Messages...)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // turn goroutine: locked rebuild (system Content rewrite)
		defer wg.Done()
		for range 500 {
			m.histMu.Lock()
			m.rebuildSystemPromptForTurn(context.Background(), "query")
			m.histMu.Unlock()
		}
	}()
	wg.Add(1)
	go func() { // Update goroutine: locked memory reload (same Content)
		defer wg.Done()
		for range 500 {
			_, _ = reloadMemoryNow(m, "")
		}
	}()
	wg.Wait()
}

func TestRebuildSystemPromptForTurn_NoSystemMessageNoOp(t *testing.T) {
	m := Model{
		cwd:              t.TempDir(),
		baseSystemPrompt: "BASE",
		fileCfg:          config.Config{Retrieval: config.RetrievalConfig{Enabled: true}},
		sess:             &session.Session{Messages: nil},
	}
	// Should not panic, should not insert a system message out of thin air.
	m.rebuildSystemPromptForTurn(context.Background(), "anything")
	if len(m.sess.Messages) != 0 {
		t.Errorf("expected no messages to be added; got %d", len(m.sess.Messages))
	}
}

func TestExtractSummarySection(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"absent", "BASE\n## User preferences\nfoo", ""},
		{
			"present at end",
			"BASE\n\n## Prior session context (summarized)\nbody text",
			"body text",
		},
		{
			"bounded by refs section",
			"BASE\n\n## Prior session context (summarized)\nbody text\n\n## Referenced files (auto-injected from @-prefixed paths)\nfoo",
			"body text",
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			if got := extractSummarySection(c.in); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
