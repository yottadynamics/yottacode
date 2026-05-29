package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestLoad_NeitherFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	got, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.UserText != "" || got.ProjectText != "" {
		t.Errorf("expected both empty; got %+v", got)
	}
}

func TestLoad_UserOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(home, ".yottacode", "USER.md"), "prefer table-driven tests")

	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.UserText == "" {
		t.Errorf("UserText should be loaded")
	}
	if !strings.Contains(got.UserText, "prefer table-driven") {
		t.Errorf("UserText content = %q", got.UserText)
	}
	if got.ProjectText != "" {
		t.Errorf("ProjectText should be empty; got %q", got.ProjectText)
	}
}

func TestLoad_ProjectOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, ".yottacode", "YOTTACODE.md"), "this repo is yottacode")

	got, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.UserText != "" {
		t.Errorf("UserText should be empty")
	}
	if !strings.Contains(got.ProjectText, "yottacode") {
		t.Errorf("ProjectText content = %q", got.ProjectText)
	}
}

func TestLoad_BothTrustAnchors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	writeFile(t, filepath.Join(home, ".yottacode", "USER.md"), "USER content")
	writeFile(t, filepath.Join(cwd, ".yottacode", "YOTTACODE.md"), "YOTTACODE content")

	got, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(got.UserText, "USER content") {
		t.Errorf("UserText: %q", got.UserText)
	}
	if !strings.Contains(got.ProjectText, "YOTTACODE content") {
		t.Errorf("ProjectText: %q", got.ProjectText)
	}
	sum := got.Summary()
	if !sum.User || !sum.Project {
		t.Errorf("summary = %+v, want both true", sum)
	}
	if sum.String() != "USER+YOTTA" {
		t.Errorf("summary string = %q, want USER+YOTTA", sum.String())
	}
}

func TestSystemPrompt_AppendsLabeledSections(t *testing.T) {
	l := Loaded{
		UserText:    "be terse",
		ProjectText: "this is a Go project",
	}
	got := SystemPrompt("base prompt", l)
	if !strings.HasPrefix(got, "base prompt") {
		t.Errorf("output should start with base prompt; got %q", got)
	}
	if !strings.Contains(got, "User preferences") || !strings.Contains(got, "be terse") {
		t.Errorf("user section missing: %q", got)
	}
	if !strings.Contains(got, "Project context") || !strings.Contains(got, "Go project") {
		t.Errorf("project section missing: %q", got)
	}
	if strings.Index(got, "User preferences") > strings.Index(got, "Project context") {
		t.Errorf("ordering wrong: user must come before project")
	}
}

func TestSystemPrompt_NoSectionsWhenEmpty(t *testing.T) {
	got := SystemPrompt("just the base", Loaded{})
	if got != "just the base" {
		t.Errorf("got %q, want unchanged base", got)
	}
}

func TestSystemPrompt_AppendsActionDirective(t *testing.T) {
	const opening = "The following is BACKGROUND REFERENCE only"
	const closing = "End of background. Take the user's request and act on it"
	cases := []struct {
		name string
		l    Loaded
	}{
		{"user only", Loaded{UserText: "be terse"}},
		{"project only", Loaded{ProjectText: "this repo is yottacode"}},
		{"user-mem only", Loaded{
			UserMemories: []MemoryEntry{{Scope: "user", Name: "x", Type: "reference", Body: "fact"}},
		}},
		{"project-mem only", Loaded{
			ProjectMemories: []MemoryEntry{{Scope: "project", Name: "x", Type: "reference", Body: "fact"}},
		}},
		{"all four", Loaded{
			UserText:        "u",
			ProjectText:     "p",
			UserMemories:    []MemoryEntry{{Scope: "user", Name: "ua", Type: "reference", Body: "ua-fact"}},
			ProjectMemories: []MemoryEntry{{Scope: "project", Name: "pa", Type: "reference", Body: "pa-fact"}},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SystemPrompt("base", c.l)
			if !strings.Contains(got, opening) {
				t.Errorf("missing background opening %q in:\n%s", opening, got)
			}
			if !strings.Contains(got, closing) {
				t.Errorf("missing closing directive %q in:\n%s", closing, got)
			}
			openIdx := strings.Index(got, opening)
			for _, header := range []string{"## User preferences", "## Project context", "## User memory index", "## Project memory index"} {
				if h := strings.Index(got, header); h >= 0 && h < openIdx {
					t.Errorf("opening directive must precede section %q (got open=%d, header=%d)", header, openIdx, h)
				}
			}
			if !strings.HasSuffix(strings.TrimRight(got, "\n"), "Do not narrate or describe how the agent works.") {
				t.Errorf("closing directive must end the prompt; got tail: %q", got[max(0, len(got)-160):])
			}
		})
	}
}

func TestSystemPrompt_HeadersFrameAsBackground(t *testing.T) {
	l := Loaded{
		UserText:        "u",
		ProjectText:     "p",
		UserMemories:    []MemoryEntry{{Scope: "user", Name: "x", Type: "reference", Body: "fact"}},
		ProjectMemories: []MemoryEntry{{Scope: "project", Name: "y", Type: "reference", Body: "fact"}},
	}
	got := SystemPrompt("base", l)
	for _, want := range []string{
		"## User preferences (background — do not recite)",
		"## Project context (background — do not describe or narrate the agent's architecture)",
		"## User memory index (background — do not narrate)",
		"## Project memory index (background — do not narrate)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing reframed header %q in:\n%s", want, got)
		}
	}
}

