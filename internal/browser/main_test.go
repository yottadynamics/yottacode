package browser

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"testing"
)

// TestMain keeps the package's tests hermetic: navigations to hostnames like
// example.com go through checkNavigationTarget, which resolves them, and a
// real lookup would make every such test depend on (and, on a runner that
// blackholes DNS, wait out) the network. A lookup that fails is let through,
// so failing instantly changes nothing but the wait. Tests that care about
// resolution install their own table with withFakeDNS.
func TestMain(m *testing.M) {
	lookupHost = func(context.Context, string) ([]netip.Addr, error) {
		return nil, errors.New("DNS is disabled in this package's tests")
	}
	os.Exit(m.Run())
}
