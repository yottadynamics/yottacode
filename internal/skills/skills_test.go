package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSkillFile_HappyPath(t *testing.T) {
	body := []byte(`---
name: remote-ops
description: SSH playbook.
license: MIT
metadata:
  author: yottacode
  slash: "true"
allowed-tools: Bash(ssh:*) Bash(scp:*) Read
---
# Remote operations

Use this to connect to remote hosts.
`)
	sk, err := ParseSkillFile(body, "remote-ops")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if sk.Name != "remote-ops" {
		t.Errorf("name = %q, want %q", sk.Name, "remote-ops")
	}
	if sk.Description != "SSH playbook." {
		t.Errorf("description = %q", sk.Description)
	}
	if sk.License != "MIT" {
		t.Errorf("license = %q", sk.License)
	}
	if sk.Metadata["author"] != "yottacode" {
		t.Errorf("metadata.author = %q", sk.Metadata["author"])
	}
	if sk.Metadata["slash"] != "true" {
		t.Errorf("metadata.slash = %q", sk.Metadata["slash"])
	}
	want := []string{"Bash(ssh:*)", "Bash(scp:*)", "Read"}
	if len(sk.AllowedTools) != len(want) {
		t.Fatalf("allowed-tools = %v, want %v", sk.AllowedTools, want)
	}
	for i := range want {
		if sk.AllowedTools[i] != want[i] {
			t.Errorf("allowed-tools[%d] = %q, want %q", i, sk.AllowedTools[i], want[i])
		}
	}
	if !strings.Contains(sk.Body, "# Remote operations") {
		t.Errorf("body missing heading: %q", sk.Body)
	}
}

func TestParseSkillFile_MissingFrontmatter(t *testing.T) {
	if _, err := ParseSkillFile([]byte("no frontmatter here\n"), ""); err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}

func TestParseSkillFile_UnterminatedFrontmatter(t *testing.T) {
	if _, err := ParseSkillFile([]byte("---\nname: foo\n"), ""); err == nil {
		t.Fatal("expected error for unterminated frontmatter")
	}
}

func TestParseSkillFile_NameMustMatchDir(t *testing.T) {
	body := []byte("---\nname: foo\ndescription: bar\n---\nbody\n")
	if _, err := ParseSkillFile(body, "bar"); err == nil {
		t.Fatal("expected error when name != expectedName")
	}
}

func TestParseSkillFile_InvalidNamePatterns(t *testing.T) {
	cases := []string{
		"-leading",
		"trailing-",
		"double--hyphen",
		"UPPER",
		"with space",
		"with.dot",
		"with_underscore",
	}
	for _, n := range cases {
		t.Run(n, func(t *testing.T) {
			body := []byte("---\nname: " + n + "\ndescription: x\n---\nbody\n")
			if _, err := ParseSkillFile(body, ""); err == nil {
				t.Fatalf("expected error for name %q", n)
			}
		})
	}
}

func TestParseSkillFile_EmptyBody(t *testing.T) {
	body := []byte("---\nname: foo\ndescription: x\n---\n\n  \n")
	if _, err := ParseSkillFile(body, "foo"); err == nil {
		t.Fatal("expected error for empty body")
	}
}

func TestParseSkillFile_DescriptionTooLong(t *testing.T) {
	long := strings.Repeat("a", maxDescriptionLen+1)
	body := []byte("---\nname: foo\ndescription: " + long + "\n---\nbody\n")
	if _, err := ParseSkillFile(body, "foo"); err == nil {
		t.Fatal("expected error for over-long description")
	}
}

func TestSkill_SlashEnabled(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", true},
		{"true", true},
		{"yes", true},
		{"false", false},
		{"FALSE", false},
		{"no", false},
		{"0", false},
		{"off", false},
	}
	for _, c := range cases {
		t.Run(c.val, func(t *testing.T) {
			s := Skill{Metadata: map[string]string{"slash": c.val}}
			if c.val == "" {
				s.Metadata = nil
			}
			if got := s.SlashEnabled(); got != c.want {
				t.Errorf("slash=%q: got %v, want %v", c.val, got, c.want)
			}
		})
	}
}

