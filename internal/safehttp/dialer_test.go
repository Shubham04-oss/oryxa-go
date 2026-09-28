package safehttp

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeResolver answers each host from a table, in turn: the first lookup
// gets the first answer, and the last answer repeats.
type fakeResolver struct {
	mu       sync.Mutex
	answers  map[string][][]netip.Addr
	lookups  map[string]int
	networks []string
}

func newResolver() *fakeResolver {
	return &fakeResolver{answers: map[string][][]netip.Addr{}, lookups: map[string]int{}}
}

// answer adds what the next lookup of host answers.
func (r *fakeResolver) answer(host string, addrs ...string) *fakeResolver {
	answer := make([]netip.Addr, len(addrs))
	for index, addr := range addrs {
		answer[index] = netip.MustParseAddr(addr)
	}
	r.answers[host] = append(r.answers[host], answer)
	return r
}

func (r *fakeResolver) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.networks = append(r.networks, network)
	answers, ok := r.answers[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	answer := answers[min(r.lookups[host], len(answers)-1)]
	r.lookups[host]++
	return answer, nil
}

func (r *fakeResolver) count(host string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lookups[host]
}

// listen starts a loopback listener that counts the connections it accepts.
func listen(t *testing.T) (netip.AddrPort, *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	var accepted atomic.Int32
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = conn.Close()
		}
	}()
	return netip.MustParseAddrPort(listener.Addr().String()), &accepted
}

// admitting lets a Dialer reach exactly these addresses beyond what IsPublic
// allows.
func admitting(addrs ...netip.AddrPort) func(netip.AddrPort) bool {
	return func(target netip.AddrPort) bool { return slices.Contains(addrs, target) || IsPublic(target.Addr()) }
}

// recordingConnect stands in for the network: it records what would have
// been dialed, and connects only to succeed, over a pipe.
type recordingConnect struct {
	mu      sync.Mutex
	dialed  []string
	succeed string
}

var errNotDialed = errors.New("the test does not dial")

func (r *recordingConnect) connect(_ context.Context, _, address string) (net.Conn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dialed = append(r.dialed, address)
	if address == r.succeed {
		conn, other := net.Pipe()
		_ = other.Close()
		return conn, nil
	}
	return nil, errNotDialed
}

// TestDialerRefusesEveryRefusedAddress dials each refused address as a
// literal and as a name resolving to it, in each of its IPv6 forms, and
// proves none is dialed.
func TestDialerRefusesEveryRefusedAddress(t *testing.T) {
	t.Parallel()
	var refused []string
	for _, tc := range ipv4Cases {
		if !tc.public {
			refused = append(refused, tc.ip, "::ffff:"+tc.ip)
		}
	}
	for _, tc := range ipv6Cases {
		if !tc.public {
			refused = append(refused, tc.ip)
		}
	}
	for _, ip := range refused {
		recorder := &recordingConnect{}
		dialer := &Dialer{Resolver: newResolver().answer("refused.example", ip), connect: recorder.connect}
		for _, address := range []string{net.JoinHostPort(ip, "443"), "refused.example:443"} {
			if _, err := dialer.DialContext(context.Background(), "tcp", address); !errors.Is(err, ErrRefused) {
				t.Errorf("dial %s (%s) = %v, want ErrRefused", address, ip, err)
			}
		}
		if len(recorder.dialed) != 0 {
			t.Errorf("%s: dialed %v", ip, recorder.dialed)
		}
	}
}

// TestDialerRefusesRebindingName is DNS rebinding: a name that resolves to
// a loopback server. The server must never see a connection, and the error
// names the host, not the address it resolved to.
func TestDialerRefusesRebindingName(t *testing.T) {
	t.Parallel()
	addr, accepted := listen(t)
	dialer := &Dialer{Resolver: newResolver().answer("rebind.example", addr.Addr().String())}
	_, err := dialer.DialContext(context.Background(), "tcp", "rebind.example:"+portOf(addr))
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("dial = %v, want ErrRefused", err)
	}
	if strings.Contains(err.Error(), addr.Addr().String()) {
		t.Fatalf("the error reveals the resolved address: %v", err)
	}
	if accepted.Load() != 0 {
		t.Fatal("the loopback server was reached")
	}
}

