package sandbox

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestSelectPruneTargets_SkipsUnlabeledRunning covers a container with no
// yottacode.owner_pid label — created before that labeling existed, or by
// something other than yottacode. ownerAlive can't identify it as
// abandoned, so it must be left alone exactly as before this check existed.
func TestSelectPruneTargets_SkipsUnlabeledRunning(t *testing.T) {
	now := time.Now()
	old := now.Add(-1 * time.Hour).Unix()
	entries := []prunePSEntry{
		{Names: []string{"yc-live-session"}, State: "running", Created: old},
	}
	got := selectPruneTargets(entries, now)
	if len(got) != 0 {
		t.Fatalf("selectPruneTargets = %v, want empty (unlabeled running container must never be pruned)", got)
	}
}

// TestSelectPruneTargets_SkipsRunningWithLiveOwner is the core safety
// case: a "running" container whose owning process is still alive must
// never be pruned, no matter its age — this is what protects a genuinely
// long-idle-but-active session.
func TestSelectPruneTargets_SkipsRunningWithLiveOwner(t *testing.T) {
	origAlive := processAlive
	defer func() { processAlive = origAlive }()
	processAlive = func(pid int, startTicks int64, haveStartTicks bool) bool { return true }

	now := time.Now()
	old := now.Add(-30 * time.Hour).Unix()
	entries := []prunePSEntry{{
		Names:   []string{"yc-live-session"},
		State:   "running",
		Created: old,
		Labels:  map[string]string{"yottacode.owner_pid": "4242", "yottacode.owner_started": "12345"},
	}}
	got := selectPruneTargets(entries, now)
	if len(got) != 0 {
		t.Fatalf("selectPruneTargets = %v, want empty (owner still alive)", got)
	}
}

// TestSelectPruneTargets_PrunesRunningWithDeadOwner is the actual fix:
// a "running" container whose owning yottacode process is confirmed gone
// (SIGKILL, SIGHUP from a closed terminal, a crash — anything that skips
// Close()) is no longer left to accumulate forever.
func TestSelectPruneTargets_PrunesRunningWithDeadOwner(t *testing.T) {
	origAlive := processAlive
	defer func() { processAlive = origAlive }()
	processAlive = func(pid int, startTicks int64, haveStartTicks bool) bool { return false }

	now := time.Now()
	old := now.Add(-23 * time.Hour).Unix()
	entries := []prunePSEntry{{
		Names:   []string{"yc-20260831-141822.524464"},
		State:   "running",
		Created: old,
		Labels:  map[string]string{"yottacode.owner_pid": "4242", "yottacode.owner_started": "12345"},
	}}
	got := selectPruneTargets(entries, now)
	if len(got) != 1 || got[0] != "yc-20260831-141822.524464" {
		t.Fatalf("selectPruneTargets = %v, want the dead-owner container pruned", got)
	}
}

// TestSelectPruneTargets_RunningWithDeadOwnerRespectsGracePeriod: even a
// confirmed-dead owner doesn't bypass pruneGracePeriod — guards against
// racing a container that is mid-transition through a concurrent Close()
// call, or whose owner label hasn't propagated to `podman ps` output yet.
func TestSelectPruneTargets_RunningWithDeadOwnerRespectsGracePeriod(t *testing.T) {
	origAlive := processAlive
	defer func() { processAlive = origAlive }()
	processAlive = func(pid int, startTicks int64, haveStartTicks bool) bool { return false }

	now := time.Now()
	recent := now.Add(-1 * time.Minute).Unix()
	entries := []prunePSEntry{{
		Names:   []string{"yc-just-created"},
		State:   "running",
		Created: recent,
		Labels:  map[string]string{"yottacode.owner_pid": "4242"},
	}}
	got := selectPruneTargets(entries, now)
	if len(got) != 0 {
		t.Fatalf("selectPruneTargets = %v, want empty (within grace period)", got)
	}
}

func TestOwnerAlive_MissingLabelDefaultsTrue(t *testing.T) {
	if !ownerAlive(nil) {
		t.Error("ownerAlive(nil) = false, want true (no label means can't identify as abandoned)")
	}
	if !ownerAlive(map[string]string{}) {
		t.Error("ownerAlive({}) = false, want true")
	}
}

