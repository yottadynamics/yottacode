package browser

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func TestCheckNavigationURL_BlocksMetadataEndpoints(t *testing.T) {
	blocked := []string{
		"http://169.254.169.254/latest/meta-data/",
		"https://169.254.169.254",
		"http://169.254.169.254:8080/x",
		"http://169.254.170.2/v2/credentials", // ECS task credentials, same /16
		"http://169.254.1.1/",                 // anywhere in link-local
		"http://metadata.google.internal/computeMetadata/v1/",
		"http://METADATA.GOOGLE.INTERNAL./x", // case + trailing dot
		"http://metadata.goog/",
		"http://instance-data/latest",
		"http://[fd00:ec2::254]/latest",
		"http://100.100.100.200/latest/meta-data",
		"http://168.63.129.16/machine",
		"http://[::ffff:169.254.169.254]/", // IPv4-mapped IPv6
		// Legacy IPv4 spellings Chrome resolves to 169.254.169.254.
		"http://2852039166/",
		"http://0xA9FEA9FE/",
		"http://0251.0376.0251.0376/",
		"http://0xa9.0xfe.0xa9.0xfe/",
		"http://169.254.43518/", // 3-part form
		"  http://169.254.169.254/  ",
	}
	for _, u := range blocked {
		if err := checkNavigationURL(u); !errors.Is(err, ErrBlockedURL) {
			t.Errorf("checkNavigationURL(%q) = %v, want ErrBlockedURL", u, err)
		}
	}
}

func TestCheckNavigationURL_AllowsOrdinaryTargets(t *testing.T) {
	// Loopback and private ranges are NOT blocked: localhost:3000 is this
	// tool's main use case.
	allowed := []string{
		"http://localhost:3000/",
		"http://127.0.0.1:8080/x",
		"http://[::1]:3000/",
		"http://192.168.1.10/admin",
		"http://10.0.0.5/",
		"https://example.com/",
		"https://example.com/169.254.169.254", // in the path, not the host
		"https://169.254.169.254.example.com/",
		"http://169.253.1.1/", // just outside the /16
		"http://1.2.3.4/",
		"file:///etc/hosts", // scheme policy is the approval layer's job
		"about:blank",
		"not a url at all",
		"",
	}
	for _, u := range allowed {
		if err := checkNavigationURL(u); err != nil {
			t.Errorf("checkNavigationURL(%q) = %v, want nil", u, err)
		}
	}
}

func TestParseHostAddr_LegacyForms(t *testing.T) {
	cases := map[string]string{
		"2852039166":       "169.254.169.254",
		"0xa9fea9fe":       "169.254.169.254",
		"0251.254.169.254": "169.254.169.254",
		"127.1":            "127.0.0.1",
		"1.2.3.4":          "1.2.3.4",
		"::1":              "::1",
	}
	for in, want := range cases {
		got, ok := parseHostAddr(in)
		if !ok || got.String() != want {
			t.Errorf("parseHostAddr(%q) = %v,%t want %s", in, got, ok, want)
		}
	}
	for _, in := range []string{"example.com", "1.2.3.4.5", "256.1.1.1", "1..2", "4294967296", "0xZZ", ""} {
		if got, ok := parseHostAddr(in); ok {
			t.Errorf("parseHostAddr(%q) = %v, want not an address", in, got)
		}
	}
}

func TestBlockedURLPatterns_CoverKnownEndpointsWithAndWithoutPort(t *testing.T) {
	have := strings.Join(blockedURLPatterns, "\n")
	for _, want := range []string{
		"*://169.254.169.254/*", "*://169.254.169.254:*/*",
		"*://metadata.google.internal/*",
		"*://[fd00:ec2::254]/*",
		"*://100.100.100.200/*",
	} {
		if !strings.Contains(have, want) {
			t.Errorf("blockedURLPatterns missing %q", want)
		}
	}
}

func TestTrackedPage_BlockedCounter(t *testing.T) {
	tp := &trackedPage{}
	before := tp.blockedCount()
	if _, ok := tp.blockedSince(before); ok {
		t.Fatal("nothing blocked yet")
	}
	tp.noteBlocked("http://169.254.169.254/a")
	tp.noteBlocked("http://169.254.169.254/b")
	if u, ok := tp.blockedSince(before); !ok || u != "http://169.254.169.254/b" {
		t.Errorf("blockedSince = %q,%t want the latest URL", u, ok)
	}
	if _, ok := tp.blockedSince(tp.blockedCount()); ok {
		t.Error("blockedSince(current count) must report nothing new")
	}
}

