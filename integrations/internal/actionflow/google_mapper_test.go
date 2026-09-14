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
		{name: "upload", action: "upload-drive-file",
			props:      `{"file_name":"note.txt","file":{"file_id":"file_abc"}}`,
			output:     `{"id":"file-9","name":"note.txt","webContentLink":"https://drive.google.com/uc?id=secret"}`,
			detailType: integrationevents.GoogleDriveItemCreatedDetailType},
		{name: "replace", action: "replace-drive-file",
			props:      `{"file_id":"file-9","file":{"file_id":"file_abc"}}`,
			output:     `{"id":"file-9","name":"note.txt"}`,
			detailType: integrationevents.GoogleDriveItemMutatedDetailType},
		{name: "public access", action: "set-drive-public-access",
			props:      `{"file_id":"file-1","role":"reader"}`,
			output:     `{"id":"perm-1","type":"anyone","role":"reader","webViewLink":"https://drive.google.com/file/d/file-1/view","downloadUrl":{"fileName":"secret.bin","fileId":"file_xyz"}}`,
			detailType: integrationevents.GoogleDriveItemMutatedDetailType},
		{name: "empty trash", action: "empty-trash",
			props:      `{"drive_id":"drive-1"}`,
			output:     `{"success":true,"driveId":"drive-1"}`,
			detailType: integrationevents.GoogleDriveItemMutatedDetailType},
		{name: "from text", action: "create-file-from-text",
			props:      `{"file_name":"note.txt","text":"private body","content_type":"plain/text"}`,
			output:     `{"id":"file-10","name":"note.txt"}`,
			detailType: integrationevents.GoogleDriveItemCreatedDetailType},
		{name: "from url", action: "upload-from-url",
			props:      `{"source_url":"https://example.com/file.pdf","file_name":"file.pdf"}`,
			output:     `{"id":"file-11","name":"file.pdf","webContentLink":"https://drive.google.com/uc?id=secret"}`,
			detailType: integrationevents.GoogleDriveItemCreatedDetailType},
		{name: "docs create", action: "create-document",
			props:      `{"title":"private title","body":"private body"}`,
			output:     `{"documentId":"doc-1","title":"private title","body":{"content":[{"paragraph":{"elements":[{"textRun":{"content":"private body"}}]}}]}}`,
			detailType: integrationevents.GoogleDocsDocumentCreatedDetailType},
		{name: "docs mutate", action: "append-text",
			props:      `{"documentId":"doc-1","text":"private body"}`,
			output:     `{"documentId":"doc-1"}`,
			detailType: integrationevents.GoogleDocsDocumentMutatedDetailType},
		{name: "sheets create", action: "sheets-create-spreadsheet",
			props:      `{"spreadsheet_title":"private title"}`,
			output:     `{"spreadsheetId":"sheet-1","properties":{"title":"private title"}}`,
			detailType: integrationevents.GoogleSheetsSpreadsheetCreatedDetailType},
		{name: "sheets mutate", action: "sheets-add-row",
			props:      `{"spreadsheet_id":"sheet-1","sheet_id":0,"first_row_headers":false,"values":["private body"]}`,
			output:     `{"spreadsheetId":"sheet-1","updates":{"updatedRange":"Sheet1!A1"}}`,
			detailType: integrationevents.GoogleSheetsSpreadsheetMutatedDetailType},
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
			for _, forbidden := range []string{"private@example.com", "private subject", "private body", "private title", "private description", "Bearer secret", "webContentLink", "downloadUrl", "secret.bin"} {
				if strings.Contains(string(event), forbidden) {
					t.Fatalf("event leaked %q: %s", forbidden, event)
				}
			}
			for _, forbidden := range []string{"webContentLink", "downloadUrl", "secret.bin"} {
				if strings.Contains(string(mapped.SafeResult), forbidden) {
					t.Fatalf("safe result leaked %q: %s", forbidden, mapped.SafeResult)
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
	search, err := (DomainMapper{}).Map(MapRequest{
		Connection: domain.Connection{Provider: "google"}, ActionName: "search-drive-files",
		Output: json.RawMessage(`[{"id":"file-2","name":"notes.txt","mimeType":"text/plain"}]`),
	})
	if err != nil || !strings.Contains(string(search.SafeResult), `"file-2"`) {
		t.Fatalf("search result/error = %s / %v", search.SafeResult, err)
	}
	downloaded, err := (DomainMapper{}).Map(MapRequest{
		Connection: domain.Connection{Provider: "google"}, ActionName: "download-drive-file",
		Output: json.RawMessage(`{"fileName":"report.pdf","fileId":"file_xyz","data":"private bytes"}`),
	})
	if err != nil || downloaded.DetailType != "" || strings.Contains(string(downloaded.SafeResult), "private bytes") ||
		!strings.Contains(string(downloaded.SafeResult), `"file_id":"file_xyz"`) {
		t.Fatalf("download result/error = %s / %v", downloaded.SafeResult, err)
	}
	attachment, err := (DomainMapper{}).Map(MapRequest{
		Connection: domain.Connection{Provider: "google"}, ActionName: "get-email-attachment",
		Output: json.RawMessage(`{"fileName":"invoice.pdf","size":12,"data":{"fileName":"invoice.pdf","fileId":"file_att"}}`),
	})
	if err != nil || attachment.DetailType != "" || strings.Contains(string(attachment.SafeResult), `"size"`) ||
		!strings.Contains(string(attachment.SafeResult), `"file_id":"file_att"`) {
		t.Fatalf("attachment result/error = %s / %v", attachment.SafeResult, err)
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
	mail, err := (DomainMapper{}).Map(MapRequest{
		Connection: domain.Connection{Provider: "google"}, ActionName: "get-email",
		Output: json.RawMessage(`{"id":"m-1","subject":"Quarterly plan","from":{"text":"Boss <boss@example.com>","value":[{"address":"boss@example.com"}]},"to":{"text":"me@example.com"},"date":"2026-09-14T09:00:00Z","text":"private body","html":"<p>private body</p>","attachments":[{"fileName":"invoice.pdf","mimeType":"application/pdf","size":12,"content":"","data":{"fileName":"invoice.pdf","fileId":"file_att"}}]}`),
	})
	if err != nil || mail.DetailType != "" || strings.Contains(string(mail.SafeResult), "private body") ||
		!strings.Contains(string(mail.SafeResult), `"file_id":"file_att"`) || !strings.Contains(string(mail.SafeResult), "Quarterly plan") {
		t.Fatalf("get-email result/error = %s / %v", mail.SafeResult, err)
	}
	_, err = (DomainMapper{}).Map(MapRequest{
		Connection: domain.Connection{Provider: "google"}, ActionName: "get-email",
		Output: json.RawMessage(`{"id":"m-1","text":"private body","attachments":[{"fileName":"invoice.pdf","data":"private bytes"}]}`),
	})
	if err == nil {
		t.Fatal("get-email accepted leftover attachment bytes")
	}
	found, err := (DomainMapper{}).Map(MapRequest{
		Connection: domain.Connection{Provider: "google"}, ActionName: "find-email",
		Output: json.RawMessage(`{"found":true,"results":{"messages":[{"id":"m-1","subject":"Quarterly plan","text":"private body","attachments":[]}],"count":1}}`),
	})
	if err != nil || found.DetailType != "" || strings.Contains(string(found.SafeResult), "private body") ||
		!strings.Contains(string(found.SafeResult), `"id":"m-1"`) {
		t.Fatalf("find-email result/error = %s / %v", found.SafeResult, err)
	}
	api, err := (DomainMapper{}).Map(MapRequest{
		Connection: domain.Connection{Provider: "google"}, ActionName: "gmail-custom-api-call",
		Output: json.RawMessage(`{"status":200,"headers":{"authorization":"Bearer secret"},"body":{"emailAddress":"user@example.com"}}`),
	})
	if err != nil || api.DetailType != "" || strings.Contains(string(api.SafeResult), "Bearer") ||
		!strings.Contains(string(api.SafeResult), `"status":200`) {
		t.Fatalf("gmail custom API result/error = %s / %v", api.SafeResult, err)
	}
	plaintext, err := (DomainMapper{}).Map(MapRequest{
		Connection: domain.Connection{Provider: "google"}, ActionName: "get-document-plaintext",
		Props: json.RawMessage(`{"documentId":"doc-1"}`), Output: json.RawMessage(`{"text":"private body"}`),
	})
	if err != nil || plaintext.DetailType != "" || !strings.Contains(string(plaintext.SafeResult), `"document_id":"doc-1"`) {
		t.Fatalf("docs plaintext result/error = %s / %v", plaintext.SafeResult, err)
	}
	rows, err := (DomainMapper{}).Map(MapRequest{
		Connection: domain.Connection{Provider: "google"}, ActionName: "sheets-get-values",
		Output: json.RawMessage(`{"values":[["a"],["b"],["c"]]}`),
	})
	if err != nil || rows.DetailType != "" || !strings.Contains(string(rows.SafeResult), `"truncated":false`) {
		t.Fatalf("sheets values result/error = %s / %v", rows.SafeResult, err)
	}
	exported, err := (DomainMapper{}).Map(MapRequest{
		Connection: domain.Connection{Provider: "google"}, ActionName: "export-as-pdf",
		Output: json.RawMessage(`{"fileName":"plan.pdf","fileId":"file_pdf"}`),
	})
	if err != nil || exported.DetailType != "" || !strings.Contains(string(exported.SafeResult), `"file_id":"file_pdf"`) {
		t.Fatalf("docs export result/error = %s / %v", exported.SafeResult, err)
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
	if missing := missingScopes("google", []string{"https://www.googleapis.com/auth/drive.readonly"},
		[]string{"https://www.googleapis.com/auth/drive"}); len(missing) != 0 {
		t.Fatalf("drive did not satisfy drive.readonly: %v", missing)
	}
	if missing := missingScopes("google", []string{"https://www.googleapis.com/auth/gmail.metadata"},
		[]string{"https://www.googleapis.com/auth/gmail.send"}); len(missing) != 1 {
		t.Fatalf("gmail.send unexpectedly satisfied gmail.metadata: %v", missing)
	}
	if missing := missingScopes("google", []string{"https://www.googleapis.com/auth/documents.readonly"},
		[]string{"https://www.googleapis.com/auth/documents"}); len(missing) != 0 {
		t.Fatalf("documents did not satisfy documents.readonly: %v", missing)
	}
	if missing := missingScopes("google", []string{"https://www.googleapis.com/auth/spreadsheets.readonly"},
		[]string{"https://www.googleapis.com/auth/spreadsheets"}); len(missing) != 0 {
		t.Fatalf("spreadsheets did not satisfy spreadsheets.readonly: %v", missing)
	}
}
