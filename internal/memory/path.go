package memory

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Memory layout:
//
//	~/.yottacode/USER.md                                  — global preferences (curated, human-only)
//	<cwd>/.yottacode/YOTTACODE.md                         — per-repo context (curated, agent-writable)
//	~/.yottacode/memory/<name>.md                         — user-scope agent-managed memories
//	~/.yottacode/projects/<slug>/memory/<name>.md         — project-scope agent-managed memories

// subdirProjects is the top-level dir under ~/.yottacode/ that holds
// per-project agent-managed memory. Each project gets its own subdir
// keyed by ProjectSlug(cwd).
const subdirProjects = "projects"

// subdirMemory is the dir name used inside both ~/.yottacode/memory/
// (user scope) and ~/.yottacode/projects/<slug>/memory/ (project
// scope). Naming is symmetric on purpose so the two layouts read the
// same way.
const subdirMemory = "memory"

// topicNamePattern allows lowercase letters, digits, and hyphens, starting
// with a letter or digit. Conservative on purpose — topic slugs become
// filenames, and a strict pattern eliminates a whole class of injection
// concerns up front.
var topicNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// reservedTopicNames are filenames that conflict with existing memory
// surfaces (the curated context files and infrastructure paths).
// Reserving them prevents memory_save from accidentally clobbering
// them.
var reservedTopicNames = map[string]bool{
	"user":      true,
	"project":   true,
	"memory":    true,
	"index":     true,
	"sessions":  true,
	"yottacode": true,
	"feedback":  true,
	"reference": true,
}

// projectSlugPattern keeps a slug to lowercase letters / digits /
// hyphens, with at most one leading character of either letter or
// digit. Mirrors topicNamePattern so the per-project dir name has
// the same safety guarantees as a topic file name.
var projectSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,127}$`)

// nonSlugChars is the inverse character class used by slugify: any rune
// not allowed in a slug is replaced with a hyphen. Compiled once at
// package init so slugify can stay pure-call.
var nonSlugChars = regexp.MustCompile(`[^a-z0-9-]+`)

// repeatedHyphens collapses runs of hyphens (introduced by slugify)
// down to a single hyphen, so "my  project" → "my-project" rather
// than "my--project".
var repeatedHyphens = regexp.MustCompile(`-{2,}`)

// ProjectSlug returns a stable, slug-safe identifier for the project
// rooted at cwd. Strategy (highest-priority first):
//
//  1. Git remote URL of `origin`. Survives renames of cwd, and is the
//     same string across machines and clones — project memory follows
//     the project, not the directory it happens to live in. Parsed
//     from formats like `https://github.com/user/repo.git`,
//     `git@github.com:user/repo.git`, `ssh://git@host/path/repo`.
//  2. `filepath.Base(cwd)` slugified. Used when no git remote is
//     configured (uncommitted scratch dir, vendored copy without
//     git, brand-new project pre-init).
//
// Returns "default" when both lookups fail (empty cwd, root-only
// path, exotic edge cases). The returned string is guaranteed to
// match projectSlugPattern, so callers can use it directly as a
// path component without further sanitization.
//
// Two unrelated repos with the same basename and no git remote will
// collide. The collision is documented; users who care can either
// initialize a git repo (which gets them a unique remote-derived
// slug) or live with the merge.
func ProjectSlug(cwd string) string {
	if slug := projectSlugFromGit(cwd); slug != "" {
		return slug
	}
	if cwd != "" {
		base := filepath.Base(cwd)
		if base != "" && base != "." && base != "/" {
			if s := slugify(base); s != "" {
				return s
			}
		}
	}
	return "default"
}

// gitSlugCache memoizes projectSlugFromGit per cwd. The TUI re-resolves
// the project slug on every turn (memory reload + memory_search both
// call Load → ProjectMemoryDir → ProjectSlug), and each miss otherwise
// forks a `git` subprocess. A project's `origin` remote is effectively
// immutable for the life of a session, so caching the result — including
// the negative "" result for non-repos — keeps the per-turn path off the
// process fork. Negative results are cached deliberately: a `git init`
// mid-session won't shift the memory location until restart, which is the
// desired stability (project memory shouldn't migrate underfoot).
var (
	gitSlugCacheMu sync.RWMutex
	gitSlugCache   = map[string]string{}
)

// projectSlugFromGit returns the cached git-derived slug for cwd,
// computing it on first use. See gitSlugCache for the caching rationale.
func projectSlugFromGit(cwd string) string {
	if cwd == "" {
		return ""
	}
	gitSlugCacheMu.RLock()
	slug, ok := gitSlugCache[cwd]
	gitSlugCacheMu.RUnlock()
	if ok {
		return slug
	}
	slug = computeProjectSlugFromGit(cwd)
	gitSlugCacheMu.Lock()
	gitSlugCache[cwd] = slug
	gitSlugCacheMu.Unlock()
	return slug
}