func TestParseAllowedTools(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"Read", []string{"Read"}},
		{"Read Write", []string{"Read", "Write"}},
		{"Read,Write", []string{"Read", "Write"}},
		{"Bash(git:*) Read", []string{"Bash(git:*)", "Read"}},
		{"Bash(git:*, jq:*) Read", []string{"Bash(git:*, jq:*)", "Read"}},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := parseAllowedTools(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("[%d] got %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestLoadBuiltins(t *testing.T) {
	skills := LoadBuiltins()
	if len(skills) == 0 {
		t.Fatal("expected at least one built-in skill")
	}
	expected := map[string]bool{
		"remote-ops":                       true,
		"git-investigation":                true,
		"dockerfile-review":                true,
		"test-driven-development":          true,
		"verification-before-completion":   true,
		"webapp-testing":                   true,
		"diagnose":                         true,
		"security-auditor":                 true,
		"writing-plans":                    true,
		"executing-plans":                  true,
		"brainstorming":                    true,
		"receiving-code-review":            true,
		"improve-codebase-architecture":    true,
		"prototype":                        true,
		"handoff":                          true,
		"performance-profiler":             true,
		"documentation-and-adrs":           true,
	}
	seen := map[string]bool{}
	for _, sk := range skills {
		seen[sk.Name] = true
		if sk.Source != ScopeBuiltin {
			t.Errorf("skill %q: source = %q, want builtin", sk.Name, sk.Source)
		}
		if !strings.HasPrefix(sk.SourcePath, "embed:builtins/") {
			t.Errorf("skill %q: source path = %q, want embed:builtins/...", sk.Name, sk.SourcePath)
		}
	}
	for name := range expected {
		if !seen[name] {
			t.Errorf("missing built-in skill %q", name)
		}
	}
}

func TestLoadAll_DiskPrecedence(t *testing.T) {
	cwd := t.TempDir()
	projectDir := filepath.Join(cwd, ".yottacode", "skills", "remote-ops")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	projectBody := []byte(`---
name: remote-ops
description: Project override.
---
project body
`)
	if err := os.WriteFile(filepath.Join(projectDir, "SKILL.md"), projectBody, 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := LoadAll(cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	sk := Find(res.Skills, "remote-ops")
	if sk == nil {
		t.Fatal("remote-ops not found")
	}
	if sk.Source != ScopeProject {
		t.Errorf("source = %q, want project (project should shadow builtin)", sk.Source)
	}
	if sk.Description != "Project override." {
		t.Errorf("description = %q, want project override", sk.Description)
	}
}

func TestLoadAll_ReservedNameDropped(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".yottacode", "skills", "help")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("---\nname: help\ndescription: bad.\n---\nbody\n")
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	reserved := map[string]bool{"help": true}
	res, err := LoadAll(cwd, reserved)
	if err != nil {
		t.Fatal(err)
	}
	if Find(res.Skills, "help") != nil {
		t.Error("reserved skill should have been dropped")
	}
	hasWarn := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "shadows a built-in") {
			hasWarn = true
		}
	}
	if !hasWarn {
		t.Errorf("expected shadow warning, got %v", res.Warnings)
	}
}

func TestLoadAll_NameDirMismatchDropped(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".yottacode", "skills", "outer")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("---\nname: inner\ndescription: bad.\n---\nbody\n")
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := LoadAll(cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	if Find(res.Skills, "outer") != nil || Find(res.Skills, "inner") != nil {
		t.Error("name/dir mismatch should drop the skill")
	}
	if len(res.Warnings) == 0 {
		t.Error("expected a parse warning for name/dir mismatch")
	}
}

func TestLoadAll_SubdirWithoutSkillMd_Ignored(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".yottacode", "skills", "empty-dir")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	res, err := LoadAll(cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "empty-dir") {
			t.Errorf("subdir without SKILL.md should be silent: %s", w)
		}
	}
}
