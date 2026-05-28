package skills

// Phase 1 installer: bring a SKILL.md into ~/.yottacode/skills/<slug>/
// from one of three source shapes:
//
//   1. local path        ./remote-ops/ or /abs/path/SKILL.md
//   2. URL               https://.../SKILL.md (single-file fetch only)
//   3. GitHub shorthand  owner/repo[/path/to/skill] (Contents API)
//
// The installer stages every fetched file in a sibling tempdir, parses
// the SKILL.md to derive the canonical slug, then atomically renames
// the staged dir into <DestRoot>/<slug>. On any error the tempdir is
// torn down and the existing skill (if any) is untouched. The slug on
// disk is the frontmatter `name`, not the source-side directory name —
// install rewrites the wrapping so the spec's "name == parent dir"
// invariant always holds for the loaded skill regardless of how the
// source was laid out.
//
// Provenance/lockfile + update detection is Phase 2; this file
// deliberately stops at "bytes are on disk and parseable."

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// SourceType classifies an install source. Surfaced in InstallResult
// so the CLI/TUI can render "installed remote-ops from github" and so
// Phase 2's lockfile has the discriminator it needs.
type SourceType string

const (
	SourceLocal  SourceType = "local"
	SourceURL    SourceType = "url"
	SourceGitHub SourceType = "github"
)

// InstallOptions configures one install attempt.
type InstallOptions struct {
	// Source is the user-supplied locator. classifySource picks the
	// SourceType based on shape and (for ambiguous bare names) a stat.
	Source string

	// Force overwrites an existing skill dir of the same slug. Without
	// it, an existing install is a hard error — matches the notes'
	// "Refuse overwrite unless --force".
	Force bool

	// DestRoot is the parent dir installs go into. Defaults to
	// UserSkillsDir() when empty. Tests inject a tempdir.
	DestRoot string

	// HTTPClient overrides the default client. Tests inject an
	// httptest server's client; production callers pass nil.
	HTTPClient *http.Client
}

// InstallResult describes a completed install. Returned so the
// surface (CLI / TUI) can render "installed remote-ops (github) at
// /home/.../skills/remote-ops" without re-parsing the SKILL.md.
//
// Warnings carries non-fatal post-install issues (lockfile save
// failure is the main one in Phase 2) so the caller can surface them
// to the user without failing the whole install — the bytes are on
// disk and the skill works regardless.
type InstallResult struct {
	Skill      Skill
	Dir        string
	SourceType SourceType
	Lock       LockEntry
	Warnings   []string
}