// computeProjectSlugFromGit shells out to `git -C <cwd> remote get-url
// origin` and converts the URL to a slug. Returns "" on any failure (not
// a git repo, no `origin` remote, git not on PATH).
func computeProjectSlugFromGit(cwd string) string {
	if _, err := exec.LookPath("git"); err != nil {
		return ""
	}
	out, err := exec.Command("git", "-C", cwd, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return slugifyGitURL(strings.TrimSpace(string(out)))
}

// slugifyGitURL turns a git remote URL into a stable per-project
// slug. Examples:
//
//	https://github.com/user/repo.git    → github-com-user-repo
//	git@github.com:user/repo.git        → github-com-user-repo
//	ssh://git@gitlab.com/user/repo.git  → gitlab-com-user-repo
//	file:///srv/git/repo.git            → srv-git-repo
//
// The .git suffix is dropped (almost everyone has it; treating it as
// noise keeps slugs stable across `repo` ↔ `repo.git` clone
// conventions). Empty input returns "" so the caller can fall back.
func slugifyGitURL(url string) string {
	if url == "" {
		return ""
	}
	// Strip protocol prefix.
	for _, prefix := range []string{"git+ssh://", "ssh://", "git://", "https://", "http://", "file://"} {
		if strings.HasPrefix(url, prefix) {
			url = url[len(strings.TrimSuffix(prefix, "://"))+3:]
			break
		}
	}
	// SCP-style: git@host:path → host/path. Rewrite to a / separator
	// so the rest of the function treats the URL uniformly.
	if at := strings.Index(url, "@"); at >= 0 && at < len(url)-1 {
		// Find the first ':' after the '@' that splits host from path.
		rest := url[at+1:]
		if colon := strings.Index(rest, ":"); colon >= 0 {
			url = rest[:colon] + "/" + rest[colon+1:]
		} else {
			url = rest
		}
	}
	// Drop the .git suffix if present.
	url = strings.TrimSuffix(url, ".git")
	// Drop any trailing slash and the user-credential prefix some
	// embeds carry (e.g. https://token@host/...).
	url = strings.TrimRight(url, "/")
	if at := strings.Index(url, "@"); at >= 0 {
		url = url[at+1:]
	}
	return slugify(url)
}

// slugify lowercases, replaces every non-[a-z0-9-] run with "-",
// trims edge hyphens, and clamps length. Designed to be idempotent:
// slugify(slugify(x)) == slugify(x) for any input.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonSlugChars.ReplaceAllString(s, "-")
	s = repeatedHyphens.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 128 {
		s = s[:128]
	}
	if !projectSlugPattern.MatchString(s) {
		return ""
	}
	return s
}

// UserMemoryDir returns ~/.yottacode/memory/ — the cross-project
// agent-managed memory directory. Memories saved here apply to every
// session for this user.
func UserMemoryDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("memory: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".yottacode", subdirMemory), nil
}

// ProjectMemoryDir returns ~/.yottacode/projects/<slug>/memory/ — the
// per-project, per-user agent-managed memory directory.
func ProjectMemoryDir(cwd string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("memory: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".yottacode", subdirProjects, ProjectSlug(cwd), subdirMemory), nil
}

// EnsureUserMemoryDir creates the user-scope memory directory if missing.
func EnsureUserMemoryDir() (string, error) {
	dir, err := UserMemoryDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("memory: create %q: %w", dir, err)
	}
	return dir, nil
}

// EnsureProjectMemoryDir creates the project-scope memory directory.
func EnsureProjectMemoryDir(cwd string) (string, error) {
	dir, err := ProjectMemoryDir(cwd)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("memory: create %q: %w", dir, err)
	}
	return dir, nil
}

// MemoryFilePath validates a memory name and returns the absolute file
// path under the chosen scope. Scope must be "user" or "project".
func MemoryFilePath(scope, name, cwd string) (string, error) {
	if !topicNamePattern.MatchString(name) {
		return "", fmt.Errorf("memory: invalid name %q (allowed: lowercase letters, digits, hyphens; max 64 chars)", name)
	}
	if reservedTopicNames[name] {
		return "", fmt.Errorf("memory: name %q is reserved", name)
	}
	var dir string
	var err error
	switch scope {
	case "user":
		dir, err = UserMemoryDir()
	case "project":
		dir, err = ProjectMemoryDir(cwd)
	default:
		return "", fmt.Errorf("memory: invalid scope %q (want \"user\" or \"project\")", scope)
	}
	if err != nil {
		return "", err
	}
	target := filepath.Join(dir, name+".md")
	clean := filepath.Clean(target)
	rel, err := filepath.Rel(dir, clean)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("memory: path %q escapes memory dir root", target)
	}
	if info, err := os.Lstat(clean); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("memory: refusing to follow symlink at %q", clean)
		}
	}
	return clean, nil
}

