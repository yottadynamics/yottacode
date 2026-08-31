package sandbox

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// hostCapabilities gates which resource-limit flags podmanRunArgs actually
// includes, based on what this host can honor. Both fields degrade
// gracefully to false rather than NewPodmanSandbox failing outright — see
// storageOptSupported and cgroupLimitsSupported.
type hostCapabilities struct {
	StorageOpt   bool
	CgroupLimits bool
}

// probeTimeout bounds each host-capability probe's real podman subprocess
// calls. Generous relative to detectTimeout's 3s (detect.go) since these
// start and tear down an actual throwaway container rather than a metadata
// lookup, but still bounded so a hung probe can't stall session startup
// indefinitely.
const probeTimeout = 20 * time.Second

var (
	storageOptOnce   sync.Once
	storageOptResult bool

	cgroupLimitsOnce   sync.Once
	cgroupLimitsResult bool
)

// podmanStoreDriver and podmanRunProbe are swapped in tests to avoid real
// podman subprocess calls, mirroring podmanLookPath's seam (podman.go).
// Unit tests exercise probeStorageOptSupported/probeCgroupLimitsSupported
// directly against faked versions of these — never the cached
// storageOptSupported/cgroupLimitsSupported wrappers below, whose
// process-wide sync.Once caching only makes sense against a real host and
// would otherwise get permanently poisoned by a unit test's fake result
// for the rest of the test binary's run (including podman_integration_test.go
// if run in the same invocation).
var podmanStoreDriver = func(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "podman", "info", "--format", "{{.Store.GraphDriverName}}").Output()
	return strings.TrimSpace(string(out)), err
}

var podmanRunProbe = func(ctx context.Context, args ...string) error {
	return exec.CommandContext(ctx, "podman", args...).Run()
}

// storageOptSupported reports whether this host's podman storage driver
// supports --storage-opt size= (a per-container writable-layer disk
// quota). Only overlay on XFS with pquota supports it — most Linux hosts
// run ext4, where the flag errors every container start. Probed once per
// process against image (the same sandbox image about to be used for
// real, so no extra pull) and cached, since the result is host-wide, not
// image-specific. Ported from Hermes Agent's _storage_opt_supported
// (tools/environments/docker.py).
func storageOptSupported(ctx context.Context, image string) bool {
	storageOptOnce.Do(func() {
		storageOptResult = probeStorageOptSupported(ctx, image)
	})
	return storageOptResult
}

// probeStorageOptSupported is storageOptSupported's uncached probe logic,
// split out so unit tests can exercise it directly against faked
// podmanStoreDriver/podmanRunProbe without going through (and poisoning)
// the process-wide cache above.
func probeStorageOptSupported(ctx context.Context, image string) bool {
	if strings.TrimSpace(image) == "" {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	driver, err := podmanStoreDriver(cctx)
	if err != nil || driver != "overlay" {
		return false
	}
	// overlay only supports --storage-opt size= on XFS with pquota. A real
	// throwaway `run --rm` is the fastest reliable check — --storage-opt
	// validation happens at container-create time, which `run` performs
	// internally same as `create` would, and --rm auto-cleans on exit.
	cctx2, cancel2 := context.WithTimeout(ctx, probeTimeout)
	defer cancel2()
	err = podmanRunProbe(cctx2, "run", "--rm", "--storage-opt", "size=1m", image, "sleep", "0")
	return err == nil
}

// cgroupLimitsSupported reports whether this host can honor
// --cpus/--memory/--pids-limit together. On hosts where the corresponding
// cgroup controllers aren't delegated to this process (unprivileged LXCs,
// some nested-container/CI/cloud-VM setups), these flags fail every
// container start. Probed once per process against image (no extra pull)
// and cached. Ported from Hermes Agent's _cgroup_limits_available
// (tools/environments/docker.py).
func cgroupLimitsSupported(ctx context.Context, image string) bool {
	cgroupLimitsOnce.Do(func() {
		cgroupLimitsResult = probeCgroupLimitsSupported(ctx, image)
	})
	return cgroupLimitsResult
}

// probeCgroupLimitsSupported is cgroupLimitsSupported's uncached probe
// logic — see probeStorageOptSupported's doc comment for why this split
// exists.
func probeCgroupLimitsSupported(ctx context.Context, image string) bool {
	if strings.TrimSpace(image) == "" {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	err := podmanRunProbe(cctx, "run", "--rm",
		"--cpus", "0.5", "--memory", "64m", "--pids-limit", "32",
		image, "sleep", "0")
	return err == nil
}
