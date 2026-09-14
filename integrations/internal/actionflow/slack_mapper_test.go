package actionflow

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

func TestSlackMapperAllowsSendAsUserWritesAndOmitsMessageBody(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	mapped, err := (DomainMapper{}).Map(MapRequest{
		Execution:  domain.ActionExecution{ID: "act-1", MemberID: "member-1"},
		Connection: domain.Connection{ID: "connection-1", Provider: "slack"},
		ActionName: "delete-message",
		Props:      json.RawMessage(`{"channel":"C123","ts":"171.2"}`),
		Output:     json.RawMessage(`{"ok":true,"channel":"C123"}`),
		OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if mapped.DetailType != integrationevents.SlackMessageDeletedDetailType {
		t.Fatalf("detail type = %s", mapped.DetailType)
	}
	if strings.Contains(string(mapped.SafeResult), "token") {
		t.Fatalf("safe result leaked token: %s", mapped.SafeResult)
	}
	dm, err := (DomainMapper{}).Map(MapRequest{
		Execution:  domain.ActionExecution{ID: "act-2", MemberID: "member-1"},
		Connection: domain.Connection{ID: "connection-1", Provider: "slack"},
		ActionName: "send-direct-message",
		Props:      json.RawMessage(`{"userId":"U123","text":"secret body"}`),
		Output:     json.RawMessage(`{"ok":true,"channel":"D123","ts":"171.3","message":{"text":"secret body"}}`),
		OccurredAt: now,
	})
	if err != nil || dm.DetailType != integrationevents.SlackMessageSentDetailType {
		t.Fatalf("dm mapped/error = %+v / %v", dm, err)
	}
	if strings.Contains(string(dm.SafeResult), "secret body") {
		t.Fatalf("direct message body leaked: %s", dm.SafeResult)
	}
}
