package browser

import (
	"context"
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// remoteHandle ties a session to a browser it does not own: a cloud session
// that keeps running (and billing) until explicitly released.
type remoteHandle struct {
	// label names the provider for messages ("browserbase").
	label string
	// release ends the provider-side session. Best effort and idempotent.
	release func(ctx context.Context) error
}

// remoteReleaseTimeout bounds each attempt to release a remote session — the
// teardown paths run after the action deadline may already have passed.
const remoteReleaseTimeout = 10 * time.Second

// remoteProbeTimeout bounds the liveness probe a remote session answers in
// place of a local process check (see session.alive).
const remoteProbeTimeout = 3 * time.Second

// remoteFileErr explains why a file-transfer action can't run on a remote
// browser: CDP file paths name the *browser's* filesystem, which for a cloud
// browser is not ours, so an upload would look for a local path on their
// machine and a download would land where yottacode cannot read it.
func remoteFileErr(action string) error {
	return fmt.Errorf("%w: browser_%s moves files between yottacode's machine and the browser, but this session's browser is remote — use the local browser provider for file transfers", ErrUnsupportedRemote, action)
}

// degradedFeatures lists provider features dropped at launch (see
// Status.Degraded).
func (s *session) degradedFeatures() []string { return s.degraded }

// releaseRemote ends the provider-side session, if this session has one.
func (s *session) releaseRemote() error {
	if s.remote == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), remoteReleaseTimeout)
	defer cancel()
	return s.remote.release(ctx)
}

// connectBrowser dials br's CDP endpoint, giving up when ctx does. rod's
// Connect takes no context of its own, and the *rod.Browser must not be
// bound to ctx — it outlives this one launch call (see launchSession) — so
// the dial runs in a goroutine the caller can abandon.
func connectBrowser(ctx context.Context, br *rod.Browser) error {
	done := make(chan error, 1)
	go func() { done <- br.Connect() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// The dial is still in flight and may yet succeed. Nobody will use that
		// connection, so close it when it lands rather than leave a live
		// websocket to a rented (billed) browser behind.
		go func() {
			if err := <-done; err == nil {
				_ = br.Close()
			}
		}()
		return ctx.Err()
	}
}

// launchRemoteSession connects to an already-running browser over CDP and
// wraps it in a session. It adopts the browser's existing first page when it
// has one (a rented browser opens with a tab) rather than leaving a stray
// blank one. The remote browser's own identity is left alone: no rod device
// emulation, since a provider's fingerprint is the point of renting one.
//
// On failure the caller still owns handle.release — nothing has been
// released yet.
func launchRemoteSession(ctx context.Context, connectURL string, handle *remoteHandle) (*session, error) {
	br := rod.New().NoDefaultDevice().ControlURL(connectURL)
	if err := connectBrowser(ctx, br); err != nil {
		return nil, fmt.Errorf("%w: connect to %s: %v", ErrLaunchFailed, handle.label, err)
	}
	var pg *rod.Page
	if pages, err := br.Pages(); err == nil && len(pages) > 0 {
		pg = pages[0]
	} else {
		created, err := br.Page(proto.TargetCreateTarget{URL: "about:blank"})
		if err != nil {
			_ = br.Close()
			return nil, fmt.Errorf("%w: open a page on %s: %v", ErrLaunchFailed, handle.label, err)
		}
		pg = created
	}
	return newSessionCore(br, nil, pg, sessionConfig{remote: handle}), nil
}

// launchBrowserbaseSession rents a Browserbase session and connects to it.
// If the connection fails the rented session is released straight away —
// otherwise it would bill until its server-side timeout.
func launchBrowserbaseSession(ctx context.Context, c *browserbaseClient) (pageSession, error) {
	bb, err := c.create(ctx)
	if err != nil {
		return nil, err
	}
	handle := &remoteHandle{
		label:   ProviderBrowserbase,
		release: func(ctx context.Context) error { return c.release(ctx, bb.ID) },
	}
	s, err := launchRemoteSession(ctx, bb.ConnectURL, handle)
	if err != nil {
		_ = releaseHandle(handle)
		return nil, err
	}
	s.degraded = bb.Degraded
	return s, nil
}

// releaseHandle releases a remote session that never became a *session (a
// failed launch), under the same bound as releaseRemote.
func releaseHandle(h *remoteHandle) error {
	ctx, cancel := context.WithTimeout(context.Background(), remoteReleaseTimeout)
	defer cancel()
	return h.release(ctx)
}
