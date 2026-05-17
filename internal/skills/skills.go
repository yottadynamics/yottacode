// Package skills owns spec-compliant Agent Skills: discovery, parsing,
// and the in-memory registry the agent loop reads. A Skill is a
// reusable capability playbook the model invokes via the `Skill` tool
// (description-matched) or the user invokes via `/<skill-name>` slash.
//
// Disk layout per skill (mirrors agentskills.io/specification):
//
//	skills/<slug>/
//	├── SKILL.md          required — frontmatter + body
//	├── scripts/          optional — executable, loaded on demand
//	├── references/       optional — docs loaded on demand
//	└── assets/           optional — templates, schemas
//
// Progressive disclosure is the spec's load contract — only the
// metadata (name + description) is read into the system prompt at
// startup; the body loads when the skill is invoked; resources load
// when the model reads them via read_file or runs them via run_bash.
package skills

import (
	"bufio"
	"fmt"
	"regexp"
	"strings"
)

// Scope distinguishes where a skill came from. Mirrors subagents/usercmd.
type Scope string

const (
	ScopeBuiltin Scope = "built-in"
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
)

// Skill is one parsed SKILL.md plus the directory it lives in. Body is
// the post-frontmatter markdown content; Dir is the absolute path of
// the skill's directory (used to resolve scripts/, references/,
// assets/ relative paths the body may reference).
//
// Field shapes follow the canonical SKILL.md spec verbatim: name,
// description, license, compatibility, metadata, allowed-tools. We do
// not invent required fields beyond the spec.
type Skill struct {
	Name          string            // required, [a-z0-9-]{1,64}, must match parent dir
	Description   string            // required, 1-1024 chars
	License       string            // optional
	Compatibility string            // optional, ≤500 chars, documentation-only
	Metadata      map[string]string // optional, free-form host-specific keys (e.g. slash, source-url)
	AllowedTools  []string          // optional, experimental; parsed and stored but not enforced in v1
	Body          string            // markdown body
	Dir           string            // absolute directory of the skill, or "" for embedded built-ins
	Source        Scope             // builtin | user | project
	SourcePath    string            // absolute path to SKILL.md, or "embed:builtins/<slug>/SKILL.md"
}

// SlashEnabled reports whether this skill should be exposed as a
// `/<name>` slash command. Default is enabled; opt-out by setting
// `metadata.slash: "false"` in the frontmatter.
func (s Skill) SlashEnabled() bool {
	v, ok := s.Metadata["slash"]
	if !ok {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "false", "no", "0", "off":
		return false
	}
	return true
}

// skillNamePattern enforces the spec's name rules: 1-64 chars,
// `[a-z0-9-]`, no leading/trailing/consecutive hyphens. Strict on
// purpose — the name doubles as a slash command name, a parent dir
// name, and a tool argument value, so a permissive pattern would
// invite escaping bugs across all three.
var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

const (
	maxNameLen          = 64
	maxDescriptionLen   = 1024
	maxCompatibilityLen = 500
)

