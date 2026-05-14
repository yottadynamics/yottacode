package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Three-tier auth precedence chain mirrored from the v0.5.0
// roadmap (yottacode-roadmap/github-integration.md):
//
//   1. $GITHUB_TOKEN env var (CI-friendly, explicit, fully
//      gh-independent).
//   2. `gh auth token` shell-out (one-shot at first use, cached
//      in memory for the session; convenience for users who
//      already ran `gh auth login`).
//   3. ~/.yottacode/github.json (yottacode-native PAT storage,
//      written by a future `yottacode setup github` flow).
//
// The chain stops at the first tier that yields a non-empty
// token. ResolveToken returns ErrNoToken when every tier fails;
// callers surface ErrGhUnavailable / draft-only fallback the
// same way they do today.
//
// Token discovery happens at most once per process (via the
// TokenResolver's sync.Once cache). Cache invalidation is
// out-of-scope for v1 — a user who reauths with `gh auth login`
// mid-session restarts yottacode to pick up the new token.

// ErrNoToken signals that the auth chain found no usable token.
// Distinct from ErrGhUnavailable: gh might be installed and
// authed but not on PATH (env var path), or vice versa.
var ErrNoToken = errors.New("no GitHub auth token configured (set $GITHUB_TOKEN, run `gh auth login`, or run `yottacode setup github`)")

// TokenResolver caches the resolved token for a process. Safe
// for concurrent use — the underlying sync.Once + struct fields
// are immutable after the first successful resolve.
type TokenResolver struct {
	once   sync.Once
	token  string
	source string // "env" | "gh" | "file" | "" when not yet resolved
	err    error
}

// NewTokenResolver returns a fresh resolver. Each ShellOut /
// TypedClient instance carries its own so they don't share
// cached tokens across (theoretical) multi-tenant runs in the
// same process.
func NewTokenResolver() *TokenResolver {
	return &TokenResolver{}
}

// Resolve runs the precedence chain. First call does the work;
// subsequent calls return the cached result (success or error).
// Returns (token, source, err) where source identifies which
// tier won — useful for `yottacode doctor` and for tests.
func (r *TokenResolver) Resolve(ctx context.Context) (token, source string, err error) {
	r.once.Do(func() {
		r.token, r.source, r.err = resolveTokenChain(ctx)
	})
	return r.token, r.source, r.err
}

// resolveTokenChain walks the three tiers in order. Pulled out
// of Resolve so tests can drive the chain directly without
// fighting sync.Once across test runs.
func resolveTokenChain(ctx context.Context) (token, source string, err error) {
	if t := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); t != "" {
		return t, "env", nil
	}
	if t, ok := tokenFromGh(ctx); ok {
		return t, "gh", nil
	}
	if t, ok := tokenFromFile(); ok {
		return t, "file", nil
	}
	return "", "", ErrNoToken
}

// tokenFromGh runs `gh auth token` and returns the printed
// token. Returns (token, true) on success; (",", false) when
// gh isn't on PATH, isn't authed, or returns empty. The bool
// rather than error because every failure mode here is "fall
// through to the next tier", not "stop the chain".
func tokenFromGh(ctx context.Context) (string, bool) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", false
	}
	cmd := exec.CommandContext(ctx, "gh", "auth", "token")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// Suppress stderr — gh auth token writes nothing useful on
	// success and a confusing "not logged in" message on
	// failure. The next-tier fall-through gives the user a
	// cleaner final error.
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Run(); err != nil {
		return "", false
	}
	t := strings.TrimSpace(stdout.String())
	if t == "" {
		return "", false
	}
	return t, true
}

// tokenFromFile reads ~/.yottacode/github.json. Schema:
//
//	{"token": "ghp_..."}
//
// Designed to be the on-disk shape `yottacode setup github`
// writes. Returns (",", false) on any failure (missing file,
// permission error, malformed JSON) — every failure mode is
// "fall through", same as tokenFromGh.
func tokenFromFile() (string, bool) {
	path, err := tokenFilePath()
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", false
	}
	t := strings.TrimSpace(payload.Token)
	if t == "" {
		return "", false
	}
	return t, true
}

// tokenFilePath returns the canonical on-disk location for the
// yottacode-native PAT file. Exposed so tests and the future
// `yottacode setup github` flow can target the same path
// without duplicating the home-dir computation.
func tokenFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".yottacode", "github.json"), nil
}

// IsGhAvailable reports whether the auth chain can resolve a
// usable token from any tier. The historical name is preserved
// for caller compatibility (the gh_pr_context tool exposes a
// "GhAvailable" flag in its snapshot), but with the typed client
// in place the check is provider-agnostic: any of the three
// auth tiers ($GITHUB_TOKEN, gh, file) satisfying counts.
//
// Cheap: doesn't make a GitHub API call. Returns false when
// every tier misses — same semantics as the old gh-only check
// but no longer dependent on gh specifically being installed.
func IsGhAvailable(ctx context.Context) bool {
	_, _, err := resolveTokenChain(ctx)
	return err == nil
}

// TokenFile is the on-disk shape WriteTokenFile produces and
// tokenFromFile reads. Token is the only field the resolver
// uses; User and Created are metadata for `yottacode doctor`
// and future audit/UX surfaces.
type TokenFile struct {
	Token   string `json:"token"`
	User    string `json:"user,omitempty"`
	Created string `json:"created,omitempty"` // RFC3339
}

// WriteTokenFile persists a token to the canonical location
// (~/.yottacode/github.json). Atomic via temp-file + rename so
// a crash mid-write never leaves a half-written file. Mode 0600
// on the file, 0700 on the parent dir — the same posture other
// yottacode secret stores use (auth/openai-auth.json, etc.).
//
// Caller passes the verified token + the GitHub login the
// token authenticated as (for the User field). Created is
// stamped here so callers don't have to remember.
func WriteTokenFile(token, user string) (string, error) {
	path, err := tokenFilePath()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(token) == "" {
		return "", errors.New("WriteTokenFile: token is empty")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create auth dir: %w", err)
	}
	payload := TokenFile{
		Token:   token,
		User:    user,
		Created: time.Now().UTC().Format(time.RFC3339),
	}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal token file: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return "", fmt.Errorf("write temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		// Best-effort cleanup so a crashed rename doesn't leave
		// the .tmp lying around.
		_ = os.Remove(tmp)
		return "", fmt.Errorf("rename: %w", err)
	}
	return path, nil
}

// RemoveTokenFile deletes the on-disk token. Returns the path
// removed and a bool indicating whether the file existed
// pre-call (so callers can render "Already empty" vs "Removed"
// distinctly). Missing-file is NOT an error — idempotent by
// design.
func RemoveTokenFile() (path string, existed bool, err error) {
	path, err = tokenFilePath()
	if err != nil {
		return "", false, err
	}
	if _, statErr := os.Stat(path); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return path, false, nil
		}
		return path, false, fmt.Errorf("stat: %w", statErr)
	}
	if err := os.Remove(path); err != nil {
		return path, true, fmt.Errorf("remove: %w", err)
	}
	return path, true, nil
}
