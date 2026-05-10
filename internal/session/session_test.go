package session

import (
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

func redirectHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	return tmp
}

func TestNew_CreatesFreshID(t *testing.T) {
	redirectHome(t)
	s1, err := New("m1", "/cwd/a")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	time.Sleep(2 * time.Millisecond) // guarantee distinct timestamp IDs
	s2, err := New("m1", "/cwd/a")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s1.ID == s2.ID {
		t.Errorf("consecutive IDs collided: %q", s1.ID)
	}
	if s1.Model != "m1" {
		t.Errorf("Model = %q, want m1", s1.Model)
	}
	if s1.Cwd != "/cwd/a" {
		t.Errorf("Cwd = %q, want /cwd/a", s1.Cwd)
	}
}

func TestSaveLoad_Roundtrip(t *testing.T) {
	redirectHome(t)
	s, err := New("qwen3.5:latest", "/proj")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.Messages = []adapter.Message{
		{Role: adapter.RoleSystem, Content: "sys"},
		{Role: adapter.RoleUser, Content: "hi"},
		{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{
			{ID: "c1", Name: "read_file", ArgsJSON: `{"path":"x"}`},
		}},
		{Role: adapter.RoleTool, Content: "result", ToolCallID: "c1"},
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.ID != s.ID || loaded.Model != s.Model || loaded.Cwd != s.Cwd {
		t.Errorf("metadata mismatch: %+v vs %+v", loaded, s)
	}
	if len(loaded.Messages) != len(s.Messages) {
		t.Fatalf("messages len = %d, want %d", len(loaded.Messages), len(s.Messages))
	}
	if loaded.Messages[2].ToolCalls[0].Name != "read_file" {
		t.Errorf("tool_calls lost in roundtrip: %+v", loaded.Messages[2])
	}
}

// TestLoad_LastIsNoLongerMagic guards against regressions to the
// retired "last" keyword. The /sessions picker covers the
// "load most recent" workflow now; Load("last") must error like any
// other unknown id so `yottacode sessions resume last` fails with a
// clear "no session" message rather than silently grabbing the
// newest entry.
func TestLoad_LastIsNoLongerMagic(t *testing.T) {
	redirectHome(t)
	s, err := New("m", "/x")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := Load("last"); err == nil {
		t.Errorf("Load(\"last\") should error now that the keyword is retired; got nil")
	}
}

func TestLoad_UnknownID(t *testing.T) {
	redirectHome(t)
	if _, err := Load("does-not-exist"); err == nil {
		t.Errorf("Load(nonexistent) should error")
	}
}

func TestLoad_ByName(t *testing.T) {
	redirectHome(t)
	s, err := New("m", "/x")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.Name = "feature-branch"
	s.Messages = []adapter.Message{{Role: adapter.RoleUser, Content: "hi"}}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load("feature-branch")
	if err != nil {
		t.Fatalf("Load by name: %v", err)
	}
	if loaded.ID != s.ID {
		t.Errorf("loaded ID = %q, want %q", loaded.ID, s.ID)
	}
	if loaded.Name != "feature-branch" {
		t.Errorf("loaded Name = %q, want feature-branch", loaded.Name)
	}
}

func TestList_NewestFirst(t *testing.T) {
	redirectHome(t)
	first, _ := New("m", "/x")
	first.Save()
	time.Sleep(2 * time.Millisecond)
	second, _ := New("m", "/x")
	second.Name = "labelled"
	second.Save()

	infos, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("len(infos) = %d, want 2", len(infos))
	}
	if infos[0].ID != second.ID {
		t.Errorf("expected newest first; got %q then %q", infos[0].ID, infos[1].ID)
	}
	if infos[0].Name != "labelled" {
		t.Errorf("Name didn't round-trip: %+v", infos[0])
	}
}
