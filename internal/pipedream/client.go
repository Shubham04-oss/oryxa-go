// Package pipedream is Oryxa's client for Pipedream Connect's REST API, which
// connects a member's third-party apps, runs actions in them, deploys their
// triggers and proxies requests to their APIs.
//
// Pipedream has no Go SDK, so this speaks its REST API directly
// (https://pipedream.com/docs/connect/api-reference). It holds the project's
// OAuth client, which reaches every external user in the project, so only a
// backend that has already decided whose account a call may use should hold
// one of these: never an agent, and never a browser.
package pipedream

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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

// DefaultBaseURL is Pipedream's REST API. The OAuth token endpoint and every
// Connect endpoint hang off it.
const DefaultBaseURL = "https://api.pipedream.com/v1"

// Environments a Connect project has. Every project-scoped call names one in
// the x-pd-environment header, and accounts made in one are not seen in the
// other.
const (
	EnvironmentDevelopment = "development"
	EnvironmentProduction  = "production"
)

const (
	// requestTimeout bounds one call. An action can take a while, so it is
	// generous; a caller wanting less passes a context with its own deadline.
	requestTimeout = 90 * time.Second
	// maxResponseBytes caps what one call may hand back.
	maxResponseBytes = 32 << 20
	// tokenSlack is how long before its expiry a cached access token is
	// replaced, so a call never goes out with one that lapses in flight.
	tokenSlack = time.Minute
)

var (
	// ErrUnauthorized means Pipedream refused the OAuth client or the token
	// made from it, or the token may not do this.
	ErrUnauthorized = errors.New("pipedream refused the credentials")
	// ErrNotFound means what the call named does not exist in the project.
	ErrNotFound = errors.New("not found at pipedream")
	// ErrInvalid means Pipedream refused the request as malformed.
	ErrInvalid = errors.New("pipedream refused the request")
	// ErrRateLimited means the project is over a rate limit.
	ErrRateLimited = errors.New("pipedream rate limited the request")
	// ErrApp means an action ran and failed: in its own code, at the app's
	// API, or on the way there. Error.Origin says which.
	ErrApp = errors.New("the pipedream action failed")
	// ErrUnavailable means Pipedream did not answer usefully.
	ErrUnavailable = errors.New("pipedream is unavailable")
)

// Origins of an action's failure, as Pipedream attributes them
// (https://pipedream.com/docs/connect/components/actions#error-responses).
// Attribution is best-effort on Pipedream's side.
const (
	OriginComponentCode   = "component_code"
	OriginUpstreamAPI     = "upstream_api"
	OriginNetworkIO       = "network_io"
	OriginResponseParsing = "response_parsing"
)

// Error is what Pipedream answered when a call failed. Its message may repeat
// what the caller sent, or what an app's API said, so a caller decides
// whether to show it.
type Error struct {
	Status  int
	Code    string
	Message string
	// RetryAfter is how long Pipedream asked to wait, when it said.
	RetryAfter time.Duration
	// Origin is set when an action failed, and names where (Origin*).
	Origin string
	// UpstreamStatus is what the app's API answered on the action's last
	// call, when there was one.
	UpstreamStatus int
}

func (err *Error) Error() string {
	message := err.Message
	if message == "" {
		message = http.StatusText(err.Status)
	}
	if err.Origin != "" {
		return fmt.Sprintf("pipedream action failed (%s): %s", err.Origin, message)
	}
	return fmt.Sprintf("pipedream answered %d: %s", err.Status, message)
}

