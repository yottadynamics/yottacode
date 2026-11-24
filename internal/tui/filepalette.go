package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yottadynamics/yottacode/internal/filerefs"
)

// fileWalkMaxDepth caps directory recursion when collecting candidates
// for the @-palette. Six path segments reach "internal/tui/foo.go" and
// deeper without descending into nested vendor trees that would balloon
// the listing.
const fileWalkMaxDepth = 6

// fileWalkMaxEntries caps the candidate list. The palette is fuzzy-
// filtered as the user types so an absolute upper bound keeps walks
// bounded on huge monorepos. 2000 hits the sweet spot — large enough
// to cover any realistic project, small enough to keep fuzzy filter
// cost imperceptible at every keystroke.
const fileWalkMaxEntries = 2000

// fileWalkSkipDirs is the always-skip list. We keep dot-directories
// out (.git, .next, …) but allow individual dotfiles (.gitignore,
// .env.example) to surface; the LLM often needs to read those.
var fileWalkSkipDirs = map[string]struct{}{
	".git":          {},
	".yottacode":    {},
	".next":         {},
	".cache":        {},
	".venv":         {},
	".idea":         {},
	".vscode":       {},
	".hg":           {},
	".svn":          {},
	"node_modules":  {},
	"vendor":        {},
	"dist":          {},
	"build":         {},
	"target":        {},
	"__pycache__":   {},
	"venv":          {},
	"env":           {},
	"out":           {},
	"coverage":      {},
	".pytest_cache": {},
	".mypy_cache":   {},
	".tox":          {},
}

// fileEntry pairs a path with whether it's a directory. Directories
// are flagged so the palette can render a trailing "/" and the user
// knows tab-completing keeps the palette open for further nesting.
type fileEntry struct {
	Path  string // relative to walk root, forward-slash separated
	IsDir bool
}

// filterFilePalette ranks entries against the user's query (the part
// after `@`). Empty query returns all entries in walk order. Otherwise
// we score each candidate, cheap-fuzzy:
//
//   - basename equals query             (rank 0; perfect)
//   - basename has-prefix query         (rank 1)
//   - basename contains query           (rank 2)
//   - path has-prefix query             (rank 3)
//   - path contains query               (rank 4)
//   - subsequence match across path     (rank 5)
//
// Anything not matching is dropped. Comparisons are case-insensitive
// to match how people type ("@read" should hit "ReadFile" too).
//
// The full ranked list is returned — windowing to filePaletteVisible
// rows happens in renderFilePalette so the user can scroll past the
// initial window with Up/Down.
func filterFilePalette(query string, entries []fileEntry) []fileEntry {
	if query == "" {
		out := make([]fileEntry, len(entries))
		copy(out, entries)
		return out
	}
	// A slash means the user is navigating a path, not searching for a
	// filename. Keep completion scoped to that path or its descendants so
	// an input such as @roadmap/ cannot surface unrelated fuzzy matches.
	if strings.Contains(query, "/") && !strings.HasSuffix(query, "/") {
		// A partial final segment ("docs/RE", "app/[id"): plain
		// case-insensitive prefix match on the whole path, still scoped
		// to the typed directories.
		prefix := strings.ToLower(strings.TrimPrefix(filepath.ToSlash(query), "./"))
		out := make([]fileEntry, 0, 16)
		for _, e := range entries {
			if strings.HasPrefix(strings.ToLower(filepath.ToSlash(e.Path)), prefix) {
				out = append(out, e)
			}
		}
		return out
	}
	if strings.Contains(query, "/") {
		prefix := filepath.ToSlash(filepath.Clean(query))
		prefix = strings.TrimPrefix(prefix, "./")
		prefixLower := strings.ToLower(prefix)
		out := make([]fileEntry, 0, len(entries))
		for _, e := range entries {
			path := filepath.ToSlash(e.Path)
			pathLower := strings.ToLower(path)
			if pathLower == prefixLower || strings.HasPrefix(pathLower, prefixLower+"/") {
				out = append(out, e)
			}
		}
		return out
	}
	q := strings.ToLower(query)
	type scored struct {
		fileEntry
		rank int
	}
	hits := make([]scored, 0, 64)
	for _, e := range entries {
		base := strings.ToLower(filepath.Base(e.Path))
		full := strings.ToLower(e.Path)
		switch {
		case base == q:
			hits = append(hits, scored{e, 0})
		case strings.HasPrefix(base, q):
			hits = append(hits, scored{e, 1})
		case wordInitialsMatch(q, base):
			hits = append(hits, scored{e, 2})
		case strings.Contains(base, q):
			hits = append(hits, scored{e, 3})
		case strings.HasPrefix(full, q):
			hits = append(hits, scored{e, 4})
		case wordInitialsMatch(q, full):
			hits = append(hits, scored{e, 5})
		case strings.Contains(full, q):
			hits = append(hits, scored{e, 6})
		case isSubsequence(q, full):
			hits = append(hits, scored{e, 7})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].rank != hits[j].rank {
			return hits[i].rank < hits[j].rank
		}
		// At equal rank, shallower paths sort first — `main.go` should
		// beat `internal/x/main.go` when both are perfect basename hits.
		di := strings.Count(hits[i].Path, "/")
		dj := strings.Count(hits[j].Path, "/")
		if di != dj {
			return di < dj
		}
		return hits[i].Path < hits[j].Path
	})
	out := make([]fileEntry, len(hits))
	for i, h := range hits {
		out[i] = h.fileEntry
	}
	return out
}