// Install resolves Source, validates its SKILL.md via ParseSkillFile,
// stages a copy in a tempdir, then atomic-renames into
// <DestRoot>/<slug>. Refuses to overwrite an existing slug unless
// Force is set.
func Install(opts InstallOptions) (InstallResult, error) {
	if strings.TrimSpace(opts.Source) == "" {
		return InstallResult{}, errors.New("source is required")
	}
	destRoot := opts.DestRoot
	if destRoot == "" {
		d, err := UserSkillsDir()
		if err != nil {
			return InstallResult{}, err
		}
		destRoot = d
	}
	if err := os.MkdirAll(destRoot, 0o700); err != nil {
		return InstallResult{}, fmt.Errorf("create %s: %w", destRoot, err)
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	typ := classifySource(opts.Source)
	if typ == "" {
		return InstallResult{}, fmt.Errorf("unrecognized source %q (want a local path, https://... URL, or owner/repo[/path])", opts.Source)
	}

	tempDir, err := os.MkdirTemp(destRoot, ".staging-")
	if err != nil {
		return InstallResult{}, fmt.Errorf("create staging dir: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tempDir)
		}
	}()

	var sk Skill
	switch typ {
	case SourceLocal:
		sk, err = stageLocal(opts.Source, tempDir)
	case SourceURL:
		sk, err = stageURL(opts.Source, tempDir, client)
	case SourceGitHub:
		sk, err = stageGitHub(opts.Source, tempDir, client)
	}
	if err != nil {
		return InstallResult{}, err
	}

	destDir := filepath.Join(destRoot, sk.Name)
	if _, statErr := os.Stat(destDir); statErr == nil {
		if !opts.Force {
			return InstallResult{}, fmt.Errorf("skill %q is already installed at %s — pass --force to overwrite", sk.Name, destDir)
		}
		if err := os.RemoveAll(destDir); err != nil {
			return InstallResult{}, fmt.Errorf("remove existing %s: %w", destDir, err)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return InstallResult{}, statErr
	}

	if err := os.Rename(tempDir, destDir); err != nil {
		return InstallResult{}, fmt.Errorf("install %s: %w", destDir, err)
	}
	cleanup = false

	sk.Source = ScopeUser
	sk.Dir = destDir
	sk.SourcePath = filepath.Join(destDir, "SKILL.md")

	// Phase 2 provenance: record where this came from + the content
	// hash so `skills check` / `skills update` can detect drift and
	// re-fetch later. Failures are non-fatal — the install itself
	// succeeded; the user loses update-tracking but the skill works.
	entry := LockEntry{
		Name:        sk.Name,
		SourceType:  typ,
		Source:      opts.Source,
		InstalledAt: time.Now().UTC(),
		Trust:       TrustUnverified,
	}
	var warnings []string
	if hash, err := HashInstalledDir(destDir); err != nil {
		warnings = append(warnings, "hash installed dir: "+err.Error())
	} else {
		entry.Hash = hash
	}
	lf, err := LoadLockfile(destRoot)
	if err != nil {
		warnings = append(warnings, "load lockfile: "+err.Error())
	} else {
		lf.Set(entry)
		if err := lf.Save(destRoot); err != nil {
			warnings = append(warnings, "save lockfile: "+err.Error())
		}
	}
	return InstallResult{
		Skill:      sk,
		Dir:        destDir,
		SourceType: typ,
		Lock:       entry,
		Warnings:   warnings,
	}, nil
}

// Uninstall removes a user-scope skill by name. Returns the path
// removed (for logging). Intentionally scoped to UserSkillsDir() — a
// project-scope skill is committed source, the user removes that via
// git/rm directly; a built-in is embedded in the binary and can't be
// removed at runtime.
func Uninstall(name string) (string, error) {
	if name == "" {
		return "", errors.New("name is required")
	}
	if !skillNamePattern.MatchString(name) {
		return "", fmt.Errorf("invalid name %q", name)
	}
	dir, err := UserSkillsDir()
	if err != nil {
		return "", err
	}
	target := filepath.Join(dir, name)
	info, err := os.Stat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("no user skill named %q at %s", name, target)
		}
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", target)
	}
	if err := os.RemoveAll(target); err != nil {
		return "", err
	}
	// Drop the lockfile entry too. Best-effort: a missing lockfile or
	// save failure shouldn't undo the uninstall — the dir is already
	// gone. `skills check` will surface the orphan entry on the next
	// run if it sticks around.
	if lf, err := LoadLockfile(dir); err == nil {
		lf.Remove(name)
		_ = lf.Save(dir)
	}
	return target, nil
}

// classifySource picks the right source type. Order matters: an
// existing local path wins over the shape-based GitHub-shorthand
// check, so `./owner/repo/skill` resolves to local even though it
// parses as a three-segment slash form.
func classifySource(src string) SourceType {
	src = strings.TrimSpace(src)
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		return SourceURL
	}
	if strings.HasPrefix(src, "/") || strings.HasPrefix(src, "./") ||
		strings.HasPrefix(src, "../") || src == "." || src == ".." {
		return SourceLocal
	}
	if _, err := os.Stat(src); err == nil {
		return SourceLocal
	}
	if ghShorthandRE.MatchString(src) {
		return SourceGitHub
	}
	return ""
}

