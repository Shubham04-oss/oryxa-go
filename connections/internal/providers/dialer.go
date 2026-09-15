package providers

// This file is the outbound address check every provider HTTP call goes
// through: NewHTTPClient refuses to connect to a non-public address, even one
// a provider hostname resolves to only after DNS lookup, so a provider
// endpoint (built-in or a custom OpenAPI one) can never reach private
// networks or cloud metadata.

import (
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// NewHTTPClient returns the client provider calls use: bounded in time,
// never following a redirect away from the endpoint it was given, never
// proxied, and never connecting to a non-public address.
func NewHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: requestTimeout, Control: controlPublicAddress}
	return &http.Client{
		Timeout: requestTimeout,
		// Proxy is nil so an environment proxy cannot redirect a provider
		// call to an address the dialer's Control func would have refused.
		Transport: &http.Transport{
			Proxy: nil, DialContext: dialer.DialContext, ForceAttemptHTTP2: true,
			TLSHandshakeTimeout: requestTimeout, IdleConnTimeout: 90 * time.Second, MaxIdleConnsPerHost: 10,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// controlPublicAddress is a net.Dialer Control func. It runs after DNS
// resolution, once for each address the dialer is about to connect to, and
// before the connection is made, so a hostname that resolves to a private or
// cloud-metadata address is refused before any bytes go out.
func controlPublicAddress(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: dial address is invalid", domain.ErrProviderUnavailable)
	}
	ip := net.ParseIP(host)
	if ip == nil || !isPublicAddress(ip) {
		return fmt.Errorf("%w: refusing to dial a non-public address", domain.ErrProviderUnavailable)
	}
	return nil
}

var (
	// cgnatBlock is the carrier-grade NAT range, RFC 6598.
	cgnatBlock = net.IPNet{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)}
	// thisNetworkBlock is 0.0.0.0/8, which Linux dials as the local host.
	thisNetworkBlock = net.IPNet{IP: net.IPv4(0, 0, 0, 0).To4(), Mask: net.CIDRMask(8, 32)}
	// nat64Block is the well-known NAT64 prefix, RFC 6052: its last four
	// bytes are the IPv4 address a NAT64 gateway reaches.
	nat64Block = net.IPNet{IP: net.ParseIP("64:ff9b::"), Mask: net.CIDRMask(96, 128)}
	// sixToFourBlock is 6to4, RFC 3056: bytes two to five are an IPv4
	// address.
	sixToFourBlock = net.IPNet{IP: net.ParseIP("2002::"), Mask: net.CIDRMask(16, 128)}
)

// isPublicAddress reports whether ip may be dialed. It rejects loopback,
// private (RFC 1918), link-local, unique local (RFC 4193), carrier-grade NAT
// (RFC 6598), 0.0.0.0/8, unspecified, multicast, and the IPv4 broadcast
// address. An IPv6 address that carries an IPv4 one (IPv4-mapped,
// IPv4-compatible, NAT64 or 6to4) is also judged by that IPv4 address, so it
// cannot hide a blocked address behind an IPv6 wrapper.
func isPublicAddress(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	if len(ip) == net.IPv6len {
		if embedded := embeddedIPv4(ip); embedded != nil && !isPublicAddress(embedded) {
			return false
		}
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	if len(ip) == net.IPv4len && (ip.Equal(net.IPv4bcast) || cgnatBlock.Contains(ip) || thisNetworkBlock.Contains(ip)) {
		return false
	}
	return true
}

// embeddedIPv4 returns the IPv4 address an IPv6 address carries through
// NAT64, 6to4 or the deprecated IPv4-compatible form, or nil.
func embeddedIPv4(ip net.IP) net.IP {
	switch {
	case nat64Block.Contains(ip):
		return net.IPv4(ip[12], ip[13], ip[14], ip[15]).To4()
	case sixToFourBlock.Contains(ip):
		return net.IPv4(ip[2], ip[3], ip[4], ip[5]).To4()
	case isZero(ip[:12]) && !isZero(ip[12:]):
		return net.IPv4(ip[12], ip[13], ip[14], ip[15]).To4()
	}
	return nil
}

func isZero(value []byte) bool {
	for _, b := range value {
		if b != 0 {
			return false
		}
	}
	return true
}
