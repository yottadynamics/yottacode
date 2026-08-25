package agentruntime

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/config"
)

type managerSpySandbox struct {
	label       string
	closeCount  int
	commandsRun []string
}

func (s *managerSpySandbox) Command(ctx context.Context, command, cwd string) *exec.Cmd {
	s.commandsRun = append(s.commandsRun, command)
	return exec.CommandContext(ctx, "/bin/sh", "-c", "true")
}

func (s *managerSpySandbox) Label() string { return s.label }

func (s *managerSpySandbox) Close() error {
	s.closeCount++
	return nil
}

func TestSandboxManager_LazilyCreatesAndReusesProfiles(t *testing.T) {
	var calls []string
	created := map[string]*managerSpySandbox{}
	mgr := NewSandboxManager(config.Default().Sandbox, "sess", t.TempDir(), func(ctx context.Context, cfg config.SandboxConfig, id, mountRoot string) (agent.Sandbox, error) {
		calls = append(calls, id+":"+cfg.Image)
		sb := &managerSpySandbox{label: "[" + id + "]"}
		created[id] = sb
		return sb, nil
	})

	h := mgr.Handler().(agent.ProfiledSandbox)
	_ = h.CommandProfile(context.Background(), agent.SandboxProfileDefault, "echo one", t.TempDir())
	_ = h.CommandProfile(context.Background(), agent.SandboxProfileDefault, "echo two", t.TempDir())
	_ = h.CommandProfile(context.Background(), agent.SandboxProfileDocuments, "pandoc --version", t.TempDir())

	if len(calls) != 2 {
		t.Fatalf("constructor calls = %v, want exactly default + documents", calls)
	}
	if !strings.HasPrefix(calls[0], "sess:") {
		t.Errorf("default profile id = %q, want sess", calls[0])
	}
	if !strings.HasPrefix(calls[1], "sess-documents:") {
		t.Errorf("documents profile id = %q, want sess-documents", calls[1])
	}
	if got := len(created["sess"].commandsRun); got != 2 {
		t.Errorf("default command count = %d, want 2", got)
	}
}

func TestSandboxManager_CreationFailureDoesNotFallBackToHost(t *testing.T) {
	mgr := NewSandboxManager(config.Default().Sandbox, "sess", t.TempDir(), func(ctx context.Context, cfg config.SandboxConfig, id, mountRoot string) (agent.Sandbox, error) {
		return nil, assertErr("podman missing")
	})

	cmd := mgr.Command(context.Background(), agent.SandboxProfileDocuments, "echo should-not-run", t.TempDir())
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected failing command for sandbox creation failure")
	}
	text := string(out)
	if !strings.Contains(text, "podman missing") || strings.Contains(text, "should-not-run") {
		t.Fatalf("failure output = %q, want sandbox error and no original command execution", text)
	}
}

func TestSandboxManager_CloseClosesCreatedProfiles(t *testing.T) {
	var sandboxes []*managerSpySandbox
	mgr := NewSandboxManager(config.Default().Sandbox, "sess", t.TempDir(), func(ctx context.Context, cfg config.SandboxConfig, id, mountRoot string) (agent.Sandbox, error) {
		sb := &managerSpySandbox{label: "[" + id + "]"}
		sandboxes = append(sandboxes, sb)
		return sb, nil
	})

	_ = mgr.Command(context.Background(), agent.SandboxProfileDefault, "true", t.TempDir())
	_ = mgr.Command(context.Background(), agent.SandboxProfileDocuments, "true", t.TempDir())
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for i, sb := range sandboxes {
		if sb.closeCount != 1 {
			t.Errorf("sandbox %d closeCount = %d, want 1", i, sb.closeCount)
		}
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
