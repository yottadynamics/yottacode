// Package permissions implements the project-local permissions layer that
// gates tool calls. Two files carry the rules:
//
//   - .yottacode/permissions.json        committable, team-shared rules
//   - .yottacode/permissions.local.json  gitignored, personal additions
//
// Both files merge at load time. Each file holds three pattern lists:
//
//   {
//     "permissions": {
//       "allow": ["Bash(go test *)", "Edit(internal/**)"],
//       "ask":   ["Bash(rm *)"],
//       "deny":  ["Bash(curl * | sh)", "Edit(/etc/**)"]
//     }
//   }
//
// Each rule is "<Tool>(<pattern>)". Tool names are capitalized rule
// prefixes (Bash, Read, Write, Edit, Mkdir, Copy, Move, Delete, List,
// Glob, Grep, Fetch, Git, Tests, Rollback) — distinct from internal tool
// names (run_bash, read_file, …). The mapping lives in tool_targets.go
// so per-tool descriptor extraction stays in one place.
//
// Pattern semantics:
//
//   - Path-typed permissions (Read/Write/Edit/Mkdir/Copy/Move/Delete/List)
//     match against a cwd-relative path via doublestar so `**` works as
//     expected. Absolute patterns (leading "/") match against the
//     resolved absolute path.
//   - String-typed permissions (Bash/Git/Glob/Grep/Fetch/Tests/Rollback)
//     match against a free-form descriptor (Bash command, joined Git
//     args, glob pattern, URL, …) with `*` = "any sequence" and `?` =
//     "any single char".
//
// Decision precedence: Deny > Allow > Ask > Default. Default means "the
// tool's own RequiresApproval policy decides" (i.e. the agent loop's
// pre-existing behavior).
package permissions

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/bmatcuk/doublestar/v4"
)

// Decision is the verdict for a single tool call.
type Decision int

const (
	// Default means no rule matched; the loop falls back to the tool's
	// own RequiresApproval (read-only auto-execute, mutators prompt).
	Default Decision = iota
	// Allow means a rule explicitly approves this call — execute
	// silently, no prompt.
	Allow
	// Ask means a rule explicitly forces a prompt even if the tool would
	// normally auto-execute. Useful for things like `Read(.env)`.
	Ask
	// Deny means a rule explicitly refuses this call — never execute,
	// even under --bypass-permissions. The user wrote the rule on
	// purpose; bypass is a "skip prompts" knob, not a "ignore my
	// policy" knob.
	Deny
)

func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Ask:
		return "ask"
	case Deny:
		return "deny"
	default:
		return "default"
	}
}

// Rule is one parsed entry from the permissions file: tool prefix + the
// raw pattern between the parens.
type Rule struct {
	Tool    string // e.g. "Bash", "Edit"
	Pattern string // e.g. "go test *", "internal/**"
	// Source is "permissions.json" or "permissions.local.json" — used
	// only for diagnostics in /permissions.
	Source string
}

// Permissions holds the loaded rules from a project's permissions
// files. Concurrency-safe: the agent goroutine reads via Evaluate while
// the TUI may write via AddAllow.
type Permissions struct {
	mu       sync.RWMutex
	cwd      string
	deny     []Rule
	allow    []Rule
	ask      []Rule
	// localPath is .yottacode/permissions.local.json — the only file
	// AddAllow ever writes to. The shared permissions.json is never
	// touched by the agent so a team's committable file stays under
	// version control.
	localPath  string
	sharedPath string
}

// fileShape mirrors the on-disk JSON exactly.
type fileShape struct {
	Permissions struct {
		Allow []string `json:"allow,omitempty"`
		Ask   []string `json:"ask,omitempty"`
		Deny  []string `json:"deny,omitempty"`
	} `json:"permissions"`
}

// Load reads <cwd>/.yottacode/permissions.json and
// .yottacode/permissions.local.json (both optional), merges their rule
// lists, and returns a usable Permissions value. Missing files are not
// errors. Malformed files are surfaced so the user can fix them rather
// than silently running with stale rules.
func Load(cwd string) (*Permissions, error) {
	if cwd == "" {
		return nil, errors.New("permissions: cwd is required")
	}
	dir := filepath.Join(cwd, ".yottacode")
	p := &Permissions{
		cwd:        cwd,
		sharedPath: filepath.Join(dir, "permissions.json"),
		localPath:  filepath.Join(dir, "permissions.local.json"),
	}
	if err := p.loadFile(p.sharedPath, "permissions.json"); err != nil {
		return nil, err
	}
	if err := p.loadFile(p.localPath, "permissions.local.json"); err != nil {
		return nil, err
	}
	return p, nil
}

