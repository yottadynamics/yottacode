package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// secretOpTimeout bounds each individual `podman secret create`/`rm` call —
// mirrors containerCloseTimeout's role for removeContainer.
const secretOpTimeout = 10 * time.Second

// secretMount pairs a podman secret name with the in-container env var name
// it stands in for (the secret's mount `target`). podmanRunArgs stays a
// pure, easily-tested argv builder by taking these as an already-resolved
// list rather than doing the `podman secret create` side effect itself —
// see NewPodmanSandbox and createSessionSecrets.
type secretMount struct {
	SecretName string
	EnvName    string
}

// podmanSecretRM and podmanSecretCreate are swapped in tests to simulate
// podman secret operations without a real podman daemon — mirrors
// podmanLookPath's seam (see podman.go).
var podmanSecretRM = func(ctx context.Context, name string) error {
	return exec.CommandContext(ctx, "podman", "secret", "rm", name).Run()
}

var podmanSecretCreate = func(ctx context.Context, name, value string, labels []string) error {
	args := []string{"secret", "create"}
	for _, l := range labels {
		args = append(args, "--label", l)
	}
	args = append(args, name, "-")
	cmd := exec.CommandContext(ctx, "podman", args...)
	cmd.Stdin = strings.NewReader(value)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("podman secret create %s: %w (output: %s)", name, err, strings.TrimSpace(out.String()))
	}
	return nil
}

// createSessionSecrets resolves each name in envNames from yottacode's own
// process environment. Names that are unset are skipped; an explicitly set
// empty value is preserved as an empty secret so presence semantics match the
// previous bare `-e NAME` behavior. envNames is assumed already validated
// (see config.validEnvVarName): this is the one place a name gets
// interpolated into a value an attacker doesn't control (the podman secret
// name), so a defensive re-check here would be redundant, not additional
// safety — the actual injection-sensitive use is Command's shell
// interpolation of EnvName, guarded by secretExportPrelude's own check.
//
// A creation failure partway through is fatal: unlike hostCapabilities'
// resource limits, a credential the user explicitly configured is not
// something to silently drop, so any secrets already created in this call
// are rolled back and the error surfaces to NewPodmanSandbox's caller.
func createSessionSecrets(ctx context.Context, containerName string, envNames []string, owner sandboxOwner) ([]secretMount, error) {
	labels := ownerLabels(owner)
	var mounts []secretMount
	for _, name := range envNames {
		value, ok := os.LookupEnv(name)
		if !ok {
			continue
		}
		secretName := containerName + "-secret-" + name
		// Bound the best-effort replacement sweep: a stuck Podman
		// operation must not block sandbox startup indefinitely.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), secretOpTimeout)
		_ = podmanSecretRM(cleanupCtx, secretName)
		cancel()
		if err := podmanSecretCreate(ctx, secretName, value, labels); err != nil {
			removeSecrets(context.Background(), mounts)
			return nil, fmt.Errorf("sandbox: %w", err)
		}
		mounts = append(mounts, secretMount{SecretName: secretName, EnvName: name})
	}
	return mounts, nil
}

// removeSecrets is createSessionSecrets'/Close's best-effort teardown
// counterpart, mirroring removeContainer's posture: callers swallow the
// result, consistent with this package's existing best-effort cleanup
// convention.
func removeSecrets(ctx context.Context, mounts []secretMount) {
	for _, m := range mounts {
		cctx, cancel := context.WithTimeout(ctx, secretOpTimeout)
		_ = podmanSecretRM(cctx, m.SecretName)
		cancel()
	}
}

func secretMountNames(mounts []secretMount) []string {
	names := make([]string, len(mounts))
	for i, m := range mounts {
		names[i] = m.SecretName
	}
	return names
}

// secretExportPrelude builds the shell snippet Command prepends to every
// wrapped script, re-exposing each mounted secret as the env var name it
// stands in for. podman mounts a secret at /run/secrets/<target> read-only
// inside the container; unlike a container-level `-e NAME=value` (baked
// into the container's own config for its whole life and inherited by
// every exec automatically), a secret file has to be re-read into the
// shell's own environment on each exec — there is no container-wide env to
// inherit it from.
//
// EnvName is safe to interpolate directly: both call sites that populate
// it (config.Validate's validEnvVarName check, enforced before a session
// ever builds a sandbox, and createSessionSecrets which only ever copies
// names through unchanged) restrict it to a bare POSIX env var name
// ([A-Za-z_][A-Za-z0-9_]*) — the same bar this package already holds
// itself to for the marker text in Command.
func secretExportPrelude(mounts []secretMount) string {
	var b strings.Builder
	for _, m := range mounts {
		fmt.Fprintf(&b, "export %s=\"$(cat /run/secrets/%s 2>/dev/null)\"\n", m.EnvName, m.EnvName)
	}
	return b.String()
}
