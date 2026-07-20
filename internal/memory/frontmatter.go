package memory

import (
	"bufio"
	"fmt"
	"strings"
	"time"
)

// Frontmatter is the YAML-ish header on every agent-managed memory
// file. Real YAML is overkill for four flat fields — a tolerant
// key:value scanner keeps the writer one-line-per-field and the
// reader tiny.
type Frontmatter struct {
	Name          string
	Type          string
	Description   string
	Created       string
	SourceSession string
	SourceTurn    string
}

// ParseFrontmatter splits frontmatter from body. Returns ok=false when
// the frontmatter block is missing or malformed; in that case fm is
// zero and body equals the input. Tolerant of files written by hand
// (no frontmatter at all is fine — caller defaults Type to "reference"
// and Name to the basename).
func ParseFrontmatter(data []byte) (fm Frontmatter, body string, ok bool) {
	// Normalize CRLF first: files hand-edited on Windows / under WSL
	// otherwise miss the "\n---\n" fence and get treated as headerless,
	// which silently injects the raw frontmatter as body text.
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return Frontmatter{}, s, false
	}
	header, rest, found := strings.Cut(s[4:], "\n---\n")
	if !found {
		// Tolerate a closing fence that is the final line with no trailing
		// newline ("...\n---" at EOF) — an empty-body memory.
		if after := s[4:]; strings.HasSuffix(after, "\n---") {
			header = strings.TrimSuffix(after, "\n---")
			rest = ""
		} else {
			return Frontmatter{}, s, false
		}
	}
	body = rest
	sc := bufio.NewScanner(strings.NewReader(header))
	for sc.Scan() {
		line := sc.Text()
		key, val, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "name":
			fm.Name = val
		case "type":
			fm.Type = val
		case "description":
			fm.Description = val
		case "created":
			fm.Created = val
		case "source_session":
			fm.SourceSession = val
		case "source_turn":
			fm.SourceTurn = val
		}
	}
	return fm, body, true
}

// Source records optional provenance for a memory save. It is intentionally
// lightweight frontmatter metadata, not an embedded transcript: session recall
// remains the path for full source context.
type Source struct {
	Session string
	Turn    string
}

// RenderFrontmatter writes the base header. Description is
// expected to already be one line (caller strips newlines before
// passing); the writer doesn't re-validate. Created is rendered as
// RFC3339 in UTC.
func RenderFrontmatter(name, memType, description string, created time.Time) string {
	return RenderFrontmatterWithSource(name, memType, description, created, Source{})
}

// RenderFrontmatterWithSource writes the memory header and includes source
// provenance fields when they are available.
func RenderFrontmatterWithSource(name, memType, description string, created time.Time, source Source) string {
	var b strings.Builder
	fmt.Fprintf(&b, "---\nname: %s\ntype: %s\ndescription: %s\ncreated: %s\n",
		name, memType, description, created.UTC().Format(time.RFC3339))
	if source.Session != "" {
		fmt.Fprintf(&b, "source_session: %s\n", source.Session)
	}
	if source.Turn != "" {
		fmt.Fprintf(&b, "source_turn: %s\n", source.Turn)
	}
	b.WriteString("---\n")
	return b.String()
}
