package actionflow

import (
	"bytes"
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
	case "list-issues":
		return mapGitHubListed(request, githubProjectIssue)
	case "update-issue", "lock-issue", "unlock-issue", "add-labels-to-issue", "remove-label-from-issue",
		"set-issue-labels", "add-assignees-to-issue", "remove-assignees-from-issue":
		return mapGitHubIssueMutation(request)
	case "list-issue-comments":
		return mapGitHubListed(request, githubProjectComment)
	case "list-issue-labels", "list-repository-labels":
		return mapGitHubListed(request, githubProjectLabel)
	case "list-assignees", "list-organization-members", "list-collaborators", "list-contributors":
		return mapGitHubListed(request, githubProjectUser)
	case "create-label", "update-label":
		return mapGitHubLabelMutation(request)
	case "create-pull-request":
		return mapGitHubCreatedPullRequest(request)
	case "get-pull-request":
		return mapGitHubPullRequest(request)
	case "list-pull-requests":
		return mapGitHubListed(request, githubProjectPullRequest)
	case "update-pull-request", "merge-pull-request", "create-pull-request-review",
		"request-pull-request-reviewers", "create-review-comment":
		return mapGitHubPullRequestMutation(request)
	case "list-pull-request-files":
		return mapGitHubListed(request, githubProjectChangedFile)
	case "list-pull-request-commits", "list-commits":
		return mapGitHubListed(request, githubProjectCommit)
	case "list-pull-request-reviews":
		return mapGitHubListed(request, githubProjectReview)
	case "list-review-comments":
		return mapGitHubListed(request, githubProjectReviewComment)
	case "get-repository":
		return mapGitHubRepository(request, false)
	case "list-my-repositories", "list-user-repositories", "list-organization-repositories", "list-starred-repositories":
		return mapGitHubListed(request, githubProjectRepository)
	case "create-repository", "create-organization-repository":
		return mapGitHubRepository(request, true)
	case "update-repository", "add-collaborator", "remove-collaborator", "star-repository", "unstar-repository":
		return mapGitHubRepositoryMutation(request)
	case "list-branches":
		return mapGitHubListed(request, githubProjectBranch)
	case "get-branch":
		return mapGitHubBranch(request)
	case "create-branch", "delete-branch":
		return mapGitHubRefMutation(request)
	case "get-reference":
		return mapGitHubReference(request)
	case "get-commit":
		return mapGitHubCommit(request)
	case "compare-commits":
		return mapGitHubComparison(request)
	case "list-languages":
		return mapGitHubLanguages(request)
	case "list-tags":
		return mapGitHubListed(request, githubProjectTag)
	case "get-repository-tree":
		return mapGitHubTree(request)
	case "search-repositories":
		return mapGitHubSearch(request, githubProjectRepository)
	case "search-issues":
		return mapGitHubSearch(request, githubProjectIssue)
	case "search-users":
		return mapGitHubSearch(request, githubProjectUser)
	case "get-authenticated-user", "get-user":
		return mapGitHubUser(request)
	case "create-release":
		return mapGitHubRelease(request, true)
	case "list-releases":
		return mapGitHubListed(request, githubProjectRelease)
	case "get-release", "get-latest-release":
		return mapGitHubRelease(request, false)
	case "create-milestone":
		return mapGitHubMilestone(request, true)
	case "list-milestones":
		return mapGitHubListed(request, githubProjectMilestone)
	case "get-milestone":
		return mapGitHubMilestone(request, false)
	case "update-milestone":
		return mapGitHubMilestoneMutation(request)
	case "check-repository-starred":
		return mapGitHubStarCheck(request)
	case "get-file-content":
		return mapGitHubFileContent(request)
	case "create-or-update-file", "delete-file":
		return mapGitHubFileMutation(request)
	case "search-code":
		return mapGitHubSearch(request, githubProjectCode)
	case "create-gist":
		return mapGitHubGistCreated(request)
	case "raw-graphql-query", "custom-api-call":
		return mapProviderHTTPCall(request)
	case "dispatch-workflow":
		return mapGitHubWorkflowDispatched(request)
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
	result, err := json.Marshal(githubSafeIssue(output))
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
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
	result, err := json.Marshal(struct {
		CommentID int64  `json:"comment_id"`
		URL       string `json:"url"`
	}{CommentID: output.ID, URL: output.HTMLURL})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
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

func mapGitHubIssueMutation(request MapRequest) (MappedResult, error) {
	owner, repo, number, err := githubProps(request.Props)
	if err != nil || number < 1 {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	safe, url := githubIssueMutationResult(request.Output, number)
	result, err := json.Marshal(safe)
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GitHubIssueMutatedDetailType,
		Data: integrationevents.GitHubIssueMutated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			Repository: owner + "/" + repo, IssueNumber: number, URL: url,
			RequestedBy: request.Execution.MemberID, MutatedAt: request.OccurredAt,
		}}, nil
}

