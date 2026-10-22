package agent

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/sandbox"
)

// Real-podman coverage for run_bash's deadline. Gated like
// internal/sandbox's integration tests: an explicit env opt-in plus a
// podman-on-PATH check, so CI and machines without podman skip cleanly.
//
//	YOTTACODE_SANDBOX_E2E=1 go test -v -run Podman ./internal/agent/
func TestRunBashTool_TimeoutKillsCommandInsidePodmanSandbox(t *testing.T) {
	if os.Getenv("YOTTACODE_SANDBOX_E2E") == "" {
		t.Skip("set YOTTACODE_SANDBOX_E2E=1 to run against a real podman container")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman not available in PATH")
	}

	dir := t.TempDir()
	startCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sb, err := sandbox.NewPodmanSandbox(startCtx, config.SandboxConfig{
		Backend:   "podman",
		Image:     config.DefaultSandboxImage,
		Network:   "none",
		Mounts:    []string{"."},
		Memory:    "256m",
		CPUs:      1,
		PidsLimit: 128,
	}, "e2e-run-bash-timeout", dir)
	if err != nil {
		t.Fatalf("NewPodmanSandbox: %v", err)
	}
	defer func() { _ = sb.Close() }()

	tool := &RunBashTool{Cwd: NewCwdRef(dir), Sandbox: sb}

	// The touch only runs if the sleep is allowed to finish, so its absence
	// after the timeout proves the process died inside the container and was
	// not merely detached from the local podman client.
	start := time.Now()
	out, err := tool.Execute(context.Background(),
		`{"command":"echo partial; sleep 30; touch /tmp/should-not-exist","timeout_ms":1000}`)
	if err != nil {
		t.Fatalf("a timeout must be a recoverable result, got error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("Execute took %v; timeout did not fire promptly", elapsed)
	}
	for _, want := range []string{"exit=124", "timed out after 1s", "partial"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}

	time.Sleep(2 * time.Second) // let the in-container kill finish reaping
	check, err := tool.Execute(context.Background(),
		`{"command":"test -f /tmp/should-not-exist && echo STILL-RUNNING || echo KILLED"}`)
	if err != nil {
		t.Fatalf("follow-up check: %v", err)
	}
	if !strings.Contains(check, "KILLED") {
		t.Errorf("timed-out command survived inside the container: %q", check)
	}
}
