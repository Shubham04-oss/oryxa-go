package actionflow

import (
	"encoding/json"
	"fmt"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
)

func mapGitHub(request MapRequest) (MappedResult, error) {
	switch request.ActionName {
	case "create-issue":
		return mapGitHubCreatedIssue(request)
	case "comment-on-issue":
		return mapGitHubCreatedComment(request)
	case "get-issue":
		return mapGitHubIssue(request)
	case "list-pull-requests":
		return mapGitHubPullRequests(request)
	default:
		return MappedResult{}, fmt.Errorf("%w: unsupported GitHub action", ErrPieceOutputInvalid)
	}
}

type githubIssueOutput struct {
	Number  int64  `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func mapGitHubCreatedIssue(request MapRequest) (MappedResult, error) {
	var output githubIssueOutput
	owner, repo, _, err := githubProps(request.Props)
	if err != nil || json.Unmarshal(request.Output, &output) != nil || output.Number < 1 || output.HTMLURL == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, _ := json.Marshal(githubSafeIssue(output))
	data := integrationevents.GitHubIssueCreated{ExecutionID: request.Execution.ID,
		ConnectionID: request.Connection.ID, Repository: owner + "/" + repo,
		IssueNumber: output.Number, URL: output.HTMLURL,
		RequestedBy: request.Execution.MemberID, CreatedAt: request.OccurredAt}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GitHubIssueCreatedDetailType, Data: data}, nil
}

func mapGitHubCreatedComment(request MapRequest) (MappedResult, error) {
	var output struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	owner, repo, issueNumber, err := githubProps(request.Props)
	if err != nil || json.Unmarshal(request.Output, &output) != nil || issueNumber < 1 || output.ID < 1 || output.HTMLURL == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, _ := json.Marshal(struct {
		CommentID int64  `json:"comment_id"`
		URL       string `json:"url"`
	}{CommentID: output.ID, URL: output.HTMLURL})
	data := integrationevents.GitHubIssueCommentCreated{ExecutionID: request.Execution.ID,
		ConnectionID: request.Connection.ID, Repository: owner + "/" + repo,
		IssueNumber: issueNumber, CommentID: output.ID, URL: output.HTMLURL,
		RequestedBy: request.Execution.MemberID, CreatedAt: request.OccurredAt}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GitHubIssueCommentCreatedDetailType, Data: data}, nil
}

func mapGitHubIssue(request MapRequest) (MappedResult, error) {
	var output githubIssueOutput
	if json.Unmarshal(request.Output, &output) != nil || output.Number < 1 || output.HTMLURL == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(githubSafeIssue(output))
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func mapGitHubPullRequests(request MapRequest) (MappedResult, error) {
	type pullRequest struct {
		Number  int64  `json:"number"`
		Title   string `json:"title"`
		State   string `json:"state"`
		HTMLURL string `json:"html_url"`
		Draft   bool   `json:"draft"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	var output struct {
		Items []pullRequest `json:"items"`
		Count int           `json:"count"`
	}
	if json.Unmarshal(request.Output, &output) != nil || output.Items == nil || len(output.Items) > 100 || output.Count != len(output.Items) {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	type safePullRequest struct {
		Number int64  `json:"number"`
		Title  string `json:"title"`
		State  string `json:"state"`
		URL    string `json:"url"`
		Draft  bool   `json:"draft"`
		Author string `json:"author,omitempty"`
	}
	items := make([]safePullRequest, 0, len(output.Items))
	for _, pullRequest := range output.Items {
		if pullRequest.Number < 1 || pullRequest.HTMLURL == "" {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		items = append(items, safePullRequest{Number: pullRequest.Number, Title: pullRequest.Title,
			State: pullRequest.State, URL: pullRequest.HTMLURL, Draft: pullRequest.Draft,
			Author: pullRequest.User.Login})
	}
	result, err := json.Marshal(struct {
		Items []safePullRequest `json:"items"`
		Count int               `json:"count"`
	}{Items: items, Count: output.Count})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func githubProps(raw json.RawMessage) (owner, repo string, issueNumber int64, err error) {
	var props struct {
		Owner       string `json:"owner"`
		Repo        string `json:"repo"`
		IssueNumber int64  `json:"issue_number"`
	}
	if json.Unmarshal(raw, &props) != nil || props.Owner == "" || props.Repo == "" {
		return "", "", 0, ErrPieceOutputInvalid
	}
	return props.Owner, props.Repo, props.IssueNumber, nil
}

func githubSafeIssue(output githubIssueOutput) any {
	labels := make([]string, 0, len(output.Labels))
	for _, label := range output.Labels {
		if label.Name != "" {
			labels = append(labels, label.Name)
		}
	}
	return struct {
		Number int64    `json:"number"`
		Title  string   `json:"title"`
		State  string   `json:"state"`
		URL    string   `json:"url"`
		Author string   `json:"author,omitempty"`
		Labels []string `json:"labels"`
	}{Number: output.Number, Title: output.Title, State: output.State,
		URL: output.HTMLURL, Author: output.User.Login, Labels: labels}
}
