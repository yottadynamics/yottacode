package browser

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// The navigation floor: cloud instance-metadata endpoints serve IAM
// credentials to anything that can reach them, and a browser running on a
// cloud VM can. There is no legitimate reason for the agent to navigate to
// one, so it is refused unconditionally — including under --yolo and auto
// mode, which skip the approval prompt that would otherwise catch it.
//
// Deliberately narrow: private and loopback addresses are NOT blocked —
// pointing the browser at localhost:3000 is this tool's main use case.

// blockedMetadataPrefixes are address ranges no agent navigation may target:
// the whole IPv4 link-local block (AWS/GCP/Azure/DO/Oracle metadata, ECS task
// credentials, Azure's wire server all live in it).
var blockedMetadataPrefixes = []netip.Prefix{
	netip.MustParsePrefix("169.254.0.0/16"),
}

// blockedMetadataAddrs are individual endpoints outside that range.
var blockedMetadataAddrs = []netip.Addr{
	netip.MustParseAddr("fd00:ec2::254"),   // AWS metadata over IPv6
	netip.MustParseAddr("100.100.100.200"), // Alibaba Cloud metadata
	netip.MustParseAddr("168.63.129.16"),   // Azure wire server
}

// blockedMetadataHosts are hostnames that resolve to those endpoints.
var blockedMetadataHosts = []string{
	"metadata.google.internal",
	"metadata.goog",
	"instance-data",
	"instance-data.ec2.internal",
}

// blockedURLPatterns is the request-interception form of the floor (see
// armRequestGuard), armed on every page so a redirect, subresource or
// script-initiated request to one of these endpoints is stopped too — not
// just a direct navigation. CDP patterns cannot express a CIDR range, so
// this enumerates the known endpoints; the /16 as a whole is only enforced
// by checkNavigationURL. A var so tests can add a pattern for a local server.
var blockedURLPatterns = buildBlockedURLPatterns()

func buildBlockedURLPatterns() []string {
	var out []string
	add := func(host string) {
		out = append(out, "*://"+host+"/*", "*://"+host+":*/*")
	}
	for _, h := range blockedMetadataHosts {
		add(h)
	}
	for _, a := range blockedMetadataAddrs {
		if a.Is6() {
			add("[" + a.String() + "]")
		} else {
			add(a.String())
		}
	}
	add("169.254.169.254")
	add("169.254.170.2")
	add("169.254.169.253")
	return out
}

// checkNavigationURL returns ErrBlockedURL when raw targets a metadata
// endpoint. It normalizes the host the way a browser would — IPv4 written as
// a single decimal/hex/octal number ("http://2852039166/") resolves to
// 169.254.169.254 in Chrome — so those spellings can't slip past a string
// comparison. An unparseable URL is not this check's concern; the navigation
// itself will fail on it.
func checkNavigationURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return nil
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if slices.Contains(blockedMetadataHosts, host) {
		return fmt.Errorf("%w: %s is a cloud metadata endpoint", ErrBlockedURL, host)
	}
	if addr, ok := parseHostAddr(host); ok {
		if reason := blockedAddrReason(addr); reason != "" {
			return fmt.Errorf("%w: %s", ErrBlockedURL, reason)
		}
	}
	return nil
}

// blockedAddrReason says why addr is refused ("" when it isn't).
func blockedAddrReason(addr netip.Addr) string {
	addr = addr.Unmap()
	for _, p := range blockedMetadataPrefixes {
		if p.Contains(addr) {
			return fmt.Sprintf("%s is in the link-local range %s used by cloud metadata services", addr, p)
		}
	}
	if slices.Contains(blockedMetadataAddrs, addr) {
		return fmt.Sprintf("%s is a cloud metadata endpoint", addr)
	}
	return ""
}