// LoadEmpty returns a Permissions with no rules but with cwd set so
// AddAllow knows where to write. Useful for tests and for callers that
// want to skip disk I/O.
func LoadEmpty(cwd string) *Permissions {
	dir := filepath.Join(cwd, ".yottacode")
	return &Permissions{
		cwd:        cwd,
		sharedPath: filepath.Join(dir, "permissions.json"),
		localPath:  filepath.Join(dir, "permissions.local.json"),
	}
}

func (p *Permissions) loadFile(path, source string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("permissions: read %s: %w", path, err)
	}
	var shape fileShape
	if err := json.Unmarshal(b, &shape); err != nil {
		return fmt.Errorf("permissions: parse %s: %w", path, err)
	}
	parse := func(raw []string, dst *[]Rule) error {
		for _, r := range raw {
			rule, err := parseRule(r, source)
			if err != nil {
				return fmt.Errorf("permissions: %s: %w", path, err)
			}
			*dst = append(*dst, rule)
		}
		return nil
	}
	if err := parse(shape.Permissions.Deny, &p.deny); err != nil {
		return err
	}
	if err := parse(shape.Permissions.Allow, &p.allow); err != nil {
		return err
	}
	if err := parse(shape.Permissions.Ask, &p.ask); err != nil {
		return err
	}
	return nil
}

// Reload re-reads both files from disk, replacing the in-memory rule
// set. Useful after the user edits permissions.json with their editor.
func (p *Permissions) Reload() error {
	fresh, err := Load(p.cwd)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.deny, p.allow, p.ask = fresh.deny, fresh.allow, fresh.ask
	p.mu.Unlock()
	return nil
}

// ruleForm matches "Tool(pattern)" with whitespace tolerance and a
// possibly-paren-bearing pattern. We use a regex with a non-greedy match
// for the prefix and consume to the last ')'.
var ruleForm = regexp.MustCompile(`^\s*([A-Za-z][A-Za-z0-9_]*)\s*\((.*)\)\s*$`)

func parseRule(raw, source string) (Rule, error) {
	m := ruleForm.FindStringSubmatch(raw)
	if m == nil {
		return Rule{}, fmt.Errorf("invalid rule %q (want \"Tool(pattern)\")", raw)
	}
	return Rule{Tool: m[1], Pattern: m[2], Source: source}, nil
}

// Evaluate runs the precedence chain (deny > allow > ask) against a
// single tool call described by toolName + argsJSON. Returns Default
// when no rule matches, leaving the existing approval flow in charge.
//
// Multi-target calls (Target.Descriptors set, e.g. apply_diff touching
// several files) use ratcheted semantics: Deny if any path is denied,
// Allow only if every path matches an allow rule, Ask if any path
// matches ask. The conservative Allow rule prevents a diff that mixes
// rule-covered and unknown paths from skipping the modal.
func (p *Permissions) Evaluate(toolName, argsJSON string) Decision {
	if p == nil {
		return Default
	}
	target := targetFor(toolName, argsJSON, p.cwd)
	if target.PermName == "" {
		return Default
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if target.Multi {
		return p.evaluateMulti(target)
	}
	if matchAny(target, p.deny, p.cwd) {
		return Deny
	}
	if matchAny(target, p.allow, p.cwd) {
		return Allow
	}
	if matchAny(target, p.ask, p.cwd) {
		return Ask
	}
	return Default
}

// evaluateMulti iterates a multi-descriptor target through the
// precedence chain. Caller holds p.mu.
func (p *Permissions) evaluateMulti(target Target) Decision {
	descs := target.Descriptors
	// Empty descriptor slice (e.g. an apply_diff with no parseable
	// header lines) falls through to Default rather than auto-allowing.
	// "Allow if all match" with zero items would otherwise vacuously
	// allow — the wrong answer when the diff content is opaque.
	if len(descs) == 0 {
		return Default
	}
	hit := func(rules []Rule) (matchedAll, matchedAny bool) {
		matchedAll = true
		for _, d := range descs {
			sub := Target{PermName: target.PermName, Descriptor: d, IsPath: target.IsPath}
			if matchAny(sub, rules, p.cwd) {
				matchedAny = true
			} else {
				matchedAll = false
			}
		}
		return
	}
	if _, anyDeny := hit(p.deny); anyDeny {
		return Deny
	}
	if allAllow, _ := hit(p.allow); allAllow {
		return Allow
	}
	if _, anyAsk := hit(p.ask); anyAsk {
		return Ask
	}
	return Default
}

// AddAllow appends a rule to permissions.local.json (creating the file
// and parent dir if needed). Used by the TUI's "always allow" path.
// Idempotent: a duplicate rule is silently dropped.
func (p *Permissions) AddAllow(rule string) error {
	parsed, err := parseRule(rule, "permissions.local.json")
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, existing := range p.allow {
		if existing.Tool == parsed.Tool && existing.Pattern == parsed.Pattern {
			return nil
		}
	}
	p.allow = append(p.allow, parsed)

	// Persist by re-reading the local file (so we don't clobber
	// concurrent edits the user made), appending, and atomic-renaming.
	if err := os.MkdirAll(filepath.Dir(p.localPath), 0o700); err != nil {
		return err
	}
	var shape fileShape
	if b, err := os.ReadFile(p.localPath); err == nil {
		_ = json.Unmarshal(b, &shape)
	}
	for _, existing := range shape.Permissions.Allow {
		if existing == rule {
			return nil
		}
	}
	shape.Permissions.Allow = append(shape.Permissions.Allow, rule)
	out, err := json.MarshalIndent(shape, "", "  ")
	if err != nil {
		return err
	}
	tmp := p.localPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.localPath)
}

