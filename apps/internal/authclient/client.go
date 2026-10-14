// Package authclient asks auth who the caller is in an organization.
// Apps never verifies a token itself: it forwards the caller's
// credentials and believes the answer, so a member's reach is decided in one
// place rather than reasoned about here.
package authclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/cliscope"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

const requestTimeout = 15 * time.Second

const maxResponseBytes = 1 << 20

// AccessCookieName is the cookie the web app signs a member in with.
const AccessCookieName = "oryxa_access"

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

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: requestTimeout, Transport: observability.Transport(nil)}}
}

type organizationResponse struct {
	CLI        bool `json:"cli"`
	Membership struct {
		UserID string `json:"user_id"`
		Role   string `json:"role"`
	} `json:"membership"`
	Agent bool `json:"agent"`
	// AgentClaims name which agent an agent token acts as.
	AgentClaims struct {
		Agent string `json:"agent"`
		Role  string `json:"role"`
	} `json:"agent_claims"`
}

// Membership answers what the caller is in an organization, and whether they
// are an agent acting for a member rather than the member themselves.
func (client *Client) Membership(ctx context.Context, organizationID string, credentials Credentials) (domain.OrgMembership, error) {
	if !credentials.Present() {
		return domain.OrgMembership{}, domain.ErrUnauthorized
	}
	path := "/v1/organizations/" + url.PathEscape(organizationID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+path, nil)
	if err != nil {
		return domain.OrgMembership{}, fmt.Errorf("build auth request: %w", err)
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
		return domain.OrgMembership{}, fmt.Errorf("%w: auth did not answer", domain.ErrUnavailable)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return domain.OrgMembership{}, fmt.Errorf("%w: auth's answer could not be read", domain.ErrUnavailable)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return domain.OrgMembership{}, authError(response.StatusCode)
	}

	var decoded organizationResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return domain.OrgMembership{}, fmt.Errorf("decode auth response")
	}
	if decoded.CLI && !cliscope.AllowedContext(ctx) {
		return domain.OrgMembership{}, domain.ErrForbidden
	}
	if decoded.Membership.UserID == "" {
		return domain.OrgMembership{}, domain.ErrForbidden
	}
	return domain.OrgMembership{
		OrganizationID: organizationID,
		UserID:         decoded.Membership.UserID,
		Role:           domain.OrgRole(decoded.Membership.Role),
		Agent:          decoded.Agent,
		AgentID:        decoded.AgentClaims.Agent,
		AgentRole:      agentRole(decoded.Agent, decoded.AgentClaims.Role),
	}, nil
}

// agentRole is an agent caller's role; an agent whose token names none is
// the primary.
func agentRole(agent bool, role string) string {
	if agent && role == "" {
		return "primary"
	}
	return role
}

func authError(status int) error {
	switch {
	case status == http.StatusUnauthorized:
		return domain.ErrUnauthorized
	case status == http.StatusForbidden, status == http.StatusNotFound:
		// Auth answers not-found for an organization the caller is not in, so
		// neither tells the caller anything they did not already know.
		return domain.ErrForbidden
	case status == http.StatusTooManyRequests:
		return domain.ErrRateLimited
	case status >= 500:
		return fmt.Errorf("%w: auth answered %d", domain.ErrUnavailable, status)
	default:
		return fmt.Errorf("auth answered %d: %w", status, domain.ErrInvalid)
	}
}
