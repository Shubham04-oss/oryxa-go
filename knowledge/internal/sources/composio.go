package sources

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	neturl "net/url"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/composio"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// Proxier is how a request reaches a toolkit's own API, with Composio
// placing the credential. *composio.Client satisfies it; a test fakes it.
type Proxier interface {
	Proxy(ctx context.Context, request composio.ProxyRequest) (composio.ProxyResponse, error)
}

// credentialContextKey carries the credential a request is read with from
// transport.RoundTrip down to composioDoer. It is unexported so nothing
// outside this package can set or read it.
type credentialContextKey struct{}

func withCredential(ctx context.Context, credential domain.Credential) context.Context {
	return context.WithValue(ctx, credentialContextKey{}, credential)
}

func credentialFromContext(ctx context.Context) domain.Credential {
	credential, _ := ctx.Value(credentialContextKey{}).(domain.Credential)
	return credential
}

// binaryAllowedContextKey marks a request whose answer may legitimately be a
// file rather than JSON: Drive's export and alt=media downloads, which
// Composio's proxy never returns inline (see BinaryData). Every other call
// this package makes is a page of JSON, and a toolkit answering one with a
// file is the provider holding something this package cannot read — which
// stays a refusal unless the caller says otherwise.
type binaryAllowedContextKey struct{}

// AllowBinary marks ctx as a call whose answer may be a file. Composio does
// not hand a file's bytes back inline: it stores the file and answers a link
// to it (BinaryData.URL), which composioDoer follows on the caller's behalf
// once this marker says the caller is ready to receive that rather than
// ErrUnsupportedType.
func AllowBinary(ctx context.Context) context.Context {
	return context.WithValue(ctx, binaryAllowedContextKey{}, true)
}

func binaryAllowed(ctx context.Context) bool {
	allowed, _ := ctx.Value(binaryAllowedContextKey{}).(bool)
	return allowed
}

// maxBinaryFetchBytes bounds the file composioDoer will follow Composio's own
// link to fetch, before a caller's own, usually tighter, limit — DoWithLimit's
// maxBytes — trims the response further. It exists so a size Composio itself
// misreports can never make this read an unbounded stream into memory.
const maxBinaryFetchBytes = MaxOriginalBytes + (8 << 20)

// storedFiles is the client fetchBinary reads Composio's stored files with.
// The link arrives in a response, so it is treated as any address from
// outside: HTTPS only, and every connection it makes, redirects included,
// must reach a public address, checked after DNS has answered. Never the
// task's metadata endpoint, a loopback or a private network.
var storedFiles = &http.Client{
	Timeout: 5 * time.Minute,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second,
			Control: func(_, address string, _ syscall.RawConn) error {
				host, _, err := net.SplitHostPort(address)
				if err != nil {
					return err
				}
				ip, err := netip.ParseAddr(host)
				if err != nil {
					return err
				}
				if !publicAddress(ip) {
					return fmt.Errorf("a stored file may only come from a public address, not %s", ip)
				}
				return nil
			},
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   true,
	},
	CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if request.URL.Scheme != "https" {
			return errors.New("a stored file may only be fetched over https")
		}
		if len(via) >= 5 {
			return errors.New("a stored file redirected too many times")
		}
		return nil
	},
}

// sharedAddressSpace is carrier-grade NAT, which IsPrivate does not count as
// private but which is no more the public internet than 10/8 is.
var sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")

// publicAddress reports whether ip is on the public internet.
func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !sharedAddressSpace.Contains(ip)
}

// fetchBinary follows the link Composio answered with — its own storage, not
// the toolkit's — to read a file's bytes. It carries no credential of any
// kind: BinaryData.URL is Composio's, presumed self-authorizing (presigned or
// equivalent), and nothing here has a toolkit credential to place on it
// regardless.
func fetchBinary(ctx context.Context, url string) ([]byte, error) {
	return fetchStoredFile(ctx, storedFiles, url)
}

func fetchStoredFile(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build the request for a stored file: %w", err)
	}
	if request.URL.Scheme != "https" {
		return nil, fmt.Errorf("a stored file may only be fetched over https: %w", domain.ErrInvalid)
	}
	response, err := client.Do(request)
	if err != nil {
		// Why is kept, and the link is not: it is presigned, and a log line
		// holding it would hand the file to whoever read the log.
		var withURL *neturl.Error
		if errors.As(err, &withURL) {
			err = withURL.Err
		}
		return nil, fmt.Errorf("%w: the stored file did not answer: %v", domain.ErrProviderUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: the stored file answered %d", domain.ErrProviderUnavailable, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBinaryFetchBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: the stored file could not be read", domain.ErrProviderUnavailable)
	}
	if len(body) > maxBinaryFetchBytes {
		return nil, fmt.Errorf("the stored file is more than %d bytes: %w", maxBinaryFetchBytes, domain.ErrInvalid)
	}
	return body, nil
}

// composioDoer is the base Doer a Client built by NewClient runs on. It never
// sends a request itself: it turns one into a call through Composio's proxy,
// since Composio holds the credential and this process never does.
type composioDoer struct {
	proxy Proxier
	// binaries fetches a file Composio stored and answered a link to, for a
	// request AllowBinary marked. NewClient wires this to fetchBinary; nil
	// leaves the default behaviour of refusing a binary answer outright, which
	// is what every test that builds a composioDoer literal gets unless it
	// sets this itself.
	binaries func(ctx context.Context, url string) ([]byte, error)
}

