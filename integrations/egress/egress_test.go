package egress

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/manifest"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

var t0 = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func testKeys() Keys {
	return Keys{Current: "k2", Keys: map[string]secrets.Secret{
		"k1": secrets.Secret(strings.Repeat("a", 32)),
		"k2": secrets.Secret(strings.Repeat("b", 32)),
	}}
}

func claims() Claims {
	return Claims{Connection: "conn1", TokenKind: "bot", Member: "m1", Agent: "ag1", Expires: t0.Add(time.Minute)}
}

func TestHandleRoundTrip(t *testing.T) {
	keys := testKeys()
	h, err := keys.Mint(claims(), t0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := keys.Verify(h, t0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Connection != "conn1" || got.TokenKind != "bot" || got.Member != "m1" || got.Agent != "ag1" {
		t.Fatalf("claims = %+v", got)
	}
	// A handle minted under the old key still opens while that key is held.
	old := Keys{Current: "k1", Keys: keys.Keys}
	h1, _ := old.Mint(claims(), t0)
	if _, err := keys.Verify(h1, t0); err != nil {
		t.Fatalf("rotated key: %v", err)
	}
	delete(keys.Keys, "k1")
	if _, err := keys.Verify(h1, t0); !errors.Is(err, ErrHandle) {
		t.Fatalf("dropped key: %v", err)
	}
}

func TestForgedHandle(t *testing.T) {
	keys := testKeys()
	h, _ := keys.Mint(claims(), t0)
	parts := strings.Split(h, ".")

	other := claims()
	other.Connection = "conn2"
	forged, _ := keys.Mint(other, t0)
	fp := strings.Split(forged, ".")

	for name, bad := range map[string]string{
		"claims swapped": strings.Join([]string{parts[0], parts[1], fp[2], parts[3]}, "."),
		"mac flipped":    strings.Join(parts[:3], ".") + "." + base64.RawURLEncoding.EncodeToString([]byte("nope")),
		"unknown kid":    strings.Join([]string{parts[0], "k9", parts[2], parts[3]}, "."),
		"wrong prefix":   "eh2." + strings.Join(parts[1:], "."),
		"too few parts":  "eh1.k2.abc",
		"empty":          "",
		"signed by a stranger": func() string {
			s := Keys{Current: "k2", Keys: map[string]secrets.Secret{"k2": secrets.Secret(strings.Repeat("z", 32))}}
			x, _ := s.Mint(claims(), t0)
			return x
		}(),
	} {
		if _, err := keys.Verify(bad, t0); !errors.Is(err, ErrHandle) {
			t.Errorf("%s: err = %v, want ErrHandle", name, err)
		}
	}
}

func TestExpiredAndOverlongHandle(t *testing.T) {
	keys := testKeys()
	h, _ := keys.Mint(claims(), t0)
	if _, err := keys.Verify(h, t0.Add(2*time.Minute)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
	long := claims()
	long.Expires = t0.Add(MaxTTL + time.Minute)
	if _, err := keys.Mint(long, t0); err == nil {
		t.Fatal("minted a handle over the TTL cap")
	}
	if _, err := keys.Mint(Claims{TokenKind: "bot", Expires: t0.Add(time.Minute)}, t0); err == nil {
		t.Fatal("minted a handle with no connection")
	}
	// A handle signed with the right key but a long expiry is still refused.
	forgedLong := claims()
	forgedLong.Expires = t0.Add(time.Hour)
	forgedLong.Exp = forgedLong.Expires.Unix()
	if _, err := keys.Verify(mintUnchecked(t, keys, forgedLong), t0); !errors.Is(err, ErrHandle) {
		t.Fatalf("long handle: %v", err)
	}
}

// mintUnchecked signs claims without Mint's TTL check, as a bug in a minter
// would.
func mintUnchecked(t *testing.T, k Keys, c Claims) string {
	t.Helper()
	short := c
	short.Expires = t0.Add(time.Minute)
	h, err := k.Mint(short, t0)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(h, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(parts[2])
	raw = []byte(strings.Replace(string(raw), `"e":`+itoa(short.Expires.Unix()), `"e":`+itoa(c.Exp), 1))
	key, _ := k.key(k.Current)
	signed := strings.Join([]string{parts[0], parts[1], base64.RawURLEncoding.EncodeToString(raw)}, ".")
	return signed + "." + base64.RawURLEncoding.EncodeToString(mac(key, signed))
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestKeysValidate(t *testing.T) {
	if err := testKeys().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, k := range map[string]Keys{
		"short key":  {Current: "a", Keys: map[string]secrets.Secret{"a": "short"}},
		"dotted kid": {Current: "a.b", Keys: map[string]secrets.Secret{"a.b": secrets.Secret(strings.Repeat("a", 32))}},
		"no current": {Current: "x", Keys: map[string]secrets.Secret{"a": secrets.Secret(strings.Repeat("a", 32))}},
	} {
		if k.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

type fakeConns struct {
	m          *manifest.Manifest
	tokens     []secrets.Secret // handed out in order, then the last repeats
	tokenCalls atomic.Int32
	refreshes  atomic.Int32
	refreshErr error
}

func (f *fakeConns) Manifest(context.Context, string) (*manifest.Manifest, error) { return f.m, nil }
func (f *fakeConns) Token(context.Context, string, string) (secrets.Secret, error) {
	f.tokenCalls.Add(1)
	return f.tokens[0], nil
}
func (f *fakeConns) Refresh(_ context.Context, _, _ string, _ secrets.Secret) (secrets.Secret, error) {
	f.refreshes.Add(1)
	if f.refreshErr != nil {
		return "", f.refreshErr
	}
	return f.tokens[1], nil
}

// proxyTo returns a proxy whose client dials server whatever the host, with
// server's certificate trusted, so tests name api.example.test.
func proxyTo(server *httptest.Server, conns *fakeConns) *Proxy {
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test server's own certificate
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Proxy{Keys: testKeys(), Connections: conns, Client: client, Now: func() time.Time { return t0 }}
}

func testManifest() *manifest.Manifest {
	return &manifest.Manifest{EgressAllowlist: []string{"api.example.test", "*.cdn.example.test"}}
}

func handle(t *testing.T) string {
	t.Helper()
	h, err := testKeys().Mint(claims(), t0)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestProxyInjectsTokenAndStripsCredentials(t *testing.T) {
	var gotAuth, gotCookie string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotCookie = r.Header.Get("Authorization"), r.Header.Get("Cookie")
		w.Header().Add("Set-Cookie", "session=abc")
		w.Header().Set("X-Keep", "yes")
		_, _ = w.Write([]byte(`{"echo":"` + strings.TrimPrefix(gotAuth, "Bearer ") + `"}`))
	}))
	defer server.Close()
	conns := &fakeConns{m: testManifest(), tokens: []secrets.Secret{"tok-1", "tok-2"}}

	resp, err := proxyTo(server, conns).Do(context.Background(), handle(t), Request{
		Method: "GET", URL: "https://api.example.test/v1/things",
		Header: http.Header{"Authorization": {"Bearer connector-chosen"}, "Cookie": {"c=1"}, "X-Custom": {"1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok-1" || gotCookie != "" {
		t.Fatalf("app saw Authorization %q Cookie %q", gotAuth, gotCookie)
	}
	if resp.Header.Get("Set-Cookie") != "" || resp.Header.Get("X-Keep") != "yes" {
		t.Fatalf("response headers = %v", resp.Header)
	}
	if strings.Contains(string(resp.Body), "tok-1") {
		t.Fatalf("token echoed to the connector: %s", resp.Body)
	}
}

func TestProxyRefusesBadHandleAndHosts(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer server.Close()
	conns := &fakeConns{m: testManifest(), tokens: []secrets.Secret{"tok-1", "tok-2"}}
	p := proxyTo(server, conns)

	if _, err := p.Do(context.Background(), "eh1.k2.x.y", Request{URL: "https://api.example.test/"}); !errors.Is(err, ErrHandle) {
		t.Errorf("forged: %v", err)
	}
	p.Now = func() time.Time { return t0.Add(time.Hour) }
	if _, err := p.Do(context.Background(), handle(t), Request{URL: "https://api.example.test/"}); !errors.Is(err, ErrExpired) {
		t.Errorf("expired: %v", err)
	}
	p.Now = func() time.Time { return t0 }
	for _, raw := range []string{
		"https://evil.example.test/",          // off the allowlist
		"https://cdn.example.test/",           // bare domain of a wildcard
		"https://api.example.test.evil.test/", // suffix trick
		"http://api.example.test/",            // not https
		"https://user:pw@api.example.test/",   // credentials in the URL
		"https://api.example.test:8443/",      // other port
		"https://127.0.0.1/",                  // an address
	} {
		if _, err := p.Do(context.Background(), handle(t), Request{URL: raw}); !errors.Is(err, ErrHost) {
			t.Errorf("%s: err = %v, want ErrHost", raw, err)
		}
	}
	if _, err := p.Do(context.Background(), handle(t), Request{URL: "https://a.cdn.example.test/x"}); err != nil {
		t.Errorf("wildcard host: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("server hit %d times, want only the wildcard request", hits.Load())
	}
	if conns.tokenCalls.Load() != 1 {
		t.Errorf("token fetched %d times for refused requests", conns.tokenCalls.Load())
	}
}

func TestProxyRefreshesOnce(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer tok-2" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	conns := &fakeConns{m: testManifest(), tokens: []secrets.Secret{"tok-1", "tok-2"}}

	resp, err := proxyTo(server, conns).Do(context.Background(), handle(t), Request{Method: "POST", URL: "https://api.example.test/x", Body: []byte("body")})
	if err != nil || resp.Status != 200 || string(resp.Body) != "ok" {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}
	if conns.refreshes.Load() != 1 || calls.Load() != 2 {
		t.Fatalf("refreshes = %d, app calls = %d; want 1 and 2", conns.refreshes.Load(), calls.Load())
	}
}

func TestProxyRefreshOnceWhenStill401(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	conns := &fakeConns{m: testManifest(), tokens: []secrets.Secret{"tok-1", "tok-2"}}

	resp, err := proxyTo(server, conns).Do(context.Background(), handle(t), Request{URL: "https://api.example.test/x"})
	if err != nil || resp.Status != 401 {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}
	if conns.refreshes.Load() != 1 || calls.Load() != 2 {
		t.Fatalf("refreshes = %d, app calls = %d; want 1 and 2", conns.refreshes.Load(), calls.Load())
	}
}

func TestProxyRefreshFailure(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) }))
	defer server.Close()
	boom := errors.New("invalid_grant")
	conns := &fakeConns{m: testManifest(), tokens: []secrets.Secret{"tok-1", "tok-2"}, refreshErr: boom}
	if _, err := proxyTo(server, conns).Do(context.Background(), handle(t), Request{URL: "https://api.example.test/x"}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the refresh error", err)
	}
}

func TestProxyBodyCaps(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxBody+1)))
	}))
	defer server.Close()
	conns := &fakeConns{m: testManifest(), tokens: []secrets.Secret{"tok-1", "tok-2"}}
	p := proxyTo(server, conns)
	if _, err := p.Do(context.Background(), handle(t), Request{URL: "https://api.example.test/x"}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("response: %v", err)
	}
	if _, err := p.Do(context.Background(), handle(t), Request{Method: "POST", URL: "https://api.example.test/x", Body: make([]byte, maxBody+1)}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("request: %v", err)
	}
}

func TestProxyDoesNotFollowRedirects(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example.test/steal", http.StatusFound)
	}))
	defer server.Close()
	conns := &fakeConns{m: testManifest(), tokens: []secrets.Secret{"tok-1", "tok-2"}}
	resp, err := proxyTo(server, conns).Do(context.Background(), handle(t), Request{URL: "https://api.example.test/x"})
	if err != nil || resp.Status != http.StatusFound {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}
}
