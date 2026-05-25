package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

const (
	defaultListMax       = 100
	defaultGlobMax       = 200
	defaultGrepMax       = 50
	maxGrepOutBytes      = 256 * 1024
	defaultStructureMax  = 500
	defaultStructureDepth = 3
)

// ListDirTool returns the immediate children of a directory.
type ListDirTool struct {
	Cwd *CwdRef
}

func (t *ListDirTool) Name() string { return "list_dir" }

func (t *ListDirTool) Description() string {
	return "List the immediate children of a directory. Each line is '<type>\\t<name>' where type is d (directory), f (regular file), or l (symlink)."
}

func (t *ListDirTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":        map[string]any{"type": "string", "description": "Directory path (default: cwd)"},
			"max_entries": map[string]any{"type": "integer", "description": fmt.Sprintf("Cap on number of entries (default %d)", defaultListMax)},
		},
	}
}

func (t *ListDirTool) RequiresApproval(string) bool { return false }
func (t *ListDirTool) ParallelSafe(string) bool     { return true }

func (t *ListDirTool) PreviewCall(argsJSON string) string {
	var a struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if a.Path == "" {
		a.Path = "."
	}
	return fmt.Sprintf("list_dir(%s)", a.Path)
}

func (t *ListDirTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Path       string `json:"path"`
		MaxEntries int    `json:"max_entries"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("list_dir: invalid args: %w", err)
	}
	if a.MaxEntries <= 0 {
		a.MaxEntries = defaultListMax
	}
	p := a.Path
	if p == "" {
		p = t.Cwd.Get()
	} else {
		p = resolvePath(t.Cwd.Get(), p)
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return "", fmt.Errorf("list_dir: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var b strings.Builder
	shown := 0
	for _, e := range entries {
		if shown >= a.MaxEntries {
			fmt.Fprintf(&b, "…[truncated, %d total]\n", len(entries))
			break
		}
		marker := "f"
		if e.IsDir() {
			marker = "d"
		} else if e.Type()&os.ModeSymlink != 0 {
			marker = "l"
		}
		fmt.Fprintf(&b, "%s\t%s\n", marker, e.Name())
		shown++
	}
	return b.String(), nil
}

// GlobTool finds files matching a doublestar pattern (e.g., "**/*.go").
type GlobTool struct {
	Cwd *CwdRef
}

func (t *GlobTool) Name() string { return "glob" }

func (t *GlobTool) Description() string {
	return "Find files matching a glob pattern. Supports ** for recursive descent (e.g. \"**/*.go\"). Returns one path per line."
}

func (t *GlobTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern":     map[string]any{"type": "string", "description": "Glob pattern (** allowed)"},
			"root":        map[string]any{"type": "string", "description": "Directory to search from (default: cwd)"},
			"max_results": map[string]any{"type": "integer", "description": fmt.Sprintf("Cap on number of matches (default %d)", defaultGlobMax)},
		},
		"required": []string{"pattern"},
	}
}

func (t *GlobTool) RequiresApproval(string) bool { return false }
func (t *GlobTool) ParallelSafe(string) bool     { return true }

func (t *GlobTool) PreviewCall(argsJSON string) string {
	var a struct {
		Pattern string `json:"pattern"`
		Root    string `json:"root"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	root := a.Root
	if root == "" {
		root = "."
	}
	return fmt.Sprintf("glob(%s in %s)", a.Pattern, root)
}

