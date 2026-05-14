package github

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// Repo detection — figures out (owner, repo) from a working
// directory's git remote. The typed client needs this because
// it doesn't shell out to gh (which would auto-infer from cwd).
// We do the inference ourselves by running `git remote get-url
// origin` and parsing GitHub's URL shapes.
//
// Two URL forms in the wild:
//
//   SSH:        git@github.com:owner/repo.git
//   HTTPS:      https://github.com/owner/repo.git
//   HTTPS-token: https://<token>@github.com/owner/repo.git
//   git://:    git://github.com/owner/repo.git    (legacy, rare)
//
// All four normalize to (owner, repo). The `.git` suffix is
// optional in the URL but normalized off here.

var (
	// sshRemotePattern matches the SSH form: git@host:owner/repo[.git].
	sshRemotePattern = regexp.MustCompile(`^git@github\.com:([^/]+)/([^/]+?)(?:\.git)?$`)
	// httpsRemotePattern matches both https and git:// schemes
	// over github.com. The optional `<user>@` in the authority is
	// the credential-in-URL form some setups use.
	httpsRemotePattern = regexp.MustCompile(`^(?:https?|git)://(?:[^@/]+@)?github\.com/([^/]+)/([^/]+?)(?:\.git)?$`)
)

// DetectRepo reads the cwd's `origin` remote URL and parses it
// into (owner, repo). Returns an error when no remote is
// configured, the remote isn't a GitHub URL, or the URL shape
// doesn't match the expected forms.
//
// Specifically targets `origin` (not other remotes) because
// that's the convention every gh / git workflow assumes. Users
// with multi-remote setups can pass owner/repo explicitly via
// the request types, which short-circuits this lookup.
func DetectRepo(ctx context.Context, cwd string) (owner, repo string, err error) {
	if _, lookErr := exec.LookPath("git"); lookErr != nil {
		return "", "", fmt.Errorf("detect repo: git not found in PATH")
	}
	cmd := exec.CommandContext(ctx, "git", "remote", "get-url", "origin")
	cmd.Dir = cwd
	out, runErr := cmd.Output()
	if runErr != nil {
		return "", "", fmt.Errorf("detect repo: read origin remote: %w", runErr)
	}
	return ParseRemoteURL(strings.TrimSpace(string(out)))
}

// ParseRemoteURL is the pure-function core of DetectRepo. Tries
// each known URL shape; returns the first match. Exposed for
// testability — callers shouldn't need to invoke it directly.
func ParseRemoteURL(remote string) (owner, repo string, err error) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", "", fmt.Errorf("parse remote: empty URL")
	}
	if m := sshRemotePattern.FindStringSubmatch(remote); m != nil {
		return m[1], m[2], nil
	}
	if m := httpsRemotePattern.FindStringSubmatch(remote); m != nil {
		return m[1], m[2], nil
	}
	return "", "", fmt.Errorf("parse remote: unrecognized GitHub URL shape: %q", remote)
}
