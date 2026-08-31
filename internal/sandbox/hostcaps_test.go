package sandbox

import (
	"context"
	"errors"
	"testing"
)

// These tests exercise the uncached probe* functions directly, never the
// cached storageOptSupported/cgroupLimitsSupported wrappers — see
// hostcaps.go's doc comment on podmanStoreDriver/podmanRunProbe for why.

func fakeStoreDriver(driver string, err error) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return driver, err }
}

func fakeRunProbe(err error) func(context.Context, ...string) error {
	return func(context.Context, ...string) error { return err }
}

func TestProbeStorageOptSupported_OverlayWithPquotaAvailable(t *testing.T) {
	origDriver, origRun := podmanStoreDriver, podmanRunProbe
	defer func() { podmanStoreDriver, podmanRunProbe = origDriver, origRun }()
	podmanStoreDriver = fakeStoreDriver("overlay", nil)
	podmanRunProbe = fakeRunProbe(nil)

	if !probeStorageOptSupported(context.Background(), "some-image") {
		t.Fatal("expected supported when driver is overlay and the dry-run succeeds")
	}
}

func TestProbeStorageOptSupported_OverlayWithoutPquota(t *testing.T) {
	origDriver, origRun := podmanStoreDriver, podmanRunProbe
	defer func() { podmanStoreDriver, podmanRunProbe = origDriver, origRun }()
	podmanStoreDriver = fakeStoreDriver("overlay", nil)
	podmanRunProbe = fakeRunProbe(errors.New("storage option size not supported"))

	if probeStorageOptSupported(context.Background(), "some-image") {
		t.Fatal("expected unsupported when the dry-run --storage-opt create fails")
	}
}

func TestProbeStorageOptSupported_NonOverlayDriver(t *testing.T) {
	origDriver, origRun := podmanStoreDriver, podmanRunProbe
	defer func() { podmanStoreDriver, podmanRunProbe = origDriver, origRun }()
	podmanStoreDriver = fakeStoreDriver("vfs", nil)
	podmanRunProbe = fakeRunProbe(nil) // must not even be consulted

	if probeStorageOptSupported(context.Background(), "some-image") {
		t.Fatal("expected unsupported for a non-overlay storage driver")
	}
}

func TestProbeStorageOptSupported_InfoErrorIsUnsupported(t *testing.T) {
	origDriver, origRun := podmanStoreDriver, podmanRunProbe
	defer func() { podmanStoreDriver, podmanRunProbe = origDriver, origRun }()
	podmanStoreDriver = fakeStoreDriver("", errors.New("podman info failed"))
	podmanRunProbe = fakeRunProbe(nil)

	if probeStorageOptSupported(context.Background(), "some-image") {
		t.Fatal("expected unsupported when podman info itself errors")
	}
}

func TestProbeStorageOptSupported_EmptyImageIsUnsupported(t *testing.T) {
	origDriver, origRun := podmanStoreDriver, podmanRunProbe
	defer func() { podmanStoreDriver, podmanRunProbe = origDriver, origRun }()
	called := false
	podmanStoreDriver = func(context.Context) (string, error) { called = true; return "overlay", nil }
	podmanRunProbe = fakeRunProbe(nil)

	if probeStorageOptSupported(context.Background(), "") {
		t.Fatal("expected unsupported for an empty image")
	}
	if called {
		t.Fatal("expected no podman subprocess calls for an empty image")
	}
}

func TestProbeCgroupLimitsSupported_Available(t *testing.T) {
	orig := podmanRunProbe
	defer func() { podmanRunProbe = orig }()
	podmanRunProbe = fakeRunProbe(nil)

	if !probeCgroupLimitsSupported(context.Background(), "some-image") {
		t.Fatal("expected supported when the throwaway run succeeds")
	}
}

func TestProbeCgroupLimitsSupported_Unavailable(t *testing.T) {
	orig := podmanRunProbe
	defer func() { podmanRunProbe = orig }()
	podmanRunProbe = fakeRunProbe(errors.New("OCI runtime error: cgroup controller not delegated"))

	if probeCgroupLimitsSupported(context.Background(), "some-image") {
		t.Fatal("expected unsupported when the throwaway run fails")
	}
}

func TestProbeCgroupLimitsSupported_EmptyImageIsUnsupported(t *testing.T) {
	orig := podmanRunProbe
	defer func() { podmanRunProbe = orig }()
	called := false
	podmanRunProbe = func(context.Context, ...string) error { called = true; return nil }

	if probeCgroupLimitsSupported(context.Background(), "") {
		t.Fatal("expected unsupported for an empty image")
	}
	if called {
		t.Fatal("expected no podman subprocess calls for an empty image")
	}
}