func (t *GlobTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Pattern    string `json:"pattern"`
		Root       string `json:"root"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("glob: invalid args: %w", err)
	}
	if a.Pattern == "" {
		return "", fmt.Errorf("glob: pattern is required")
	}
	if a.MaxResults <= 0 {
		a.MaxResults = defaultGlobMax
	}
	root := a.Root
	if root == "" {
		root = t.Cwd.Get()
	} else {
		root = resolvePath(t.Cwd.Get(), root)
	}
	fsys := os.DirFS(root)
	matches, err := doublestar.Glob(fsys, a.Pattern)
	if err != nil {
		return "", fmt.Errorf("glob: %w", err)
	}
	if len(matches) == 0 {
		return "(no matches)\n", nil
	}
	sort.Strings(matches)
	var b strings.Builder
	for i, m := range matches {
		if i >= a.MaxResults {
			fmt.Fprintf(&b, "…[truncated, %d total]\n", len(matches))
			break
		}
		// Return paths relative to root for portability.
		fmt.Fprintln(&b, m)
	}
	return b.String(), nil
}

// GrepTool searches files for a pattern. Uses ripgrep if available, otherwise
// falls back to GNU grep. Args are passed via argv (no /bin/sh) so the model
// can't inject shell metacharacters. When the user supplies an explicit
// path, it's validated against DenyReadPaths so a targeted grep can't
// extract secrets line-by-line.
type GrepTool struct {
	Cwd           *CwdRef
	DenyReadPaths []string
}

func (t *GrepTool) Name() string { return "grep" }

func (t *GrepTool) Description() string {
	return "Search for a pattern in files. Returns 'path:line:match' rows. " +
		"Uses ripgrep when available, otherwise GNU grep. " +
		"Set regex=true to interpret the pattern as a regular expression (default: literal). " +
		"Set ignore_case=true for case-insensitive matching."
}

func (t *GrepTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern":     map[string]any{"type": "string"},
			"path":        map[string]any{"type": "string", "description": "File or directory to search (default: cwd)"},
			"regex":       map[string]any{"type": "boolean", "description": "Treat pattern as regex (default: false)"},
			"ignore_case": map[string]any{"type": "boolean", "description": "Case-insensitive (default: false)"},
			"max_results": map[string]any{"type": "integer", "description": fmt.Sprintf("Cap on matching lines (default %d)", defaultGrepMax)},
		},
		"required": []string{"pattern"},
	}
}

func (t *GrepTool) RequiresApproval(string) bool { return false }
func (t *GrepTool) ParallelSafe(string) bool     { return true }

type grepArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Regex      bool   `json:"regex"`
	IgnoreCase bool   `json:"ignore_case"`
	MaxResults int    `json:"max_results"`
}

func (t *GrepTool) PreviewCall(argsJSON string) string {
	var a grepArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	root := a.Path
	if root == "" {
		root = "."
	}
	return fmt.Sprintf("grep(%q in %s)", a.Pattern, root)
}

func (t *GrepTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a grepArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("grep: invalid args: %w", err)
	}
	if a.Pattern == "" {
		return "", fmt.Errorf("grep: pattern is required")
	}
	if a.MaxResults <= 0 {
		a.MaxResults = defaultGrepMax
	}
	root := a.Path
	if root == "" {
		root = t.Cwd.Get()
	} else {
		root = resolvePath(t.Cwd.Get(), root)
	}
	if err := ValidateReadPath(root, t.DenyReadPaths); err != nil {
		return "", fmt.Errorf("grep: %w", err)
	}

	cmd, err := buildGrepCmd(ctx, a, root)
	if err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &capped{buf: &stdout, max: maxGrepOutBytes}
	cmd.Stderr = &capped{buf: &stderr, max: 8 * 1024}
	if err := cmd.Run(); err != nil {
		// Both rg and grep return non-zero when there are no matches; that's
		// not an error. Anything else (e.g., bad regex) we report.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "(no matches)\n", nil
		}
		return "", fmt.Errorf("grep: %w; stderr=%q", err, stderr.String())
	}
	out := stdout.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) > a.MaxResults {
		lines = append(lines[:a.MaxResults], fmt.Sprintf("…[truncated at %d matches]", a.MaxResults))
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func buildGrepCmd(ctx context.Context, a grepArgs, root string) (*exec.Cmd, error) {
	if _, err := exec.LookPath("rg"); err == nil {
		args := []string{"-n", "--no-heading", "--color=never"}
		if !a.Regex {
			args = append(args, "-F")
		}
		if a.IgnoreCase {
			args = append(args, "-i")
		}
		args = append(args, "--", a.Pattern, root)
		return exec.CommandContext(ctx, "rg", args...), nil
	}
	if _, err := exec.LookPath("grep"); err != nil {
		return nil, fmt.Errorf("grep: neither rg nor grep is installed")
	}
	args := []string{"-rnI"} // -I skips binary files
	switch {
	case a.Regex:
		args = append(args, "-E") // ERE so + ? | () behave as expected
	default:
		args = append(args, "-F") // fixed string
	}
	if a.IgnoreCase {
		args = append(args, "-i")
	}
	args = append(args, "--", a.Pattern, root)
	return exec.CommandContext(ctx, "grep", args...), nil
}

// capped accumulates bytes up to a hard ceiling, then drops the rest.
type capped struct {
	buf *bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	rem := c.max - c.buf.Len()
	if rem <= 0 {
		return len(p), nil
	}
	if len(p) > rem {
		c.buf.Write(p[:rem])
		return len(p), nil
	}
	return c.buf.Write(p)
}

// ListProjectStructureTool returns a bounded tree view of files and
// directories with sizes and last-modified timestamps. Designed as the
// "survey first" tool: the model can scan structure once and choose
// what to read with read_many_files instead of reading exploratorily.
type ListProjectStructureTool struct {
	Cwd *CwdRef
}

func (t *ListProjectStructureTool) Name() string { return "list_project_structure" }

func (t *ListProjectStructureTool) Description() string {
	return "Survey a directory tree. Returns one entry per line: '<type>  <size>  <mtime>  <relpath>' where type is 'd' (dir) or 'f' (file). Skips .git/, node_modules/, vendor/, .yottacode/, and dotfiles unless include_hidden=true. Prefer this over many list_dir calls when surveying a project; pair with read_many_files to fetch the files you need in one call."
}

func (t *ListProjectStructureTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":           map[string]any{"type": "string", "description": "Root directory (default: cwd)"},
			"max_depth":      map[string]any{"type": "integer", "description": fmt.Sprintf("Recursion depth from the root (default %d, 1 = root only)", defaultStructureDepth)},
			"max_entries":    map[string]any{"type": "integer", "description": fmt.Sprintf("Cap on total entries returned (default %d)", defaultStructureMax)},
			"include_hidden": map[string]any{"type": "boolean", "description": "Include dotfiles and dot-directories (default: false)"},
		},
	}
}

func (t *ListProjectStructureTool) RequiresApproval(string) bool { return false }
func (t *ListProjectStructureTool) ParallelSafe(string) bool     { return true }

func (t *ListProjectStructureTool) PreviewCall(argsJSON string) string {
	var a struct {
		Path     string `json:"path"`
		MaxDepth int    `json:"max_depth"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	root := a.Path
	if root == "" {
		root = "."
	}
	depth := a.MaxDepth
	if depth <= 0 {
		depth = defaultStructureDepth
	}
	return fmt.Sprintf("list_project_structure(%s, depth=%d)", root, depth)
}

