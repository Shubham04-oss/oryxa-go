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

func TestSlackFileMappersKeepBytesAndPrivateURLsOut(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	uploaded, err := (DomainMapper{}).Map(MapRequest{
		Execution:  domain.ActionExecution{ID: "act-1", MemberID: "member-1"},
		Connection: domain.Connection{ID: "connection-1", Provider: "slack"},
		ActionName: "upload-file",
		Props:      json.RawMessage(`{"file":{"file_id":"file_abc"}}`),
		Output:     json.RawMessage(`{"ok":true,"files":[{"id":"F123","url_private_download":"https://files.slack.com/secret"}]}`),
		OccurredAt: now,
	})
	if err != nil || uploaded.DetailType != integrationevents.SlackFileUploadedDetailType {
		t.Fatalf("upload mapped/error = %+v / %v", uploaded, err)
	}
	if strings.Contains(string(uploaded.SafeResult), "secret") || !strings.Contains(string(uploaded.SafeResult), `"slack_file_id":"F123"`) {
		t.Fatalf("upload result = %s", uploaded.SafeResult)
	}
	got, err := (DomainMapper{}).Map(MapRequest{
		Connection: domain.Connection{Provider: "slack"}, ActionName: "get-file",
		Output: json.RawMessage(`{"id":"F123","name":"note.txt","mimetype":"text/plain","url_private":"https://files.slack.com/secret","data":{"fileName":"note.txt","fileId":"file_xyz"}}`),
	})
	if err != nil || got.DetailType != "" || strings.Contains(string(got.SafeResult), "secret") ||
		!strings.Contains(string(got.SafeResult), `"file_id":"file_xyz"`) {
		t.Fatalf("get-file result/error = %s / %v", got.SafeResult, err)
	}
	api, err := (DomainMapper{}).Map(MapRequest{
		Connection: domain.Connection{Provider: "slack"}, ActionName: "custom-api-call",
		Output: json.RawMessage(`{"status":200,"headers":{"authorization":"Bearer secret"},"body":{"ok":true,"user":{"id":"U123"}}}`),
	})
	if err != nil || api.DetailType != "" || strings.Contains(string(api.SafeResult), "secret") ||
		!strings.Contains(string(api.SafeResult), `"ok":true`) {
		t.Fatalf("Slack custom API result/error = %s / %v", api.SafeResult, err)
	}
}
