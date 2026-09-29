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
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

// Client talks to the existing organization and team APIs with a person's
// short-lived access token. It never persists that token or includes it in
// errors.
type Client struct {
	BaseURL        string
	Token          string
	HTTP           *http.Client
	TokenExpiresAt time.Time
}

const ApplyTimeout = 3 * time.Minute

var ErrSessionExpiring = errors.New("CLI session expires before the apply budget; sign in again before applying")

type StatusError struct {
	Method string
	Path   string
	Status int
	Detail string
	Code   string
}

func (err *StatusError) Error() string {
	message := fmt.Sprintf("%s %s: API returned %d", err.Method, err.Path, err.Status)
	if err.Detail != "" {
		message += ": " + err.Detail
	}
	return message
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
	httpClient := client.HTTP
	if method == http.MethodPut && strings.HasSuffix(path, "/workspace") || method == http.MethodPost && strings.HasSuffix(path, "/workspace:rollback") {
		if !client.TokenExpiresAt.IsZero() && client.TokenExpiresAt.Before(time.Now().Add(ApplyTimeout+30*time.Second)) {
			return ErrSessionExpiring
		}
		copy := *client.HTTP
		copy.Timeout = ApplyTimeout
		httpClient = &copy
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return statusError(response, method, path)
	}
	if output == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(output); err != nil {
		return fmt.Errorf("%s %s: invalid API response: %w", method, path, err)
	}
	return nil
}

var errorCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var credentialPattern = regexp.MustCompile(`(?i)(bearer|token|credential|secret|password|authorization|eyJ[A-Za-z0-9_-]+\.|xox[baprs]-|ghp_|sk_live_|AKIA[0-9A-Z]|[A-Za-z0-9_+/=-]{80,})`)

// Only bounded structured client errors from our setup APIs carry detail.
// Provider responses and any credential-like text are never propagated.
func statusError(response *http.Response, method, path string) *StatusError {
	result := &StatusError{Method: method, Path: path, Status: response.StatusCode}
	if response.StatusCode < 400 || response.StatusCode >= 500 {
		return result
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	setupAPI := len(parts) >= 3 && parts[0] == "v1" && parts[1] == "organizations" &&
		(len(parts) == 3 || strings.HasPrefix(parts[3], "workspace") || parts[3] == "members" || parts[3] == "invitations" || parts[3] == "teams" || parts[3] == "organization-agent")
	if !setupAPI || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		return result
	}
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1024)).Decode(&body) == nil {
		detail := strings.TrimSpace(body.Error)
		if len(detail) <= 512 && strings.IndexFunc(detail, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) < 0 && !credentialPattern.MatchString(detail) {
			result.Detail = detail
		}
		if errorCodePattern.MatchString(body.Code) {
			result.Code = body.Code
		}
	}
	return result
}

type Organization struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Slug          string   `json:"slug"`
	Domains       []string `json:"domains"`
	Timezone      string   `json:"timezone"`
	GuestsAllowed bool     `json:"guests_allowed"`
}

// OrganizationBySlug resolves a member's organization without fetching the
// full workspace snapshot needed for planning.
func (client *Client) OrganizationBySlug(ctx context.Context, slug string) (Organization, error) {
	var listed struct {
		Organizations []Organization `json:"organizations"`
	}
	if err := client.request(ctx, http.MethodGet, "/v1/organizations", nil, &listed); err != nil {
		return Organization{}, err
	}
	for _, organization := range listed.Organizations {
		if organization.Slug == slug {
			return organization, nil
		}
	}
	return Organization{}, fmt.Errorf("organization %q was not found", slug)
}

type AppConnection struct {
	ID            string `json:"id"`
	Toolkit       string `json:"toolkit"`
	Status        string `json:"status"`
	StatusReason  string `json:"status_reason,omitempty"`
	AutoSubscribe bool   `json:"auto_subscribe"`
}

type ExternalMapping struct {
	Source     string `json:"source"`
	Kind       string `json:"kind"`
	ExternalID string `json:"external_id"`
	Reference  string `json:"reference"`
	ResourceID string `json:"resource_id,omitempty"`
}

func (client *Client) WorkspaceMappings(ctx context.Context, organizationID string) ([]ExternalMapping, error) {
	var listed struct {
		Mappings []ExternalMapping `json:"mappings"`
	}
	path := "/v1/organizations/" + url.PathEscape(organizationID) + "/workspace/mappings"
	if err := client.request(ctx, http.MethodGet, path, nil, &listed); err != nil {
		return nil, err
	}
	return listed.Mappings, nil
}

