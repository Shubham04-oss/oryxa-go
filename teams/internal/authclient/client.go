// Package authclient asks auth who the caller is in an organization. Teams
// never verifies tokens itself: it forwards the caller's credentials, so auth
// stays the only service that decides identity and organization membership.
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

	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
)

// AccessCookieName is auth's session cookie.
const AccessCookieName = "oryxa_access"

// Credentials are what the caller presented to teams.
type Credentials struct {
	Authorization string
	AccessCookie  string
}

func (credentials Credentials) Present() bool {
	return credentials.Authorization != "" || credentials.AccessCookie != ""
}

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 10 * time.Second, Transport: observability.Transport(nil)},
	}
}

type organizationResponse struct {
	Organization struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"organization"`
	Membership struct {
		OrganizationID string `json:"organization_id"`
		UserID         string `json:"user_id"`
		Email          string `json:"email"`
		DisplayName    string `json:"display_name"`
		Role           string `json:"role"`
	} `json:"membership"`
	// Agent is set on a token auth minted for an agent, and absent on a
	// member's own session.
	Agent bool `json:"agent"`
}

type membersResponse struct {
	Members []struct {
		UserID      string `json:"user_id"`
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
		Role        string `json:"role"`
	} `json:"members"`
}

// Membership returns the caller's membership of the organization, and
// whether they are an agent acting for a member rather than the member
// themselves. A caller who isn't a member gets ErrNotFound, as auth conceals
// which organizations exist.
func (client *Client) Membership(ctx context.Context, organizationID string, credentials Credentials) (domain.OrgMembership, error) {
	var decoded organizationResponse
	if err := client.get(ctx, "/v1/organizations/"+url.PathEscape(organizationID), credentials, &decoded); err != nil {
		return domain.OrgMembership{}, err
	}
	if decoded.Organization.ID != organizationID || decoded.Membership.OrganizationID != organizationID || decoded.Membership.UserID == "" {
		return domain.OrgMembership{}, fmt.Errorf("auth answered for a different organization or no member")
	}
	return domain.OrgMembership{
		OrganizationID:   organizationID,
		OrganizationName: decoded.Organization.Name,
		UserID:           decoded.Membership.UserID,
		Email:            decoded.Membership.Email,
		DisplayName:      decoded.Membership.DisplayName,
		Role:             domain.OrgRole(decoded.Membership.Role),
		Agent:            decoded.Agent,
	}, nil
}

// Members lists the organization's members, as auth sees them. Adding someone
// to a team requires they already belong to the organization.
func (client *Client) Members(ctx context.Context, organizationID string, credentials Credentials) ([]domain.OrgMembership, error) {
	var decoded membersResponse
	if err := client.get(ctx, "/v1/organizations/"+url.PathEscape(organizationID)+"/members", credentials, &decoded); err != nil {
		return nil, err
	}
	members := make([]domain.OrgMembership, 0, len(decoded.Members))
	for _, member := range decoded.Members {
		if member.UserID == "" {
			continue
		}
		members = append(members, domain.OrgMembership{
			OrganizationID: organizationID,
			UserID:         member.UserID,
			Email:          member.Email,
			DisplayName:    member.DisplayName,
			Role:           domain.OrgRole(member.Role),
		})
	}
	return members, nil
}

func (client *Client) get(ctx context.Context, path string, credentials Credentials, target any) error {
	if !credentials.Present() {
		return domain.ErrUnauthorized
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build auth request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if credentials.Authorization != "" {
		request.Header.Set("Authorization", credentials.Authorization)
	} else {
		request.AddCookie(&http.Cookie{Name: AccessCookieName, Value: credentials.AccessCookie})
	}

	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("ask auth: %w", err)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return domain.ErrUnauthorized
	case http.StatusForbidden:
		return domain.ErrForbidden
	case http.StatusNotFound:
		return domain.ErrNotFound
	default:
		return fmt.Errorf("ask auth: status %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
		return fmt.Errorf("decode auth response: %w", err)
	}
	return nil
}
