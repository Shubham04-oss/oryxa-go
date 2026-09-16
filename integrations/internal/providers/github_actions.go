package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/issues"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

var (
	githubGetIssueSchema = json.RawMessage(`{"type":"object","properties":{"repository":{"type":"string","description":"The repository to look in, as owner/name."},"number":{"type":"integer","description":"The issue number to fetch."}},"required":["repository","number"],"additionalProperties":false}`)

	githubListIssuesSchema = json.RawMessage(`{"type":"object","properties":{"repository":{"type":"string","description":"The repository to list issues from, as owner/name."},"state":{"type":"string","description":"Which issues to return: open, closed or all; defaults to open."},"labels":{"type":"string","description":"A comma separated list of label names to filter by."},"limit":{"type":"integer","description":"The most issues to return, 1 to 50; defaults to 20."}},"required":["repository"],"additionalProperties":false}`)

	githubCreateIssueSchema = json.RawMessage(`{"type":"object","properties":{"repository":{"type":"string","description":"The repository to create the issue in, as owner/name."},"title":{"type":"string","description":"The issue's title."},"body":{"type":"string","description":"The issue's body text."},"labels":{"type":"array","items":{"type":"string"},"description":"Label names to apply to the issue."},"assignees":{"type":"array","items":{"type":"string"},"description":"Logins to assign to the issue."}},"required":["repository","title"],"additionalProperties":false}`)

	githubCommentOnIssueSchema = json.RawMessage(`{"type":"object","properties":{"repository":{"type":"string","description":"The repository the issue is in, as owner/name."},"number":{"type":"integer","description":"The issue or pull request number to comment on."},"body":{"type":"string","description":"The comment's body text."}},"required":["repository","number","body"],"additionalProperties":false}`)

	githubCloseIssueSchema = json.RawMessage(`{"type":"object","properties":{"repository":{"type":"string","description":"The repository the issue is in, as owner/name."},"number":{"type":"integer","description":"The issue number to close."},"reason":{"type":"string","description":"Why the issue is closing: completed or not_planned."}},"required":["repository","number"],"additionalProperties":false}`)
)

// githubActions are what a caller may do with a GitHub connection: read and
// change issues, which on GitHub are also how pull requests are discussed and
// closed.
func githubActions(apiURL string) []Action {
	return []Action{
		{
			Name:    "get_issue",
			Summary: "Answers one GitHub issue's number, title, state, body, url, author, labels and comment count.",
			Input:   githubGetIssueSchema,
			Writes:  false,
			Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
				return githubGetIssue(ctx, client, apiURL, request)
			},
		},
		{
			Name:    "list_issues",
			Summary: "Lists a GitHub repository's issues.",
			Input:   githubListIssuesSchema,
			Writes:  false,
			Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
				return githubListIssues(ctx, client, apiURL, request)
			},
		},
		{
			Name:    "create_issue",
			Summary: "Creates a GitHub issue.",
			Input:   githubCreateIssueSchema,
			Writes:  true,
			Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
				return githubCreateIssue(ctx, client, apiURL, request)
			},
		},
		{
			Name:    "comment_on_issue",
			Summary: "Comments on a GitHub issue or pull request, since a pull request is an issue.",
			Input:   githubCommentOnIssueSchema,
			Writes:  true,
			Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
				return githubCommentOnIssue(ctx, client, apiURL, request)
			},
		},
		{
			Name:    "close_issue",
			Summary: "Closes a GitHub issue.",
			Input:   githubCloseIssueSchema,
			Writes:  true,
			Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
				return githubCloseIssue(ctx, client, apiURL, request)
			},
		},
	}
}

// githubIssuesClient is githubClient for the issues API area.
func githubIssuesClient(client *http.Client, apiURL string, credential domain.Credential) (*issues.ClientWithResponses, error) {
	return issues.NewClientWithResponses(apiURL, issues.WithHTTPClient(client),
		issues.WithRequestEditorFn(github.Versioned),
		issues.WithRequestEditorFn(func(_ context.Context, request *http.Request) error {
			Authorize(request, credential)
			return nil
		}))
}

// githubParseRepository splits "owner/name" the way a GitHub subscription's
// target does; anything else is domain.ErrInvalid.
func githubParseRepository(repository string) (owner, name string, err error) {
	if !githubRepositoryPattern.MatchString(repository) {
		return "", "", fmt.Errorf("%w: repository must be owner/name", domain.ErrInvalid)
	}
	owner, name, _ = strings.Cut(repository, "/")
	return owner, name, nil
}

