// Package sandbox holds the podman-backed Sandbox implementation
// (internal/agent.Sandbox) — a session- or worker-scoped rootless
// container that RunBashTool dispatches commands into via `podman exec`
// instead of running them directly on the host. See
// roadmap/sandbox-podman.md for the design.
//
// This package mirrors internal/lsp's role: a sibling package holding an
// external-process lifecycle, imported only by the two session entry
// points (internal/tui, internal/oneshot) and internal/agent's dispatch
// worker wiring — never by internal/agent itself, which only needs the
// Sandbox interface.
package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/worktree"
)

// podmanLookPath is swapped in tests to simulate podman being missing
// from PATH without touching the real environment. Mirrors
// internal/agent's mediaLookPath pattern (media_tools.go).
var podmanLookPath = exec.LookPath

// unsafeNameChars matches anything not allowed in a podman container name
// beyond the first character. sess.ID / dispatch task IDs are already
// podman-safe (digits, hyphens, dots, hex), but this sanitizes
// defensively rather than trusting an upstream ID format forever.
var unsafeNameChars = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// containerStopGrace is passed as `podman rm -f -t <seconds>`: how long
// podman waits after SIGTERM before it escalates to SIGKILL on a still-
// running container. Podman's own default is 10s; we don't need the
// process to shut down cleanly (it's `sleep infinity`), so cut this down.
const containerStopGrace = 5 * time.Second

// containerCloseTimeout bounds Close's `podman rm -f` end-to-end — Close
// has no caller-supplied context (it's typically called from a defer/
// cleanup path where the original ctx may already be canceled), so it
// owns its own deadline instead of hanging a session/worker teardown.
// Must stay comfortably larger than containerStopGrace, or the external
// timeout can kill the podman CLI mid-shutdown before it finishes its own
// SIGTERM-then-SIGKILL sequence.
const containerCloseTimeout = 20 * time.Second

// tmpTmpfsSize bounds the container's /tmp — kept well under the smallest
// realistic cfg.Memory so a full /tmp can't alone exhaust the memory
// cgroup (tmpfs pages count against it). noexec/nosuid/nodev applies only
// to this one mount, not the whole rootfs (rootfs stays writable, not
// --read-only, because installed packages must persist for the session —
// see roadmap/sandbox-podman.md's Lifecycle section). It still blocks the
// download-to-/tmp-then-execute pattern without touching where package
// managers actually install (project dir, /usr, /root, etc.).
const tmpTmpfsSize = "128m"

// execKillTimeout bounds the best-effort in-container kill issued from
// Command's cmd.Cancel (see below) and the leftover-container cleanup in
// NewPodmanSandbox.
const execKillTimeout = 5 * time.Second

// execMarkerSeq numbers each Command call so its wrapped shell script has
// a marker unique within the process — used to find and signal the right
// process inside the container on cancellation (see Command).
var execMarkerSeq atomic.Int64

// PodmanSandbox runs commands inside a single long-lived rootless podman
// container (`podman run -d ... sleep infinity`), dispatching each
// RunBashTool call via `podman exec`. One container per session or per
// dispatch write-worker — never per command (see roadmap/sandbox-podman.md
// for why a fresh container per command is rejected).
type PodmanSandbox struct {
	name string
}

