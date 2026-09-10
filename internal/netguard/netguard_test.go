package netguard

import (
	"net"
	"testing"
)

// TestIsNonPublicIP spot-checks the blocked ranges, including the
// 169.254.169.254 cloud-metadata endpoint.
func TestIsNonPublicIP(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "::1", "169.254.169.254", "10.0.0.5", "192.168.1.1", "172.16.0.1", "0.0.0.0"} {
		if !IsNonPublicIP(net.ParseIP(s)) {
			t.Errorf("IsNonPublicIP(%s) = false, want true (blocked)", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "93.184.216.34"} {
		if IsNonPublicIP(net.ParseIP(s)) {
			t.Errorf("IsNonPublicIP(%s) = true, want false (public)", s)
		}
	}
}
