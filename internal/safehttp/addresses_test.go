package safehttp

import (
	"fmt"
	"net/netip"
	"testing"
)

// ipv4Cases are judged as they are and in every IPv6 form that carries an
// IPv4 address: IPv4-mapped, NAT64 and 6to4.
var ipv4Cases = []struct {
	ip     string
	public bool
}{
	{"93.184.216.34", true},    // public
	{"8.8.8.8", true},          // public
	{"11.0.0.1", true},         // just outside 10/8
	{"172.15.255.255", true},   // just below 172.16/12
	{"172.32.0.1", true},       // just above 172.16/12
	{"100.63.255.255", true},   // just below carrier-grade NAT
	{"100.128.0.1", true},      // just above carrier-grade NAT
	{"169.253.255.255", true},  // just below link-local
	{"192.88.99.1", true},      // the old 6to4 relay anycast, still routed
	{"198.20.0.1", true},       // just above benchmarking
	{"223.255.255.255", true},  // just below multicast
	{"0.0.0.0", false},         // unspecified
	{"0.1.2.3", false},         // 0.0.0.0/8, dialed as the local host
	{"10.0.0.1", false},        // private, RFC 1918
	{"10.255.255.255", false},  // private, RFC 1918
	{"100.64.0.1", false},      // carrier-grade NAT, RFC 6598
	{"100.127.255.255", false}, // carrier-grade NAT, RFC 6598
	{"127.0.0.1", false},       // loopback
	{"127.53.0.1", false},      // loopback range
	{"169.254.169.254", false}, // link-local: the EC2 metadata service
	{"169.254.170.2", false},   // link-local: the ECS task credential endpoint
	{"169.254.169.253", false}, // link-local: the Amazon DNS server
	{"172.16.0.1", false},      // private, RFC 1918
	{"172.31.255.255", false},  // private, RFC 1918
	{"192.0.0.1", false},       // IETF protocol assignments
	{"192.0.2.1", false},       // documentation
	{"192.168.1.1", false},     // private, RFC 1918
	{"198.18.0.1", false},      // benchmarking
	{"198.51.100.1", false},    // documentation
	{"203.0.113.1", false},     // documentation
	{"224.0.0.1", false},       // multicast
	{"239.255.255.250", false}, // multicast
	{"240.0.0.1", false},       // reserved
	{"255.255.255.255", false}, // limited broadcast
}

var ipv6Cases = []struct {
	ip     string
	public bool
}{
	{"2606:2800:220:1:248:1893:25c8:1946", true}, // public
	{"2606:4700:4700::1111", true},               // public
	{"::", false},                                // unspecified
	{"::1", false},                               // loopback
	{"::a00:1", false},                           // IPv4-compatible private
	{"::5db8:d822", false},                       // IPv4-compatible, deprecated even when public
	{"::ffff:0:a00:1", false},                    // IPv4-translated, RFC 2765
	{"64:ff9b:1::a00:1", false},                  // local-use NAT64, RFC 8215
	{"100::1", false},                            // discard-only
	{"2001::1", false},                           // Teredo
	{"2001:2::1", false},                         // benchmarking
	{"2001:db8::1", false},                       // documentation
	{"3fff::1", false},                           // documentation
	{"fc00::1", false},                           // unique local, RFC 4193
	{"fd12:3456:789a::1", false},                 // unique local, RFC 4193
	{"fd00:ec2::254", false},                     // unique local: the EC2 metadata service over IPv6
	{"fe80::1", false},                           // link-local
	{"fe80::1%en0", false},                       // link-local with a zone
	{"2606:4700:4700::1111%en0", false},          // any zone
	{"::ffff:93.184.216.34%en0", false},          // any zone, even on a mapped public address
	{"fec0::1", false},                           // site-local, deprecated
	{"ff02::1", false},                           // link-local multicast
	{"ff05::1", false},                           // site-local multicast
	{"ff0e::1", false},                           // global multicast
}

func TestIsPublic(t *testing.T) {
	t.Parallel()
	for _, tc := range ipv4Cases {
		v4 := netip.MustParseAddr(tc.ip)
		b := v4.As4()
		forms := map[string]netip.Addr{
			"":        v4,
			"mapped ": netip.MustParseAddr("::ffff:" + tc.ip),
			"NAT64 ":  netip.MustParseAddr(fmt.Sprintf("64:ff9b::%02x%02x:%02x%02x", b[0], b[1], b[2], b[3])),
			"6to4 ":   netip.MustParseAddr(fmt.Sprintf("2002:%02x%02x:%02x%02x::1", b[0], b[1], b[2], b[3])),
		}
		for form, addr := range forms {
			if got := IsPublic(addr); got != tc.public {
				t.Errorf("IsPublic(%s%s = %s) = %v, want %v", form, tc.ip, addr, got, tc.public)
			}
		}
	}
	for _, tc := range ipv6Cases {
		if got := IsPublic(netip.MustParseAddr(tc.ip)); got != tc.public {
			t.Errorf("IsPublic(%s) = %v, want %v", tc.ip, got, tc.public)
		}
	}
	if IsPublic(netip.Addr{}) {
		t.Error("IsPublic(the zero Addr) = true")
	}
}
