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
		{action: "send-draft-email", props: `{"messageId":"draft-1"}`, output: `{"success":true,"message":"Draft sent successfully.","messageId":"draft-1","body":{"content":"private body"}}`, detailType: integrationevents.MicrosoftOutlookEmailAcceptedDetailType},
		{action: "reply-email", props: `{"messageId":"m-1","draft":false}`, output: `{"success":true,"message":"Reply sent successfully.","draftId":"draft-1","body":{"content":"private body"}}`, detailType: integrationevents.MicrosoftOutlookEmailAcceptedDetailType},
		{action: "reply-email", props: `{"messageId":"m-1","draft":true}`, output: `{"success":true,"message":"Draft created successfully.","draftId":"draft-1","draftLink":"https://outlook.office.com/mail/drafts/id/draft-1"}`, detailType: integrationevents.MicrosoftOutlookDraftCreatedDetailType},
		{action: "create-draft-email", output: `{"id":"draft-1","subject":"private subject","body":{"content":"private body"},"toRecipients":[{"emailAddress":{"address":"private@example.com"}}]}`, detailType: integrationevents.MicrosoftOutlookDraftCreatedDetailType},
		{action: "forward-email", props: `{"messageId":"m-1"}`, output: `{"success":true,"messageId":"m-1","id":"m-1","subject":"private subject","body":{"content":"private body"}}`, detailType: integrationevents.MicrosoftOutlookEmailForwardedDetailType},
		{action: "add-label-to-email", props: `{"messageId":"m-1","categories":["secret"]}`, output: `{"id":"m-1","subject":"private subject","categories":["secret"],"body":{"content":"private body"}}`, detailType: integrationevents.MicrosoftOutlookEmailMutatedDetailType},
		{action: "remove-label-from-email", props: `{"messageId":"m-1"}`, output: `{}`, detailType: integrationevents.MicrosoftOutlookEmailMutatedDetailType},
		{action: "move-email-to-folder", props: `{"messageId":"m-1","destinationFolderId":"folder-1"}`, output: `{"id":"m-1","parentFolderId":"folder-1","subject":"private subject","body":{"content":"private body"}}`, detailType: integrationevents.MicrosoftOutlookEmailMutatedDetailType},
		{action: "create-calendar-event", props: `{"calendarId":"calendar-1"}`, output: `{"id":"event-1","subject":"private subject","webLink":"https://outlook.test/event/1","start":{"dateTime":"2026-09-14T09:00:00","timeZone":"UTC"},"body":{"content":"private body"}}`, detailType: integrationevents.MicrosoftCalendarEventCreatedDetailType},
		{action: "list-calendar-events", output: `{"value":[{"id":"event-1","subject":"private subject","attendees":[{"emailAddress":{"address":"private@example.com"}}]}]}`},
		{action: "delete-calendar-event", props: `{"calendarId":"calendar-1","eventId":"event-1"}`, output: `null`, detailType: integrationevents.MicrosoftCalendarEventDeletedDetailType},
		{action: "list-files", output: `[{"id":"file-1","name":"report.pdf","webUrl":"javascript:alert(1)","file":{"mimeType":"application/pdf"},"@microsoft.graph.downloadUrl":"secret-url"}]`},
		{action: "list-folders", output: `[{"id":"folder-1","name":"Archive","webUrl":"javascript:alert(1)","folder":{}}]`},
		{action: "copy-file", output: `{"success":true,"resourceId":"file-2","file":{"id":"file-2","name":"copy.pdf","webUrl":"javascript:alert(1)","@microsoft.graph.downloadUrl":"secret-url"}}`, detailType: integrationevents.MicrosoftOneDriveItemCopiedDetailType},
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

func TestMicrosoftFindEmailStripsBodiesSendersAndNextPage(t *testing.T) {
	t.Parallel()
	mapped, err := (DomainMapper{}).Map(MapRequest{
		Execution:  domain.ActionExecution{ID: "act-1", MemberID: "member-1"},
		Connection: domain.Connection{ID: "con-1", Provider: "microsoft"},
		ActionName: "find-email",
		Output:     json.RawMessage(`{"found":true,"hasMore":true,"nextPageUrl":"https://graph.microsoft.com/secret-url","result":[{"id":"m-1","subject":"Quarterly plan","from":{"emailAddress":{"address":"private@example.com"}},"body":{"content":"private body"},"receivedDateTime":"2026-09-14T09:00:00Z"}]}`),
		OccurredAt: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private@example.com", "private body", "secret-url", "nextPageUrl", `"from":`} {
		if strings.Contains(string(mapped.SafeResult), forbidden) {
			t.Fatalf("find-email leaked %q: %s", forbidden, mapped.SafeResult)
		}
	}
	if !strings.Contains(string(mapped.SafeResult), `"id":"m-1"`) || !strings.Contains(string(mapped.SafeResult), "Quarterly plan") {
		t.Fatalf("find-email dropped lookup metadata: %s", mapped.SafeResult)
	}
	if mapped.DetailType != "" {
		t.Fatalf("lookup published a domain event: %q", mapped.DetailType)
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
	if missing := missingScopes("microsoft", []string{"Mail.Read"}, []string{"Mail.ReadWrite"}); len(missing) != 0 {
		t.Fatalf("Mail.ReadWrite did not satisfy read: %v", missing)
	}
	if missing := missingScopes("microsoft", []string{"Files.Read"}, []string{"Files.ReadWrite"}); len(missing) != 0 {
		t.Fatalf("Files.ReadWrite did not satisfy read: %v", missing)
	}
	if missing := missingScopes("microsoft", []string{"Mail.Send"}, []string{"Mail.ReadWrite"}); len(missing) != 1 {
		t.Fatalf("Mail.ReadWrite unexpectedly satisfied Mail.Send: %v", missing)
	}
}