// withFakeDNS swaps the resolver for a table and records which names were looked up.
func withFakeDNS(t *testing.T, table map[string][]string, failFor ...string) (lookups *[]string) {
	t.Helper()
	orig := lookupHost
	var seen []string
	lookupHost = func(_ context.Context, host string) ([]netip.Addr, error) {
		seen = append(seen, host)
		for _, f := range failFor {
			if host == f {
				return nil, errors.New("no such host")
			}
		}
		var out []netip.Addr
		for _, a := range table[host] {
			out = append(out, netip.MustParseAddr(a))
		}
		return out, nil
	}
	t.Cleanup(func() { lookupHost = orig })
	return &seen
}

func TestCheckNavigationTarget_RefusesANameThatResolvesToAMetadataAddress(t *testing.T) {
	withFakeDNS(t, map[string][]string{
		"169-254-169-254.nip.example": {"169.254.169.254"},
		"mixed.example":               {"93.184.216.34", "169.254.170.2"}, // one bad answer among good ones
		"v6.example":                  {"fd00:ec2::254"},
		"mapped.example":              {"::ffff:169.254.169.254"},
		"fine.example":                {"93.184.216.34"},
		"private.example":             {"10.0.0.5"}, // private is allowed: localhost:3000 is the use case
	})
	for _, u := range []string{
		"http://169-254-169-254.nip.example/latest/meta-data",
		"https://mixed.example/",
		"http://v6.example:8080/x",
		"http://mapped.example/",
	} {
		err := checkNavigationTarget(context.Background(), u)
		if !errors.Is(err, ErrBlockedURL) || !strings.Contains(err.Error(), "resolves to") {
			t.Errorf("checkNavigationTarget(%q) = %v, want ErrBlockedURL naming what it resolves to", u, err)
		}
	}
	for _, u := range []string{"https://fine.example/", "http://private.example:3000/"} {
		if err := checkNavigationTarget(context.Background(), u); err != nil {
			t.Errorf("checkNavigationTarget(%q) = %v, want nil", u, err)
		}
	}
}

func TestCheckNavigationTarget_LiteralChecksNeedNoDNSAndUnresolvableNamesPass(t *testing.T) {
	lookups := withFakeDNS(t, nil, "gone.example")

	// Literal hosts are judged without a lookup.
	if err := checkNavigationTarget(context.Background(), "http://169.254.169.254/"); !errors.Is(err, ErrBlockedURL) {
		t.Errorf("literal metadata IP: %v", err)
	}
	for _, u := range []string{"http://127.0.0.1:3000/", "http://[::1]/", "http://localhost:3000/", "http://app.localhost/", "about:blank", "", "not a url"} {
		if err := checkNavigationTarget(context.Background(), u); err != nil {
			t.Errorf("checkNavigationTarget(%q) = %v", u, err)
		}
	}
	if len(*lookups) != 0 {
		t.Errorf("literal and local hosts must not be resolved, but looked up %v", *lookups)
	}

	// A name that does not resolve is the browser's problem, not a refusal.
	if err := checkNavigationTarget(context.Background(), "http://gone.example/"); err != nil {
		t.Errorf("an unresolvable name should pass through: %v", err)
	}
}

func TestManager_NavigateRefusesANameResolvingToMetadataWithoutLaunching(t *testing.T) {
	withFakeDNS(t, map[string][]string{"evil.example": {"169.254.169.254"}})
	fake := &fakeSession{}
	m := newTestManager(fake, t.TempDir())
	if _, err := m.Navigate(context.Background(), "http://evil.example/", "load"); !errors.Is(err, ErrBlockedURL) {
		t.Fatalf("Navigate: err = %v, want ErrBlockedURL", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("a refused name must not touch the session: %v", fake.calls)
	}
	if _, err := m.Download(context.Background(), "", "http://evil.example/f", t.TempDir()+"/o"); !errors.Is(err, ErrBlockedURL) {
		t.Errorf("Download by URL: err = %v, want ErrBlockedURL", err)
	}
}
