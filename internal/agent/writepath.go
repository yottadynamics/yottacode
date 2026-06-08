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

	"github.com/yottadynamics/yottacode/internal/memory"
)

// WritePathOptions configures the validator for a single tool. Build it
// once per session in run.go and share across every mutating tool.
type WritePathOptions struct {
	// Cwd is the primary allowed root, queried at validate time so an
	// in-session cwd swap (enter_worktree) flows through to the write
	// validator without rebuilding WriteOpts. Required. Shared pointer
	// across all mutating tools registered for one session, same as
	// each tool's own t.Cwd.
	Cwd *CwdRef

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

	// PlanModeAllowedFile is the absolute path of the single plan file
	// the agent is permitted to write to while plan mode is active.
	// When non-empty, ValidateWritePath short-circuits to nil for an
	// exact match (after symlink rejection still applies). Empty when
	// plan mode is off — the regular Cwd / AllowedPaths / DenyExact
	// stack is the only authority. The TUI mutates this field on the
	// registered *WriteFileTool / *EditFileTool / *ApplyDiffTool when
	// /plan toggles, and zeroes it on exit.
	PlanModeAllowedFile string
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
	lexicalAbs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve write path: %w", err)
	}
	lexicalAbs = filepath.Clean(lexicalAbs)
	abs := lexicalAbs

	// Symlink handling (default; --allow-symlinks opts out). First reject a
	// symlinked LEAF (the classic "symlink to /etc/passwd" trick), then
	// resolve symlinks in the PARENT chain so the deny-list and containment
	// checks run on the real destination — a symlinked parent dir must not
	// launder a lexically-in-cwd path into a write outside the workspace.
	if !opts.AllowSymlinks {
		if info, err := os.Lstat(lexicalAbs); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %q is a symlink; refusing to follow (pass --allow-symlinks to override)", lexicalAbs)
		}
		abs = resolveWriteTarget(lexicalAbs)
	}

	// Deny-list check in BOTH frames. DefaultDenyPaths entries are built
	// from the cwd string as given, so when cwd traverses a symlink (macOS
	// /tmp -> /private/tmp, a symlinked home) they're in the lexical frame
	// while `abs` is resolved — checking only one frame lets a write to
	// e.g. .yottacode/permissions.json slip through and self-grant. Match
	// the lexical path (direct writes) AND the resolved path (symlinked
	// path -> deny target).
	if matchesAny(lexicalAbs, opts.DenyExact) || matchesAny(abs, opts.DenyExact) {
		return fmt.Errorf("path %q is in the deny list (yottacode-managed state, git internals)", abs)
	}

	// Plan-mode short-circuit: an exact match against the
	// session-resolved plan-file path is the one allowed write outside
	// cwd. Symlink rejection above still applies (no symlink
	// laundering). Deny list above also still applies (no naming a
	// plan file at .yottacode/permissions.json or similar — the
	// PlansDir lives under ~/.yottacode/plans/, which is not in the
	// deny list).
	if opts.PlanModeAllowedFile != "" {
		allowedAbs, err := filepath.Abs(opts.PlanModeAllowedFile)
		if err == nil {
			allowedAbs = filepath.Clean(allowedAbs)
			if !opts.AllowSymlinks {
				allowedAbs = resolveWriteTarget(allowedAbs)
			}
			if allowedAbs == abs {
				return nil
			}
		}
	}

	// Containment: compare the (resolved) target against the workspace
	// roots in the same real-filesystem frame. canonicalExisting resolves
	// the roots too, so a symlinked cwd (macOS /tmp -> /private/tmp) and
	// the resolved target line up instead of spuriously mismatching.
	container := func(root string) string {
		if opts.AllowSymlinks {
			a, err := filepath.Abs(root)
			if err != nil {
				return ""
			}
			return filepath.Clean(a)
		}
		return canonicalExisting(root)
	}
	cwd := opts.Cwd.Get()
	if pathUnder(abs, container(cwd)) {
		return nil
	}
	for _, root := range opts.AllowedPaths {
		if root == "" {
			continue
		}
		if pathUnder(abs, container(root)) {
			return nil
		}
	}
	return &ErrPathOutsideWorkspace{
		// Report the lexical path the model/user actually specified, not the
		// symlink-resolved `abs` used for the containment check — Cwd is
		// lexical too, and on macOS a resolved /etc -> /private/etc (or a
		// /var/folders tempdir) would otherwise surface a confusing path in
		// the trust modal and the --allow-paths hint.
		Path:         lexicalAbs,
		Cwd:          cwd,
		AllowedRoots: append([]string(nil), opts.AllowedPaths...),
	}
}

// ErrPathOutsideWorkspace is the structured error ValidateWritePath
// returns when a write target falls outside cwd and every
// --allow-paths root. Callers in the TUI (errors.As) catch this to
// render the inline path-trust elevation modal — Prompt 2 in
// yottacode-roadmap/folder-trust.md — and offer the user a choice
// between Allow-once / Trust-for-session / Reject.
//
// The fields are the bits the modal needs to render a useful
// dialog: the absolute path the model wanted, the workspace it's
// outside of, and the existing allow-list so the user can see
// what's already trusted before deciding.
//
// Error() returns a descriptive message the model sees on Reject:
// names the workspace boundary plus a recovery hint, so the model
// can switch to an in-workspace target or stop and ask the user
// to relaunch with --allow-paths. Mirrors Claude Code's per-tool
// deny semantics — informative, not prescriptive.
type ErrPathOutsideWorkspace struct {
	Path         string
	Cwd          string
	AllowedRoots []string
}

