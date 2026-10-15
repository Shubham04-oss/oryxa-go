package egress

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/manifest"
	"github.com/Shubham04-oss/oryxa-go/internal/safehttp"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// maxBody caps a request body and a response body, in bytes.
const maxBody = 10 << 20

var (
	// ErrHost means the URL is not https, names credentials, or is on a host
	// the connection's manifest does not allow. Nothing was sent.
	ErrHost = errors.New("egress: host not allowed")
	// ErrTooLarge means a request or response body passed the cap.
	ErrTooLarge = errors.New("egress: body too large")
)

// Connections answers what the proxy must know about a connection.
type Connections interface {
	// Manifest returns the manifest of the app the connection is with.
	Manifest(ctx context.Context, connection string) (*manifest.Manifest, error)
	// Token returns the connection's current token of kind.
	Token(ctx context.Context, connection, kind string) (secrets.Secret, error)
	// Refresh renews the connection's token of kind after stale was refused,
	// and returns the new one. If another caller already renewed it, it
	// returns that token rather than renewing again.
	Refresh(ctx context.Context, connection, kind string, stale secrets.Secret) (secrets.Secret, error)
}

// Request is a connector's call to an app. Its credentials, if any, are
// dropped.
type Request struct {
	Method string
	URL    string
	Header http.Header
	Body   []byte
}

// Response is the app's answer with credentials removed.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Proxy sends connectors' requests to apps.
type Proxy struct {
	Keys        Keys
	Connections Connections
	// Client sends the request. Nil means a safehttp client that is https
	// only and follows no redirect: a 3xx comes back to the connector, whose
	// next request is checked against the allowlist like any other.
	Client *http.Client
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// requestHeadersDropped are never forwarded from a connector: credentials it
// should not hold or place, and what the proxy sets itself.
var requestHeadersDropped = []string{
	"Authorization", "Proxy-Authorization", "Cookie", "Host", "Content-Length", "Connection", "Upgrade",
	"Transfer-Encoding", "Te", "Trailer", "Keep-Alive", "Proxy-Connection",
}

// responseHeadersDropped are credentials an app may set that no connector
// should keep.
var responseHeadersDropped = []string{"Set-Cookie", "Set-Cookie2", "Authorization", "Proxy-Authorization"}

func (p *Proxy) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Proxy) client() *http.Client {
	if p.Client == nil {
		p.Client = safehttp.NewClient(safehttp.HTTPSOnly(), safehttp.MaxRedirects(0))
	}
	return p.Client
}

// Do checks handle and the host, sends request with the connection's token,
// and, if the app answers 401, refreshes the token and sends once more.
func (p *Proxy) Do(ctx context.Context, handle string, request Request) (*Response, error) {
	claims, err := p.Keys.Verify(handle, p.now())
	if err != nil {
		return nil, err
	}
	if len(request.Body) > maxBody {
		return nil, ErrTooLarge
	}
	target, err := url.Parse(request.URL)
	if err != nil || target.Scheme != "https" || target.User != nil || target.Hostname() == "" {
		return nil, fmt.Errorf("%w: %q", ErrHost, request.URL)
	}
	if port := target.Port(); port != "" && port != "443" {
		return nil, fmt.Errorf("%w: port %s", ErrHost, port)
	}
	m, err := p.Connections.Manifest(ctx, claims.Connection)
	if err != nil {
		return nil, fmt.Errorf("egress: manifest: %w", err)
	}
	if !m.AllowsHost(strings.ToLower(target.Hostname())) {
		return nil, fmt.Errorf("%w: %s", ErrHost, target.Hostname())
	}

	token, err := p.Connections.Token(ctx, claims.Connection, claims.TokenKind)
	if err != nil {
		return nil, fmt.Errorf("egress: token: %w", err)
	}
	response, err := p.send(ctx, request, target, token)
	if err != nil {
		return nil, err
	}
	if response.Status == http.StatusUnauthorized {
		fresh, err := p.Connections.Refresh(ctx, claims.Connection, claims.TokenKind, token)
		if err != nil {
			return nil, fmt.Errorf("egress: refresh: %w", err)
		}
		if response, err = p.send(ctx, request, target, fresh); err != nil {
			return nil, err
		}
		token = fresh
	}
	// An app can echo the token it was given; a connector must not read it.
	response.Body = bytes.ReplaceAll(response.Body, []byte(token.Reveal()), []byte("[redacted]"))
	return response, nil
}

func (p *Proxy) send(ctx context.Context, request Request, target *url.URL, token secrets.Secret) (*Response, error) {
	method := request.Method
	if method == "" {
		method = http.MethodGet
	}
	out, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(request.Body))
	if err != nil {
		return nil, fmt.Errorf("egress: %w", err)
	}
	out.Header = request.Header.Clone()
	if out.Header == nil {
		out.Header = http.Header{}
	}
	for _, name := range requestHeadersDropped {
		out.Header.Del(name)
	}
	// ponytail: every app takes a bearer token; one that wants another scheme
	// needs a manifest field for it.
	out.Header.Set("Authorization", "Bearer "+token.Reveal())

	resp, err := p.client().Do(out)
	if err != nil {
		// The client's error holds the URL, never the token.
		return nil, fmt.Errorf("egress: send: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("egress: read: %w", err)
	}
	if len(body) > maxBody {
		return nil, ErrTooLarge
	}
	header := resp.Header.Clone()
	for _, name := range responseHeadersDropped {
		header.Del(name)
	}
	return &Response{Status: resp.StatusCode, Header: header, Body: body}, nil
}
