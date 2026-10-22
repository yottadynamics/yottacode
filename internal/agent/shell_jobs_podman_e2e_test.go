//go:build unix

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

// Real-podman coverage for background shell jobs: a job started through a
// PodmanSandbox keeps running in the container, can be polled, and kill really
// terminates the in-container process. Same opt-in gate as the other sandbox
// integration tests.
//
//	YOTTACODE_SANDBOX_E2E=1 go test -v -run PodmanSandbox ./internal/agent/
func TestShellJobs_BackgroundJobInPodmanSandboxCanBePolledAndKilled(t *testing.T) {
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
	}, "e2e-shell-jobs", dir)
	if err != nil {
		t.Fatalf("NewPodmanSandbox: %v", err)
	}
	defer func() { _ = sb.Close() }()

	h := newJobsHarness(t)
	// The touch only runs if the sleep completes, so its absence after kill
	// proves the process died inside the container.
	job, err := h.jobs.Start(sb, "echo up-$((6*7)); sleep 30; touch /tmp/should-not-exist-bg", dir, false)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	eventually(t, "job output from inside the container", func() bool {
		out, _ := h.jobs.Poll(job.id)
		return strings.Contains(out, "up-42")
	})
	if out, _ := h.jobs.Poll(job.id); !strings.Contains(out, "status: running") {
		t.Fatalf("job should still be running:\n%s", out)
	}

	out, err := h.jobs.Kill(context.Background(), job.id)
	if err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if !strings.Contains(out, "status: stopped") {
		t.Fatalf("kill result:\n%s", out)
	}

	time.Sleep(2 * time.Second) // let the in-container kill finish reaping
	tool := &RunBashTool{Cwd: NewCwdRef(dir), Sandbox: sb}
	check, err := tool.Execute(context.Background(),
		`{"command":"test -f /tmp/should-not-exist-bg && echo STILL-RUNNING || echo KILLED"}`)
	if err != nil {
		t.Fatalf("follow-up check: %v", err)
	}
	if !strings.Contains(check, "KILLED") {
		t.Errorf("killed job survived inside the container: %q", check)
	}
}