func (e *ErrPathOutsideWorkspace) Error() string {
	return fmt.Sprintf(
		"write to %q denied: outside session workspace %q. Choose a target under %q, or stop and ask the user to relaunch with --allow-paths %q.",
		e.Path, e.Cwd, e.Cwd, filepath.Dir(e.Path),
	)
}

// canonicalExisting resolves every symlink in p when p exists on disk,
// returning the cleaned, absolute real path; otherwise it returns the
// lexically-cleaned abs of p. Used to put a candidate path and the
// workspace roots into the same real-filesystem frame before a
// containment check, so a symlinked cwd (e.g. macOS /tmp -> /private/tmp)
// doesn't cause false rejections.
func canonicalExisting(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// resolveWriteTarget canonicalizes a write target's PARENT chain
// (resolving symlinks in the existing ancestors) while leaving the final
// component unresolved — the leaf may not exist yet, and its own
// symlink-ness is checked separately by the caller. Non-existent trailing
// components (the file being created, new intermediate dirs) are
// re-appended after resolving the deepest existing ancestor. This stops a
// symlinked parent directory from laundering an otherwise-in-cwd path
// into a write outside the workspace.
func resolveWriteTarget(abs string) string {
	dir := filepath.Dir(abs)
	leaf := filepath.Base(abs)
	var missing []string
	cur := dir
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			parts := []string{resolved}
			for i := len(missing) - 1; i >= 0; i-- {
				parts = append(parts, missing[i])
			}
			parts = append(parts, leaf)
			return filepath.Clean(filepath.Join(parts...))
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs // reached the root without resolving; fall back to lexical
		}
		missing = append(missing, filepath.Base(cur))
		cur = parent
	}
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
	// read_file / read_many_files open() FOLLOWS symlinks, so an
	// innocuously-named symlink can point straight at a credential store.
	// Re-check the deny list against the resolved real path so
	// `ln -s ~/.ssh/id_rsa ./notes` then reading ./notes is still blocked.
	// Compare in a single real-filesystem frame: the resolved candidate is
	// canonical, so the deny entries must be canonicalized too — otherwise a
	// symlinked workspace root (macOS /tmp -> /private/tmp, /var/folders ->
	// /private/var/folders) leaves the two sides in different frames and the
	// check silently misses (it did, on macOS CI).
	if resolved := canonicalExisting(abs); resolved != abs {
		if matchesAny(resolved, deny) || matchesAny(resolved, canonicalizeDeny(deny)) {
			return fmt.Errorf("path %q resolves to %q, which is in the read deny list (credential-bearing); use run_bash if you really need the contents", abs, resolved)
		}
	}
	return nil
}

// canonicalizeDeny resolves every deny entry's symlinks (via
// canonicalExisting) so a resolved candidate path can be compared against the
// deny list in one real-filesystem frame. Entries that don't exist on disk
// fall back to their lexical absolute form, matching matchesAny's behavior.
func canonicalizeDeny(deny []string) []string {
	out := make([]string, 0, len(deny))
	for _, d := range deny {
		if d == "" {
			continue
		}
		out = append(out, canonicalExisting(d))
	}
	return out
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
			filepath.Join(yc, "auth"), // OAuth tokens — only the login flow writes here
			filepath.Join(yc, "index.sqlite"),
			filepath.Join(yc, "USER.md"),
			filepath.Join(yc, "trusted-roots.json"), // folder-trust store — only the trust prompt / `yottacode trust` writes here
		)
	}
	// The whole memory tree: memory/user/, memory/projects/<slug>/, and
	// the per-project subagents/ transcript dirs nested inside. Only
	// memory_save / memory_forget write memories (the harness writes
	// transcripts directly, which bypasses this list). Resolved via
	// memory.MemoryRoot rather than ~/.yottacode so the $YOTTACODE_HOME
	// override stays covered.
	if root, err := memory.MemoryRoot(); err == nil {
		out = append(out, root)
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
		// In a *linked git worktree* (what dispatch's write subtasks run
		// in), `.git` is a pointer FILE — `gitdir: …/.git/worktrees/<name>`
		// — not a directory. Rewriting it repoints the worktree at another
		// gitdir and escapes the per-worktree isolation dispatch relies on,
		// so deny the pointer file specifically. We deliberately do NOT add
		// `.git` unconditionally: in the main repo `.git` is a directory,
		// and matchesAny treats every deny entry as a directory prefix, so
		// listing it would also re-deny `.git/hooks/` — which the deny list
		// intentionally leaves writable (model hook authoring is allowed,
		// see above). Gating on "is a non-directory" targets exactly the
		// worktree pointer-file case and leaves the main repo untouched.
		gitPath := filepath.Join(cwd, ".git")
		if info, err := os.Lstat(gitPath); err == nil && !info.IsDir() {
			out = append(out, gitPath)
		}
	}
	return out
}
