// Package worktree owns the git-worktree machinery yottacode layers on
// top of plain `git worktree`: name generation, path resolution under
// <repo>/.yottacode/worktrees/, .worktreeinclude file copying, and
// clean/dirty detection for end-of-session cleanup.
//
// The package is intentionally free of agent / tool / TUI imports so
// the same primitives drive the --worktree CLI flag, the enter/exit
// agent tools, and the worktree admin subcommand.
package worktree

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// HomeSubdir is the user-home subdirectory under which yottacode
// materializes worktrees: ~/.yottacode/worktrees/<repo-slug>/<name>/.
// Living outside the originating repo keeps the working tree clean
// (no nested worktree dirs the IDE / find / grep would walk into),
// keeps the repo's gitignore unchanged, and matches the existing
// ~/.yottacode/sessions/ convention.
//
// Two repos with the same basename get distinct slugs (basename + an
// 8-char hash of the absolute repo path), so cross-repo name reuse
// (e.g. `feature-x` in two projects) does not collide.
const HomeSubdir = "worktrees"

// BranchPrefix is prepended to every yottacode-managed branch so the
// list in `git branch -a` is easy to filter.
const BranchPrefix = "worktree-"

// home returns ~/.yottacode/worktrees. Returns "" on os.UserHomeDir
// failure; callers downstream surface that as a path error from the
// resulting empty Dir.
func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(h, ".yottacode", HomeSubdir)
}

// RepoSlug derives a stable, human-readable directory name from an
// absolute repo root: `<basename>-<short-hash>`. The hash (sha256 of
// the absolute path, first 8 hex chars) prevents collisions when two
// repos share a basename. Stable: the same repo always produces the
// same slug across invocations.
func RepoSlug(absRepoRoot string) string {
	base := filepath.Base(filepath.Clean(absRepoRoot))
	if base == "" || base == "." || base == "/" {
		base = "repo"
	}
	sum := sha256.Sum256([]byte(filepath.Clean(absRepoRoot)))
	return base + "-" + hex.EncodeToString(sum[:])[:8]
}

// RepoRoot returns the absolute path of the git repository that
// contains cwd. Works from anywhere inside the work tree, including
// inside an existing yottacode worktree (git rev-parse --show-toplevel
// returns the worktree dir; --git-common-dir resolves the original).
//
// Use ResolveRepoRoot if you need the main repository root even when
// called from inside a worktree.
func RepoRoot(ctx context.Context, cwd string) (string, error) {
	out, err := gitOut(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("worktree: not a git repo (or git missing): %w", err)
	}
	return strings.TrimSpace(out), nil
}

// ResolveRepoRoot returns the main repository root regardless of
// whether cwd is inside a worktree. Used by the CLI flag handler so
// `yottacode --worktree foo` from inside an existing worktree still
// creates the new sibling under the original repo's
// .yottacode/worktrees/, not nested.
func ResolveRepoRoot(ctx context.Context, cwd string) (string, error) {
	commonDir, err := gitOut(ctx, cwd, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("worktree: %w", err)
	}
	commonDir = strings.TrimSpace(commonDir)
	// git emits either an absolute path (newer git, called from a
	// linked worktree) or a path relative to cwd (in-repo). Only
	// prefix cwd in the relative case — filepath.Join doesn't strip
	// the absolute-leading slash, so doing so for absolute input
	// produces "/cwd/abs/path".
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(cwd, commonDir)
	}
	abs, err := filepath.Abs(commonDir)
	if err != nil {
		return "", err
	}
	// git-common-dir resolves to the main repo's .git/ directory; the
	// repo root is its parent.
	return filepath.Dir(abs), nil
}

// SlugDir returns the per-repo container under user home:
// ~/.yottacode/worktrees/<slug>/. All worktrees for one repo live
// under this dir.
func SlugDir(repoRoot string) string {
	return filepath.Join(home(), RepoSlug(filepath.Clean(repoRoot)))
}

// OriginFile is the dotfile name stored in each slug dir whose
// contents is the absolute path of the originating repo. Lets the
// `yottacode worktree where` admin command answer "which repo does
// this slug belong to?" without needing a live worktree to query
// via git. Dot-prefixed so listings of the slug dir contents (e.g.
// for `worktree list`) can trivially skip it.
const OriginFile = ".origin"

