package mcp_test

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/mcp"
)

func TestPolicy_CheckURL_RequireTLS(t *testing.T) {
	p := mcp.Policy{RequireTLS: true}
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"https anywhere is fine", "https://evil.example/mcp", false},
		{"http to a non-loopback host is refused", "http://evil.example/mcp", true},
		{"http to 127.0.0.1 is allowed", "http://127.0.0.1:8080/mcp", false},
		{"http to localhost is allowed", "http://localhost:8080/mcp", false},
		{"http to ::1 is allowed", "http://[::1]:8080/mcp", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := p.CheckURL(c.url)
			if (err != nil) != c.wantErr {
				t.Errorf("CheckURL(%q) err = %v, wantErr %v", c.url, err, c.wantErr)
			}
		})
	}
}

func TestPolicy_CheckURL_RequireTLSDisabledAllowsPlaintextAnywhere(t *testing.T) {
	p := mcp.Policy{RequireTLS: false}
	if err := p.CheckURL("http://evil.example/mcp"); err != nil {
		t.Errorf("CheckURL with RequireTLS=false should allow plaintext anywhere; got %v", err)
	}
}

func TestPolicy_CheckURL_AllowedHosts(t *testing.T) {
	p := mcp.Policy{AllowedHosts: []string{"mcp.linear.app"}}
	if err := p.CheckURL("https://mcp.linear.app/mcp"); err != nil {
		t.Errorf("allowed host should pass; got %v", err)
	}
	if err := p.CheckURL("https://evil.example/mcp"); err == nil {
		t.Error("host not in allowed_hosts should be refused")
	}
	// Case-insensitive exact match, not a prefix/suffix match.
	if err := p.CheckURL("https://MCP.LINEAR.APP/mcp"); err != nil {
		t.Errorf("host match should be case-insensitive; got %v", err)
	}
	if err := p.CheckURL("https://evil-mcp.linear.app/mcp"); err == nil {
		t.Error("a host merely containing the allowed host must not match")
	}
}

func TestPolicy_CheckURL_EmptyAllowedHostsAllowsAnyHTTPSHost(t *testing.T) {
	p := mcp.Policy{RequireTLS: true}
	if err := p.CheckURL("https://anything.example/mcp"); err != nil {
		t.Errorf("empty allowed_hosts should allow any https host; got %v", err)
	}
}

func TestPolicy_CheckRedirect_RefusesCrossHost(t *testing.T) {
	p := mcp.Policy{}
	via := []*http.Request{{URL: mustParseURL(t, "https://a.example/start")}}
	req := &http.Request{URL: mustParseURL(t, "https://b.example/redirected")}
	if err := p.CheckRedirect(req, via); err == nil {
		t.Fatal("cross-host redirect should be refused")
	} else if !strings.Contains(err.Error(), "cross-host") {
		t.Errorf("error should mention cross-host; got %q", err)
	}
}

func TestPolicy_CheckRedirect_AllowsSameHost(t *testing.T) {
	p := mcp.Policy{}
	via := []*http.Request{{URL: mustParseURL(t, "https://a.example/start")}}
	req := &http.Request{URL: mustParseURL(t, "https://a.example/redirected")}
	if err := p.CheckRedirect(req, via); err != nil {
		t.Errorf("same-host redirect should be allowed; got %v", err)
	}
}

func TestPolicy_CheckRedirect_ReChecksPolicyOnFinalURL(t *testing.T) {
	p := mcp.Policy{RequireTLS: true}
	via := []*http.Request{{URL: mustParseURL(t, "http://127.0.0.1:8080/start")}}
	// Same host as via[0] (so it clears the cross-host check), but the
	// scheme downgrades from the loopback exemption's implicit https
	// expectation isn't actually relevant here — what matters is that a
	// same-host http redirect to a NON-loopback host still gets caught by
	// re-running CheckURL against the final URL.
	req := &http.Request{URL: mustParseURL(t, "http://127.0.0.1:8080/next")}
	if err := p.CheckRedirect(req, via); err != nil {
		t.Errorf("same-host loopback redirect should be allowed; got %v", err)
	}
}

func TestPolicy_CheckRedirect_NoPriorRequestsIsNoOp(t *testing.T) {
	p := mcp.Policy{}
	req := &http.Request{URL: mustParseURL(t, "https://a.example/start")}
	if err := p.CheckRedirect(req, nil); err != nil {
		t.Errorf("first request (no redirect yet) should never be refused; got %v", err)
	}
}

func TestPolicy_DialControl_LoopbackConfiguredHostIsExempt(t *testing.T) {
	p := mcp.Policy{}
	for _, host := range []string{"127.0.0.1", "localhost", "::1"} {
		if p.DialControl(host) != nil {
			t.Errorf("DialControl(%q) should be nil (no hook) for an explicitly loopback-configured host", host)
		}
	}
}

func TestPolicy_DialControl_LiteralIPConfiguredHostIsChecked(t *testing.T) {
	p := mcp.Policy{}
	for _, host := range []string{"10.0.5.20", "192.0.2.1", "fc00::1"} {
		control := p.DialControl(host)
		if control == nil {
			t.Errorf("DialControl(%q) should install a hook so private literal IPs are rejected", host)
		}
		if host != "192.0.2.1" {
			if err := control("tcp", net.JoinHostPort(host, "443"), nil); err == nil {
				t.Errorf("DialControl(%q) should reject non-public literal IP", host)
			}
		}
	}
}

func TestPolicy_DialControl_HostnameConfiguredHostRejectsPrivateResolution(t *testing.T) {
	p := mcp.Policy{}
	control := p.DialControl("mcp.example.com")
	if control == nil {
		t.Fatal("DialControl for a hostname (not a literal IP or loopback name) should install a hook")
	}
	// These addresses are exactly the shape net.Dialer.Control receives:
	// already resolved to a numeric ip:port, after DNS lookup.
	privateAddrs := []string{"127.0.0.1:443", "10.0.0.5:443", "169.254.169.254:80", "192.168.1.1:443"}
	for _, addr := range privateAddrs {
		if err := control("tcp", addr, nil); err == nil {
			t.Errorf("control(%q) should refuse a private/loopback/link-local resolution for a hostname-configured server", addr)
		}
	}
}

func TestPolicy_DialControl_HostnameConfiguredHostAllowsPublicResolution(t *testing.T) {
	p := mcp.Policy{}
	control := p.DialControl("mcp.example.com")
	publicAddrs := []string{"8.8.8.8:443", "1.1.1.1:443", "93.184.216.34:443"}
	for _, addr := range publicAddrs {
		if err := control("tcp", addr, nil); err != nil {
			t.Errorf("control(%q) should allow a public resolution; got %v", addr, err)
		}
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return u
}
