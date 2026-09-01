package sandboxcache

import (
	"path/filepath"
	"testing"
)

// TestGoHostCacheDirUsesYottacodeHome verifies the shared cache helper stays
// rooted in ~/.yottacode, which is the path both podman startup and run_tests
// command preparation must agree on for cache persistence.
func TestGoHostCacheDirUsesYottacodeHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := GoHostCacheDir()
	if err != nil {
		t.Fatalf("GoHostCacheDir: %v", err)
	}
	want := filepath.Join(home, ".yottacode", GoCacheHomeSubdir)
	if got != want {
		t.Fatalf("GoHostCacheDir() = %q, want %q", got, want)
	}
}