func githubIssueMutationResult(raw json.RawMessage, number int64) (any, string) {
	var issue githubIssueOutput
	if json.Unmarshal(raw, &issue) == nil && issue.Number >= 1 {
		return githubSafeIssue(issue), issue.HTMLURL
	}
	var labels []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &labels) == nil && labels != nil {
		names := make([]string, 0, len(labels))
		for _, label := range labels {
			if label.Name != "" {
				names = append(names, label.Name)
			}
		}
		return struct {
			Number int64    `json:"number"`
			Labels []string `json:"labels"`
		}{Number: number, Labels: names}, ""
	}
	return struct {
		Number  int64 `json:"number"`
		Success bool  `json:"success"`
	}{Number: number, Success: true}, ""
}

func mapGitHubCreatedPullRequest(request MapRequest) (MappedResult, error) {
	var output githubPullRequestOutput
	owner, repo, _, err := githubProps(request.Props)
	if err != nil || json.Unmarshal(request.Output, &output) != nil || output.Number < 1 || output.HTMLURL == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(githubSafePullRequest(output))
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GitHubPullRequestCreatedDetailType,
		Data: integrationevents.GitHubPullRequestCreated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			Repository: owner + "/" + repo, PullRequestNumber: output.Number, URL: output.HTMLURL,
			RequestedBy: request.Execution.MemberID, CreatedAt: request.OccurredAt,
		}}, nil
}

func mapGitHubPullRequest(request MapRequest) (MappedResult, error) {
	var output githubPullRequestOutput
	if json.Unmarshal(request.Output, &output) != nil || output.Number < 1 || output.HTMLURL == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(githubSafePullRequest(output))
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func mapGitHubPullRequestMutation(request MapRequest) (MappedResult, error) {
	owner, repo, number, err := githubProps(request.Props)
	if err != nil || number < 1 {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	safe, url := githubPullMutationResult(request.Output, number)
	result, err := json.Marshal(safe)
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GitHubPullRequestMutatedDetailType,
		Data: integrationevents.GitHubPullRequestMutated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			Repository: owner + "/" + repo, PullRequestNumber: number, URL: url,
			RequestedBy: request.Execution.MemberID, MutatedAt: request.OccurredAt,
		}}, nil
}

func githubPullMutationResult(raw json.RawMessage, number int64) (any, string) {
	var pull githubPullRequestOutput
	if json.Unmarshal(raw, &pull) == nil && pull.Number >= 1 {
		return githubSafePullRequest(pull), pull.HTMLURL
	}
	var merged struct {
		SHA     string `json:"sha"`
		Merged  bool   `json:"merged"`
		HTMLURL string `json:"html_url"`
	}
	if json.Unmarshal(raw, &merged) == nil && (merged.SHA != "" || merged.Merged) {
		return struct {
			Number int64  `json:"number"`
			SHA    string `json:"sha,omitempty"`
			Merged bool   `json:"merged"`
		}{Number: number, SHA: merged.SHA, Merged: merged.Merged}, merged.HTMLURL
	}
	var review struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
		State   string `json:"state"`
	}
	if json.Unmarshal(raw, &review) == nil && review.ID >= 1 {
		return struct {
			Number   int64  `json:"number"`
			ReviewID int64  `json:"review_id"`
			State    string `json:"state,omitempty"`
			URL      string `json:"url,omitempty"`
		}{Number: number, ReviewID: review.ID, State: review.State, URL: review.HTMLURL}, review.HTMLURL
	}
	return struct {
		Number  int64 `json:"number"`
		Success bool  `json:"success"`
	}{Number: number, Success: true}, ""
}

func mapGitHubRepository(request MapRequest, created bool) (MappedResult, error) {
	var output githubRepositoryOutput
	if json.Unmarshal(request.Output, &output) != nil || output.FullName == "" && (output.Name == "" || output.Owner.Login == "") {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	if output.FullName == "" {
		output.FullName = output.Owner.Login + "/" + output.Name
	}
	result, err := json.Marshal(githubSafeRepository(output))
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	if !created {
		return MappedResult{SafeResult: result}, nil
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GitHubRepositoryCreatedDetailType,
		Data: integrationevents.GitHubRepositoryCreated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			Repository: output.FullName, URL: output.HTMLURL,
			RequestedBy: request.Execution.MemberID, CreatedAt: request.OccurredAt,
		}}, nil
}

func mapGitHubRepositoryMutation(request MapRequest) (MappedResult, error) {
	owner, repo, _, err := githubProps(request.Props)
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	repository := owner + "/" + repo
	url := ""
	var output githubRepositoryOutput
	if json.Unmarshal(request.Output, &output) == nil && (output.FullName != "" || output.HTMLURL != "") {
		if output.FullName != "" {
			repository = output.FullName
		}
		url = output.HTMLURL
		result, marshalErr := json.Marshal(githubSafeRepository(output))
		if marshalErr != nil {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		return MappedResult{SafeResult: result, DetailType: integrationevents.GitHubRepositoryMutatedDetailType,
			Data: integrationevents.GitHubRepositoryMutated{
				ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, Repository: repository, URL: url,
				RequestedBy: request.Execution.MemberID, MutatedAt: request.OccurredAt,
			}}, nil
	}
	result, marshalErr := json.Marshal(struct {
		Repository string `json:"repository"`
		Success    bool   `json:"success"`
	}{Repository: repository, Success: true})
	if marshalErr != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GitHubRepositoryMutatedDetailType,
		Data: integrationevents.GitHubRepositoryMutated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, Repository: repository,
			RequestedBy: request.Execution.MemberID, MutatedAt: request.OccurredAt,
		}}, nil
}

