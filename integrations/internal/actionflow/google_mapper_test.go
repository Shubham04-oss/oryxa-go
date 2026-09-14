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
		{name: "reply", action: "reply-email",
			props:      `{"message_id":"message-1","body":"private body"}`,
			output:     `{"id":"message-2","threadId":"thread-1","snippet":"private body"}`,
			detailType: integrationevents.GoogleGmailMessageSentDetailType},
		{name: "draft", action: "create-draft-email",
			props:      `{"subject":"private subject","body":"private body"}`,
			output:     `{"id":"draft-1","message":{"id":"message-1","threadId":"thread-1","snippet":"private body"}}`,
			detailType: integrationevents.GoogleGmailDraftCreatedDetailType},
		{name: "forward", action: "forward-email",
			props:      `{"message_id":"message-1","receiver":["private@example.com"]}`,
			output:     `{"id":"message-2","threadId":"thread-1"}`,
			detailType: integrationevents.GoogleGmailEmailForwardedDetailType},
		{name: "label", action: "add-label-to-email",
			props:      `{"message_id":"message-1","label":"INBOX"}`,
			output:     `{"id":"message-1","labelIds":["INBOX"],"snippet":"private body"}`,
			detailType: integrationevents.GoogleGmailEmailMutatedDetailType},
		{name: "Calendar", action: "create-calendar-event",
			props:      `{"calendar_id":"primary","title":"private title","start_date_time":"2026-09-14T09:00:00Z","description":"private description"}`,
			output:     `{"id":"event-1","htmlLink":"https://calendar.google.com/event?id=1","summary":"private title","description":"private description","start":{"dateTime":"2026-09-14T09:00:00Z"},"end":{"dateTime":"2026-09-14T09:30:00Z"}}`,
			detailType: integrationevents.GoogleCalendarEventCreatedDetailType},
		{name: "update", action: "update-calendar-event",
			props:      `{"calendar_id":"primary","event_id":"event-1","description":"private description"}`,
			output:     `{"id":"event-1","summary":"private title","description":"private description"}`,
			detailType: integrationevents.GoogleCalendarEventUpdatedDetailType},
		{name: "delete", action: "delete-calendar-event",
			props:      `{"calendar_id":"primary","event_id":"event-1"}`,
			output:     `null`,
			detailType: integrationevents.GoogleCalendarEventDeletedDetailType},
		{name: "folder", action: "create-drive-folder",
			props:      `{"folder_name":"private title"}`,
			output:     `{"id":"folder-1","name":"private title"}`,
			detailType: integrationevents.GoogleDriveItemCreatedDetailType},
		{name: "copy", action: "copy-drive-file",
			props:      `{"file_id":"file-1","name":"copy","folder_id":"folder-1"}`,
			output:     `{"id":"file-2","name":"copy","webContentLink":"https://drive.google.com/uc?id=secret"}`,
			detailType: integrationevents.GoogleDriveItemCopiedDetailType},
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
			for _, forbidden := range []string{"private@example.com", "private subject", "private body", "private title", "private description", "Bearer secret", "webContentLink"} {
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
	if drive.DetailType != "" {
		t.Fatalf("drive lookup published %q", drive.DetailType)
	}
	calendar, err := (DomainMapper{}).Map(MapRequest{
		Connection: domain.Connection{Provider: "google"}, ActionName: "list-calendar-events",
		Output: json.RawMessage(`{"status":200,"body":{"items":[{"id":"event-1","summary":"Planning","description":"private notes","start":{"dateTime":"2026-09-14T09:00:00Z"}}]}}`),
	})
	if err != nil || strings.Contains(string(calendar.SafeResult), "private notes") || !strings.Contains(string(calendar.SafeResult), `"event-1"`) {
		t.Fatalf("calendar result/error = %s / %v", calendar.SafeResult, err)
	}
	thread, err := (DomainMapper{}).Map(MapRequest{
		Connection: domain.Connection{Provider: "google"}, ActionName: "get-thread",
		Output: json.RawMessage(`{"id":"thread-1","messages":[{"id":"message-1","snippet":"private body","payload":{"body":{"data":"cHJpdmF0ZSBib2R5"}}}]}`),
	})
	if err != nil || strings.Contains(string(thread.SafeResult), "private body") || strings.Contains(string(thread.SafeResult), "payload") {
		t.Fatalf("thread result/error = %s / %v", thread.SafeResult, err)
	}
}

func TestGoogleScopeImplications(t *testing.T) {
	t.Parallel()
	if missing := missingScopes("google", []string{"https://www.googleapis.com/auth/calendar.readonly"},
		[]string{"https://www.googleapis.com/auth/calendar.events"}); len(missing) != 0 {
		t.Fatalf("calendar.events did not satisfy calendar.readonly: %v", missing)
	}
	if missing := missingScopes("google", []string{"https://www.googleapis.com/auth/gmail.readonly"},
		[]string{"https://www.googleapis.com/auth/gmail.modify"}); len(missing) != 0 {
		t.Fatalf("gmail.modify did not satisfy gmail.readonly: %v", missing)
	}
	if missing := missingScopes("google", []string{"https://www.googleapis.com/auth/gmail.send"},
		[]string{"https://www.googleapis.com/auth/gmail.compose"}); len(missing) != 0 {
		t.Fatalf("gmail.compose did not satisfy gmail.send: %v", missing)
	}
	if missing := missingScopes("google", []string{"https://www.googleapis.com/auth/drive.metadata.readonly"},
		[]string{"https://www.googleapis.com/auth/drive"}); len(missing) != 0 {
		t.Fatalf("drive did not satisfy metadata.readonly: %v", missing)
	}
	if missing := missingScopes("google", []string{"https://www.googleapis.com/auth/gmail.metadata"},
		[]string{"https://www.googleapis.com/auth/gmail.send"}); len(missing) != 1 {
		t.Fatalf("gmail.send unexpectedly satisfied gmail.metadata: %v", missing)
	}
}
