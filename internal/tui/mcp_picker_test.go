package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/mcp"
)

func TestMCPPicker_OpenShowsMenu(t *testing.T) {
	m := newTestModel(t)
	m.openMCPPicker()
	if !m.mcpPickerOpen {
		t.Fatal("expected mcpPickerOpen=true")
	}
	if m.mcpPicker == nil {
		t.Fatal("expected mcpPicker to be non-nil")
	}
	if m.mcpPicker.mode != mcpMenuMode {
		t.Errorf("mode = %d, want mcpMenuMode", m.mcpPicker.mode)
	}
	body := renderMCPPicker(m.mcpPicker, 80)
	if !strings.Contains(body, "List") || !strings.Contains(body, "Add") || !strings.Contains(body, "Remove") {
		t.Errorf("menu should show List/Add/Remove; got %q", body)
	}
}

func TestMCPPicker_EscClosesMenu(t *testing.T) {
	m := newTestModel(t)
	m.openMCPPicker()
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mcpPickerOpen {
		t.Error("Esc on menu should close picker")
	}
}

func TestMCPPicker_NavigateMenu(t *testing.T) {
	m := newTestModel(t)
	m.openMCPPicker()
	if m.mcpPicker.menuCursor != 0 {
		t.Fatalf("initial cursor = %d, want 0", m.mcpPicker.menuCursor)
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.mcpPicker.menuCursor != 1 {
		t.Errorf("after down, cursor = %d, want 1", m.mcpPicker.menuCursor)
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.mcpPicker.menuCursor != 0 {
		t.Errorf("after up, cursor = %d, want 0", m.mcpPicker.menuCursor)
	}
}

func TestMCPPicker_EnterOnListOpensListMode(t *testing.T) {
	m := newTestModel(t)
	m.openMCPPicker()
	// cursor=0 → List
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mcpPicker.mode != mcpListMode {
		t.Errorf("mode = %d, want mcpListMode", m.mcpPicker.mode)
	}
}

func TestMCPPicker_EnterOnAddOpensAddMode(t *testing.T) {
	m := newTestModel(t)
	m.openMCPPicker()
	// Navigate to Add (index 1)
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mcpPicker.mode != mcpAddMode {
		t.Errorf("mode = %d, want mcpAddMode", m.mcpPicker.mode)
	}
	if len(m.mcpPicker.addFields) != 3 {
		t.Errorf("expected 3 add fields, got %d", len(m.mcpPicker.addFields))
	}
}

func TestMCPPicker_EscFromListReturnsToMenu(t *testing.T) {
	m := newTestModel(t)
	m.openMCPPicker()
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → list
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mcpPicker.mode != mcpMenuMode {
		t.Errorf("Esc from list should return to menu; mode = %d", m.mcpPicker.mode)
	}
}

func TestMCPPicker_AddValidation(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")
	m.openMCPPicker(mcpAddMode)

	// Empty name → error
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mcpPicker == nil {
		t.Fatal("picker should still be open on validation error")
	}
	if m.mcpPicker.addInputErr == "" {
		t.Error("expected validation error for empty name")
	}
}

func TestMCPPicker_ListWithServers(t *testing.T) {
	m := newTestModel(t)
	mgr := mcp.NewManager([]config.MCPServer{
		{Name: "fake", Command: "/no/such/binary/yottacode-picker-test"},
	})
	mgr.Start(t.Context())
	m.mcpManager = mgr

	m.openMCPPicker(mcpListMode)
	if len(m.mcpPicker.servers) != 1 {
		t.Fatalf("expected 1 server entry; got %d", len(m.mcpPicker.servers))
	}
	if m.mcpPicker.servers[0].Name != "fake" {
		t.Errorf("server name = %q, want fake", m.mcpPicker.servers[0].Name)
	}
	body := renderMCPPicker(m.mcpPicker, 80)
	if !strings.Contains(body, "fake") {
		t.Errorf("list should show server name; got %q", body)
	}
}

func TestMCPPicker_RemoveCommitsAndCloses(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, `
[[mcp_servers]]
name    = "test-srv"
command = "echo"
`)
	m.openMCPPicker(mcpRemoveMode)
	if len(m.mcpPicker.servers) == 0 {
		t.Fatal("expected at least 1 server for removal")
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mcpPickerOpen {
		t.Error("picker should close after remove")
	}
	content := m.transcript.String()
	if !strings.Contains(content, "removed") {
		t.Errorf("should confirm removal; got %q", content)
	}
}

func TestSlash_MCPOpensPickerWithNoArgs(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/mcp")
	if !m.mcpPickerOpen {
		t.Error("/mcp with no args should open the picker")
	}
}

func TestSlash_MCPListStillWorks(t *testing.T) {
	m := newTestModel(t)
	mgr := mcp.NewManager([]config.MCPServer{
		{Name: "fake", Command: "/no/such/binary/yottacode-picker-test"},
	})
	mgr.Start(t.Context())
	m.mcpManager = mgr

	m, _ = typeAndEnter(t, m, "/mcp list")
	if m.mcpPickerOpen {
		t.Error("/mcp list should not open picker")
	}
	content := m.transcript.String()
	if !strings.Contains(content, "fake") {
		t.Errorf("/mcp list should show server; got %q", content)
	}
}
