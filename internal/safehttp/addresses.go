package safehttp

// This file is the one rule every connection answers to: IsPublic.

import "net/netip"

var (
	// refusedIPv4 are the IPv4 ranges that are not the public internet:
	// IANA's special-purpose ranges that are not globally reachable, and
	// multicast.
	refusedIPv4 = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),       // "this network"; Linux dials 0.0.0.0 as the local host
		netip.MustParsePrefix("10.0.0.0/8"),      // private, RFC 1918
		netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT, RFC 6598
		netip.MustParsePrefix("127.0.0.0/8"),     // loopback
		netip.MustParsePrefix("169.254.0.0/16"),  // link-local: the task's metadata and credential endpoints
		netip.MustParsePrefix("172.16.0.0/12"),   // private, RFC 1918
		netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
		netip.MustParsePrefix("192.0.2.0/24"),    // documentation
		netip.MustParsePrefix("192.168.0.0/16"),  // private, RFC 1918
		netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
		netip.MustParsePrefix("198.51.100.0/24"), // documentation
		netip.MustParsePrefix("203.0.113.0/24"),  // documentation
		netip.MustParsePrefix("224.0.0.0/4"),     // multicast
		netip.MustParsePrefix("240.0.0.0/4"),     // reserved, and the broadcast address
	}

	// globalIPv6 is where every public IPv6 address is allocated from.
	// Everything outside it is refused: loopback, unspecified, the
	// IPv4-compatible and translated forms, discard-only, unique local
	// (fc00::/7), link-local, site-local and multicast.
	globalIPv6 = netip.MustParsePrefix("2000::/3")

	// refusedIPv6 are the parts of globalIPv6 that are not the public
	// internet.
	refusedIPv6 = []netip.Prefix{
		netip.MustParsePrefix("2001::/23"),     // IETF protocol assignments: Teredo, benchmarking, ORCHID
		netip.MustParsePrefix("2001:db8::/32"), // documentation
		netip.MustParsePrefix("3fff::/20"),     // documentation
	}

	// nat64 is the well-known NAT64 prefix, RFC 6052: a NAT64 gateway
	// reaches the IPv4 address in its last four bytes.
	nat64 = netip.MustParsePrefix("64:ff9b::/96")
	// sixToFour is 6to4, RFC 3056: bytes two to five are an IPv4 address.
	sixToFour = netip.MustParsePrefix("2002::/16")
)

// IsPublic reports whether addr is on the public internet, the only place a
// Dialer connects to. It refuses loopback, private (RFC 1918) and unique
// local (fc00::/7), link-local (169.254.169.254 among them), carrier-grade
// NAT, unspecified, multicast, broadcast, reserved and documentation
// addresses, and every IPv6 address outside 2000::/3. An IPv6 address that
// carries an IPv4 one (IPv4-mapped, NAT64 or 6to4) is judged by that IPv4
// address, so a refused address cannot hide behind an IPv6 form. An address
// with a zone is refused, since only a local address has one.
//
// Checking a URL's host with IsPublic does not replace dialing through a
// Dialer: by the time it is dialed, a name can resolve to another address.
func IsPublic(addr netip.Addr) bool {
	if !addr.IsValid() || addr.Zone() != "" {
		return false
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	if addr.Is4() {
		return !containsAny(refusedIPv4, addr)
	}
	bytes := addr.As16()
	switch {
	case nat64.Contains(addr):
		return IsPublic(netip.AddrFrom4([4]byte(bytes[12:16])))
	case sixToFour.Contains(addr):
		return IsPublic(netip.AddrFrom4([4]byte(bytes[2:6])))
	}
	return globalIPv6.Contains(addr) && !containsAny(refusedIPv6, addr)
}

func containsAny(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