func mapGitHubBranch(request MapRequest) (MappedResult, error) {
	mapped, err := githubProjectBranch(request.Output)
	if err != nil {
		return MappedResult{}, err
	}
	result, err := json.Marshal(mapped)
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func mapGitHubRefMutation(request MapRequest) (MappedResult, error) {
	owner, repo, _, err := githubProps(request.Props)
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	var props struct {
		Branch        string `json:"branch"`
		NewBranchName string `json:"new_branch_name"`
	}
	_ = json.Unmarshal(request.Props, &props)
	ref := props.NewBranchName
	if ref == "" {
		ref = props.Branch
	}
	sha := ""
	var created struct {
		Ref    string `json:"ref"`
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if json.Unmarshal(request.Output, &created) == nil && created.Ref != "" {
		ref = created.Ref
		sha = created.Object.SHA
	}
	if ref == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		Ref string `json:"ref"`
		SHA string `json:"sha,omitempty"`
	}{Ref: ref, SHA: sha})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GitHubRefMutatedDetailType,
		Data: integrationevents.GitHubRefMutated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			Repository: owner + "/" + repo, Ref: ref, SHA: sha,
			RequestedBy: request.Execution.MemberID, MutatedAt: request.OccurredAt,
		}}, nil
}

func mapGitHubReference(request MapRequest) (MappedResult, error) {
	var output struct {
		Ref    string `json:"ref"`
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}
	if json.Unmarshal(request.Output, &output) != nil || output.Ref == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha,omitempty"`
		Type string `json:"type,omitempty"`
	}{Ref: output.Ref, SHA: output.Object.SHA, Type: output.Object.Type})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func mapGitHubCommit(request MapRequest) (MappedResult, error) {
	mapped, err := githubProjectCommit(request.Output)
	if err != nil {
		return MappedResult{}, err
	}
	result, err := json.Marshal(mapped)
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func mapGitHubComparison(request MapRequest) (MappedResult, error) {
	var output struct {
		Status       string            `json:"status"`
		AheadBy      int               `json:"ahead_by"`
		BehindBy     int               `json:"behind_by"`
		TotalCommits int               `json:"total_commits"`
		HTMLURL      string            `json:"html_url"`
		Commits      []json.RawMessage `json:"commits"`
		Files        []json.RawMessage `json:"files"`
	}
	if json.Unmarshal(request.Output, &output) != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	commits := githubProjectMany(output.Commits, githubProjectCommit)
	files := githubProjectMany(output.Files, githubProjectChangedFile)
	if commits == nil || files == nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		Status       string `json:"status,omitempty"`
		AheadBy      int    `json:"ahead_by"`
		BehindBy     int    `json:"behind_by"`
		TotalCommits int    `json:"total_commits"`
		URL          string `json:"url,omitempty"`
		Commits      []any  `json:"commits"`
		Files        []any  `json:"files"`
		Truncated    bool   `json:"truncated"`
	}{Status: output.Status, AheadBy: output.AheadBy, BehindBy: output.BehindBy, TotalCommits: output.TotalCommits,
		URL: output.HTMLURL, Commits: commits, Files: files,
		Truncated: len(output.Commits) > 100 || len(output.Files) > 100})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func mapGitHubLanguages(request MapRequest) (MappedResult, error) {
	var languages map[string]int64
	if json.Unmarshal(request.Output, &languages) != nil || languages == nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	if len(languages) > 50 {
		trimmed := make(map[string]int64, 50)
		count := 0
		for name, bytes := range languages {
			if count == 50 {
				break
			}
			trimmed[name] = bytes
			count++
		}
		languages = trimmed
	}
	result, err := json.Marshal(struct {
		Languages map[string]int64 `json:"languages"`
	}{Languages: languages})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func mapGitHubTree(request MapRequest) (MappedResult, error) {
	var output struct {
		SHA       string `json:"sha"`
		Truncated bool   `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
			Size int64  `json:"size"`
		} `json:"tree"`
	}
	if json.Unmarshal(request.Output, &output) != nil || output.Tree == nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	truncated := output.Truncated || len(output.Tree) > 100
	if len(output.Tree) > 100 {
		output.Tree = output.Tree[:100]
	}
	entries := make([]any, 0, len(output.Tree))
	for _, entry := range output.Tree {
		if entry.Path == "" {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		entries = append(entries, struct {
			Path string `json:"path"`
			Type string `json:"type,omitempty"`
			SHA  string `json:"sha,omitempty"`
			Size int64  `json:"size,omitempty"`
		}{Path: entry.Path, Type: entry.Type, SHA: entry.SHA, Size: entry.Size})
	}
	result, err := json.Marshal(struct {
		SHA       string `json:"sha,omitempty"`
		Entries   []any  `json:"entries"`
		Truncated bool   `json:"truncated"`
	}{SHA: output.SHA, Entries: entries, Truncated: truncated})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func mapGitHubUser(request MapRequest) (MappedResult, error) {
	mapped, err := githubProjectUser(request.Output)
	if err != nil {
		return MappedResult{}, err
	}
	result, err := json.Marshal(mapped)
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func mapGitHubRelease(request MapRequest, created bool) (MappedResult, error) {
	mapped, err := githubProjectRelease(request.Output)
	if err != nil {
		return MappedResult{}, err
	}
	result, err := json.Marshal(mapped)
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	if !created {
		return MappedResult{SafeResult: result}, nil
	}
	owner, repo, _, propErr := githubProps(request.Props)
	var release struct {
		ID      int64  `json:"id"`
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if propErr != nil || json.Unmarshal(request.Output, &release) != nil || release.ID < 1 {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GitHubReleaseCreatedDetailType,
		Data: integrationevents.GitHubReleaseCreated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			Repository: owner + "/" + repo, ReleaseID: release.ID, Tag: release.TagName, URL: release.HTMLURL,
			RequestedBy: request.Execution.MemberID, CreatedAt: request.OccurredAt,
		}}, nil
}

func mapGitHubMilestone(request MapRequest, created bool) (MappedResult, error) {
	mapped, err := githubProjectMilestone(request.Output)
	if err != nil {
		return MappedResult{}, err
	}
	result, err := json.Marshal(mapped)
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	if !created {
		return MappedResult{SafeResult: result}, nil
	}
	return mapGitHubMilestoneMutation(request)
}

func mapGitHubMilestoneMutation(request MapRequest) (MappedResult, error) {
	owner, repo, number, err := githubProps(request.Props)
	var output struct {
		Number  int64  `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if json.Unmarshal(request.Output, &output) != nil || output.Number < 1 {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	if number == 0 {
		number = output.Number
	}
	mapped, mapErr := githubProjectMilestone(request.Output)
	if mapErr != nil {
		return MappedResult{}, mapErr
	}
	result, marshalErr := json.Marshal(mapped)
	if marshalErr != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GitHubMilestoneMutatedDetailType,
		Data: integrationevents.GitHubMilestoneMutated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			Repository: owner + "/" + repo, MilestoneNumber: number, URL: output.HTMLURL,
			RequestedBy: request.Execution.MemberID, MutatedAt: request.OccurredAt,
		}}, nil
}

func mapGitHubLabelMutation(request MapRequest) (MappedResult, error) {
	owner, repo, _, err := githubProps(request.Props)
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	mapped, mapErr := githubProjectLabel(request.Output)
	if mapErr != nil {
		return MappedResult{}, mapErr
	}
	var output struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(request.Output, &output)
	if output.Name == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, marshalErr := json.Marshal(mapped)
	if marshalErr != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GitHubLabelMutatedDetailType,
		Data: integrationevents.GitHubLabelMutated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			Repository: owner + "/" + repo, Name: output.Name,
			RequestedBy: request.Execution.MemberID, MutatedAt: request.OccurredAt,
		}}, nil
}

