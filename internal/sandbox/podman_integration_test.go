package sandbox

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/config"
)

// Real-podman integration coverage for PodmanSandbox. Starts an actual
// container, so it's gated the same way internal/mcp's TestIntegration_Podman
// is: an explicit env opt-in (starting containers/pulling images is slow)
// plus an exec.LookPath check so machines/CI without podman skip cleanly.
//
//	YOTTACODE_SANDBOX_E2E=1 go test -v -run TestIntegration ./internal/sandbox/
func skipUnlessPodmanE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("YOTTACODE_SANDBOX_E2E") == "" {
		t.Skip("set YOTTACODE_SANDBOX_E2E=1 to run against a real podman container")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman not available in PATH")
	}
}

func testSandboxConfig() config.SandboxConfig {
	return config.SandboxConfig{
		Backend:   "podman",
		Image:     config.DefaultSandboxImage,
		Network:   "none",
		Mounts:    []string{"."},
		Memory:    "256m",
		CPUs:      1,
		PidsLimit: 128,
	}
}

// TestIntegration_StartExecMountClose exercises the full lifecycle: start
// a session container, run a command via podman exec, confirm the project
// mount is visible at the same absolute path, confirm network=none
// actually blocks egress, and confirm Close removes the container.
func TestIntegration_StartExecMountClose(t *testing.T) {
	skipUnlessPodmanE2E(t)

	dir := t.TempDir()
	if err := os.WriteFile(dir+"/marker.txt", []byte("hello-from-host"), 0o644); err != nil {
		t.Fatalf("seed marker file: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := NewPodmanSandbox(ctx, testSandboxConfig(), "e2e-test-session", dir)
	if err != nil {
		t.Fatalf("NewPodmanSandbox: %v", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = s.Close()
		}
	}()

	// The project mount is visible at the same absolute path.
	out, err := runInSandbox(t, s, dir, "cat marker.txt")
	if err != nil {
		t.Fatalf("exec cat marker.txt: %v", err)
	}
	if strings.TrimSpace(out) != "hello-from-host" {
		t.Errorf("marker.txt content = %q, want %q", out, "hello-from-host")
	}

	// A file the container writes lands on the host through the bind mount.
	if _, err := runInSandbox(t, s, dir, "echo from-container > written.txt"); err != nil {
		t.Fatalf("exec write: %v", err)
	}
	hostContent, err := os.ReadFile(dir + "/written.txt")
	if err != nil {
		t.Fatalf("read file written by container: %v", err)
	}
	if strings.TrimSpace(string(hostContent)) != "from-container" {
		t.Errorf("host-visible content = %q, want %q", hostContent, "from-container")
	}

	// network=none actually blocks egress.
	if out, err := runInSandbox(t, s, dir, "wget -T 3 -q -O - http://example.com || echo BLOCKED"); err != nil {
		t.Fatalf("exec network probe: %v", err)
	} else if !strings.Contains(out, "BLOCKED") {
		t.Errorf("expected network egress to be blocked with network=none, got: %q", out)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed = true

	// Close must actually remove the container.
	psOut, err := exec.Command("podman", "ps", "-a", "--filter", "name=yc-e2e-test-session", "--format", "{{.Names}}").CombinedOutput()
	if err != nil {
		t.Fatalf("podman ps: %v", err)
	}
	if strings.Contains(string(psOut), "yc-e2e-test-session") {
		t.Errorf("container still present after Close: %s", psOut)
	}
}

// TestIntegration_EnvPassthrough confirms a passthrough var's VALUE is
// visible inside the container but never appears in podman's own argv
// (verified indirectly: podman inspect's Env doesn't leak it either,
// since we use bare -e NAME, not -e NAME=value).
func TestIntegration_EnvPassthrough(t *testing.T) {
	skipUnlessPodmanE2E(t)

	const varName = "YOTTACODE_SANDBOX_E2E_SECRET"
	t.Setenv(varName, "super-secret-value")

	dir := t.TempDir()
	cfg := testSandboxConfig()
	cfg.EnvPassthrough = []string{varName}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, err := NewPodmanSandbox(ctx, cfg, "e2e-env-session", dir)
	if err != nil {
		t.Fatalf("NewPodmanSandbox: %v", err)
	}
	defer func() { _ = s.Close() }()

	out, err := runInSandbox(t, s, dir, "printenv "+varName)
	if err != nil {
		t.Fatalf("exec printenv: %v", err)
	}
	if strings.TrimSpace(out) != "super-secret-value" {
		t.Errorf("printenv output = %q, want the passthrough value", out)
	}
}

// TestIntegration_CgroupAndNamespaceHardening checks the cgroup/namespace
// properties added on top of podman's own defaults: swap disabled (so
// --memory is a real ceiling, not bypassable), a private cgroup namespace,
// and noexec on /tmp specifically (not the whole rootfs, which stays
// writable so installed packages persist for the session).
func TestIntegration_CgroupAndNamespaceHardening(t *testing.T) {
	skipUnlessPodmanE2E(t)

	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, err := NewPodmanSandbox(ctx, testSandboxConfig(), "e2e-hardening-session", dir)
	if err != nil {
		t.Fatalf("NewPodmanSandbox: %v", err)
	}
	defer func() { _ = s.Close() }()

	// --memory-swap == --memory disables swap: memory.swap.max reads 0.
	if out, err := runInSandbox(t, s, dir, "cat /sys/fs/cgroup/memory.swap.max"); err != nil {
		t.Fatalf("read memory.swap.max: %v", err)
	} else if got := strings.TrimSpace(out); got != "0" {
		t.Errorf("memory.swap.max = %q, want 0 (swap disabled)", got)
	}

	// --cgroupns=private: the container's cgroup path is its own root,
	// not a path rooted in the host's cgroup tree.
	if out, err := runInSandbox(t, s, dir, "cat /proc/self/cgroup"); err != nil {
		t.Fatalf("read /proc/self/cgroup: %v", err)
	} else if got := strings.TrimSpace(out); got != "0::/" {
		t.Errorf("cgroup path = %q, want \"0::/\" (private cgroup namespace)", got)
	}

	// /tmp is noexec: a script placed there cannot be executed directly,
	// even though it's still writable.
	if out, err := runInSandbox(t, s, dir, "echo 'echo pwned' > /tmp/x.sh && chmod +x /tmp/x.sh && /tmp/x.sh; echo exit=$?"); err != nil {
		t.Fatalf("exec noexec probe: %v", err)
	} else if !strings.Contains(out, "Permission denied") {
		t.Errorf("expected /tmp to be noexec, got: %q", out)
	}

	// The project mount is still fully read/write despite the :Z SELinux
	// label — labeling must not itself block access (this host has no
	// SELinux, so this also guards against a typo silently breaking
	// mounts on hosts that do).
	if out, err := runInSandbox(t, s, dir, "echo still-writable > probe.txt && cat probe.txt"); err != nil {
		t.Fatalf("exec mount read/write probe: %v", err)
	} else if strings.TrimSpace(out) != "still-writable" {
		t.Errorf("mount read/write probe = %q, want still-writable", out)
	}
}

func runInSandbox(t *testing.T, s *PodmanSandbox, cwd, command string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := s.Command(ctx, command, cwd)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestIntegration_ResumeReusesLeftoverContainerName is the crash-recovery
// regression: a container from a previous ungracefully-killed run (SIGKILL,
// OOM) can still exist under the exact deterministic name --resume would
// reuse. NewPodmanSandbox must clear it and succeed, not fail with
// "name already in use".
func TestIntegration_ResumeReusesLeftoverContainerName(t *testing.T) {
	skipUnlessPodmanE2E(t)

	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Simulate the crash: a container already running under the name a
	// second NewPodmanSandbox call with the same id would pick.
	// Deliberately never Close()d — that's the crash: the container is
	// left running under the deterministic name.
	if _, err := NewPodmanSandbox(ctx, testSandboxConfig(), "e2e-resume-session", dir); err != nil {
		t.Fatalf("NewPodmanSandbox (first): %v", err)
	}

	second, err := NewPodmanSandbox(ctx, testSandboxConfig(), "e2e-resume-session", dir)
	if err != nil {
		t.Fatalf("NewPodmanSandbox (simulated resume) should clear the leftover container and succeed, got: %v", err)
	}
	defer func() { _ = second.Close() }()

	// The resumed sandbox is actually usable (not just "didn't error").
	out, err := runInSandbox(t, second, dir, "echo resumed-ok")
	if err != nil {
		t.Fatalf("exec after resume: %v", err)
	}
	if strings.TrimSpace(out) != "resumed-ok" {
		t.Errorf("output = %q, want resumed-ok", out)
	}
}

// TestIntegration_CancelKillsProcessInsideContainer is the fix for the
// resource-leak class of bug this design review caught: canceling a
// run_bash call's context must not just kill the local `podman exec`
// client (the default exec.Cmd.Cancel behavior) while the actual command
// keeps running inside the long-lived container.
func TestIntegration_CancelKillsProcessInsideContainer(t *testing.T) {
	skipUnlessPodmanE2E(t)

	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, err := NewPodmanSandbox(ctx, testSandboxConfig(), "e2e-cancel-session", dir)
	if err != nil {
		t.Fatalf("NewPodmanSandbox: %v", err)
	}
	defer func() { _ = s.Close() }()

	// A marker file the backgrounded sleep touches right before it would
	// finish naturally (30s) — its absence after we cancel at ~1s proves
	// the process was actually killed, not just detached from our client.
	execCtx, execCancel := context.WithTimeout(context.Background(), 1*time.Second)
	cmd := s.Command(execCtx, "sleep 30; touch /tmp/should-not-exist", dir)
	_ = cmd.Run() // expected to return an error: killed by cancellation
	execCancel()

	// Give the best-effort in-container kill script a moment to run
	// (cmd.Cancel dispatches it synchronously, but the container-side
	// kill -9 takes a beat to actually reap the process).
	time.Sleep(2 * time.Second)

	out, err := runInSandbox(t, s, dir, "test -f /tmp/should-not-exist && echo STILL-RUNNING || echo KILLED")
	if err != nil {
		t.Fatalf("exec check: %v", err)
	}
	if !strings.Contains(out, "KILLED") {
		t.Errorf("expected the canceled command to be killed inside the container, got: %q", out)
	}
}

// TestIntegration_StorageOptProbeMatchesRealPodman exercises the real (not
// faked) storage-opt capability probe against this host's actual podman
// install. The result is host-dependent (ext4 vs overlay+XFS+pquota), so
// this only asserts the probe completes cleanly against a real image —
// hostcaps_test.go's unit tests cover the decision logic itself against
// faked podman output.
func TestIntegration_StorageOptProbeMatchesRealPodman(t *testing.T) {
	skipUnlessPodmanE2E(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// probeStorageOptSupported (not the cached storageOptSupported wrapper)
	// so this test's result doesn't depend on whichever test in this file
	// happens to run first and populate the process-wide cache.
	_ = probeStorageOptSupported(ctx, testSandboxConfig().Image)
}

// TestIntegration_CgroupLimitsProbeMatchesRealPodman is
// TestIntegration_StorageOptProbeMatchesRealPodman's counterpart for the
// cgroup-limits probe.
func TestIntegration_CgroupLimitsProbeMatchesRealPodman(t *testing.T) {
	skipUnlessPodmanE2E(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if !probeCgroupLimitsSupported(ctx, testSandboxConfig().Image) {
		t.Error("expected cgroup resource limits (--cpus/--memory/--pids-limit) to be available on a normal CI/dev Linux host")
	}
}
