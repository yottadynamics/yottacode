package browser

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// hostClass says how much a destination should be trusted by a page the agent
// is driving. checkNavigableURL only vets the URL the agent hands us; the
// network policy in this file vets every request the page makes afterwards —
// redirects, subresources, fetch/XHR, iframes — because those are where an
// untrusted public page reaches the user's machine and LAN (SSRF).
type hostClass int

const (
	classPublic hostClass = iota
	// classLoopback is localhost / 127.0.0.0/8 / ::1. Dev servers live here,
	// so an explicit navigation to any loopback host opens the whole class.
	classLoopback
	// classPrivate is RFC 1918, CGNAT, ULA and unspecified addresses. Allowed
	// only for hosts the agent explicitly navigated to.
	classPrivate
	// classBlocked is link-local space (169.254.0.0/16, fe80::/10) and the
	// well-known cloud metadata endpoints. Never reachable, even by explicit
	// navigation: credentials for the host's cloud identity live there.
	classBlocked
)

func (c hostClass) String() string {
	switch c {
	case classLoopback:
		return "loopback"
	case classPrivate:
		return "private-network"
	case classBlocked:
		return "link-local/metadata"
	}
	return "public"
}

var metadataHostnames = map[string]bool{
	"metadata":                   true,
	"metadata.google.internal":   true,
	"instance-data":              true,
	"instance-data.ec2.internal": true,
}

var (
	cgnat       = netip.MustParsePrefix("100.64.0.0/10")
	ula         = netip.MustParsePrefix("fc00::/7")
	azureWire   = netip.MustParseAddr("168.63.129.16")
	alibabaMeta = netip.MustParseAddr("100.100.100.200")
)

// classifyAddr maps one IP to its class.
func classifyAddr(a netip.Addr) hostClass {
	a = a.Unmap()
	switch {
	case a == azureWire || a == alibabaMeta:
		return classBlocked
	case a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast():
		return classBlocked
	case a.IsLoopback():
		return classLoopback
	case a.IsPrivate() || cgnat.Contains(a) || ula.Contains(a) || a.IsUnspecified() || a.IsMulticast():
		return classPrivate
	}
	return classPublic
}

// netPolicy decides whether a request made by a page may leave the browser.
// It is shared by every tab of one session.
type netPolicy struct {
	mu       syncutil.Mutex
	resolve  func(ctx context.Context, host string) ([]netip.Addr, error)
	now      func() time.Time
	cache    map[string]cachedClass
	inflight map[string]*lookup // lookups in progress, so a burst for one host asks once

	// Explicit-navigation tokens: set when the agent (with the user's approval)
	// navigates to a loopback or private host. They are NOT a standing grant: a
	// top-level navigation to any public host clears them all, so once the
	// browser has gone to a public site that site cannot reach local services
	// just because an earlier page did.
	loopbackTok bool
	privateTok  map[string]bool

	// top is the host each page target is currently on, learned from its
	// top-level navigations. A request from a page may reach a local address
	// only if the page itself is local (see decide).
	top map[string]topInfo
}

type topInfo struct {
	host  string
	class hostClass
}

// reqCtx says who is making a request.
type reqCtx struct {
	// targetID identifies the PAGE the request belongs to (for a frame or
	// worker, its owning page); "" when no page is known.
	targetID string
	// topNav marks a top-level document navigation of a page (including each
	// hop of a redirect chain).
	topNav bool
}

// lookup is one in-progress resolution that concurrent requests wait on.
type lookup struct {
	done  chan struct{}
	class hostClass
	ok    bool // the lookup succeeded
}

type cachedClass struct {
	class   hostClass
	expires time.Time
}

const (
	resolveTimeout  = 2 * time.Second
	classCacheTTL   = 30 * time.Second
	classCacheLimit = 1024
)

func newNetPolicy() *netPolicy {
	return &netPolicy{
		resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			return ips, err
		},
		now:        time.Now,
		cache:      map[string]cachedClass{},
		inflight:   map[string]*lookup{},
		privateTok: map[string]bool{},
		top:        map[string]topInfo{},
	}
}

// normalizeHost lowercases and strips brackets/trailing dot.
func normalizeHost(h string) string {
	h = strings.TrimSuffix(strings.ToLower(strings.Trim(h, "[]")), ".")
	if i := strings.IndexByte(h, '%'); i >= 0 { // IPv6 zone
		h = h[:i]
	}
	return h
}

