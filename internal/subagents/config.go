package subagents

import (
	"bufio"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// AgentConfig is one typed-subagent definition. The body of the source
// file becomes the child's system prompt; the frontmatter declares
// metadata + an optional tool allowlist + an optional model override.
type AgentConfig struct {
	Name        string   // subagent_type the parent uses in the Agent tool call
	Description string   // shown to the parent model as part of the Agent tool schema
	Tools       []string // optional tool allowlist; ["*"] or nil means "inherit all parent tools (minus Agent)"
	Model       string   // optional adapter model override; empty means inherit parent's
	Prompt      string   // markdown body — used as the child's system prompt
	Source      string   // "builtin" | "global" | "project" — diagnostics only
	SourcePath  string   // absolute path of the source file (empty for builtins)
}

// agentNamePattern matches the slug rules subagents follow:
// [a-z][a-z0-9-]* up to 64 chars. Conservative on purpose — names
// double as `subagent_type` schema values and as transcript filename
// prefixes, and a strict pattern eliminates a whole class of escaping
// concerns.
var agentNamePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

// ParseAgentFile parses one agents/*.md file into an AgentConfig.
// Returns an error when the frontmatter is missing or required fields
// are blank — the caller is expected to surface the error with the
// file path so the user can fix it.
//
// Frontmatter format (YAML-ish, deliberately tolerant of hand-edits;
// matches the approach internal/memory uses):
//
//	---
//	name: Explore
//	description: Fast read-only search agent for locating code.
//	tools: [read_file, grep, glob]      # optional; "*" or absent = inherit
//	model: claude-haiku-4-5             # optional
//	---
//	<markdown body — used as the child's system prompt>
func ParseAgentFile(data []byte) (AgentConfig, error) {
	s := string(data)
	if !strings.HasPrefix(s, "---\n") {
		return AgentConfig{}, fmt.Errorf("missing frontmatter block (expected `---` on line 1)")
	}
	header, body, found := strings.Cut(s[4:], "\n---\n")
	if !found {
		return AgentConfig{}, fmt.Errorf("unterminated frontmatter block (expected closing `---`)")
	}
	cfg := AgentConfig{Prompt: strings.TrimLeft(body, "\n")}
	sc := bufio.NewScanner(strings.NewReader(header))
	for sc.Scan() {
		line := sc.Text()
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "name":
			cfg.Name = val
		case "description":
			cfg.Description = val
		case "tools":
			cfg.Tools = parseToolList(val)
		case "model":
			cfg.Model = val
		}
	}
	if cfg.Name == "" {
		return AgentConfig{}, fmt.Errorf("frontmatter missing `name`")
	}
	if !agentNamePattern.MatchString(cfg.Name) {
		return AgentConfig{}, fmt.Errorf("invalid `name` %q (allowed: letters, digits, underscore, hyphen; max 64; must start with a letter)", cfg.Name)
	}
	if strings.TrimSpace(cfg.Prompt) == "" {
		return AgentConfig{}, fmt.Errorf("body is empty (expected the child's system prompt below the frontmatter)")
	}
	if cfg.Description == "" {
		// Synthesize a stub description so the model still sees something
		// reasonable in the schema. Better to ship than to fail validation
		// for a stylistic omission.
		cfg.Description = "Subagent: " + cfg.Name
	}
	return cfg, nil
}

// parseToolList accepts the YAML-flow list form (`[a, b, c]`),
// space/comma-separated tokens, and the wildcard `*` (or `["*"]`).
// Returns nil for "inherit all"; returns the explicit slice otherwise.
// Tolerant of quotes and surrounding whitespace so hand-edited
// frontmatter parses without surprises.
func parseToolList(val string) []string {
	val = strings.TrimSpace(val)
	if val == "" || val == "*" {
		return nil
	}
	if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
		val = strings.TrimSuffix(strings.TrimPrefix(val, "["), "]")
	}
	parts := strings.FieldsFunc(val, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	out := make([]string, 0, len(parts))
	wildcard := false
	for _, p := range parts {
		p = strings.Trim(p, "\"' \t")
		if p == "" {
			continue
		}
		if p == "*" {
			wildcard = true
			continue
		}
		out = append(out, p)
	}
	if wildcard && len(out) == 0 {
		return nil
	}
	return out
}

// ToolAllowed reports whether the named tool should be exposed to the
// child registry. Returns true when Tools is nil/empty (inherit all)
// or when the name is in the allowlist. Caller is still responsible
// for the unconditional Agent/exit_plan_mode exclusion — this method
// only encodes the per-config allowlist semantics.
func (c AgentConfig) ToolAllowed(name string) bool {
	if len(c.Tools) == 0 {
		return true
	}
	return slices.Contains(c.Tools, name)
}
