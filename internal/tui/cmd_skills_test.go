package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// TestSkillsSlash_BareOpensPicker locks the regression where the
// /skills entry point routes the no-args path to the picker (the
// pre-subcommand behavior). Without this, a future refactor that
// drops the "len(args)==0" branch silently breaks every user who
// types /skills to enable skills for the session.
func TestSkillsSlash_BareOpensPicker(t *testing.T) {
	m := newTestModel(t)
	m.skillTool = &agent.SkillTool{All: nil}
	m, _ = m.runSlash("/skills")
	if !m.skillsPickerOpen {
		t.Fatal("/skills with no args should open the picker")
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
