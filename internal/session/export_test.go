package session

import (
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

func TestExport_ContainsHeaderMetadata(t *testing.T) {
	s := &Session{
		ID:      "test-id",
		Name:    "demo",
		Model:   "qwen3.5",
		Created: time.Date(2026, 4, 25, 6, 30, 0, 0, time.UTC),
		Cwd:     "/tmp/x",
	}
	got := ExportMarkdown(s)
	for _, want := range []string{"test-id", "demo", "qwen3.5", "2026-04-25", "/tmp/x"} {
		if !strings.Contains(got, want) {
			t.Errorf("export missing %q:\n%s", want, got)
		}
	}
}

func TestExport_ContainsConversationTurns(t *testing.T) {
	s := &Session{
		ID: "x", Model: "m", Created: time.Now(),
		Messages: []adapter.Message{
			{Role: adapter.RoleSystem, Content: "you are helpful"},
			{Role: adapter.RoleUser, Content: "hello"},
			{Role: adapter.RoleAssistant, Content: "world"},
			{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{
				{ID: "c1", Name: "list_dir", ArgsJSON: `{"path":"."}`},
			}},
			{Role: adapter.RoleTool, Content: "f\tmain.go"},
		},
	}
	got := ExportMarkdown(s)

	// User + assistant content rendered.
	if !strings.Contains(got, "## User") || !strings.Contains(got, "hello") {
		t.Errorf("user turn missing")
	}
	if !strings.Contains(got, "## Assistant") || !strings.Contains(got, "world") {
		t.Errorf("assistant content missing")
	}
	// System message must be omitted (boilerplate).
	if strings.Contains(got, "you are helpful") {
		t.Errorf("system message should be omitted from export:\n%s", got)
	}
	// Tool call signaled.
	if !strings.Contains(got, "list_dir") {
		t.Errorf("tool call name missing")
	}
	// Tool result fenced.
	if !strings.Contains(got, "Tool output") || !strings.Contains(got, "main.go") {
		t.Errorf("tool output missing")
	}
}

func TestExport_IncludesAssistantSources(t *testing.T) {
	s := &Session{
		ID: "x", Model: "m", Created: time.Now(),
		Messages: []adapter.Message{
			{
				Role:    adapter.RoleAssistant,
				Content: "answer",
				Citations: []adapter.Citation{
					{Type: "url_citation", Title: "Example", URL: "https://example.com"},
					{Type: "file_citation", Filename: "notes.txt", FileID: "file_123"},
				},
			},
		},
	}
	got := ExportMarkdown(s)
	for _, want := range []string{"**Sources**", "Example (https://example.com)", "notes.txt (file_123)"} {
		if !strings.Contains(got, want) {
			t.Errorf("export missing %q:\n%s", want, got)
		}
	}
}
