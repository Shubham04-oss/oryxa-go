package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

var (
	linearGetIssueSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"issue": {"type": "string", "description": "The issue's UUID or its identifier, such as ENG-42."}
		},
		"required": ["issue"],
		"additionalProperties": false
	}`)

	linearListIssuesSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"team": {"type": "string", "description": "The team to list issues from, by key such as ENG or by ID."},
			"state": {"type": "string", "description": "Only issues in this workflow state type: triage, backlog, unstarted, started, completed or canceled. Defaults to every state."},
			"limit": {"type": "integer", "description": "Most issues to return, from 1 to 50. Defaults to 20."}
		},
		"required": ["team"],
		"additionalProperties": false
	}`)

	linearCreateIssueSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"team": {"type": "string", "description": "The team to create the issue in, by key such as ENG or by ID."},
			"title": {"type": "string", "description": "The issue's title."},
			"description": {"type": "string", "description": "The issue's description, in markdown."},
			"priority": {"type": "integer", "description": "The issue's priority: 0 none, 1 urgent, 2 high, 3 normal, 4 low. Defaults to none."}
		},
		"required": ["team", "title"],
		"additionalProperties": false
	}`)

	linearCommentOnIssueSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"issue": {"type": "string", "description": "The issue's UUID or its identifier, such as ENG-42."},
			"body": {"type": "string", "description": "The comment's body, in markdown."}
		},
		"required": ["issue", "body"],
		"additionalProperties": false
	}`)

	linearCloseIssueSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"issue": {"type": "string", "description": "The issue's UUID or its identifier, such as ENG-42."}
		},
		"required": ["issue"],
		"additionalProperties": false
	}`)
)

// linearActions are what a caller may do with a Linear connection: read and
// change issues. Every call goes through linearGraphQL, so Linear's error
// extensions map to the same domain errors linear.go's webhook registration
// already maps them to.
func linearActions(graphqlURL string) []Action {
	return []Action{
		{
			Name:    "get_issue",
			Summary: "Answers one Linear issue's identifier, title, description, state, priority, url and assignee.",
			Input:   linearGetIssueSchema,
			Writes:  false,
			Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
				return linearGetIssue(ctx, client, graphqlURL, request)
			},
		},
		{
			Name:    "list_issues",
			Summary: "Lists a Linear team's issues.",
			Input:   linearListIssuesSchema,
			Writes:  false,
			Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
				return linearListIssues(ctx, client, graphqlURL, request)
			},
		},
		{
			Name:    "create_issue",
			Summary: "Creates a Linear issue.",
			Input:   linearCreateIssueSchema,
			Writes:  true,
			Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
				return linearCreateIssue(ctx, client, graphqlURL, request)
			},
		},
		{
			Name:    "comment_on_issue",
			Summary: "Comments on a Linear issue.",
			Input:   linearCommentOnIssueSchema,
			Writes:  true,
			Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
				return linearCommentOnIssue(ctx, client, graphqlURL, request)
			},
		},
		{
			Name:    "close_issue",
			Summary: "Closes a Linear issue by moving it to its team's completed workflow state.",
			Input:   linearCloseIssueSchema,
			Writes:  true,
			Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
				return linearCloseIssue(ctx, client, graphqlURL, request)
			},
		},
	}
}

// linearIssueID is the UUID of an issue an input names by UUID or by its
// identifier, such as ENG-42. Linear's issue(id:) query resolves either, the
// same way linearTeamID resolves a team, but a mutation's own id-shaped input
// fields (issueId in CommentCreateInput, for one) are not assumed to share
// that, so this resolves to a UUID first wherever an action goes on to write
// with it.
func linearIssueID(ctx context.Context, client *http.Client, graphqlURL string, credential domain.Credential, issue string) (string, error) {
	if linearIDPattern.MatchString(issue) {
		return issue, nil
	}
	var found struct {
		Issue struct {
			ID string `json:"id"`
		} `json:"issue"`
	}
	if err := linearGraphQL(ctx, client, graphqlURL, credential,
		`query IssueByIdentifier($id: String!) { issue(id: $id) { id } }`,
		map[string]any{"id": issue}, &found); err != nil {
		return "", err
	}
	if found.Issue.ID == "" {
		return "", fmt.Errorf("Linear has no issue %q the connection can see: %w", issue, domain.ErrNotFound)
	}
	return found.Issue.ID, nil
}

// --- get_issue ---

type linearGetIssueInput struct {
	Issue string `json:"issue"`
}

// linearIssueDetail is get_issue's answer.
type linearIssueDetail struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	State       string `json:"state"`
	Priority    int    `json:"priority"`
	URL         string `json:"url"`
	Assignee    string `json:"assignee,omitempty"`
}

func linearGetIssue(ctx context.Context, client *http.Client, graphqlURL string, request ActionRequest) (ActionResult, error) {
	var input linearGetIssueInput
	if err := DecodeInput(request.Input, &input); err != nil {
		return ActionResult{}, err
	}
	if err := Required("issue", input.Issue); err != nil {
		return ActionResult{}, err
	}
	// issue(id:) accepts the UUID or the identifier, so this needs no
	// resolution step of its own: one call answers everything get_issue does.
	var answer struct {
		Issue struct {
			ID          string `json:"id"`
			Identifier  string `json:"identifier"`
			Title       string `json:"title"`
			Description string `json:"description"`
			State       struct {
				Name string `json:"name"`
			} `json:"state"`
			Priority float64 `json:"priority"`
			URL      string  `json:"url"`
			Assignee struct {
				Name string `json:"name"`
			} `json:"assignee"`
		} `json:"issue"`
	}
	if err := linearGraphQL(ctx, client, graphqlURL, request.Credential,
		`query GetIssue($id: String!) { issue(id: $id) { id identifier title description state { name } priority url assignee { name } } }`,
		map[string]any{"id": input.Issue}, &answer); err != nil {
		return ActionResult{}, err
	}
	issue := answer.Issue
	if issue.ID == "" {
		return ActionResult{}, fmt.Errorf("Linear has no issue %q the connection can see: %w", input.Issue, domain.ErrNotFound)
	}
	return ActionResult{
		Resource: integrationsv1.Resource{Type: "issue", ID: issue.ID, Name: issue.Title, URL: issue.URL},
		Result: Answered(linearIssueDetail{
			ID: issue.ID, Identifier: issue.Identifier, Title: issue.Title, Description: issue.Description,
			State: issue.State.Name, Priority: int(issue.Priority), URL: issue.URL, Assignee: issue.Assignee.Name,
		}),
	}, nil
}

// --- list_issues ---

type linearListIssuesInput struct {
	Team  string `json:"team"`
	State string `json:"state"`
	Limit int    `json:"limit"`
}

// linearIssueSummary is one entry of list_issues' answer.
type linearIssueSummary struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	State      string `json:"state"`
	URL        string `json:"url"`
}

func linearListIssues(ctx context.Context, client *http.Client, graphqlURL string, request ActionRequest) (ActionResult, error) {
	var input linearListIssuesInput
	if err := DecodeInput(request.Input, &input); err != nil {
		return ActionResult{}, err
	}
	if err := Required("team", input.Team); err != nil {
		return ActionResult{}, err
	}
	switch input.State {
	case "", "triage", "backlog", "unstarted", "started", "completed", "canceled":
	default:
		return ActionResult{}, fmt.Errorf("%w: state must be \"triage\", \"backlog\", \"unstarted\", \"started\", \"completed\" or \"canceled\"", domain.ErrInvalid)
	}
	limit := input.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 {
		return ActionResult{}, fmt.Errorf("%w: limit must be 1 to 50", domain.ErrInvalid)
	}
	teamID, err := linearTeamID(ctx, client, graphqlURL, request.Credential, input.Team)
	if err != nil {
		return ActionResult{}, err
	}
	filter := map[string]any{"team": map[string]any{"id": map[string]any{"eq": teamID}}}
	if input.State != "" {
		filter["state"] = map[string]any{"type": map[string]any{"eq": input.State}}
	}
	var answer struct {
		Issues struct {
			Nodes []struct {
				ID         string `json:"id"`
				Identifier string `json:"identifier"`
				Title      string `json:"title"`
				State      struct {
					Name string `json:"name"`
				} `json:"state"`
				URL string `json:"url"`
			} `json:"nodes"`
		} `json:"issues"`
	}
	if err := linearGraphQL(ctx, client, graphqlURL, request.Credential,
		`query ListIssues($filter: IssueFilter!, $first: Int!) { issues(filter: $filter, first: $first) { nodes { id identifier title state { name } url } } }`,
		map[string]any{"filter": filter, "first": limit}, &answer); err != nil {
		return ActionResult{}, err
	}
	summaries := make([]linearIssueSummary, 0, len(answer.Issues.Nodes))
	for _, issue := range answer.Issues.Nodes {
		summaries = append(summaries, linearIssueSummary{
			ID: issue.ID, Identifier: issue.Identifier, Title: issue.Title, State: issue.State.Name, URL: issue.URL,
		})
	}
	// Read-only: this names no single resource the action changed.
	return ActionResult{Result: Answered(summaries)}, nil
}

// --- create_issue ---

type linearCreateIssueInput struct {
	Team        string `json:"team"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    *int   `json:"priority"`
}