func (client *Client) AppOffer(ctx context.Context, organizationID string) (*[]string, error) {
	var policy struct {
		Offer *[]string `json:"offer"`
	}
	path := "/v1/organizations/" + url.PathEscape(organizationID) + "/apps/offer"
	if err := client.request(ctx, http.MethodGet, path, nil, &policy); err != nil {
		return nil, err
	}
	return policy.Offer, nil
}

func (client *Client) AppConnections(ctx context.Context, organizationID, toolkit string) ([]AppConnection, error) {
	var listed struct {
		Connections []AppConnection `json:"connections"`
	}
	path := "/v1/organizations/" + url.PathEscape(organizationID) + "/apps/connections?toolkit=" + url.QueryEscape(toolkit)
	if err := client.request(ctx, http.MethodGet, path, nil, &listed); err != nil {
		return nil, err
	}
	return listed.Connections, nil
}

func (client *Client) StartAppConnection(ctx context.Context, organizationID, toolkit string) (AppConnection, string, error) {
	var started struct {
		Connection  AppConnection `json:"connection"`
		RedirectURL string        `json:"redirect_url"`
	}
	path := "/v1/organizations/" + url.PathEscape(organizationID) + "/apps/connections"
	if err := client.request(ctx, http.MethodPost, path, map[string]string{"toolkit": toolkit}, &started); err != nil {
		return AppConnection{}, "", err
	}
	return started.Connection, started.RedirectURL, nil
}

func (client *Client) AppConnection(ctx context.Context, organizationID, connectionID string) (AppConnection, error) {
	var connection AppConnection
	path := "/v1/organizations/" + url.PathEscape(organizationID) + "/apps/connections/" + url.PathEscape(connectionID)
	if err := client.request(ctx, http.MethodGet, path, nil, &connection); err != nil {
		return AppConnection{}, err
	}
	return connection, nil
}

type Member struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

