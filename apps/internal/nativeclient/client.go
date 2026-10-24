// Package nativeclient calls the integrations service's internal API for the
// toolkits apps hands to its native broker.
package nativeclient

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

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

const (
	// Audience is the service name integrations verifies tokens against.
	Audience = "integrations"
	// ConnectPurpose is the purpose of the token that starts a sign-in.
	ConnectPurpose = "connect"
	// ToolsPurpose is the purpose of the token that runs a tool or lists a
	// member's tools.
	ToolsPurpose = "tools"
	// ConnectionsPurpose is the purpose of the token that lists or removes
	// connections.
	ConnectionsPurpose = "connections"

	requestTimeout   = 30 * time.Second
	maxResponseBytes = 1 << 20
)

// Errors apps maps to its own answers. Anything else is an unexpected failure
// of the integrations service.
var (
	// ErrNotSetUp is a connect for an app neither the org nor Oryxa has an
	// OAuth app for: the member's admin has to set it up.
	ErrNotSetUp = errors.New("nativeclient: the app is not set up")
	// ErrBroken is a connect for an app whose org OAuth app is not working.
	ErrBroken = errors.New("nativeclient: the org's app is not working")
	// ErrNotFound is an app the integrations service has no connector for.
	ErrNotFound = errors.New("nativeclient: no such app")
	// ErrInvalid is a request the integrations service refused as malformed.
	ErrInvalid = errors.New("nativeclient: invalid request")
	// ErrConfirmation is a tool that changes something, called unconfirmed.
	// Nothing ran.
	ErrConfirmation = errors.New("nativeclient: the tool needs the member's confirmation")
	// ErrNeedsReauth is a connection the member has to sign in to again.
	ErrNeedsReauth = errors.New("nativeclient: the connection needs a new sign-in")
	// ErrUnavailable is a connection that cannot be used right now.
	ErrUnavailable = errors.New("nativeclient: the connection is not usable")
	// ErrOutputTooLarge is a tool whose output was over the service's limit.
	ErrOutputTooLarge = errors.New("nativeclient: the tool's output is too large")
)

// Signer proves which service is calling. serviceauth.Signer satisfies it.
type Signer interface {
	Token(ctx context.Context, audience, purpose string) (string, error)
}

// Config configures a Client.
type Config struct {
	// BaseURL is the integrations service's URL. It must be HTTPS or loopback
	// HTTP, since a service token travels on it.
	BaseURL string
	Signer  Signer
	// HTTP is the client requests go out on. It is copied and made to refuse
	// redirects: a service token is never sent anywhere its URL did not name.
	HTTP *http.Client
}

// Client calls the integrations service. It is safe for concurrent use.
type Client struct {
	baseURL string
	signer  Signer
	http    *http.Client
}

// New returns a client for the integrations service at config.BaseURL.
func New(config Config) (*Client, error) {
	baseURL, err := CheckURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	if config.Signer == nil {
		return nil, errors.New("nativeclient: a service token signer is required")
	}
	httpClient := config.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout, Transport: observability.Transport(nil)}
	}
	copied := *httpClient
	copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: baseURL, signer: config.Signer, http: &copied}, nil
}

