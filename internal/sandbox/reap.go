package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
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
// the Unix-seconds field to use instead. Labels carries the
// yottacode.owner_pid/yottacode.owner_started pair podmanRunArgs stamps at
// creation (see podman.go's sandboxOwner) — nil/missing on a container
// created before this labeling existed, or by something other than
// yottacode.
type prunePSEntry struct {
	Names   []string          `json:"Names"`
	State   string            `json:"State"`
	Created int64             `json:"Created"`
	Labels  map[string]string `json:"Labels"`
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
// Safety: container State is the primary filter, but "running" is no
// longer an unconditional skip. A container backing a live session or
// dispatch worker is always "running" (its entrypoint is `sleep infinity`)
// even when idle between commands, so State alone can't distinguish
// "genuinely still in use" from "abandoned, nothing ever told it to stop"
// — SIGHUP (closed terminal, dropped SSH), SIGKILL (force-kill, the
// process itself becoming an OOM-kill target), or a crash that skips
// Close() all leave a container "running" forever with no session left to
// use it. ownerAlive resolves this by checking whether the process that
// created the container (recorded as podman labels, see podman.go's
// sandboxOwner) is still alive: a "running" container is only pruned once
// its owner is confirmed gone. A container with no owner label (created
// before this labeling existed, or by something other than yottacode) is
// left alone, same as before this check existed. pruneGracePeriod guards
// every prune target — running-with-dead-owner included — against racing
// a container that is mid-transition through a concurrent Close() call
// right now, or one whose owner label hasn't propagated to `podman ps`
// output yet immediately after creation.
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
// primary safety filter, and why "running" is no longer an unconditional
// skip.
func selectPruneTargets(entries []prunePSEntry, now time.Time) []string {
	var names []string
	for _, e := range entries {
		if len(e.Names) == 0 {
			continue
		}
		if now.Sub(time.Unix(e.Created, 0)) < pruneGracePeriod {
			continue
		}
		if e.State == "running" && ownerAlive(e.Labels) {
			continue
		}
		names = append(names, e.Names[0])
	}
	return names
}

// ownerAlive reports whether the process that created a "running"
// container (per its yottacode.owner_pid/yottacode.owner_started labels —
// see podman.go's sandboxOwner) still appears to be alive. Missing or
// unparsable labels resolve to true — "assume alive, leave it alone" is
// the safe default for a container this code can't positively identify as
// abandoned, matching this file's existing safe-by-default posture.
func ownerAlive(labels map[string]string) bool {
	pidStr, ok := labels["yottacode.owner_pid"]
	if !ok {
		return true
	}
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return true
	}
	var startTicks int64
	haveStartTicks := false
	if s, ok := labels["yottacode.owner_started"]; ok {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			startTicks = v
			haveStartTicks = true
		}
	}
	return processAlive(pid, startTicks, haveStartTicks)
}

// processAlive checks whether pid currently exists and, when startTicks is
// known, still matches the process that owned it when the label was
// written — guarding against a since-terminated PID being coincidentally
// reused by an unrelated process. Swapped in tests to avoid depending on
// real OS process state.
var processAlive = func(pid int, startTicks int64, haveStartTicks bool) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess always succeeds regardless of whether pid
	// exists; Signal(0) is the actual existence probe — it changes no
	// process state, and returns an error (typically ESRCH, or EPERM for
	// a live-but-inaccessible process, which this still counts as "alive"
	// rather than risk pruning a container it can't fully verify) when
	// no such process exists.
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return errors.Is(err, syscall.EPERM)
	}
	if !haveStartTicks {
		return true
	}
	current, ok := processStartTicks(pid)
	if !ok {
		// Can't verify further (e.g. non-Linux, or a raced /proc read) —
		// trust the existence check rather than falsely calling a live
		// process dead.
		return true
	}
	return current == startTicks
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

// podmanSecretLS lists the IDs of secrets this package could have created
// (see createSessionSecrets' containerName+"-secret-"+envName naming) — `-q`
// with a name filter rather than `ls --format json`: unlike `podman ps`,
// `podman secret ls --format` only supports a Go-template string, not the
// "json" keyword (verified against a real podman 4.9.3 install — passing
// "json" as the template just prints the literal word once). Swapped in
// tests, mirroring podmanPS's seam.
var podmanSecretLS = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "podman", "secret", "ls", "-q", "--filter", "name=^yc-.*-secret-").Output()
}

// podmanSecretInspect fetches full metadata (CreatedAt, owner Labels) for
// the given secret IDs in one call — `podman secret ls` itself doesn't
// expose Labels through its template context. Swapped in tests.
var podmanSecretInspect = func(ctx context.Context, ids []string) ([]byte, error) {
	args := append([]string{"secret", "inspect"}, ids...)
	return exec.CommandContext(ctx, "podman", args...).Output()
}

// secretInspectEntry decodes the fields PruneOrphanedSecrets needs from
// `podman secret inspect`. Verified against a real podman 4.9.3 install:
// CreatedAt is RFC3339 (unlike podman ps -a's Unix-seconds Created), and
// Labels lives under Spec, not at the top level.
type secretInspectEntry struct {
	CreatedAt string         `json:"CreatedAt"`
	Spec      secretSpecInfo `json:"Spec"`
}

type secretSpecInfo struct {
	Name   string            `json:"Name"`
	Labels map[string]string `json:"Labels"`
}

// PruneOrphanedSecrets is createSessionSecrets' counterpart to
// PruneOrphaned: a session that crashes (or is killed) between creating a
// secret and either mounting it into a container or reaching Close()
// leaves it behind forever without this. Unlike a container, a secret has
// no "running" state to muddy the safety check — ownerAlive (see
// PruneOrphaned's doc comment) is the whole filter, gated by the same
// pruneGracePeriod. Runs once at session startup, best-effort: only the
// listing call's own error is returned, individual removal failures are
// swallowed.
func PruneOrphanedSecrets(ctx context.Context) error {
	if _, err := podmanLookPath("podman"); err != nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, pruneTimeout)
	defer cancel()
	idsOut, err := podmanSecretLS(cctx)
	if err != nil {
		return fmt.Errorf("sandbox: podman secret ls: %w", err)
	}
	ids := strings.Fields(string(idsOut))
	if len(ids) == 0 {
		return nil
	}
	detailsOut, err := podmanSecretInspect(cctx, ids)
	if err != nil {
		return fmt.Errorf("sandbox: podman secret inspect: %w", err)
	}
	entries, err := parseSecretInspectOutput(detailsOut)
	if err != nil {
		return fmt.Errorf("sandbox: parse podman secret inspect output: %w", err)
	}
	for _, name := range selectPruneSecretTargets(entries, time.Now()) {
		_ = podmanSecretRM(context.Background(), name)
	}
	return nil
}

// selectPruneSecretTargets is PruneOrphanedSecrets' pure decision logic,
// split out for the same reason selectPruneTargets is.
func selectPruneSecretTargets(entries []secretInspectEntry, now time.Time) []string {
	var names []string
	for _, e := range entries {
		if e.Spec.Name == "" {
			continue
		}
		created, err := time.Parse(time.RFC3339Nano, e.CreatedAt)
		if err != nil {
			continue
		}
		if now.Sub(created) < pruneGracePeriod {
			continue
		}
		if ownerAlive(e.Spec.Labels) {
			continue
		}
		names = append(names, e.Spec.Name)
	}
	return names
}

func parseSecretInspectOutput(out []byte) ([]secretInspectEntry, error) {
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var entries []secretInspectEntry
	if err := json.Unmarshal(trimmed, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}