type githubGetIssueInput struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
}

// githubIssueDetail is get_issue's answer.
type githubIssueDetail struct {
	Number   int      `json:"number"`
	Title    string   `json:"title"`
	State    string   `json:"state"`
	Body     string   `json:"body,omitempty"`
	URL      string   `json:"url"`
	Author   string   `json:"author,omitempty"`
	Labels   []string `json:"labels,omitempty"`
	Comments int      `json:"comments"`
}

func githubGetIssue(ctx context.Context, client *http.Client, apiURL string, request ActionRequest) (ActionResult, error) {
	var input githubGetIssueInput
	if err := DecodeInput(request.Input, &input); err != nil {
		return ActionResult{}, err
	}
	if err := Required("repository", input.Repository); err != nil {
		return ActionResult{}, err
	}
	if input.Number <= 0 {
		return ActionResult{}, fmt.Errorf("%w: number must be positive", domain.ErrInvalid)
	}
	owner, name, err := githubParseRepository(input.Repository)
	if err != nil {
		return ActionResult{}, err
	}
	api, err := githubIssuesClient(client, apiURL, request.Credential)
	if err != nil {
		return ActionResult{}, fmt.Errorf("%w: GitHub client could not be built", domain.ErrProviderUnavailable)
	}
	response, err := api.GetWithResponse(ctx, owner, name, input.Number)
	if err != nil {
		return ActionResult{}, fmt.Errorf("%w: GitHub request failed", domain.ErrProviderUnavailable)
	}
	if err := githubStatusError(response.HTTPResponse); err != nil {
		return ActionResult{}, err
	}
	if response.JSON200 == nil {
		return ActionResult{}, fmt.Errorf("%w: GitHub answered without an issue", domain.ErrProviderUnavailable)
	}
	issue := response.JSON200
	return ActionResult{
		Resource: integrationsv1.Resource{Type: "issue", ID: strconv.FormatInt(issue.ID, 10), Name: issue.Title, URL: issue.HTMLURL},
		Result: Answered(githubIssueDetail{
			Number: issue.Number, Title: issue.Title, State: issue.State,
			Body: githubStringValue(issue.Body), URL: issue.HTMLURL,
			Author: githubUserLogin(issue.User), Labels: githubLabelNames(issue.Labels), Comments: issue.Comments,
		}),
	}, nil
}

type githubListIssuesInput struct {
	Repository string `json:"repository"`
	State      string `json:"state"`
	Labels     string `json:"labels"`
	Limit      int    `json:"limit"`
}

// githubIssueSummary is one entry of list_issues' answer.
type githubIssueSummary struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	URL    string `json:"url"`
	Author string `json:"author,omitempty"`
}

func githubListIssues(ctx context.Context, client *http.Client, apiURL string, request ActionRequest) (ActionResult, error) {
	var input githubListIssuesInput
	if err := DecodeInput(request.Input, &input); err != nil {
		return ActionResult{}, err
	}
	if err := Required("repository", input.Repository); err != nil {
		return ActionResult{}, err
	}
	var state issues.ListForRepoParamsState
	switch input.State {
	case "", "open":
		state = issues.ListForRepoParamsStateOpen
	case "closed":
		state = issues.ListForRepoParamsStateClosed
	case "all":
		state = issues.ListForRepoParamsStateAll
	default:
		return ActionResult{}, fmt.Errorf("%w: state must be \"open\", \"closed\" or \"all\"", domain.ErrInvalid)
	}
	limit := input.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 {
		return ActionResult{}, fmt.Errorf("%w: limit must be 1 to 50", domain.ErrInvalid)
	}
	owner, name, err := githubParseRepository(input.Repository)
	if err != nil {
		return ActionResult{}, err
	}
	params := &issues.ListForRepoParams{State: &state, PerPage: &limit}
	if input.Labels != "" {
		params.Labels = &input.Labels
	}
	api, err := githubIssuesClient(client, apiURL, request.Credential)
	if err != nil {
		return ActionResult{}, fmt.Errorf("%w: GitHub client could not be built", domain.ErrProviderUnavailable)
	}
	response, err := api.ListForRepoWithResponse(ctx, owner, name, params)
	if err != nil {
		return ActionResult{}, fmt.Errorf("%w: GitHub request failed", domain.ErrProviderUnavailable)
	}
	if err := githubStatusError(response.HTTPResponse); err != nil {
		return ActionResult{}, err
	}
	if response.JSON200 == nil {
		return ActionResult{}, fmt.Errorf("%w: GitHub answered without issues", domain.ErrProviderUnavailable)
	}
	summaries := make([]githubIssueSummary, 0, len(*response.JSON200))
	for _, issue := range *response.JSON200 {
		summaries = append(summaries, githubIssueSummary{
			Number: issue.Number, Title: issue.Title, State: issue.State,
			URL: issue.HTMLURL, Author: githubUserLogin(issue.User),
		})
	}
	return ActionResult{Result: Answered(summaries)}, nil
}