func mapGitHubStarCheck(request MapRequest) (MappedResult, error) {
	var output struct {
		Starred bool `json:"starred"`
	}
	if json.Unmarshal(request.Output, &output) != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(output)
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func mapGitHubFileContent(request MapRequest) (MappedResult, error) {
	trimmed := bytesTrim(request.Output)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var items []json.RawMessage
		if json.Unmarshal(request.Output, &items) != nil || items == nil {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		truncated := len(items) > 100
		if truncated {
			items = items[:100]
		}
		safe := githubProjectMany(items, githubProjectContentEntry)
		if safe == nil {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		result, err := json.Marshal(struct {
			Items     []any `json:"items"`
			Count     int   `json:"count"`
			Truncated bool  `json:"truncated"`
		}{Items: safe, Count: len(safe), Truncated: truncated})
		if err != nil {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		return MappedResult{SafeResult: result}, nil
	}
	mapped, err := githubProjectContentEntry(request.Output)
	if err != nil {
		return MappedResult{}, err
	}
	result, err := json.Marshal(mapped)
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func mapGitHubFileMutation(request MapRequest) (MappedResult, error) {
	owner, repo, _, err := githubProps(request.Props)
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	var props struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(request.Props, &props)
	var output struct {
		Content *struct {
			Path    string `json:"path"`
			SHA     string `json:"sha"`
			Size    int64  `json:"size"`
			HTMLURL string `json:"html_url"`
			Type    string `json:"type"`
		} `json:"content"`
		Commit struct {
			SHA     string `json:"sha"`
			HTMLURL string `json:"html_url"`
		} `json:"commit"`
	}
	if json.Unmarshal(request.Output, &output) != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	path := props.Path
	sha := output.Commit.SHA
	url := output.Commit.HTMLURL
	size := int64(0)
	kind := ""
	if output.Content != nil {
		if output.Content.Path != "" {
			path = output.Content.Path
		}
		if output.Content.SHA != "" {
			sha = output.Content.SHA
		}
		if output.Content.HTMLURL != "" {
			url = output.Content.HTMLURL
		}
		size = output.Content.Size
		kind = output.Content.Type
	}
	if path == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		Path string `json:"path"`
		SHA  string `json:"sha,omitempty"`
		Size int64  `json:"size,omitempty"`
		URL  string `json:"url,omitempty"`
		Type string `json:"type,omitempty"`
	}{Path: path, SHA: sha, Size: size, URL: url, Type: kind})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GitHubFileMutatedDetailType,
		Data: integrationevents.GitHubFileMutated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			Repository: owner + "/" + repo, Path: path, SHA: sha, URL: url,
			RequestedBy: request.Execution.MemberID, MutatedAt: request.OccurredAt,
		}}, nil
}

