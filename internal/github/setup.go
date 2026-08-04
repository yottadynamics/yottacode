package github

import (
	"context"
	"errors"
	"fmt"
	"strings"

	gogithub "github.com/google/go-github/v66/github"
)

// VerifyResult is the typed outcome of a token check. Login is
// the authenticated user's GitHub handle (so the setup wizard
// can display "Authenticated as @octocat" for sanity check).
// Returned only on success — failure modes return an error.
type VerifyResult struct {
	Login string
}

// VerifyToken makes one cheap GitHub API call (`/user` endpoint
// via Users.Get with empty string) to confirm the token is
// valid and capture the authenticated identity. Used by the
// setup wizard before writing the token to disk so we never
// persist a bad token.
//
// Returns a typed error distinguishing the common failure
// modes:
//   - ErrGitHubUnavailable: token rejected (401) or auth otherwise
//     failed.
//   - Anything else: network failure or unexpected GitHub-side
//     error, wrapped with context.
func VerifyToken(ctx context.Context, token string) (VerifyResult, error) {
	var res VerifyResult
	if strings.TrimSpace(token) == "" {
		return res, errors.New("VerifyToken: token is empty")
	}
	client := gogithub.NewClient(nil).WithAuthToken(token)
	user, _, err := client.Users.Get(ctx, "")
	if err != nil {
		// Map 401 to ErrGitHubUnavailable — same shape callers
		// already branch on. Other API errors bubble up
		// wrapped.
		return res, fmt.Errorf("verify token: %w", classifyAPIError(err))
	}
	if user == nil || user.Login == nil || *user.Login == "" {
		return res, errors.New("verify token: GitHub returned empty user identity")
	}
	res.Login = *user.Login
	return res, nil
}

// SaveVerifiedToken is the setup wizard's atomic
// "verify-then-persist" entry point. Doesn't write anything on
// verify failure — keeping the on-disk state consistent across
// failed setup attempts. Returns the on-disk path on success so
// the caller can surface it ("Saved to <path>").
func SaveVerifiedToken(ctx context.Context, token string) (path string, login string, err error) {
	v, err := VerifyToken(ctx, token)
	if err != nil {
		return "", "", err
	}
	written, err := WriteTokenFile(token, v.Login)
	if err != nil {
		return "", "", fmt.Errorf("save token: %w", err)
	}
	return written, v.Login, nil
}