// classify returns the strictest class a host maps to. Names are resolved so a
// public-looking name pointing at 10.0.0.5 or 169.254.169.254 is caught; a
// resolution failure is treated as public (Chrome will fail the load itself).
func (p *netPolicy) classify(host string) hostClass {
	host = normalizeHost(host)
	if host == "" {
		return classPublic
	}
	if metadataHostnames[host] {
		return classBlocked
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return classLoopback
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return classifyAddr(a)
	}
	// Legacy IPv4 spellings (0x7f.1, 2130706433, 017700000001) are parsed by
	// Chrome as IPv4 but rejected by netip; resolve them through the system
	// resolver, which understands them too.
	p.mu.Lock()
	// Do not reuse hostname classifications: Chrome performs its own DNS lookup,
	// so caching a public result would widen the DNS-rebinding window.
	if l, busy := p.inflight[host]; busy {
		p.mu.Unlock()
		<-l.done
		return l.class
	}
	l := &lookup{done: make(chan struct{})}
	p.inflight[host] = l
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	ips, err := p.resolve(ctx, host)
	class := classBlocked
	if err == nil && len(ips) > 0 {
		class = classPublic
		for _, ip := range ips {
			if c := classifyAddr(ip); c > class {
				class = c
			}
		}
	}

	p.mu.Lock()
	l.class, l.ok = class, err == nil && len(ips) > 0
	delete(p.inflight, host)
	p.mu.Unlock()
	close(l.done)
	return class
}

// allowExplicit records that the agent — with the user's approval — navigated
// to rawURL directly. It arms a one-purpose token for that local host: the
// navigation itself may proceed, and so may requests from the local page it
// opens. The token dies at the next top-level navigation to a public host.
func (p *netPolicy) allowExplicit(rawURL string) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Hostname() == "" {
		return
	}
	host := normalizeHost(u.Hostname())
	switch p.classify(host) {
	case classLoopback:
		p.mu.Lock()
		p.loopbackTok = true
		p.mu.Unlock()
	case classPrivate:
		p.mu.Lock()
		p.privateTok[host] = true
		p.mu.Unlock()
	}
}

// forgetTarget drops what is known about a target that went away.
func (p *netPolicy) forgetTarget(targetID string) {
	p.mu.Lock()
	delete(p.top, targetID)
	p.mu.Unlock()
}

// check is checkRequest for a caller that knows nothing about the requester.
func (p *netPolicy) check(rawURL string) error { return p.checkRequest(rawURL, reqCtx{}) }

// checkRequest reports whether a request to rawURL may proceed. A nil error
// means allow. Errors wrap ErrBlockedURL and say why, so the agent can relay
// them.
func (p *netPolicy) checkRequest(rawURL string, rc reqCtx) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: unparseable request URL", ErrBlockedURL)
	}
	switch strings.ToLower(u.Scheme) {
	case "data", "blob", "about", "filesystem":
		return nil
	case "http", "https", "ws", "wss":
	default:
		return fmt.Errorf("%w: request scheme %q is not allowed", ErrBlockedURL, u.Scheme)
	}
	host := normalizeHost(u.Hostname())
	class := p.classify(host)
	if err := p.decide(class, host, rc); err != nil {
		return err
	}
	if rc.topNav && rc.targetID != "" {
		p.noteTop(rc.targetID, host, class)
	}
	return nil
}

func (p *netPolicy) decide(class hostClass, host string, rc reqCtx) error {
	switch class {
	case classPublic:
		return nil
	case classBlocked:
		return fmt.Errorf("%w: %s is a link-local/cloud-metadata address and is never reachable from the browser", ErrBlockedURL, host)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	token := p.loopbackTok
	what := "this machine"
	if class == classPrivate {
		token = p.privateTok[host]
		what = "the private network"
	}
	allowed := token
	if !rc.topNav {
		// A subresource, fetch, frame or worker request. Whichever page it
		// belongs to must itself be local: a public page never reaches local
		// addresses, whatever happened earlier in the session or in other
		// tabs. A requester whose page is unknown — a worker with no page above
		// it, a page that has not navigated yet — gets no local access at all.
		t, ok := p.top[rc.targetID]
		switch {
		case rc.targetID == "" || !ok || t.host == "":
			allowed = false
		case class == classLoopback:
			allowed = t.class == classLoopback
		default:
			allowed = t.host == host
		}
	}
	if allowed {
		return nil
	}
	return fmt.Errorf("%w: a page tried to reach %s (%s). Navigate to it explicitly first if that is intended; a public page can never reach it", ErrBlockedURL, host, what)
}

// noteTop records a page's new top-level destination. Going to a public host
// retires every explicit-navigation token.
func (p *netPolicy) noteTop(targetID, host string, class hostClass) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.top[targetID] = topInfo{host: host, class: class}
	if class == classPublic {
		p.loopbackTok = false
		p.privateTok = map[string]bool{}
	}
}
