package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/skills"
)

// sampleSkillBody is a minimal, valid SKILL.md fixture for slash
// tests. Mirrors the fixtures in internal/skills/install_test.go.
const sampleSkillBody = `---
name: slash-sample
description: Sample skill for slash-form tests.
---
# Slash sample

Body.
`

// TestSkillsSlash_BareOpensMenu locks the regression where the
// /skills entry point opens the top-level menu (Catalog / Install /
// Check / Update). The picker (now Catalog) is reachable via the
// menu — not directly from the slash command anymore.
func TestSkillsSlash_BareOpensMenu(t *testing.T) {
	m := newTestModel(t)
	m.skillTool = &agent.SkillTool{All: nil}
	m, _ = m.runSlash("/skills")
	if !m.skillsMenuOpen {
		t.Fatal("/skills with no args should open the menu")
	}
	if m.skillsPickerOpen {
		t.Fatal("/skills should NOT open the picker directly anymore")
	}
}

// A bracketed paste while the skills catalog's `/` filter is active
// must land in the filter buffer, not leak through to the hidden main
// chat textarea underneath the popup. Regression test for the
// bubbletea v2 migration: pastes arrive as tea.PasteMsg, distinct
// from the msg.Text accumulation updateSkillsPickerFilter uses for
// typed runes.
func TestSkillsPicker_FilterAcceptsPaste(t *testing.T) {
	m := newTestModel(t)
	m.skillsPickerOpen = true
	m.skillsPicker = &skillsPickerState{filterMode: true}

	m, _ = applyMsg(m, tea.PasteMsg{Content: "brainstorm"})
	if m.skillsPicker.filter != "brainstorm" {
		t.Fatalf("paste should fill the filter buffer; got %q", m.skillsPicker.filter)
	}
	if got := m.textInput.Value(); got != "" {
		t.Errorf("paste should not leak into the main chat textarea; got %q", got)
	}
}

// TestSkillsSlash_InstallAndUninstall exercises the install→list→
// uninstall round trip through the slash dispatcher and confirms the
// in-session SkillTool reflects each mutation. Without this, the
// reloadSkillsRegistry plumbing could silently break and /skills
// would lie about what's installed until a session restart.
func TestSkillsSlash_InstallAndUninstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOTTACODE_HOME", home)
	t.Setenv("HOME", home)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(sampleSkillBody), 0o600); err != nil {
		t.Fatal(err)
	}

	m := newTestModel(t)
	m.skillTool = &agent.SkillTool{All: nil}
	m.skillTool.SetEnabled(map[string]bool{}) // none enabled — matches the default session policy

	m, _ = m.runSlash("/skills install " + src)
	if skills.Find(m.skillTool.All, "slash-sample") == nil {
		t.Fatal("install did not refresh SkillTool.All")
	}
	if skills.Find(m.skills, "slash-sample") == nil {
		t.Error("install did not refresh m.skills")
	}
	// Slash dispatch list should pick up the new skill (metadata.slash
	// defaults to enabled).
	found := false
	for _, c := range m.skillSlash {
		if c.Name == "slash-sample" {
			found = true
			break
		}
	}
	if !found {
		t.Error("install did not refresh m.skillSlash")
	}

	m, _ = m.runSlash("/skills uninstall slash-sample")
	if skills.Find(m.skillTool.All, "slash-sample") != nil {
		t.Error("uninstall did not refresh SkillTool.All")
	}
	if _, err := os.Stat(filepath.Join(home, "skills", "slash-sample")); !os.IsNotExist(err) {
		t.Errorf("uninstall did not remove dir: err=%v", err)
	}
}

// TestSkillsSlash_UnknownSubcommandErrors guards the help text shown
// when a user typos a subcommand. Without this, a silent no-op would
// be very confusing — the user types /skills foo, nothing happens,
// they have no idea why.
func TestSkillsSlash_UnknownSubcommandErrors(t *testing.T) {
	m := newTestModel(t)
	m.skillTool = &agent.SkillTool{All: nil}
	before := m.transcript.String()
	m, _ = m.runSlash("/skills not-a-subcommand")
	after := m.transcript.String()
	if !strings.Contains(after[len(before):], "unknown subcommand") {
		t.Errorf("expected unknown-subcommand message; got %q", after[len(before):])
	}
}

// TestSkillsSlash_CheckAndUpdate is the Phase 2 slash-surface
// integration test: install, edit upstream, /skills check (should
// show `ok` since the installed copy hasn't drifted), /skills update
// (should refetch and refresh the registry).
func TestSkillsSlash_CheckAndUpdate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOTTACODE_HOME", home)
	t.Setenv("HOME", home)

	src := t.TempDir()
	upstream := filepath.Join(src, "SKILL.md")
	if err := os.WriteFile(upstream, []byte(sampleSkillBody), 0o600); err != nil {
		t.Fatal(err)
	}

	m := newTestModel(t)
	m.skillTool = &agent.SkillTool{All: nil}
	m.skillTool.SetEnabled(map[string]bool{})

	m, _ = m.runSlash("/skills install " + src)

	before := m.transcript.String()
	m, _ = m.runSlash("/skills check")
	after := m.transcript.String()
	checkBlock := after[len(before):]
	if !strings.Contains(checkBlock, "slash-sample") || !strings.Contains(checkBlock, "ok") {
		t.Errorf("clean /skills check expected slash-sample ok; got %q", checkBlock)
	}

	// Bump upstream so update has work to do.
	body, _ := os.ReadFile(upstream)
	if err := os.WriteFile(upstream, append(body, '\n', 'u', 'p', '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	before = m.transcript.String()
	m, _ = m.runSlash("/skills update")
	after = m.transcript.String()
	updateBlock := after[len(before):]
	if !strings.Contains(updateBlock, "updated") {
		t.Errorf("/skills update expected `updated`; got %q", updateBlock)
	}
}
