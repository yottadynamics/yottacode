package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/session"
	"github.com/yottadynamics/yottacode/internal/skills"
)

// TestAppendSkillsSection_FramesWithoutEnumerating locks the metadata
// dedup: the system prompt frames the skills surface and points at the
// Skill tool, but does NOT re-list every skill by name+description. That
// enumeration is the Skill tool schema's job (TestSkillTool_DescriptionListsSkills
// in the agent package) — duplicating it here doubled the always-loaded
// metadata cost for no benefit.
func TestAppendSkillsSection_FramesWithoutEnumerating(t *testing.T) {
	loaded := []skills.Skill{
		{Name: "deep-research", Description: "fan-out web research", Source: skills.ScopeBuiltin},
		{Name: "code-review", Description: "review a PR", Source: skills.ScopeBuiltin},
	}
	got := appendSkillsSection("BASE", loaded)

	if !strings.Contains(got, "# Available skills") || !strings.Contains(got, "`Skill` tool") {
		t.Errorf("section should frame the skills surface and point at the Skill tool:\n%s", got)
	}
	// The per-skill enumeration must be gone — neither the "- name: desc"
	// lines nor the bare skill names should appear in the system prompt.
	for _, sk := range loaded {
		line := "- " + sk.Name + ": " + sk.Description
		if strings.Contains(got, line) {
			t.Errorf("system prompt must not enumerate skills (found %q):\n%s", line, got)
		}
		if strings.Contains(got, sk.Name) {
			t.Errorf("system prompt must not name individual skills (found %q):\n%s", sk.Name, got)
		}
	}

	// Sanity: the Skill tool schema — the surviving copy — still lists them.
	tool := &agent.SkillTool{All: loaded}
	desc := tool.Description()
	for _, sk := range loaded {
		if !strings.Contains(desc, sk.Name) {
			t.Errorf("Skill tool description should still list %q:\n%s", sk.Name, desc)
		}
	}
}

// TestAppendSkillsSection_EmptyIsNoOp keeps the no-skills case clean: no
// header, no framing, just the base prompt.
func TestAppendSkillsSection_EmptyIsNoOp(t *testing.T) {
	if got := appendSkillsSection("BASE", nil); got != "BASE" {
		t.Errorf("no skills should leave the base prompt untouched, got %q", got)
	}
}

func TestResumeHint_EmptySessionReturnsBlank(t *testing.T) {
	sess := &session.Session{ID: "20260101-000000.000000"}
	if got := resumeHint(sess); got != "" {
		t.Fatalf("empty session should suppress hint, got %q", got)
	}
}

func TestResumeHint_SystemOnlyReturnsBlank(t *testing.T) {
	sess := &session.Session{
		ID: "20260101-000000.000000",
		Messages: []adapter.Message{
			{Role: adapter.RoleSystem, Content: "you are helpful"},
		},
	}
	if got := resumeHint(sess); got != "" {
		t.Fatalf("system-only session should suppress hint, got %q", got)
	}
}

func TestResumeHint_UsesIDWhenNameUnset(t *testing.T) {
	sess := &session.Session{
		ID: "20260101-000000.000000",
		Messages: []adapter.Message{
			{Role: adapter.RoleUser, Content: "hello"},
		},
	}
	got := resumeHint(sess)
	if !strings.Contains(got, "yottacode sessions resume 20260101-000000.000000") {
		t.Fatalf("hint should reference id, got %q", got)
	}
}

func TestResumeHint_PrefersNameOverID(t *testing.T) {
	sess := &session.Session{
		ID:   "20260101-000000.000000",
		Name: "feature-foo",
		Messages: []adapter.Message{
			{Role: adapter.RoleUser, Content: "hello"},
		},
	}
	got := resumeHint(sess)
	if !strings.Contains(got, "yottacode sessions resume feature-foo") {
		t.Fatalf("hint should prefer name when set, got %q", got)
	}
	if strings.Contains(got, "20260101") {
		t.Fatalf("hint should not include id when name is set, got %q", got)
	}
}

// TestIsNormalExit_InterruptIsNotAFailure is the regression guard for the
// Ctrl+C bug: Run used to bail on any non-nil error from prog.Run, which
// skipped the session save and the resume hint whenever ^C arrived as a real
// SIGINT (non-TTY stdin, ^C outside raw mode, kill -INT) instead of as a
// keystroke. Interrupt and kill must both read as ordinary exits so shutdown
// still persists the session and prints "yottacode sessions resume <id>".
func TestIsNormalExit_InterruptIsNotAFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"clean quit", nil, true},
		{"sigint", tea.ErrInterrupted, true},
		{"killed", tea.ErrProgramKilled, true},
		// bubbletea wraps: a SIGINT surfaces as "program was killed:
		// program was interrupted", so == comparison would miss it.
		{"wrapped sigint", fmt.Errorf("%w: %w", tea.ErrProgramKilled, tea.ErrInterrupted), true},
		{"wrapped by caller", fmt.Errorf("tui: %w", tea.ErrInterrupted), true},
		{"real failure", errors.New("could not open tty"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNormalExit(tc.err); got != tc.want {
				t.Fatalf("isNormalExit(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
