package sandboxcache

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceSafeCacheOutsideRepoHome(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("HOME", repo)
	for _, fn := range []struct {
		name string
		call func(string) (string, error)
	}{
		{"go scratch", HostGoScratchDir},
		{"shell scratch", HostShellScratchDir},
		{"sandbox cache", GoHostCacheDirForWorkspace},
	} {
		t.Run(fn.name, func(t *testing.T) {
			got, err := fn.call(repo)
			if err != nil {
				t.Fatal(err)
			}
			if PathWithinWorkspace(got, repo) {
				t.Fatalf("cache %q is inside workspace %q", got, repo)
			}
		})
	}
}

// TestGoHostCacheDirUsesCanonicalHostPath verifies startup and command
// preparation share a HOME-independent path, including when HOME is workspace.
func TestGoHostCacheDirUsesCanonicalHostPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := GoHostCacheDir()
	if err != nil {
		t.Fatalf("GoHostCacheDir: %v", err)
	}
	want := filepath.Join(string(filepath.Separator), "var", "tmp", fmt.Sprintf("yottacode-%d", os.Getuid()), GoCacheHomeSubdir)
	if got != want {
		t.Fatalf("GoHostCacheDir() = %q, want %q", got, want)
	}
	fromWorkspace, err := GoHostCacheDirForWorkspace(home)
	if err != nil || fromWorkspace != got {
		t.Fatalf("GoHostCacheDirForWorkspace() = %q, %v; want %q", fromWorkspace, err, got)
	}
}
