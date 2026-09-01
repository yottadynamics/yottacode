package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/sandboxcache"
	"github.com/yottadynamics/yottacode/internal/worktree"
)

// Compile-time proof PodmanSandbox satisfies agent.Sandbox — the whole
// point of the seam is that RunBashTool can hold this without
// internal/agent ever importing internal/sandbox.
var _ agent.Sandbox = (*PodmanSandbox)(nil)

func TestNewPodmanSandbox_PodmanMissingReturnsActionableError(t *testing.T) {
	orig := podmanLookPath
	defer func() { podmanLookPath = orig }()
	podmanLookPath = func(string) (string, error) { return "", errors.New("no such file") }

	_, err := NewPodmanSandbox(context.Background(), config.SandboxConfig{}, "test-session", t.TempDir())
	if err == nil {
		t.Fatal("expected error when podman is not on PATH")
	}
	if !strings.Contains(err.Error(), "podman not found") {
		t.Errorf("error = %q, want it to mention podman not found", err.Error())
	}
}

func TestNewPodmanSandbox_RejectsRelativeMountRoot(t *testing.T) {
	orig := podmanLookPath
	defer func() { podmanLookPath = orig }()
	podmanLookPath = func(string) (string, error) { return "/usr/bin/podman", nil }

	_, err := NewPodmanSandbox(context.Background(), config.SandboxConfig{}, "test-session", "relative/path")
	if err == nil {
		t.Fatal("expected error for a non-absolute mount root")
	}
}

func TestMountPaths_DefaultCollapsesToSingleMount(t *testing.T) {
	got, err := mountPaths([]string{"."}, "/proj")
	if err != nil {
		t.Fatalf("mountPaths: %v", err)
	}
	if len(got) != 1 || got[0] != "/proj" {
		t.Errorf("mountPaths([.], /proj) = %v, want [/proj]", got)
	}
}

func TestMountPaths_EmptyMountsStillYieldsRoot(t *testing.T) {
	got, err := mountPaths(nil, "/proj")
	if err != nil {
		t.Fatalf("mountPaths: %v", err)
	}
	if len(got) != 1 || got[0] != "/proj" {
		t.Errorf("mountPaths(nil, /proj) = %v, want [/proj]", got)
	}
}

