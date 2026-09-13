package catalog

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/actionflow"
)

func githubActionDefinition(actionName string) (actionflow.Definition, bool) {
	definition := actionflow.Definition{Provider: "github", PieceName: GitHubPieceName,
		PieceVersion: GitHubPieceVersion, ActionName: actionName, RequiredScopes: []string{"repo"}}
	switch actionName {
	case "create-issue":
		definition.RunnerActionName = "create_issue_ai"
		definition.ValidateProps = validateGitHubCreateIssue
	case "comment-on-issue":
		definition.RunnerActionName = "create_issue_comment_ai"
		definition.ValidateProps = validateGitHubIssueComment
	case "get-issue":
		definition.RunnerActionName = "get_issue_ai"
		definition.ValidateProps = validateGitHubIssueNumber
	case "list-pull-requests":
		definition.RunnerActionName = "list_pull_requests"
		definition.ValidateProps = validateGitHubPullRequestList
	default:
		return actionflow.Definition{}, false
	}
	return definition, true
}

func validateGitHubCreateIssue(raw json.RawMessage) error {
	var props struct {
		Owner     string   `json:"owner"`
		Repo      string   `json:"repo"`
		Title     string   `json:"title"`
		Body      string   `json:"body"`
		Labels    []string `json:"labels"`
		Assignees []string `json:"assignees"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		strings.TrimSpace(props.Title) == "" || len(props.Title) > 256 || len(props.Body) > 64*1024 ||
		!boundedNames(props.Labels, 20, 100) || !boundedNames(props.Assignees, 20, 100) {
		return errors.New("invalid GitHub create-issue properties")
	}
	return nil
}

func validateGitHubIssueComment(raw json.RawMessage) error {
	var props struct {
		Owner       string `json:"owner"`
		Repo        string `json:"repo"`
		IssueNumber int64  `json:"issue_number"`
		Body        string `json:"body"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		props.IssueNumber < 1 || props.IssueNumber > 2_147_483_647 || strings.TrimSpace(props.Body) == "" || len(props.Body) > 64*1024 {
		return errors.New("invalid GitHub issue-comment properties")
	}
	return nil
}

func validateGitHubIssueNumber(raw json.RawMessage) error {
	var props struct {
		Owner       string `json:"owner"`
		Repo        string `json:"repo"`
		IssueNumber int64  `json:"issue_number"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) || props.IssueNumber < 1 || props.IssueNumber > 2_147_483_647 {
		return errors.New("invalid GitHub issue properties")
	}
	return nil
}

func validateGitHubPullRequestList(raw json.RawMessage) error {
	var props struct {
		Owner     string `json:"owner"`
		Repo      string `json:"repo"`
		State     string `json:"state"`
		Head      string `json:"head"`
		Base      string `json:"base"`
		Sort      string `json:"sort"`
		Direction string `json:"direction"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!oneOf(props.State, "", "open", "closed", "all") || !oneOf(props.Sort, "", "created", "updated", "popularity", "long-running") ||
		!oneOf(props.Direction, "", "asc", "desc") || len(props.Head) > 200 || len(props.Base) > 200 || strings.ContainsAny(props.Head+props.Base, "\r\n\t") {
		return errors.New("invalid GitHub pull-request list properties")
	}
	return nil
}

func githubName(value string) bool {
	if value == "" || len(value) > 100 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("-_.", character)) {
			return false
		}
	}
	return true
}

func boundedNames(values []string, maximumItems, maximumLength int) bool {
	if len(values) > maximumItems {
		return false
	}
	for _, value := range values {
		if value == "" || len(value) > maximumLength || strings.ContainsAny(value, "\r\n\t") {
			return false
		}
	}
	return true
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
