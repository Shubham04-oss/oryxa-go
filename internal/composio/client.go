// Package composio is Oryxa's client for Composio's REST API (v3.1), which
// connects a member's third-party apps, runs tools in them and delivers their
// triggers.
//
// Composio has no Go SDK, so this speaks its REST API directly. It holds the
// project API key, which reaches every user in the project, so only a
// backend that has already decided whose connected account a call may use
// should hold one of these: never an agent, and never a browser.
package composio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is Composio's current REST API.
const DefaultBaseURL = "https://backend.composio.dev/api/v3.1"

const (
	// requestTimeout bounds one call. A tool can take a while — sending mail
	// with attachments, say — so it is generous; a caller wanting less passes
	// a context with its own deadline.
	requestTimeout = 90 * time.Second
	// maxResponseBytes caps what one call may hand back.
	maxResponseBytes = 32 << 20
)

var (
	// ErrUnauthorized means Composio refused the API key.
	ErrUnauthorized = errors.New("composio refused the API key")
	// ErrForbidden means the key may not do this, such as a proxy call
	// without the proxy permission.
	ErrForbidden = errors.New("composio forbade the request")
	// ErrNotFound means what the call named does not exist in the project.
	ErrNotFound = errors.New("not found at composio")
	// ErrInvalid means Composio refused the request as malformed.
	ErrInvalid = errors.New("composio refused the request")
	// ErrRateLimited means the project is over its rate limit.
	ErrRateLimited = errors.New("composio rate limited the request")
	// ErrUnavailable means Composio did not answer usefully.
	ErrUnavailable = errors.New("composio is unavailable")
)

// Error is what Composio answered when a call failed. Its message may repeat
// what the caller sent, so a caller decides whether to show it.
type Error struct {
	Status    int
	Code      string
	Message   string
	RequestID string
	// RetryAfter is how long Composio asked to wait, when it said.
	RetryAfter time.Duration
}

func (err *Error) Error() string {
	message := err.Message
	if message == "" {
		message = http.StatusText(err.Status)
	}
	return fmt.Sprintf("composio answered %d: %s", err.Status, message)
}

// Unwrap names the kind of failure, so a caller can errors.Is it.
func (err *Error) Unwrap() error {
	switch {
	case err.Status == http.StatusUnauthorized:
		return ErrUnauthorized
	case err.Status == http.StatusForbidden:
		return ErrForbidden
	case err.Status == http.StatusNotFound, err.Status == http.StatusGone:
		return ErrNotFound
	case err.Status == http.StatusTooManyRequests:
		return ErrRateLimited
	case err.Status >= 500, err.Status == http.StatusRequestTimeout:
		return ErrUnavailable
	default:
		return ErrInvalid
	}
}

// Config configures a Client.
type Config struct {
	// APIKey is the project API key. Required.
	APIKey string
	// BaseURL defaults to DefaultBaseURL.
	BaseURL string
	// HTTP defaults to a client with a generous timeout.
	HTTP *http.Client
}

// Client calls Composio's REST API. It is safe for concurrent use.
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// New builds a client, refusing one with no API key.
func New(config Config) (*Client, error) {
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, errors.New("composio: an API key is required")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if parsed, err := url.Parse(baseURL); err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("composio: base URL %q must be absolute", baseURL)
	}
	httpClient := config.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	return &Client{apiKey: strings.TrimSpace(config.APIKey), baseURL: baseURL, http: httpClient}, nil
}

// Page is one page of a listing. An empty NextCursor means it was the last.
type Page[T any] struct {
	Items      []T
	NextCursor string
}

// call sends one request and decodes a 2xx answer into answer, which may be
// nil when nothing in the answer is needed.
func (client *Client) call(ctx context.Context, method, path string, query url.Values, body, answer any) error {
	endpoint := client.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("composio: encode %s %s: %w", method, path, err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("composio: build %s %s: %w", method, path, err)
	}
	request.Header.Set("x-api-key", client.apiKey)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := client.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %s %s did not answer", ErrUnavailable, method, path)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("%w: %s %s could not be read", ErrUnavailable, method, path)
	}
	if len(payload) > maxResponseBytes {
		return fmt.Errorf("%w: %s %s answered more than %d bytes", ErrUnavailable, method, path, maxResponseBytes)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return decodeError(response, payload)
	}
	if answer == nil || len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, answer); err != nil {
		return fmt.Errorf("%w: %s %s answered something unreadable", ErrUnavailable, method, path)
	}
	return nil
}

// decodeError reads Composio's error envelope, {"error": {...}}, as far as it
// goes: not every field is present on every error.
func decodeError(response *http.Response, payload []byte) *Error {
	failure := &Error{Status: response.StatusCode}
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(payload, &envelope) == nil && len(envelope.Error) > 0 {
		var detail struct {
			Message   string `json:"message"`
			Code      any    `json:"code"`
			Slug      string `json:"slug"`
			RequestID string `json:"request_id"`
		}
		if json.Unmarshal(envelope.Error, &detail) == nil {
			failure.Message = detail.Message
			failure.RequestID = detail.RequestID
			failure.Code = detail.Slug
			if failure.Code == "" && detail.Code != nil {
				failure.Code = fmt.Sprint(detail.Code)
			}
		} else {
			var message string
			if json.Unmarshal(envelope.Error, &message) == nil {
				failure.Message = message
			}
		}
	}
	if seconds, err := strconv.Atoi(strings.TrimSpace(response.Header.Get("Retry-After"))); err == nil && seconds > 0 {
		failure.RetryAfter = time.Duration(seconds) * time.Second
	}
	return failure
}

// pageQuery adds a cursor and limit to a listing's query.
func pageQuery(query url.Values, cursor string, limit int) url.Values {
	if query == nil {
		query = url.Values{}
	}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	return query
}

// addAll adds a repeated query parameter, the form Composio's array
// parameters take.
func addAll(query url.Values, name string, values []string) {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			query.Add(name, value)
		}
	}
}

// pathEscape escapes one path segment, such as an id or a slug.
func pathEscape(segment string) string { return url.PathEscape(segment) }

// timeOf parses one of Composio's timestamps, leaving a zero time for
// anything it cannot read rather than failing a whole listing over it.
func timeOf(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// cursorOf reads a listing's next cursor, which Composio sends as null on the
// last page.
func cursorOf(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
