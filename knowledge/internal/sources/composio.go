package sources

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
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

// composioDoer is the base Doer a Client built by NewClient runs on. It never
// sends a request itself: it turns one into a call through Composio's proxy,
// since Composio holds the credential and this process never does.
type composioDoer struct {
	proxy Proxier
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
	return providerResponse(response)
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
// directly. A file is the one shape it cannot carry forward: nothing here
// extracts text from one, so it is refused rather than handed to a decoder
// that would only fail on it less clearly.
func providerResponse(response composio.ProxyResponse) (*http.Response, error) {
	if response.Binary != nil {
		return nil, fmt.Errorf("%w: the provider answered with a file, not text", domain.ErrUnsupportedType)
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
