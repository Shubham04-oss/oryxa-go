// Package teamsclient asks teams whether the caller can see a team. Files
// never copies the team graph: it forwards the caller's credentials, so
// teams stays the service that conceals teams the caller cannot see.
package teamsclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/files/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

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

type teamResponse struct {
	Team struct {
		ID string `json:"id"`
	} `json:"team"`
	// Membership is the caller's own place on the team, and null when they
	// see it only as an org admin.
	Membership *struct {
		UserID string `json:"user_id"`
		Role   string `json:"role"`
	} `json:"membership"`
	CanManage bool `json:"can_manage"`
}

// teamAdminRole is teams' name for the role that manages a team in its own
// right, whatever the holder's role in the organization.
const teamAdminRole = "admin"

func (client *Client) Team(ctx context.Context, organizationID, teamID string, credentials authclient.Credentials) (domain.TeamAccess, error) {
	if strings.TrimSpace(organizationID) == "" || strings.TrimSpace(teamID) == "" {
		return domain.TeamAccess{}, domain.ErrNotFound
	}
	var decoded teamResponse
	path := "/v1/organizations/" + url.PathEscape(organizationID) + "/teams/" + url.PathEscape(teamID)
	if err := client.get(ctx, path, credentials, &decoded); err != nil {
		return domain.TeamAccess{}, err
	}
	if decoded.Team.ID != teamID {
		return domain.TeamAccess{}, fmt.Errorf("teams answered for a different team")
	}
	member := decoded.Membership != nil && decoded.Membership.UserID != ""
	return domain.TeamAccess{
		TeamID:    decoded.Team.ID,
		CanManage: decoded.CanManage,
		Member:    member,
		TeamAdmin: member && decoded.Membership.Role == teamAdminRole,
	}, nil
}

func (client *Client) get(ctx context.Context, path string, credentials authclient.Credentials, target any) error {
	if !credentials.Present() {
		return domain.ErrUnauthorized
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build teams request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if credentials.Authorization != "" {
		request.Header.Set("Authorization", credentials.Authorization)
	} else {
		request.AddCookie(&http.Cookie{Name: authclient.AccessCookieName, Value: credentials.AccessCookie})
	}

	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("ask teams: %w", err)
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
		return fmt.Errorf("ask teams: status %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
		return fmt.Errorf("decode teams response: %w", err)
	}
	return nil
}
