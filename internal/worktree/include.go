package worktree

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// IncludeFile is the per-repo file (in the repo root) that lists
// gitignored files which yottacode should copy into each fresh
// worktree. Without it the new worktree is missing .env / IDE
// configs / build artifacts the user's project actually needs to
// run.
//
// Format: one line per glob pattern, relative to the repo root.
// Lines starting with '#' and blank lines are comments. Patterns
// use filepath.Match semantics — sufficient for the common cases
// (.env, .env.*, .vscode/settings.json) without dragging in a full
// gitignore parser.
const IncludeFile = ".worktreeinclude"

// ReadIncludePatterns reads <repoRoot>/.worktreeinclude and returns
// the parsed list of patterns. A missing file is not an error — it
// just means no extra files get copied.
func ReadIncludePatterns(repoRoot string) ([]string, error) {
	f, err := os.Open(filepath.Join(repoRoot, IncludeFile))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var patterns []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns, sc.Err()
}

// CopyIncluded copies files matching .worktreeinclude patterns from
// repoRoot into worktreeDir, preserving relative paths and creating
// parent directories as needed. Patterns are matched against paths
// relative to repoRoot using filepath.Match for each segment, with
// '**' supported as "any number of intermediate segments".
//
// Files that don't exist at the source are silently skipped (the
// user's .worktreeinclude may list optional files like .env.local).
// Symlinks and other non-regular files (devices, FIFOs, sockets) are
// skipped rather than followed: matchPatterns surfaces a symlink as an
// ordinary entry, and following one could copy a file from outside the
// repo (e.g. a symlinked ~/.ssh/id_rsa) into the worktree or hang on a
// device. Files that exist but are unreadable cause an error so the
// user isn't silently shipped a broken worktree.
func CopyIncluded(repoRoot, worktreeDir string) error {
	patterns, err := ReadIncludePatterns(repoRoot)
	if err != nil {
		return fmt.Errorf("worktree: read %s: %w", IncludeFile, err)
	}
	if len(patterns) == 0 {
		return nil
	}
	matches, err := matchPatterns(repoRoot, patterns)
	if err != nil {
		return err
	}
	for _, rel := range matches {
		src := filepath.Join(repoRoot, rel)
		dst := filepath.Join(worktreeDir, rel)
		if err := copyOne(src, dst); err != nil {
			return fmt.Errorf("worktree: copy %s: %w", rel, err)
		}
	}
	return nil
}

// matchPatterns walks repoRoot once and returns every relative path
// (file, not dir) matching at least one pattern. The walk is cheap
// (a typical repo is in the low thousands of files) and avoids the
// surprises of running filepath.Glob with `**` glued in.
//
// Skips .git/ and any stray .yottacode/worktrees/ subdir (left over
// from a pre-relocation install) to keep the walk bounded.
func matchPatterns(repoRoot string, patterns []string) ([]string, error) {
	var out []string
	staleInRepoWorktrees := filepath.Join(repoRoot, ".yottacode", "worktrees")
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			base := d.Name()
			if path != repoRoot && (base == ".git" || path == staleInRepoWorktrees) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		for _, p := range patterns {
			ok, err := matchPattern(p, rel)
			if err != nil {
				return fmt.Errorf("worktree: invalid pattern %q: %w", p, err)
			}
			if ok {
				out = append(out, rel)
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// matchPattern reports whether rel (forward-slash, repo-relative)
// matches the given pattern. Supports '**' as a wildcard that spans
// zero or more path segments; other segments use filepath.Match
// semantics ('*', '?', '[…]').
func matchPattern(pattern, rel string) (bool, error) {
	pp := strings.Split(pattern, "/")
	rp := strings.Split(rel, "/")
	return matchSegments(pp, rp)
}

func matchSegments(pp, rp []string) (bool, error) {
	switch {
	case len(pp) == 0:
		return len(rp) == 0, nil
	case pp[0] == "**":
		// '**' matches zero or more path segments. Try every split.
		// If '**' is the last segment, it matches the rest entirely.
		if len(pp) == 1 {
			return true, nil
		}
		for i := 0; i <= len(rp); i++ {
			ok, err := matchSegments(pp[1:], rp[i:])
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	case len(rp) == 0:
		return false, nil
	default:
		ok, err := filepath.Match(pp[0], rp[0])
		if err != nil || !ok {
			return false, err
		}
		return matchSegments(pp[1:], rp[1:])
	}
}

func copyOne(src, dst string) error {
	// Lstat (not Stat / Open) so a symlink is inspected as the link
	// itself, never followed. matchPatterns surfaces a symlink-to-file
	// as a regular DirEntry, so without this guard copyOne would open
	// the link target — a file outside the repo (e.g. ~/.ssh/id_rsa) or
	// a device like /dev/zero that io.Copy would stream forever. Skip
	// anything that isn't a plain regular file (directories included —
	// defensive, since matchPatterns only emits files).
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}