type Invitation struct {
	ID     string `json:"id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	Status string `json:"status"`
	Guest  bool   `json:"guest"`
}

type Team struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type TeamMember struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

type OrganizationAgent struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
	Status       string `json:"status"`
	CanManage    bool   `json:"can_manage"`
}

type Snapshot struct {
	Organization *Organization
	ActorID      string
	ActorRole    string
	ActorIsAgent bool
	Members      map[string]Member
	Invitations  map[string]Invitation
	Teams        map[string]Team
	TeamMembers  map[string]map[string]TeamMember
	Agent        *OrganizationAgent
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
	var current struct {
		User struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := client.request(ctx, http.MethodGet, "/v1/auth/me", nil, &current); err != nil {
		return Snapshot{}, err
	}
	state.ActorID = current.User.ID
	for _, organization := range organizations.Organizations {
		if organization.Slug == slug {
			state.Organization = &organization
			break
		}
	}
	if state.Organization == nil {
		if current.User.ID != "" && current.User.Email != "" {
			state.Members[strings.ToLower(current.User.Email)] = Member{
				UserID: current.User.ID, Email: strings.ToLower(current.User.Email), Role: "owner",
			}
		}
		return state, nil
	}
	base := "/v1/organizations/" + url.PathEscape(state.Organization.ID)
	var access struct {
		Membership struct {
			UserID string `json:"user_id"`
			Role   string `json:"role"`
		} `json:"membership"`
		Agent bool `json:"agent"`
	}
	if err := client.request(ctx, http.MethodGet, base, nil, &access); err != nil {
		return Snapshot{}, err
	}
	if access.Membership.UserID != state.ActorID {
		return Snapshot{}, fmt.Errorf("auth answered for a different member")
	}
	state.ActorRole, state.ActorIsAgent = access.Membership.Role, access.Agent
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
	var agent OrganizationAgent
	err := client.request(ctx, http.MethodGet, base+"/organization-agent?preview=1", nil, &agent)
	if err == nil {
		state.Agent = &agent
	} else if !IsStatus(err, http.StatusNotFound) && !IsStatus(err, http.StatusServiceUnavailable) {
		return Snapshot{}, err
	}
	return state, nil
}

func (client *Client) UpdateOrganizationAgent(ctx context.Context, orgID string, input map[string]any) error {
	return client.request(ctx, http.MethodPatch, "/v1/organizations/"+url.PathEscape(orgID)+"/organization-agent", input, nil)
}

func (client *Client) Whoami(ctx context.Context, output *json.RawMessage) error {
	return client.request(ctx, http.MethodGet, "/v1/auth/me", nil, output)
}

func (client *Client) CreateOrganization(ctx context.Context, name, slug string, document spec.Document) error {
	input := map[string]any{"name": name, "slug": slug}
	if document.Metadata.Domains != nil {
		input["domains"] = document.Metadata.Domains
	}
	if document.Organization != nil && document.Organization.Timezone != nil {
		input["timezone"] = *document.Organization.Timezone
	}
	if document.People != nil && document.People.Guests != nil {
		input["guests_allowed"] = *document.People.Guests
	}
	return client.request(ctx, http.MethodPost, "/v1/organizations", input, nil)
}

func (client *Client) UpdateOrganization(ctx context.Context, id string, organization Organization, document spec.Document) error {
	name := organization.Name
	if document.Organization != nil && document.Organization.Name != "" {
		name = document.Organization.Name
	}
	input := map[string]any{"name": name, "slug": organization.Slug}
	if document.Metadata.Domains != nil {
		input["domains"] = document.Metadata.Domains
	}
	if document.Organization != nil && document.Organization.Timezone != nil {
		input["timezone"] = *document.Organization.Timezone
	}
	if document.People != nil && document.People.Guests != nil {
		input["guests_allowed"] = *document.People.Guests
	}
	return client.request(ctx, http.MethodPatch, "/v1/organizations/"+url.PathEscape(id), input, nil)
}

func (client *Client) Invite(ctx context.Context, orgID, email, role string, quiet, guest bool) error {
	return client.request(ctx, http.MethodPost, "/v1/organizations/"+url.PathEscape(orgID)+"/invitations", map[string]any{"email": email, "role": role, "quiet": quiet, "guest": guest}, nil)
}

func (client *Client) ChangeMemberRole(ctx context.Context, orgID, memberID, role string) error {
	return client.request(ctx, http.MethodPatch, "/v1/organizations/"+url.PathEscape(orgID)+"/members/"+url.PathEscape(memberID), map[string]string{"role": role}, nil)
}

func (client *Client) RemoveMember(ctx context.Context, orgID, memberID string) error {
	return client.request(ctx, http.MethodDelete, "/v1/organizations/"+url.PathEscape(orgID)+"/members/"+url.PathEscape(memberID), nil, nil)
}

func (client *Client) RevokeInvitation(ctx context.Context, orgID, invitationID string) error {
	return client.request(ctx, http.MethodDelete, "/v1/organizations/"+url.PathEscape(orgID)+"/invitations/"+url.PathEscape(invitationID), nil, nil)
}

func (client *Client) CreateTeam(ctx context.Context, orgID, name string, skipCreator bool) error {
	return client.request(ctx, http.MethodPost, "/v1/organizations/"+url.PathEscape(orgID)+"/teams", map[string]any{"name": name, "skip_creator": skipCreator}, nil)
}

func (client *Client) AddTeamMember(ctx context.Context, orgID, teamID, memberID, role string) error {
	return client.request(ctx, http.MethodPost, "/v1/organizations/"+url.PathEscape(orgID)+"/teams/"+url.PathEscape(teamID)+"/members", map[string]string{"user_id": memberID, "role": role}, nil)
}

func (client *Client) ChangeTeamMemberRole(ctx context.Context, orgID, teamID, memberID, role string) error {
	return client.request(ctx, http.MethodPatch, "/v1/organizations/"+url.PathEscape(orgID)+"/teams/"+url.PathEscape(teamID)+"/members/"+url.PathEscape(memberID), map[string]string{"role": role}, nil)
}

func (client *Client) RemoveTeamMember(ctx context.Context, orgID, teamID, memberID string) error {
	return client.request(ctx, http.MethodDelete, "/v1/organizations/"+url.PathEscape(orgID)+"/teams/"+url.PathEscape(teamID)+"/members/"+url.PathEscape(memberID), nil, nil)
}

func (client *Client) DeleteTeam(ctx context.Context, orgID, teamID string) error {
	return client.request(ctx, http.MethodDelete, "/v1/organizations/"+url.PathEscape(orgID)+"/teams/"+url.PathEscape(teamID), nil, nil)
}