// skippedDirs are heavy / uninteresting trees that almost always blow
// out a structure listing without telling the model anything new.
// Hidden dirs are dropped via the dot-prefix check unless
// include_hidden is set.
var skippedDirs = map[string]struct{}{
	".git":         {},
	".hg":          {},
	".svn":         {},
	"node_modules": {},
	"vendor":       {},
	"target":       {},        // rust
	"build":        {},
	"dist":         {},
	".yottacode":   {},
}

func (t *ListProjectStructureTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Path          string `json:"path"`
		MaxDepth      int    `json:"max_depth"`
		MaxEntries    int    `json:"max_entries"`
		IncludeHidden bool   `json:"include_hidden"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("list_project_structure: invalid args: %w", err)
	}
	if a.MaxDepth <= 0 {
		a.MaxDepth = defaultStructureDepth
	}
	if a.MaxEntries <= 0 {
		a.MaxEntries = defaultStructureMax
	}
	root := a.Path
	if root == "" {
		root = t.Cwd.Get()
	} else {
		root = resolvePath(t.Cwd.Get(), root)
	}

	rootInfo, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("list_project_structure: %w", err)
	}
	if !rootInfo.IsDir() {
		return "", fmt.Errorf("list_project_structure: %s is not a directory", root)
	}

	type entry struct {
		isDir bool
		size  int64
		mtime time.Time
		rel   string
	}
	var entries []entry
	truncated := false

	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Permission errors and similar — skip the entry, keep walking.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if p == root {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return nil
		}
		// Depth = number of separators in the relative path + 1.
		depth := strings.Count(rel, string(filepath.Separator)) + 1
		name := d.Name()
		if !a.IncludeHidden && strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if _, skip := skippedDirs[name]; skip {
				return fs.SkipDir
			}
		}
		if depth > a.MaxDepth {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		if len(entries) >= a.MaxEntries {
			truncated = true
			return filepath.SkipAll
		}
		entries = append(entries, entry{
			isDir: d.IsDir(),
			size:  info.Size(),
			mtime: info.ModTime(),
			rel:   filepath.ToSlash(rel),
		})
		return nil
	})
	if err != nil && !errors.Is(err, filepath.SkipAll) {
		return "", fmt.Errorf("list_project_structure: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })

	var b strings.Builder
	for _, e := range entries {
		marker := "f"
		if e.isDir {
			marker = "d"
		}
		fmt.Fprintf(&b, "%s\t%d\t%s\t%s\n", marker, e.size, e.mtime.UTC().Format(time.RFC3339), e.rel)
	}
	if truncated {
		fmt.Fprintf(&b, "…[truncated at %d entries — narrow with path or lower max_depth]\n", a.MaxEntries)
	}
	if b.Len() == 0 {
		return "(empty)\n", nil
	}
	return b.String(), nil
}
