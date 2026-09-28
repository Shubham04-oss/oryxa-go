package runtime

import (
	"bytes"
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

// Client talks to the existing organization and team APIs with a person's
// short-lived access token. It never persists that token or includes it in
// errors.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

type StatusError struct {
	Method string
	Path   string
	Status int
}

func (err *StatusError) Error() string {
	return fmt.Sprintf("%s %s: API returned %d", err.Method, err.Path, err.Status)
}

func IsStatus(err error, status int) bool {
	var remote *StatusError
	return errors.As(err, &remote) && remote.Status == status
}

func NewClient(baseURL, token string) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1"))) {
		return nil, fmt.Errorf("--api must be an HTTPS URL or a loopback HTTP URL")
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("ORYXA_ACCESS_TOKEN is required for remote commands")
	}
	return &Client{BaseURL: strings.TrimRight(parsed.String(), "/"), Token: token, HTTP: &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (client *Client) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.BaseURL+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+client.Token)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.HTTP.Do(request)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &StatusError{Method: method, Path: path, Status: response.StatusCode}
	}
	if output == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(output); err != nil {
		return fmt.Errorf("%s %s: invalid API response: %w", method, path, err)
	}
	return nil
}

type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type Member struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

type Invitation struct {
	Email  string `json:"email"`
	Role   string `json:"role"`
	Status string `json:"status"`
}

type Team struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type TeamMember struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

type Snapshot struct {
	Organization *Organization
	Members      map[string]Member
	Invitations  map[string]Invitation
	Teams        map[string]Team
	TeamMembers  map[string]map[string]TeamMember
}

func (client *Client) Snapshot(ctx context.Context, slug string) (Snapshot, error) {
	var organizations struct {
		Organizations []Organization `json:"organizations"`
	}
	if err := client.request(ctx, http.MethodGet, "/v1/organizations", nil, &organizations); err != nil {
		return Snapshot{}, err
	}
	state := Snapshot{
		Members: map[string]Member{}, Invitations: map[string]Invitation{},
		Teams: map[string]Team{}, TeamMembers: map[string]map[string]TeamMember{},
	}
	for _, organization := range organizations.Organizations {
		if organization.Slug == slug {
			state.Organization = &organization
			break
		}
	}
	if state.Organization == nil {
		var current struct {
			User struct {
				ID    string `json:"id"`
				Email string `json:"email"`
			} `json:"user"`
		}
		if err := client.request(ctx, http.MethodGet, "/v1/auth/me", nil, &current); err != nil {
			return Snapshot{}, err
		}
		if current.User.ID != "" && current.User.Email != "" {
			state.Members[strings.ToLower(current.User.Email)] = Member{
				UserID: current.User.ID, Email: strings.ToLower(current.User.Email), Role: "owner",
			}
		}
		return state, nil
	}
	base := "/v1/organizations/" + url.PathEscape(state.Organization.ID)
	var members struct {
		Members []Member `json:"members"`
	}
	if err := client.request(ctx, http.MethodGet, base+"/members", nil, &members); err != nil {
		return Snapshot{}, err
	}
	for _, member := range members.Members {
		state.Members[strings.ToLower(member.Email)] = member
	}
	var invitations struct {
		Invitations []Invitation `json:"invitations"`
	}
	if err := client.request(ctx, http.MethodGet, base+"/invitations", nil, &invitations); err != nil {
		return Snapshot{}, err
	}
	for _, invitation := range invitations.Invitations {
		if invitation.Status == "pending" {
			state.Invitations[strings.ToLower(invitation.Email)] = invitation
		}
	}
	var teams struct {
		Teams []Team `json:"teams"`
	}
	if err := client.request(ctx, http.MethodGet, base+"/teams", nil, &teams); err != nil {
		return Snapshot{}, err
	}
	for _, team := range teams.Teams {
		state.Teams[team.Name] = team
		var result struct {
			Members []TeamMember `json:"members"`
		}
		if err := client.request(ctx, http.MethodGet, base+"/teams/"+url.PathEscape(team.ID)+"/members", nil, &result); err != nil {
			return Snapshot{}, err
		}
		state.TeamMembers[team.Name] = map[string]TeamMember{}
		for _, member := range result.Members {
			state.TeamMembers[team.Name][strings.ToLower(member.Email)] = member
		}
	}
	return state, nil
}

func (client *Client) Whoami(ctx context.Context, output *json.RawMessage) error {
	return client.request(ctx, http.MethodGet, "/v1/auth/me", nil, output)
}

func (client *Client) CreateOrganization(ctx context.Context, name, slug string) error {
	return client.request(ctx, http.MethodPost, "/v1/organizations", map[string]string{"name": name, "slug": slug}, nil)
}

func (client *Client) UpdateOrganization(ctx context.Context, id, name, slug string) error {
	return client.request(ctx, http.MethodPatch, "/v1/organizations/"+url.PathEscape(id), map[string]string{"name": name, "slug": slug}, nil)
}

func (client *Client) Invite(ctx context.Context, orgID, email, role string) error {
	return client.request(ctx, http.MethodPost, "/v1/organizations/"+url.PathEscape(orgID)+"/invitations", map[string]string{"email": email, "role": role}, nil)
}

func (client *Client) ChangeMemberRole(ctx context.Context, orgID, memberID, role string) error {
	return client.request(ctx, http.MethodPatch, "/v1/organizations/"+url.PathEscape(orgID)+"/members/"+url.PathEscape(memberID), map[string]string{"role": role}, nil)
}

func (client *Client) CreateTeam(ctx context.Context, orgID, name string) error {
	return client.request(ctx, http.MethodPost, "/v1/organizations/"+url.PathEscape(orgID)+"/teams", map[string]string{"name": name}, nil)
}

func (client *Client) AddTeamMember(ctx context.Context, orgID, teamID, memberID string) error {
	return client.request(ctx, http.MethodPost, "/v1/organizations/"+url.PathEscape(orgID)+"/teams/"+url.PathEscape(teamID)+"/members", map[string]string{"user_id": memberID, "role": "member"}, nil)
}