func TestMountPaths_AdditionalMountResolvesRelativeToRoot(t *testing.T) {
	got, err := mountPaths([]string{".", "sub/dir"}, "/proj")
	if err != nil {
		t.Fatalf("mountPaths: %v", err)
	}
	want := []string{"/proj", "/proj/sub/dir"}
	if len(got) != len(want) {
		t.Fatalf("mountPaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("mountPaths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestMountPaths_RejectsPathsOutsideRoot guards the documented
// project-dir-only mount boundary. Relative entries may name subdirectories,
// but must not use .. segments or absolute paths to bind-mount host paths
// outside the sandbox's project root.
func TestMountPaths_RejectsPathsOutsideRoot(t *testing.T) {
	for _, mounts := range [][]string{{".."}, {"../other"}, {"sub/../../other"}, {"/etc"}} {
		if got, err := mountPaths(mounts, "/proj"); err == nil {
			t.Fatalf("mountPaths(%v) = %v, nil error; want rejection", mounts, got)
		}
	}
}

func TestMountPaths_DuplicatesCollapse(t *testing.T) {
	got, err := mountPaths([]string{".", ".", "."}, "/proj")
	if err != nil {
		t.Fatalf("mountPaths: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("mountPaths with repeated \".\" = %v, want single entry", got)
	}
}

func TestSandboxMountPathsAddsManagedWorktreeRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mountRoot := filepath.Join(t.TempDir(), "repo")
	got := sandboxMountPaths(mountRoot, []string{mountRoot})
	want := worktree.SlugDir(mountRoot)
	if len(got) != 2 || got[0].Path != mountRoot || got[0].SELinuxLabel != "z" || got[1].Path != want || got[1].SELinuxLabel != "z" {
		t.Fatalf("sandboxMountPaths = %+v, want [%s:z %s:z]", got, mountRoot, want)
	}
}

func TestPathVisibleFromMountRootIncludesManagedWorktrees(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mountRoot := filepath.Join(t.TempDir(), "repo")
	if !PathVisibleFromMountRoot(filepath.Join(mountRoot, "file.go"), mountRoot) {
		t.Fatal("project path should be visible")
	}
	if !PathVisibleFromMountRoot(filepath.Join(worktree.SlugDir(mountRoot), "feature", "file.go"), mountRoot) {
		t.Fatal("managed worktree path should be visible")
	}
}

func TestPodmanRunArgsIncludesConfiguredDNS(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mountRoot := filepath.Join(t.TempDir(), "repo")
	cfg := config.SandboxConfig{
		Image:     "sandbox-image",
		Network:   "host",
		DNS:       []string{"1.1.1.1", "8.8.8.8"},
		Mounts:    []string{"."},
		Memory:    "256m",
		CPUs:      1,
		PidsLimit: 128,
	}
	args, err := podmanRunArgs(cfg, "yc-test", mountRoot, hostCapabilities{StorageOpt: true, CgroupLimits: true})
	if err != nil {
		t.Fatalf("podmanRunArgs: %v", err)
	}
	joined := " " + strings.Join(args, " ") + " "
	if !strings.Contains(joined, " --dns 1.1.1.1 ") || !strings.Contains(joined, " --dns 8.8.8.8 ") {
		t.Fatalf("podman args missing configured DNS servers: %v", args)
	}
}

// TestPodmanRunArgsMountsPersistentGoCacheDir guards the fix for slow
// sandboxed Go tests: every session starts a fresh container (see
// NewPodmanSandbox's doc comment), so without a host-persisted mount for
// GOCACHE/GOMODCACHE (set by internal/agent's prepareRunTestsCommand under
// this exact path — see internal/sandboxcache), each new session pays
// a full `go mod download` plus full recompile on its first Go command.
func TestPodmanRunArgsMountsPersistentGoCacheDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mountRoot := filepath.Join(t.TempDir(), "repo")
	cfg := config.SandboxConfig{
		Image:     "sandbox-image",
		Network:   "host",
		Mounts:    []string{"."},
		Memory:    "256m",
		CPUs:      1,
		PidsLimit: 128,
	}
	args, err := podmanRunArgs(cfg, "yc-test", mountRoot, hostCapabilities{StorageOpt: true, CgroupLimits: true})
	if err != nil {
		t.Fatalf("podmanRunArgs: %v", err)
	}
	wantDir := filepath.Join(home, ".yottacode", sandboxcache.GoCacheHomeSubdir)
	wantMount := "-v " + wantDir + ":" + wantDir + ":z"
	if !strings.Contains(strings.Join(args, " "), wantMount) {
		t.Fatalf("podman args missing persistent go cache mount %q: %v", wantMount, args)
	}
	if info, err := os.Stat(wantDir); err != nil || !info.IsDir() {
		t.Fatalf("expected podmanRunArgs to create %s, stat error: %v", wantDir, err)
	}
}

// TestPodmanRunArgsGoCacheDirIsHomeRootedNotWorktreeScoped: the cache mount
// must be a single directory shared across every repo/worktree/session (like
// a host's own $GOCACHE/$GOMODCACHE), not nested under mountRoot — otherwise
// it would either violate the project-mount boundary (mountPaths rejects
// paths outside mountRoot) or fail to persist once a worktree is removed.
func TestPodmanRunArgsGoCacheDirIsHomeRootedNotWorktreeScoped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mountRootA := filepath.Join(t.TempDir(), "repo-a")
	mountRootB := filepath.Join(t.TempDir(), "repo-b")
	cfg := config.SandboxConfig{Image: "sandbox-image", Network: "host", Mounts: []string{"."}, Memory: "256m", CPUs: 1, PidsLimit: 128}

	argsA, err := podmanRunArgs(cfg, "yc-a", mountRootA, hostCapabilities{StorageOpt: true, CgroupLimits: true})
	if err != nil {
		t.Fatalf("podmanRunArgs(a): %v", err)
	}
	argsB, err := podmanRunArgs(cfg, "yc-b", mountRootB, hostCapabilities{StorageOpt: true, CgroupLimits: true})
	if err != nil {
		t.Fatalf("podmanRunArgs(b): %v", err)
	}
	wantMount := "-v " + filepath.Join(home, ".yottacode", sandboxcache.GoCacheHomeSubdir)
	for _, args := range [][]string{argsA, argsB} {
		if !strings.Contains(strings.Join(args, " "), wantMount) {
			t.Fatalf("expected the same shared go cache mount %q regardless of mount root, got %v", wantMount, args)
		}
	}
}

func TestPodmanRunArgsOmitsDNSWhenNetworkNone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mountRoot := filepath.Join(t.TempDir(), "repo")
	cfg := config.SandboxConfig{
		Image:     "sandbox-image",
		Network:   "none",
		DNS:       []string{"1.1.1.1", "8.8.8.8"},
		Mounts:    []string{"."},
		Memory:    "256m",
		CPUs:      1,
		PidsLimit: 128,
	}
	args, err := podmanRunArgs(cfg, "yc-test", mountRoot, hostCapabilities{StorageOpt: true, CgroupLimits: true})
	if err != nil {
		t.Fatalf("podmanRunArgs: %v", err)
	}
	joined := " " + strings.Join(args, " ") + " "
	if strings.Contains(joined, " --dns ") {
		t.Fatalf("podman args must not include DNS with --network=none: %v", args)
	}
}

