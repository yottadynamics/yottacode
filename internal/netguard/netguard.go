// Package netguard holds small, dependency-free network-address checks
// shared by anything that dials a URL it doesn't fully control the
// destination of — a model-chosen fetch, or a hostname-configured remote
// server whose DNS could be rebound after the admin approved it. Kept
// separate from any one caller's package so internal/agent (fetch_url)
// and internal/mcp (remote MCP servers) share one definition instead of
// two copies that could quietly drift apart.
package netguard

import "net"

// IsNonPublicIP reports whether ip is loopback, link-local (this covers
// the 169.254.169.254 cloud-instance-metadata endpoint), private,
// multicast, or unspecified — the address ranges a connection to an
// untrusted or DNS-rebindable destination must never reach.
func IsNonPublicIP(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() ||
		ip.IsMulticast() ||
		ip.IsUnspecified()
}