// ParseSkillFile parses one SKILL.md into a Skill. expectedName is the
// parent directory name; the spec requires `name` to match the parent
// dir exactly, so we validate that here. Pass "" to skip the dir
// check (used by tests that hand in raw bytes).
//
// Frontmatter format (YAML-ish, deliberately tolerant of hand-edits,
// matching the subagents/usercmd parsing style — no full YAML library
// dependency for what is functionally a flat map of scalars):
//
//	---
//	name: remote-ops
//	description: Connect to and operate on remote hosts over SSH.
//	license: MIT
//	compatibility: linux, darwin
//	metadata:
//	  slash: "true"
//	  source-url: https://github.com/example/remote-ops
//	allowed-tools: Bash(ssh:*) Bash(scp:*) Read
//	---
//	<markdown body>
func ParseSkillFile(data []byte, expectedName string) (Skill, error) {
	s := string(data)
	if !strings.HasPrefix(s, "---\n") {
		return Skill{}, fmt.Errorf("missing frontmatter block (expected `---` on line 1)")
	}
	header, body, found := strings.Cut(s[4:], "\n---\n")
	if !found {
		return Skill{}, fmt.Errorf("unterminated frontmatter block (expected closing `---`)")
	}
	out := Skill{
		Body:     strings.TrimLeft(body, "\n"),
		Metadata: map[string]string{},
	}
	// Track whether we're inside the `metadata:` block — entries
	// indented under metadata become map keys; everything else is a
	// scalar at the top level.
	var inMetadata bool
	sc := bufio.NewScanner(strings.NewReader(header))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		// A top-level key resets `inMetadata`. We detect that by
		// "not indented" (no leading space/tab).
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inMetadata = false
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		trimKey := strings.TrimSpace(key)
		trimVal := strings.TrimSpace(val)
		if inMetadata {
			// Indented entry under metadata: store as-is, stripping
			// outer quotes for ergonomics.
			out.Metadata[trimKey] = unquote(trimVal)
			continue
		}
		switch trimKey {
		case "name":
			out.Name = unquote(trimVal)
		case "description":
			out.Description = unquote(trimVal)
		case "license":
			out.License = unquote(trimVal)
		case "compatibility":
			out.Compatibility = unquote(trimVal)
		case "allowed-tools":
			out.AllowedTools = parseAllowedTools(trimVal)
		case "metadata":
			// Either inline-map form `metadata: {k: v}` (rare) or
			// the block form where subsequent indented lines are
			// keys. We only support the block form for now —
			// matching how every example skill in the wild writes
			// it. An inline map would still parse the rest of the
			// header correctly because inMetadata is gated on
			// indentation.
			inMetadata = true
		}
	}
	if err := validate(out, expectedName); err != nil {
		return Skill{}, err
	}
	return out, nil
}

// validate enforces the spec's required-field rules + the optional-
// length caps. Returns a single error per problem so the caller can
// surface a clear message to the user.
func validate(s Skill, expectedName string) error {
	if s.Name == "" {
		return fmt.Errorf("frontmatter missing `name`")
	}
	if len(s.Name) > maxNameLen {
		return fmt.Errorf("`name` too long (%d > %d chars)", len(s.Name), maxNameLen)
	}
	if !skillNamePattern.MatchString(s.Name) {
		return fmt.Errorf("invalid `name` %q (allowed: lowercase letters, digits, hyphens; no leading/trailing/consecutive hyphens)", s.Name)
	}
	if expectedName != "" && s.Name != expectedName {
		return fmt.Errorf("`name` %q must match parent directory %q (spec requirement)", s.Name, expectedName)
	}
	if s.Description == "" {
		return fmt.Errorf("frontmatter missing `description`")
	}
	if len(s.Description) > maxDescriptionLen {
		return fmt.Errorf("`description` too long (%d > %d chars)", len(s.Description), maxDescriptionLen)
	}
	if len(s.Compatibility) > maxCompatibilityLen {
		return fmt.Errorf("`compatibility` too long (%d > %d chars)", len(s.Compatibility), maxCompatibilityLen)
	}
	if strings.TrimSpace(s.Body) == "" {
		return fmt.Errorf("body is empty (expected the skill content below the frontmatter)")
	}
	return nil
}

// parseAllowedTools splits the experimental `allowed-tools` value into
// individual entries. The spec example uses space-separated tokens
// like `Bash(git:*) Bash(jq:*) Read` — tokenization respects
// parentheses so we don't split `Bash(git:*)` mid-token.
func parseAllowedTools(val string) []string {
	val = strings.TrimSpace(val)
	if val == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	depth := 0
	flush := func() {
		t := strings.TrimSpace(cur.String())
		cur.Reset()
		if t != "" {
			out = append(out, t)
		}
	}
	for _, r := range val {
		switch r {
		case '(':
			depth++
			cur.WriteRune(r)
		case ')':
			if depth > 0 {
				depth--
			}
			cur.WriteRune(r)
		case ' ', '\t', ',':
			if depth == 0 {
				flush()
				continue
			}
			cur.WriteRune(r)
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// unquote strips a surrounding pair of matching `"` or `'` from val.
// Hand-edited frontmatter regularly carries quotes around values with
// colons or hyphens; the parser's job is to be tolerant of that.
func unquote(val string) string {
	if len(val) >= 2 {
		f, l := val[0], val[len(val)-1]
		if (f == '"' && l == '"') || (f == '\'' && l == '\'') {
			return val[1 : len(val)-1]
		}
	}
	return val
}