// linearIssueCreated is create_issue's answer.
type linearIssueCreated struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	URL        string `json:"url"`
}

func linearCreateIssue(ctx context.Context, client *http.Client, graphqlURL string, request ActionRequest) (ActionResult, error) {
	var input linearCreateIssueInput
	if err := DecodeInput(request.Input, &input); err != nil {
		return ActionResult{}, err
	}
	if err := Required("team", input.Team); err != nil {
		return ActionResult{}, err
	}
	if err := Required("title", input.Title); err != nil {
		return ActionResult{}, err
	}
	if input.Priority != nil && (*input.Priority < 0 || *input.Priority > 4) {
		return ActionResult{}, fmt.Errorf("%w: priority must be 0 to 4", domain.ErrInvalid)
	}
	teamID, err := linearTeamID(ctx, client, graphqlURL, request.Credential, input.Team)
	if err != nil {
		return ActionResult{}, err
	}
	issueInput := map[string]any{"teamId": teamID, "title": input.Title}
	if input.Description != "" {
		issueInput["description"] = input.Description
	}
	if input.Priority != nil {
		issueInput["priority"] = *input.Priority
	}
	var created struct {
		IssueCreate struct {
			Success bool `json:"success"`
			Issue   struct {
				ID         string `json:"id"`
				Identifier string `json:"identifier"`
				Title      string `json:"title"`
				URL        string `json:"url"`
			} `json:"issue"`
		} `json:"issueCreate"`
	}
	if err := linearGraphQL(ctx, client, graphqlURL, request.Credential,
		`mutation CreateIssue($input: IssueCreateInput!) { issueCreate(input: $input) { success issue { id identifier title url } } }`,
		map[string]any{"input": issueInput}, &created); err != nil {
		return ActionResult{}, err
	}
	if !created.IssueCreate.Success || created.IssueCreate.Issue.ID == "" {
		return ActionResult{}, fmt.Errorf("%w: Linear did not create the issue", domain.ErrInvalid)
	}
	issue := created.IssueCreate.Issue
	return ActionResult{
		Resource: integrationsv1.Resource{Type: "issue", ID: issue.ID, Name: issue.Title, URL: issue.URL},
		Result:   Answered(linearIssueCreated{ID: issue.ID, Identifier: issue.Identifier, Title: issue.Title, URL: issue.URL}),
	}, nil
}