// OriginPath returns the absolute path of the .origin file for a
// given slug dir. Exposed so callers can audit / unlink it without
// open-coding the filename constant.
func OriginPath(slugDir string) string {
	return filepath.Join(slugDir, OriginFile)
}

// WriteOrigin records the absolute repo path in `<slugDir>/.origin`,
// creating the slug dir if it doesn't exist. Idempotent: writing the
// same content over an existing file is a no-op from the user's
// perspective. Returns nil on a soft-fail when the slug dir simply
// cannot be created (e.g. permission denied) — `enter_worktree`
// soft-fails on this rather than rolling back the just-created
// worktree, since `.origin` is purely a discovery aid.
func WriteOrigin(slugDir, repoRoot string) error {
	if err := os.MkdirAll(slugDir, 0o755); err != nil {
		return err
	}
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		return err
	}
	// Trailing newline so the file reads cleanly under `cat` / shell
	// tools without "no newline at end of file" warnings.
	return os.WriteFile(OriginPath(slugDir), []byte(filepath.Clean(abs)+"\n"), 0o644)
}

// ReadOrigin returns the repo path recorded in `<slugDir>/.origin`,
// or an error if the file is missing or unreadable. The returned
// path is trimmed of trailing whitespace (so the writer's terminating
// newline doesn't leak into the value).
func ReadOrigin(slugDir string) (string, error) {
	b, err := os.ReadFile(OriginPath(slugDir))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// Dir returns ~/.yottacode/worktrees/<slug>/<name>, the materialized
// worktree directory for the given (repoRoot, name) pair.
func Dir(repoRoot, name string) string {
	return filepath.Join(SlugDir(repoRoot), name)
}

// Branch returns the canonical branch name for a worktree.
func Branch(name string) string {
	return BranchPrefix + name
}

// IsWorktreePath reports whether path lives inside a worktree that
// belongs to the given repoRoot — i.e. under
// ~/.yottacode/worktrees/<slug(repoRoot)>/<name>/... — and returns
// the <name> segment. Cross-repo worktree paths return false: a
// session in repo A should not infer worktree names from repo B's
// worktree subtree.
func IsWorktreePath(repoRoot, path string) (name string, ok bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	root := filepath.Clean(SlugDir(repoRoot))
	rel, err := filepath.Rel(root, filepath.Clean(abs))
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return "", false
	}
	// First segment is the worktree name. Everything deeper is
	// inside the worktree.
	parts := strings.SplitN(rel, string(filepath.Separator), 2)
	if parts[0] == "" {
		return "", false
	}
	return parts[0], true
}

// NormalizeForRule rewrites the worktree-name segment of a path
// inside a yottacode worktree to `*`, producing a worktree-agnostic
// glob pattern. Used by the permission rule deriver so an
// `[A]-always` click inside an auto-generated worktree
// (`noble-hopping-salmon`) doesn't pollute permissions.json with a
// rule that only matches that one ephemeral tree. Result is still
// per-repo-scoped via the slug.
//
// Examples (with HOME=/home/me):
//
//	"/home/me/.yottacode/worktrees/proj-1a2b3c4d/noble-hopping-salmon/foo.go"
//	  → "/home/me/.yottacode/worktrees/proj-1a2b3c4d/*/foo.go"
//
//	"/home/me/.yottacode/worktrees/proj-1a2b3c4d/feature-x"
//	  → "/home/me/.yottacode/worktrees/proj-1a2b3c4d/*"
//
// Paths that aren't inside a yottacode worktree pass through unchanged.
func NormalizeForRule(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	slug, _, ok := IsAnyWorktreePath(abs)
	if !ok {
		return p
	}
	base := filepath.Join(home(), slug)
	rel, err := filepath.Rel(base, filepath.Clean(abs))
	if err != nil {
		return p
	}
	parts := strings.SplitN(rel, string(filepath.Separator), 2)
	if len(parts) < 1 || parts[0] == "" {
		return p
	}
	if len(parts) == 1 {
		return filepath.Join(base, "*")
	}
	return filepath.Join(base, "*", parts[1])
}

