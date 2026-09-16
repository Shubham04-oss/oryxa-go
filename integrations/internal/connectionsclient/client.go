// Package connectionsclient calls connections for what integrations may not
// hold itself: a connection's details, and credentials for it. Every internal
// call carries integrations' signed service identity; a call made while a
// member acts also forwards the member's access token, so connections checks
// the member's access live.
package connectionsclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// Audience is the audience connections accepts service tokens for.
const Audience = "connections"

// Signer issues service tokens.
type Signer interface {
	Token(ctx context.Context, audience, purpose string) (string, error)
}

type Client struct {
	baseURL string
	http    *http.Client
	signer  Signer
}

func New(baseURL string, signer Signer) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: 15 * time.Second}, signer: signer}
}

// App is what connections shows members about an organization's app.
type App struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Status   string `json:"status"`
}

// Connection answers a connection the member whose access token is given can
// see.
func (client *Client) Connection(ctx context.Context, organizationID, connectionID, accessToken string) (domain.Connection, error) {
	var connection domain.Connection
	path := "/v1/internal/connections/" + url.PathEscape(connectionID) + "?organization_id=" + url.QueryEscape(organizationID)
	if err := client.call(ctx, http.MethodGet, path, domain.PurposeTrigger, accessToken, nil, &connection); err != nil {
		return domain.Connection{}, err
	}
	if connection.ID != connectionID || connection.OrganizationID != organizationID {
		return domain.Connection{}, fmt.Errorf("connections answered for a different connection")
	}
	return connection, nil
}

// TokenRequest asks for a credential: for the member whose access token is
// forwarded, or, without one, for the member the work runs as.
type TokenRequest struct {
	OrganizationID string
	ConnectionID   string
	Purpose        domain.Purpose
	AccessToken    string
	ActingMemberID string
}

type issuedToken struct {
	AccessToken string `json:"access_token"`
	Placement   struct {
		In     string `json:"in"`
		Name   string `json:"name"`
		Prefix string `json:"prefix"`
	} `json:"placement"`
	Domain   string            `json:"domain"`
	Metadata map[string]string `json:"metadata"`
}

func (client *Client) Token(ctx context.Context, request TokenRequest) (domain.Credential, error) {
	body := map[string]string{"organization_id": request.OrganizationID, "purpose": string(request.Purpose)}
	if request.AccessToken == "" {
		body["acting_member_id"] = request.ActingMemberID
	}
	var token issuedToken
	if err := client.call(ctx, http.MethodPost, "/v1/internal/connections/"+url.PathEscape(request.ConnectionID)+"/token",
		request.Purpose, request.AccessToken, body, &token); err != nil {
		return domain.Credential{}, err
	}
	if token.AccessToken == "" {
		return domain.Credential{}, fmt.Errorf("connections issued no credential")
	}
	return domain.Credential{
		AccessToken: token.AccessToken, In: token.Placement.In, Name: token.Placement.Name, Prefix: token.Placement.Prefix,
		Domain: token.Domain, Metadata: token.Metadata,
	}, nil
}

// AddHold keeps a connection's credentials after it is disconnected, until
// integrations releases the hold.
func (client *Client) AddHold(ctx context.Context, organizationID, connectionID string) error {
	return client.call(ctx, http.MethodPost, "/v1/internal/connections/"+url.PathEscape(connectionID)+"/holds", domain.PurposeCleanup, "",
		map[string]string{"organization_id": organizationID, "holder": domain.Holder}, nil)
}

func (client *Client) ReleaseHold(ctx context.Context, organizationID, connectionID string) error {
	path := "/v1/internal/connections/" + url.PathEscape(connectionID) + "/holds/" + url.PathEscape(domain.Holder) +
		"?organization_id=" + url.QueryEscape(organizationID)
	return client.call(ctx, http.MethodDelete, path, domain.PurposeCleanup, "", nil, nil)
}

// App answers an organization's app, as the member whose access token is given
// sees it on connections' public API.
func (client *Client) App(ctx context.Context, organizationID, appID, accessToken string) (App, error) {
	var list struct {
		Apps []App `json:"apps"`
	}
	if err := client.call(ctx, http.MethodGet, "/v1/organizations/"+url.PathEscape(organizationID)+"/apps", "", accessToken, nil, &list); err != nil {
		return App{}, err
	}
	for _, app := range list.Apps {
		if app.ID == appID {
			return app, nil
		}
	}
	return App{}, domain.ErrNotFound
}

// Connections are an organization's connections as the member whose access
// token is given sees them on connections' public API.
func (client *Client) Connections(ctx context.Context, organizationID, accessToken string) ([]domain.Connection, error) {
	var list struct {
		Connections []domain.Connection `json:"connections"`
	}
	if err := client.call(ctx, http.MethodGet, "/v1/organizations/"+url.PathEscape(organizationID)+"/connections", "", accessToken, nil, &list); err != nil {
		return nil, err
	}
	return list.Connections, nil
}

type errorBody struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

// call sends a request to connections: signed with integrations' identity for
// purpose, unless purpose is empty, which calls the public API as the member.
func (client *Client) call(ctx context.Context, method, path string, purpose domain.Purpose, accessToken string, body, target any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode connections request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build connections request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if purpose != "" {
		serviceToken, err := client.signer.Token(ctx, Audience, string(purpose))
		if err != nil {
			return fmt.Errorf("sign connections request: %w", err)
		}
		request.Header.Set(serviceauth.Header, serviceToken)
	}
	if accessToken != "" {
		request.Header.Set("Authorization", "Bearer "+accessToken)
	}
	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("%w: connections request failed", domain.ErrProviderUnavailable)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: connections response could not be read", domain.ErrProviderUnavailable)
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		if target != nil && json.Unmarshal(payload, target) != nil {
			return fmt.Errorf("decode connections response")
		}
		return nil
	}
	var failure errorBody
	_ = json.Unmarshal(payload, &failure)
	return connectionsError(response.StatusCode, failure.Error.Code)
}

// connectionsError turns connections' documented errors into integrations'.
func connectionsError(status int, code string) error {
	switch {
	case code == "needs_reconnect":
		return domain.ErrNeedsReconnect
	case code == "app_disabled":
		return fmt.Errorf("the connection's app is disabled: %w", domain.ErrForbidden)
	case code == "credential_rejected":
		return domain.ErrCredentialRejected
	case code == "scope_required":
		return fmt.Errorf("the connection is missing a scope: %w", domain.ErrForbidden)
	case status == http.StatusBadRequest:
		return fmt.Errorf("connections refused the request: %w", domain.ErrInvalid)
	case status == http.StatusUnauthorized:
		return domain.ErrUnauthorized
	case status == http.StatusForbidden:
		return domain.ErrForbidden
	case status == http.StatusNotFound:
		return domain.ErrNotFound
	case status == http.StatusConflict:
		return domain.ErrConflict
	case status == http.StatusServiceUnavailable || status == http.StatusTooManyRequests || status >= 500:
		return fmt.Errorf("%w: connections returned %d", domain.ErrProviderUnavailable, status)
	default:
		return fmt.Errorf("connections returned %d", status)
	}
}