// Unwrap names the kind of failure, so a caller can errors.Is it.
func (err *Error) Unwrap() error {
	switch {
	case err.Origin != "":
		return ErrApp
	case err.Status == http.StatusUnauthorized, err.Status == http.StatusForbidden:
		return ErrUnauthorized
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

// Retryable says whether running the action again might succeed, following
// Pipedream's own table: a network failure or an app's 5xx may be transient;
// a bug in the component, an app's 4xx or an unreadable answer will not be.
func (err *Error) Retryable() bool {
	switch err.Origin {
	case OriginNetworkIO:
		return true
	case OriginUpstreamAPI:
		return err.UpstreamStatus >= 500
	case "":
		return err.Status == http.StatusTooManyRequests || err.Status >= 500
	default:
		return false
	}
}

// Config configures a Client.
type Config struct {
	// ProjectID is the Connect project, proj_… Required.
	ProjectID string
	// Environment is EnvironmentDevelopment or EnvironmentProduction.
	// Required.
	Environment string
	// ClientID and ClientSecret are the workspace's OAuth client. Required.
	ClientID     string
	ClientSecret string
	// BaseURL defaults to DefaultBaseURL. Anything else must be on a
	// loopback host: it exists for tests and fakes, and the OAuth client's
	// secret is sent to it.
	BaseURL string
	// HTTP defaults to a client with a generous timeout.
	HTTP *http.Client
}

// Client calls Pipedream's REST API. It is safe for concurrent use.
type Client struct {
	projectID    string
	environment  string
	clientID     string
	clientSecret string
	baseURL      string
	http         *http.Client
	now          func() time.Time

	// tokenMu is held across a refresh, so concurrent callers wait for the
	// one exchange rather than each making their own.
	tokenMu     sync.Mutex
	token       string
	tokenExpiry time.Time
}

// New builds a client, refusing one with anything required missing.
func New(config Config) (*Client, error) {
	config.ProjectID = strings.TrimSpace(config.ProjectID)
	config.ClientID = strings.TrimSpace(config.ClientID)
	config.ClientSecret = strings.TrimSpace(config.ClientSecret)
	switch {
	case config.ProjectID == "":
		return nil, errors.New("pipedream: a project ID is required")
	case config.ClientID == "" || config.ClientSecret == "":
		return nil, errors.New("pipedream: an OAuth client ID and secret are required")
	case config.Environment != EnvironmentDevelopment && config.Environment != EnvironmentProduction:
		return nil, fmt.Errorf("pipedream: environment %q must be development or production", config.Environment)
	}
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if baseURL != DefaultBaseURL {
		parsed, err := url.Parse(baseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("pipedream: base URL %q must be absolute", baseURL)
		}
		if !loopback(parsed.Hostname()) {
			return nil, fmt.Errorf("pipedream: base URL %q must be Pipedream's or on a loopback host", baseURL)
		}
	}
	httpClient := config.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout, Transport: observability.Transport(nil)}
	}
	return &Client{
		projectID: config.ProjectID, environment: config.Environment,
		clientID: config.ClientID, clientSecret: config.ClientSecret,
		baseURL: baseURL, http: httpClient, now: time.Now,
	}, nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// accessToken answers a cached OAuth access token, exchanging the client
// credentials for a new one when there is none or it is about to lapse
// (https://pipedream.com/docs/connect/api-reference/create-oauth-token).
func (client *Client) accessToken(ctx context.Context) (string, error) {
	client.tokenMu.Lock()
	defer client.tokenMu.Unlock()
	if client.token != "" && client.now().Before(client.tokenExpiry) {
		return client.token, nil
	}
	body := map[string]string{
		"grant_type": "client_credentials", "client_id": client.clientID, "client_secret": client.clientSecret,
	}
	var answer struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := client.send(ctx, http.MethodPost, "/oauth/token", nil, body, "", &answer); err != nil {
		return "", err
	}
	if answer.AccessToken == "" {
		return "", fmt.Errorf("%w: the token answer held no token", ErrUnavailable)
	}
	lifetime := time.Duration(answer.ExpiresIn) * time.Second
	if lifetime > 2*tokenSlack {
		lifetime -= tokenSlack
	} else {
		lifetime /= 2
	}
	client.token, client.tokenExpiry = answer.AccessToken, client.now().Add(lifetime)
	return client.token, nil
}

// forgetToken drops a token Pipedream refused, so the next call makes a new
// one rather than failing until the old one would have lapsed.
func (client *Client) forgetToken(refused string) {
	client.tokenMu.Lock()
	if client.token == refused {
		client.token = ""
	}
	client.tokenMu.Unlock()
}

// Page is one page of a listing. An empty NextCursor means there is nothing
// after it.
type Page[T any] struct {
	Items      []T
	NextCursor string
}

// pageInfo is Pipedream's paging envelope. end_cursor is what the next page
// passes as after.
type pageInfo struct {
	Count      int     `json:"count"`
	TotalCount int     `json:"total_count"`
	EndCursor  *string `json:"end_cursor"`
}

// next reads the next cursor. The reference does not say how the last page
// ends; the live API answers a null end_cursor there (seen 2026-10-13), and an
// empty page is taken as the end too, so a listing can never loop.
func (info pageInfo) next(items int) string {
	if info.EndCursor == nil || items == 0 {
		return ""
	}
	return *info.EndCursor
}

// project is the path of a project-scoped endpoint.
func (client *Client) project(path string) string {
	return "/connect/" + url.PathEscape(client.projectID) + path
}

// call sends one authenticated request and decodes a 2xx answer into answer,
// which may be nil when nothing in the answer is needed.
func (client *Client) call(ctx context.Context, method, path string, query url.Values, body, answer any) error {
	token, err := client.accessToken(ctx)
	if err != nil {
		return err
	}
	err = client.send(ctx, method, path, query, body, token, answer)
	var failure *Error
	if errors.As(err, &failure) && failure.Status == http.StatusUnauthorized {
		client.forgetToken(token)
	}
	return err
}

// send makes one request, with the bearer token when there is one, and every
// call names the environment: the Connect endpoints require it, and the ones
// that do not (apps, usage, the token) ignore it.
func (client *Client) send(ctx context.Context, method, path string, query url.Values, body any, token string, answer any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	endpoint := client.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("pipedream: encode %s %s: %w", method, path, err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("pipedream: build %s %s: %w", method, path, err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("x-pd-environment", client.environment)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := client.http.Do(request)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %s %s did not answer", ErrUnavailable, method, path)
	}
	defer response.Body.Close()
	payload, err := readCapped(response.Body)
	if err != nil {
		return fmt.Errorf("%w: %s %s: %v", ErrUnavailable, method, path, err)
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

func readCapped(body io.Reader) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return nil, errors.New("the answer could not be read")
	}
	if len(payload) > maxResponseBytes {
		return nil, fmt.Errorf("the answer is more than %d bytes", maxResponseBytes)
	}
	return payload, nil
}

// decodeError reads Pipedream's error envelope as far as it goes. The
// reference's ErrorResponse is {"error": "message", "code": "…"}; a failed
// action's error is an object with an attribution instead.
func decodeError(response *http.Response, payload []byte) *Error {
	failure := &Error{Status: response.StatusCode}
	var envelope struct {
		Error json.RawMessage `json:"error"`
		Code  string          `json:"code"`
	}
	if json.Unmarshal(payload, &envelope) == nil {
		failure.Code = envelope.Code
		var message string
		if json.Unmarshal(envelope.Error, &message) == nil {
			failure.Message = message
		} else {
			attribute(failure, envelope.Error)
		}
	}
	if seconds, err := strconv.Atoi(strings.TrimSpace(response.Header.Get("Retry-After"))); err == nil && seconds > 0 {
		failure.RetryAfter = time.Duration(seconds) * time.Second
	}
	return failure
}

// actionErrorWire is a failed action's error object.
type actionErrorWire struct {
	Name        string `json:"name"`
	Message     string `json:"message"`
	Attribution struct {
		Origin   string `json:"origin"`
		LastCall struct {
			Status int `json:"status"`
		} `json:"last_call"`
	} `json:"attribution"`
}

// attribute fills a failure from an action's error object, when raw is one.
// An error with no attribution still counts as the action's own.
func attribute(failure *Error, raw json.RawMessage) bool {
	var wire actionErrorWire
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &wire) != nil {
		return false
	}
	failure.Message = strings.TrimSpace(strings.TrimPrefix(wire.Name+": "+wire.Message, ": "))
	failure.Origin = wire.Attribution.Origin
	if failure.Origin == "" {
		failure.Origin = OriginComponentCode
	}
	failure.UpstreamStatus = wire.Attribution.LastCall.Status
	return true
}
