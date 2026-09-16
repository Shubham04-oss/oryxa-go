// Package authclient asks auth who the caller is in an organization.
// Integrations never verifies tokens itself: it forwards the caller's
// credentials, so auth stays the only service that decides identity and
// organization membership.
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

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

const AccessCookieName = "oryxa_access"

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
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

type organizationResponse struct {
	Organization struct {
		ID string `json:"id"`
	} `json:"organization"`
	Membership struct {
		OrganizationID string `json:"organization_id"`
		UserID         string `json:"user_id"`
		Role           string `json:"role"`
	} `json:"membership"`
}

func (client *Client) Membership(ctx context.Context, organizationID string, credentials Credentials) (domain.OrgMembership, error) {
	var decoded organizationResponse
	if err := client.get(ctx, "/v1/organizations/"+url.PathEscape(organizationID), credentials, &decoded); err != nil {
		return domain.OrgMembership{}, err
	}
	if decoded.Organization.ID != organizationID || decoded.Membership.OrganizationID != organizationID || decoded.Membership.UserID == "" {
		return domain.OrgMembership{}, fmt.Errorf("auth answered for a different organization or no member")
	}
	return domain.OrgMembership{
		OrganizationID: organizationID,
		UserID:         decoded.Membership.UserID,
		Role:           domain.OrgRole(decoded.Membership.Role),
	}, nil
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

// AccessToken is the member's access token the credentials carry, which
// connections takes as a forwarded bearer token: the Authorization header's
// token, or the access cookie, which holds the same token.
func (credentials Credentials) AccessToken() string {
	if parts := strings.Fields(credentials.Authorization); len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return parts[1]
	}
	if credentials.Authorization != "" {
		return ""
	}
	return credentials.AccessCookie
}
