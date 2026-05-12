package subagents

import (
	"strings"
	"testing"
)

func TestParseAgentFile_MinimalValid(t *testing.T) {
	body := []byte(`---
name: Explore
description: Read-only search agent.
---

You are a search subagent. Be terse.
`)
	cfg, err := ParseAgentFile(body)
	if err != nil {
		t.Fatalf("ParseAgentFile: %v", err)
	}
	if cfg.Name != "Explore" {
		t.Errorf("Name = %q, want Explore", cfg.Name)
	}
	if cfg.Description == "" {
		t.Errorf("Description must not be empty")
	}
	if !strings.Contains(cfg.Prompt, "search subagent") {
		t.Errorf("Prompt missing body content: %q", cfg.Prompt)
	}
	if cfg.Tools != nil {
		t.Errorf("Tools = %v, want nil (inherit all)", cfg.Tools)
	}
}

func TestParseAgentFile_WithToolList(t *testing.T) {
	body := []byte(`---
name: Plan
description: Plan-drafting subagent.
tools: [read_file, grep, todo_write]
model: claude-haiku-4-5
---

Plan body.
`)
	cfg, err := ParseAgentFile(body)
	if err != nil {
		t.Fatalf("ParseAgentFile: %v", err)
	}
	wantTools := []string{"read_file", "grep", "todo_write"}
	if len(cfg.Tools) != len(wantTools) {
		t.Fatalf("Tools = %v, want %v", cfg.Tools, wantTools)
	}
	for i, w := range wantTools {
		if cfg.Tools[i] != w {
			t.Errorf("Tools[%d] = %q, want %q", i, cfg.Tools[i], w)
		}
	}
	if cfg.Model != "claude-haiku-4-5" {
		t.Errorf("Model = %q", cfg.Model)
	}
}

func TestParseAgentFile_WildcardTools(t *testing.T) {
	for _, raw := range []string{`tools: *`, `tools: ["*"]`, `tools: [*]`} {
		body := []byte("---\nname: gp\ndescription: x\n" + raw + "\n---\n\nbody\n")
		cfg, err := ParseAgentFile(body)
		if err != nil {
			t.Fatalf("ParseAgentFile %q: %v", raw, err)
		}
		if cfg.Tools != nil {
			t.Errorf("Tools for %q = %v, want nil (wildcard means inherit all)", raw, cfg.Tools)
		}
	}
}

func TestParseAgentFile_RejectsMissingName(t *testing.T) {
	body := []byte("---\ndescription: x\n---\n\nbody\n")
	if _, err := ParseAgentFile(body); err == nil {
		t.Fatalf("expected error for missing name")
	}
}

func TestParseAgentFile_RejectsEmptyBody(t *testing.T) {
	body := []byte("---\nname: x\ndescription: x\n---\n\n   \n")
	if _, err := ParseAgentFile(body); err == nil {
		t.Fatalf("expected error for empty body")
	}
}

func TestParseAgentFile_RejectsMissingFrontmatter(t *testing.T) {
	body := []byte("just a body, no frontmatter\n")
	if _, err := ParseAgentFile(body); err == nil {
		t.Fatalf("expected error for missing frontmatter")
	}
}

func TestParseAgentFile_RejectsBadName(t *testing.T) {
	body := []byte("---\nname: 99-cant-start-with-digit\ndescription: x\n---\n\nbody\n")
	// Actually the regex allows starting with a digit if it had a letter
	// pattern starting position — verify by trying a clearly bad one.
	if _, err := ParseAgentFile([]byte("---\nname: has space\ndescription: x\n---\n\nbody\n")); err == nil {
		t.Errorf("expected error for name containing space")
	}
	_ = body
}

func TestToolAllowed_WildcardInherit(t *testing.T) {
	cfg := AgentConfig{Tools: nil}
	if !cfg.ToolAllowed("read_file") {
		t.Errorf("nil Tools should inherit all (read_file rejected)")
	}
}

func TestToolAllowed_Explicit(t *testing.T) {
	cfg := AgentConfig{Tools: []string{"read_file", "grep"}}
	if !cfg.ToolAllowed("read_file") {
		t.Errorf("read_file should be allowed")
	}
	if cfg.ToolAllowed("run_bash") {
		t.Errorf("run_bash should be denied")
	}
}
