package filerefs

import "strings"

// trailingPunct is stripped from the right edge of an unquoted path.
// Periods are deliberately excluded — `.` is part of normal extensions
// and stripping it would mangle "@foo.go" anywhere it sits next to a
// sentence boundary. We accept that "@foo.go." (sentence-final) keeps
// the trailing dot and may fail to load; the user gets a clear error.
const trailingPunct = ",;:!?)]}>"

// isBoundary reports whether b may precede an `@` that starts a
// reference. It keeps "email@example.com" from being treated as a file.
func isBoundary(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '(', '[', '{', ',', ';':
		return true
	}
	return false
}

func isPathEnd(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '@', '(', ')', '[', ']', '{', '}':
		return true
	}
	return false
}

// span is one detected reference: input[at] is the '@', input[at:end]
// is the whole token (quotes and escapes included, trailing punctuation
// excluded), and path is the decoded filesystem path.
type span struct {
	at, end int
	path    string
}

// scan finds every @-reference in input. Supported spellings:
//
//	@dir/file.go                plain
//	@"dir/my file.png"          double- or single-quoted (spaces allowed)
//	@dir/my\ file.png           backslash-escaped spaces
//	@file:///home/u/a%20b.png   file:// URL (percent-decoded)
//	@/abs/path, @~/path         absolute and home-relative
func scan(input string) []span {
	var out []span
	for i := 0; i < len(input); i++ {
		if input[i] != '@' || (i > 0 && !isBoundary(input[i-1])) {
			continue
		}
		j := i + 1
		if j >= len(input) {
			break
		}
		// Quoted form: needs a closing quote on the same line.
		if q := input[j]; q == '"' || q == '\'' {
			if rel := strings.IndexByte(input[j+1:], q); rel > 0 {
				body := input[j+1 : j+1+rel]
				if !strings.ContainsAny(body, "\r\n") {
					end := j + 1 + rel + 1
					// Quoted bodies are literal (only a file:// URL is decoded).
					path := body
					if strings.HasPrefix(body, "file://") {
						path = fileURLToPath(body)
					}
					out = append(out, span{at: i, end: end, path: path})
					i = end - 1
					continue
				}
			}
		}
		// Unquoted: runs to whitespace or a bracket/@, honouring
		// backslash escapes ("\ ", "\(", "\[", …).
		k := j
		for k < len(input) {
			if input[k] == '\\' && k+1 < len(input) && IsEscapable(input[k+1]) {
				k += 2
				continue
			}
			if isPathEnd(input[k]) {
				break
			}
			k++
		}
		raw := strings.TrimRight(input[j:k], trailingPunct)
		if raw != "" && raw != "." && raw != ".." {
			out = append(out, span{at: i, end: j + len(raw), path: decodePath(raw)})
		}
		i = k - 1
	}
	return out
}

// fileURLToPath converts a file:// URL to a local path, decoding
// percent-encoded characters.
func fileURLToPath(s string) string {
	s = strings.TrimPrefix(s, "file://")
	s = strings.TrimPrefix(s, "localhost")
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if hi, lo := unhex(s[i+1]), unhex(s[i+2]); hi >= 0 && lo >= 0 {
				b.WriteByte(byte(hi<<4 | lo))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func unhex(c byte) int {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0')
	case 'a' <= c && c <= 'f':
		return int(c - 'a' + 10)
	case 'A' <= c && c <= 'F':
		return int(c - 'A' + 10)
	}
	return -1
}

// Parse scans input for @<path> tokens and returns one Ref per unique
// path, in first-appearance order. The Token field preserves the
// original "@path" form for echoing back to the user; Path is the
// decoded version Load consumes. Empty input or no matches → nil.
func Parse(input string) []Ref {
	if !strings.Contains(input, "@") {
		return nil
	}
	spans := scan(input)
	if len(spans) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(spans))
	out := make([]Ref, 0, len(spans))
	for _, sp := range spans {
		if seen[sp.path] {
			continue
		}
		seen[sp.path] = true
		out = append(out, Ref{Token: input[sp.at:sp.end], Path: sp.path})
	}
	return out
}
