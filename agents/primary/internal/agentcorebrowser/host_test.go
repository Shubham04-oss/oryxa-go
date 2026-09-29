package agentcorebrowser

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	publicV4 = "93.184.216.34"
	publicV6 = "2606:2800:220:1:248:1893:25c8:1946"
)

// fakeResolver answers a host from its table, and a host it has no entry for
// as the system's resolver answers a name that does not exist, unless it has a
// wildcard: then every such host gets that answer, as it does from a resolver
// whose search domain answers any name.
type fakeResolver struct {
	answers  map[string][]netip.Addr
	wildcard []netip.Addr
	asked    []string
}

func (resolver *fakeResolver) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	if network != "ip" {
		return nil, &net.DNSError{Err: "asked for " + network + ", not both families", Name: host}
	}
	resolver.asked = append(resolver.asked, host)
	if answer, ok := resolver.answers[host]; ok {
		return answer, nil
	}
	if resolver.wildcard != nil {
		return resolver.wildcard, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func addresses(texts ...string) []netip.Addr {
	parsed := make([]netip.Addr, len(texts))
	for index, text := range texts {
		parsed[index] = netip.MustParseAddr(text)
	}
	return parsed
}

// publicResolver knows example.test and nothing else.
func publicResolver() *fakeResolver {
	return &fakeResolver{answers: map[string][]netip.Addr{"example.test": addresses(publicV4)}}
}

func navigation(rawURL string) Request { return Request{Action: Navigate, URL: rawURL} }

// TestBrowserRefusesAHostThatResolvesToANonPublicAddress: a name is judged by
// what it resolves to, and by every address it resolves to. It is refused
// when any of them is not public, whichever comes first, and when it
// resolves to none.
func TestBrowserRefusesAHostThatResolvesToANonPublicAddress(t *testing.T) {
	resolver := &fakeResolver{answers: map[string][]netip.Addr{
		"metadata.test":             addresses("169.254.169.254"),
		"internal.test":             addresses("10.1.2.3"),
		"loopback.test":             addresses("127.0.0.1"),
		"carrier-nat.test":          addresses("100.64.0.9"),
		"unspecified.test":          addresses("0.0.0.0"),
		"metadata-ipv6.test":        addresses("fd00:ec2::254"),
		"link-local6.test":          addresses("fe80::1"),
		"loopback6.test":            addresses("::1"),
		"mapped.test":               addresses("::ffff:169.254.169.254"),
		"nat64.test":                addresses("64:ff9b::a9fe:a9fe"),
		"6to4.test":                 addresses("2002:a9fe:a9fe::1"),
		"public-then-private.test":  addresses(publicV4, "10.0.0.5"),
		"private-then-public.test":  addresses("10.0.0.5", publicV4),
		"public-then-private6.test": addresses(publicV4, publicV6, "::1"),
		"answers-nothing.test":      {},
	}}
	for host := range resolver.answers {
		err := navigation("https://"+host+"/path?q=1").validate(t.Context(), resolver)
		if err == nil || !strings.Contains(err.Error(), "non-public address") && !strings.Contains(err.Error(), "could not be resolved") {
			t.Errorf("%s: validate error = %v, want a refusal", host, err)
		}
	}
	// A name the resolver does not know is refused too: there is nothing to
	// check it against.
	err := navigation("https://does-not-exist.test/").validate(t.Context(), resolver)
	if err == nil || !strings.Contains(err.Error(), "could not be resolved") {
		t.Errorf("an unknown host: validate error = %v, want it refused as unresolved", err)
	}
}

// TestBrowserRefusalNamesNoAddress: the host is in the refusal, the address it
// resolved to is not. Where a name points inside the VPC is not for the model
// to learn.
func TestBrowserRefusalNamesNoAddress(t *testing.T) {
	resolver := &fakeResolver{answers: map[string][]netip.Addr{"internal.test": addresses("10.1.2.3")}}
	err := navigation("https://internal.test/").validate(t.Context(), resolver)
	if err == nil || !strings.Contains(err.Error(), "internal.test") {
		t.Fatalf("validate error = %v, want it to name the host", err)
	}
	if strings.Contains(err.Error(), "10.1.2.3") {
		t.Fatalf("validate error = %v, want no address in it", err)
	}
}

// TestBrowserAdmitsAHostThatResolvesOnlyToPublicAddresses: the host it asks
// the resolver about is the browser's reading of it: lower case, without a
// port, a path or the dot that ends a fully qualified name.
func TestBrowserAdmitsAHostThatResolvesOnlyToPublicAddresses(t *testing.T) {
	resolver := &fakeResolver{answers: map[string][]netip.Addr{
		"example.test":                 addresses(publicV4),
		"dual.test":                    addresses(publicV4, publicV6),
		"xn--bcher-kva.test":           addresses(publicV6),
		"with_underscore.example.test": addresses(publicV4),
	}}
	for rawURL, wantHost := range map[string]string{
		"https://example.test":                  "example.test",
		"http://example.test/path?q=1#top":      "example.test",
		"https://EXAMPLE.test:8443/x":           "example.test",
		"https://example.test./":                "example.test",
		"https://dual.test/":                    "dual.test",
		"https://bücher.test/":                  "xn--bcher-kva.test",
		"https://with_underscore.example.test/": "with_underscore.example.test",
		"  https://example.test/padded  ":       "example.test",
	} {
		resolver.asked = nil
		if err := navigation(rawURL).validate(t.Context(), resolver); err != nil {
			t.Errorf("%q: validate error = %v", rawURL, err)
			continue
		}
		if !slices.Equal(resolver.asked, []string{wantHost}) {
			t.Errorf("%q: looked up %v, want [%s]", rawURL, resolver.asked, wantHost)
		}
	}
}

// TestBrowserRefusesAnIPAddressHoweverItIsWritten: an address, and a host a
// browser reads as one, is refused from the URL alone. The resolver here
// answers a public address for anything, as a search domain that answers any
// name would, and is never asked: nothing turns on what it says.
func TestBrowserRefusesAnIPAddressHoweverItIsWritten(t *testing.T) {
	for _, rawURL := range []string{
		// Literals, IPv4 and IPv6, in every range IsPublic refuses.
		"http://127.0.0.1/", "http://10.0.0.1/private", "http://172.16.0.1/", "http://192.168.1.1/",
		"http://169.254.169.254/latest/meta-data/", "http://100.64.0.1/", "http://0.0.0.0/",
		"http://224.0.0.1/", "http://255.255.255.255/", "http://198.18.0.1/",
		"http://[::1]/", "http://[::]/", "http://[fd00:ec2::254]/latest/meta-data/", "http://[fe80::1]/",
		"http://[fe80::1%25eth0]/", "http://[::ffff:169.254.169.254]/", "http://[::ffff:127.0.0.1]:8080/",
		"http://[64:ff9b::a9fe:a9fe]/", "http://[2002:a9fe:a9fe::1]/",
		// The same addresses as a browser reads them from other writings.
		"http://2130706433/", "http://2852039166/latest/meta-data/", "http://0xa9fea9fe/", "http://0XA9FEA9FE/",
		"http://0x7f.0.0.1/", "http://0177.0.0.1/", "http://127.1/", "http://169.254.43518/", "http://0/",
		"http://169.254.169.254./", "http://10.0.0.1.:8080/",
		"http://１２７.０.０.１/", "http://169。254。169。254/", "http://０ｘａ９ｆｅａ９ｆｅ/",
		// Names for the host itself.
		"http://localhost/", "http://LOCALHOST:8080/", "http://localhost./", "http://app.localhost/",
		"http://ｌｏｃａｌｈｏｓｔ/", "http://localhost。/",
	} {
		resolver := &fakeResolver{wildcard: addresses(publicV4)}
		if err := navigation(rawURL).validate(t.Context(), resolver); err == nil {
			t.Errorf("%q was admitted", rawURL)
		}
		if len(resolver.asked) != 0 {
			t.Errorf("%q: looked up %v, want the URL alone to decide", rawURL, resolver.asked)
		}
	}
}

// TestBrowserAdmitsAPublicIPAddress: the same reading admits a public address
// in the writings that mean one.
func TestBrowserAdmitsAPublicIPAddress(t *testing.T) {
	for _, rawURL := range []string{
		"http://" + publicV4 + "/", "https://8.8.8.8:8443/x", "http://8.8.8.8./", "http://1.1.1.1/",
		"http://[" + publicV6 + "]/", "https://[2001:4860:4860::8888]:8443/",
	} {
		resolver := &fakeResolver{}
		if err := navigation(rawURL).validate(t.Context(), resolver); err != nil {
			t.Errorf("%q: validate error = %v", rawURL, err)
		}
		if len(resolver.asked) != 0 {
			t.Errorf("%q: looked up %v, want an address taken as it is", rawURL, resolver.asked)
		}
	}
}

// TestBrowserRefusesAHostThatIsNoName: a URL with no host in it, and an
// international name the mapping refuses, is not sent to the resolver.
func TestBrowserRefusesAHostThatIsNoName(t *testing.T) {
	for _, rawURL := range []string{
		"http://:8080/", "http://./", "https://a\uFFFDb.test/", "https://-bad-.m\u00fcnchen.test/",
	} {
		resolver := &fakeResolver{wildcard: addresses(publicV4)}
		if err := navigation(rawURL).validate(t.Context(), resolver); err == nil {
			t.Errorf("%q was admitted", rawURL)
		}
		if len(resolver.asked) != 0 {
			t.Errorf("%q: looked up %v, want the URL alone to decide", rawURL, resolver.asked)
		}
	}
}

// TestBrowserHostLookupIsBoundedByTheCallersContext: a name server that never
// answers holds the lookup for no longer than the tool call.
func TestBrowserHostLookupIsBoundedByTheCallersContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	refused := make(chan error, 1)
	go func() { refused <- navigation("https://slow.test/").validate(ctx, silentResolver{}) }()
	select {
	case err := <-refused:
		if err == nil || !strings.Contains(err.Error(), "could not be resolved") {
			t.Fatalf("validate error = %v, want it refused as unresolved", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the lookup was not bound to the caller's context")
	}
}

// silentResolver never answers until it is told to stop waiting.
type silentResolver struct{}

func (silentResolver) LookupNetIP(ctx context.Context, _, _ string) ([]netip.Addr, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestOtherActionsLookNothingUp: only a navigation names a host.
func TestOtherActionsLookNothingUp(t *testing.T) {
	for _, request := range []Request{
		{Action: Snapshot}, {Action: Screenshot}, {Action: Click, Selector: "#go"},
		{Action: Fill, Selector: "input", Text: "hello"},
	} {
		resolver := &fakeResolver{}
		if err := request.validate(t.Context(), resolver); err != nil {
			t.Errorf("%s: validate error = %v", request.Action, err)
		}
		if len(resolver.asked) != 0 {
			t.Errorf("%s: looked up %v", request.Action, resolver.asked)
		}
	}
}

// TestManagerRefusesARebindingHostBeforeStartingABrowser: Invoke judges a
// navigation with the manager's own resolver, so a name that resolves to the
// metadata address gets no session at all, and a public one is passed on as it
// was written.
func TestManagerRefusesARebindingHostBeforeStartingABrowser(t *testing.T) {
	api := &fakeRuntime{}
	connector := &fakeConnector{}
	resolver := publicResolver()
	resolver.answers["rebind.test"] = addresses(publicV4, "169.254.169.254")
	manager, err := newManager(api, Options{connector: connector, resolver: resolver})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := manager.Invoke(t.Context(), "one", navigation("http://rebind.test/latest/meta-data/iam/security-credentials/")); err == nil {
		t.Fatal("a host that resolves to the metadata address was admitted")
	}
	if len(api.starts) != 0 || len(connector.clients) != 0 || manager.sessions["one"] != nil {
		t.Fatalf("a refused URL started a browser: starts = %+v, clients = %d", api.starts, len(connector.clients))
	}

	if _, err := manager.Invoke(t.Context(), "one", navigation("https://example.test/a?b=c")); err != nil {
		t.Fatalf("a public host was refused: %v", err)
	}
	if len(connector.clients) != 1 || len(connector.clients[0].requests) != 1 || connector.clients[0].requests[0].URL != "https://example.test/a?b=c" {
		t.Fatalf("browser requests = %+v", connector.clients)
	}
}

// TestNewManagerLooksHostsUpWithTheSystemResolverByDefault: assembly gives no
// resolver, and the manager must not be left without one.
func TestNewManagerLooksHostsUpWithTheSystemResolverByDefault(t *testing.T) {
	manager, err := newManager(&fakeRuntime{}, Options{connector: &fakeConnector{}})
	if err != nil {
		t.Fatal(err)
	}
	if manager.resolver != net.DefaultResolver {
		t.Fatalf("resolver = %#v, want the system's", manager.resolver)
	}
}
