package agents

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/runtime"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

//go:embed toolboxes/directory.md
var directoryInstructions string

type DirectoryOrganization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type DirectoryTeam struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id,omitempty"`
	Name           string `json:"name"`
	Path           string `json:"path,omitempty"`
	Role           string `json:"role,omitempty"`
	CanManage      bool   `json:"can_manage"`
}

type DirectoryTeamRef struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Path       string `json:"path,omitempty"`
	MemberPath string `json:"member_path,omitempty"`
	Role       string `json:"role"`
}

type DirectoryAgent struct {
	ID               string `json:"id"`
	OwnerUserID      string `json:"owner_user_id"`
	OwnerEmail       string `json:"owner_email,omitempty"`
	OwnerDisplayName string `json:"owner_display_name,omitempty"`
	Kind             string `json:"kind"`
	Name             string `json:"name"`
	Description      string `json:"description,omitempty"`
}

type DirectoryPerson struct {
	UserID      string             `json:"user_id"`
	Email       string             `json:"email"`
	DisplayName string             `json:"display_name,omitempty"`
	OrgRole     string             `json:"org_role"`
	Path        string             `json:"path,omitempty"`
	Teams       []DirectoryTeamRef `json:"teams"`
	Agents      []DirectoryAgent   `json:"agents"`
}

type OrganizationDirectory struct {
	Organization DirectoryOrganization `json:"organization"`
	Teams        []DirectoryTeam       `json:"teams"`
	People       []DirectoryPerson     `json:"people"`
}

// Directory is the caller-visible organization directory. directoryclient.Client
// implements it by joining Teams' visibility-filtered tree with safe agent handles.
type Directory interface {
	Load(context.Context, string, string) (OrganizationDirectory, error)
	FindPeople(context.Context, string, string, string, int) ([]DirectoryPerson, error)
	FindTeams(context.Context, string, string, string, int) ([]DirectoryTeam, error)
	FindAgents(context.Context, string, string, string, int) ([]DirectoryAgent, error)
}

type DirectoryContext struct {
	Ownership []spec.OwnerHint `json:"ownership"`
	OrgChart  spec.OrgChart    `json:"orgChart"`
}

type ContextDirectory interface {
	Load(context.Context, string, string) (DirectoryContext, error)
}

type DirectoryConfig struct {
	Directory Directory
	Context   ContextDirectory
	Tokens    memberauth.TokenSource
}

// DirectoryToolbox gives primary and specialist agents the same read-only,
// member-scoped view of people, teams and active agents.
func DirectoryToolbox(config DirectoryConfig) (Toolbox, error) {
	if config.Directory == nil {
		return Toolbox{}, errors.New("agents: DirectoryToolbox requires a directory")
	}
	tools := directoryTools{directory: config.Directory, context: config.Context, tokens: config.Tokens}
	load, err := functiontool.New(functiontool.Config{
		Name: "directory_load", Description: "Loads the caller-visible organization directory: people, team tree, and each person's active primary and specialist agents.",
	}, tools.load)
	if err != nil {
		return Toolbox{}, err
	}
	people, err := functiontool.New(functiontool.Config{
		Name: "directory_find_people", Description: "Finds people in the organization by name, email, or user id and returns their visible teams and active agents.",
	}, tools.people)
	if err != nil {
		return Toolbox{}, err
	}
	teams, err := functiontool.New(functiontool.Config{
		Name: "directory_find_teams", Description: "Finds teams visible to the member by semantic name matching and returns their ids and organization paths.",
	}, tools.teams)
	if err != nil {
		return Toolbox{}, err
	}
	agentsTool, err := functiontool.New(functiontool.Config{
		Name: "directory_find_agents", Description: "Finds active primary and specialist agents in the organization by agent name, purpose, or their owner's identity.",
	}, tools.agents)
	if err != nil {
		return Toolbox{}, err
	}
	available := []tool.Tool{load, people, teams, agentsTool}
	if config.Context != nil {
		owners, err := functiontool.New(functiontool.Config{
			Name: "directory_find_owners", Description: "Finds joined members explicitly mapped as owners of a topic, system, channel, or tag in the workspace file.",
		}, tools.owners)
		if err != nil {
			return Toolbox{}, err
		}
		above, err := functiontool.New(functiontool.Config{
			Name: "directory_is_above", Description: "Checks whether one joined member is above another in the configured organization chart.",
		}, tools.above)
		if err != nil {
			return Toolbox{}, err
		}
		available = append(available, owners, above)
	}
	return Toolbox{
		Name: "directory", Description: "The member's organization: people, the visible team tree, and active member-owned agents, with semantic lookup by identity or purpose.",
		Instructions: directoryInstructions,
		Toolset:      fixedTools{name: "directory", tools: available},
	}, nil
}

type directoryTools struct {
	directory Directory
	context   ContextDirectory
	tokens    memberauth.TokenSource
}

