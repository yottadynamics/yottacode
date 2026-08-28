package agentruntime

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/config"
)

type managerSpySandbox struct {
	label       string
	closeCount  int
	mu          sync.Mutex
	commandsRun []string
}

func (s *managerSpySandbox) Command(ctx context.Context, command, cwd string) *exec.Cmd {
	s.mu.Lock()
	s.commandsRun = append(s.commandsRun, command)
	s.mu.Unlock()
	return exec.CommandContext(ctx, "/bin/sh", "-c", "true")
}

func (s *managerSpySandbox) Label() string { return s.label }

func (s *managerSpySandbox) Close() error {
	s.mu.Lock()
	s.closeCount++
	s.mu.Unlock()
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

func TestSandboxManager_CloseDoesNotWaitForSlowProfileCreation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defaultSandbox := &managerSpySandbox{label: "[default]"}
	mgr := NewSandboxManager(config.Default().Sandbox, "sess", t.TempDir(), func(ctx context.Context, cfg config.SandboxConfig, id, mountRoot string) (agent.Sandbox, error) {
		if id == "sess" {
			return defaultSandbox, nil
		}
		close(started)
		<-release
		return &managerSpySandbox{label: "[documents]"}, nil
	})

	_ = mgr.Command(context.Background(), agent.SandboxProfileDefault, "true", t.TempDir())
	done := make(chan struct{})
	go func() {
		_ = mgr.Command(context.Background(), agent.SandboxProfileDocuments, "true", t.TempDir())
		close(done)
	}()
	<-started

	if err := mgr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if defaultSandbox.closeCount != 1 {
		t.Fatalf("default closeCount = %d, want 1", defaultSandbox.closeCount)
	}
	close(release)
	<-done
	if profiles := mgr.LiveProfiles(); len(profiles) != 0 {
		t.Fatalf("LiveProfiles after Close = %v, want none", profiles)
	}
}

func TestSandboxManager_ReloadsConfigBeforeLazyDocumentsProfile(t *testing.T) {
	var calls []string
	cfg := config.Default().Sandbox
	cfg.DocumentsImage = "old-documents-image"
	mgr := NewSandboxManager(cfg, "sess", t.TempDir(), func(ctx context.Context, cfg config.SandboxConfig, id, mountRoot string) (agent.Sandbox, error) {
		calls = append(calls, id+":"+cfg.Image)
		return &managerSpySandbox{label: "[" + id + "]"}, nil
	})
	mgr.SetConfigReloader(func() (config.Config, error) {
		latest := config.Default()
		latest.Sandbox.Backend = "podman"
		latest.Sandbox.DocumentsImage = "fixed-documents-image"
		return latest, nil
	})

	cmd := mgr.Command(context.Background(), agent.SandboxProfileDocuments, "true", t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("documents command failed: %v (%s)", err, out)
	}

	if len(calls) != 1 || calls[0] != "sess-documents:fixed-documents-image" {
		t.Fatalf("constructor calls = %v, want lazy documents profile with reloaded image", calls)
	}
}

func TestSandboxManager_DoesNotReloadAlreadyLiveProfile(t *testing.T) {
	var calls []string
	cfg := config.Default().Sandbox
	cfg.Image = "startup-default-image"
	mgr := NewSandboxManager(cfg, "sess", t.TempDir(), func(ctx context.Context, cfg config.SandboxConfig, id, mountRoot string) (agent.Sandbox, error) {
		calls = append(calls, id+":"+cfg.Image)
		return &managerSpySandbox{label: "[" + id + "]"}, nil
	})
	mgr.SetConfigReloader(func() (config.Config, error) {
		latest := config.Default()
		latest.Sandbox.Backend = "podman"
		latest.Sandbox.Image = "changed-default-image"
		return latest, nil
	})

	for range 2 {
		cmd := mgr.Command(context.Background(), agent.SandboxProfileDefault, "true", t.TempDir())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("default command failed: %v (%s)", err, out)
		}
	}

	if len(calls) != 1 || calls[0] != "sess:changed-default-image" {
		t.Fatalf("constructor calls = %v, want one creation using reloaded startup image", calls)
	}
}