func TestOwnerAlive_UnparsablePidDefaultsTrue(t *testing.T) {
	if !ownerAlive(map[string]string{"yottacode.owner_pid": "not-a-pid"}) {
		t.Error("ownerAlive with unparsable pid = false, want true")
	}
}

func TestOwnerAlive_DelegatesToProcessAlive(t *testing.T) {
	origAlive := processAlive
	defer func() { processAlive = origAlive }()

	var gotPID int
	var gotTicks int64
	var gotHave bool
	processAlive = func(pid int, startTicks int64, haveStartTicks bool) bool {
		gotPID, gotTicks, gotHave = pid, startTicks, haveStartTicks
		return true
	}
	ownerAlive(map[string]string{"yottacode.owner_pid": "777", "yottacode.owner_started": "999"})
	if gotPID != 777 || gotTicks != 999 || !gotHave {
		t.Errorf("ownerAlive did not forward parsed label values, got pid=%d ticks=%d have=%v", gotPID, gotTicks, gotHave)
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

// TestSelectPruneSecretTargets_SkipsUnlabeled mirrors
// TestSelectPruneTargets_SkipsUnlabeledRunning: a secret with no owner
// label (created before this labeling existed, or by something other than
// yottacode) can't be identified as abandoned, so it must be left alone.
func TestSelectPruneSecretTargets_SkipsUnlabeled(t *testing.T) {
	now := time.Now()
	old := now.Add(-1 * time.Hour).Format(time.RFC3339Nano)
	entries := []secretInspectEntry{{CreatedAt: old, Spec: secretSpecInfo{Name: "yc-session-secret-GITHUB_TOKEN"}}}
	got := selectPruneSecretTargets(entries, now)
	if len(got) != 0 {
		t.Fatalf("selectPruneSecretTargets = %v, want empty (unlabeled secret must never be pruned)", got)
	}
}

func TestSelectPruneSecretTargets_SkipsLiveOwner(t *testing.T) {
	origAlive := processAlive
	defer func() { processAlive = origAlive }()
	processAlive = func(pid int, startTicks int64, haveStartTicks bool) bool { return true }

	now := time.Now()
	old := now.Add(-30 * time.Hour).Format(time.RFC3339Nano)
	entries := []secretInspectEntry{{CreatedAt: old, Spec: secretSpecInfo{Name: "yc-session-secret-GITHUB_TOKEN", Labels: map[string]string{"yottacode.owner_pid": "4242"}}}}
	got := selectPruneSecretTargets(entries, now)
	if len(got) != 0 {
		t.Fatalf("selectPruneSecretTargets = %v, want empty (owner still alive)", got)
	}
}

func TestSelectPruneSecretTargets_PrunesDeadOwner(t *testing.T) {
	origAlive := processAlive
	defer func() { processAlive = origAlive }()
	processAlive = func(pid int, startTicks int64, haveStartTicks bool) bool { return false }

	now := time.Now()
	old := now.Add(-30 * time.Hour).Format(time.RFC3339Nano)
	entries := []secretInspectEntry{{CreatedAt: old, Spec: secretSpecInfo{Name: "yc-session-secret-GITHUB_TOKEN", Labels: map[string]string{"yottacode.owner_pid": "4242"}}}}
	got := selectPruneSecretTargets(entries, now)
	if len(got) != 1 || got[0] != "yc-session-secret-GITHUB_TOKEN" {
		t.Fatalf("selectPruneSecretTargets = %v, want the dead-owner secret pruned", got)
	}
}

func TestSelectPruneSecretTargets_RespectsGracePeriod(t *testing.T) {
	origAlive := processAlive
	defer func() { processAlive = origAlive }()
	processAlive = func(pid int, startTicks int64, haveStartTicks bool) bool { return false }

	now := time.Now()
	recent := now.Add(-1 * time.Minute).Format(time.RFC3339Nano)
	entries := []secretInspectEntry{{CreatedAt: recent, Spec: secretSpecInfo{Name: "yc-just-created-secret-GITHUB_TOKEN", Labels: map[string]string{"yottacode.owner_pid": "4242"}}}}
	got := selectPruneSecretTargets(entries, now)
	if len(got) != 0 {
		t.Fatalf("selectPruneSecretTargets = %v, want empty (within grace period)", got)
	}
}

func TestSelectPruneSecretTargets_SkipsUnparsableCreatedAt(t *testing.T) {
	entries := []secretInspectEntry{{CreatedAt: "not-a-timestamp", Spec: secretSpecInfo{Name: "yc-weird-secret-X"}}}
	got := selectPruneSecretTargets(entries, time.Now())
	if len(got) != 0 {
		t.Fatalf("selectPruneSecretTargets = %v, want empty for unparsable CreatedAt", got)
	}
}

// TestParseSecretInspectOutput_RealPodmanShape decodes a trimmed fixture
// captured from an actual `podman secret inspect` run (podman 4.9.3):
// CreatedAt is RFC3339 (unlike podman ps -a's Unix-seconds Created), and
// Name/Labels live under Spec, not at the top level.
func TestParseSecretInspectOutput_RealPodmanShape(t *testing.T) {
	const fixture = `[
  {
    "ID": "e7b96236719fceb7b26fae82d",
    "CreatedAt": "2026-09-21T21:18:43.34020319-04:00",
    "UpdatedAt": "2026-09-21T21:18:43.354971035-04:00",
    "Spec": {
      "Name": "yc-e2e-test-session-secret-GITHUB_TOKEN",
      "Driver": {"Name": "file", "Options": {"path": "/home/user/.local/share/containers/storage/secrets/filedriver"}},
      "Labels": {"yottacode.owner_pid": "12345", "yottacode.owner_started": "999"}
    }
  }
]`
	entries, err := parseSecretInspectOutput([]byte(fixture))
	if err != nil {
		t.Fatalf("parseSecretInspectOutput: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Spec.Name != "yc-e2e-test-session-secret-GITHUB_TOKEN" {
		t.Errorf("Spec.Name = %q, want yc-e2e-test-session-secret-GITHUB_TOKEN", e.Spec.Name)
	}
	if e.Spec.Labels["yottacode.owner_pid"] != "12345" {
		t.Errorf("Spec.Labels[owner_pid] = %q, want 12345", e.Spec.Labels["yottacode.owner_pid"])
	}
	if e.CreatedAt != "2026-09-21T21:18:43.34020319-04:00" {
		t.Errorf("CreatedAt = %q, unexpected", e.CreatedAt)
	}
}

func TestParseSecretInspectOutput_EmptyBytes(t *testing.T) {
	entries, err := parseSecretInspectOutput(nil)
	if err != nil {
		t.Fatalf("parseSecretInspectOutput: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %v, want empty", entries)
	}
}

func TestPruneOrphanedSecrets_NoPodmanReturnsNilError(t *testing.T) {
	origLookPath := podmanLookPath
	defer func() { podmanLookPath = origLookPath }()
	podmanLookPath = func(string) (string, error) { return "", errors.New("no such file") }

	if err := PruneOrphanedSecrets(context.Background()); err != nil {
		t.Fatalf("PruneOrphanedSecrets = %v, want nil when podman is not on PATH", err)
	}
}

func TestPruneOrphanedSecrets_LSErrorIsWrapped(t *testing.T) {
	origLookPath := podmanLookPath
	origLS := podmanSecretLS
	defer func() {
		podmanLookPath = origLookPath
		podmanSecretLS = origLS
	}()
	podmanLookPath = func(string) (string, error) { return "/usr/bin/podman", nil }
	podmanSecretLS = func(context.Context) ([]byte, error) { return nil, errors.New("boom") }

	if err := PruneOrphanedSecrets(context.Background()); err == nil {
		t.Fatal("PruneOrphanedSecrets = nil, want an error when podman secret ls fails")
	}
}

func TestPruneOrphanedSecrets_NoMatchesSkipsInspect(t *testing.T) {
	origLookPath := podmanLookPath
	origLS := podmanSecretLS
	origInspect := podmanSecretInspect
	defer func() {
		podmanLookPath = origLookPath
		podmanSecretLS = origLS
		podmanSecretInspect = origInspect
	}()
	podmanLookPath = func(string) (string, error) { return "/usr/bin/podman", nil }
	podmanSecretLS = func(context.Context) ([]byte, error) { return []byte(""), nil }
	podmanSecretInspect = func(context.Context, []string) ([]byte, error) {
		t.Fatal("podman secret inspect must not be called when ls found nothing")
		return nil, nil
	}

	if err := PruneOrphanedSecrets(context.Background()); err != nil {
		t.Fatalf("PruneOrphanedSecrets = %v, want nil", err)
	}
}