func mapGitHubGistCreated(request MapRequest) (MappedResult, error) {
	payload := unwrapHTTPBody(request.Output)
	var output struct {
		ID      string `json:"id"`
		HTMLURL string `json:"html_url"`
		Public  bool   `json:"public"`
		Files   map[string]struct {
			Filename string `json:"filename"`
			Type     string `json:"type"`
			Language string `json:"language"`
			Size     int64  `json:"size"`
		} `json:"files"`
	}
	if json.Unmarshal(payload, &output) != nil || output.ID == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	files := make(map[string]any, len(output.Files))
	for name, file := range output.Files {
		filename := file.Filename
		if filename == "" {
			filename = name
		}
		files[name] = struct {
			Filename string `json:"filename"`
			Type     string `json:"type,omitempty"`
			Language string `json:"language,omitempty"`
			Size     int64  `json:"size,omitempty"`
		}{Filename: filename, Type: file.Type, Language: file.Language, Size: file.Size}
	}
	result, err := json.Marshal(struct {
		ID     string         `json:"id"`
		URL    string         `json:"url,omitempty"`
		Public bool           `json:"public"`
		Files  map[string]any `json:"files,omitempty"`
	}{ID: output.ID, URL: output.HTMLURL, Public: output.Public, Files: files})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GitHubGistCreatedDetailType,
		Data: integrationevents.GitHubGistCreated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, GistID: output.ID,
			URL: output.HTMLURL, RequestedBy: request.Execution.MemberID, CreatedAt: request.OccurredAt,
		}}, nil
}

func mapGitHubWorkflowDispatched(request MapRequest) (MappedResult, error) {
	payload := unwrapHTTPBody(request.Output)
	var output struct {
		ID           int64  `json:"id"`
		Status       string `json:"status"`
		Conclusion   string `json:"conclusion"`
		HTMLURL      string `json:"html_url"`
		DisplayTitle string `json:"display_title"`
		Repository   struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if json.Unmarshal(payload, &output) != nil || output.ID < 1 {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	var props struct {
		Owner      string `json:"owner"`
		Repo       string `json:"repo"`
		WorkflowID string `json:"workflow_id"`
		Ref        string `json:"ref"`
	}
	_ = json.Unmarshal(request.Props, &props)
	repository := output.Repository.FullName
	if repository == "" && props.Owner != "" && props.Repo != "" {
		repository = props.Owner + "/" + props.Repo
	}
	if repository == "" || props.WorkflowID == "" || props.Ref == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		ID         int64  `json:"id"`
		Status     string `json:"status,omitempty"`
		Conclusion string `json:"conclusion,omitempty"`
		URL        string `json:"url,omitempty"`
		Repository string `json:"repository"`
		WorkflowID string `json:"workflow_id"`
		Ref        string `json:"ref"`
	}{ID: output.ID, Status: output.Status, Conclusion: output.Conclusion, URL: output.HTMLURL,
		Repository: repository, WorkflowID: props.WorkflowID, Ref: props.Ref})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GitHubWorkflowDispatchedDetailType,
		Data: integrationevents.GitHubWorkflowDispatched{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			Repository: repository, WorkflowID: props.WorkflowID, Ref: props.Ref, RunID: output.ID,
			URL: output.HTMLURL, RequestedBy: request.Execution.MemberID, DispatchedAt: request.OccurredAt,
		}}, nil
}

