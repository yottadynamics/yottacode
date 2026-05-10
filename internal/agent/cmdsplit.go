package agent

import (
	"regexp"
	"strings"
)

// Risk classifies how dangerous a command segment looks at a glance.
// Used by the approval modal to color-code parts of a compound command
// so users can see destructive segments without parsing the whole line.
type Risk int

const (
	RiskNone Risk = iota
	RiskCaution
	RiskDestructive
)

func (r Risk) String() string {
	switch r {
	case RiskCaution:
		return "caution"
	case RiskDestructive:
		return "destructive"
	default:
		return "none"
	}
}

// CommandSegment is one piece of a (possibly) compound shell command,
// separated from the next segment by a logical operator or pipe. The
// separator that *precedes* this segment is recorded so the modal can
// label "and then" vs "or" vs "piped to" relationships.
type CommandSegment struct {
	Text      string // the segment, trimmed
	Separator string // "" for first segment; "&&", "||", ";", "|" thereafter
	Risk      Risk
	Reason    string // human-readable why this is flagged, "" when RiskNone
}

// SplitCommand parses a shell command into segments separated by &&,
// ||, ;, and pipes. Quoted metacharacters (`"foo && bar"`) and escaped
// ones (`\&\&`) are ignored. Command substitutions ($(...) and `...`)
// are NOT recursively split; the whole substitution stays as part of
// its enclosing segment with a "contains substitution" caution flag if
// the substitution is non-trivial.
//
// The output is suitable for display in the approval modal — not for
// execution semantics. We're trying to surface what a tired human
// might miss, not reproduce a real shell parser.
func SplitCommand(cmd string) []CommandSegment {
	if strings.TrimSpace(cmd) == "" {
		return nil
	}
	segments := splitOnSeparators(cmd)
	for i := range segments {
		segments[i].Text = strings.TrimSpace(segments[i].Text)
		segments[i].Risk, segments[i].Reason = AssessRisk(segments[i].Text)
	}
	// Drop empty segments that can result from `;;` or trailing operators.
	out := segments[:0]
	for _, s := range segments {
		if s.Text != "" {
			out = append(out, s)
		}
	}
	return out
}

// splitOnSeparators is the actual tokenizer. Single-pass char scan with
// quote and escape state tracking. Returns segments with the separator
// that preceded each (empty for the first).
func splitOnSeparators(s string) []CommandSegment {
	var (
		out     []CommandSegment
		current strings.Builder
		sep     = "" // the separator that came before `current`
		inSingle bool
		inDouble bool
		escape   bool
		// substitution depth — `$(...)` and backtick blocks are kept
		// intact so nested && etc. inside them don't split.
		parenDepth int
		inBack     bool
	)
	push := func() {
		out = append(out, CommandSegment{
			Text:      current.String(),
			Separator: sep,
		})
		current.Reset()
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escape {
			current.WriteByte(c)
			escape = false
			continue
		}
		if c == '\\' {
			current.WriteByte(c)
			escape = true
			continue
		}
		if !inSingle && !inDouble && !inBack && parenDepth == 0 {
			// Look for two-char separators first.
			if c == '&' && i+1 < len(s) && s[i+1] == '&' {
				push()
				sep = "&&"
				i++
				continue
			}
			if c == '|' && i+1 < len(s) && s[i+1] == '|' {
				push()
				sep = "||"
				i++
				continue
			}
			if c == ';' {
				push()
				sep = ";"
				continue
			}
			if c == '|' {
				push()
				sep = "|"
				continue
			}
			if c == '$' && i+1 < len(s) && s[i+1] == '(' {
				parenDepth++
				current.WriteByte(c)
				current.WriteByte(s[i+1])
				i++
				continue
			}
			if c == '`' {
				inBack = true
				current.WriteByte(c)
				continue
			}
		}
		if parenDepth > 0 {
			if c == '(' {
				parenDepth++
			} else if c == ')' {
				parenDepth--
			}
		}
		if inBack && c == '`' {
			inBack = false
		}
		if !inDouble && !inBack && parenDepth == 0 && c == '\'' {
			inSingle = !inSingle
		}
		if !inSingle && !inBack && parenDepth == 0 && c == '"' {
			inDouble = !inDouble
		}
		current.WriteByte(c)
	}
	push()
	return out
}

// destructiveRe matches obvious destructive patterns. Order matters —
// the first match wins on the assumption that "rm -rf" is more notable
// than "redirect" in a command containing both.
var destructiveRe = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`\brm\s+(-[rRfF]+\s+|--recursive\s+|--force\s+){1,2}`), "rm with -r/-f"},
	{regexp.MustCompile(`:\(\)\s*\{`), "fork bomb shape"},
	{regexp.MustCompile(`\bdd\s+if=`), "dd"},
	{regexp.MustCompile(`\bmkfs\b`), "mkfs"},
	{regexp.MustCompile(`>\s*/(etc|dev|var|usr|sys|proc|boot)\b`), "redirect to system path"},
	{regexp.MustCompile(`\bchmod\s+777\b`), "chmod 777"},
	{regexp.MustCompile(`\bsudo\s+rm\b`), "sudo rm"},
}

// cautionRe matches patterns that aren't outright destructive but
// deserve a second look — pipes into shells, sudo, redirects to any
// file, and curl-pipe-to-shell antipatterns.
var cautionRe = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`\b(curl|wget|fetch)\b.*\|\s*(sh|bash|zsh|sudo|python)\b`), "pipe download into shell"},
	{regexp.MustCompile(`\|\s*(sh|bash|zsh|python|node|perl|ruby)\b`), "pipe into interpreter"},
	{regexp.MustCompile(`\bsudo\b`), "sudo"},
	{regexp.MustCompile(`\bchmod\b`), "chmod"},
	{regexp.MustCompile(`\bchown\b`), "chown"},
	{regexp.MustCompile(`>\s*\S`), "redirect to file"},
	{regexp.MustCompile(`>>\s*\S`), "append to file"},
	{regexp.MustCompile(`\beval\b`), "eval"},
	{regexp.MustCompile(`\$\(`), "command substitution"},
	{regexp.MustCompile("`"), "backtick substitution"},
}

// AssessRisk classifies a single segment. Returns RiskNone for
// boring commands; RiskCaution for things worth a glance; RiskDestructive
// for patterns that almost always end in tears if mistakenly approved.
// The reason string is human-readable for display next to the segment.
func AssessRisk(segment string) (Risk, string) {
	if segment == "" {
		return RiskNone, ""
	}
	for _, p := range destructiveRe {
		if p.re.MatchString(segment) {
			return RiskDestructive, p.reason
		}
	}
	for _, p := range cautionRe {
		if p.re.MatchString(segment) {
			return RiskCaution, p.reason
		}
	}
	return RiskNone, ""
}
