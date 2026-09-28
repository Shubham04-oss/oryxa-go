package safehttp

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"syscall"
	"time"
)

const (
	// dialTimeout bounds looking a host up and connecting to it.
	dialTimeout = 10 * time.Second
	// minAttempt is the least time one of a host's addresses gets, however
	// many follow it.
	minAttempt = 2 * time.Second
	keepAlive  = 30 * time.Second
)

// lookupNetworks maps each network a Dialer dials to the address family it
// looks a host up in.
var lookupNetworks = map[string]string{"tcp": "ip", "tcp4": "ip4", "tcp6": "ip6"}

// Resolver looks a hostname up. *net.Resolver satisfies it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Dialer connects to public addresses only. It looks a hostname up once per
// dial and refuses the host outright if any address it resolves to is not
// public. Otherwise it connects to exactly those addresses, in the order the
// resolver gave. There is no second lookup, so a name that answers a public
// address first and a private one after (DNS rebinding) is never asked
// again.
//
// The zero Dialer is ready to use. Its DialContext suits an http.Transport.
// Give that transport no Proxy, or the proxy is the only address checked.
type Dialer struct {
	// Resolver looks hostnames up. When nil, the system's resolver does.
	Resolver Resolver

	// permit reports whether an address may be dialed. When nil, IsPublic
	// decides. Only this package's tests set it, to admit their own servers.
	permit func(netip.AddrPort) bool
	// connect makes a connection. When nil, a net.Dialer does. Only this
	// package's tests set it, to see what would have been dialed.
	connect func(ctx context.Context, network, address string) (net.Conn, error)
}

// DialContext connects to address, a host and a port, over tcp, tcp4 or
// tcp6. An address that is not public is an ErrRefused error, and nothing is
// sent to it.
func (d *Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	lookup, ok := lookupNetworks[network]
	if !ok {
		return nil, fmt.Errorf("safehttp: cannot dial %q, only tcp, tcp4 and tcp6", network)
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("safehttp: %w", err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return nil, fmt.Errorf("safehttp: %q has no valid port", address)
	}
	if host == "" {
		return nil, fmt.Errorf("%w: %q names no host", ErrRefused, address)
	}

	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	addrs, err := d.resolve(ctx, lookup, host)
	if err != nil {
		return nil, err
	}
	targets := make([]netip.AddrPort, len(addrs))
	for index, addr := range addrs {
		// Each address is checked as resolved, zone and all. An
		// IPv4-mapped one is then dialed as plain IPv4.
		if !d.allowed(netip.AddrPortFrom(addr, uint16(port))) {
			// The host, never the address: a name that resolves inside
			// the VPC is not for the caller to learn about.
			return nil, fmt.Errorf("%w: %s", ErrRefused, host)
		}
		targets[index] = netip.AddrPortFrom(addr.Unmap(), uint16(port))
	}
	return d.dialEach(ctx, network, targets)
}

// resolve answers an IP literal as it is, and looks anything else up.
func (d *Dialer) resolve(ctx context.Context, network, host string) ([]netip.Addr, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{addr}, nil
	}
	resolver := d.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addrs, err := resolver.LookupNetIP(ctx, network, host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, &net.DNSError{Err: "no addresses", Name: host, IsNotFound: true}
	}
	return addrs, nil
}

// dialEach tries each address in turn until one connects, and answers the
// first failure if none does.
func (d *Dialer) dialEach(ctx context.Context, network string, targets []netip.AddrPort) (net.Conn, error) {
	var first error
	for index, target := range targets {
		attempt, cancel := shareOf(ctx, len(targets)-index)
		conn, err := d.dial(attempt, network, target.String())
		cancel()
		if err == nil {
			return conn, nil
		}
		if first == nil {
			first = err
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, first
}

// shareOf gives one of the remaining addresses its share of the time ctx has
// left, and at least minAttempt, so an address that never answers cannot use
// up the time the others need.
func shareOf(ctx context.Context, remaining int) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok || remaining < 2 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, max(time.Until(deadline)/time.Duration(remaining), minAttempt))
}

func (d *Dialer) dial(ctx context.Context, network, address string) (net.Conn, error) {
	if d.connect != nil {
		return d.connect(ctx, network, address)
	}
	dialer := net.Dialer{KeepAlive: keepAlive, Control: d.control}
	return dialer.DialContext(ctx, network, address)
}

// control runs as a connection is made, on the address it is made to.
// DialContext has checked that address already. This makes sure nothing
// reaches the network through a Dialer without the check.
func (d *Dialer) control(_, address string, _ syscall.RawConn) error {
	target, err := netip.ParseAddrPort(address)
	if err != nil || !d.allowed(target) {
		return fmt.Errorf("%w: %s", ErrRefused, address)
	}
	return nil
}

func (d *Dialer) allowed(target netip.AddrPort) bool {
	if d.permit != nil {
		return d.permit(target)
	}
	return IsPublic(target.Addr())
}
