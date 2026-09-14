package catalog

import "testing"

func TestCatalogToolsHaveCallScopedSkills(t *testing.T) {
	catalog := Static{PublicBaseURL: "https://connect.oryxa.test"}
	for _, operation := range []struct{ provider, name string }{
		{"slack", "post-message"}, {"slack", "add-reaction"}, {"slack", "find-channel"},
		{"slack", "send-direct-message"}, {"slack", "delete-message"}, {"slack", "search-messages"},
		{"slack", "upload-file"}, {"slack", "get-file"}, {"slack", "custom-api-call"},
		{"github", "create-issue"}, {"github", "comment-on-issue"}, {"github", "get-issue"},
		{"github", "list-pull-requests"}, {"github", "create-pull-request"}, {"github", "merge-pull-request"},
		{"github", "get-repository"}, {"github", "search-issues"}, {"github", "get-file-content"},
		{"github", "create-or-update-file"}, {"github", "search-code"}, {"github", "create-gist"},
		{"github", "raw-graphql-query"}, {"github", "dispatch-workflow"}, {"github", "custom-api-call"},
		{"google", "send-email"}, {"google", "reply-email"}, {"google", "create-calendar-event"},
		{"google", "list-calendar-events"}, {"google", "delete-calendar-event"},
		{"google", "list-drive-files"}, {"google", "copy-drive-file"}, {"google", "get-drive-file-metadata"},
		{"google", "upload-drive-file"}, {"google", "get-email-attachment"}, {"google", "set-drive-public-access"},
		{"google", "create-file-from-text"}, {"google", "empty-trash"}, {"google", "get-email"},
		{"google", "find-email"}, {"google", "upload-from-url"}, {"google", "gmail-custom-api-call"},
		{"google", "calendar-custom-api-call"}, {"google", "drive-custom-api-call"},
		{"google", "create-document"}, {"google", "export-as-pdf"}, {"google", "docs-custom-api-call"},
		{"google", "sheets-create-spreadsheet"}, {"google", "sheets-export-worksheet"},
		{"microsoft", "send-email"}, {"microsoft", "reply-email"}, {"microsoft", "find-email"},
		{"microsoft", "create-calendar-event"}, {"microsoft", "list-calendar-events"},
		{"microsoft", "delete-calendar-event"}, {"microsoft", "list-files"},
		{"microsoft", "list-folders"}, {"microsoft", "copy-file"},
		{"microsoft", "upload-onedrive-file"}, {"microsoft", "download-file"}, {"microsoft", "download-attachment"},
		{"microsoft", "custom-api-call"},
		{"microsoft", "create-channel"}, {"microsoft", "teams-custom-api-call"},
		{"microsoft", "upload-sharepoint-file"}, {"microsoft", "sharepoint-custom-api-call"},
	} {
		if tool, ok := catalog.ActionTool(operation.provider, operation.name); !ok ||
			tool.Skill.Content == "" || tool.Definition.ActionName != operation.name {
			t.Fatalf("action %s/%s has no tool skill", operation.provider, operation.name)
		}
	}
	for _, operation := range []struct{ provider, name string }{
		{"slack", "new-message"}, {"slack", "new-direct-message"}, {"slack", "new-reaction"},
		{"microsoft", "new-email"}, {"microsoft", "calendar-event-changed"},
		{"github", "new-issue"}, {"github", "new-pull-request"}, {"github", "new-star"}, {"github", "new-push"},
		{"github", "new-release"}, {"github", "new-review"},
		{"google", "new-email"}, {"google", "calendar-event-changed"}, {"google", "drive-file-changed"},
	} {
		if tool, ok := catalog.TriggerTool(operation.provider, operation.name); !ok ||
			tool.Skill.Content == "" || tool.Definition.TriggerName != operation.name {
			t.Fatalf("trigger %s/%s has no tool skill", operation.provider, operation.name)
		}
	}
	for _, provider := range []string{"slack", "github", "google", "microsoft"} {
		if tool, ok := catalog.ConnectionTool(provider); !ok ||
			tool.Skill.Content == "" || tool.Definition.Provider != provider {
			t.Fatalf("connection %s has no tool skill", provider)
		}
	}
}

func TestCatalogDoesNotExposeUnboundTools(t *testing.T) {
	catalog := Static{PublicBaseURL: "https://connect.oryxa.test"}
	if _, ok := catalog.ActionTool("slack", "request-approval-message"); ok {
		t.Fatal("disabled action exposed a skill")
	}
	if _, ok := catalog.TriggerTool("google", "new-drive-change"); ok {
		t.Fatal("disabled trigger exposed a skill")
	}
}