func TestPodmanRunArgsOmitsResourceLimitsWhenCgroupUnsupported(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mountRoot := filepath.Join(t.TempDir(), "repo")
	cfg := config.SandboxConfig{
		Image:     "sandbox-image",
		Network:   "host",
		Mounts:    []string{"."},
		Memory:    "256m",
		CPUs:      1,
		PidsLimit: 128,
	}
	args, err := podmanRunArgs(cfg, "yc-test", mountRoot, hostCapabilities{StorageOpt: true, CgroupLimits: false})
	if err != nil {
		t.Fatalf("podmanRunArgs: %v", err)
	}
	joined := " " + strings.Join(args, " ") + " "
	for _, want := range []string{"--pids-limit", "--memory", "--memory-swap", "--cpus"} {
		if strings.Contains(joined, " "+want) {
			t.Errorf("podman args must omit %s when CgroupLimits is unsupported: %v", want, args)
		}
	}
}

func TestPodmanRunArgsOmitsStorageOptWhenUnsupportedOrZeroDisk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mountRoot := filepath.Join(t.TempDir(), "repo")
	baseCfg := config.SandboxConfig{
		Image: "sandbox-image", Network: "host", Mounts: []string{"."},
		Memory: "256m", CPUs: 1, PidsLimit: 128, Disk: 4096,
	}

	unsupported, err := podmanRunArgs(baseCfg, "yc-test", mountRoot, hostCapabilities{StorageOpt: false, CgroupLimits: true})
	if err != nil {
		t.Fatalf("podmanRunArgs: %v", err)
	}
	if strings.Contains(strings.Join(unsupported, " "), "--storage-opt") {
		t.Errorf("podman args must omit --storage-opt when StorageOpt is unsupported: %v", unsupported)
	}

	zeroDisk := baseCfg
	zeroDisk.Disk = 0
	noQuota, err := podmanRunArgs(zeroDisk, "yc-test", mountRoot, hostCapabilities{StorageOpt: true, CgroupLimits: true})
	if err != nil {
		t.Fatalf("podmanRunArgs: %v", err)
	}
	if strings.Contains(strings.Join(noQuota, " "), "--storage-opt") {
		t.Errorf("podman args must omit --storage-opt when Disk is 0: %v", noQuota)
	}

	supported, err := podmanRunArgs(baseCfg, "yc-test", mountRoot, hostCapabilities{StorageOpt: true, CgroupLimits: true})
	if err != nil {
		t.Fatalf("podmanRunArgs: %v", err)
	}
	if !strings.Contains(strings.Join(supported, " "), "--storage-opt=size=4096m") {
		t.Errorf("podman args must include --storage-opt=size=4096m when supported and Disk > 0: %v", supported)
	}
}

// Sanity: sandbox names must survive a real exec.Command construction —
// this doesn't run podman, just confirms Command builds a well-formed argv.
func TestPodmanSandbox_CommandBuildsExpectedArgv(t *testing.T) {
	s := &PodmanSandbox{name: "yc-test"}
	cmd := s.Command(context.Background(), "echo hi", "/proj")
	want := []string{"podman", "exec", "-w", "/proj", "yc-test", "/bin/sh", "-c"}
	if len(cmd.Args) != len(want)+1 {
		t.Fatalf("argv = %v, want %d args", cmd.Args, len(want)+1)
	}
	for i := range want {
		if cmd.Args[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, cmd.Args[i], want[i])
		}
	}
	// The final -c argument is the original command wrapped with a
	// cancellation marker (see Command's doc comment) — it must still
	// contain the original command verbatim, not just a marker prefix.
	if got := cmd.Args[len(cmd.Args)-1]; !strings.HasSuffix(got, "\necho hi") {
		t.Errorf("wrapped command = %q, want it to end with the original command", got)
	}
	if cmd.Cancel == nil {
		t.Error("Command should set cmd.Cancel to kill the in-container process on cancellation")
	}
}

func TestPodmanSandbox_Label(t *testing.T) {
	s := &PodmanSandbox{name: "yc-test"}
	if s.Label() != "[podman-sandbox]" {
		t.Errorf("Label() = %q, want [podman-sandbox]", s.Label())
	}
}

// TestPodmanSandbox_LabelConformsToBracketContract pins PodmanSandbox to
// the agent.Sandbox.Label() contract documented on the interface: leading
// "[", trailing "]", NO trailing space (RunBashTool.PreviewCall adds the
// separating space itself, and the TUI's toolHeader recovers the tag by
// scanning for that exact "[...] " shape in the concatenated result). A
// future edit to this label — a new prefix, an added space, dropped
// brackets — would silently break the TUI's [podman] card annotation
// without this regression test to catch it.
func TestPodmanSandbox_LabelConformsToBracketContract(t *testing.T) {
	label := (&PodmanSandbox{name: "yc-test"}).Label()
	if !strings.HasPrefix(label, "[") || !strings.HasSuffix(label, "]") {
		t.Fatalf("Label() = %q, want it wrapped in exactly one leading [ and trailing ]", label)
	}
	if strings.HasSuffix(label, " ]") || strings.Contains(label, "] ") {
		t.Errorf("Label() = %q, must not include a trailing space before or after ] — PreviewCall adds the separator itself", label)
	}
}