// TestDialerRefusesAHostWithAnyRefusedAddress refuses a name that resolves
// to one admitted address and one refused one, rather than dialing whichever
// passes.
func TestDialerRefusesAHostWithAnyRefusedAddress(t *testing.T) {
	t.Parallel()
	addr, accepted := listen(t)
	dialer := &Dialer{Resolver: newResolver().answer("mixed.example", addr.Addr().String(), "10.0.0.7"), permit: admitting(addr)}
	if _, err := dialer.DialContext(context.Background(), "tcp", "mixed.example:"+portOf(addr)); !errors.Is(err, ErrRefused) {
		t.Fatalf("dial = %v, want ErrRefused", err)
	}
	if accepted.Load() != 0 {
		t.Fatal("the admitted address was dialed anyway")
	}
}

// TestDialerChecksEachDialsOwnLookup is rebinding between connections: each
// dial looks the name up once, and dials what that lookup answered.
func TestDialerChecksEachDialsOwnLookup(t *testing.T) {
	t.Parallel()
	addr, accepted := listen(t)
	resolver := newResolver().answer("rebind.example", addr.Addr().String()).answer("rebind.example", "169.254.169.254")
	dialer := &Dialer{Resolver: resolver, permit: admitting(addr)}

	conn, err := dialer.DialContext(context.Background(), "tcp", "rebind.example:"+portOf(addr))
	if err != nil {
		t.Fatalf("first dial: %v", err)
	}
	if got := conn.RemoteAddr().String(); got != addr.String() {
		t.Fatalf("connected to %s, want the resolved %s", got, addr)
	}
	_ = conn.Close()
	if got := resolver.count("rebind.example"); got != 1 {
		t.Fatalf("lookups after one dial = %d, want 1", got)
	}

	if _, err := dialer.DialContext(context.Background(), "tcp", "rebind.example:"+portOf(addr)); !errors.Is(err, ErrRefused) {
		t.Fatalf("second dial = %v, want ErrRefused", err)
	}
	if got := resolver.count("rebind.example"); got != 2 {
		t.Fatalf("lookups after two dials = %d, want 2", got)
	}
	if accepted.Load() > 1 {
		t.Fatalf("accepted %d connections, want 1", accepted.Load())
	}
}

// TestDialerTriesEachAddress dials a name's addresses in the order the
// resolver gave, until one connects.
func TestDialerTriesEachAddress(t *testing.T) {
	t.Parallel()
	resolver := newResolver().answer("two.example", "93.184.216.34", "93.184.216.35", "93.184.216.36")
	recorder := &recordingConnect{succeed: "93.184.216.35:443"}
	dialer := &Dialer{Resolver: resolver, connect: recorder.connect}
	conn, err := dialer.DialContext(context.Background(), "tcp", "two.example:443")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()
	if want := []string{"93.184.216.34:443", "93.184.216.35:443"}; !slices.Equal(recorder.dialed, want) {
		t.Fatalf("dialed %v, want %v", recorder.dialed, want)
	}

	recorder = &recordingConnect{}
	dialer.connect = recorder.connect
	if _, err := dialer.DialContext(context.Background(), "tcp", "two.example:443"); !errors.Is(err, errNotDialed) {
		t.Fatalf("dial with every address failing = %v, want the first failure", err)
	}
	if len(recorder.dialed) != 3 {
		t.Fatalf("dialed %v, want all three addresses", recorder.dialed)
	}
}

