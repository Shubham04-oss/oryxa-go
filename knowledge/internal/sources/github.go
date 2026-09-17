package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

// githubAPI is where GitHub's REST API lives.
const githubAPI = "https://api.github.com"

// githubAPIVersion is the REST API version every call asks for.
const githubAPIVersion = "2022-11-28"

// githubMaxPage is the most GitHub answers with at once.
const githubMaxPage = 100

// GitHub reads a repository's issues and pull requests, which GitHub lists
// together. There is no generated client for it here any more than there is
// for Slack — Composio holds the credential, and a generated client has
// nowhere to place one — so this speaks the REST API directly, hand-rolled
// on the package's shared client.
type GitHub struct {
	client *Client
	api    string
}

func NewGitHub(client *Client) *GitHub {
	return &GitHub{client: client, api: githubAPI}
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

	number := pageNumber(request.Cursor.Value)
	perPage := Limit(request.Limit, githubMaxPage)
	query := url.Values{
		"state":     {"all"},
		"sort":      {"updated"},
		"direction": {"asc"},
		"per_page":  {strconv.Itoa(perPage)},
		"page":      {strconv.Itoa(number)},
	}
	if !request.Full && request.Cursor.Watermark != nil {
		query.Set("since", request.Cursor.Watermark.UTC().Format(time.RFC3339))
	}

	endpoint := source.api + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repository) + "/issues?" + query.Encode()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Page{}, fmt.Errorf("build the GitHub request: %w", err)
	}
	httpRequest.Header.Set("Accept", "application/vnd.github+json")
	httpRequest.Header.Set("X-GitHub-Api-Version", githubAPIVersion)

	response, err := source.client.Do(ctx, httpRequest, request.Credential)
	if err != nil {
		return Page{}, err
	}
	var issues []githubIssue
	if err := json.Unmarshal(response.Body, &issues); err != nil {
		return Page{}, fmt.Errorf("%w: GitHub's answer could not be read", domain.ErrProviderUnavailable)
	}

	page := Page{Items: make([]Item, 0, len(issues))}
	for _, issue := range issues {
		page.Items = append(page.Items, githubItem(issue))
		page.Watermark = watermarkAfter(page.Watermark, issue.UpdatedAt)
	}
	// A short page is the last one: GitHub fills a page it can fill.
	if len(page.Items) == perPage {
		page.Cursor = strconv.Itoa(number + 1)
	}
	return page, nil
}

// githubIssue is the slice of GitHub's issue object Fetch reads. GitHub lists
// pull requests through this same endpoint, marking one with a pull_request
// field that a plain issue never carries — present or not, since GitHub
// leaves the field out rather than sending it null.
type githubIssue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	State     string    `json:"state"`
	HTMLURL   string    `json:"html_url"`
	Comments  int       `json:"comments"`
	UpdatedAt time.Time `json:"updated_at"`
	User      *struct {
		Login string `json:"login"`
	} `json:"user"`
	PullRequest *struct{} `json:"pull_request"`
}

func githubItem(issue githubIssue) Item {
	kind := knowledgev1.KindIssue
	if issue.PullRequest != nil {
		kind = knowledgev1.KindPullRequest
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
		Content:    []byte(issue.Body),
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
