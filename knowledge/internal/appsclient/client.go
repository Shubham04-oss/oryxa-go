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

type targetsResponse struct {
	Targets []struct {
		Kind   string            `json:"kind"`
		ID     string            `json:"id"`
		Name   string            `json:"name"`
		Target map[string]string `json:"target"`
	} `json:"targets"`
}

// Targets reads what a connection holds that could be indexed — a
// repository, a channel, a database — as apps discovered it from the app
// itself. Each carries the target a source is registered with, so nothing
// here has to know each provider's spelling of one.
func (client *Client) Targets(ctx context.Context, organizationID, connectionID string, credentials authclient.Credentials) ([]domain.AppTarget, error) {
	if !credentials.Present() {
		return nil, domain.ErrUnauthorized
	}
	path := "/v1/organizations/" + url.PathEscape(organizationID) +
		"/apps/connections/" + url.PathEscape(connectionID) + "/targets"
	payload, err := client.get(ctx, path, credentials)
	if err != nil {
		return nil, err
	}
	var decoded targetsResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("decode apps response")
	}
	targets := make([]domain.AppTarget, 0, len(decoded.Targets))
	for _, target := range decoded.Targets {
		if len(target.Target) == 0 {
			continue
		}
		targets = append(targets, domain.AppTarget{
			Kind: target.Kind, ID: target.ID, Name: target.Name, Target: target.Target,
		})
	}
	return targets, nil
}

// get makes one authenticated read of apps, forwarding the caller's own
// credentials: apps never verifies a token itself, so what a caller may see
// is decided there rather than here.
func (client *Client) get(ctx context.Context, path string, credentials authclient.Credentials) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build apps request: %w", err)
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
		return nil, fmt.Errorf("%w: apps did not answer", domain.ErrProviderUnavailable)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: apps' answer could not be read", domain.ErrProviderUnavailable)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, appsError(response.StatusCode)
	}
	return payload, nil
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
