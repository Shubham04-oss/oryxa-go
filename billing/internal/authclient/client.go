// Package authclient asks auth who the caller is in an organization. Billing
// never verifies tokens itself: it forwards the caller's credentials, so auth
// stays the only service that decides identity and membership.
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

	"github.com/Shubham04-oss/oryxa-go/billing/internal/domain"
)

// AccessCookieName is auth's session cookie.
const AccessCookieName = "oryxa_access"

// Credentials are what the caller presented to billing.
type Credentials struct {
	// Authorization is the Authorization header, when the caller sent one.
	Authorization string
	// AccessCookie is the value of auth's session cookie, when the caller sent one.
	AccessCookie string
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
}

// Membership returns the caller's membership of the organization. A caller
// who isn't a member gets ErrNotFound, as auth conceals which organizations
// exist.
func (client *Client) Membership(ctx context.Context, organizationID string, credentials Credentials) (domain.Membership, error) {
	if !credentials.Present() {
		return domain.Membership{}, domain.ErrUnauthorized
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+"/v1/organizations/"+url.PathEscape(organizationID), nil)
	if err != nil {
		return domain.Membership{}, fmt.Errorf("build auth request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if credentials.Authorization != "" {
		request.Header.Set("Authorization", credentials.Authorization)
	} else {
		request.AddCookie(&http.Cookie{Name: AccessCookieName, Value: credentials.AccessCookie})
	}

	response, err := client.http.Do(request)
	if err != nil {
		return domain.Membership{}, fmt.Errorf("ask auth for membership: %w", err)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return domain.Membership{}, domain.ErrUnauthorized
	case http.StatusForbidden:
		return domain.Membership{}, domain.ErrForbidden
	case http.StatusNotFound:
		return domain.Membership{}, domain.ErrNotFound
	default:
		return domain.Membership{}, fmt.Errorf("ask auth for membership: status %d", response.StatusCode)
	}

	var decoded organizationResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&decoded); err != nil {
		return domain.Membership{}, fmt.Errorf("decode auth membership: %w", err)
	}
	if decoded.Organization.ID != organizationID || decoded.Membership.OrganizationID != organizationID || decoded.Membership.UserID == "" {
		return domain.Membership{}, fmt.Errorf("auth answered for a different organization or no member")
	}
	return domain.Membership{
		OrganizationID:   organizationID,
		OrganizationName: decoded.Organization.Name,
		UserID:           decoded.Membership.UserID,
		Email:            decoded.Membership.Email,
		DisplayName:      decoded.Membership.DisplayName,
		Role:             domain.Role(decoded.Membership.Role),
	}, nil
}