func (doer *composioDoer) Do(request *http.Request) (*http.Response, error) {
	accountID := strings.TrimSpace(credentialFromContext(request.Context()).Metadata["composio_account_id"])
	if accountID == "" {
		// Nothing to call through. Answered the same shape a toolkit's own
		// refusal takes, so the rest of this package treats it identically.
		return syntheticResponse(http.StatusForbidden, ""), nil
	}

	body, err := readBody(request)
	if err != nil {
		return nil, err
	}
	response, err := doer.proxy.Proxy(request.Context(), composio.ProxyRequest{
		ConnectedAccountID: accountID,
		// The query is part of the endpoint Composio calls, not a separate
		// parameter list: request.URL already carries it.
		Endpoint:   request.URL.String(),
		Method:     request.Method,
		Body:       body,
		Parameters: headerParameters(request.Header),
	})
	if err != nil {
		return composioFailure(err)
	}
	return providerResponse(request.Context(), response, doer.binaries)
}

// readBody reads a request's body whole, since Composio's proxy takes it as
// one JSON value rather than a stream. Everything this package sends is a
// page of issues, pages or messages, never a file, so nothing here needs
// bounding the way reading a toolkit's answer does.
func readBody(request *http.Request) ([]byte, error) {
	if request.Body == nil || request.Body == http.NoBody {
		return nil, nil
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	return body, nil
}

// headerParameters carries a request's own headers through as Composio's
// proxy takes them, such as Notion-Version or X-GitHub-Api-Version. An
// Authorization header never travels this way — Composio places the
// credential itself, from the connected account named on the request — even
// though nothing in this package ever sets one.
func headerParameters(header http.Header) []composio.ProxyParameter {
	var parameters []composio.ProxyParameter
	for name, values := range header {
		if strings.EqualFold(name, "Authorization") {
			continue
		}
		for _, value := range values {
			parameters = append(parameters, composio.ProxyParameter{Name: name, Value: value, Type: "header"})
		}
	}
	// A deterministic order makes a test's assertion about what was sent
	// stable, and costs nothing real callers notice.
	slices.SortFunc(parameters, func(a, b composio.ProxyParameter) int {
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		return strings.Compare(a.Value, b.Value)
	})
	return parameters
}

// providerResponse turns what the toolkit answered into the *http.Response
// the rest of this package reads exactly as if it had called the toolkit
// directly. A file is refused, the same way a decoder failing on one would
// read, unless the request was marked with AllowBinary and this doer has a
// way to fetch one — Drive's export and alt=media downloads are the only
// calls this package makes that ever set the marker.
func providerResponse(ctx context.Context, response composio.ProxyResponse, fetch func(context.Context, string) ([]byte, error)) (*http.Response, error) {
	if response.Binary != nil {
		if !binaryAllowed(ctx) || fetch == nil {
			return nil, fmt.Errorf("%w: the provider answered with a file, not text", domain.ErrUnsupportedType)
		}
		body, err := fetch(ctx, response.Binary.URL)
		if err != nil {
			return nil, err
		}
		header := http.Header{}
		if response.Binary.ContentType != "" {
			header.Set("Content-Type", response.Binary.ContentType)
		}
		return &http.Response{
			StatusCode: cmp.Or(response.Status, http.StatusOK),
			Header:     header,
			Body:       io.NopCloser(bytes.NewReader(body)),
		}, nil
	}
	header := http.Header{}
	for name, value := range response.Headers {
		header.Set(name, value)
	}
	body := response.Data
	if body == nil {
		body = []byte{}
	}
	return &http.Response{
		StatusCode: response.Status,
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}, nil
}

// composioFailure turns Composio's own refusal — as opposed to the
// toolkit's, which arrives as an ordinary ProxyResponse that providerResponse
// already carries through — into the synthetic answer that lets the retry
// and StatusError logic in http.go handle it exactly as it would a real one.
// Composio unable to find, use or reach the connected account itself reads
// as domain.ErrNeedsReconnect the same way a toolkit's own 401 would: the
// connection needs attention, not another attempt. A rate limit or an
// outage is Composio's own rather than the toolkit's, but is worth exactly
// the same waiting and retrying.
func composioFailure(err error) (*http.Response, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	switch {
	case errors.Is(err, composio.ErrRateLimited):
		retryAfter := ""
		var apiErr *composio.Error
		if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
			retryAfter = strconv.Itoa(int(apiErr.RetryAfter / time.Second))
		}
		return syntheticResponse(http.StatusTooManyRequests, retryAfter), nil
	case errors.Is(err, composio.ErrNotFound), errors.Is(err, composio.ErrForbidden), errors.Is(err, composio.ErrUnauthorized):
		return syntheticResponse(http.StatusUnauthorized, ""), nil
	default:
		// composio.ErrUnavailable, and anything else Composio itself failed
		// on: worth retrying, the same as a toolkit that wobbled.
		return syntheticResponse(http.StatusServiceUnavailable, ""), nil
	}
}

// syntheticResponse is an answer this package invented rather than one a
// toolkit sent, for a failure Composio reported about itself. retryAfter, when
// not empty, is seconds, the same form a real Retry-After header takes.
func syntheticResponse(status int, retryAfter string) *http.Response {
	header := http.Header{}
	if retryAfter != "" {
		header.Set("Retry-After", retryAfter)
	}
	return &http.Response{StatusCode: status, Header: header, Body: http.NoBody}
}
