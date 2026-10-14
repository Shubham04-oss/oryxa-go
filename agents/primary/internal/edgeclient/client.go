// Package edgeclient calls the oryxa-edge Worker on Cloudflare: the room,
// sandbox and browser APIs of docs/design/live-layer/01-contracts.md. Every
// attempt carries its own edge token (the edge refuses a jti it has seen),
// signed by agents' service key for the room, member, agent, run and scopes
// the call names.
package edgeclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

// TokenIssuer signs edge tokens. serviceauth.Signer satisfies it.
type TokenIssuer interface {
	EdgeToken(context.Context, edgev1.TokenClaims) (string, error)
}

const (
	// DefaultTimeout bounds a call that names no timeout of its own.
	DefaultTimeout = 60 * time.Second
	// DefaultAttempts is how often a call is tried when it may be retried.
	DefaultAttempts = 3
	// maxResponseBytes bounds a response body: a screenshot's base64 is the
	// largest thing the edge returns.
	maxResponseBytes = 32 << 20
	// maxErrorBytes bounds how much of an error body is read.
	maxErrorBytes = 16 << 10
)

// Config configures a Client.
type Config struct {
	// BaseURL is the edge, such as https://edge.oryxa.in.
	BaseURL string
	Tokens  TokenIssuer
	// HTTP defaults to a client with no overall timeout; each call sets its
	// own deadline.
	HTTP *http.Client
	// Attempts defaults to DefaultAttempts; Backoff to 200ms, doubling.
	Attempts int
	Backoff  time.Duration
}

// Client calls the edge.
type Client struct {
	base     *url.URL
	tokens   TokenIssuer
	http     *http.Client
	attempts int
	backoff  time.Duration
}

// New checks config and returns a Client.
func New(config Config) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(config.BaseURL, "/"))
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") {
		return nil, errors.New("edgeclient: base URL must be an absolute http(s) URL")
	}
	if config.Tokens == nil {
		return nil, errors.New("edgeclient: a token issuer is required")
	}
	client := config.HTTP
	if client == nil {
		client = &http.Client{Transport: http.DefaultTransport}
	}
	attempts := config.Attempts
	if attempts <= 0 {
		attempts = DefaultAttempts
	}
	backoff := config.Backoff
	if backoff <= 0 {
		backoff = 200 * time.Millisecond
	}
	return &Client{base: base, tokens: config.Tokens, http: client, attempts: attempts, backoff: backoff}, nil
}

// Call is one request to the edge.
type Call struct {
	// Claims names the room, member, agent, run and scopes; the issuer fills
	// in the rest for each attempt.
	Claims edgev1.TokenClaims
	Method string
	// Path is the API path, from edgev1's path helpers plus the operation's
	// suffix.
	Path  string
	Query url.Values
	// Body is encoded as JSON when not nil.
	Body any
	// Timeout bounds every attempt together; DefaultTimeout when zero.
	Timeout time.Duration
	// Idempotent lets any transport failure or 5xx be retried. A call that
	// isn't is retried only when the edge says the operation never ran
	// (retryable), or when the connection was never made.
	Idempotent bool
}

// Error is the edge's answer to a call that failed.
type Error struct {
	Status    int
	Code      edgev1.ErrorCode
	Message   string
	Retryable bool
}

func (err *Error) Error() string {
	return fmt.Sprintf("edge %d %s: %s", err.Status, err.Code, err.Message)
}

// IsCode reports whether err is an edge Error with code.
func IsCode(err error, code edgev1.ErrorCode) bool {
	var edgeErr *Error
	return errors.As(err, &edgeErr) && edgeErr.Code == code
}

// Do sends call and decodes a 2xx response's JSON into out, when out is not
// nil.
func (client *Client) Do(ctx context.Context, call Call, out any) error {
	if !strings.HasPrefix(call.Path, "/v1/") {
		return fmt.Errorf("edgeclient: path %q is not an edge API path", call.Path)
	}
	timeout := call.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var body []byte
	if call.Body != nil {
		encoded, err := json.Marshal(call.Body)
		if err != nil {
			return fmt.Errorf("edgeclient: encode body: %w", err)
		}
		body = encoded
	}
	target := *client.base
	target.Path = client.base.Path + call.Path
	target.RawQuery = call.Query.Encode()

	var lastErr error
	delay := client.backoff
	for attempt := 1; attempt <= client.attempts; attempt++ {
		retry, err := client.attempt(ctx, call, target.String(), body, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retry || attempt == client.attempts || ctx.Err() != nil {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(lastErr, ctx.Err())
		case <-timer.C:
		}
		delay *= 2
	}
	return lastErr
}

// attempt makes one request, reporting whether its failure may be retried.
func (client *Client) attempt(ctx context.Context, call Call, target string, body []byte, out any) (bool, error) {
	token, err := client.tokens.EdgeToken(ctx, call.Claims)
	if err != nil {
		// KMS is briefly unavailable more often than it refuses: try again.
		return true, fmt.Errorf("edgeclient: sign edge token: %w", err)
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, call.Method, target, reader)
	if err != nil {
		return false, fmt.Errorf("edgeclient: build request: %w", err)
	}
	request.Header.Set("Authorization", edgev1.AuthorizationScheme+token)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := client.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return false, fmt.Errorf("edgeclient: %s %s: %w", call.Method, call.Path, ctx.Err())
		}
		return call.Idempotent || neverConnected(err), fmt.Errorf("edgeclient: %s %s: %w", call.Method, call.Path, err)
	}
	defer response.Body.Close()

	if response.StatusCode >= 200 && response.StatusCode < 300 {
		if out == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
			return false, nil
		}
		limited := io.LimitReader(response.Body, maxResponseBytes+1)
		raw, err := io.ReadAll(limited)
		if err != nil {
			return call.Idempotent, fmt.Errorf("edgeclient: read response: %w", err)
		}
		if len(raw) > maxResponseBytes {
			return false, errors.New("edgeclient: response too large")
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return false, fmt.Errorf("edgeclient: decode response: %w", err)
		}
		return false, nil
	}

	edgeErr := &Error{Status: response.StatusCode, Code: edgev1.CodeInternal, Message: response.Status}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBytes))
	var decoded edgev1.ErrorBody
	if json.Unmarshal(raw, &decoded) == nil && decoded.Error.Code != "" {
		edgeErr.Code, edgeErr.Message, edgeErr.Retryable = decoded.Error.Code, decoded.Error.Message, decoded.Error.Retryable
	} else if response.StatusCode == http.StatusServiceUnavailable || response.StatusCode == http.StatusBadGateway {
		edgeErr.Code = edgev1.CodeUnavailable
	}
	retry := edgeErr.Retryable || (call.Idempotent && response.StatusCode >= 500)
	return retry, edgeErr
}

// neverConnected reports whether err happened before a connection existed,
// so the edge cannot have run anything.
func neverConnected(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}
