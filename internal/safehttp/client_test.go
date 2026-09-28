package safehttp

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// admit lets a client reach this test's own servers, which listen on
// loopback, and trust their certificates. Nothing else IsPublic refuses
// becomes reachable.
func admit(servers ...*httptest.Server) Option {
	return func(s *settings) {
		admitted := make([]netip.AddrPort, len(servers))
		roots := x509.NewCertPool()
		for index, server := range servers {
			admitted[index] = netip.MustParseAddrPort(server.Listener.Addr().String())
			if server.TLS != nil {
				roots.AddCert(server.Certificate())
			}
		}
		s.dialer.permit = admitting(admitted...)
		s.tlsConfig.RootCAs = roots
	}
}

// noDialing fails the test if the client dials anything at all.
func noDialing(t *testing.T) Option {
	return func(s *settings) {
		s.dialer.connect = func(_ context.Context, _, address string) (net.Conn, error) {
			t.Errorf("dialed %s", address)
			return nil, errNotDialed
		}
	}
}

// serve starts a server whose handler counts the requests it gets.
func serve(t *testing.T, secure bool, handler http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	counted := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		handler(w, r)
	})
	server := httptest.NewUnstartedServer(counted)
	if secure {
		server.StartTLS()
	} else {
		server.Start()
	}
	t.Cleanup(server.Close)
	return server, &hits
}

func ok(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }

// redirector redirects every request to its "to" query parameter.
func redirector(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, r.URL.Query().Get("to"), http.StatusFound)
}

// redirectVia is a URL on origin that redirects to target.
func redirectVia(origin *httptest.Server, target string) string {
	return origin.URL + "/?to=" + url.QueryEscape(target)
}

func get(t *testing.T, client *http.Client, address string) (*http.Response, error) {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, address, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	response, err := client.Do(request)
	if err == nil {
		t.Cleanup(func() { _ = response.Body.Close() })
	}
	return response, err
}

// TestClientRefusesLoopback is the plain case: every test server listens on
// loopback, and a client with nothing admitted must not reach one.
func TestClientRefusesLoopback(t *testing.T) {
	t.Parallel()
	server, hits := serve(t, false, ok)
	if _, err := get(t, NewClient(), server.URL); !errors.Is(err, ErrRefused) {
		t.Fatalf("get = %v, want ErrRefused", err)
	}
	if hits.Load() != 0 {
		t.Fatal("the loopback server was reached")
	}
}

// TestClientRefusesRefusedLiterals refuses a URL whose host is a refused
// address before dialing anything.
func TestClientRefusesRefusedLiterals(t *testing.T) {
	t.Parallel()
	client := NewClient(noDialing(t))
	for _, address := range []string{
		"http://169.254.169.254/latest/meta-data/iam/security-credentials/",
		"http://169.254.170.2/v2/credentials",
		"http://[fd00:ec2::254]/latest/meta-data/",
		"http://[::ffff:169.254.169.254]/",
		"https://10.0.0.1/",
		"https://[::1]:8443/",
		"https://[fe80::1%25en0]/",
		"http://0.0.0.0:8080/",
	} {
		if _, err := get(t, client, address); !errors.Is(err, ErrRefused) {
			t.Errorf("get %s = %v, want ErrRefused", address, err)
		}
	}
}

// TestClientRefusesRedirectsToRefusedAddresses follows a redirect from an
// admitted server toward a victim server that is not admitted, by address,
// by an IPv6 form of it, and by names that resolve to it. The victim must
// never be reached.
func TestClientRefusesRedirectsToRefusedAddresses(t *testing.T) {
	t.Parallel()
	origin, _ := serve(t, false, redirector)
	victim, victimHits := serve(t, false, ok)
	victimAddr := netip.MustParseAddrPort(victim.Listener.Addr().String())
	port := portOf(victimAddr)
	resolver := newResolver().
		answer("rebind.example", victimAddr.Addr().String()).
		answer("localhost", "127.0.0.1", "::1").
		answer("metadata.example", "169.254.169.254")
	client := NewClient(admit(origin), ResolveWith(resolver))

	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://" + victimAddr.String() + "/",
		"http://[::ffff:" + victimAddr.Addr().String() + "]:" + port + "/",
		"http://rebind.example:" + port + "/",
		"http://localhost:" + port + "/",
		"http://metadata.example/latest/meta-data/",
	} {
		response, err := get(t, client, redirectVia(origin, target))
		if !errors.Is(err, ErrRefused) {
			t.Errorf("redirect to %s = %v, want ErrRefused", target, err)
		}
		// When the redirect policy refuses, http.Client answers the
		// redirect itself beside the error, its body closed. Nothing else
		// may come back.
		if response != nil && response.StatusCode != http.StatusFound {
			t.Errorf("redirect to %s answered %d", target, response.StatusCode)
		}
	}
	if victimHits.Load() != 0 {
		t.Fatalf("the victim was reached %d times", victimHits.Load())
	}
}