// --- comment_on_issue ---

type linearCommentOnIssueInput struct {
	Issue string `json:"issue"`
	Body  string `json:"body"`
}

// linearCommentCreated is comment_on_issue's answer.
type linearCommentCreated struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

func linearCommentOnIssue(ctx context.Context, client *http.Client, graphqlURL string, request ActionRequest) (ActionResult, error) {
	var input linearCommentOnIssueInput
	if err := DecodeInput(request.Input, &input); err != nil {
		return ActionResult{}, err
	}
	if err := Required("issue", input.Issue); err != nil {
		return ActionResult{}, err
	}
	if err := Required("body", input.Body); err != nil {
		return ActionResult{}, err
	}
	// CommentCreateInput's issueId is not assumed to accept an identifier the
	// way issue(id:) does, so this resolves to the UUID first.
	issueID, err := linearIssueID(ctx, client, graphqlURL, request.Credential, input.Issue)
	if err != nil {
		return ActionResult{}, err
	}
	var created struct {
		CommentCreate struct {
			Success bool `json:"success"`
			Comment struct {
				ID  string `json:"id"`
				URL string `json:"url"`
			} `json:"comment"`
		} `json:"commentCreate"`
	}
	if err := linearGraphQL(ctx, client, graphqlURL, request.Credential,
		`mutation CreateComment($input: CommentCreateInput!) { commentCreate(input: $input) { success comment { id url } } }`,
		map[string]any{"input": map[string]any{"issueId": issueID, "body": input.Body}}, &created); err != nil {
		return ActionResult{}, err
	}
	if !created.CommentCreate.Success || created.CommentCreate.Comment.ID == "" {
		return ActionResult{}, fmt.Errorf("%w: Linear did not create the comment", domain.ErrInvalid)
	}
	comment := created.CommentCreate.Comment
	return ActionResult{
		Resource: integrationsv1.Resource{Type: "comment", ID: comment.ID, URL: comment.URL},
		Result:   Answered(linearCommentCreated{ID: comment.ID, URL: comment.URL}),
	}, nil
}

