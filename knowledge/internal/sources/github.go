package sources

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/issues"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

// githubMaxPage is the most GitHub answers with at once.
const githubMaxPage = 100

// GitHub reads a repository's issues and pull requests, which GitHub lists
// together. It is built on the generated client rather than on integrations'
// action, which caps a list at fifty and offers no cursor at all.
type GitHub struct {
	client *Client
	server string
}

func NewGitHub(client *Client) *GitHub {
	return &GitHub{client: client, server: github.Server}
}

func (source *GitHub) Provider() string { return "github" }

func (source *GitHub) Target(target map[string]string) error {
	_, _, err := splitRepository(target["repository"])
	return err
}

// Fetch walks the repository a page at a time, oldest change first, so the
// page number is a cursor a stopped run resumes from. An incremental pass asks
// only for what changed after the watermark; a full pass asks for everything,
// because only a full pass may retract what it did not see.
func (source *GitHub) Fetch(ctx context.Context, request Request) (Page, error) {
	owner, repository, err := splitRepository(request.Source.Target["repository"])
	if err != nil {
		return Page{}, err
	}
	client, err := issues.NewClientWithResponses(source.server,
		issues.WithHTTPClient(source.client.For(request.Credential)),
		issues.WithRequestEditorFn(github.Versioned))
	if err != nil {
		return Page{}, fmt.Errorf("build the GitHub client: %w", err)
	}

	number := pageNumber(request.Cursor.Value)
	perPage := Limit(request.Limit, githubMaxPage)
	state := issues.ListForRepoParamsStateAll
	sort := issues.ListForRepoParamsSortUpdated
	direction := issues.ListForRepoParamsDirectionAsc
	params := &issues.ListForRepoParams{
		State: &state, Sort: &sort, Direction: &direction,
		PerPage: &perPage, Page: &number,
	}
	if !request.Full && request.Cursor.Watermark != nil {
		since := issues.Since(*request.Cursor.Watermark)
		params.Since = &since
	}

	answer, err := client.ListForRepoWithResponse(ctx, owner, repository, params)
	if err != nil {
		return Page{}, providerError(err)
	}
	if answer.JSON200 == nil {
		return Page{}, StatusError(answer.HTTPResponse.StatusCode, answer.HTTPResponse.Header)
	}

	page := Page{Items: make([]Item, 0, len(*answer.JSON200))}
	for _, issue := range *answer.JSON200 {
		page.Items = append(page.Items, githubItem(issue))
		page.Watermark = watermarkAfter(page.Watermark, issue.UpdatedAt)
	}
	// A short page is the last one: GitHub fills a page it can fill.
	if len(page.Items) == perPage {
		page.Cursor = strconv.Itoa(number + 1)
	}
	return page, nil
}

func githubItem(issue issues.Issue) Item {
	kind := knowledgev1.KindIssue
	if issue.PullRequest != nil {
		kind = knowledgev1.KindPullRequest
	}
	body := ""
	if issue.Body != nil {
		body = *issue.Body
	}
	updated := issue.UpdatedAt.UTC()
	metadata := map[string]string{
		"state":    issue.State,
		"number":   strconv.Itoa(issue.Number),
		"comments": strconv.Itoa(issue.Comments),
	}
	if issue.User != nil && issue.User.Login != "" {
		metadata["author"] = issue.User.Login
	}
	return Item{
		// The number is what a person and the API both call this issue, and it
		// does not change; the internal id is neither.
		ExternalID: strconv.Itoa(issue.Number),
		Kind:       kind,
		Title:      issue.Title,
		URL:        issue.HTMLURL,
		MIME:       transform.MIMEMarkdown,
		Content:    []byte(body),
		Metadata:   metadata,
		UpdatedAt:  &updated,
	}
}

// splitRepository reads the one field a GitHub source names.
func splitRepository(value string) (string, string, error) {
	repository := strings.TrimSpace(value)
	if repository == "" {
		return "", "", MissingTarget("a repository, as owner/name")
	}
	owner, name, found := strings.Cut(repository, "/")
	if !found || strings.TrimSpace(owner) == "" || strings.TrimSpace(name) == "" || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("a repository reads owner/name, not %q: %w", repository, domain.ErrInvalid)
	}
	return owner, name, nil
}

// pageNumber reads a cursor back. Anything unreadable starts at the first
// page rather than failing the run.
func pageNumber(cursor string) int {
	if cursor == "" {
		return 1
	}
	number, err := strconv.Atoi(cursor)
	if err != nil || number < 1 {
		return 1
	}
	return number
}

// providerError keeps a reason the caller can act on, and never lets a
// generated client's wrapped text carry a provider's own error body.
func providerError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{
		domain.ErrRateLimited, domain.ErrNeedsReconnect, domain.ErrForbidden,
		domain.ErrNotFound, domain.ErrProviderUnavailable, domain.ErrInvalid,
		context.Canceled, context.DeadlineExceeded,
	} {
		if errors.Is(err, known) {
			return known
		}
	}
	return fmt.Errorf("%w: the provider did not answer", domain.ErrProviderUnavailable)
}

// watermarkAfter is the later of two times, for a page that advances one.
func watermarkAfter(current *time.Time, candidate time.Time) *time.Time {
	if current == nil || candidate.After(*current) {
		value := candidate.UTC()
		return &value
	}
	return current
}