type directoryRelationQuery struct {
	From string `json:"from" jsonschema:"Email of the person who might be above."`
	To   string `json:"to" jsonschema:"Email of the person who might be below."`
}

type directoryAboveResult struct {
	Above bool `json:"above"`
}

type directoryOwnersResult struct {
	Ownership []spec.OwnerHint `json:"ownership"`
}

type directoryQuery struct {
	Query string `json:"query" jsonschema:"A person's name/email/id, team name, or agent name/purpose."`
	Limit int    `json:"limit,omitempty" jsonschema:"Maximum results; 20 by default and at most 100."`
}

type directoryPeopleResult struct {
	People []DirectoryPerson `json:"people"`
}

type directoryTeamsResult struct {
	Teams []DirectoryTeam `json:"teams"`
}

type directoryAgentsResult struct {
	Agents []DirectoryAgent `json:"agents"`
}

func (tools directoryTools) auth(ctx agent.Context) (memberauth.Member, string, error) {
	member, token, err := memberauth.Bearer(ctx, tools.tokens)
	if err != nil {
		return memberauth.Member{}, "", directoryError(err)
	}
	return member, token, nil
}

func (tools directoryTools) load(ctx agent.Context, _ struct{}) (OrganizationDirectory, error) {
	member, token, err := tools.auth(ctx)
	if err != nil {
		return OrganizationDirectory{}, err
	}
	result, err := tools.directory.Load(ctx, member.OrganizationID, token)
	if err != nil {
		return OrganizationDirectory{}, directoryError(err)
	}
	return result, nil
}

func (tools directoryTools) people(ctx agent.Context, args directoryQuery) (directoryPeopleResult, error) {
	member, token, err := tools.auth(ctx)
	if err != nil {
		return directoryPeopleResult{}, err
	}
	result, err := tools.directory.FindPeople(ctx, member.OrganizationID, token, args.Query, args.Limit)
	if err != nil {
		return directoryPeopleResult{}, directoryError(err)
	}
	return directoryPeopleResult{People: result}, nil
}

func (tools directoryTools) teams(ctx agent.Context, args directoryQuery) (directoryTeamsResult, error) {
	member, token, err := tools.auth(ctx)
	if err != nil {
		return directoryTeamsResult{}, err
	}
	result, err := tools.directory.FindTeams(ctx, member.OrganizationID, token, args.Query, args.Limit)
	if err != nil {
		return directoryTeamsResult{}, directoryError(err)
	}
	return directoryTeamsResult{Teams: result}, nil
}

func (tools directoryTools) agents(ctx agent.Context, args directoryQuery) (directoryAgentsResult, error) {
	member, token, err := tools.auth(ctx)
	if err != nil {
		return directoryAgentsResult{}, err
	}
	result, err := tools.directory.FindAgents(ctx, member.OrganizationID, token, args.Query, args.Limit)
	if err != nil {
		return directoryAgentsResult{}, directoryError(err)
	}
	return directoryAgentsResult{Agents: result}, nil
}

func (tools directoryTools) owners(ctx agent.Context, args directoryQuery) (directoryOwnersResult, error) {
	member, token, err := tools.auth(ctx)
	if err != nil {
		return directoryOwnersResult{}, err
	}
	query := strings.ToLower(strings.TrimSpace(args.Query))
	if query == "" {
		return directoryOwnersResult{}, fmt.Errorf("an ownership query is required")
	}
	result, err := tools.context.Load(ctx, member.OrganizationID, token)
	if err != nil {
		return directoryOwnersResult{}, directoryError(err)
	}
	found := make([]spec.OwnerHint, 0)
	for _, hint := range result.Ownership {
		parts := append([]string{hint.What, hint.System, hint.Channel}, hint.Tags...)
		for _, part := range parts {
			if strings.Contains(strings.ToLower(part), query) {
				found = append(found, hint)
				break
			}
		}
		if len(found) >= 20 {
			break
		}
	}
	return directoryOwnersResult{Ownership: found}, nil
}

func (tools directoryTools) above(ctx agent.Context, args directoryRelationQuery) (directoryAboveResult, error) {
	member, token, err := tools.auth(ctx)
	if err != nil {
		return directoryAboveResult{}, err
	}
	result, err := tools.context.Load(ctx, member.OrganizationID, token)
	if err != nil {
		return directoryAboveResult{}, directoryError(err)
	}
	return directoryAboveResult{Above: runtime.Above(result.OrgChart, strings.ToLower(strings.TrimSpace(args.From)), strings.ToLower(strings.TrimSpace(args.To)))}, nil
}

func directoryError(err error) error {
	if known := memberauth.Explain(err); known != nil {
		return known
	}
	for _, known := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, known) {
			return known
		}
	}
	return fmt.Errorf("the organization directory could not be read: %w", err)
}
