// Path-validation helpers for the mutating filesystem tools (write_file,
// edit_file, mkdir, copy_file, move_file, delete_file). Three layers of
// defense:
//
//   1. Cwd confinement — paths must resolve under the session's cwd, or
//      under one of the user-provided AllowedPaths roots.
//   2. Symlink rejection — symlinked write targets are refused unless the
//      user explicitly opts in. Stops the "symlink to /etc/passwd" trick.
//   3. Deny list — yottacode's own state directories (sessions, auto/,
//      permissions.json, permissions.local.json) and git-internal paths
//      are off-limits regardless of approval. Self-grants and memory
//      injection don't go through the tool surface.
//
// Reads run through a narrower validator (ValidateReadPath) gated by a
// targeted deny list of credential-bearing paths
// (DefaultDenyReadPaths). The agent legitimately reads dotfiles,
// USER.md, /etc/os-release, etc., so the read deny list is targeted —
// well-known credential locations only — instead of a blanket
// cwd-confinement. Reading credentials directly into the model's
// context (and from there into the upstream provider's logs) is a
// silent exfiltration vector via prompt injection; run_bash is the
// escape hatch for the rare case the user really wants the contents,
// because run_bash always prompts.
package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WritePathOptions configures the validator for a single tool. Build it
// once per session in run.go and share across every mutating tool.
type WritePathOptions struct {
	// Cwd is the primary allowed root. Required.
	Cwd string

	// AllowedPaths is the list of additional roots a user has opted into
	// via --allow-paths or YOTTACODE_ALLOW_PATHS. Each entry is treated
	// as an absolute root the model is allowed to write under.
	AllowedPaths []string

	// DenyExact is a list of absolute paths (or path prefixes) the model
	// must never write to. Populated from DefaultDenyPaths(cwd) at
	// registration time. Always wins, even if a path otherwise matches
	// Cwd or AllowedPaths.
	DenyExact []string

	// AllowSymlinks lets the validator follow symlinks on write paths.
	// Default false — symlinks are a known exfil vector.
	AllowSymlinks bool
}

// ValidateWritePath returns nil if a write to path is permitted under
// the given options, or a descriptive error. Validation order matters:
// deny list checked first (so even a path inside cwd can be refused),
// then symlink check, then containment check against cwd / allowed
// roots.
func ValidateWritePath(path string, opts WritePathOptions) error {
	if path == "" {
		return errors.New("write path is empty")
	}
	if strings.ContainsRune(path, 0) {
		return errors.New("write path contains NUL byte")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve write path: %w", err)
	}
	abs = filepath.Clean(abs)

	if matchesAny(abs, opts.DenyExact) {
		return fmt.Errorf("path %q is in the deny list (yottacode-managed state, git internals)", abs)
	}

	if !opts.AllowSymlinks {
		if info, err := os.Lstat(abs); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %q is a symlink; refusing to follow (pass --allow-symlinks to override)", abs)
		}
	}

	if pathUnder(abs, opts.Cwd) {
		return nil
	}
	for _, root := range opts.AllowedPaths {
		if root == "" {
			continue
		}
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if pathUnder(abs, rootAbs) {
			return nil
		}
	}
	return fmt.Errorf("path %q is outside cwd and not in --allow-paths", abs)
}

// pathUnder reports whether descendant is at or below ancestor on the
// filesystem tree. Pure string check after normalization — no I/O.
func pathUnder(descendant, ancestor string) bool {
	if ancestor == "" {
		return false
	}
	a, err := filepath.Abs(ancestor)
	if err != nil {
		return false
	}
	a = filepath.Clean(a)
	d := filepath.Clean(descendant)
	if d == a {
		return true
	}
	rel, err := filepath.Rel(a, d)
	if err != nil {
		return false
	}
	if strings.HasPrefix(rel, "..") || rel == ".." {
		return false
	}
	return true
}

// matchesAny reports whether path equals or is descended from any entry
// in patterns. Used for the deny-list check.
func matchesAny(path string, patterns []string) bool {
	for _, p := range patterns {
		if p == "" {
			continue
		}
		pa, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		pa = filepath.Clean(pa)
		if path == pa {
			return true
		}
		// Treat the entry as a directory prefix.
		if pathUnder(path, pa) {
			return true
		}
	}
	return false
}