// TestDialerFallsBackOverTheNetwork does the same with real sockets: a
// refused connection moves on to the next address.
func TestDialerFallsBackOverTheNetwork(t *testing.T) {
	t.Parallel()
	closed := closedPort(t)
	addr, _ := listen(t)
	dialer := &Dialer{permit: admitting(closed, addr)}
	conn, err := dialer.dialEach(context.Background(), "tcp", []netip.AddrPort{closed, addr})
	if err != nil {
		t.Fatalf("dialEach: %v", err)
	}
	defer conn.Close()
	if got := conn.RemoteAddr().String(); got != addr.String() {
		t.Fatalf("connected to %s, want %s", got, addr)
	}
}

// TestDialerControlRefuses proves the last check, on the connection itself,
// holds even for an address that skipped DialContext's.
func TestDialerControlRefuses(t *testing.T) {
	t.Parallel()
	addr, accepted := listen(t)
	dialer := &Dialer{}
	if _, err := dialer.dial(context.Background(), "tcp", addr.String()); !errors.Is(err, ErrRefused) {
		t.Fatalf("dial past DialContext = %v, want ErrRefused", err)
	}
	if accepted.Load() != 0 {
		t.Fatal("the loopback server was reached")
	}
}

func TestDialerLooksUpTheNetworksFamily(t *testing.T) {
	t.Parallel()
	resolver := newResolver().answer("public.example", "93.184.216.34")
	recorder := &recordingConnect{}
	dialer := &Dialer{Resolver: resolver, connect: recorder.connect}
	for _, network := range []string{"tcp", "tcp4", "tcp6"} {
		if _, err := dialer.DialContext(context.Background(), network, "public.example:443"); !errors.Is(err, errNotDialed) {
			t.Fatalf("dial %s = %v, want the recorder's error", network, err)
		}
	}
	if want := []string{"ip", "ip4", "ip6"}; !slices.Equal(resolver.networks, want) {
		t.Fatalf("lookup networks = %v, want %v", resolver.networks, want)
	}
	if want := []string{"93.184.216.34:443", "93.184.216.34:443", "93.184.216.34:443"}; !slices.Equal(recorder.dialed, want) {
		t.Fatalf("dialed %v, want %v", recorder.dialed, want)
	}
}

func TestDialerRefusesWhatItCannotCheck(t *testing.T) {
	t.Parallel()
	recorder := &recordingConnect{}
	dialer := &Dialer{Resolver: newResolver().answer("public.example", "93.184.216.34"), connect: recorder.connect}
	for _, attempt := range []struct{ network, address string }{
		{"udp", "public.example:53"},
		{"unix", "/var/run/docker.sock"},
		{"ip", "public.example:443"},
		{"tcp", "public.example"},
		{"tcp", "public.example:0"},
		{"tcp", "public.example:65536"},
		{"tcp", "public.example:https"},
		{"tcp", ":443"},
	} {
		if _, err := dialer.DialContext(context.Background(), attempt.network, attempt.address); err == nil || errors.Is(err, errNotDialed) {
			t.Errorf("dial %s %q = %v, want a refusal", attempt.network, attempt.address, err)
		}
	}
	if len(recorder.dialed) != 0 {
		t.Fatalf("dialed %v", recorder.dialed)
	}
}

func TestDialerAnswersLookupFailures(t *testing.T) {
	t.Parallel()
	resolver := newResolver()
	resolver.answers["empty.example"] = [][]netip.Addr{{}}
	dialer := &Dialer{Resolver: resolver, connect: (&recordingConnect{}).connect}
	for _, host := range []string{"missing.example", "empty.example"} {
		var dnsError *net.DNSError
		if _, err := dialer.DialContext(context.Background(), "tcp", host+":443"); !errors.As(err, &dnsError) || !dnsError.IsNotFound {
			t.Fatalf("dial %s = %v, want a not-found DNS error", host, err)
		}
	}
}

func portOf(addr netip.AddrPort) string {
	_, port, _ := net.SplitHostPort(addr.String())
	return port
}

// closedPort answers a loopback address nothing listens on.
func closedPort(t *testing.T) netip.AddrPort {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := netip.MustParseAddrPort(listener.Addr().String())
	_ = listener.Close()
	return addr
}
