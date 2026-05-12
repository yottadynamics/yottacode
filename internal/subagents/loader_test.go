package subagents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadBuiltins_ShipsExpectedAgents(t *testing.T) {
	cfgs := LoadBuiltins()
	want := map[string]bool{"general-purpose": false, "Explore": false, "Plan": false}
	for _, c := range cfgs {
		if _, ok := want[c.Name]; ok {
			want[c.Name] = true
		}
		if c.Source != "builtin" {
			t.Errorf("builtin %q has Source=%q, want builtin", c.Name, c.Source)
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("expected builtin %q to be loaded", name)
		}
	}
}

func TestLoadAll_ProjectOverridesGlobal(t *testing.T) {
	// Project beats global which beats builtin. We point YOTTACODE_HOME at a
	// scratch dir so the user's real ~/.yottacode/agents doesn't leak in.
	tmpHome := t.TempDir()
	t.Setenv("YOTTACODE_HOME", tmpHome)

	// Global override for Explore.
	globalDir := filepath.Join(tmpHome, "agents")
	if err := os.MkdirAll(globalDir, 0o700); err != nil {
		t.Fatalf("mkdir global: %v", err)
	}
	globalBody := []byte("---\nname: Explore\ndescription: GLOBAL override\n---\n\nGlobal body.\n")
	if err := os.WriteFile(filepath.Join(globalDir, "explore.md"), globalBody, 0o600); err != nil {
		t.Fatalf("write global: %v", err)
	}

	// Project override for Explore — should win.
	tmpProject := t.TempDir()
	projectDir := filepath.Join(tmpProject, ".yottacode", "agents")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	projectBody := []byte("---\nname: Explore\ndescription: PROJECT override\n---\n\nProject body.\n")
	if err := os.WriteFile(filepath.Join(projectDir, "explore.md"), projectBody, 0o600); err != nil {
		t.Fatalf("write project: %v", err)
	}

	res, err := LoadAll(tmpProject, nil)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	got := Find(res.Configs, "Explore")
	if got == nil {
		t.Fatalf("Explore missing from LoadAll result")
	}
	if !strings.Contains(got.Description, "PROJECT") {
		t.Errorf("Explore description = %q, want project override", got.Description)
	}
	if got.Source != "project" {
		t.Errorf("Source = %q, want project", got.Source)
	}
}

func TestLoadAll_DropsUnknownTools(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("YOTTACODE_HOME", tmpHome)
	tmpProject := t.TempDir()
	projectDir := filepath.Join(tmpProject, ".yottacode", "agents")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := []byte("---\nname: Custom\ndescription: x\ntools: [read_file, no_such_tool]\n---\n\nbody\n")
	if err := os.WriteFile(filepath.Join(projectDir, "custom.md"), body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	valid := map[string]bool{"read_file": true}
	res, err := LoadAll(tmpProject, valid)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	custom := Find(res.Configs, "Custom")
	if custom == nil {
		t.Fatalf("Custom missing")
	}
	if len(custom.Tools) != 1 || custom.Tools[0] != "read_file" {
		t.Errorf("Tools = %v, want [read_file]", custom.Tools)
	}
	if len(res.Warnings) == 0 {
		t.Errorf("expected a warning for the unknown tool")
	}
	wantWarn := "no_such_tool"
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, wantWarn) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("warnings did not mention %q: %v", wantWarn, res.Warnings)
	}
}
