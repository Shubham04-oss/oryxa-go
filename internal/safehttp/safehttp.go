// Package safehttp is the HTTP client for URLs Oryxa does not fully choose,
// such as an API at a host a member or an admin typed, or a link in a
// provider's answer. It connects to the public internet only. It never
// reaches the task's metadata or credential endpoints, a loopback address,
// or a private or VPC-internal network.
//
// The check runs when a connection is made, on the addresses actually dialed
// (see Dialer). A hostname that resolves to a refused address is refused
// however the request got there, and DNS rebinding gets no second lookup.
// Each redirect is checked before it is followed, then dialed the same way.
package safehttp

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"time"
)

var (
	// ErrRefused means a request would have reached an address that is not
	// on the public internet. Nothing was sent to it.
	ErrRefused = errors.New("refusing an address that is not on the public internet")
	// ErrScheme means a request or a redirect used a scheme the client does
	// not allow: anything but https for an HTTPSOnly client, and anything
	// but http or https otherwise.
	ErrScheme = errors.New("refusing a URL scheme")
	// ErrTooManyRedirects means a request was redirected more times than the
	// client follows.
	ErrTooManyRedirects = errors.New("too many redirects")
)

const (
	// defaultTimeout bounds a whole request, every redirect and reading the
	// body included, unless Timeout changes it.
	defaultTimeout = 30 * time.Second
	// defaultMaxRedirects is how many redirects a client follows unless
	// MaxRedirects changes it.
	defaultMaxRedirects   = 5
	tlsHandshakeTimeout   = 10 * time.Second
	responseHeaderTimeout = 30 * time.Second
	// maxResponseHeaderBytes bounds a response's headers, which come from a
	// server nobody here chose.
	maxResponseHeaderBytes = 1 << 20
)

// Option adjusts a client NewClient builds.
type Option func(*settings)

// HTTPSOnly refuses every request and redirect that is not https. Use it for
// any URL built from what a member or an admin typed.
func HTTPSOnly() Option {
	return func(s *settings) { s.httpsOnly = true }
}

// Timeout bounds a whole request, redirects and reading the body included,
// in place of the default 30 seconds. With zero, only the request's context
// bounds it.
func Timeout(timeout time.Duration) Option {
	return func(s *settings) { s.timeout = max(timeout, 0) }
}

// MaxRedirects is how many redirects a client follows, in place of the
// default 5. A redirect after that fails with ErrTooManyRedirects. With zero
// the client follows none, and the redirect itself is the response.
func MaxRedirects(n int) Option {
	return func(s *settings) { s.maxRedirects = max(n, 0) }
}

// ResolveWith looks hostnames up with resolver, in place of the system's.
func ResolveWith(resolver Resolver) Option {
	return func(s *settings) { s.dialer.Resolver = resolver }
}

type settings struct {
	httpsOnly    bool
	timeout      time.Duration
	maxRedirects int
	dialer       Dialer
	tlsConfig    *tls.Config
}

// NewClient returns a client that connects to the public internet only,
// never through a proxy. By default it follows at most 5 redirects, checking
// each before following it, and gives up on a request after 30 seconds. The
// options change that.
//
// To add a layer such as observability.Transport, wrap the client's
// Transport. Anything that places a credential goes inside that layer, so
// traces never see it. Such a layer sees every redirect, whatever host it
// goes to, so it must place a credential only for the host the credential
// belongs to, or the client must use MaxRedirects(0).
func NewClient(options ...Option) *http.Client {
	s := &settings{
		timeout:      defaultTimeout,
		maxRedirects: defaultMaxRedirects,
		tlsConfig:    &tls.Config{MinVersion: tls.VersionTLS12},
	}
	for _, option := range options {
		option(s)
	}
	return &http.Client{
		Transport:     s.transport(),
		CheckRedirect: s.checkRedirect,
		Timeout:       s.timeout,
	}
}

func (s *settings) transport() http.RoundTripper {
	return &checkedTransport{settings: s, base: &http.Transport{
		// No proxy, whatever the environment says: through one, the dialer
		// would check the proxy's address and not the destination's.
		Proxy:                  nil,
		DialContext:            s.dialer.DialContext,
		TLSClientConfig:        s.tlsConfig,
		TLSHandshakeTimeout:    tlsHandshakeTimeout,
		ResponseHeaderTimeout:  responseHeaderTimeout,
		ExpectContinueTimeout:  time.Second,
		IdleConnTimeout:        90 * time.Second,
		MaxIdleConns:           100,
		MaxIdleConnsPerHost:    10,
		MaxResponseHeaderBytes: maxResponseHeaderBytes,
		ForceAttemptHTTP2:      true,
	}}
}

// checkRedirect is the client's redirect policy: at most maxRedirects, each
// to a URL checkURL allows. The redirect is then dialed through the Dialer
// like any request.
func (s *settings) checkRedirect(request *http.Request, via []*http.Request) error {
	if s.maxRedirects == 0 {
		return http.ErrUseLastResponse
	}
	if len(via) > s.maxRedirects {
		return fmt.Errorf("%w: stopped after %d", ErrTooManyRedirects, s.maxRedirects)
	}
	return s.checkURL(request.URL)
}

// checkURL refuses what a URL alone shows is not allowed: a scheme the
// client refuses, no host, or a host that is a refused IP address. A
// hostname's addresses are the Dialer's to check, when it is dialed.
func (s *settings) checkURL(target *url.URL) error {
	if target.Scheme != "https" && (s.httpsOnly || target.Scheme != "http") {
		return fmt.Errorf("%w: %q", ErrScheme, target.Scheme)
	}
	host := target.Hostname()
	if host == "" {
		return fmt.Errorf("%w: the URL names no host", ErrRefused)
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return nil
	}
	port := uint64(443)
	if target.Scheme == "http" {
		port = 80
	}
	if target.Port() != "" {
		if port, err = strconv.ParseUint(target.Port(), 10, 16); err != nil {
			return fmt.Errorf("safehttp: %q is not a port", target.Port())
		}
	}
	if !s.dialer.allowed(netip.AddrPortFrom(addr, uint16(port))) {
		return fmt.Errorf("%w: %s", ErrRefused, host)
	}
	return nil
}

// checkedTransport refuses a request that checkURL refuses before anything is
// dialed, redirects included, since each goes through RoundTrip too.
type checkedTransport struct {
	settings *settings
	base     *http.Transport
}

func (t *checkedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := t.settings.checkURL(request.URL); err != nil {
		// A RoundTripper closes the body, even when it fails.
		if request.Body != nil {
			_ = request.Body.Close()
		}
		return nil, err
	}
	return t.base.RoundTrip(request)
}

// CloseIdleConnections lets http.Client.CloseIdleConnections reach the
// transport underneath.
func (t *checkedTransport) CloseIdleConnections() { t.base.CloseIdleConnections() }