// NewPodmanSandbox starts a session-scoped container per cfg, bind-mounting
// mountRoot at the SAME absolute path inside the container (so paths the
// model uses are valid on both sides). id becomes part of the container
// name (e.g. a session ID or "<session>-<taskID>" for a dispatch worker).
//
// Synchronous: blocks until the container is confirmed started. On any
// failure (podman missing, image pull failure, rootless setup issues) it
// returns a wrapped, actionable error and leaves no container running —
// callers must treat this as fatal to session/worker startup and must
// NEVER fall back to HostSandbox on error, which would silently defeat
// the isolation the caller asked for.
func NewPodmanSandbox(ctx context.Context, cfg config.SandboxConfig, id, mountRoot string) (*PodmanSandbox, error) {
	if _, err := podmanLookPath("podman"); err != nil {
		return nil, fmt.Errorf("sandbox: podman not found in PATH: %w", err)
	}
	mountRoot = filepath.Clean(mountRoot)
	if !filepath.IsAbs(mountRoot) {
		return nil, fmt.Errorf("sandbox: mount root %q must be an absolute path", mountRoot)
	}
	name := "yc-" + unsafeNameChars.ReplaceAllString(id, "-")

	// A container from a previous ungracefully-killed run (SIGKILL, OOM,
	// crash — anything that skips Close()) can still be sitting under
	// this exact name: it's deterministic (session ID, or session ID +
	// task ID for a dispatch worker), and --resume reloads the same
	// session ID. Without this, `podman run --name` below would fail
	// with "name already in use" and the resumed session could never
	// start a sandbox again. Best-effort and silent: "no such container"
	// is the overwhelmingly common case and not worth surfacing.
	_ = removeContainer(ctx, name)

	args := []string{
		"run", "-d", "--name", name,
		// Namespace isolation (PID/IPC/UTS/mount/user/network) is podman's
		// own default for every rootless container — verified via
		// /proc/1/ns inside a throwaway container, not something these
		// flags need to request. --cgroupns=private is set explicitly
		// anyway rather than relying on that default, since it determines
		// whether the cgroup limits below are even meaningful (a shared/
		// host cgroup namespace would let the container see, and
		// potentially reason about escaping, the host's cgroup tree).
		"--cgroupns=private",
		"--userns=keep-id",
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		fmt.Sprintf("--pids-limit=%d", cfg.PidsLimit),
		fmt.Sprintf("--memory=%s", cfg.Memory),
		// Without an explicit --memory-swap, a process can spill past
		// --memory into swap and the limit above stops being real
		// pressure. Setting it equal to --memory disables swap for this
		// container entirely (verified: memory.swap.max reads 0 with
		// this pairing) rather than leaving it at podman's default
		// (memory-swap unset, which lets the container swap up to double
		// its memory limit — or unlimited, depending on host config).
		fmt.Sprintf("--memory-swap=%s", cfg.Memory),
		fmt.Sprintf("--cpus=%v", cfg.CPUs),
		fmt.Sprintf("--network=%s", cfg.Network),
		fmt.Sprintf("--tmpfs=/tmp:rw,noexec,nosuid,nodev,size=%s", tmpTmpfsSize),
	}
	mounts, err := mountPaths(cfg.Mounts, mountRoot)
	if err != nil {
		return nil, err
	}
	for _, m := range sandboxMountPaths(mountRoot, mounts) {
		// :Z relabels configured project mounts for this container's EXCLUSIVE
		// use under SELinux. The managed worktree root uses :z because multiple
		// sessions for the same repo slug may legitimately mount it at once.
		args = append(args, "-v", m.Path+":"+m.Path+":"+m.SELinuxLabel)
	}
	args = append(args, "-w", mountRoot)
	for _, envName := range cfg.EnvPassthrough {
		// Bare `-e NAME` (no `=value`): podman reads the value from its
		// OWN process environment and forwards it into the container.
		// The value itself never appears in podman's argv, so it never
		// shows up in `ps`/`/proc/<pid>/cmdline` for other local users —
		// the whole point of opt-in credential injection.
		args = append(args, "-e", envName)
	}
	args = append(args, cfg.Image, "sleep", "infinity")

	cmd := exec.CommandContext(ctx, "podman", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		// `-d` detaches: the podman daemon can finish creating and
		// starting the container server-side even though ctx cancellation
		// killed OUR client before it read the result. Clean up
		// best-effort so a caller that gives up on the returned error
		// doesn't leave an orphaned container behind. Uses a fresh
		// context, not ctx — ctx itself may be why we're here (already
		// canceled), and this cleanup must still get to run.
		_ = removeContainer(context.Background(), name)
		return nil, fmt.Errorf("sandbox: podman run failed: %w (output: %s)", err, strings.TrimSpace(out.String()))
	}
	return &PodmanSandbox{name: name}, nil
}

type sandboxMountPath struct {
	Path         string
	SELinuxLabel string
}

// sandboxMountPaths extends user-configured project-relative mounts with this
// repo's managed worktree storage. yottacode worktrees live outside the repo
// checkout under ~/.yottacode/worktrees/<slug>/, so mounting only mountRoot
// strands an already-live sandbox when enter_worktree later swaps cwd there.
func sandboxMountPaths(mountRoot string, configured []string) []sandboxMountPath {
	out := make([]sandboxMountPath, 0, len(configured)+1)
	for _, m := range configured {
		out = append(out, sandboxMountPath{Path: m, SELinuxLabel: "Z"})
	}
	worktreeRoot := worktree.SlugDir(mountRoot)
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		return out
	}
	for _, m := range configured {
		if sameOrContains(m, worktreeRoot) || sameOrContains(worktreeRoot, m) {
			return out
		}
	}
	return append(out, sandboxMountPath{Path: worktreeRoot, SELinuxLabel: "z"})
}

// PathVisibleFromMountRoot mirrors sandboxMountPaths without creating anything.
func PathVisibleFromMountRoot(path, mountRoot string) bool {
	return sameOrContains(mountRoot, path) || sameOrContains(worktree.SlugDir(mountRoot), path)
}