func TestSandboxManager_ReloadBackendChangeFailsClosed(t *testing.T) {
	mgr := NewSandboxManager(config.Default().Sandbox, "sess", t.TempDir(), func(ctx context.Context, cfg config.SandboxConfig, id, mountRoot string) (agent.Sandbox, error) {
		return &managerSpySandbox{label: "[unexpected]"}, nil
	})
	mgr.SetConfigReloader(func() (config.Config, error) { return config.Default(), nil })

	cmd := mgr.Command(context.Background(), agent.SandboxProfileDocuments, "echo should-not-run", t.TempDir())
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "restart yottacode to change sandbox backends") || strings.Contains(string(out), "should-not-run") {
		t.Fatalf("backend change should fail closed without running the command; out=%q err=%v", out, err)
	}
}

func TestSandboxManager_ConcurrentFirstUseCreatesOneProfileContainer(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var constructorCalls atomic.Int32
	mgr := NewSandboxManager(config.Default().Sandbox, "sess", t.TempDir(), func(ctx context.Context, cfg config.SandboxConfig, id, mountRoot string) (agent.Sandbox, error) {
		constructorCalls.Add(1)
		close(started)
		<-release
		return &managerSpySandbox{label: "[" + id + "]"}, nil
	})

	firstDone := make(chan error, 1)
	go func() {
		_, err := mgr.Command(context.Background(), agent.SandboxProfileDocuments, "true", t.TempDir()).CombinedOutput()
		firstDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first constructor did not start")
	}

	secondDone := make(chan error, 1)
	go func() {
		_, err := mgr.Command(context.Background(), agent.SandboxProfileDocuments, "true", t.TempDir()).CombinedOutput()
		secondDone <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if got := constructorCalls.Load(); got != 1 {
		t.Fatalf("constructor calls while first creation in flight = %d, want 1", got)
	}

	close(release)
	for i, ch := range []chan error{firstDone, secondDone} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("command %d failed: %v", i+1, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("command %d did not finish", i+1)
		}
	}
	if got := constructorCalls.Load(); got != 1 {
		t.Fatalf("constructor calls after both commands = %d, want 1", got)
	}
}

func TestSandboxManager_WaitingCallerHonorsContextDuringProfileCreation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	mgr := NewSandboxManager(config.Default().Sandbox, "sess", t.TempDir(), func(ctx context.Context, cfg config.SandboxConfig, id, mountRoot string) (agent.Sandbox, error) {
		close(started)
		<-release
		return &managerSpySandbox{label: "[documents]"}, nil
	})

	firstDone := make(chan error, 1)
	go func() {
		_, err := mgr.Command(context.Background(), agent.SandboxProfileDocuments, "true", t.TempDir()).CombinedOutput()
		firstDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first constructor did not start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan string, 1)
	go func() {
		out, err := mgr.Command(ctx, agent.SandboxProfileDocuments, "true", t.TempDir()).CombinedOutput()
		if err != nil {
			secondDone <- string(out)
			return
		}
		secondDone <- ""
	}()
	cancel()

	select {
	case out := <-secondDone:
		if !strings.Contains(out, context.Canceled.Error()) {
			t.Fatalf("waiting caller output = %q, want context cancellation", out)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting caller stayed blocked after its context was canceled")
	}

	close(release)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first command failed after release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first command did not finish")
	}
}

func TestSandboxHandler_ZeroValueIsSafe(t *testing.T) {
	var h SandboxHandler
	if got := h.Label(); got != (agent.HostSandbox{}).Label() {
		t.Fatalf("zero-value label = %q, want host label", got)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("zero-value Close: %v", err)
	}
	out, err := h.Command(context.Background(), "printf ok", t.TempDir()).CombinedOutput()
	if err != nil || string(out) != "ok" {
		t.Fatalf("zero-value Command output=%q err=%v", out, err)
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
