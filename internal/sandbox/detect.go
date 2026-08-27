package sandbox

import (
	"context"
	"os/exec"
	"time"
)

// Status reports whether podman is usable and whether the configured base
// image is already present locally. Surfaced by the TUI /sandbox picker so
// a missing podman or unpulled image shows up as a warning there, instead
// of only failing later when NewPodmanSandbox runs at session start.
type Status struct {
	Installed             bool
	ImagePresent          bool // only meaningful when Installed
	DocumentsImagePresent bool // only meaningful when Installed
}

// detectTimeout bounds both checks so a slow or hung podman can't stall
// the TUI when a picker opens.
const detectTimeout = 3 * time.Second

// DetectStatus checks podman availability and whether image is already
// pulled locally. Never triggers a pull or touches the network —
// `podman image exists` is a local metadata lookup.
func DetectStatus(ctx context.Context, image string, documentsImage ...string) Status {
	if _, err := podmanLookPath("podman"); err != nil {
		return Status{}
	}
	st := Status{Installed: true}
	if image != "" {
		st.ImagePresent = imageExists(ctx, image)
	}
	if len(documentsImage) > 0 && documentsImage[0] != "" {
		st.DocumentsImagePresent = imageExists(ctx, documentsImage[0])
	}
	return st
}

func imageExists(ctx context.Context, image string) bool {
	cctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()
	return exec.CommandContext(cctx, "podman", "image", "exists", image).Run() == nil
}