func TestSummary_StringForms(t *testing.T) {
	tests := []struct {
		s    Summary
		want string
	}{
		{Summary{}, ""},
		{Summary{User: true}, "USER"},
		{Summary{Project: true}, "YOTTA"},
		{Summary{User: true, Project: true}, "USER+YOTTA"},
		{Summary{UserMem: 1}, "UMEM"},
		{Summary{User: true, UserMem: 3}, "USER+UMEM"},
		{Summary{ProjectMem: 5}, "PMEM"},
		{Summary{User: true, Project: true, UserMem: 3, ProjectMem: 5}, "USER+YOTTA+UMEM+PMEM"},
	}
	for _, c := range tests {
		if got := c.s.String(); got != c.want {
			t.Errorf("Summary{%+v}.String() = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestLoad_UserMemoriesFromNewDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	memDir := filepath.Join(home, ".yottacode", "memory")
	writeFile(t, filepath.Join(memDir, "code-style.md"),
		"---\nname: code-style\ntype: user\ndescription: prefers table-driven tests\ncreated: 2026-05-08T00:00:00Z\n---\nUse table-driven Go tests.\n")
	writeFile(t, filepath.Join(memDir, "tooling.md"),
		"---\nname: tooling\ntype: reference\ndescription: pinned tools\ncreated: 2026-05-08T00:00:00Z\n---\nripgrep is the default search tool.\n")

	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.UserMemories) != 2 {
		t.Fatalf("UserMemories count = %d, want 2: %+v", len(got.UserMemories), got.UserMemories)
	}
	if got.UserMemories[0].Name != "code-style" || got.UserMemories[1].Name != "tooling" {
		t.Errorf("expected sorted-by-name order; got %v",
			[]string{got.UserMemories[0].Name, got.UserMemories[1].Name})
	}
	if got.UserMemories[0].Type != "user" {
		t.Errorf("type from frontmatter not parsed; got %q", got.UserMemories[0].Type)
	}
	if !strings.Contains(got.UserMemories[1].Body, "ripgrep") {
		t.Errorf("body content not loaded: %q", got.UserMemories[1].Body)
	}
}

func TestLoad_ProjectMemoriesIsolatedAcrossProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwdA := filepath.Join(t.TempDir(), "alpha")
	cwdB := filepath.Join(t.TempDir(), "beta")
	if err := os.MkdirAll(cwdA, 0o755); err != nil {
		t.Fatalf("mkdir A: %v", err)
	}
	if err := os.MkdirAll(cwdB, 0o755); err != nil {
		t.Fatalf("mkdir B: %v", err)
	}
	writeFile(t,
		filepath.Join(home, ".yottacode", "projects", ProjectSlug(cwdA), "memory", "alpha-fact.md"),
		"---\nname: alpha-fact\ntype: project\ndescription: alpha\n---\nalpha body\n")
	writeFile(t,
		filepath.Join(home, ".yottacode", "projects", ProjectSlug(cwdB), "memory", "beta-fact.md"),
		"---\nname: beta-fact\ntype: project\ndescription: beta\n---\nbeta body\n")

	gotA, err := Load(cwdA)
	if err != nil {
		t.Fatalf("Load A: %v", err)
	}
	if len(gotA.ProjectMemories) != 1 || gotA.ProjectMemories[0].Name != "alpha-fact" {
		t.Errorf("project A should see only alpha-fact; got %+v", gotA.ProjectMemories)
	}
	gotB, err := Load(cwdB)
	if err != nil {
		t.Fatalf("Load B: %v", err)
	}
	if len(gotB.ProjectMemories) != 1 || gotB.ProjectMemories[0].Name != "beta-fact" {
		t.Errorf("project B should see only beta-fact; got %+v", gotB.ProjectMemories)
	}
}

func TestLoad_MemorySkipsSymlinks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	memDir := filepath.Join(home, ".yottacode", "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(memDir, "real.md"),
		"---\nname: real\ntype: user\ndescription: x\n---\nreal body\n")
	if err := os.Symlink("/etc/passwd", filepath.Join(memDir, "link.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.UserMemories) != 1 || got.UserMemories[0].Name != "real" {
		t.Errorf("expected only 'real' entry, got %+v", got.UserMemories)
	}
}

func TestLoad_PrefersOnDiskIndex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	memDir := filepath.Join(home, ".yottacode", "memory")
	writeFile(t, filepath.Join(memDir, "a.md"),
		"---\nname: a\ntype: user\ndescription: x\n---\nbody\n")
	writeFile(t, filepath.Join(memDir, "MEMORY.md"), "# CUSTOM INDEX TEXT\n")

	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(got.UserMemoryIndex, "CUSTOM INDEX TEXT") {
		t.Errorf("on-disk MEMORY.md should win; got %q", got.UserMemoryIndex)
	}
}

func TestLoad_GeneratesIndexWhenMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	memDir := filepath.Join(home, ".yottacode", "memory")
	writeFile(t, filepath.Join(memDir, "a.md"),
		"---\nname: a\ntype: user\ndescription: alpha\n---\nbody\n")

	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(got.UserMemoryIndex, "[a](a.md)") {
		t.Errorf("generated index should list entry; got %q", got.UserMemoryIndex)
	}
	if _, err := os.Stat(filepath.Join(memDir, "MEMORY.md")); !os.IsNotExist(err) {
		t.Errorf("Load must not write MEMORY.md to disk; stat err = %v", err)
	}
}
