package vertex

import (
	"context"
	"errors"
	"testing"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// TestTokenSource_DoesNotRetainCallerContext guards the lazy ADC load
// path. Probes and doctor calls use short-lived contexts; the reusable
// token source must not capture one of those contexts and then fail every
// later refresh after the probe returns.
func TestTokenSource_DoesNotRetainCallerContext(t *testing.T) {
	probeCtx, cancelProbe := context.WithCancel(context.Background())
	var findCtx context.Context
	s := &TokenSource{
		find: func(ctx context.Context, scopes ...string) (*google.Credentials, error) {
			findCtx = ctx
			return &google.Credentials{
				ProjectID:   "p",
				TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "ya29.test"}),
			}, nil
		},
	}

	if _, err := s.Token(probeCtx); err != nil {
		t.Fatalf("Token: %v", err)
	}
	cancelProbe()
	if err := findCtx.Err(); err != nil {
		t.Fatalf("FindDefaultCredentials received the probe context and is now canceled: %v", err)
	}
	if _, err := s.Token(context.Background()); err != nil {
		t.Fatalf("later Token after probe cancellation: %v", err)
	}
}

func TestTokenSource_CanceledCallerStillFailsFast(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &TokenSource{
		find: func(context.Context, ...string) (*google.Credentials, error) {
			t.Fatal("find should not run when the caller context is already canceled")
			return nil, errors.New("unreachable")
		},
	}
	if _, err := s.Token(ctx); err == nil {
		t.Fatal("Token with canceled caller context succeeded")
	}
}
