package catalog

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/actionflow"
)

const (
	githubRepoScope    = "repo"
	githubUserScope    = "read:user"
	githubOrgReadScope = "read:org"
	githubGistScope    = "gist"
)

type githubActionSpec struct {
	Runner   string
	Scopes   []string
	Validate func(json.RawMessage) error
}

var githubRepoScopes = []string{githubRepoScope}
var githubUserScopes = []string{githubUserScope}
var githubOrgScopes = []string{githubOrgReadScope}
var githubGistScopes = []string{githubGistScope}

var githubActions = map[string]githubActionSpec{
	"create-issue":                   {"create_issue_ai", githubRepoScopes, validateGitHubCreateIssue},
	"get-issue":                      {"get_issue_ai", githubRepoScopes, validateGitHubIssueNumber},
	"list-issues":                    {"list_repository_issues", githubRepoScopes, validateGitHubIssueList},
	"update-issue":                   {"update_issue_ai", githubRepoScopes, validateGitHubUpdateIssue},
	"comment-on-issue":               {"create_issue_comment_ai", githubRepoScopes, validateGitHubIssueComment},
	"list-issue-comments":            {"list_issue_comments", githubRepoScopes, validateGitHubIssueNumber},
	"lock-issue":                     {"lock_issue_ai", githubRepoScopes, validateGitHubLockIssue},
	"unlock-issue":                   {"unlock_issue_ai", githubRepoScopes, validateGitHubIssueNumber},
	"add-labels-to-issue":            {"add_labels_to_issue_ai", githubRepoScopes, validateGitHubIssueLabels},
	"remove-label-from-issue":        {"remove_label_from_issue", githubRepoScopes, validateGitHubRemoveIssueLabel},
	"set-issue-labels":               {"set_issue_labels", githubRepoScopes, validateGitHubIssueLabels},
	"list-issue-labels":              {"list_issue_labels", githubRepoScopes, validateGitHubIssueNumber},
	"add-assignees-to-issue":         {"add_assignees_to_issue", githubRepoScopes, validateGitHubIssueAssignees},
	"remove-assignees-from-issue":    {"remove_assignees_from_issue", githubRepoScopes, validateGitHubIssueAssignees},
	"list-assignees":                 {"list_assignees", githubRepoScopes, validateGitHubRepo},
	"list-repository-labels":         {"list_repository_labels", githubRepoScopes, validateGitHubRepo},
	"create-label":                   {"create_label", githubRepoScopes, validateGitHubCreateLabel},
	"update-label":                   {"update_label", githubRepoScopes, validateGitHubUpdateLabel},
	"create-pull-request":            {"create_pull_request", githubRepoScopes, validateGitHubCreatePullRequest},
	"get-pull-request":               {"get_pull_request", githubRepoScopes, validateGitHubPullNumber},
	"list-pull-requests":             {"list_pull_requests", githubRepoScopes, validateGitHubPullRequestList},
	"update-pull-request":            {"update_pull_request", githubRepoScopes, validateGitHubUpdatePullRequest},
	"merge-pull-request":             {"merge_pull_request", githubRepoScopes, validateGitHubMergePullRequest},
	"list-pull-request-files":        {"list_pull_request_files", githubRepoScopes, validateGitHubPullNumber},
	"list-pull-request-commits":      {"list_pull_request_commits", githubRepoScopes, validateGitHubPullNumber},
	"create-pull-request-review":     {"create_pull_request_review", githubRepoScopes, validateGitHubCreateReview},
	"request-pull-request-reviewers": {"request_pull_request_reviewers", githubRepoScopes, validateGitHubRequestReviewers},
	"create-review-comment":          {"create_pull_request_review_comment_ai", githubRepoScopes, validateGitHubReviewComment},
	"list-pull-request-reviews":      {"list_pull_request_reviews", githubRepoScopes, validateGitHubPullNumber},
	"list-review-comments":           {"list_pull_request_review_comments", githubRepoScopes, validateGitHubPullNumber},
	"get-repository":                 {"get_repository", githubRepoScopes, validateGitHubRepo},
	"list-my-repositories":           {"list_my_repositories", githubRepoScopes, validateGitHubMyRepos},
	"list-user-repositories":         {"list_user_repositories", githubUserScopes, validateGitHubUsername},
	"list-organization-repositories": {"list_organization_repositories", githubRepoScopes, validateGitHubOrg},
	"create-repository":              {"create_repository", githubRepoScopes, validateGitHubCreateRepository},
	"create-organization-repository": {"create_organization_repository", githubRepoScopes, validateGitHubCreateOrgRepository},
	"update-repository":              {"update_repository", githubRepoScopes, validateGitHubUpdateRepository},
	"list-branches":                  {"list_branches", githubRepoScopes, validateGitHubRepo},
	"get-branch":                     {"get_branch", githubRepoScopes, validateGitHubBranch},
	"create-branch":                  {"create_branch_ai", githubRepoScopes, validateGitHubCreateBranch},
	"delete-branch":                  {"delete_branch_ai", githubRepoScopes, validateGitHubBranch},
	"get-reference":                  {"get_reference", githubRepoScopes, validateGitHubReference},
	"list-commits":                   {"list_commits", githubRepoScopes, validateGitHubCommitList},
	"get-commit":                     {"get_commit", githubRepoScopes, validateGitHubCommit},
	"compare-commits":                {"compare_commits", githubRepoScopes, validateGitHubCompare},
	"list-contributors":              {"list_contributors", githubRepoScopes, validateGitHubRepo},
	"list-languages":                 {"list_languages", githubRepoScopes, validateGitHubRepo},
	"list-tags":                      {"list_tags", githubRepoScopes, validateGitHubRepo},
	"get-repository-tree":            {"get_repository_tree", githubRepoScopes, validateGitHubTree},
	"search-repositories":            {"search_repositories", githubRepoScopes, validateGitHubRepoSearch},
	"search-issues":                  {"search_issues_and_pull_requests", githubRepoScopes, validateGitHubIssueSearch},
	"search-users":                   {"search_users", githubUserScopes, validateGitHubUserSearch},
	"get-authenticated-user":         {"get_authenticated_user", githubUserScopes, validateGitHubEmpty},
	"get-user":                       {"get_user", githubUserScopes, validateGitHubUsername},
	"list-organization-members":      {"list_organization_members", githubOrgScopes, validateGitHubOrg},
	"create-release":                 {"create_release", githubRepoScopes, validateGitHubCreateRelease},
	"list-releases":                  {"list_releases", githubRepoScopes, validateGitHubRepo},
	"get-release":                    {"get_release", githubRepoScopes, validateGitHubReleaseID},
	"get-latest-release":             {"get_latest_release", githubRepoScopes, validateGitHubRepo},
	"list-collaborators":             {"list_collaborators", githubRepoScopes, validateGitHubRepo},
	"add-collaborator":               {"add_collaborator", githubRepoScopes, validateGitHubCollaborator},
	"remove-collaborator":            {"remove_collaborator", githubRepoScopes, validateGitHubRemoveCollaborator},
	"create-milestone":               {"create_milestone", githubRepoScopes, validateGitHubCreateMilestone},
	"list-milestones":                {"list_milestones", githubRepoScopes, validateGitHubMilestoneList},
	"get-milestone":                  {"get_milestone", githubRepoScopes, validateGitHubMilestoneNumber},
	"update-milestone":               {"update_milestone", githubRepoScopes, validateGitHubUpdateMilestone},
	"star-repository":                {"star_repository", githubRepoScopes, validateGitHubRepo},
	"unstar-repository":              {"unstar_repository", githubRepoScopes, validateGitHubRepo},
	"list-starred-repositories":      {"list_starred_repositories", githubRepoScopes, validateGitHubEmpty},
	"check-repository-starred":       {"check_repository_starred", githubRepoScopes, validateGitHubRepo},
	"get-file-content":               {"get_file_content", githubRepoScopes, validateGitHubGetFile},
	"create-or-update-file":          {"create_or_update_file", githubRepoScopes, validateGitHubCreateOrUpdateFile},
	"delete-file":                    {"delete_file", githubRepoScopes, validateGitHubDeleteFile},
	"search-code":                    {"search_code", githubRepoScopes, validateGitHubCodeSearch},
	"create-gist":                    {"github_create_gist", githubGistScopes, validateGitHubCreateGist},
	"raw-graphql-query":              {"rawGraphqlQuery", githubUserScopes, validateGitHubGraphQL},
	"dispatch-workflow":              {"trigger_workflow_dispatch", githubRepoScopes, validateGitHubWorkflowDispatch},
	"custom-api-call":                {"custom_api_call", githubUserScopes, validateCustomAPICall("api.github.com")},
}

