package sandbox

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSelectPruneTargets_SkipsRunning(t *testing.T) {
	now := time.Now()
	old := now.Add(-1 * time.Hour).Unix()
	entries := []prunePSEntry{
		{Names: []string{"yc-live-session"}, State: "running", Created: old},
	}
	got := selectPruneTargets(entries, now)
	if len(got) != 0 {
		t.Fatalf("selectPruneTargets = %v, want empty (running container must never be pruned)", got)
	}
}

func TestSelectPruneTargets_SkipsYoungNonRunning(t *testing.T) {
	now := time.Now()
	recent := now.Add(-1 * time.Minute).Unix()
	entries := []prunePSEntry{
		{Names: []string{"yc-just-stopped"}, State: "stopping", Created: recent},
	}
	got := selectPruneTargets(entries, now)
	if len(got) != 0 {
		t.Fatalf("selectPruneTargets = %v, want empty (within grace period)", got)
	}
}

func TestSelectPruneTargets_IncludesOldNonRunning(t *testing.T) {
	now := time.Now()
	old := now.Add(-24 * time.Hour).Unix()
	entries := []prunePSEntry{
		{Names: []string{"yc-20260829-032855.016911"}, State: "stopping", Created: old},
		{Names: []string{"yc-20260830-001735.388517"}, State: "exited", Created: old},
	}
	got := selectPruneTargets(entries, now)
	if len(got) != 2 {
		t.Fatalf("selectPruneTargets = %v, want both stale non-running containers", got)
	}
}

func TestSelectPruneTargets_SkipsEmptyNames(t *testing.T) {
	now := time.Now()
	old := now.Add(-24 * time.Hour).Unix()
	entries := []prunePSEntry{
		{Names: nil, State: "exited", Created: old},
	}
	got := selectPruneTargets(entries, now)
	if len(got) != 0 {
		t.Fatalf("selectPruneTargets = %v, want empty for a nameless entry", got)
	}
}

// TestParsePrunePSOutput_RealPodmanShape decodes a trimmed fixture captured
// from an actual `podman ps -a --filter name=^yc- --format json` run
// (podman 4.9.3) — CreatedAt is a human-relative string ("2 days ago"), not
// parsed by this code; Created is the Unix-seconds field actually used.
func TestParsePrunePSOutput_RealPodmanShape(t *testing.T) {
	const fixture = `[
  {
    "AutoRemove": false,
    "Command": ["sleep", "infinity"],
    "CreatedAt": "2 days ago",
    "Exited": false,
    "ExitedAt": -62135596800,
    "ExitCode": 0,
    "Id": "ba5cb524f84349c5a114e2a9625d40b6f3b4720afb0db00d7685641d4d108c39",
    "Image": "ghcr.io/yottadynamics/yottacode-sandbox:latest",
    "Names": ["yc-20260829-032855.016911"],
    "State": "stopping",
    "Status": "Stopping",
    "Created": 1787974157
  }
]`
	entries, err := parsePrunePSOutput([]byte(fixture))
	if err != nil {
		t.Fatalf("parsePrunePSOutput: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	e := entries[0]
	if len(e.Names) != 1 || e.Names[0] != "yc-20260829-032855.016911" {
		t.Errorf("Names = %v, want [yc-20260829-032855.016911]", e.Names)
	}
	if e.State != "stopping" {
		t.Errorf("State = %q, want stopping", e.State)
	}
	if e.Created != 1787974157 {
		t.Errorf("Created = %d, want 1787974157", e.Created)
	}
}

func TestParsePrunePSOutput_EmptyArray(t *testing.T) {
	entries, err := parsePrunePSOutput([]byte("[]"))
	if err != nil {
		t.Fatalf("parsePrunePSOutput: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %v, want empty", entries)
	}
}

func TestParsePrunePSOutput_EmptyBytes(t *testing.T) {
	entries, err := parsePrunePSOutput(nil)
	if err != nil {
		t.Fatalf("parsePrunePSOutput: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %v, want empty", entries)
	}
}

func TestPruneOrphaned_NoPodmanReturnsNilError(t *testing.T) {
	origLookPath := podmanLookPath
	defer func() { podmanLookPath = origLookPath }()
	podmanLookPath = func(string) (string, error) { return "", errors.New("no such file") }

	if err := PruneOrphaned(context.Background()); err != nil {
		t.Fatalf("PruneOrphaned = %v, want nil when podman is not on PATH", err)
	}
}

func TestPruneOrphaned_PSErrorIsWrapped(t *testing.T) {
	origLookPath := podmanLookPath
	origPS := podmanPS
	defer func() {
		podmanLookPath = origLookPath
		podmanPS = origPS
	}()
	podmanLookPath = func(string) (string, error) { return "/usr/bin/podman", nil }
	podmanPS = func(context.Context) ([]byte, error) { return nil, errors.New("boom") }

	err := PruneOrphaned(context.Background())
	if err == nil {
		t.Fatal("PruneOrphaned = nil, want an error when podman ps -a fails")
	}
}

func TestPruneOrphaned_NoStaleContainersIsNoop(t *testing.T) {
	origLookPath := podmanLookPath
	origPS := podmanPS
	defer func() {
		podmanLookPath = origLookPath
		podmanPS = origPS
	}()
	podmanLookPath = func(string) (string, error) { return "/usr/bin/podman", nil }
	podmanPS = func(context.Context) ([]byte, error) { return []byte("[]"), nil }

	if err := PruneOrphaned(context.Background()); err != nil {
		t.Fatalf("PruneOrphaned = %v, want nil", err)
	}
}
