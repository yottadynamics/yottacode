package subagents

import (
	"strings"
	"testing"
)

// TestLoadBuiltins_NewRoles asserts the implementation roster added for the
// dispatch/background beta loads with the right dispatch defaults:
// implement/test/docs are write-capable and background-capable for dispatch;
// review is read-only and foreground. Each must declare an explicit tools list
// and a non-empty prompt.
func TestLoadBuiltins_NewRoles(t *testing.T) {
	byName := map[string]AgentConfig{}
	for _, c := range LoadBuiltins() {
		byName[c.Name] = c
	}

	writeTools := map[string]bool{
		"write_file": true, "edit_file": true, "apply_hashline": true, "apply_diff": true,
		"mkdir": true, "copy_file": true, "move_file": true, "delete_file": true,
	}
	hasWrite := func(c AgentConfig) bool {
		for _, name := range c.Tools {
			if writeTools[name] {
				return true
			}
		}
		return false
	}

	cases := []struct {
		name      string
		wantBg    bool
		wantWrite bool
	}{
		{"implement", true, true},
		{"test", true, true},
		{"docs", true, true},
		{"review", false, false},
		{"code-verifier", false, false},
	}
	for _, tc := range cases {
		c, ok := byName[tc.name]
		if !ok {
			t.Errorf("builtin %q not loaded", tc.name)
			continue
		}
		if c.Source != "builtin" {
			t.Errorf("%q Source = %q, want builtin", tc.name, c.Source)
		}
		if c.Background != tc.wantBg {
			t.Errorf("%q Background = %v, want %v", tc.name, c.Background, tc.wantBg)
		}
		if hasWrite(c) != tc.wantWrite {
			t.Errorf("%q write-capable = %v, want %v", tc.name, hasWrite(c), tc.wantWrite)
		}
		if len(c.Tools) == 0 {
			t.Errorf("%q should declare an explicit tools list", tc.name)
		}
		if strings.TrimSpace(c.Prompt) == "" {
			t.Errorf("%q has an empty system-prompt body", tc.name)
		}
	}
}

// TestLoadBuiltins_ResearchRolesPreferSemanticTools pins the stock research
// policy: indexed/semantic navigation first, targeted text search as fallback,
// with no workspace-edit capability leaking into read-only children.
func TestLoadBuiltins_ResearchRolesPreferSemanticTools(t *testing.T) {
	byName := map[string]AgentConfig{}
	for _, cfg := range LoadBuiltins() {
		byName[cfg.Name] = cfg
	}
	for _, role := range []string{"Explore", "Plan"} {
		cfg, ok := byName[role]
		if !ok {
			t.Fatalf("builtin %q not loaded", role)
		}
		for _, tool := range []string{"lsp_status", "lsp_symbols", "lsp_references", "lsp_implementation", "lsp_impact", "code_map", "code_symbols", "code_impact"} {
			if !cfg.ToolAllowed(tool) {
				t.Errorf("%s does not allow semantic tool %q", role, tool)
			}
		}
		if cfg.ToolAllowed("lsp_apply_workspace_edit") {
			t.Errorf("%s must not allow lsp_apply_workspace_edit", role)
		}
		for _, want := range []string{"Code Map", "lsp_status", "unsupported languages", "Do not repeat"} {
			if !strings.Contains(strings.ToLower(cfg.Prompt), strings.ToLower(want)) {
				t.Errorf("%s prompt missing policy %q", role, want)
			}
		}
	}
}
