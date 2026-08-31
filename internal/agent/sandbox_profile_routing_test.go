package agent

import (
	"context"
	"os/exec"
	"testing"
)

type profileRecorderSandbox struct {
	profile SandboxProfile
	command string
}

func (s *profileRecorderSandbox) Command(ctx context.Context, command, cwd string) *exec.Cmd {
	return s.CommandProfile(ctx, SandboxProfileDefault, command, cwd)
}

func (s *profileRecorderSandbox) CommandProfile(ctx context.Context, profile SandboxProfile, command, cwd string) *exec.Cmd {
	s.profile = profile
	s.command = command
	return exec.CommandContext(ctx, "/bin/sh", "-c", "true")
}

func (s *profileRecorderSandbox) Label() string { return s.LabelProfile(SandboxProfileDefault) }

func (s *profileRecorderSandbox) LabelProfile(profile SandboxProfile) string {
	return "[profile-" + string(profile) + "]"
}

func (s *profileRecorderSandbox) Close() error { return nil }

func TestCheckCommandAvailable_UsesDocumentsProfile(t *testing.T) {
	sb := &profileRecorderSandbox{}
	if err := checkCommandAvailable(context.Background(), sb, t.TempDir(), "pandoc"); err != nil {
		t.Fatalf("checkCommandAvailable: %v", err)
	}
	if sb.profile != SandboxProfileDocuments {
		t.Fatalf("profile = %q, want %q", sb.profile, SandboxProfileDocuments)
	}
	if sb.command == "" {
		t.Fatal("expected command to be routed through profiled sandbox")
	}
}

func TestReadDocumentRunSandboxCommand_UsesDocumentsProfile(t *testing.T) {
	sb := &profileRecorderSandbox{}
	tool := &ReadDocumentTool{Cwd: NewCwdRef(t.TempDir()), Sandbox: sb}
	_, _, err := tool.runSandboxCommand(context.Background(), "pdfinfo fixture.pdf")
	if err != nil {
		t.Fatalf("runSandboxCommand: %v", err)
	}
	if sb.profile != SandboxProfileDocuments {
		t.Fatalf("profile = %q, want %q", sb.profile, SandboxProfileDocuments)
	}
}