// lookupHost resolves a hostname for the navigation check. A var so tests can
// stand in for DNS.
var lookupHost = func(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// dnsCheckTimeout bounds the resolution done before a navigation.
const dnsCheckTimeout = 3 * time.Second

// checkNavigationTarget is checkNavigationURL plus DNS: a hostname that
// *resolves* to a metadata address is refused too, which closes the trivial
// bypass of pointing an attacker-controlled name (169-254-169-254.nip.io, or
// any A record) at the endpoint. It is deliberately best-effort and says so in
// the docs: a lookup that fails is let through (the browser will fail on it
// itself), and DNS can answer differently when the browser asks a moment later
// (rebinding), or for a redirect target or subresource, which only the literal
// host patterns cover. The literal checks remain the guarantee; this is the
// cheap second layer for the case a person would actually try.
func checkNavigationTarget(ctx context.Context, raw string) error {
	if err := checkNavigationURL(raw); err != nil {
		return err
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return nil
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if _, isLiteral := parseHostAddr(host); isLiteral || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil // nothing to resolve; checkNavigationURL already judged it
	}
	ctx, cancel := context.WithTimeout(ctx, dnsCheckTimeout)
	defer cancel()
	addrs, err := lookupHost(ctx, host)
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		if reason := blockedAddrReason(a); reason != "" {
			return fmt.Errorf("%w: %s resolves to %s", ErrBlockedURL, host, reason)
		}
	}
	return nil
}

// parseHostAddr parses host as an IP literal, including the legacy IPv4
// spellings browsers accept (inet_aton style: 1–4 dot-separated parts, each
// decimal, 0x-hex or 0-octal, the last part filling the remaining bytes).
func parseHostAddr(host string) (netip.Addr, bool) {
	if a, err := netip.ParseAddr(host); err == nil {
		return a, true
	}
	parts := strings.Split(host, ".")
	if len(parts) < 1 || len(parts) > 4 {
		return netip.Addr{}, false
	}
	nums := make([]uint64, len(parts))
	for i, p := range parts {
		if p == "" {
			return netip.Addr{}, false
		}
		n, err := strconv.ParseUint(p, 0, 32)
		if err != nil {
			return netip.Addr{}, false
		}
		nums[i] = n
	}
	var v uint64
	for i := 0; i < len(nums)-1; i++ {
		if nums[i] > 255 {
			return netip.Addr{}, false
		}
		v |= nums[i] << (8 * uint(3-i))
	}
	last := nums[len(nums)-1]
	if last >= uint64(1)<<(8*uint(4-(len(nums)-1))) {
		return netip.Addr{}, false
	}
	v |= last
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}), true
}

// armRequestGuard arms the request-level half of the floor on pg: it
// intercepts (via the Fetch domain) only requests matching
// blockedURLPatterns — every other request flows untouched, with no CDP
// round-trip — and fails each with BlockedByClient. This is used instead of
// Network.setBlockedURLs because that one does not apply to a redirected
// main-frame navigation, which is exactly the metadata-fetch trick a
// hostile page would use. Each stopped request is counted on tp so
// session.navigate can report it as ErrBlockedURL rather than as a page
// that merely failed to load. Best effort: if interception can't be
// enabled only the pre-navigation check is in force.
func armRequestGuard(pg *rod.Page, tp *trackedPage) {
	// The same Fetch session also answers an authenticating proxy's
	// challenge (see Options.ProxyURL). Fetch.enable replaces rather than
	// merges a previous enable, so both jobs must be armed by this one call.
	// Chrome only raises authRequired for requests that are being
	// intercepted, so with a proxy password to supply *every* request is
	// intercepted (pattern "*") and the safe ones are continued by hand; the
	// extra round-trip per request is the price of proxy auth. Without one,
	// only the blocked patterns are intercepted and everything else is
	// untouched.
	auth := tp.proxyAuth
	if err := (proto.FetchEnable{Patterns: guardPatterns(auth != nil), HandleAuthRequests: auth != nil}).Call(pg); err != nil {
		return
	}
	go pg.EachEvent(func(e *proto.FetchRequestPaused) {
		answerPaused(pg, tp, e, auth != nil, pg.FrameID)
	}, func(e *proto.FetchAuthRequired) {
		answerAuth(pg, auth, e)
	})()
}