// IsAnyWorktreePath is the repo-agnostic variant of IsWorktreePath:
// reports whether path is under any yottacode worktree dir at all
// and, if so, returns both the <slug> and <name>. Used by callers
// (TUI status-line chip, permission-rule rewriter) that need to
// recognize "this is inside SOME worktree" without first resolving
// the originating repo.
func IsAnyWorktreePath(path string) (slug, name string, ok bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", false
	}
	root := filepath.Clean(home())
	rel, err := filepath.Rel(root, filepath.Clean(abs))
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return "", "", false
	}
	parts := strings.SplitN(rel, string(filepath.Separator), 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// Info is a single entry produced by `git worktree list --porcelain`.
// Only the fields we actually use are surfaced; the porcelain format
// also emits HEAD sha and detached state, which we don't need yet.
type Info struct {
	Path   string
	Branch string // empty if detached
	Locked bool
}

// List parses `git worktree list --porcelain` into a slice of Info.
// The first entry is always the main worktree.
func List(ctx context.Context, repoRoot string) ([]Info, error) {
	out, err := gitOut(ctx, repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("worktree: list: %w", err)
	}
	var (
		result []Info
		cur    Info
		have   bool
	)
	flush := func() {
		if have {
			result = append(result, cur)
		}
		cur = Info{}
		have = false
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur.Path = strings.TrimPrefix(line, "worktree ")
			have = true
		case strings.HasPrefix(line, "branch "):
			// Format: "branch refs/heads/<name>"
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "locked" || strings.HasPrefix(line, "locked "):
			cur.Locked = true
		case line == "":
			flush()
		}
	}
	flush()
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// Generate returns a worktree name like "bright-running-fox" that
// does not collide with any existing entry under
// <repoRoot>/.yottacode/worktrees/. Falls back to a hex suffix if
// the adjective+noun namespace is exhausted (it won't be in
// practice, but the loop is bounded).
func Generate(ctx context.Context, repoRoot string) (string, error) {
	existing, err := List(ctx, repoRoot)
	if err != nil {
		return "", err
	}
	used := make(map[string]bool, len(existing))
	for _, w := range existing {
		used[filepath.Base(w.Path)] = true
	}
	for i := 0; i < 32; i++ {
		name, err := randomName()
		if err != nil {
			return "", err
		}
		if !used[name] {
			return name, nil
		}
	}
	// Fallback: pure hex suffix.
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "worktree-" + hex.EncodeToString(b[:]), nil
}

// ValidateName rejects names that would escape the worktrees dir or
// produce confusing paths. Allowed chars: ASCII letters, digits,
// hyphen, underscore. Length 1..64. No leading dot or dash.
func ValidateName(name string) error {
	if name == "" {
		return errors.New("worktree: name is required")
	}
	if len(name) > 64 {
		return errors.New("worktree: name exceeds 64 chars")
	}
	if name[0] == '.' || name[0] == '-' {
		return errors.New("worktree: name must not start with '.' or '-'")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return fmt.Errorf("worktree: name contains invalid char %q", r)
		}
	}
	return nil
}

func gitOut(ctx context.Context, cwd string, args ...string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", errors.New("git binary not found in PATH")
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

var (
	adjectives = []string{
		"bright", "calm", "clever", "cozy", "crisp", "eager", "fancy", "feisty",
		"gentle", "grand", "happy", "humble", "jolly", "keen", "lively", "lucky",
		"merry", "mighty", "nimble", "noble", "patient", "peppy", "polite", "proud",
		"quick", "quiet", "shiny", "silver", "snappy", "stoic", "sunny", "swift",
		"tidy", "vivid", "warm", "witty", "zesty",
	}
	nouns = []string{
		"badger", "beetle", "cougar", "dolphin", "eagle", "falcon", "fox", "gecko",
		"heron", "ibex", "jaguar", "koala", "lynx", "marten", "newt", "otter",
		"panda", "quail", "raven", "salmon", "tiger", "urial", "viper", "walrus",
		"yak", "zebra", "puffin", "stoat",
	}
	verbs = []string{
		"running", "soaring", "leaping", "diving", "climbing", "trotting",
		"darting", "gliding", "hopping", "prowling",
	}
)

func randomName() (string, error) {
	pick := func(s []string) (string, error) {
		var b [1]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		return s[int(b[0])%len(s)], nil
	}
	adj, err := pick(adjectives)
	if err != nil {
		return "", err
	}
	v, err := pick(verbs)
	if err != nil {
		return "", err
	}
	n, err := pick(nouns)
	if err != nil {
		return "", err
	}
	return adj + "-" + v + "-" + n, nil
}