func githubProjectContentEntry(raw json.RawMessage) (any, error) {
	var output struct {
		Name     string `json:"name"`
		Path     string `json:"path"`
		SHA      string `json:"sha"`
		Size     int64  `json:"size"`
		HTMLURL  string `json:"html_url"`
		Type     string `json:"type"`
		FileID   string `json:"file_id"`
		Filename string `json:"filename"`
		Content  string `json:"content"`
	}
	if json.Unmarshal(raw, &output) != nil || (output.Path == "" && output.Name == "") {
		return nil, ErrPieceOutputInvalid
	}
	if output.Content != "" {
		return nil, ErrPieceOutputInvalid
	}
	path := output.Path
	if path == "" {
		path = output.Name
	}
	filename := output.Filename
	if filename == "" {
		filename = output.Name
	}
	return struct {
		Path     string `json:"path"`
		SHA      string `json:"sha,omitempty"`
		Size     int64  `json:"size,omitempty"`
		URL      string `json:"url,omitempty"`
		Type     string `json:"type,omitempty"`
		FileID   string `json:"file_id,omitempty"`
		Filename string `json:"filename,omitempty"`
	}{Path: path, SHA: output.SHA, Size: output.Size, URL: output.HTMLURL, Type: output.Type,
		FileID: output.FileID, Filename: filename}, nil
}

func githubProjectCode(raw json.RawMessage) (any, error) {
	var output struct {
		Name       string `json:"name"`
		Path       string `json:"path"`
		SHA        string `json:"sha"`
		HTMLURL    string `json:"html_url"`
		Repository struct {
			FullName string `json:"full_name"`
			HTMLURL  string `json:"html_url"`
		} `json:"repository"`
	}
	if json.Unmarshal(raw, &output) != nil || output.Path == "" {
		return nil, ErrPieceOutputInvalid
	}
	return struct {
		Name       string `json:"name,omitempty"`
		Path       string `json:"path"`
		SHA        string `json:"sha,omitempty"`
		URL        string `json:"url,omitempty"`
		Repository string `json:"repository,omitempty"`
	}{Name: output.Name, Path: output.Path, SHA: output.SHA, URL: output.HTMLURL,
		Repository: output.Repository.FullName}, nil
}

func bytesTrim(raw json.RawMessage) json.RawMessage {
	return bytes.TrimSpace(raw)
}

func mapGitHubListed(request MapRequest, project func(json.RawMessage) (any, error)) (MappedResult, error) {
	var output struct {
		Items []json.RawMessage `json:"items"`
		Count int               `json:"count"`
	}
	if json.Unmarshal(request.Output, &output) != nil || output.Items == nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	truncated := len(output.Items) > 100
	if truncated {
		output.Items = output.Items[:100]
	}
	items := githubProjectMany(output.Items, project)
	if items == nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	count := output.Count
	if count == 0 {
		count = len(items)
	}
	result, err := json.Marshal(struct {
		Items     []any `json:"items"`
		Count     int   `json:"count"`
		Truncated bool  `json:"truncated"`
	}{Items: items, Count: count, Truncated: truncated})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func mapGitHubSearch(request MapRequest, project func(json.RawMessage) (any, error)) (MappedResult, error) {
	var output struct {
		TotalCount int               `json:"total_count"`
		Items      []json.RawMessage `json:"items"`
	}
	if json.Unmarshal(request.Output, &output) != nil || output.Items == nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	truncated := len(output.Items) > 100
	if truncated {
		output.Items = output.Items[:100]
	}
	items := githubProjectMany(output.Items, project)
	if items == nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		Items      []any `json:"items"`
		TotalCount int   `json:"total_count"`
		Truncated  bool  `json:"truncated"`
	}{Items: items, TotalCount: output.TotalCount, Truncated: truncated})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func githubProjectMany(items []json.RawMessage, project func(json.RawMessage) (any, error)) []any {
	if items == nil {
		return nil
	}
	if len(items) > 100 {
		items = items[:100]
	}
	safe := make([]any, 0, len(items))
	for _, item := range items {
		mapped, err := project(item)
		if err != nil {
			return nil
		}
		safe = append(safe, mapped)
	}
	return safe
}

func githubProjectIssue(raw json.RawMessage) (any, error) {
	var output githubIssueOutput
	if json.Unmarshal(raw, &output) != nil || output.Number < 1 || output.HTMLURL == "" {
		return nil, ErrPieceOutputInvalid
	}
	return githubSafeIssue(output), nil
}