// DefaultDenyPaths returns the hardcoded list of paths the agent's
// mutating filesystem tools must refuse to write to. Includes:
//
//   - User-scope yottacode state under ~/.yottacode/ (sessions,
//     memory/, projects/, auth/, index.sqlite, USER.md). The agent
//     has supported pathways for the memory dirs (memory_save /
//     memory_forget); the generic write_file / edit_file surface
//     must not be a back door. USER.md is global preferences — the
//     agent's project-scope view doesn't have enough signal to
//     curate cross-project preferences.
//   - Project-scope yottacode state under <cwd>/.yottacode/
//     (permissions.json, permissions.local.json). The permissions
//     files are the user's policy surface — letting the model edit
//     them via tools would let it self-grant approval. The
//     /permissions slash command and the user's editor are the only
//     legitimate write paths.
//   - Git internals: .git/HEAD, .git/config, .git/index, .git/refs/,
//     .git/packed-refs, .git/objects/. These define repo state; writes
//     here should go through `git` commands, not direct filesystem
//     manipulation. .git/hooks/ is deliberately NOT in the list — model
//     authoring of hooks is a legitimate task.
//
// YOTTACODE.md is deliberately NOT in the deny list. It's the
// project-scope context file the agent reads on every turn, and
// keeping it fresh requires writes — same role CLAUDE.md plays for
// Claude Code. The approval modal still gates every write, so the
// user sees each change before it lands.
//
// Bypass is not possible via flags. Power users can edit these files
// themselves with their editor; the model just can't via tools.
// ValidateReadPath returns nil if the read of path is permitted under
// the given deny list, or a descriptive error. Targeted at silent
// exfiltration of credential-bearing files via read_file /
// read_many_files / grep — tools whose RequiresApproval is false. The
// user can still read these files via run_bash, which always prompts.
func ValidateReadPath(path string, deny []string) error {
	if path == "" {
		return errors.New("read path is empty")
	}
	if strings.ContainsRune(path, 0) {
		return errors.New("read path contains NUL byte")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve read path: %w", err)
	}
	abs = filepath.Clean(abs)
	if matchesAny(abs, deny) {
		return fmt.Errorf("path %q is in the read deny list (credential-bearing); use run_bash if you really need the contents", abs)
	}
	return nil
}

// DefaultDenyReadPaths returns the hardcoded list of paths the agent's
// auto-execute read tools (read_file, read_many_files, grep) refuse to
// touch. Targeted at well-known credential stores; intentionally narrow
// so the model can still read dotfiles, /etc/os-release, USER.md, and
// other benign system files.
//
//   - ~/.yottacode/.env — the agent's own provider keys. Reading this
//     into context exfiltrates the active session's API key to the
//     upstream provider on the next turn.
//   - ~/.yottacode/auth/ — OAuth bearer + refresh tokens for the
//     openai-auth provider. Same exfiltration risk as .env, plus the
//     refresh token grants long-lived access. Whole directory denied
//     so future per-provider auth files inherit the protection.
//   - ~/.ssh/, ~/.gnupg/ — private key material.
//   - ~/.aws/{credentials,config}, ~/.config/gcloud/ — cloud provider
//     access.
//   - ~/.netrc — HTTP basic-auth credentials.
//   - ~/.config/gh/hosts.yml, ~/.config/hub — GitHub tokens.
//   - ~/.docker/config.json — registry tokens.
//   - ~/.kube/config — cluster credentials.
//   - <cwd>/.env, <cwd>/.env.local — project secrets, the most common
//     accidental-exfiltration target.
//
// Power users who need the model to read one of these can bypass at
// the OS layer (cat the contents into a non-denied file first) or via
// run_bash, which prompts. Listing more paths is cheap; the cost is
// false-positive blocks on benign reads. Keep the list to files
// universally understood as secrets.
func DefaultDenyReadPaths(cwd string) []string {
	out := []string{}
	if home, err := os.UserHomeDir(); err == nil {
		yc := filepath.Join(home, ".yottacode")
		out = append(out,
			filepath.Join(yc, ".env"),
			filepath.Join(yc, "auth"),
			filepath.Join(home, ".ssh"),
			filepath.Join(home, ".gnupg"),
			filepath.Join(home, ".aws"),
			filepath.Join(home, ".config", "gcloud"),
			filepath.Join(home, ".netrc"),
			filepath.Join(home, ".config", "gh", "hosts.yml"),
			filepath.Join(home, ".config", "hub"),
			filepath.Join(home, ".docker", "config.json"),
			filepath.Join(home, ".kube", "config"),
		)
	}
	if cwd != "" {
		out = append(out,
			filepath.Join(cwd, ".env"),
			filepath.Join(cwd, ".env.local"),
		)
	}
	return out
}

func DefaultDenyPaths(cwd string) []string {
	out := []string{}
	if home, err := os.UserHomeDir(); err == nil {
		yc := filepath.Join(home, ".yottacode")
		out = append(out,
			filepath.Join(yc, "sessions"),
			filepath.Join(yc, "memory"),   // user-scope memories — only memory_save / memory_forget write here
			filepath.Join(yc, "projects"), // project-scope memories — same
			filepath.Join(yc, "auth"),     // OAuth tokens — only the login flow writes here
			filepath.Join(yc, "index.sqlite"),
			filepath.Join(yc, "USER.md"),
		)
	}
	if cwd != "" {
		out = append(out,
			filepath.Join(cwd, ".yottacode", "permissions.json"),
			filepath.Join(cwd, ".yottacode", "permissions.local.json"),
			filepath.Join(cwd, ".git", "HEAD"),
			filepath.Join(cwd, ".git", "config"),
			filepath.Join(cwd, ".git", "index"),
			filepath.Join(cwd, ".git", "refs"),
			filepath.Join(cwd, ".git", "packed-refs"),
			filepath.Join(cwd, ".git", "objects"),
		)
	}
	return out
}
