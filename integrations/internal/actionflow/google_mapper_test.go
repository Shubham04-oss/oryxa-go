package actionflow

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

func TestGoogleMappersKeepContentOutOfDomainEvents(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		action     string
		props      string
		output     string
		detailType string
	}{
		{name: "Gmail", action: "send-email",
			props:      `{"receiver":["private@example.com"],"subject":"private subject","body_type":"plain_text","body":"private body"}`,
			output:     `{"data":{"id":"message-1","threadId":"thread-1"},"config":{"headers":{"authorization":"Bearer secret"}}}`,
			detailType: integrationevents.GoogleGmailMessageSentDetailType},
		{name: "Calendar", action: "create-calendar-event",
			props:      `{"calendar_id":"primary","title":"private title","start_date_time":"2026-09-14T09:00:00Z","description":"private description"}`,
			output:     `{"id":"event-1","htmlLink":"https://calendar.google.com/event?id=1","summary":"private title","description":"private description","start":{"dateTime":"2026-09-14T09:00:00Z"},"end":{"dateTime":"2026-09-14T09:30:00Z"}}`,
			detailType: integrationevents.GoogleCalendarEventCreatedDetailType},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mapped, err := (DomainMapper{}).Map(MapRequest{
				Execution:  domain.ActionExecution{ID: "act-1", MemberID: "member-1"},
				Connection: domain.Connection{ID: "connection-1", Provider: "google"},
				ActionName: test.action, Props: json.RawMessage(test.props), Output: json.RawMessage(test.output), OccurredAt: now,
			})
			if err != nil {
				t.Fatal(err)
			}
			if mapped.DetailType != test.detailType {
				t.Fatalf("detail type = %s", mapped.DetailType)
			}
			event, err := json.Marshal(mapped.Data)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"private@example.com", "private subject", "private body", "private title", "private description", "Bearer secret"} {
				if strings.Contains(string(event), forbidden) {
					t.Fatalf("event leaked %q: %s", forbidden, event)
				}
			}
		})
	}
}

func TestGoogleReadMappersProjectBoundedMetadata(t *testing.T) {
	drive, err := (DomainMapper{}).Map(MapRequest{
		Connection: domain.Connection{Provider: "google"}, ActionName: "list-drive-files",
		Output: json.RawMessage(`{"type":"drive#fileList","files":[{"id":"file-1","name":"report.pdf","mimeType":"application/pdf","parents":["root"]}]}`),
	})
	if err != nil || strings.Contains(string(drive.SafeResult), "drive#fileList") || !strings.Contains(string(drive.SafeResult), `"file-1"`) {
		t.Fatalf("drive result/error = %s / %v", drive.SafeResult, err)
	}
	calendar, err := (DomainMapper{}).Map(MapRequest{
		Connection: domain.Connection{Provider: "google"}, ActionName: "list-calendar-events",
		Output: json.RawMessage(`{"status":200,"body":{"items":[{"id":"event-1","summary":"Planning","description":"private notes","start":{"dateTime":"2026-09-14T09:00:00Z"}}]}}`),
	})
	if err != nil || strings.Contains(string(calendar.SafeResult), "private notes") || !strings.Contains(string(calendar.SafeResult), `"event-1"`) {
		t.Fatalf("calendar result/error = %s / %v", calendar.SafeResult, err)
	}
}
