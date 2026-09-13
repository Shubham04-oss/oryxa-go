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