// CheckURL answers the integrations URL to use: HTTPS, or HTTP on a loopback
// host, with no credentials, query or fragment.
func CheckURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("nativeclient: the integrations URL must be an absolute URL without credentials, query or fragment")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopback(parsed.Hostname())) {
		return "", errors.New("nativeclient: the integrations URL must be https, or http on a loopback host")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ConnectRequest is what POST /v1/connect takes.
type ConnectRequest struct {
	Org      string `json:"org"`
	Member   string `json:"member"`
	App      string `json:"app"`
	Level    string `json:"level"`
	ReturnTo string `json:"return_to"`
	// BrowserHash is the hex SHA-256 of the nonce in the member's browser
	// cookie; the integrations callback asks for that cookie back.
	BrowserHash string `json:"browser_hash"`
}

// Connect starts a sign-in and answers the URL the member's browser goes to.
func (client *Client) Connect(ctx context.Context, request ConnectRequest) (string, error) {
	var answer struct {
		URL string `json:"url"`
	}
	if err := client.post(ctx, "/v1/connect", ConnectPurpose, request, &answer); err != nil {
		return "", err
	}
	if answer.URL == "" {
		return "", errors.New("nativeclient: connect answered no URL")
	}
	return answer.URL, nil
}

// CallRequest is what POST /v1/tools/call takes.
type CallRequest struct {
	Org        string          `json:"org"`
	Member     string          `json:"member"`
	Agent      string          `json:"agent"`
	Role       string          `json:"role"`
	Connection string          `json:"connection"`
	Tool       string          `json:"tool"`
	Input      json.RawMessage `json:"input"`
	Confirmed  bool            `json:"confirmed"`
}

// ToolError is a failure the app or connector reported when the tool ran.
type ToolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// CallResult is a tool's answer: Output, or the ToolError the app gave. A
// tool that could not run at all is an error from CallTool instead.
type CallResult struct {
	Output    json.RawMessage `json:"output"`
	ToolError *ToolError      `json:"tool_error"`
}

// CallTool runs a tool of a connection for a member and agent apps has
// already authenticated.
func (client *Client) CallTool(ctx context.Context, request CallRequest) (CallResult, error) {
	var result CallResult
	err := client.post(ctx, "/v1/tools/call", ToolsPurpose, request, &result)
	return result, err
}

// Remove revokes a connection of the org at the integrations service. A
// connection it does not know (already removed, or never there) is
// ErrNotFound.
func (client *Client) Remove(ctx context.Context, org, connection string) error {
	var answer struct {
		ProviderRevoked bool `json:"provider_revoked"`
	}
	path := "/v1/connections/" + url.PathEscape(connection) + "?" + url.Values{"org": {org}}.Encode()
	return client.do(ctx, http.MethodDelete, path, ConnectionsPurpose, nil, &answer)
}

// ListConnections answers the org's live connections at the integrations
// service, in the shape of its events: with a member, that member's own and
// the org-level ones; without, every one of the org. A revoked connection is
// not listed.
func (client *Client) ListConnections(ctx context.Context, org, member string) ([]integrationsv1.Connection, error) {
	var answer struct {
		Connections []integrationsv1.Connection `json:"connections"`
	}
	query := url.Values{"org": {org}}
	if member != "" {
		query.Set("member", member)
	}
	if err := client.do(ctx, http.MethodGet, "/v1/connections?"+query.Encode(), ConnectionsPurpose, nil, &answer); err != nil {
		return nil, err
	}
	if answer.Connections == nil {
		return nil, errors.New("nativeclient: the connections list answered no connections field")
	}
	return answer.Connections, nil
}

// Tool is one tool a member's connection offers, as its connector's manifest
// declares it. Name is the manifest's, the app's id and a dot before the
// tool's own name, as github.list_issues.
type Tool struct {
	App         string `json:"app"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Effect is read, write, external or delete; only read runs unconfirmed.
	Effect      string          `json:"effect"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ListTools answers the tools of the apps a member's live connections use;
// with app, that app's only.
func (client *Client) ListTools(ctx context.Context, org, member, app string) ([]Tool, error) {
	var answer struct {
		Tools []Tool `json:"tools"`
	}
	query := url.Values{"org": {org}, "member": {member}}
	if app != "" {
		query.Set("app", app)
	}
	if err := client.do(ctx, http.MethodGet, "/v1/tools?"+query.Encode(), ToolsPurpose, nil, &answer); err != nil {
		return nil, err
	}
	return answer.Tools, nil
}

func (client *Client) post(ctx context.Context, path, purpose string, body, into any) error {
	return client.do(ctx, http.MethodPost, path, purpose, body, into)
}

func (client *Client) do(ctx context.Context, method, path, purpose string, body, into any) error {
	var encoded io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("nativeclient: encode request: %w", err)
		}
		encoded = bytes.NewReader(data)
	}
	token, err := client.signer.Token(ctx, Audience, purpose)
	if err != nil {
		return fmt.Errorf("nativeclient: sign service token: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, encoded)
	if err != nil {
		return fmt.Errorf("nativeclient: build request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("nativeclient: %s: %w", path, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("nativeclient: read %s: %w", path, err)
	}
	if response.StatusCode != http.StatusOK {
		return statusError(path, response.StatusCode, data)
	}
	if err := json.Unmarshal(data, into); err != nil {
		return fmt.Errorf("nativeclient: decode %s: %w", path, err)
	}
	return nil
}

// statusError maps the service's error body, {"error":{"code":…}}, to one of
// the package's errors; the body's message is not repeated, as it is the
// service's to word.
func statusError(path string, status int, data []byte) error {
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &body)
	switch body.Error.Code {
	case "not_set_up":
		return ErrNotSetUp
	case "app_broken":
		return ErrBroken
	case "not_found":
		return ErrNotFound
	case "invalid_request":
		return ErrInvalid
	case "confirmation_required":
		return ErrConfirmation
	case "needs_reauth":
		return ErrNeedsReauth
	case "connection_unavailable":
		return ErrUnavailable
	case "output_too_large":
		return ErrOutputTooLarge
	}
	return fmt.Errorf("nativeclient: %s answered %d (%s)", path, status, body.Error.Code)
}
