// Package appsclient asks apps for what a provider source needs before it may
// index through a connection: whether it is usable, and whose it is. Apps
// forwards nothing secret — a connection's credential never leaves Composio —
// so this holds nothing worth protecting either.
package appsclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

const requestTimeout = 15 * time.Second

// maxResponseBytes bounds what apps may hand back: one connection, never a
// listing.
const maxResponseBytes = 1 << 20

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: requestTimeout}}
}

type connectionResponse struct {
	ID                string `json:"id"`
	Toolkit           string `json:"toolkit"`
	Status            string `json:"status"`
	Label             string `json:"label"`
	MemberID          string `json:"member_id"`
	ComposioAccountID string `json:"composio_account_id"`
}

// Connection reads one of a member's connections to a third-party app,
// forwarding the caller's own credentials exactly as authclient does: apps
// never verifies a token itself, so what the caller may see is decided
// there, not here.
func (client *Client) Connection(ctx context.Context, organizationID, connectionID string, credentials authclient.Credentials) (domain.AppConnection, error) {
	if !credentials.Present() {
		return domain.AppConnection{}, domain.ErrUnauthorized
	}
	path := "/v1/organizations/" + url.PathEscape(organizationID) + "/apps/connections/" + url.PathEscape(connectionID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+path, nil)
	if err != nil {
		return domain.AppConnection{}, fmt.Errorf("build apps request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if credentials.Authorization != "" {
		request.Header.Set("Authorization", credentials.Authorization)
	}
	if credentials.AccessCookie != "" {
		request.AddCookie(&http.Cookie{Name: authclient.AccessCookieName, Value: credentials.AccessCookie})
	}

	response, err := client.http.Do(request)
	if err != nil {
		return domain.AppConnection{}, fmt.Errorf("%w: apps did not answer", domain.ErrProviderUnavailable)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return domain.AppConnection{}, fmt.Errorf("%w: apps' answer could not be read", domain.ErrProviderUnavailable)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return domain.AppConnection{}, appsError(response.StatusCode)
	}

	var decoded connectionResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return domain.AppConnection{}, fmt.Errorf("decode apps response")
	}
	return domain.AppConnection{
		ID: decoded.ID, Toolkit: decoded.Toolkit, Status: decoded.Status,
		MemberID: decoded.MemberID, ComposioAccountID: decoded.ComposioAccountID, Label: decoded.Label,
	}, nil
}

func appsError(status int) error {
	switch {
	case status == http.StatusUnauthorized:
		return domain.ErrUnauthorized
	case status == http.StatusForbidden, status == http.StatusNotFound:
		// A connection the caller may not see answers the same as one that
		// does not exist, so neither tells them anything they did not know.
		return domain.ErrNotFound
	case status == http.StatusTooManyRequests:
		return domain.ErrRateLimited
	case status >= 500:
		return fmt.Errorf("%w: apps answered %d", domain.ErrProviderUnavailable, status)
	default:
		return fmt.Errorf("apps answered %d: %w", status, domain.ErrInvalid)
	}
}
