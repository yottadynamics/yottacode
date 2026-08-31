package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func workflowText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", path))
	if err != nil {
		t.Fatalf("read workflow: %v", err)
	}
	return string(data)
}

func TestSandboxImageWorkflowPublishesReleaseTaggedPackage(t *testing.T) {
	text := workflowText(t, ".github/workflows/sandbox-image.yml")
	for _, want := range []string{
		"tags: ['v*']",
		"GITHUB_REF_NAME",
		"${IMAGE_NAME}:${TAG_NAME}",
		"podman pull \"${IMAGE_NAME}:latest\"",
		"scripts/smoke/sandbox-image.sh",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("sandbox image workflow missing %q", want)
		}
	}
}

func TestDocumentsImageWorkflowTracksHelpersAndVerifiesPull(t *testing.T) {
	text := workflowText(t, ".github/workflows/documents-image.yml")
	for _, want := range []string{
		"tags: ['v*']",
		"internal/documents/pyhelpers/**",
		"GITHUB_REF_NAME",
		"${IMAGE_NAME}:${TAG_NAME}",
		"podman pull \"${IMAGE_NAME}:latest\"",
		"scripts/smoke/documents-image.sh",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("documents image workflow missing %q", want)
		}
	}
}
