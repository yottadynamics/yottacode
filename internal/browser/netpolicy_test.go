package browser

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPolicy(hosts map[string][]string) *netPolicy {
	p := newNetPolicy()
	p.resolve = func(_ context.Context, host string) ([]netip.Addr, error) {
		var out []netip.Addr
		for _, s := range hosts[host] {
			out = append(out, netip.MustParseAddr(s))
		}
		if out == nil {
			return nil, errors.New("no such host")
		}
		return out, nil
	}
	return p
}

func TestClassifyAddr(t *testing.T) {
	cases := map[string]hostClass{
		"8.8.8.8":                classPublic,
		"2606:4700::1111":        classPublic,
		"127.0.0.1":              classLoopback,
		"::1":                    classLoopback,
		"::ffff:127.0.0.1":       classLoopback,
		"10.1.2.3":               classPrivate,
		"172.16.0.1":             classPrivate,
		"192.168.1.1":            classPrivate,
		"100.64.0.1":             classPrivate,
		"fd12::1":                classPrivate,
		"0.0.0.0":                classPrivate,
		"169.254.169.254":        classBlocked,
		"169.254.170.2":          classBlocked,
		"fe80::1":                classBlocked,
		"100.100.100.200":        classBlocked,
		"168.63.129.16":          classBlocked,
		"::ffff:169.254.169.254": classBlocked,
	}
	for in, want := range cases {
		if got := classifyAddr(netip.MustParseAddr(in)); got != want {
			t.Errorf("classifyAddr(%s) = %v, want %v", in, got, want)
		}
	}
}

func TestNetPolicyPublicPageCannotReachInternal(t *testing.T) {
	p := testPolicy(map[string][]string{
		"example.com":      {"93.184.216.34"},
		"rebind.evil.test": {"10.0.0.5"},
		"meta.evil.test":   {"169.254.169.254"},
		"mixed.evil.test":  {"93.184.216.34", "127.0.0.1"},
	})
	allowed := []string{
		"https://example.com/x", "http://example.com:8080/", "data:text/plain,hi",
		"blob:https://example.com/abc", "about:blank", "wss://example.com/socket",
	}
	for _, u := range allowed {
		if err := p.check(u); err != nil {
			t.Errorf("check(%q) = %v, want allowed", u, err)
		}
	}
	denied := []string{
		"http://localhost:3000/", "http://127.0.0.1/", "http://[::1]/", "http://foo.localhost/",
		"http://10.0.0.1/", "http://192.168.0.1/admin", "http://[fd00::1]/",
		"http://169.254.169.254/latest/meta-data/", "http://metadata.google.internal/",
		"http://rebind.evil.test/", "http://meta.evil.test/", "http://mixed.evil.test/",
		"ftp://example.com/", "file:///etc/passwd", "chrome://settings",
	}
	for _, u := range denied {
		err := p.check(u)
		if !errors.Is(err, ErrBlockedURL) {
			t.Errorf("check(%q) = %v, want ErrBlockedURL", u, err)
		}
	}
}

func TestNetPolicyExplicitNavigationOpensOnlyItsClass(t *testing.T) {
	p := testPolicy(nil)

	p.allowExplicit("http://localhost:3000/app")
	if err := p.checkRequest("http://localhost:3000/app", nav("T1")); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"http://localhost:8080/api", "http://127.0.0.1:9000/", "http://[::1]/"} {
		if err := p.checkRequest(u, sub("T1")); err != nil {
			t.Errorf("after explicit loopback navigation check(%q) = %v", u, err)
		}
	}
	if err := p.checkRequest("http://10.0.0.1/", sub("T1")); err == nil {
		t.Error("explicit loopback navigation must not open the private network")
	}

	p.allowExplicit("http://192.168.1.20:5000/")
	if err := p.checkRequest("http://192.168.1.20:5000/", nav("T2")); err != nil {
		t.Fatal(err)
	}
	if err := p.checkRequest("http://192.168.1.20:5000/api", sub("T2")); err != nil {
		t.Errorf("explicitly opened private host: %v", err)
	}
	if err := p.checkRequest("http://192.168.1.21/", sub("T2")); err == nil {
		t.Error("a different private host must stay blocked")
	}

	// Metadata is never opened, whatever the agent navigated to.
	p.allowExplicit("http://169.254.169.254/")
	if err := p.checkRequest("http://169.254.169.254/latest/meta-data/", sub("T1")); err == nil {
		t.Error("metadata endpoint must stay blocked after an explicit navigation")
	}
}

func TestCheckNavigableURLRefusesMetadata(t *testing.T) {
	for _, u := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://metadata.google.internal/computeMetadata/v1/",
		"http://[fe80::1]/",
		"http://[::ffff:169.254.169.254]/",
	} {
		if err := checkNavigableURL(u); !errors.Is(err, ErrBlockedURL) {
			t.Errorf("checkNavigableURL(%q) = %v, want ErrBlockedURL", u, err)
		}
	}
	for _, u := range []string{"http://localhost:3000/", "http://192.168.1.5/", "https://example.com/"} {
		if err := checkNavigableURL(u); err != nil {
			t.Errorf("checkNavigableURL(%q) = %v, want allowed", u, err)
		}
	}
}

func TestNetPolicyResolvesEachRequest(t *testing.T) {
	calls := 0
	p := newNetPolicy()
	p.resolve = func(context.Context, string) ([]netip.Addr, error) {
		calls++
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	for range 5 {
		_ = p.check("https://example.com/")
	}
	if calls != 5 {
		t.Errorf("resolver called %d times, want 5 (one per request)", calls)
	}
}

// The package must never write diagnostics to stderr: under the TUI that
// corrupts the screen. Use the console buffer or return errors instead.
func TestNoStderrWritesInProductionCode(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "os.Stderr") || strings.Contains(string(b), "\"DEBUG ") {
			t.Errorf("%s writes to stderr / has DEBUG output", f)
		}
	}
}
