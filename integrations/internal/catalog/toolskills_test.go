package catalog

import "testing"

func TestCatalogToolsHaveCallScopedSkills(t *testing.T) {
	catalog := Static{PublicBaseURL: "https://connect.oryxa.test"}
	for _, operation := range []struct{ provider, name string }{
		{"slack", "post-message"}, {"slack", "add-reaction"}, {"slack", "find-channel"},
		{"slack", "send-direct-message"}, {"slack", "delete-message"}, {"slack", "search-messages"},
		{"github", "create-issue"}, {"github", "comment-on-issue"}, {"github", "get-issue"},
		{"github", "list-pull-requests"}, {"github", "create-pull-request"}, {"github", "merge-pull-request"},
		{"github", "get-repository"}, {"github", "search-issues"},
		{"google", "send-email"}, {"google", "reply-email"}, {"google", "create-calendar-event"},
		{"google", "list-calendar-events"}, {"google", "delete-calendar-event"},
		{"google", "list-drive-files"}, {"google", "copy-drive-file"}, {"google", "get-drive-file-metadata"},
		{"microsoft", "send-email"}, {"microsoft", "reply-email"}, {"microsoft", "find-email"},
		{"microsoft", "create-calendar-event"}, {"microsoft", "list-calendar-events"},
		{"microsoft", "delete-calendar-event"}, {"microsoft", "list-files"},
		{"microsoft", "list-folders"}, {"microsoft", "copy-file"},
	} {
		if tool, ok := catalog.ActionTool(operation.provider, operation.name); !ok ||
			tool.Skill.Content == "" || tool.Definition.ActionName != operation.name {
			t.Fatalf("action %s/%s has no tool skill", operation.provider, operation.name)
		}
	}
	for _, operation := range []struct{ provider, name string }{
		{"slack", "new-message"}, {"slack", "new-direct-message"}, {"slack", "new-reaction"},
		{"microsoft", "new-email"}, {"microsoft", "calendar-event-changed"},
		{"github", "new-issue"}, {"github", "new-pull-request"},
		{"google", "new-email"}, {"google", "calendar-event-changed"},
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
	if _, ok := catalog.ActionTool("slack", "custom-api-call"); ok {
		t.Fatal("disabled action exposed a skill")
	}
	if _, ok := catalog.TriggerTool("google", "new-drive-change"); ok {
		t.Fatal("disabled trigger exposed a skill")
	}
}