// mountPaths resolves cfg.Mounts (relative to mountRoot, "." meaning
// mountRoot itself) into an absolute, de-duplicated list that always
// includes mountRoot — the common single-mount case collapses to exactly
// one entry. Every resolved path must stay inside mountRoot so the sandbox's
// project-dir-only mount boundary cannot be widened with ".." or absolute
// paths in config.
func mountPaths(mounts []string, mountRoot string) ([]string, error) {
	seen := map[string]bool{mountRoot: true}
	out := []string{mountRoot}
	for _, raw := range mounts {
		m := strings.TrimSpace(raw)
		abs := mountRoot
		if m != "" && m != "." {
			if filepath.IsAbs(m) {
				return nil, fmt.Errorf("sandbox: mount %q must be relative to the project root", raw)
			}
			abs = filepath.Clean(filepath.Join(mountRoot, m))
			if abs != mountRoot && !strings.HasPrefix(abs, mountRoot+string(filepath.Separator)) {
				return nil, fmt.Errorf("sandbox: mount %q resolves outside project root %s", raw, mountRoot)
			}
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		out = append(out, abs)
	}
	return out, nil
}

func sameOrContains(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if root == path {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Command returns a `podman exec` into this sandbox's container. Mirrors
// HostSandbox.Command's contract exactly: command is a single /bin/sh -c
// argument, so there is no new shell-injection surface beyond what
// already exists for the host path today.
//
// The command is tagged with a unique marker and cmd.Cancel is set to
// best-effort kill the marked process (and its direct children) inside
// the container when ctx is canceled or times out. Without this, the
// default Cancel behavior (kill the local `podman exec` client) does NOT
// kill the process it started inside the long-lived container — verified
// empirically: a canceled `sleep 30` kept running, reparented to the
// container's PID 1, after the local client was killed. A plain
// process-group kill doesn't reach it either — this container setup
// gives no per-exec process group (children inherit the container's own
// group rather than a fresh one), also verified by direct test.
//
// Known residual limitation: only the marked process and its DIRECT
// children are targeted, covering the wrapper shell + whatever it
// directly forks — the overwhelming majority of real run_bash shapes. A
// grandchild the command itself spawns can still survive as an orphan
// inside the container until Close() removes it. Full recursive-tree
// cleanup would need per-exec process-group isolation this container
// doesn't have.
func (s *PodmanSandbox) Command(ctx context.Context, command, cwd string) *exec.Cmd {
	marker := fmt.Sprintf("__yc_job_%d__", execMarkerSeq.Add(1))
	wrapped := ": " + marker + "\n" + command
	cmd := exec.CommandContext(ctx, "podman", "exec", "-w", cwd, s.name, "/bin/sh", "-c", wrapped)
	cmd.Cancel = func() error {
		killCtx, killCancel := context.WithTimeout(context.Background(), execKillTimeout)
		defer killCancel()
		// $$ excludes this kill script's own process from the match —
		// its own argv necessarily contains the marker literal too, via
		// the -c script text below, and would otherwise self-match.
		killScript := `victims=""
for p in /proc/[0-9]*; do
  pid=${p#/proc/}
  [ "$pid" = "$$" ] && continue
  grep -qa ` + marker + ` "$p/cmdline" 2>/dev/null && victims="$victims $pid"
done
for p in /proc/[0-9]*; do
  cpid=${p#/proc/}
  [ "$cpid" = "$$" ] && continue
  ppid=$(sed -n "s/^PPid:[[:space:]]*//p" "$p/status" 2>/dev/null)
  for v in $victims; do
    [ "$ppid" = "$v" ] && victims="$victims $cpid"
  done
done
for v in $victims; do kill -9 "$v" 2>/dev/null; done`
		_ = exec.CommandContext(killCtx, "podman", "exec", s.name, "/bin/sh", "-c", killScript).Run()
		return cmd.Process.Kill()
	}
	return cmd
}

// Unambiguously "-sandbox", not just "[podman]" — a bare "[podman]" tag on
// a run_bash card reads like a statement about the command itself (e.g.
// "this ran podman"), not "this ran inside a podman sandbox container".
func (s *PodmanSandbox) Label() string { return "[podman-sandbox]" }

// Close removes the container. See removeContainer — Close just adds the
// caller-facing error wrap; callers at the two documented call sites
// (session teardown, dispatch worker cleanup) treat it as non-fatal and
// swallow it, consistent with this codebase's existing best-effort
// cleanup convention (dispatch_cleanup.go).
func (s *PodmanSandbox) Close() error {
	if err := removeContainer(context.Background(), s.name); err != nil {
		return fmt.Errorf("sandbox: %w", err)
	}
	return nil
}

// removeContainer runs `podman rm -f -t <containerStopGrace> name`,
// bounded by containerCloseTimeout. Shared by three call sites: Close
// (normal teardown), the pre-create sweep in NewPodmanSandbox (clears a
// leftover container from a crashed prior run under the same
// deterministic name), and the post-failure cleanup in NewPodmanSandbox
// (a container `podman run -d` created server-side despite the local
// client erroring, e.g. via ctx cancellation).
//
// The explicit -t is required, not cosmetic: without it, `podman rm -f`
// on a running container waits podman's own default 10s SIGTERM grace
// period before escalating to SIGKILL, which can exceed a shorter
// enclosing timeout and leave the container neither removed nor
// reported as an error — the CLI itself gets killed mid-shutdown. -t
// forces the grace period we actually want instead of trusting an
// enclosing timeout to happen to be long enough.
func removeContainer(parent context.Context, name string) error {
	ctx, cancel := context.WithTimeout(parent, containerCloseTimeout)
	defer cancel()
	stopGrace := fmt.Sprintf("%d", int(containerStopGrace.Seconds()))
	out, err := exec.CommandContext(ctx, "podman", "rm", "-f", "-t", stopGrace, name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("podman rm -f %s: %w (output: %s)", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
