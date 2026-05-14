package github

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveTokenChain_EnvTierWins(t *testing.T) {
	// Force HOME elsewhere so a real ~/.yottacode/github.json on
	// the test machine can't leak into the assertion.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "env-token-abc")

	token, source, err := resolveTokenChain(context.Background())
	if err != nil {
		t.Fatalf("resolveTokenChain: %v", err)
	}
	if token != "env-token-abc" {
		t.Errorf("token = %q; want env-token-abc", token)
	}
	if source != "env" {
		t.Errorf("source = %q; want env", source)
	}
}

func TestResolveTokenChain_EnvTrimsWhitespace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "  spaced-token  ")
	token, _, err := resolveTokenChain(context.Background())
	if err != nil {
		t.Fatalf("resolveTokenChain: %v", err)
	}
	if token != "spaced-token" {
		t.Errorf("expected whitespace trimmed; got %q", token)
	}
}

func TestResolveTokenChain_EmptyEnvFallsThrough(t *testing.T) {
	// Empty env var must NOT short-circuit the chain — env tier
	// only wins on non-empty content. Verify by setting env to
	// whitespace-only and seeing the chain reach the file tier.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GITHUB_TOKEN", "   ")

	// Seed a file token so the chain has somewhere to land.
	// Skip the gh tier by relying on this machine's gh state —
	// if gh happens to be installed and authed, that's fine
	// (the test asserts the chain reached *past* env, which is
	// true either way: source != "env").
	writeTokenFile(t, home, "file-token-xyz")

	_, source, err := resolveTokenChain(context.Background())
	if err != nil {
		t.Fatalf("resolveTokenChain: %v", err)
	}
	if source == "env" {
		t.Errorf("source should not be env with whitespace-only $GITHUB_TOKEN; got %q", source)
	}
}

func TestTokenFromFile_HappyPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeTokenFile(t, home, "file-token-xyz")

	got, ok := tokenFromFile()
	if !ok {
		t.Fatalf("expected tokenFromFile to succeed")
	}
	if got != "file-token-xyz" {
		t.Errorf("token = %q", got)
	}
}

func TestTokenFromFile_MissingFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, ok := tokenFromFile(); ok {
		t.Errorf("expected miss on no file")
	}
}

func TestTokenFromFile_MalformedJSONReturnsMiss(t *testing.T) {
	// Bad JSON is a "fall through" not "stop the chain" —
	// otherwise a corrupted file would lock the user out even
	// when env/gh would otherwise resolve.
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".yottacode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "github.json"), []byte("not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, ok := tokenFromFile(); ok {
		t.Errorf("expected miss on malformed file")
	}
}

func TestTokenFromFile_EmptyTokenFieldReturnsMiss(t *testing.T) {
	// {"token": ""} should also miss — empty values can't be
	// used for auth, and the chain should fall through.
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeTokenFile(t, home, "")

	if _, ok := tokenFromFile(); ok {
		t.Errorf("expected miss on empty token field")
	}
}

func TestResolveTokenChain_NoTierMatchesErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "")

	_, _, err := resolveTokenChain(context.Background())
	if err == nil {
		// gh might be authed on the developer machine running
		// the tests — in that case source==gh wins and there's
		// no error. We can only assert error when gh tier also
		// misses, which we can't reliably arrange in a unit
		// test. So skip the assertion if a token came back.
		t.Skip("gh is authed on this machine — chain reached gh tier; can't assert no-token error here")
	}
	if !errors.Is(err, ErrNoToken) {
		t.Errorf("expected ErrNoToken; got %v", err)
	}
}

func TestTokenResolver_CachesAcrossCalls(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "first-call-token")

	r := NewTokenResolver()
	t1, _, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}

	// Change the env var — a second Resolve should NOT pick it
	// up because the resolver caches after the first call. This
	// is the documented "restart yottacode to pick up new
	// tokens" behavior.
	t.Setenv("GITHUB_TOKEN", "second-call-token")
	t2, _, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if t1 != t2 {
		t.Errorf("expected cached token; got %q then %q", t1, t2)
	}
	if t2 == "second-call-token" {
		t.Errorf("resolver re-read env on second call; cache broken")
	}
}

