package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/config"
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
	m.rebuildSystemPromptForTurn("should I use ripgrep")

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
	m.rebuildSystemPromptForTurn("anything")

	got := m.sess.Messages[0].Content
	if !strings.Contains(got, "## Prior session context (summarized)") {
		t.Errorf("summary heading should survive rebuild; got %q", got)
	}
	if !strings.Contains(got, "shipped feature X yesterday") {
		t.Errorf("summary body should survive rebuild; got %q", got)
	}
}

func TestRebuildSystemPromptForTurn_NoSystemMessageNoOp(t *testing.T) {
	m := Model{
		cwd:              t.TempDir(),
		baseSystemPrompt: "BASE",
		fileCfg:          config.Config{Retrieval: config.RetrievalConfig{Enabled: true}},
		sess:             &session.Session{Messages: nil},
	}
	// Should not panic, should not insert a system message out of thin air.
	m.rebuildSystemPromptForTurn("anything")
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
