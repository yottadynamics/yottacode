package usercmd

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// frontmatter holds the YAML keys we parse from a custom-command file.
// Unknown keys are silently ignored (forward-compat with Claude Code's
// model / allowed-tools that we may add in v2).
type frontmatter struct {
	Description  string `yaml:"description"`
	ArgumentHint string `yaml:"argument-hint"`
}

// parsed is the intermediate result of parse(): the frontmatter
// (possibly zero-value) plus the markdown body with the frontmatter
// block stripped and one leading newline trimmed.
type parsed struct {
	Frontmatter frontmatter
	Body        string
}

// parse splits a custom-command file into frontmatter + body. The
// frontmatter is the optional YAML block bounded by --- delimiters at
// the start of the file. Behavior:
//
//   - No opening "---\n": the entire content is the body; frontmatter
//     is zero-value.
//   - Opening but no closing delimiter: surface an error so an author
//     who typoed "----" or omitted the closer gets a clear signal
//     instead of having the rest of their command silently swallowed
//     into the "frontmatter".
//   - Closing found: parse the slice in between with yaml.v3, treat
//     everything after the closing delimiter as the body (one leading
//     newline trimmed so a clean blank line between frontmatter and
//     prose doesn't show up as indentation in the prompt).
func parse(content []byte) (parsed, error) {
	const delim = "---"
	s := string(content)

	if !strings.HasPrefix(s, delim+"\n") && s != delim && !strings.HasPrefix(s, delim+"\r\n") {
		return parsed{Body: s}, nil
	}
	// Skip the opening delimiter line.
	rest := strings.TrimPrefix(s, delim)
	rest = strings.TrimPrefix(rest, "\r")
	rest = strings.TrimPrefix(rest, "\n")

	closeIdx := findClosingDelim(rest)
	if closeIdx < 0 {
		return parsed{}, fmt.Errorf("frontmatter has no closing %q delimiter", delim)
	}

	yamlBlock := rest[:closeIdx]
	var fm frontmatter
	if strings.TrimSpace(yamlBlock) != "" {
		if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err != nil {
			return parsed{}, fmt.Errorf("frontmatter: %w", err)
		}
	}

	body := rest[closeIdx:]
	// Skip the closing delimiter line. closeIdx points at the start of
	// "---" preceded by a newline (or the start of the string).
	body = strings.TrimPrefix(body, delim)
	body = strings.TrimPrefix(body, "\r")
	body = strings.TrimPrefix(body, "\n")

	return parsed{Frontmatter: fm, Body: body}, nil
}

// findClosingDelim returns the byte offset of the next "---" that
// stands alone on its own line (preceded by start-of-string or "\n",
// followed by "\n" or end-of-string). Returns -1 when no such line
// exists. Matching "---" only at line boundaries means a literal
// "---" inside a YAML string field can't accidentally close the
// frontmatter.
func findClosingDelim(s string) int {
	const delim = "---"
	for i := 0; i+len(delim) <= len(s); i++ {
		if i > 0 && s[i-1] != '\n' {
			continue
		}
		if s[i:i+len(delim)] != delim {
			continue
		}
		end := i + len(delim)
		if end == len(s) || s[end] == '\n' || s[end] == '\r' {
			return i
		}
	}
	return -1
}