type githubCreateIssueInput struct {
	Repository string   `json:"repository"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Labels     []string `json:"labels"`
	Assignees  []string `json:"assignees"`
}

// githubIssueCreated is create_issue's answer.
type githubIssueCreated struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Title  string `json:"title"`
}

func githubCreateIssue(ctx context.Context, client *http.Client, apiURL string, request ActionRequest) (ActionResult, error) {
	var input githubCreateIssueInput
	if err := DecodeInput(request.Input, &input); err != nil {
		return ActionResult{}, err
	}
	if err := Required("repository", input.Repository); err != nil {
		return ActionResult{}, err
	}
	if err := Required("title", input.Title); err != nil {
		return ActionResult{}, err
	}
	owner, name, err := githubParseRepository(input.Repository)
	if err != nil {
		return ActionResult{}, err
	}
	var title issues.CreateJSONBody_Title
	if err := title.FromCreateJSONBodyTitle0(input.Title); err != nil {
		return ActionResult{}, fmt.Errorf("encode issue title: %w", err)
	}
	body := issues.CreateJSONBody{Title: title}
	if input.Body != "" {
		body.Body = &input.Body
	}
	if len(input.Labels) > 0 {
		labels := make([]issues.CreateJSONBody_Labels, len(input.Labels))
		for index, label := range input.Labels {
			if err := labels[index].FromCreateJSONBodyLabels0(label); err != nil {
				return ActionResult{}, fmt.Errorf("encode issue labels: %w", err)
			}
		}
		body.Labels = &labels
	}
	if len(input.Assignees) > 0 {
		body.Assignees = &input.Assignees
	}
	api, err := githubIssuesClient(client, apiURL, request.Credential)
	if err != nil {
		return ActionResult{}, fmt.Errorf("%w: GitHub client could not be built", domain.ErrProviderUnavailable)
	}
	response, err := api.CreateWithResponse(ctx, owner, name, issues.CreateJSONRequestBody(body))
	if err != nil {
		return ActionResult{}, fmt.Errorf("%w: GitHub request failed", domain.ErrProviderUnavailable)
	}
	if err := githubStatusError(response.HTTPResponse); err != nil {
		return ActionResult{}, err
	}
	if response.JSON201 == nil {
		return ActionResult{}, fmt.Errorf("%w: GitHub answered without an issue", domain.ErrProviderUnavailable)
	}
	issue := response.JSON201
	return ActionResult{
		Resource: integrationsv1.Resource{Type: "issue", ID: strconv.FormatInt(issue.ID, 10), Name: issue.Title, URL: issue.HTMLURL},
		Result:   Answered(githubIssueCreated{Number: issue.Number, URL: issue.HTMLURL, Title: issue.Title}),
	}, nil
}

type githubCommentOnIssueInput struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Body       string `json:"body"`
}

// githubCommentCreated is comment_on_issue's answer.
type githubCommentCreated struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

func githubCommentOnIssue(ctx context.Context, client *http.Client, apiURL string, request ActionRequest) (ActionResult, error) {
	var input githubCommentOnIssueInput
	if err := DecodeInput(request.Input, &input); err != nil {
		return ActionResult{}, err
	}
	if err := Required("repository", input.Repository); err != nil {
		return ActionResult{}, err
	}
	if err := Required("body", input.Body); err != nil {
		return ActionResult{}, err
	}
	if input.Number <= 0 {
		return ActionResult{}, fmt.Errorf("%w: number must be positive", domain.ErrInvalid)
	}
	owner, name, err := githubParseRepository(input.Repository)
	if err != nil {
		return ActionResult{}, err
	}
	api, err := githubIssuesClient(client, apiURL, request.Credential)
	if err != nil {
		return ActionResult{}, fmt.Errorf("%w: GitHub client could not be built", domain.ErrProviderUnavailable)
	}
	response, err := api.CreateCommentWithResponse(ctx, owner, name, input.Number, issues.CreateCommentJSONRequestBody{Body: input.Body})
	if err != nil {
		return ActionResult{}, fmt.Errorf("%w: GitHub request failed", domain.ErrProviderUnavailable)
	}
	if err := githubStatusError(response.HTTPResponse); err != nil {
		return ActionResult{}, err
	}
	if response.JSON201 == nil {
		return ActionResult{}, fmt.Errorf("%w: GitHub answered without a comment", domain.ErrProviderUnavailable)
	}
	comment := response.JSON201
	commentID := strconv.FormatInt(comment.ID, 10)
	return ActionResult{
		Resource: integrationsv1.Resource{Type: "comment", ID: commentID, URL: comment.HTMLURL},
		Result:   Answered(githubCommentCreated{ID: commentID, URL: comment.HTMLURL}),
	}, nil
}

type githubCloseIssueInput struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Reason     string `json:"reason"`
}

// githubIssueClosed is close_issue's answer.
type githubIssueClosed struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	URL    string `json:"url"`
}

func githubCloseIssue(ctx context.Context, client *http.Client, apiURL string, request ActionRequest) (ActionResult, error) {
	var input githubCloseIssueInput
	if err := DecodeInput(request.Input, &input); err != nil {
		return ActionResult{}, err
	}
	if err := Required("repository", input.Repository); err != nil {
		return ActionResult{}, err
	}
	if input.Number <= 0 {
		return ActionResult{}, fmt.Errorf("%w: number must be positive", domain.ErrInvalid)
	}
	owner, name, err := githubParseRepository(input.Repository)
	if err != nil {
		return ActionResult{}, err
	}
	state := issues.UpdateJSONBodyStateClosed
	body := issues.UpdateJSONBody{State: &state}
	switch input.Reason {
	case "":
	case "completed":
		reason := issues.UpdateJSONBodyStateReasonCompleted
		body.StateReason = &reason
	case "not_planned":
		reason := issues.UpdateJSONBodyStateReasonNotPlanned
		body.StateReason = &reason
	default:
		return ActionResult{}, fmt.Errorf("%w: reason must be \"completed\" or \"not_planned\"", domain.ErrInvalid)
	}
	api, err := githubIssuesClient(client, apiURL, request.Credential)
	if err != nil {
		return ActionResult{}, fmt.Errorf("%w: GitHub client could not be built", domain.ErrProviderUnavailable)
	}
	response, err := api.UpdateWithResponse(ctx, owner, name, input.Number, issues.UpdateJSONRequestBody(body))
	if err != nil {
		return ActionResult{}, fmt.Errorf("%w: GitHub request failed", domain.ErrProviderUnavailable)
	}
	if err := githubStatusError(response.HTTPResponse); err != nil {
		return ActionResult{}, err
	}
	if response.JSON200 == nil {
		return ActionResult{}, fmt.Errorf("%w: GitHub answered without an issue", domain.ErrProviderUnavailable)
	}
	issue := response.JSON200
	return ActionResult{
		Resource: integrationsv1.Resource{Type: "issue", ID: strconv.FormatInt(issue.ID, 10), Name: issue.Title, URL: issue.HTMLURL},
		Result:   Answered(githubIssueClosed{Number: issue.Number, State: issue.State, URL: issue.HTMLURL}),
	}, nil
}

// githubStringValue reads a possibly absent string field, such as an issue's
// body, as the empty string.
func githubStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// githubUserLogin reads a possibly absent GitHub user's login, such as an
// issue's author after the account is deleted.
func githubUserLogin(user *issues.NullableSimpleUser) string {
	if user == nil {
		return ""
	}
	return user.Login
}

// githubLabelNames reads an issue's label names. GitHub answers each label as
// an object, but the schema also allows a bare name, which the generated
// client keeps as a second union case.
func githubLabelNames(labels []issues.Issue_Labels) []string {
	if len(labels) == 0 {
		return nil
	}
	names := make([]string, 0, len(labels))
	for _, label := range labels {
		if object, err := label.AsIssueLabels1(); err == nil && object.Name != nil {
			names = append(names, *object.Name)
			continue
		}
		if name, err := label.AsIssueLabels0(); err == nil {
			names = append(names, name)
		}
	}
	return names
}