func TestTokenFilePath_UnderHome(t *testing.T) {
	// Sanity: the canonical path lives under $HOME/.yottacode.
	// Pins the location so callers (future `yottacode setup
	// github` flow) target the same spot the resolver reads.
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := tokenFilePath()
	if err != nil {
		t.Fatalf("tokenFilePath: %v", err)
	}
	want := filepath.Join(home, ".yottacode", "github.json")
	if path != want {
		t.Errorf("path = %q; want %q", path, want)
	}
}

// writeTokenFile is the test helper that mirrors the on-disk
// schema the `yottacode setup github` flow emits.
func writeTokenFile(t *testing.T, home, token string) {
	t.Helper()
	dir := filepath.Join(home, ".yottacode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	payload, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "github.json"), payload, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestWriteTokenFile_AtomicAndPermissions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := WriteTokenFile("ghp_test_token", "octocat")
	if err != nil {
		t.Fatalf("WriteTokenFile: %v", err)
	}
	want := filepath.Join(home, ".yottacode", "github.json")
	if path != want {
		t.Errorf("path = %q; want %q", path, want)
	}

	// File permissions must be 0600 — token storage.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode = %o; want 0600", mode)
	}

	// Parent dir 0700 — same posture as other yottacode auth
	// stores.
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Errorf("dir mode = %o; want 0700", mode)
	}

	// Content shape: must round-trip through tokenFromFile so
	// the resolver picks it up.
	got, ok := tokenFromFile()
	if !ok {
		t.Fatal("tokenFromFile should round-trip a freshly-written file")
	}
	if got != "ghp_test_token" {
		t.Errorf("round-trip token = %q", got)
	}
}

func TestWriteTokenFile_PopulatesMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := WriteTokenFile("tok", "octocat")
	if err != nil {
		t.Fatalf("WriteTokenFile: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got TokenFile
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Token != "tok" {
		t.Errorf("token = %q", got.Token)
	}
	if got.User != "octocat" {
		t.Errorf("user = %q", got.User)
	}
	if got.Created == "" {
		t.Errorf("expected Created timestamp populated")
	}
}

func TestWriteTokenFile_RejectsEmptyToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := WriteTokenFile("   ", "octocat"); err == nil {
		t.Errorf("expected error on empty token")
	}
}

func TestWriteTokenFile_OverwritesExisting(t *testing.T) {
	// Re-running setup github should refresh the file.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := WriteTokenFile("first", "octocat"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := WriteTokenFile("second", "octocat"); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, _ := tokenFromFile()
	if got != "second" {
		t.Errorf("expected second write to win; got %q", got)
	}
}

func TestRemoveTokenFile_DeletesExistingFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := WriteTokenFile("tok", "octocat"); err != nil {
		t.Fatalf("setup: %v", err)
	}

	path, existed, err := RemoveTokenFile()
	if err != nil {
		t.Fatalf("RemoveTokenFile: %v", err)
	}
	if !existed {
		t.Errorf("expected existed=true on a pre-populated file")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("expected file gone after remove; stat err = %v", statErr)
	}
}

func TestRemoveTokenFile_MissingIsIdempotent(t *testing.T) {
	// Idempotent: removing a non-existent file is NOT an
	// error. Callers branch on the existed bool to render
	// "Already empty" vs "Removed".
	t.Setenv("HOME", t.TempDir())
	_, existed, err := RemoveTokenFile()
	if err != nil {
		t.Fatalf("RemoveTokenFile: %v", err)
	}
	if existed {
		t.Errorf("expected existed=false on missing file")
	}
}