type githubPullRequestOutput struct {
	Number  int64  `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
	Draft   bool   `json:"draft"`
	Merged  bool   `json:"merged"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func githubProjectPullRequest(raw json.RawMessage) (any, error) {
	var output githubPullRequestOutput
	if json.Unmarshal(raw, &output) != nil || output.Number < 1 || output.HTMLURL == "" {
		return nil, ErrPieceOutputInvalid
	}
	return githubSafePullRequest(output), nil
}

func githubProjectComment(raw json.RawMessage) (any, error) {
	var output struct {
		ID        int64  `json:"id"`
		HTMLURL   string `json:"html_url"`
		CreatedAt string `json:"created_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if json.Unmarshal(raw, &output) != nil || output.ID < 1 {
		return nil, ErrPieceOutputInvalid
	}
	return struct {
		ID        int64  `json:"id"`
		URL       string `json:"url,omitempty"`
		Author    string `json:"author,omitempty"`
		CreatedAt string `json:"created_at,omitempty"`
	}{ID: output.ID, URL: output.HTMLURL, Author: output.User.Login, CreatedAt: output.CreatedAt}, nil
}

func githubProjectLabel(raw json.RawMessage) (any, error) {
	var output struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if json.Unmarshal(raw, &output) != nil || output.Name == "" {
		return nil, ErrPieceOutputInvalid
	}
	return struct {
		Name  string `json:"name"`
		Color string `json:"color,omitempty"`
	}{Name: output.Name, Color: output.Color}, nil
}

func githubProjectUser(raw json.RawMessage) (any, error) {
	var output struct {
		Login   string `json:"login"`
		HTMLURL string `json:"html_url"`
		Type    string `json:"type"`
		ID      int64  `json:"id"`
	}
	if json.Unmarshal(raw, &output) != nil || output.Login == "" {
		return nil, ErrPieceOutputInvalid
	}
	return struct {
		Login string `json:"login"`
		URL   string `json:"url,omitempty"`
		Type  string `json:"type,omitempty"`
		ID    int64  `json:"id,omitempty"`
	}{Login: output.Login, URL: output.HTMLURL, Type: output.Type, ID: output.ID}, nil
}

type githubRepositoryOutput struct {
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	HTMLURL       string `json:"html_url"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	Language      string `json:"language"`
	Archived      bool   `json:"archived"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

func githubProjectRepository(raw json.RawMessage) (any, error) {
	var output githubRepositoryOutput
	if json.Unmarshal(raw, &output) != nil {
		return nil, ErrPieceOutputInvalid
	}
	if output.FullName == "" && output.Name != "" && output.Owner.Login != "" {
		output.FullName = output.Owner.Login + "/" + output.Name
	}
	if output.FullName == "" {
		return nil, ErrPieceOutputInvalid
	}
	return githubSafeRepository(output), nil
}

func githubProjectBranch(raw json.RawMessage) (any, error) {
	var output struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
		Protected bool `json:"protected"`
	}
	if json.Unmarshal(raw, &output) != nil || output.Name == "" {
		return nil, ErrPieceOutputInvalid
	}
	return struct {
		Name      string `json:"name"`
		SHA       string `json:"sha,omitempty"`
		Protected bool   `json:"protected"`
	}{Name: output.Name, SHA: output.Commit.SHA, Protected: output.Protected}, nil
}

func githubProjectCommit(raw json.RawMessage) (any, error) {
	var output struct {
		SHA     string `json:"sha"`
		HTMLURL string `json:"html_url"`
		Commit  struct {
			Message string `json:"message"`
		} `json:"commit"`
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		Stats struct {
			Additions int `json:"additions"`
			Deletions int `json:"deletions"`
			Total     int `json:"total"`
		} `json:"stats"`
		Files []json.RawMessage `json:"files"`
	}
	if json.Unmarshal(raw, &output) != nil || output.SHA == "" {
		return nil, ErrPieceOutputInvalid
	}
	files := githubProjectMany(output.Files, githubProjectChangedFile)
	if output.Files != nil && files == nil {
		return nil, ErrPieceOutputInvalid
	}
	return struct {
		SHA       string `json:"sha"`
		URL       string `json:"url,omitempty"`
		Message   string `json:"message,omitempty"`
		Author    string `json:"author,omitempty"`
		Additions int    `json:"additions,omitempty"`
		Deletions int    `json:"deletions,omitempty"`
		Total     int    `json:"total,omitempty"`
		Files     []any  `json:"files,omitempty"`
		Truncated bool   `json:"truncated,omitempty"`
	}{SHA: output.SHA, URL: output.HTMLURL, Message: output.Commit.Message, Author: output.Author.Login,
		Additions: output.Stats.Additions, Deletions: output.Stats.Deletions, Total: output.Stats.Total,
		Files: files, Truncated: len(output.Files) > 100}, nil
}

func githubProjectChangedFile(raw json.RawMessage) (any, error) {
	var output struct {
		Filename  string `json:"filename"`
		Status    string `json:"status"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
		Changes   int    `json:"changes"`
	}
	if json.Unmarshal(raw, &output) != nil || output.Filename == "" {
		return nil, ErrPieceOutputInvalid
	}
	return struct {
		Filename  string `json:"filename"`
		Status    string `json:"status,omitempty"`
		Additions int    `json:"additions,omitempty"`
		Deletions int    `json:"deletions,omitempty"`
		Changes   int    `json:"changes,omitempty"`
	}{Filename: output.Filename, Status: output.Status, Additions: output.Additions, Deletions: output.Deletions,
		Changes: output.Changes}, nil
}

func githubProjectReview(raw json.RawMessage) (any, error) {
	var output struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
		State   string `json:"state"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if json.Unmarshal(raw, &output) != nil || output.ID < 1 {
		return nil, ErrPieceOutputInvalid
	}
	return struct {
		ID     int64  `json:"id"`
		URL    string `json:"url,omitempty"`
		State  string `json:"state,omitempty"`
		Author string `json:"author,omitempty"`
	}{ID: output.ID, URL: output.HTMLURL, State: output.State, Author: output.User.Login}, nil
}

func githubProjectReviewComment(raw json.RawMessage) (any, error) {
	var output struct {
		ID       int64  `json:"id"`
		HTMLURL  string `json:"html_url"`
		Path     string `json:"path"`
		Position int64  `json:"position"`
		User     struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if json.Unmarshal(raw, &output) != nil || output.ID < 1 {
		return nil, ErrPieceOutputInvalid
	}
	return struct {
		ID       int64  `json:"id"`
		URL      string `json:"url,omitempty"`
		Path     string `json:"path,omitempty"`
		Position int64  `json:"position,omitempty"`
		Author   string `json:"author,omitempty"`
	}{ID: output.ID, URL: output.HTMLURL, Path: output.Path, Position: output.Position, Author: output.User.Login}, nil
}

func githubProjectTag(raw json.RawMessage) (any, error) {
	var output struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if json.Unmarshal(raw, &output) != nil || output.Name == "" {
		return nil, ErrPieceOutputInvalid
	}
	return struct {
		Name string `json:"name"`
		SHA  string `json:"sha,omitempty"`
	}{Name: output.Name, SHA: output.Commit.SHA}, nil
}

func githubProjectRelease(raw json.RawMessage) (any, error) {
	var output struct {
		ID         int64  `json:"id"`
		TagName    string `json:"tag_name"`
		Name       string `json:"name"`
		HTMLURL    string `json:"html_url"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if json.Unmarshal(raw, &output) != nil || output.ID < 1 {
		return nil, ErrPieceOutputInvalid
	}
	return struct {
		ID         int64  `json:"id"`
		Tag        string `json:"tag,omitempty"`
		Name       string `json:"name,omitempty"`
		URL        string `json:"url,omitempty"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}{ID: output.ID, Tag: output.TagName, Name: output.Name, URL: output.HTMLURL, Draft: output.Draft,
		Prerelease: output.Prerelease}, nil
}

func githubProjectMilestone(raw json.RawMessage) (any, error) {
	var output struct {
		Number       int64  `json:"number"`
		Title        string `json:"title"`
		State        string `json:"state"`
		HTMLURL      string `json:"html_url"`
		OpenIssues   int    `json:"open_issues"`
		ClosedIssues int    `json:"closed_issues"`
	}
	if json.Unmarshal(raw, &output) != nil || output.Number < 1 {
		return nil, ErrPieceOutputInvalid
	}
	return struct {
		Number       int64  `json:"number"`
		Title        string `json:"title,omitempty"`
		State        string `json:"state,omitempty"`
		URL          string `json:"url,omitempty"`
		OpenIssues   int    `json:"open_issues,omitempty"`
		ClosedIssues int    `json:"closed_issues,omitempty"`
	}{Number: output.Number, Title: output.Title, State: output.State, URL: output.HTMLURL,
		OpenIssues: output.OpenIssues, ClosedIssues: output.ClosedIssues}, nil
}

func githubProps(raw json.RawMessage) (owner, repo string, number int64, err error) {
	var props struct {
		Owner           string `json:"owner"`
		Repo            string `json:"repo"`
		Org             string `json:"org"`
		IssueNumber     int64  `json:"issue_number"`
		PullNumber      int64  `json:"pull_number"`
		MilestoneNumber int64  `json:"milestone_number"`
		ReleaseID       int64  `json:"release_id"`
	}
	if json.Unmarshal(raw, &props) != nil {
		return "", "", 0, ErrPieceOutputInvalid
	}
	if props.Owner == "" {
		props.Owner = props.Org
	}
	if props.Owner == "" {
		return "", "", 0, ErrPieceOutputInvalid
	}
	number = props.IssueNumber
	if number == 0 {
		number = props.PullNumber
	}
	if number == 0 {
		number = props.MilestoneNumber
	}
	if number == 0 {
		number = props.ReleaseID
	}
	return props.Owner, props.Repo, number, nil
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

func githubSafePullRequest(output githubPullRequestOutput) any {
	return struct {
		Number int64  `json:"number"`
		Title  string `json:"title"`
		State  string `json:"state"`
		URL    string `json:"url"`
		Draft  bool   `json:"draft"`
		Merged bool   `json:"merged"`
		Author string `json:"author,omitempty"`
		Head   string `json:"head,omitempty"`
		Base   string `json:"base,omitempty"`
	}{Number: output.Number, Title: output.Title, State: output.State, URL: output.HTMLURL,
		Draft: output.Draft, Merged: output.Merged, Author: output.User.Login, Head: output.Head.Ref, Base: output.Base.Ref}
}

func githubSafeRepository(output githubRepositoryOutput) any {
	return struct {
		FullName      string `json:"full_name"`
		URL           string `json:"url,omitempty"`
		Private       bool   `json:"private"`
		DefaultBranch string `json:"default_branch,omitempty"`
		Language      string `json:"language,omitempty"`
		Archived      bool   `json:"archived"`
	}{FullName: output.FullName, URL: output.HTMLURL, Private: output.Private,
		DefaultBranch: output.DefaultBranch, Language: output.Language, Archived: output.Archived}
}
