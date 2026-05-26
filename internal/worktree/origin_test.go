package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOriginRoundtrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := "/home/me/work/myproj"
	slug := SlugDir(repo)

	if err := WriteOrigin(slug, repo); err != nil {
		t.Fatalf("WriteOrigin: %v", err)
	}
	got, err := ReadOrigin(slug)
	if err != nil {
		t.Fatalf("ReadOrigin: %v", err)
	}
	if got != repo {
		t.Errorf("ReadOrigin = %q, want %q", got, repo)
	}

	// File should exist with our expected name + trailing newline,
	// readable by `cat`.
	raw, err := os.ReadFile(OriginPath(slug))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Errorf("origin file should end in newline, got %q", raw)
	}
}

func TestOriginIdempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := "/home/me/work/myproj"
	slug := SlugDir(repo)

	for i := 0; i < 3; i++ {
		if err := WriteOrigin(slug, repo); err != nil {
			t.Fatalf("WriteOrigin iter %d: %v", i, err)
		}
	}
	got, err := ReadOrigin(slug)
	if err != nil {
		t.Fatalf("ReadOrigin: %v", err)
	}
	if got != repo {
		t.Errorf("ReadOrigin after repeated writes = %q, want %q", got, repo)
	}
}

func TestReadOriginMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := ReadOrigin("/nowhere"); err == nil {
		t.Errorf("ReadOrigin of nonexistent slug should error")
	}
}

func TestOriginPath(t *testing.T) {
	got := OriginPath("/tmp/foo")
	want := filepath.Join("/tmp/foo", ".origin")
	if got != want {
		t.Errorf("OriginPath = %q, want %q", got, want)
	}
}
