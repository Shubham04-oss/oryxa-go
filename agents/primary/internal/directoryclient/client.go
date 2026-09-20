// Package directoryclient joins Teams' caller-visible organization tree with
// the active agent handles owned by the people in that tree.
package directoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
)

const maxResponseBytes = 8 << 20

var (
	ErrDenied      = errors.New("the organization directory is not available to this member")
	ErrUnavailable = errors.New("the organization directory did not answer")
	ErrInvalid     = errors.New("a directory search query is required")
)

type AgentIndex interface {
	OrganizationAgents(context.Context, string) ([]managedagents.Handle, error)
}

type Client struct {
	baseURL string
	http    *http.Client
	agents  AgentIndex
}

func New(baseURL string, httpClient *http.Client, agentIndex AgentIndex) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("directory client requires an absolute Teams URL")
	}
	if agentIndex == nil {
		return nil, errors.New("directory client requires an agent index")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: baseURL, http: httpClient, agents: agentIndex}, nil
}

type treeResponse struct {
	Organization agents.DirectoryOrganization `json:"organization"`
	Teams        []agents.DirectoryTeam       `json:"teams"`
	Members      []agents.DirectoryPerson     `json:"members"`
}

func (client *Client) Load(ctx context.Context, organizationID, token string) (agents.OrganizationDirectory, error) {
	var response treeResponse
	if err := client.get(ctx, organizationID, token, "/directory", nil, &response); err != nil {
		return agents.OrganizationDirectory{}, err
	}
	people, err := client.attachAgents(ctx, organizationID, response.Members)
	if err != nil {
		return agents.OrganizationDirectory{}, err
	}
	if response.Teams == nil {
		response.Teams = []agents.DirectoryTeam{}
	}
	return agents.OrganizationDirectory{Organization: response.Organization, Teams: response.Teams, People: people}, nil
}

func (client *Client) FindPeople(ctx context.Context, organizationID, token, query string, limit int) ([]agents.DirectoryPerson, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrInvalid
	}
	var response struct {
		Members []agents.DirectoryPerson `json:"members"`
	}
	params := url.Values{"q": {query}, "limit": {fmt.Sprint(clamp(limit))}}
	if err := client.get(ctx, organizationID, token, "/members/search", params, &response); err != nil {
		return nil, err
	}
	return client.attachAgents(ctx, organizationID, response.Members)
}

func (client *Client) FindTeams(ctx context.Context, organizationID, token, query string, limit int) ([]agents.DirectoryTeam, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrInvalid
	}
	var response struct {
		Teams []agents.DirectoryTeam `json:"teams"`
	}
	params := url.Values{"q": {query}, "limit": {fmt.Sprint(clamp(limit))}}
	if err := client.get(ctx, organizationID, token, "/teams/search", params, &response); err != nil {
		return nil, err
	}
	if response.Teams == nil {
		response.Teams = []agents.DirectoryTeam{}
	}
	return response.Teams, nil
}

func (client *Client) FindAgents(ctx context.Context, organizationID, token, query string, limit int) ([]agents.DirectoryAgent, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrInvalid
	}
	directory, err := client.Load(ctx, organizationID, token)
	if err != nil {
		return nil, err
	}
	type ranked struct {
		agent agents.DirectoryAgent
		score float64
	}
	var found []ranked
	for _, person := range directory.People {
		for _, handle := range person.Agents {
			score := max(similarity(query, handle.Name), similarity(query, handle.Description), similarity(query, person.DisplayName), similarity(query, person.Email), similarity(query, person.UserID))
			if score >= 0.3 {
				found = append(found, ranked{agent: handle, score: score})
			}
		}
	}
	slices.SortFunc(found, func(a, b ranked) int {
		if a.score != b.score {
			if a.score > b.score {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.agent.Name), strings.ToLower(b.agent.Name))
	})
	limit = clamp(limit)
	if len(found) > limit {
		found = found[:limit]
	}
	result := make([]agents.DirectoryAgent, 0, len(found))
	for _, item := range found {
		result = append(result, item.agent)
	}
	return result, nil
}

func (client *Client) attachAgents(ctx context.Context, organizationID string, people []agents.DirectoryPerson) ([]agents.DirectoryPerson, error) {
	handles, err := client.agents.OrganizationAgents(ctx, organizationID)
	if err != nil {
		return nil, fmt.Errorf("read organization agents: %w", err)
	}
	peopleByID := make(map[string]agents.DirectoryPerson, len(people))
	for _, person := range people {
		peopleByID[person.UserID] = person
	}
	byOwner := map[string][]agents.DirectoryAgent{}
	for _, handle := range handles {
		owner, visible := peopleByID[handle.OwnerUserID]
		if !visible {
			continue
		}
		byOwner[handle.OwnerUserID] = append(byOwner[handle.OwnerUserID], agents.DirectoryAgent{
			ID: handle.ID, OwnerUserID: handle.OwnerUserID, OwnerEmail: owner.Email, OwnerDisplayName: owner.DisplayName,
			Kind: string(handle.Kind), Name: handle.Name, Description: handle.Description,
		})
	}
	if people == nil {
		people = []agents.DirectoryPerson{}
	}
	for index := range people {
		owned := byOwner[people[index].UserID]
		if owned == nil {
			owned = []agents.DirectoryAgent{}
		}
		people[index].Agents = owned
		if people[index].Teams == nil {
			people[index].Teams = []agents.DirectoryTeamRef{}
		}
	}
	return people, nil
}

func (client *Client) get(ctx context.Context, organizationID, token, suffix string, params url.Values, target any) error {
	if strings.TrimSpace(organizationID) == "" || strings.TrimSpace(token) == "" {
		return ErrDenied
	}
	endpoint := client.baseURL + "/v1/organizations/" + url.PathEscape(organizationID) + suffix
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build directory request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Oryxa-Organization", organizationID)
	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusOK:
		if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(target); err != nil {
			return fmt.Errorf("decode directory response: %w", err)
		}
		return nil
	case response.StatusCode == http.StatusUnauthorized, response.StatusCode == http.StatusForbidden, response.StatusCode == http.StatusNotFound:
		return ErrDenied
	case response.StatusCode >= http.StatusInternalServerError:
		return fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	default:
		return fmt.Errorf("directory request failed: status %d", response.StatusCode)
	}
}

func clamp(limit int) int {
	if limit <= 0 {
		return 20
	}
	return min(limit, 100)
}

func similarity(query, value string) float64 {
	query, value = strings.ToLower(strings.TrimSpace(query)), strings.ToLower(strings.TrimSpace(value))
	if query == "" || value == "" {
		return 0
	}
	if strings.Contains(value, query) || strings.Contains(query, value) {
		return 1
	}
	a, b := trigrams(query), trigrams(value)
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	overlap := 0
	for gram := range a {
		if _, ok := b[gram]; ok {
			overlap++
		}
	}
	return 2 * float64(overlap) / float64(len(a)+len(b))
}

func trigrams(value string) map[string]struct{} {
	runes := append([]rune{' ', ' '}, []rune(value)...)
	runes = append(runes, ' ')
	grams := make(map[string]struct{}, len(runes))
	for index := 0; index+2 < len(runes); index++ {
		grams[string(runes[index:index+3])] = struct{}{}
	}
	return grams
}

var _ agents.Directory = (*Client)(nil)
