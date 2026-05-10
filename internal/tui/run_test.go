package tui

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/session"
)

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