// guardPatterns is the Fetch.enable pattern set for a guarded session: every
// request when an authenticating proxy needs its challenges seen (interceptAll),
// otherwise only the blocked endpoints.
func guardPatterns(interceptAll bool) []*proto.FetchRequestPattern {
	if interceptAll {
		return []*proto.FetchRequestPattern{{URLPattern: "*"}}
	}
	patterns := make([]*proto.FetchRequestPattern, 0, len(blockedURLPatterns))
	for _, p := range blockedURLPatterns {
		patterns = append(patterns, &proto.FetchRequestPattern{URLPattern: p})
	}
	return patterns
}

// answerPaused handles one intercepted request on session c: with everything
// intercepted, a request that isn't blocked is simply continued; a blocked one
// is failed. mainFrame is the page's own frame id, so only a main-frame
// navigation (or a redirect within one) counts against the navigation that is
// in flight — a stopped subresource or iframe just fails on its own, visible
// in browser_network_requests. Empty for a cross-process frame's session,
// which never counts.
func answerPaused(c proto.Client, tp *trackedPage, e *proto.FetchRequestPaused, interceptAll bool, mainFrame proto.PageFrameID) {
	if interceptAll && !urlBlocked(e.Request.URL) {
		_ = proto.FetchContinueRequest{RequestID: e.RequestID}.Call(c)
		return
	}
	if mainFrame != "" && e.ResourceType == proto.NetworkResourceTypeDocument && e.FrameID == mainFrame {
		tp.noteBlocked(e.Request.URL)
	}
	_ = proto.FetchFailRequest{RequestID: e.RequestID, ErrorReason: proto.NetworkErrorReasonBlockedByClient}.Call(c)
}

// answerAuth answers an authentication challenge seen on session c.
// Credentials go to the proxy only. A challenge from a *site* (HTTP Basic on
// the page itself) is left to Chrome's default, which in a headless browser
// cancels it — the proxy password must never be offered to an origin.
func answerAuth(c proto.Client, auth *proxyCreds, e *proto.FetchAuthRequired) {
	resp := &proto.FetchAuthChallengeResponse{Response: proto.FetchAuthChallengeResponseResponseDefault}
	if auth != nil && e.AuthChallenge != nil && e.AuthChallenge.Source == proto.FetchAuthChallengeSourceProxy {
		resp = &proto.FetchAuthChallengeResponse{
			Response: proto.FetchAuthChallengeResponseResponseProvideCredentials,
			Username: auth.user,
			Password: auth.pass,
		}
	}
	_ = proto.FetchContinueWithAuth{RequestID: e.RequestID, AuthChallengeResponse: resp}.Call(c)
}

// urlBlocked reports whether the guard must stop a request to url: it names a
// metadata endpoint (checkNavigationURL, which covers the whole link-local
// range) or matches one of blockedURLPatterns.
func urlBlocked(url string) bool {
	if checkNavigationURL(url) != nil {
		return true
	}
	for _, p := range blockedURLPatterns {
		if globMatch(p, url) {
			return true
		}
	}
	return false
}

// globMatch matches s against a CDP URL pattern: '*' stands for any run of
// characters (including none, and including '/'), '?' for exactly one, and
// everything else literally.
func globMatch(pattern, s string) bool {
	px, sx := 0, 0
	star, mark := -1, 0
	for sx < len(s) {
		switch {
		case px < len(pattern) && (pattern[px] == '?' || pattern[px] == s[sx]):
			px++
			sx++
		case px < len(pattern) && pattern[px] == '*':
			star, mark = px, sx
			px++
		case star >= 0:
			px = star + 1
			mark++
			sx = mark
		default:
			return false
		}
	}
	for px < len(pattern) && pattern[px] == '*' {
		px++
	}
	return px == len(pattern)
}

// noteBlocked records that the guard stopped a request to url.
func (tp *trackedPage) noteBlocked(url string) {
	tp.mu.Lock()
	tp.blockedN++
	tp.lastBlocked = url
	tp.mu.Unlock()
}

// blockedCount is how many requests the guard has stopped on this page.
func (tp *trackedPage) blockedCount() int {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.blockedN
}

// blockedSince reports the URL of the most recent request the guard stopped,
// if any were stopped after the counter read `before`.
func (tp *trackedPage) blockedSince(before int) (string, bool) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	if tp.blockedN > before {
		return tp.lastBlocked, true
	}
	return "", false
}
