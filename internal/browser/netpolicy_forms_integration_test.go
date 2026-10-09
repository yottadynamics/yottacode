//go:build integration

package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Decimal, hex, octal and short-form IPv4 spellings, and IPv4-mapped IPv6, are
// all ways to name 10.0.0.1 or 169.254.169.254 that a string comparison would
// miss. Chrome canonicalizes the URL before the guard sees it; this pins that
// the policy therefore refuses every one of them.
func TestIntegration_NetworkPolicyRefusesObfuscatedAddresses(t *testing.T) {
	skipIfNoBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<script>
for (const u of [
  'http://167.772.161/x', 'http://167772161/dec', 'http://0xa000001/hex', 'http://012.0.0.1/oct',
  'http://10.1/short', 'http://2852039166/meta-dec', 'http://0xA9FEA9FE/meta-hex', 'http://[::ffff:a00:1]/mapped',
]) fetch(u).catch(() => {});
</script>`)
	}))
	t.Cleanup(srv.Close)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })
	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)

	reqs, _ := m.NetworkRequests(ctx, 0)
	seen := 0
	for _, r := range reqs {
		if r.URL == srv.URL+"/" || r.URL == srv.URL+"/favicon.ico" {
			continue
		}
		seen++
		if !r.Failed {
			t.Errorf("request to %s was not refused (status %d)", r.URL, r.Status)
		}
	}
	if seen < 7 {
		t.Errorf("only %d probe requests observed; the page should have made 7 or 8", seen)
	}
}
