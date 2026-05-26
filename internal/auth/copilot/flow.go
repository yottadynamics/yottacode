package copilot

import (
	"context"
	"fmt"
	"net/http"
)

// LoginOptions tunes the device code flow.
type LoginOptions struct {
	ClientID   string
	HTTPClient *http.Client

	// OnDeviceCode is called with the device code response after
	// initiation. The caller should display the user_code and
	// verification_uri to the user.
	OnDeviceCode func(dc DeviceCode)
}

func (o *LoginOptions) defaults() {
	if o.ClientID == "" {
		o.ClientID = DefaultClientID
	}
}

// Login runs the full device code flow: request a code, wait for the
// user to authorize, return the GitHub OAuth token.
func Login(ctx context.Context, opts LoginOptions) (string, error) {
	opts.defaults()
	dc, err := RequestDeviceCode(ctx, opts.HTTPClient, opts.ClientID)
	if err != nil {
		return "", fmt.Errorf("copilot login: %w", err)
	}
	if opts.OnDeviceCode != nil {
		opts.OnDeviceCode(dc)
	}
	token, err := PollForToken(ctx, opts.HTTPClient, opts.ClientID, dc)
	if err != nil {
		return "", fmt.Errorf("copilot login: %w", err)
	}
	return token, nil
}

// InlineLogin runs the device code flow and persists the token to the
// default store path.
func InlineLogin(ctx context.Context, opts LoginOptions) (TokenSet, error) {
	storePath, err := DefaultStorePath()
	if err != nil {
		return TokenSet{}, err
	}
	token, err := Login(ctx, opts)
	if err != nil {
		return TokenSet{}, err
	}
	ts := TokenSet{GitHubToken: token}
	if err := Save(storePath, ts); err != nil {
		return ts, fmt.Errorf("save tokens: %w", err)
	}
	return ts, nil
}
