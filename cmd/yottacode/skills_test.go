package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sampleSkillBody mirrors the fixture used in internal/skills tests so
// the CLI assertions can hit ParseSkillFile with valid bytes without
// pulling the const into a shared package.
const sampleSkillBody = `---
name: sample
description: Sample skill for CLI tests.
---
# Sample

Body content.
`

// runSkillsCmd boots the full cobra tree (via newCLI) and runs
// `yottacode skills ...`, returning combined stdout+stderr plus the
// exit error. Going through the root tree — instead of calling
// newSkillsCmd() directly — exercises the AddCommand wiring and
// catches any persistent-flag interactions.
func runSkillsCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"skills"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

// TestSkillsCLI_InstallList_RoundTrip is the happy-path smoke test:
// install a local skill, list it, show it. Covers AddCommand wiring,
// stdout formatting, and the LoadAll path the list/show subcommands
// use.
func TestSkillsCLI_InstallList_RoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOTTACODE_HOME", home)
	t.Setenv("HOME", home)

	// Use a chdir to a temp cwd so LoadAll's project-scope check has
	// nothing to find — we only want the user-scope install we just
	// did to show up.
	cwd := t.TempDir()
	prev, _ := os.Getwd()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(sampleSkillBody), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runSkillsCmd(t, "install", src)
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if !strings.Contains(out, "installed sample") {
		t.Errorf("install output missing skill name: %q", out)
	}

	out, err = runSkillsCmd(t, "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "sample") || !strings.Contains(out, "user") {
		t.Errorf("list output missing user-scoped sample: %q", out)
	}

	out, err = runSkillsCmd(t, "show", "sample")
	if err != nil {
		t.Fatalf("show: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Body content.") {
		t.Errorf("show output missing body: %q", out)
	}
}

// TestSkillsCLI_Uninstall_RemovesInstalledSkill covers the
// install/uninstall pair end-to-end. The directory must be gone after
// uninstall and the second uninstall must fail with a name-reference
// error — that's the contract the CLI promises in its error messages.
func TestSkillsCLI_Uninstall_RemovesInstalledSkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOTTACODE_HOME", home)
	t.Setenv("HOME", home)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(sampleSkillBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runSkillsCmd(t, "install", src); err != nil {
		t.Fatalf("install: %v", err)
	}
	dest := filepath.Join(home, "skills", "sample")
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("install dir missing: %v", err)
	}
	if _, err := runSkillsCmd(t, "uninstall", "sample"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("dir still exists: %v", err)
	}
	if _, err := runSkillsCmd(t, "uninstall", "sample"); err == nil {
		t.Error("second uninstall should error")
	}
}

// TestSkillsCLI_InstallRefusesOverwrite locks the --force gate from
// the CLI's perspective. The second install fails; --force makes it
// succeed.
func TestSkillsCLI_InstallRefusesOverwrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOTTACODE_HOME", home)
	t.Setenv("HOME", home)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(sampleSkillBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runSkillsCmd(t, "install", src); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if _, err := runSkillsCmd(t, "install", src); err == nil {
		t.Error("expected second install to refuse overwrite")
	}
	if _, err := runSkillsCmd(t, "install", src, "--force"); err != nil {
		t.Errorf("--force should succeed: %v", err)
	}
}

// TestSkillsCLI_CheckReportsStatus runs `skills check` after an
// install + in-place edit and confirms the status string surfaces in
// stdout. Verifies the CLI output is grep-able by the documented
// status strings.
func TestSkillsCLI_CheckReportsStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOTTACODE_HOME", home)
	t.Setenv("HOME", home)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(sampleSkillBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runSkillsCmd(t, "install", src); err != nil {
		t.Fatalf("install: %v", err)
	}

	out, err := runSkillsCmd(t, "check")
	if err != nil {
		t.Fatalf("check: %v\n%s", err, out)
	}
	if !strings.Contains(out, "sample") || !strings.Contains(out, "ok") {
		t.Errorf("clean check should report ok: %q", out)
	}

	installed := filepath.Join(home, "skills", "sample", "SKILL.md")
	body, _ := os.ReadFile(installed)
	if err := os.WriteFile(installed, append(body, '\n', 'e', 'd', 'i', 't', '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = runSkillsCmd(t, "check")
	if err != nil {
		t.Fatalf("check after edit: %v", err)
	}
	if !strings.Contains(out, "modified") {
		t.Errorf("post-edit check should report modified: %q", out)
	}
}

// TestSkillsCLI_UpdateRefetches edits the upstream source and runs
// `skills update`, confirming the on-disk bytes change and the
// status string is `updated`. End-to-end exercise of the CLI surface
// over Phase 2's core path.
func TestSkillsCLI_UpdateRefetches(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOTTACODE_HOME", home)
	t.Setenv("HOME", home)

	src := t.TempDir()
	upstream := filepath.Join(src, "SKILL.md")
	if err := os.WriteFile(upstream, []byte(sampleSkillBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runSkillsCmd(t, "install", src); err != nil {
		t.Fatalf("install: %v", err)
	}

	// Bump upstream.
	body, _ := os.ReadFile(upstream)
	if err := os.WriteFile(upstream, append(body, '\n', 'u', 'p', 'd', '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runSkillsCmd(t, "update")
	if err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	if !strings.Contains(out, "updated") {
		t.Errorf("update output should mention updated: %q", out)
	}
	installed, _ := os.ReadFile(filepath.Join(home, "skills", "sample", "SKILL.md"))
	if !strings.Contains(string(installed), "upd") {
		t.Errorf("installed copy did not pick up the upstream change: %q", installed)
	}
}
