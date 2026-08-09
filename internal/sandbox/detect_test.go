package sandbox

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/yottadynamics/yottacode/internal/config"
)

func TestDetectStatus_PodmanMissing(t *testing.T) {
	orig := podmanLookPath
	defer func() { podmanLookPath = orig }()
	podmanLookPath = func(string) (string, error) { return "", errors.New("not found") }

	st := DetectStatus(context.Background(), config.DefaultSandboxImage)
	if st.Installed {
		t.Error("Installed should be false when podman is not on PATH")
	}
	if st.ImagePresent {
		t.Error("ImagePresent should be false when podman is not on PATH")
	}
}

func TestDetectStatus_EmptyImageSkipsImageCheck(t *testing.T) {
	orig := podmanLookPath
	defer func() { podmanLookPath = orig }()
	podmanLookPath = func(string) (string, error) { return "/usr/bin/podman", nil }

	st := DetectStatus(context.Background(), "")
	if !st.Installed {
		t.Error("Installed should be true")
	}
	if st.ImagePresent {
		t.Error("ImagePresent should be false when no image was checked")
	}
}

// TestDetectStatus_RealPodman is a light real-podman check — it's not
// gated behind YOTTACODE_SANDBOX_E2E like the container-lifecycle tests
// (no container is started; both podman calls are fast, local, read-only
// lookups), just skipped when podman isn't installed.
func TestDetectStatus_RealPodman(t *testing.T) {
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman not available in PATH")
	}
	st := DetectStatus(context.Background(), "docker.io/library/yottacode-detect-test-definitely-missing:latest")
	if !st.Installed {
		t.Error("Installed should be true — podman is on PATH")
	}
	if st.ImagePresent {
		t.Error("ImagePresent should be false for a made-up image name")
	}
}
