package actionflow

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

func TestGitHubMapperProducesDomainEventsWithoutIssueContent(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	request := MapRequest{Execution: domain.ActionExecution{ID: "act-1", MemberID: "member-1"},
		Connection: domain.Connection{ID: "connection-1", Provider: "github"}, ActionName: "create-issue",
		Props:      json.RawMessage(`{"owner":"oryxa","repo":"api","title":"private title","body":"private body"}`),
		Output:     json.RawMessage(`{"number":42,"title":"private title","body":"private body","html_url":"https://github.com/oryxa/api/issues/42","state":"open","user":{"login":"octocat"},"token":"secret"}`),
		OccurredAt: now}
	mapped, err := (DomainMapper{}).Map(request)
	if err != nil {
		t.Fatal(err)
	}
	if mapped.DetailType != integrationevents.GitHubIssueCreatedDetailType {
		t.Fatalf("detail type = %s", mapped.DetailType)
	}
	event := mapped.Data.(integrationevents.GitHubIssueCreated)
	if event.Repository != "oryxa/api" || event.IssueNumber != 42 || event.RequestedBy != "member-1" {
		t.Fatalf("event = %+v", event)
	}
	for _, forbidden := range []string{"private body", "secret", "token"} {
		if strings.Contains(string(mapped.SafeResult), forbidden) {
			t.Fatalf("safe result leaked %q: %s", forbidden, mapped.SafeResult)
		}
	}
}

func TestGitHubPullRequestMapperMatchesPinnedPieceEnvelope(t *testing.T) {
	mapped, err := (DomainMapper{}).Map(MapRequest{
		Connection: domain.Connection{Provider: "github"}, ActionName: "list-pull-requests",
		Output: json.RawMessage(`{"items":[{"number":7,"title":"Ready","state":"open","html_url":"https://github.com/oryxa/api/pull/7","draft":false,"user":{"login":"member"}}],"count":1}`),
	})
	if err != nil || !strings.Contains(string(mapped.SafeResult), `"count":1`) || !strings.Contains(string(mapped.SafeResult), `"number":7`) {
		t.Fatalf("mapped/error = %s / %v", mapped.SafeResult, err)
	}
}

func TestGitHubMappersStripBodiesPatchesAndTokens(t *testing.T) {
	t.Parallel()
	tests := []struct {
		action     string
		props      string
		output     string
		detailType string
	}{
		{action: "create-pull-request", props: `{"owner":"oryxa","repo":"api"}`, output: `{"number":8,"title":"Ready","body":"private body","html_url":"https://github.com/oryxa/api/pull/8","state":"open","user":{"login":"member"}}`, detailType: integrationevents.GitHubPullRequestCreatedDetailType},
		{action: "update-issue", props: `{"owner":"oryxa","repo":"api","issue_number":42}`, output: `{"number":42,"title":"Ready","body":"private body","html_url":"https://github.com/oryxa/api/issues/42","state":"closed"}`, detailType: integrationevents.GitHubIssueMutatedDetailType},
		{action: "get-commit", props: `{"owner":"oryxa","repo":"api","ref":"abc"}`, output: `{"sha":"abc1234","html_url":"https://github.com/oryxa/api/commit/abc1234","commit":{"message":"fix"},"files":[{"filename":"a.go","status":"modified","patch":"private body","raw_url":"secret-url"}]}`},
		{action: "list-pull-request-files", output: `{"items":[{"filename":"a.go","status":"modified","additions":1,"deletions":0,"changes":1,"patch":"private body","raw_url":"secret-url"}],"count":1}`},
		{action: "search-issues", output: `{"total_count":1,"items":[{"number":1,"title":"Ready","body":"private body","html_url":"https://github.com/oryxa/api/issues/1","state":"open"}]}`},
		{action: "get-user", output: `{"login":"octocat","html_url":"https://github.com/octocat","email":"private@example.com","token":"secret"}`},
		{action: "lock-issue", props: `{"owner":"oryxa","repo":"api","issue_number":42}`, output: `{"success":true,"locked":true}`, detailType: integrationevents.GitHubIssueMutatedDetailType},
		{action: "star-repository", props: `{"owner":"oryxa","repo":"api"}`, output: `{"success":true,"status":204}`, detailType: integrationevents.GitHubRepositoryMutatedDetailType},
	}
	for _, test := range tests {
		t.Run(test.action, func(t *testing.T) {
			mapped, err := (DomainMapper{}).Map(MapRequest{
				Execution:  domain.ActionExecution{ID: "act-1", MemberID: "member-1"},
				Connection: domain.Connection{ID: "con-1", Provider: "github"},
				ActionName: test.action, Props: json.RawMessage(test.props), Output: json.RawMessage(test.output),
				OccurredAt: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC),
			})
			if err != nil || len(mapped.SafeResult) == 0 {
				t.Fatalf("result/error = %s / %v", mapped.SafeResult, err)
			}
			for _, forbidden := range []string{"private body", "private@example.com", "secret", "secret-url", "token"} {
				if strings.Contains(string(mapped.SafeResult), forbidden) {
					t.Fatalf("result leaked %q: %s", forbidden, mapped.SafeResult)
				}
			}
			if mapped.DetailType != test.detailType {
				t.Fatalf("detail type = %q, want %q", mapped.DetailType, test.detailType)
			}
		})
	}
}