var githubNeedsFiles = map[string]bool{"create-or-update-file": true}

var githubPrepare = map[string]func(json.RawMessage) (json.RawMessage, error){
	"create-or-update-file": prepareGitHubFileContent,
	"dispatch-workflow":     prepareGitHubWorkflowDispatch,
	"custom-api-call":       prepareCustomAPICall,
}

func githubActionDefinition(actionName string) (actionflow.Definition, bool) {
	spec, ok := githubActions[actionName]
	if !ok {
		return actionflow.Definition{}, false
	}
	return actionflow.Definition{
		Provider: "github", PieceName: GitHubPieceName, PieceVersion: GitHubPieceVersion,
		ActionName: actionName, RunnerActionName: spec.Runner,
		RequiredScopes: append([]string(nil), spec.Scopes...), ValidateProps: spec.Validate,
		PrepareProps: githubPrepare[actionName], NeedsFiles: githubNeedsFiles[actionName],
	}, true
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
		!githubPositiveID(props.IssueNumber) || strings.TrimSpace(props.Body) == "" || len(props.Body) > 64*1024 {
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
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) || !githubPositiveID(props.IssueNumber) {
		return errors.New("invalid GitHub issue properties")
	}
	return nil
}

func validateGitHubIssueList(raw json.RawMessage) error {
	var props struct {
		Owner     string `json:"owner"`
		Repo      string `json:"repo"`
		State     string `json:"state"`
		Labels    string `json:"labels"`
		Assignee  string `json:"assignee"`
		Creator   string `json:"creator"`
		Milestone string `json:"milestone"`
		Since     string `json:"since"`
		Sort      string `json:"sort"`
		Direction string `json:"direction"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!oneOf(props.State, "", "open", "closed", "all") || !oneOf(props.Sort, "", "created", "updated", "comments") ||
		!oneOf(props.Direction, "", "asc", "desc") || (props.Labels != "" && !githubQueryValue(props.Labels, 200)) ||
		(props.Assignee != "" && !githubName(props.Assignee) && props.Assignee != "*" && props.Assignee != "none") ||
		(props.Creator != "" && !githubName(props.Creator)) || (props.Milestone != "" && !githubQueryValue(props.Milestone, 100)) ||
		!optionalRFC3339(props.Since) {
		return errors.New("invalid GitHub list-issues properties")
	}
	return nil
}

func validateGitHubUpdateIssue(raw json.RawMessage) error {
	var props struct {
		Owner          string   `json:"owner"`
		Repo           string   `json:"repo"`
		IssueNumber    int64    `json:"issue_number"`
		Title          string   `json:"title"`
		Body           string   `json:"body"`
		State          string   `json:"state"`
		StateReason    string   `json:"state_reason"`
		Milestone      int64    `json:"milestone"`
		ClearMilestone bool     `json:"clear_milestone"`
		Labels         []string `json:"labels"`
		Assignees      []string `json:"assignees"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!githubPositiveID(props.IssueNumber) || len(props.Title) > 256 || len(props.Body) > 64*1024 ||
		!oneOf(props.State, "", "open", "closed") ||
		!oneOf(props.StateReason, "", "completed", "not_planned", "reopened", "duplicate") ||
		props.Milestone < 0 || !boundedNames(props.Labels, 20, 100) || !boundedNames(props.Assignees, 20, 100) {
		return errors.New("invalid GitHub update-issue properties")
	}
	return nil
}

func validateGitHubLockIssue(raw json.RawMessage) error {
	var props struct {
		Owner       string `json:"owner"`
		Repo        string `json:"repo"`
		IssueNumber int64  `json:"issue_number"`
		LockReason  string `json:"lock_reason"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!githubPositiveID(props.IssueNumber) || !oneOf(props.LockReason, "", "off-topic", "too heated", "resolved", "spam") {
		return errors.New("invalid GitHub lock-issue properties")
	}
	return nil
}

func validateGitHubIssueLabels(raw json.RawMessage) error {
	var props struct {
		Owner       string   `json:"owner"`
		Repo        string   `json:"repo"`
		IssueNumber int64    `json:"issue_number"`
		Labels      []string `json:"labels"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!githubPositiveID(props.IssueNumber) || len(props.Labels) == 0 || !boundedNames(props.Labels, 20, 100) {
		return errors.New("invalid GitHub issue label properties")
	}
	return nil
}

func validateGitHubRemoveIssueLabel(raw json.RawMessage) error {
	var props struct {
		Owner       string `json:"owner"`
		Repo        string `json:"repo"`
		IssueNumber int64  `json:"issue_number"`
		Name        string `json:"name"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!githubPositiveID(props.IssueNumber) || !githubQueryValue(props.Name, 100) {
		return errors.New("invalid GitHub remove-label properties")
	}
	return nil
}

func validateGitHubIssueAssignees(raw json.RawMessage) error {
	var props struct {
		Owner       string   `json:"owner"`
		Repo        string   `json:"repo"`
		IssueNumber int64    `json:"issue_number"`
		Assignees   []string `json:"assignees"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!githubPositiveID(props.IssueNumber) || len(props.Assignees) == 0 || !boundedNames(props.Assignees, 20, 100) {
		return errors.New("invalid GitHub issue assignee properties")
	}
	for _, assignee := range props.Assignees {
		if !githubName(assignee) {
			return errors.New("invalid GitHub issue assignee properties")
		}
	}
	return nil
}

func validateGitHubCreateLabel(raw json.RawMessage) error {
	var props struct {
		Owner       string `json:"owner"`
		Repo        string `json:"repo"`
		Name        string `json:"name"`
		Color       string `json:"color"`
		Description string `json:"description"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!githubQueryValue(props.Name, 100) || !githubColor(props.Color) || len(props.Description) > 1024 {
		return errors.New("invalid GitHub create-label properties")
	}
	return nil
}

func validateGitHubUpdateLabel(raw json.RawMessage) error {
	var props struct {
		Owner       string `json:"owner"`
		Repo        string `json:"repo"`
		Name        string `json:"name"`
		NewName     string `json:"new_name"`
		Color       string `json:"color"`
		Description string `json:"description"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!githubQueryValue(props.Name, 100) || (props.NewName != "" && !githubQueryValue(props.NewName, 100)) ||
		!githubColor(props.Color) || len(props.Description) > 1024 {
		return errors.New("invalid GitHub update-label properties")
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
		!oneOf(props.Direction, "", "asc", "desc") || (props.Head != "" && !githubRef(props.Head, 200)) ||
		(props.Base != "" && !githubRef(props.Base, 200)) {
		return errors.New("invalid GitHub pull-request list properties")
	}
	return nil
}

func validateGitHubCreatePullRequest(raw json.RawMessage) error {
	var props struct {
		Owner               string `json:"owner"`
		Repo                string `json:"repo"`
		Title               string `json:"title"`
		Head                string `json:"head"`
		Base                string `json:"base"`
		Body                string `json:"body"`
		Draft               bool   `json:"draft"`
		MaintainerCanModify bool   `json:"maintainer_can_modify"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		strings.TrimSpace(props.Title) == "" || len(props.Title) > 256 || !githubRef(props.Head, 200) ||
		!githubRef(props.Base, 200) || len(props.Body) > 64*1024 {
		return errors.New("invalid GitHub create-pull-request properties")
	}
	return nil
}

func validateGitHubPullNumber(raw json.RawMessage) error {
	var props struct {
		Owner      string `json:"owner"`
		Repo       string `json:"repo"`
		PullNumber int64  `json:"pull_number"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) || !githubPositiveID(props.PullNumber) {
		return errors.New("invalid GitHub pull-request properties")
	}
	return nil
}

func validateGitHubUpdatePullRequest(raw json.RawMessage) error {
	var props struct {
		Owner      string `json:"owner"`
		Repo       string `json:"repo"`
		PullNumber int64  `json:"pull_number"`
		Title      string `json:"title"`
		Body       string `json:"body"`
		State      string `json:"state"`
		Base       string `json:"base"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!githubPositiveID(props.PullNumber) || len(props.Title) > 256 || len(props.Body) > 64*1024 ||
		!oneOf(props.State, "", "open", "closed") || (props.Base != "" && !githubRef(props.Base, 200)) {
		return errors.New("invalid GitHub update-pull-request properties")
	}
	return nil
}

func validateGitHubMergePullRequest(raw json.RawMessage) error {
	var props struct {
		Owner         string `json:"owner"`
		Repo          string `json:"repo"`
		PullNumber    int64  `json:"pull_number"`
		MergeMethod   string `json:"merge_method"`
		CommitTitle   string `json:"commit_title"`
		CommitMessage string `json:"commit_message"`
		SHA           string `json:"sha"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!githubPositiveID(props.PullNumber) || !oneOf(props.MergeMethod, "", "merge", "squash", "rebase") ||
		len(props.CommitTitle) > 256 || len(props.CommitMessage) > 64*1024 || (props.SHA != "" && !githubRef(props.SHA, 64)) {
		return errors.New("invalid GitHub merge-pull-request properties")
	}
	return nil
}

func validateGitHubCreateReview(raw json.RawMessage) error {
	var props struct {
		Owner      string `json:"owner"`
		Repo       string `json:"repo"`
		PullNumber int64  `json:"pull_number"`
		Event      string `json:"event"`
		Body       string `json:"body"`
		CommitID   string `json:"commit_id"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!githubPositiveID(props.PullNumber) || !oneOf(props.Event, "APPROVE", "REQUEST_CHANGES", "COMMENT") ||
		len(props.Body) > 64*1024 || (props.CommitID != "" && !githubRef(props.CommitID, 64)) {
		return errors.New("invalid GitHub create-pull-request-review properties")
	}
	if props.Event != "APPROVE" && strings.TrimSpace(props.Body) == "" {
		return errors.New("invalid GitHub create-pull-request-review properties")
	}
	return nil
}

func validateGitHubRequestReviewers(raw json.RawMessage) error {
	var props struct {
		Owner         string   `json:"owner"`
		Repo          string   `json:"repo"`
		PullNumber    int64    `json:"pull_number"`
		Reviewers     []string `json:"reviewers"`
		TeamReviewers []string `json:"team_reviewers"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!githubPositiveID(props.PullNumber) || (len(props.Reviewers) == 0 && len(props.TeamReviewers) == 0) ||
		!boundedNames(props.Reviewers, 20, 100) || !boundedNames(props.TeamReviewers, 20, 100) {
		return errors.New("invalid GitHub request-reviewers properties")
	}
	for _, reviewer := range props.Reviewers {
		if !githubName(reviewer) {
			return errors.New("invalid GitHub request-reviewers properties")
		}
	}
	for _, team := range props.TeamReviewers {
		if !githubName(team) {
			return errors.New("invalid GitHub request-reviewers properties")
		}
	}
	return nil
}

func validateGitHubReviewComment(raw json.RawMessage) error {
	var props struct {
		Owner      string `json:"owner"`
		Repo       string `json:"repo"`
		PullNumber int64  `json:"pull_number"`
		CommitID   string `json:"commit_id"`
		Path       string `json:"path"`
		Body       string `json:"body"`
		Position   int64  `json:"position"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!githubPositiveID(props.PullNumber) || !githubRef(props.CommitID, 64) || !githubRef(props.Path, 1024) ||
		strings.TrimSpace(props.Body) == "" || len(props.Body) > 64*1024 || props.Position < 1 {
		return errors.New("invalid GitHub create-review-comment properties")
	}
	return nil
}

func validateGitHubRepo(raw json.RawMessage) error {
	var props struct {
		Owner string `json:"owner"`
		Repo  string `json:"repo"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) {
		return errors.New("invalid GitHub repository properties")
	}
	return nil
}

func validateGitHubEmpty(raw json.RawMessage) error {
	var props struct{}
	if err := decodeStrict(raw, &props); err != nil {
		return errors.New("GitHub properties must be an empty object")
	}
	return nil
}

func validateGitHubUsername(raw json.RawMessage) error {
	var props struct {
		Username string `json:"username"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Username) {
		return errors.New("invalid GitHub username properties")
	}
	return nil
}

func validateGitHubOrg(raw json.RawMessage) error {
	var props struct {
		Org string `json:"org"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Org) {
		return errors.New("invalid GitHub organization properties")
	}
	return nil
}

func validateGitHubMyRepos(raw json.RawMessage) error {
	var props struct {
		Visibility  string `json:"visibility"`
		Affiliation string `json:"affiliation"`
		Sort        string `json:"sort"`
	}
	if err := decodeStrict(raw, &props); err != nil || !oneOf(props.Visibility, "", "all", "public", "private") ||
		!oneOf(props.Sort, "", "created", "updated", "pushed", "full_name") || !githubAffiliation(props.Affiliation) {
		return errors.New("invalid GitHub list-my-repositories properties")
	}
	return nil
}

func validateGitHubCreateRepository(raw json.RawMessage) error {
	var props struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Private     bool   `json:"private"`
		AutoInit    bool   `json:"auto_init"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Name) || len(props.Description) > 1024 {
		return errors.New("invalid GitHub create-repository properties")
	}
	return nil
}

func validateGitHubCreateOrgRepository(raw json.RawMessage) error {
	var props struct {
		Org         string `json:"org"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Private     bool   `json:"private"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Org) || !githubName(props.Name) || len(props.Description) > 1024 {
		return errors.New("invalid GitHub create-organization-repository properties")
	}
	return nil
}

func validateGitHubUpdateRepository(raw json.RawMessage) error {
	var props struct {
		Owner         string `json:"owner"`
		Repo          string `json:"repo"`
		Name          string `json:"name"`
		Description   string `json:"description"`
		Private       bool   `json:"private"`
		DefaultBranch string `json:"default_branch"`
		HasIssues     bool   `json:"has_issues"`
		Archived      bool   `json:"archived"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		(props.Name != "" && !githubName(props.Name)) || len(props.Description) > 1024 ||
		(props.DefaultBranch != "" && !githubRef(props.DefaultBranch, 200)) {
		return errors.New("invalid GitHub update-repository properties")
	}
	return nil
}

func validateGitHubBranch(raw json.RawMessage) error {
	var props struct {
		Owner  string `json:"owner"`
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) || !githubRef(props.Branch, 200) {
		return errors.New("invalid GitHub branch properties")
	}
	return nil
}

func validateGitHubCreateBranch(raw json.RawMessage) error {
	var props struct {
		Owner         string `json:"owner"`
		Repo          string `json:"repo"`
		SourceBranch  string `json:"source_branch"`
		NewBranchName string `json:"new_branch_name"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!githubRef(props.SourceBranch, 200) || !githubRef(props.NewBranchName, 200) {
		return errors.New("invalid GitHub create-branch properties")
	}
	return nil
}

func validateGitHubReference(raw json.RawMessage) error {
	var props struct {
		Owner string `json:"owner"`
		Repo  string `json:"repo"`
		Ref   string `json:"ref"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) || !githubRef(props.Ref, 256) {
		return errors.New("invalid GitHub get-reference properties")
	}
	return nil
}

func validateGitHubCommitList(raw json.RawMessage) error {
	var props struct {
		Owner  string `json:"owner"`
		Repo   string `json:"repo"`
		SHA    string `json:"sha"`
		Path   string `json:"path"`
		Author string `json:"author"`
		Since  string `json:"since"`
		Until  string `json:"until"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		(props.SHA != "" && !githubRef(props.SHA, 200)) || (props.Path != "" && !githubRef(props.Path, 1024)) ||
		(props.Author != "" && !githubName(props.Author)) || !optionalRFC3339(props.Since) || !optionalRFC3339(props.Until) {
		return errors.New("invalid GitHub list-commits properties")
	}
	return nil
}

func validateGitHubCommit(raw json.RawMessage) error {
	var props struct {
		Owner string `json:"owner"`
		Repo  string `json:"repo"`
		Ref   string `json:"ref"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) || !githubRef(props.Ref, 200) {
		return errors.New("invalid GitHub get-commit properties")
	}
	return nil
}

func validateGitHubCompare(raw json.RawMessage) error {
	var props struct {
		Owner string `json:"owner"`
		Repo  string `json:"repo"`
		Base  string `json:"base"`
		Head  string `json:"head"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!githubRef(props.Base, 200) || !githubRef(props.Head, 200) {
		return errors.New("invalid GitHub compare-commits properties")
	}
	return nil
}

func validateGitHubTree(raw json.RawMessage) error {
	var props struct {
		Owner     string `json:"owner"`
		Repo      string `json:"repo"`
		TreeSHA   string `json:"tree_sha"`
		Recursive bool   `json:"recursive"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) || !githubRef(props.TreeSHA, 200) {
		return errors.New("invalid GitHub get-repository-tree properties")
	}
	return nil
}

func validateGitHubRepoSearch(raw json.RawMessage) error {
	var props struct {
		Q       string `json:"q"`
		Sort    string `json:"sort"`
		Order   string `json:"order"`
		PerPage int    `json:"per_page"`
		Page    int    `json:"page"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubSearchQuery(props.Q) ||
		!oneOf(props.Sort, "", "stars", "forks", "help-wanted-issues", "updated") ||
		!oneOf(props.Order, "", "desc", "asc") || !githubPage(props.PerPage, props.Page) {
		return errors.New("invalid GitHub search-repositories properties")
	}
	return nil
}

func validateGitHubIssueSearch(raw json.RawMessage) error {
	var props struct {
		Q       string `json:"q"`
		Sort    string `json:"sort"`
		Order   string `json:"order"`
		PerPage int    `json:"per_page"`
		Page    int    `json:"page"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubSearchQuery(props.Q) ||
		(props.Sort != "" && !githubQueryValue(props.Sort, 40)) || !oneOf(props.Order, "", "desc", "asc") ||
		!githubPage(props.PerPage, props.Page) {
		return errors.New("invalid GitHub search-issues properties")
	}
	return nil
}

func validateGitHubUserSearch(raw json.RawMessage) error {
	var props struct {
		Q       string `json:"q"`
		PerPage int    `json:"per_page"`
		Page    int    `json:"page"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubSearchQuery(props.Q) || !githubPage(props.PerPage, props.Page) {
		return errors.New("invalid GitHub search-users properties")
	}
	return nil
}

func validateGitHubCreateRelease(raw json.RawMessage) error {
	var props struct {
		Owner           string `json:"owner"`
		Repo            string `json:"repo"`
		TagName         string `json:"tag_name"`
		Name            string `json:"name"`
		Body            string `json:"body"`
		TargetCommitish string `json:"target_commitish"`
		Draft           bool   `json:"draft"`
		Prerelease      bool   `json:"prerelease"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!githubRef(props.TagName, 128) || len(props.Name) > 256 || len(props.Body) > 64*1024 ||
		(props.TargetCommitish != "" && !githubRef(props.TargetCommitish, 200)) {
		return errors.New("invalid GitHub create-release properties")
	}
	return nil
}

func validateGitHubReleaseID(raw json.RawMessage) error {
	var props struct {
		Owner     string `json:"owner"`
		Repo      string `json:"repo"`
		ReleaseID int64  `json:"release_id"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) || !githubPositiveID(props.ReleaseID) {
		return errors.New("invalid GitHub get-release properties")
	}
	return nil
}

func validateGitHubCollaborator(raw json.RawMessage) error {
	var props struct {
		Owner      string `json:"owner"`
		Repo       string `json:"repo"`
		Username   string `json:"username"`
		Permission string `json:"permission"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!githubName(props.Username) || !oneOf(props.Permission, "", "pull", "triage", "push", "maintain", "admin") {
		return errors.New("invalid GitHub add-collaborator properties")
	}
	return nil
}

func validateGitHubRemoveCollaborator(raw json.RawMessage) error {
	var props struct {
		Owner    string `json:"owner"`
		Repo     string `json:"repo"`
		Username string `json:"username"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) || !githubName(props.Username) {
		return errors.New("invalid GitHub remove-collaborator properties")
	}
	return nil
}

func validateGitHubCreateMilestone(raw json.RawMessage) error {
	var props struct {
		Owner       string `json:"owner"`
		Repo        string `json:"repo"`
		Title       string `json:"title"`
		Description string `json:"description"`
		State       string `json:"state"`
		DueOn       string `json:"due_on"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		strings.TrimSpace(props.Title) == "" || len(props.Title) > 256 || len(props.Description) > 64*1024 ||
		!oneOf(props.State, "", "open", "closed") || !optionalRFC3339(props.DueOn) {
		return errors.New("invalid GitHub create-milestone properties")
	}
	return nil
}

func validateGitHubMilestoneList(raw json.RawMessage) error {
	var props struct {
		Owner string `json:"owner"`
		Repo  string `json:"repo"`
		State string `json:"state"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!oneOf(props.State, "", "open", "closed", "all") {
		return errors.New("invalid GitHub list-milestones properties")
	}
	return nil
}

func validateGitHubMilestoneNumber(raw json.RawMessage) error {
	var props struct {
		Owner           string `json:"owner"`
		Repo            string `json:"repo"`
		MilestoneNumber int64  `json:"milestone_number"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) || !githubPositiveID(props.MilestoneNumber) {
		return errors.New("invalid GitHub milestone properties")
	}
	return nil
}

func validateGitHubUpdateMilestone(raw json.RawMessage) error {
	var props struct {
		Owner           string `json:"owner"`
		Repo            string `json:"repo"`
		MilestoneNumber int64  `json:"milestone_number"`
		Title           string `json:"title"`
		State           string `json:"state"`
		Description     string `json:"description"`
		DueOn           string `json:"due_on"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!githubPositiveID(props.MilestoneNumber) || len(props.Title) > 256 || len(props.Description) > 64*1024 ||
		!oneOf(props.State, "", "open", "closed") || !optionalRFC3339(props.DueOn) {
		return errors.New("invalid GitHub update-milestone properties")
	}
	return nil
}

func validateGitHubGetFile(raw json.RawMessage) error {
	var props struct {
		Owner string `json:"owner"`
		Repo  string `json:"repo"`
		Path  string `json:"path"`
		Ref   string `json:"ref"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!githubPath(props.Path) || (props.Ref != "" && !githubRef(props.Ref, 200)) {
		return errors.New("invalid GitHub get-file-content properties")
	}
	return nil
}

func validateGitHubCreateOrUpdateFile(raw json.RawMessage) error {
	var props struct {
		Owner   string  `json:"owner"`
		Repo    string  `json:"repo"`
		Path    string  `json:"path"`
		Message string  `json:"message"`
		File    fileRef `json:"file"`
		SHA     string  `json:"sha"`
		Branch  string  `json:"branch"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!githubPath(props.Path) || strings.TrimSpace(props.Message) == "" || len(props.Message) > 256 ||
		!oryxaFileID(props.File.FileID) || (props.SHA != "" && !githubRef(props.SHA, 64)) ||
		(props.Branch != "" && !githubRef(props.Branch, 200)) {
		return errors.New("invalid GitHub create-or-update-file properties")
	}
	return nil
}

func validateGitHubDeleteFile(raw json.RawMessage) error {
	var props struct {
		Owner   string `json:"owner"`
		Repo    string `json:"repo"`
		Path    string `json:"path"`
		Message string `json:"message"`
		SHA     string `json:"sha"`
		Branch  string `json:"branch"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!githubPath(props.Path) || strings.TrimSpace(props.Message) == "" || len(props.Message) > 256 ||
		!githubRef(props.SHA, 64) || (props.Branch != "" && !githubRef(props.Branch, 200)) {
		return errors.New("invalid GitHub delete-file properties")
	}
	return nil
}

func validateGitHubCreateGist(raw json.RawMessage) error {
	var props struct {
		Description string `json:"description"`
		Public      bool   `json:"public"`
		Filename    string `json:"filename"`
		Content     string `json:"content"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedFilename(props.Filename) ||
		strings.TrimSpace(props.Content) == "" || len(props.Content) > 64*1024 ||
		len(props.Description) > 1024 {
		return errors.New("invalid GitHub create-gist properties")
	}
	return nil
}

func validateGitHubGraphQL(raw json.RawMessage) error {
	var props struct {
		Query     string          `json:"query"`
		Variables json.RawMessage `json:"variables"`
	}
	if err := decodeStrict(raw, &props); err != nil || strings.TrimSpace(props.Query) == "" ||
		len(props.Query) > 16*1024 || !githubGraphQLVariables(props.Variables) {
		return errors.New("invalid GitHub raw-graphql-query properties")
	}
	return nil
}

func validateGitHubWorkflowDispatch(raw json.RawMessage) error {
	var props struct {
		Owner             string         `json:"owner"`
		Repo              string         `json:"repo"`
		WorkflowID        string         `json:"workflow_id"`
		Ref               string         `json:"ref"`
		Inputs            map[string]any `json:"inputs"`
		WaitForCompletion bool           `json:"wait_for_completion"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) || !githubName(props.Repo) ||
		!githubWorkflowID(props.WorkflowID) || !githubRef(props.Ref, 256) || props.WaitForCompletion ||
		!githubWorkflowInputs(props.Inputs) {
		return errors.New("invalid GitHub dispatch-workflow properties")
	}
	return nil
}

func validateGitHubCodeSearch(raw json.RawMessage) error {
	var props struct {
		Q       string `json:"q"`
		PerPage int    `json:"per_page"`
		Page    int    `json:"page"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubSearchQuery(props.Q) || !githubPage(props.PerPage, props.Page) {
		return errors.New("invalid GitHub search-code properties")
	}
	return nil
}

func prepareGitHubWorkflowDispatch(raw json.RawMessage) (json.RawMessage, error) {
	var props struct {
		Owner      string         `json:"owner"`
		Repo       string         `json:"repo"`
		WorkflowID string         `json:"workflow_id"`
		Ref        string         `json:"ref"`
		Inputs     map[string]any `json:"inputs"`
	}
	if err := json.Unmarshal(raw, &props); err != nil {
		return nil, err
	}
	prepared := map[string]any{
		"repository":        map[string]string{"owner": props.Owner, "repo": props.Repo},
		"workflow":          map[string]any{"id": props.WorkflowID},
		"ref":               props.Ref,
		"waitForCompletion": false,
	}
	if len(props.Inputs) > 0 {
		prepared["inputs"] = props.Inputs
	}
	return json.Marshal(prepared)
}

func prepareGitHubFileContent(raw json.RawMessage) (json.RawMessage, error) {
	var props map[string]any
	if err := json.Unmarshal(raw, &props); err != nil {
		return nil, err
	}
	file, _ := props["file"].(map[string]any)
	if file == nil {
		return nil, errors.New("invalid GitHub file")
	}
	delete(props, "file")
	props["content"] = file
	return json.Marshal(props)
}

func githubPath(value string) bool {
	return !strings.HasPrefix(value, "/") && githubRef(value, 4096)
}

func validateGitHubTrigger(raw json.RawMessage) error {
	var props struct {
		Scope string `json:"scope"`
		Owner string `json:"owner"`
		Repo  string `json:"repo"`
	}
	if err := decodeStrict(raw, &props); err != nil || !githubName(props.Owner) {
		return errors.New("invalid GitHub trigger properties")
	}
	switch props.Scope {
	case "repository":
		if !githubName(props.Repo) {
			return errors.New("invalid GitHub trigger properties")
		}
	case "organization":
		if props.Repo != "" {
			return errors.New("invalid GitHub trigger properties")
		}
	default:
		return errors.New("invalid GitHub trigger properties")
	}
	return nil
}

func githubPositiveID(value int64) bool {
	return value >= 1 && value <= 2_147_483_647
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

func githubRef(value string, maximum int) bool {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value ||
		strings.Contains(value, "..") || strings.ContainsAny(value, "\r\n\t\\ ") {
		return false
	}
	return true
}

func githubQueryValue(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n")
}

func githubSearchQuery(value string) bool {
	return githubQueryValue(value, 256)
}

func githubWorkflowID(value string) bool {
	return githubRef(value, 200)
}

func githubGraphQLVariables(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return true
	}
	if len(raw) > 16*1024 || len(bytesTrimSpace(raw)) == 0 || bytesTrimSpace(raw)[0] != '{' {
		return false
	}
	var object map[string]any
	return json.Unmarshal(raw, &object) == nil
}

func githubWorkflowInputs(values map[string]any) bool {
	if values == nil {
		return true
	}
	if len(values) > 20 {
		return false
	}
	for key, value := range values {
		if !githubQueryValue(key, 100) {
			return false
		}
		switch typed := value.(type) {
		case string:
			if len(typed) > 1024 || strings.ContainsAny(typed, "\r") {
				return false
			}
		case float64, bool, nil:
		default:
			return false
		}
	}
	return true
}

func bytesTrimSpace(raw json.RawMessage) json.RawMessage {
	return json.RawMessage(strings.TrimSpace(string(raw)))
}

func githubColor(value string) bool {
	if value == "" {
		return true
	}
	if len(value) != 6 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

func githubAffiliation(value string) bool {
	if value == "" {
		return true
	}
	for _, part := range strings.Split(value, ",") {
		if !oneOf(part, "owner", "collaborator", "organization_member") {
			return false
		}
	}
	return true
}

func githubPage(perPage, page int) bool {
	if perPage < 0 || perPage > 100 || page < 0 || page > 100 {
		return false
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
