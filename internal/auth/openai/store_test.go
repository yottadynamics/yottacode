package openai

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth", "openai-auth.json")
	want := TokenSet{
		AccessToken:  "atk",
		RefreshToken: "rtk",
		ExpiresAt:    time.Now().Add(time.Hour).UTC().Truncate(time.Second),
		AccountID:    "user_42",
		Email:        "p@example.com",
	}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.AccessToken != want.AccessToken ||
		got.RefreshToken != want.RefreshToken ||
		got.AccountID != want.AccountID ||
		got.Email != want.Email ||
		!got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("round trip mismatch:\n got %+v\n want %+v", got, want)
	}
}

func TestSavePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits are not honored the same way on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "auth", "openai-auth.json")
	if err := Save(path, TokenSet{AccessToken: "x"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode = %o, want 0600", mode)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Errorf("dir mode = %o, want 0700", mode)
	}
}

func TestSaveAtomic(t *testing.T) {
	// Saving twice must end with exactly the new content; no
	// .tmp files left lying around.
	dir := t.TempDir()
	path := filepath.Join(dir, "auth", "openai-auth.json")
	if err := Save(path, TokenSet{AccessToken: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, TokenSet{AccessToken: "second"}); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "second" {
		t.Errorf("got %q, want %q", got.AccessToken, "second")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestLoadMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(filepath.Join(dir, "nope.json"))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}
