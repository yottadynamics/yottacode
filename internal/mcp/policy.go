package mcp

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"

	"github.com/yottadynamics/yottacode/internal/netguard"
)

// Policy is the transport security policy applied to every HTTP/SSE MCP
// server: which schemes/hosts are reachable, and whether a redirect may
// cross to a different host. It has no effect on stdio servers.
//
// The zero value (RequireTLS: false, AllowedHosts: nil) is permissive —
// any scheme, any host. That is NOT the production default; config.Default
// explicitly sets MCPConfig.RequireTLS to true, and Manager builds its
// Policy from the loaded config. A caller constructing Policy{} directly
// (tests, or code that hasn't been wired to config) gets the permissive
// behavior, not the safe one — do that deliberately, not by omission.
type Policy struct {
	// RequireTLS rejects http:// URLs except to a loopback host
	// (127.0.0.1, localhost, ::1). false disables the check entirely.
	RequireTLS bool
	// AllowedHosts, when non-empty, restricts connections to an exact
	// (case-insensitive) host match from this list — no "*." globs in v1,
	// to avoid an allow rule like "*.linear.app" matching
	// "evil.linear.app.attacker.tld". Empty means any host is reachable
	// (subject to RequireTLS).
	AllowedHosts []string
}

// CheckURL reports whether rawURL is reachable under this policy. Applied
// both at connect time (HTTPClient.Start) and at the CLI/TUI `mcp add`
// entry points, so a bad URL fails at add-time instead of first use.
func (p Policy) CheckURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("mcp policy: invalid URL %q: %w", rawURL, err)
	}
	host := u.Hostname()
	if p.RequireTLS && !strings.EqualFold(u.Scheme, "https") && !isLoopbackHost(host) {
		return fmt.Errorf(
			"mcp policy: %s is not allowed — require_tls is on and %s is not a loopback host (127.0.0.1, localhost, ::1)",
			rawURL, host)
	}
	if len(p.AllowedHosts) > 0 && !containsHostFold(p.AllowedHosts, host) {
		return fmt.Errorf("mcp policy: host %q is not in allowed_hosts", host)
	}
	return nil
}

// CheckRedirect is an http.Client.CheckRedirect implementation: it refuses
// a redirect that crosses to a different host, and re-applies CheckURL to
// the redirect target so a same-host redirect still can't downgrade scheme
// or land somewhere allowed_hosts excludes.
func (p Policy) CheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	if !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
		return fmt.Errorf("mcp policy: refusing cross-host redirect from %s to %s", via[0].URL.Host, req.URL.Host)
	}
	return p.CheckURL(req.URL.String())
}

// DialControl returns a net.Dialer.Control hook that defeats DNS rebinding
// for a hostname-configured remote MCP server: an admin adds a server by
// hostname expecting it to resolve to a stable, presumably-public vendor
// endpoint, but a hostile or compromised DNS answer could later resolve
// that same hostname to a private/internal address, turning a "trusted"
// configured server into a vector for probing the local network.
//
// The hook runs after DNS resolution, on the actual address being dialed,
// so it also covers every hop of a redirect chain, not just the original
// URL. If configuredHost is itself a literal IP or one of the recognized
// loopback hostnames (see isLoopbackHost), there's no DNS layer to rebind
// — the admin's configuration IS the destination — so DialControl returns
// nil (no hook installed) rather than second-guessing an explicit choice.
func (p Policy) DialControl(configuredHost string) func(network, address string, c syscall.RawConn) error {
	if isLoopbackHost(configuredHost) {
		return nil
	}
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("mcp policy: bad address %q: %w", address, err)
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("mcp policy: unresolved address %q", address)
		}
		if netguard.IsNonPublicIP(ip) {
			return fmt.Errorf(
				"mcp policy: refusing connection to non-public address %s for hostname-configured server %q (possible DNS rebinding)",
				ip, configuredHost)
		}
		return nil
	}
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

func containsHostFold(hosts []string, host string) bool {
	for _, h := range hosts {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}
