// Package authclient asks auth who the caller is in an organization. Agents
// never verifies a token itself: it forwards the caller's credentials and
// believes the answer, so a member's reach is decided in one place.
package authclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const requestTimeout = 15 * time.Second

const maxResponseBytes = 1 << 20

// AccessCookieName is the cookie the web app signs a member in with. It holds
// the same access token a bearer header would.
const AccessCookieName = "oryxa_access"

// The ways asking auth can fail. Each is what the caller is told.
var (
	// ErrUnauthorized reports that the caller offered no credentials, or
	// credentials auth does not accept.
	ErrUnauthorized = errors.New("authentication failed")
	// ErrForbidden reports that the caller is not a member of the
	// organization. Auth answers not-found for an organization the caller is
	// not in, and neither tells the caller anything they did not already know.
	ErrForbidden = errors.New("not a member of this organization")
	// ErrRateLimited reports that auth asked the caller to slow down.
	ErrRateLimited = errors.New("too many requests; retry")
	// ErrUnavailable reports that auth did not answer, or answered with a
	// server error.
	ErrUnavailable = errors.New("the sign-in service did not answer; retry")
)

// Credentials are how a caller proves who they are: a bearer token, or the
// access cookie the web app sends.
type Credentials struct {
	Authorization string
	AccessCookie  string
}

// Present says the caller offered something to be identified by.
func (credentials Credentials) Present() bool {
	return credentials.Authorization != "" || credentials.AccessCookie != ""
}

// AccessToken is the member's own access token, whichever way it came: the
// run carries it so the tools that act for the member present it as theirs.
func (credentials Credentials) AccessToken() string {
	if token, ok := bearerToken(credentials.Authorization); ok {
		return token
	}
	return credentials.AccessCookie
}

// Membership is who the caller is in an organization.
type Membership struct {
	OrganizationID string
	UserID         string
	// Role is owner, admin or member.
	Role string
	// Agent says the caller is an agent acting for the member rather than
	// the member themselves.
	Agent bool
}

// Client asks auth.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a client for auth at baseURL.
func New(baseURL string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: requestTimeout}}
}

type organizationResponse struct {
	Membership struct {
		UserID string `json:"user_id"`
		Role   string `json:"role"`
	} `json:"membership"`
	Agent bool `json:"agent"`
}

// Membership answers what the caller is in an organization.
func (client *Client) Membership(ctx context.Context, organizationID string, credentials Credentials) (Membership, error) {
	if !credentials.Present() {
		return Membership{}, ErrUnauthorized
	}
	path := "/v1/organizations/" + url.PathEscape(organizationID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+path, nil)
	if err != nil {
		return Membership{}, fmt.Errorf("build auth request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if credentials.Authorization != "" {
		request.Header.Set("Authorization", credentials.Authorization)
	}
	if credentials.AccessCookie != "" {
		request.AddCookie(&http.Cookie{Name: AccessCookieName, Value: credentials.AccessCookie})
	}

	response, err := client.http.Do(request)
	if err != nil {
		return Membership{}, ErrUnavailable
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return Membership{}, ErrUnavailable
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Membership{}, statusError(response.StatusCode)
	}

	var decoded organizationResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return Membership{}, errors.New("decode auth response")
	}
	if decoded.Membership.UserID == "" {
		return Membership{}, ErrForbidden
	}
	return Membership{
		OrganizationID: organizationID,
		UserID:         decoded.Membership.UserID,
		Role:           decoded.Membership.Role,
		Agent:          decoded.Agent,
	}, nil
}

func statusError(status int) error {
	switch {
	case status == http.StatusUnauthorized:
		return ErrUnauthorized
	case status == http.StatusForbidden, status == http.StatusNotFound:
		return ErrForbidden
	case status == http.StatusTooManyRequests:
		return ErrRateLimited
	case status >= 500:
		return ErrUnavailable
	default:
		return fmt.Errorf("auth answered %d", status)
	}
}

func bearerToken(value string) (string, bool) {
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}
