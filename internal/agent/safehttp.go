package agent

import (
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/yottadynamics/yottacode/internal/netguard"
)

// blockedDialControl is a net.Dialer Control hook that refuses connections
// to non-public destinations. It runs AFTER DNS resolution, on the actual
// IP being dialed — so it defeats DNS rebinding (a hostname that resolves
// public on the first lookup but internal at connect time) and applies to
// every hop of an HTTP redirect chain, not just the original URL.
func blockedDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("ssrf guard: bad address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("ssrf guard: unresolved address %q", address)
	}
	if netguard.IsNonPublicIP(ip) {
		return fmt.Errorf("ssrf guard: refusing to connect to non-public address %s (use run_bash if you genuinely need a local/private host)", ip)
	}
	return nil
}

// ssrfSafeHTTPClient is the shared client for model-driven fetches
// (fetch_url). Its dialer rejects loopback/link-local/private/metadata
// destinations so a prompt-injected URL can't reach cloud metadata,
// localhost services, or the private network. A fresh public connection
// is cheap; we don't reuse a pooled client's idle conns across hosts to
// keep the guard unambiguous.
var ssrfSafeHTTPClient = &http.Client{
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second,
			Control: blockedDialControl,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		MaxIdleConns:          10,
	},
}
