package filerefs

import "strings"

// quoteSet is every character that would end or confuse an unquoted
// @-token: whitespace, the brackets/@ that terminate one, quote
// characters that would open a quoted form, and the backslash escape.
const quoteSet = " \t()[]{}@\"'\\"

// IsEscapable reports whether a backslash before c is an escape that
// Parse removes (so "my\ file\(1\).md" reads back as "my file(1).md").
func IsEscapable(c byte) bool {
	return strings.IndexByte(quoteSet, c) >= 0 && c != '\t'
}

// Quote returns path in the spelling Parse reads back unchanged after a
// leading "@": plain when nothing in it needs protection, otherwise
// double-quoted, single-quoted (when the path contains a double quote),
// or — when it contains both quote kinds — backslash-escaped.
func Quote(path string) string {
	if !strings.ContainsAny(path, quoteSet) {
		return path
	}
	switch {
	case !strings.ContainsAny(path, "\"\r\n"):
		return `"` + path + `"`
	case !strings.ContainsAny(path, "'\r\n"):
		return `'` + path + `'`
	}
	var b strings.Builder
	for i := 0; i < len(path); i++ {
		if IsEscapable(path[i]) {
			b.WriteByte('\\')
		}
		b.WriteByte(path[i])
	}
	return b.String()
}

// Unescape removes the backslash escapes Parse understands from an
// unquoted token body.
func Unescape(raw string) string {
	if !strings.Contains(raw, `\`) {
		return raw
	}
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' && i+1 < len(raw) && IsEscapable(raw[i+1]) {
			i++
		}
		b.WriteByte(raw[i])
	}
	return b.String()
}

// decodePath turns an unquoted token body into a filesystem path:
// file:// URLs are percent-decoded and backslash escapes are removed.
func decodePath(raw string) string {
	if strings.HasPrefix(raw, "file://") {
		return fileURLToPath(raw)
	}
	return Unescape(raw)
}