// Snapshot returns a copy of every loaded rule (deny + allow + ask) for
// /permissions display.
func (p *Permissions) Snapshot() (deny, allow, ask []Rule) {
	if p == nil {
		return nil, nil, nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	dup := func(in []Rule) []Rule {
		out := make([]Rule, len(in))
		copy(out, in)
		return out
	}
	return dup(p.deny), dup(p.allow), dup(p.ask)
}

// SharedPath returns the path to permissions.json for /permissions UX.
func (p *Permissions) SharedPath() string {
	if p == nil {
		return ""
	}
	return p.sharedPath
}

// LocalPath returns the path to permissions.local.json for /permissions UX.
func (p *Permissions) LocalPath() string {
	if p == nil {
		return ""
	}
	return p.localPath
}

// matchAny reports whether any rule in the list matches the target.
// cwd is threaded in so absolute-pattern rules (e.g. `Write(/tmp/**)`)
// can be compared against the cwd-relative descriptors the agent
// produces (e.g. `hello.txt` when cwd is `/tmp`).
func matchAny(target Target, rules []Rule, cwd string) bool {
	for _, r := range rules {
		if r.Tool != target.PermName {
			continue
		}
		if matchPattern(r.Pattern, target.Descriptor, cwd, target.IsPath) {
			return true
		}
	}
	return false
}

// matchPattern dispatches to the right matcher based on whether the
// target is a path (use doublestar) or a free-form string (custom
// `*`/`?` glob).
func matchPattern(pattern, value, cwd string, isPath bool) bool {
	if pattern == "" {
		return value == ""
	}
	if isPath {
		// Expand "~/foo" patterns to the resolved home dir so user-
		// authored rules match the absolute descriptors the agent
		// produces (descriptors are home-expanded in relPath).
		pattern = expandHome(pattern)
		ok, err := doublestar.PathMatch(pattern, value)
		if err == nil && ok {
			return true
		}
		// Absolute pattern + relative value: descriptors are cwd-
		// relative when the file is under cwd, but auto-derived rules
		// are now cwd-anchored absolute (`Write(/abs/cwd/**)`). Join
		// the value onto cwd before retrying so the rule matches.
		if strings.HasPrefix(pattern, "/") && !strings.HasPrefix(value, "/") {
			absValue := "/" + value
			if cwd != "" {
				absValue = filepath.ToSlash(filepath.Join(cwd, value))
			}
			ok, err := doublestar.PathMatch(pattern, absValue)
			if err == nil && ok {
				return true
			}
		}
		return false
	}
	return stringGlobMatch(pattern, value)
}

// stringGlobMatch implements `*` (any sequence) and `?` (any one char)
// semantics for free-form descriptor matching. Compiled to a regex per
// call — the pattern set is small so the cost is negligible vs adding
// a cache and the lifecycle pain that comes with one.
func stringGlobMatch(pattern, value string) bool {
	var sb strings.Builder
	sb.WriteString(`^`)
	for _, r := range pattern {
		switch r {
		case '*':
			sb.WriteString(`.*`)
		case '?':
			sb.WriteString(`.`)
		default:
			sb.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	sb.WriteString(`$`)
	re, err := regexp.Compile(sb.String())
	if err != nil {
		return false
	}
	return re.MatchString(value)
}