func TestClientFollowsRedirectsItAllows(t *testing.T) {
	t.Parallel()
	target, targetHits := serve(t, false, ok)
	origin, _ := serve(t, false, redirector)
	client := NewClient(admit(origin, target))
	response, err := get(t, client, redirectVia(origin, target.URL+"/done"))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if response.StatusCode != http.StatusOK || targetHits.Load() != 1 {
		t.Fatalf("status %d after %d hits on the target, want 200 after 1", response.StatusCode, targetHits.Load())
	}
}

func TestClientLimitsRedirects(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		options []Option
		hits    int32
	}{
		{"by default", nil, 6},
		{"to two", []Option{MaxRedirects(2)}, 3},
	} {
		loop, hits := serve(t, false, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/again", http.StatusFound)
		})
		client := NewClient(append(tc.options, admit(loop))...)
		if _, err := get(t, client, loop.URL); !errors.Is(err, ErrTooManyRedirects) {
			t.Fatalf("%s: get = %v, want ErrTooManyRedirects", tc.name, err)
		}
		if hits.Load() != tc.hits {
			t.Fatalf("%s: %d requests, want %d", tc.name, hits.Load(), tc.hits)
		}
	}

	origin, originHits := serve(t, false, redirector)
	target, targetHits := serve(t, false, ok)
	response, err := get(t, NewClient(MaxRedirects(0), admit(origin, target)), redirectVia(origin, target.URL))
	if err != nil {
		t.Fatalf("get without following: %v", err)
	}
	if response.StatusCode != http.StatusFound || response.Header.Get("Location") != target.URL {
		t.Fatalf("answered %d to %q, want the redirect itself", response.StatusCode, response.Header.Get("Location"))
	}
	if originHits.Load() != 1 || targetHits.Load() != 0 {
		t.Fatalf("hits: origin %d, target %d; want 1 and 0", originHits.Load(), targetHits.Load())
	}
}

func TestClientHTTPSOnly(t *testing.T) {
	t.Parallel()
	plain, plainHits := serve(t, false, ok)
	secure, _ := serve(t, true, ok)
	downgrade, _ := serve(t, true, redirector)
	strict := NewClient(HTTPSOnly(), admit(plain, secure, downgrade))
	lenient := NewClient(admit(plain, secure, downgrade))

	if _, err := get(t, strict, plain.URL); !errors.Is(err, ErrScheme) {
		t.Fatalf("http under HTTPSOnly = %v, want ErrScheme", err)
	}
	if _, err := get(t, strict, redirectVia(downgrade, plain.URL)); !errors.Is(err, ErrScheme) {
		t.Fatalf("a redirect to http under HTTPSOnly = %v, want ErrScheme", err)
	}
	if plainHits.Load() != 0 {
		t.Fatal("HTTPSOnly reached the http server")
	}
	if response, err := get(t, strict, secure.URL); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("https under HTTPSOnly = %v, want 200", err)
	}

	if response, err := get(t, lenient, redirectVia(downgrade, plain.URL)); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("http without HTTPSOnly = %v, want 200", err)
	}
	for _, address := range []string{"ftp://example.com/file", "file:///etc/passwd", "gopher://example.com/"} {
		for name, client := range map[string]*http.Client{"strict": strict, "lenient": lenient} {
			if _, err := get(t, client, address); !errors.Is(err, ErrScheme) {
				t.Errorf("%s: get %s = %v, want ErrScheme", name, address, err)
			}
		}
	}
}

func TestClientTimeouts(t *testing.T) {
	t.Parallel()
	client := NewClient()
	transport := client.Transport.(*checkedTransport).base
	if client.Timeout != 30*time.Second || transport.TLSHandshakeTimeout != 10*time.Second || transport.ResponseHeaderTimeout != 30*time.Second {
		t.Fatalf("timeouts: request %v, TLS %v, headers %v", client.Timeout, transport.TLSHandshakeTimeout, transport.ResponseHeaderTimeout)
	}
	if transport.Proxy != nil {
		t.Fatal("the transport uses a proxy")
	}
	if transport.MaxResponseHeaderBytes != 1<<20 {
		t.Fatalf("response headers are capped at %d bytes", transport.MaxResponseHeaderBytes)
	}

	stall, _ := serve(t, false, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	started := time.Now()
	_, err := get(t, NewClient(Timeout(100*time.Millisecond), admit(stall)), stall.URL)
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("get from a stalled server = %v, want a timeout", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("the timeout took %v", elapsed)
	}
}

// TestClientClosesARefusedBody keeps RoundTrip's contract: it closes the
// request body even when it refuses the request.
func TestClientClosesARefusedBody(t *testing.T) {
	t.Parallel()
	body := &closeRecorder{Reader: strings.NewReader("payload")}
	request, err := http.NewRequest(http.MethodPost, "http://169.254.169.254/", body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewClient(noDialing(t)).Transport.RoundTrip(request); !errors.Is(err, ErrRefused) {
		t.Fatalf("RoundTrip = %v, want ErrRefused", err)
	}
	if !body.closed.Load() {
		t.Fatal("the refused request's body was not closed")
	}
}

type closeRecorder struct {
	io.Reader
	closed atomic.Bool
}

func (c *closeRecorder) Close() error {
	c.closed.Store(true)
	return nil
}
