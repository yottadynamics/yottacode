package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// pruneGracePeriod is a secondary guard against racing a container that is
// mid-transition through a concurrent Close() call happening right now. It
// is not the primary safety filter — State is (see PruneOrphaned).
const pruneGracePeriod = 10 * time.Minute

// pruneTimeout bounds the `podman ps -a` listing call itself, mirroring
// detectTimeout's role in detect.go: a hung podman can't stall session
// startup.
const pruneTimeout = 3 * time.Second

// podmanPS is swapped in tests to simulate `podman ps -a` output without a
// real podman daemon. Mirrors podmanLookPath's test-seam pattern.
var podmanPS = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "podman", "ps", "-a", "--filter", "name=^yc-", "--format", "json").Output()
}

// prunePSEntry decodes the fields PruneOrphaned needs from `podman ps -a
// --format json`. Verified against a real podman 4.9.3 install: CreatedAt
// is a human-relative string ("2 days ago"), not a timestamp — Created is
// the Unix-seconds field to use instead.
type prunePSEntry struct {
	Names   []string `json:"Names"`
	State   string   `json:"State"`
	Created int64    `json:"Created"`
}

// PruneOrphaned removes yc-* containers left behind by a session or
// dispatch worker that never reached Close() (a crash skips normal
// teardown), or that got stuck mid-teardown (Close's own `podman rm -f`
// interrupted before it finished). NewPodmanSandbox's own leftover sweep
// (see podman.go) only helps when a LATER session reuses the exact same
// deterministic name via --resume; a container from a session that is
// never resumed leaks forever without this. Runs once at session startup,
// best-effort: only the listing call's own error is returned, individual
// removal failures are swallowed.
//
// Safety: only containers NOT in the "running" state are ever removed. A
// container backing a live session or dispatch worker is always "running"
// (its entrypoint is `sleep infinity`) even when idle between commands —
// pruning by age alone would risk killing a different, currently-active
// session's sandbox out from under it. A container that crashed without
// Close() still shows "running" (nothing ever told podman to stop it) and
// is deliberately left alone here; NewPodmanSandbox's pre-create sweep
// reclaims that case instead, if/when the same deterministic name is
// reused via --resume. What PruneOrphaned reclaims is the case that sweep
// cannot: containers stuck "exited"/"stopping" from an interrupted
// teardown. pruneGracePeriod only guards against racing a container that
// is mid-transition through a concurrent Close() call right now.
func PruneOrphaned(ctx context.Context) error {
	if _, err := podmanLookPath("podman"); err != nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, pruneTimeout)
	defer cancel()
	out, err := podmanPS(cctx)
	if err != nil {
		return fmt.Errorf("sandbox: podman ps -a: %w", err)
	}
	entries, err := parsePrunePSOutput(out)
	if err != nil {
		return fmt.Errorf("sandbox: parse podman ps -a output: %w", err)
	}
	for _, name := range selectPruneTargets(entries, time.Now()) {
		_ = removeContainer(context.Background(), name)
	}
	return nil
}

// selectPruneTargets is the pure decision logic behind PruneOrphaned,
// split out so it can be unit tested without a real podman daemon. See
// PruneOrphaned's doc comment for why State, not age alone, is the
// primary safety filter.
func selectPruneTargets(entries []prunePSEntry, now time.Time) []string {
	var names []string
	for _, e := range entries {
		if len(e.Names) == 0 || e.State == "running" {
			continue
		}
		if now.Sub(time.Unix(e.Created, 0)) < pruneGracePeriod {
			continue
		}
		names = append(names, e.Names[0])
	}
	return names
}

// parsePrunePSOutput decodes `podman ps -a --format json`'s array output.
// Empty/whitespace-only output (no error but nothing printed) decodes to
// no entries rather than an error — podman itself prints "[]" for zero
// matches, but this stays defensive against an empty stdout edge case.
func parsePrunePSOutput(out []byte) ([]prunePSEntry, error) {
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var entries []prunePSEntry
	if err := json.Unmarshal(trimmed, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}