// --- close_issue ---

type linearCloseIssueInput struct {
	Issue string `json:"issue"`
}

// linearIssueClosed is close_issue's answer.
type linearIssueClosed struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	State      string `json:"state"`
	URL        string `json:"url"`
}

func linearCloseIssue(ctx context.Context, client *http.Client, graphqlURL string, request ActionRequest) (ActionResult, error) {
	var input linearCloseIssueInput
	if err := DecodeInput(request.Input, &input); err != nil {
		return ActionResult{}, err
	}
	if err := Required("issue", input.Issue); err != nil {
		return ActionResult{}, err
	}
	// One call, by UUID or identifier, for the issue's own identity and its
	// team's first completed-type state together.
	var found struct {
		Issue struct {
			ID         string `json:"id"`
			Identifier string `json:"identifier"`
			Title      string `json:"title"`
			URL        string `json:"url"`
			Team       struct {
				States struct {
					Nodes []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"nodes"`
				} `json:"states"`
			} `json:"team"`
		} `json:"issue"`
	}
	if err := linearGraphQL(ctx, client, graphqlURL, request.Credential,
		`query IssueForClose($id: String!) { issue(id: $id) { id identifier title url team { states(filter: { type: { eq: "completed" } }, first: 1) { nodes { id name } } } } }`,
		map[string]any{"id": input.Issue}, &found); err != nil {
		return ActionResult{}, err
	}
	if found.Issue.ID == "" {
		return ActionResult{}, fmt.Errorf("Linear has no issue %q the connection can see: %w", input.Issue, domain.ErrNotFound)
	}
	if len(found.Issue.Team.States.Nodes) == 0 {
		return ActionResult{}, fmt.Errorf("%s's team has no workflow state of type \"completed\" to close it into: %w", found.Issue.Identifier, domain.ErrInvalid)
	}
	completedStateID := found.Issue.Team.States.Nodes[0].ID
	var updated struct {
		IssueUpdate struct {
			Success bool `json:"success"`
			Issue   struct {
				ID         string `json:"id"`
				Identifier string `json:"identifier"`
				Title      string `json:"title"`
				State      struct {
					Name string `json:"name"`
				} `json:"state"`
				URL string `json:"url"`
			} `json:"issue"`
		} `json:"issueUpdate"`
	}
	if err := linearGraphQL(ctx, client, graphqlURL, request.Credential,
		`mutation CloseIssue($id: String!, $input: IssueUpdateInput!) { issueUpdate(id: $id, input: $input) { success issue { id identifier title state { name } url } } }`,
		map[string]any{"id": found.Issue.ID, "input": map[string]any{"stateId": completedStateID}}, &updated); err != nil {
		return ActionResult{}, err
	}
	if !updated.IssueUpdate.Success || updated.IssueUpdate.Issue.ID == "" {
		return ActionResult{}, fmt.Errorf("%w: Linear did not close the issue", domain.ErrInvalid)
	}
	issue := updated.IssueUpdate.Issue
	return ActionResult{
		Resource: integrationsv1.Resource{Type: "issue", ID: issue.ID, Name: issue.Title, URL: issue.URL},
		Result:   Answered(linearIssueClosed{ID: issue.ID, Identifier: issue.Identifier, State: issue.State.Name, URL: issue.URL}),
	}, nil
}
