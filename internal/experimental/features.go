// Package experimental gates not-yet-stable features behind named
// flags so early adopters can iterate on them while the default
// experience stays stable. A feature lives here when:
//
//   - The code is merged and tested, but
//   - The UX / model behavior / API shape isn't settled, and
//   - We want production users to encounter the feature only on
//     deliberate opt-in.
//
// Once a feature stabilizes the gate is removed (and Description
// returns "" for the removed name to keep old configs from
// erroring). New features land here as new Feature constants; the
// surface is intentionally small so adding one is one constant +
// one Description case.
//
// Resolution sources (CLI > env > config; later wins on conflict):
//
//   - --experimental <name>           (repeatable on CLI)
//   - $YOTTACODE_EXPERIMENTAL=name,…  (comma-separated env)
//   - [experimental] name = true      (config.toml section)
package experimental

import (
	"sort"
	"strings"
)

// Feature is a typed string used as the canonical identifier for an
// experimental capability. Strings are user-facing (they appear in
// flags, env vars, config sections) — pick names that are short,
// snake_case, and self-describing.
type Feature string

const (
	// BackgroundSubagents is a graduated no-op flag kept recognized for one
	// release so old configs don't warn or break. Background subagents are now
	// GA in the interactive TUI; the flag no longer gates behavior.
	BackgroundSubagents Feature = "background_subagents"

	// CodeMap enables the read-only repository structure map. It starts as an
	// outline-first graph index shared by the TUI and agent tools while the
	// dependency/impact-query UX settles.
	CodeMap Feature = "code_map"

	// Dispatch is still experimental: it enables the dispatch + integrate tools
	// for opt-in users while the decomposition + unattended-worker UX settles.
	Dispatch Feature = "dispatch"

	// LSPCodeIntelligence is a graduated no-op flag kept recognized for one
	// release so old configs don't warn or break. LSP tools are now default-on;
	// server launch still happens lazily only when a semantic tool is used.
	LSPCodeIntelligence Feature = "lsp_code_intelligence"

	// SyntaxRanges enables offline parser-backed syntax range selection. It is
	// separate from LSP so agents can choose structural edit ranges without
	// requiring a language server process.
	SyntaxRanges Feature = "syntax_ranges"
)

// All returns every recognized feature name in deterministic order.
// Used by the /experimental listing and by Parse's validation
// warnings. New features must be appended here when introduced.
func All() []Feature {
	return []Feature{
		BackgroundSubagents,
		CodeMap,
		Dispatch,
		LSPCodeIntelligence,
		SyntaxRanges,
	}
}

func IsGraduated(f Feature) bool {
	switch f {
	case BackgroundSubagents, LSPCodeIntelligence:
		return true
	default:
		return false
	}
}

// Description returns a one-line human-readable description for the
// `/experimental` overlay and docs. Returns "" for unknown names so
// graduated-feature configs don't break — callers should treat an
// empty string as "no longer recognized" and continue.
func Description(f Feature) string {
	switch f {
	case BackgroundSubagents:
		return "Background subagents have graduated to GA in the interactive TUI; this flag is recognized as a no-op for compatibility."
	case CodeMap:
		return "Repository code map. Builds a read-only structure index for the /map TUI overlay and code-map agent tools, using LSP when available and approximate fallback symbols otherwise."
	case Dispatch:
		return "Dispatch + integrate tools. Fan a batch of subtasks out to concurrent subagents (write-capable ones in isolated git worktrees, partitioned by file ownership), then merge committed branches into one integration branch for a PR."
	case LSPCodeIntelligence:
		return "LSP Code Intelligence has graduated to GA; this flag is recognized as a no-op for compatibility."
	case SyntaxRanges:
		return "Offline syntax range tools for selecting local structural edit ranges before anchored edits."
	default:
		return ""
	}
}

// Recognized reports whether name is in the current `All()` list.
// Unknown names should be tolerated (not erroring) but worth warning
// about — typos in `--experimental` shouldn't lock the user out, and
// removed features shouldn't break old configs. Caller decides what
// to do with the boolean.
func Recognized(name string) bool {
	for _, f := range All() {
		if string(f) == name {
			return true
		}
	}
	return false
}

// Set tracks which features are enabled in this session. Built once
// at startup from the three resolution sources (CLI/env/config) and
// passed by pointer to subsystems that need to gate behavior. Safe
// for concurrent read after construction; not safe for concurrent
// mutation (the startup wiring is single-threaded).
type Set struct {
	enabled map[Feature]bool
	// unknown carries feature names that were passed in but aren't
	// in the recognized list. We hold them rather than discarding
	// so callers can surface a startup warning ("you enabled
	// `foo_feature` but I don't recognize that name — typo? a
	// graduated/removed feature?").
	unknown []string
}

// NewSet returns an empty Set. Callers typically follow up with
// Parse / Enable.
func NewSet() *Set {
	return &Set{enabled: map[Feature]bool{}}
}

// Enable turns the named feature on. Unknown names are stashed in
// `unknown` (see UnknownNames) rather than rejected so the rest of
// the session continues normally.
func (s *Set) Enable(name string) {
	if s == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	if Recognized(name) {
		s.enabled[Feature(name)] = true
		return
	}
	s.unknown = append(s.unknown, name)
}

// EnableFeature is the typed-name variant; useful when callers
// already have a Feature constant (e.g. tests).
func (s *Set) EnableFeature(f Feature) {
	if s == nil {
		return
	}
	s.enabled[f] = true
}

// IsEnabled reports whether the given feature is on. Nil-safe so
// subsystems that haven't been wired yet (or test paths that don't
// construct a Set) can call this without guarding.
func (s *Set) IsEnabled(f Feature) bool {
	if s == nil {
		return false
	}
	return s.enabled[f]
}

// EnabledNames returns the on-features sorted alphabetically. Used
// for the status-bar / startup-banner "experimental: X, Y, Z"
// surface and for the /experimental overlay.
func (s *Set) EnabledNames() []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.enabled))
	for f := range s.enabled {
		out = append(out, string(f))
	}
	sort.Strings(out)
	return out
}

// UnknownNames returns the names Enable was asked to turn on that
// don't match a recognized feature. Caller can render these as a
// startup warning so users with typos or graduated features
// notice.
func (s *Set) UnknownNames() []string {
	if s == nil {
		return nil
	}
	out := append([]string(nil), s.unknown...)
	sort.Strings(out)
	return out
}

// Parse merges a comma-separated list of feature names into s.
// Whitespace around names is trimmed; empty entries are skipped;
// unknown names go to UnknownNames. Useful for env vars and any
// future single-string source.
func (s *Set) Parse(commaList string) {
	if s == nil {
		return
	}
	for _, raw := range strings.Split(commaList, ",") {
		s.Enable(raw)
	}
}
