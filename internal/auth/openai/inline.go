package openai

import (
	"context"
	"fmt"
)

// InlineLogin is the convenience helper for callers that want the
// full OAuth flow to run + persist tokens to the default store with
// no extra plumbing. Used by surfaces that block synchronously
// (typical for a single tea.Cmd that wraps the whole flow).
//
// For interactive surfaces that need to render a "click here" URL
// while the user is signing in (wizard, /provider add), use
// StartLogin and PendingLogin.Wait directly so the auth URL is
// available for fallback rendering.
//
// Returns the persisted TokenSet (also written to disk). The path
// can be inspected via DefaultStorePath if the caller wants to
// surface it.
func InlineLogin(ctx context.Context, opts LoginOptions) (TokenSet, error) {
	storePath, err := DefaultStorePath()
	if err != nil {
		return TokenSet{}, err
	}
	ts, err := Login(ctx, opts)
	if err != nil {
		return TokenSet{}, err
	}
	if err := Save(storePath, ts); err != nil {
		return ts, fmt.Errorf("save tokens: %w", err)
	}
	return ts, nil
}