// isSubsequence reports whether needle's characters appear in order
// inside haystack (not necessarily contiguous). Lets queries like
// "iclop" match "internal/cli/options.go" — typist-friendly.
func isSubsequence(needle, haystack string) bool {
	if needle == "" {
		return true
	}
	i := 0
	for j := 0; j < len(haystack) && i < len(needle); j++ {
		if haystack[j] == needle[i] {
			i++
		}
	}
	return i == len(needle)
}

// wordInitialsMatch reports whether query characters match the starts of words
// in candidate. Separators such as '-', '_', '/', and '.' open a new word; a
// lower→upper transition does too. This keeps terse filters like "grp" useful
// for "git-review-pr" and "mp" useful for "model_picker.go".
func wordInitialsMatch(query, candidate string) bool {
	if query == "" {
		return true
	}
	qi := 0
	prev := rune(0)
	for i, r := range candidate {
		start := i == 0 || isWordSeparator(prev) || isCamelBoundary(prev, r)
		if start && r == rune(query[qi]) {
			qi++
			if qi == len(query) {
				return true
			}
		}
		prev = r
	}
	return false
}

func isWordSeparator(r rune) bool {
	switch r {
	case '-', '_', '/', '.', ' ':
		return true
	default:
		return false
	}
}

func isCamelBoundary(prev, cur rune) bool {
	return prev >= 'a' && prev <= 'z' && cur >= 'A' && cur <= 'Z'
}

// filePaletteVisible caps how many entries the rendered palette shows.
// Same vertical budget as the slash palette (8 rows + chrome) feels
// consistent across both palettes.
const filePaletteVisible = 8

// renderFilePalette mirrors the slash palette layout: a bordered box
// with one entry per line, the highlighted entry styled. Empty list
// shows a hint instead of a blank box.
//
// width is the TOTAL box width including the border, same contract as
// renderPalette — the -2 at the render calls converts to lipgloss's
// border-exclusive Style.Width so the box's right edge lands exactly
// on the input frame's.
//
// When the filtered list is longer than filePaletteVisible, we render
// only the window [offset, offset+filePaletteVisible) and add muted
// `▲ N more` / `▼ N more` hints above/below to make the hidden
// entries discoverable. The caller (Model navigation) is responsible
// for keeping `offset` in sync with `idx` so the selection stays in
// view as the user arrows past the window edge.
func renderFilePalette(items []fileEntry, idx, offset, width int) string {
	if len(items) == 0 {
		return stylePaletteBox.Width(width).Render(styleEmpty.Render("(no matching files) · Esc to close"))
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		offset = len(items) - 1
	}
	end := offset + filePaletteVisible
	if end > len(items) {
		end = len(items)
	}
	var lines []string
	lines = append(lines, styleSplashTitle.Render(filePaletteTitle))
	lines = append(lines, renderMenuDivider(max(width-4, 1)))

	if offset > 0 {
		lines = append(lines, styleMeta.Render(fmt.Sprintf(" ▲ %d more", offset)))
	}
	for i := offset; i < end; i++ {
		e := items[i]
		suffix := ""
		if e.IsDir {
			suffix = "/"
		}
		line := fmt.Sprintf(" @%s%s", e.Path, suffix)
		if i == idx {
			line = stylePaletteSelected.Render(line)
		} else {
			line = stylePaletteItem.Render(line)
		}
		lines = append(lines, line)
	}
	if remaining := len(items) - end; remaining > 0 {
		lines = append(lines, styleMeta.Render(fmt.Sprintf(" ▼ %d more", remaining)))
	}
	return stylePaletteBox.Width(width).Render(strings.Join(lines, "\n"))
}

// extractFileQuery scans value for an active `@<query>` token whose
// `@` sits at the start of input or after whitespace. It returns the
// query (the part after `@`, possibly empty), and the byte index of
// the `@` itself so the caller can splice in a chosen path. found is
// false when no active `@` token is present at the END of the input
// — this is what gates the file palette open/close decision.
//
// The token is required to be at the END of value because we want the
// palette to track what the user is actively typing. A `@foo` earlier
// in the input that's already been "committed" (followed by a space
// and more text) should not retrigger the palette.
func extractFileQuery(value string) (query string, atIdx int, found bool) {
	if value == "" {
		return "", -1, false
	}
	// The active token starts after the last unquoted, unescaped
	// separator. A `\ ` escape and an open `@"…` / `@'…` quote keep
	// spaces inside the token (paths such as "Screenshot from …png").
	tokenStart := 0
	var inQuote byte
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case inQuote != 0:
			if c == inQuote {
				inQuote = 0
			}
		case c == '\\' && i+1 < len(value) && filerefs.IsEscapable(value[i+1]):
			i++
		case (c == '"' || c == '\'') && i > 0 && i-1 == tokenStart && value[i-1] == '@':
			inQuote = c
		case c == ' ' || c == '\t' || c == '\n':
			tokenStart = i + 1
		case strings.IndexByte("([{,;", c) >= 0:
			// Inside an @-token these are path characters being typed
			// ("@app/[id].tsx", "@(group)/x") — except a `,`/`;` glued to
			// the next reference ("@a,@b"). Outside one they just open a
			// new token ("(@foo").
			inAt := tokenStart < len(value) && value[tokenStart] == '@'
			if !inAt || ((c == ',' || c == ';') && i+1 < len(value) && value[i+1] == '@') {
				tokenStart = i + 1
			}
		}
	}
	token := value[tokenStart:]
	if !strings.HasPrefix(token, "@") {
		return "", -1, false
	}
	// `@` followed immediately by another `@` (e.g. `@@`) isn't a path
	// reference — bail so the user can type plain text.
	if len(token) >= 2 && token[1] == '@' {
		return "", -1, false
	}
	return token[1:], tokenStart, true
}
