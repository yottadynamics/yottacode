package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yottadynamics/yottacode/internal/filerefs"
)

// externalListMax bounds one browsed directory listing.
const externalListMax = 200

// decodePaletteQuery turns the raw text typed after `@` into the path
// the user means. A leading quote opens a literal (still unterminated)
// quoted path; otherwise backslash escapes are removed.
func decodePaletteQuery(raw string) string {
	if raw != "" && (raw[0] == '"' || raw[0] == '\'') {
		return raw[1:]
	}
	return filerefs.Unescape(raw)
}

// paletteInsert renders the text that follows the `@` when choice is
// accepted, in the spelling the parser reads back unchanged
// (filerefs.Quote): names with spaces, brackets, parens, `@` or quotes —
// Next.js `[id].tsx` and `(group)` routes, "Screenshot from ….png" — get
// quoted or escaped. Files end with a space; directories stay open
// (quote left unterminated) so the user can keep drilling in.
func paletteInsert(choice fileEntry) string {
	q := filerefs.Quote(choice.Path)
	if !choice.IsDir {
		return q + " "
	}
	if q != "" && (q[0] == '"' || q[0] == '\'') && len(q) >= 2 && q[len(q)-1] == q[0] {
		return q[:len(q)-1] + "/" // reopen: `"dir name` + "/"
	}
	return q + "/"
}

// isExternalQuery reports whether the (decoded) query points outside
// the walked project tree: absolute, home-relative or parent-relative.
func isExternalQuery(q string) bool {
	return strings.HasPrefix(q, "/") || q == "~" || strings.HasPrefix(q, "~/") ||
		q == ".." || strings.HasPrefix(q, "../")
}

// listExternal browses the directory part of an external query and
// filters its entries by the final path segment. Directories come
// first, then files newest-first (the file you just saved, such as a
// screenshot, is usually the one wanted). Dotfiles stay hidden unless
// the user starts the name with a dot.
func listExternal(query, cwd string) []fileEntry {
	if query == "~" || query == ".." {
		query += "/"
	}
	slash := strings.LastIndexByte(query, '/')
	dirPart, namePart := query[:slash+1], query[slash+1:]

	dir := dirPart
	if dir == "~/" || strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		dir = filepath.Join(home, dir[2:])
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(cwd, dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	type item struct {
		e    fileEntry
		mod  int64
		rank int
	}
	lowerName := strings.ToLower(namePart)
	showDot := strings.HasPrefix(namePart, ".")
	items := make([]item, 0, len(entries))
	for _, de := range entries {
		name := de.Name()
		if strings.HasPrefix(name, ".") && !showDot {
			continue
		}
		rank := 0
		if lowerName != "" {
			ln := strings.ToLower(name)
			switch {
			case strings.HasPrefix(ln, lowerName):
				rank = 0
			case strings.Contains(ln, lowerName):
				rank = 1
			default:
				continue
			}
		}
		isDir := de.IsDir()
		var mod int64
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil { // follows symlinks
			isDir = info.IsDir()
			mod = info.ModTime().UnixNano()
		}
		if !isDir && filerefs.IsSensitivePath(name) {
			continue
		}
		// Hide what attaching would refuse anyway (credential dirs and
		// files, symlinks into them) instead of offering a dead end.
		if filerefs.Blocked(filepath.Join(dir, name), cwd) != nil {
			continue
		}
		items = append(items, item{fileEntry{Path: dirPart + name, IsDir: isDir}, mod, rank})
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if a.e.IsDir != b.e.IsDir {
			return a.e.IsDir
		}
		if a.e.IsDir {
			return a.e.Path < b.e.Path
		}
		return a.mod > b.mod
	})
	if len(items) > externalListMax {
		items = items[:externalListMax]
	}
	out := make([]fileEntry, len(items))
	for i, it := range items {
		out[i] = it.e
	}
	return out
}
