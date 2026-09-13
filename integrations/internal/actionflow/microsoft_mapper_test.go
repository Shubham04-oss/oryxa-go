package actionflow

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

func TestMicrosoftMappersProjectOnlySafeResourceMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		action     string
		props      string
		output     string
		detailType string
	}{
		{action: "send-email", output: `null`, detailType: integrationevents.MicrosoftOutlookEmailAcceptedDetailType},
		{action: "create-calendar-event", props: `{"calendarId":"calendar-1"}`, output: `{"id":"event-1","subject":"private subject","webLink":"https://outlook.test/event/1","start":{"dateTime":"2026-09-14T09:00:00","timeZone":"UTC"},"body":{"content":"private body"}}`, detailType: integrationevents.MicrosoftCalendarEventCreatedDetailType},
		{action: "list-calendar-events", output: `{"value":[{"id":"event-1","subject":"private subject","attendees":[{"emailAddress":{"address":"private@example.com"}}]}]}`},
		{action: "list-files", output: `[{"id":"file-1","name":"report.pdf","webUrl":"javascript:alert(1)","file":{"mimeType":"application/pdf"},"@microsoft.graph.downloadUrl":"secret-url"}]`},
	}
	for _, test := range tests {
		t.Run(test.action, func(t *testing.T) {
			now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
			mapped, err := (DomainMapper{}).Map(MapRequest{
				Execution:  domain.ActionExecution{ID: "act-1", MemberID: "member-1"},
				Connection: domain.Connection{ID: "con-1", Provider: "microsoft"},
				ActionName: test.action, Props: json.RawMessage(test.props), Output: json.RawMessage(test.output), OccurredAt: now,
			})
			if err != nil || len(mapped.SafeResult) == 0 {
				t.Fatalf("result/error = %s / %v", mapped.SafeResult, err)
			}
			for _, forbidden := range []string{"private subject", "private body", "private@example.com", "secret-url", "private bytes", "javascript:"} {
				if strings.Contains(string(mapped.SafeResult), forbidden) {
					t.Fatalf("result leaked %q: %s", forbidden, mapped.SafeResult)
				}
			}
			if mapped.DetailType != test.detailType {
				t.Fatalf("detail type = %q, want %q", mapped.DetailType, test.detailType)
			}
			if test.detailType != "" {
				event, err := json.Marshal(mapped.Data)
				if err != nil {
					t.Fatal(err)
				}
				for _, forbidden := range []string{"private subject", "private body", "private@example.com", "secret-url", "private bytes", "javascript:"} {
					if strings.Contains(string(event), forbidden) {
						t.Fatalf("event leaked %q: %s", forbidden, event)
					}
				}
			}
		})
	}
}

func TestMicrosoftCalendarReadScopeAcceptsReadWriteGrant(t *testing.T) {
	t.Parallel()
	if missing := missingScopes("microsoft", []string{"Calendars.Read"}, []string{"Calendars.ReadWrite"}); len(missing) != 0 {
		t.Fatalf("Calendars.ReadWrite did not satisfy read: %v", missing)
	}
	if missing := missingScopes("microsoft", []string{"Calendars.ReadWrite"}, []string{"Calendars.Read"}); len(missing) != 1 {
		t.Fatalf("read unexpectedly satisfied write: %v", missing)
	}
}
