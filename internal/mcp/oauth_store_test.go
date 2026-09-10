package mcp

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestOAuthStore_SaveLoadDeleteRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if got, err := LoadToken("gmail"); err != nil || got != nil {
		t.Fatalf("LoadToken before any save = (%v, %v), want (nil, nil)", got, err)
	}

	want := &oauth2.Token{
		AccessToken:  "at-1",
		RefreshToken: "rt-1",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour).Truncate(time.Second),
	}
	if err := SaveToken("gmail", want); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	got, err := LoadToken("gmail")
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if got == nil || got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken {
		t.Fatalf("LoadToken = %+v, want %+v", got, want)
	}

	existed, err := DeleteToken("gmail")
	if err != nil || !existed {
		t.Fatalf("DeleteToken = (%v, %v), want (true, nil)", existed, err)
	}
	if got, err := LoadToken("gmail"); err != nil || got != nil {
		t.Fatalf("LoadToken after delete = (%v, %v), want (nil, nil)", got, err)
	}

	// Idempotent: deleting an already-absent token is not an error.
	existed, err = DeleteToken("gmail")
	if err != nil || existed {
		t.Fatalf("second DeleteToken = (%v, %v), want (false, nil)", existed, err)
	}
}

func TestOAuthStore_FileModeIsPrivate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := SaveToken("drive", &oauth2.Token{AccessToken: "at"}); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	path := filepath.Join(home, ".yottacode", "mcp-auth", "drive.json")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("token file mode = %o, want 0600", perm)
	}

	dirInfo, err := os.Stat(filepath.Join(home, ".yottacode", "mcp-auth"))
	if err != nil {
		t.Fatalf("stat mcp-auth dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("mcp-auth dir mode = %o, want 0700", perm)
	}
}

func TestOAuthStore_OneFilePerServerName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := SaveToken("gmail", &oauth2.Token{AccessToken: "gmail-at"}); err != nil {
		t.Fatalf("SaveToken(gmail): %v", err)
	}
	if err := SaveToken("drive", &oauth2.Token{AccessToken: "drive-at"}); err != nil {
		t.Fatalf("SaveToken(drive): %v", err)
	}

	if _, err := DeleteToken("gmail"); err != nil {
		t.Fatalf("DeleteToken(gmail): %v", err)
	}

	// Deleting one server's token must not touch another's.
	got, err := LoadToken("drive")
	if err != nil || got == nil || got.AccessToken != "drive-at" {
		t.Fatalf("LoadToken(drive) after DeleteToken(gmail) = (%+v, %v), want drive-at", got, err)
	}
}
