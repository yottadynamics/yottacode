package tui

import (
	"os"
	"path"
	"sort"

	"github.com/yottadynamics/yottacode/internal/filerefs"
)

// walkFiles produces the candidate list for the @-palette: every file
// and directory under root up to fileWalkMaxDepth, capped at
// fileWalkMaxEntries, sorted by depth then path. Directories from
// fileWalkSkipDirs are pruned wholesale (saves time on .git +
// node_modules in particular).
//
// The walk is breadth-first so the cap sheds the DEEPEST entries rather
// than whichever top-level directories sort last: on a big monorepo
// `web/` still shows its top files even when `a…`-prefixed directories
// are huge. Within a directory entries are visited in name order, so
// results are deterministic.
func walkFiles(root string) []fileEntry {
	out, _ := walkFilesCapped(root)
	return out
}

// fileWalkMaxPerDir bounds how many entries one directory may contribute,
// so a single huge directory (a generated assets folder, a flat test-data
// dir) cannot starve its siblings of the overall cap.
const fileWalkMaxPerDir = 400

// walkFilesCapped is walkFiles plus whether anything was left out (the
// overall cap or a per-directory cap was hit). When true, the palette
// falls back to live directory listings for path-style queries so every
// file stays reachable by typing its directory.
func walkFilesCapped(root string) (out []fileEntry, capped bool) {
	if root == "" {
		return nil, false
	}
	type dirAt struct {
		rel   string // forward-slash path relative to root ("" = root)
		depth int
	}
	out = make([]fileEntry, 0, 256)
	queue := []dirAt{{"", 0}}
	for len(queue) > 0 {
		if len(out) >= fileWalkMaxEntries {
			capped = true
			break
		}
		cur := queue[0]
		queue = queue[1:]
		abs := root
		if cur.rel != "" {
			abs = root + string(os.PathSeparator) + cur.rel
		}
		// ReadDir returns entries sorted by filename. A permission error
		// or transient I/O failure just skips this directory: the
		// palette is best-effort UX, not a guarantee.
		entries, err := os.ReadDir(abs)
		if err != nil {
			continue
		}
		added := 0
		for _, d := range entries {
			if len(out) >= fileWalkMaxEntries {
				capped = true
				break
			}
			name := d.Name()
			if d.IsDir() {
				if _, skip := fileWalkSkipDirs[name]; skip {
					continue
				}
			} else if filerefs.IsSensitivePath(name) {
				continue
			}
			if added >= fileWalkMaxPerDir {
				capped = true
				break
			}
			rel := name
			if cur.rel != "" {
				rel = path.Join(cur.rel, name)
			}
			out = append(out, fileEntry{Path: rel, IsDir: d.IsDir()})
			added++
			// Depth of a directory's children is cur.depth+1; stop
			// descending once they would exceed the cap.
			if d.IsDir() && cur.depth+1 < fileWalkMaxDepth {
				queue = append(queue, dirAt{rel, cur.depth + 1})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		di, dj := slashCount(out[i].Path), slashCount(out[j].Path)
		if di != dj {
			return di < dj
		}
		return out[i].Path < out[j].Path
	})
	return out, capped
}

func slashCount(p string) int {
	n := 0
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			n++
		}
	}
	return n
}
