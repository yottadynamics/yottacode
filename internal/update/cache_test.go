package update

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCache_RoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	rec := cacheRecord{
		LastChecked:   now,
		LatestVersion: "0.3.0",
		ReleaseURL:    "https://example.com/v0.3.0",
	}
	if err := writeCache(rec); err != nil {
		t.Fatalf("writeCache: %v", err)
	}
	got, fresh := readCache(now)
	if !fresh {
		t.Fatalf("expected fresh cache, got miss")
	}
	if got.LatestVersion != "0.3.0" || got.ReleaseURL != "https://example.com/v0.3.0" {
		t.Fatalf("unexpected record: %+v", got)
	}
}

func TestCache_Expired(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	past := time.Now().Add(-25 * time.Hour)
	if err := writeCache(cacheRecord{LastChecked: past, LatestVersion: "0.3.0"}); err != nil {
		t.Fatalf("writeCache: %v", err)
	}
	if _, fresh := readCache(time.Now()); fresh {
		t.Fatalf("expected expired cache to read as miss")
	}
}

func TestCache_Corrupt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".yottacode", "cache", "update-check.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{garbage"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, fresh := readCache(time.Now()); fresh {
		t.Fatalf("expected corrupt cache to read as miss")
	}
}

func TestCache_ClockSkew(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	future := time.Now().Add(48 * time.Hour)
	if err := writeCache(cacheRecord{LastChecked: future, LatestVersion: "0.3.0"}); err != nil {
		t.Fatalf("writeCache: %v", err)
	}
	if _, fresh := readCache(time.Now()); fresh {
		t.Fatalf("expected future-dated cache to read as miss")
	}
}

func TestCache_DirPerm(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := writeCache(cacheRecord{LastChecked: time.Now(), LatestVersion: "0.3.0"}); err != nil {
		t.Fatalf("writeCache: %v", err)
	}
	dir := filepath.Join(home, ".yottacode", "cache")
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("dir perm = %o, want 0700", perm)
	}
}