// ghShorthandRE matches `owner/repo` or `owner/repo/path/...`. The
// charset accepts GitHub-legal owner/repo characters (`-`, `_`, `.`)
// plus typical path characters for the trailing path segment.
var ghShorthandRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(/.+)?$`)

// stageLocal copies SKILL.md plus the optional resource dirs from a
// local path into tempDir. Accepts either a directory or a direct
// `.../SKILL.md` file pointer for ergonomic CLI use.
func stageLocal(src, tempDir string) (Skill, error) {
	abs, err := filepath.Abs(src)
	if err != nil {
		return Skill{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Skill{}, fmt.Errorf("read source: %w", err)
	}
	var srcDir, srcFile string
	if info.IsDir() {
		srcDir = abs
		srcFile = filepath.Join(abs, "SKILL.md")
	} else {
		if filepath.Base(abs) != "SKILL.md" {
			return Skill{}, fmt.Errorf("file source must be a SKILL.md, got %s", filepath.Base(abs))
		}
		srcDir = filepath.Dir(abs)
		srcFile = abs
	}
	data, err := os.ReadFile(srcFile)
	if err != nil {
		return Skill{}, fmt.Errorf("read SKILL.md: %w", err)
	}
	// Parse with expectedName="" — the source-side dir name doesn't
	// have to match the frontmatter; install rewrites the wrapping so
	// the on-disk slug is always sk.Name.
	sk, err := ParseSkillFile(data, "")
	if err != nil {
		return Skill{}, fmt.Errorf("parse SKILL.md: %w", err)
	}
	if err := writeFile(filepath.Join(tempDir, "SKILL.md"), data, 0o600); err != nil {
		return Skill{}, err
	}
	if err := copyResourceDirs(srcDir, tempDir); err != nil {
		return Skill{}, err
	}
	return sk, nil
}

// stageURL fetches a single SKILL.md from an HTTPS URL. Arbitrary URLs
// cannot enumerate sibling directories, so resource dirs are not
// attempted — body-only install. Use the GitHub shorthand or a local
// path when you need scripts/references/assets.
func stageURL(src, tempDir string, client *http.Client) (Skill, error) {
	u, err := url.Parse(src)
	if err != nil {
		return Skill{}, fmt.Errorf("parse url: %w", err)
	}
	if !strings.EqualFold(path.Base(u.Path), "SKILL.md") {
		return Skill{}, fmt.Errorf("URL must point at a SKILL.md (got %s); resources are not fetched from arbitrary URLs", u.Path)
	}
	data, err := httpGet(client, src, "application/octet-stream")
	if err != nil {
		return Skill{}, err
	}
	sk, err := ParseSkillFile(data, "")
	if err != nil {
		return Skill{}, fmt.Errorf("parse SKILL.md: %w", err)
	}
	if err := writeFile(filepath.Join(tempDir, "SKILL.md"), data, 0o600); err != nil {
		return Skill{}, err
	}
	return sk, nil
}

// stageGitHub walks owner/repo[/path] via the GitHub Contents API and
// downloads SKILL.md plus the spec's three optional resource dirs.
// Trailing /SKILL.md in the user-supplied path is stripped so both
// "owner/repo/skills/foo" and "owner/repo/skills/foo/SKILL.md" install
// the same thing.
func stageGitHub(src, tempDir string, client *http.Client) (Skill, error) {
	parts := strings.SplitN(src, "/", 3)
	if len(parts) < 2 {
		return Skill{}, fmt.Errorf("github shorthand needs owner/repo[/path]")
	}
	owner, repo := parts[0], parts[1]
	subpath := ""
	if len(parts) == 3 {
		subpath = strings.Trim(parts[2], "/")
		subpath = strings.TrimSuffix(subpath, "/SKILL.md")
		if subpath == "SKILL.md" {
			subpath = ""
		}
	}
	apiBase := githubAPIBase()
	listURL := apiBase + "/repos/" + owner + "/" + repo + "/contents"
	if subpath != "" {
		listURL += "/" + subpath
	}
	items, err := ghList(client, listURL)
	if err != nil {
		return Skill{}, err
	}
	var skillMd *ghContent
	var resourceDirs []ghContent
	for i := range items {
		switch {
		case items[i].Type == "file" && items[i].Name == "SKILL.md":
			skillMd = &items[i]
		case items[i].Type == "dir" && isResourceDir(items[i].Name):
			resourceDirs = append(resourceDirs, items[i])
		}
	}
	if skillMd == nil {
		loc := "/"
		if subpath != "" {
			loc = "/" + subpath
		}
		return Skill{}, fmt.Errorf("no SKILL.md found at github://%s/%s%s", owner, repo, loc)
	}
	if skillMd.DownloadURL == "" {
		return Skill{}, fmt.Errorf("github API returned no download_url for %s", skillMd.Path)
	}
	body, err := httpGet(client, skillMd.DownloadURL, "application/octet-stream")
	if err != nil {
		return Skill{}, err
	}
	sk, err := ParseSkillFile(body, "")
	if err != nil {
		return Skill{}, fmt.Errorf("parse SKILL.md: %w", err)
	}
	if err := writeFile(filepath.Join(tempDir, "SKILL.md"), body, 0o600); err != nil {
		return Skill{}, err
	}
	for _, rd := range resourceDirs {
		childURL := apiBase + "/repos/" + owner + "/" + repo + "/contents/" + rd.Path
		if err := ghDownloadTree(client, childURL, filepath.Join(tempDir, rd.Name)); err != nil {
			return Skill{}, err
		}
	}
	return sk, nil
}

// copyResourceDirs copies the spec's three optional subdirs from
// srcDir into destDir. Other top-level entries (README, LICENSE,
// .git, etc.) are intentionally ignored — the installer's contract
// is "produce a spec-shaped skill directory," not "mirror an
// arbitrary source tree."
func copyResourceDirs(srcDir, destDir string) error {
	for _, sub := range resourceDirs {
		srcSub := filepath.Join(srcDir, sub)
		info, err := os.Stat(srcSub)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if !info.IsDir() {
			continue
		}
		if err := copyTree(srcSub, filepath.Join(destDir, sub)); err != nil {
			return err
		}
	}
	return nil
}

// resourceDirs is the spec's enumeration of optional per-skill
// subdirectories. Kept as a package-level slice so the GitHub and
// local stagers reference the same set.
var resourceDirs = []string{"scripts", "references", "assets"}

func isResourceDir(name string) bool {
	return slices.Contains(resourceDirs, name)
}

func copyTree(srcDir, destDir string) error {
	return filepath.Walk(srcDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, p)
		if err != nil {
			return err
		}
		target := filepath.Join(destDir, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		mode := info.Mode().Perm()
		if mode == 0 {
			mode = 0o600
			if isUnderScripts(target) {
				mode = 0o755
			}
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return writeFile(target, data, mode)
	})
}

// isUnderScripts reports whether target lives under a `scripts/` path
// segment. Used when copying from sources that don't preserve the
// executable bit (e.g. files written through httpGet from the GitHub
// Contents API).
func isUnderScripts(target string) bool {
	for seg := range strings.SplitSeq(filepath.ToSlash(target), "/") {
		if seg == "scripts" {
			return true
		}
	}
	return false
}

// writeFile is os.WriteFile with the parent directory created on
// demand. Saves the stagers from having to MkdirAll every nested
// target.
func writeFile(p string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, data, mode)
}

// ghContent is the relevant slice of GitHub's contents-API JSON. Full
// shape is documented at
// https://docs.github.com/en/rest/repos/contents — we only need name,
// path, type, and download_url.
type ghContent struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Type        string `json:"type"`
	DownloadURL string `json:"download_url"`
}

// ghList GETs the contents-API URL and normalizes the response. The
// API returns an array for directories and a single object for files;
// callers iterate uniformly over the returned slice.
func ghList(client *http.Client, listURL string) ([]ghContent, error) {
	body, err := httpGet(client, listURL, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	if len(body) > 0 && body[0] == '[' {
		var arr []ghContent
		if err := json.Unmarshal(body, &arr); err != nil {
			return nil, fmt.Errorf("github contents API: parse %s: %w", listURL, err)
		}
		return arr, nil
	}
	var single ghContent
	if err := json.Unmarshal(body, &single); err != nil {
		return nil, fmt.Errorf("github contents API: parse %s: %w", listURL, err)
	}
	return []ghContent{single}, nil
}

// ghDownloadTree mirrors a Contents-API directory into destDir. The
// base of the API URL (everything up to and including `/contents`) is
// reused to build child-directory URLs from each item.Path, so the
// caller doesn't have to pass owner/repo through every recursion.
func ghDownloadTree(client *http.Client, listURL, destDir string) error {
	items, err := ghList(client, listURL)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return err
	}
	contentsIdx := strings.LastIndex(listURL, "/contents")
	if contentsIdx < 0 {
		return fmt.Errorf("malformed contents URL %s", listURL)
	}
	contentsBase := listURL[:contentsIdx+len("/contents")]
	for _, item := range items {
		target := filepath.Join(destDir, item.Name)
		switch item.Type {
		case "file":
			if item.DownloadURL == "" {
				return fmt.Errorf("no download_url for %s", item.Path)
			}
			data, err := httpGet(client, item.DownloadURL, "application/octet-stream")
			if err != nil {
				return err
			}
			mode := os.FileMode(0o600)
			if isUnderScripts(target) {
				mode = 0o755
			}
			if err := writeFile(target, data, mode); err != nil {
				return err
			}
		case "dir":
			childURL := contentsBase + "/" + item.Path
			if err := ghDownloadTree(client, childURL, target); err != nil {
				return err
			}
		}
	}
	return nil
}

// httpGet runs a GET with a sensible Accept + UA, an optional
// GITHUB_TOKEN for authenticated rate limits, and a 5MB response cap
// to avoid a malicious URL streaming us to OOM.
func httpGet(client *http.Client, target, accept string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "yottacode-skills")
	if tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); tok != "" &&
		strings.Contains(target, "api.github.com") {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", target, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		msg := strings.TrimSpace(string(body))
		if msg != "" {
			return nil, fmt.Errorf("fetch %s: %s — %s", target, resp.Status, msg)
		}
		return nil, fmt.Errorf("fetch %s: %s", target, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
}

// githubAPIBase resolves the GitHub API root. Defaults to the public
// host; tests inject a YOTTACODE_GITHUB_API_URL pointing at an
// httptest server so the GitHub path is exercisable without a live
// network call.
func githubAPIBase() string {
	if v := strings.TrimSpace(os.Getenv("YOTTACODE_GITHUB_API_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://api.github.com"
}
