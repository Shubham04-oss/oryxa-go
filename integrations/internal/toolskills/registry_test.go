package toolskills

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestRegistryBindsEveryApprovedToolFamily(t *testing.T) {
	if skills := All(); len(skills) != 12 {
		t.Fatalf("len(All()) = %d, want 12", len(skills))
	}
	connections := []struct{ provider, skill string }{
		{"slack", "oryxa-slack"}, {"github", "oryxa-github"},
		{"google", "oryxa-gmail"}, {"microsoft", "oryxa-microsoft-outlook"},
	}
	for _, connection := range connections {
		skill, ok := ForConnection(connection.provider)
		assertSkill(t, skill, ok)
		if skill.Name != connection.skill {
			t.Fatalf("connection %s skill = %s, want %s", connection.provider, skill.Name, connection.skill)
		}
	}
	actions := []struct{ provider, name, skill string }{
		{"slack", "post-message", "oryxa-slack"}, {"slack", "add-reaction", "oryxa-slack"},
		{"slack", "find-channel", "oryxa-slack"}, {"slack", "send-direct-message", "oryxa-slack"},
		{"slack", "delete-message", "oryxa-slack"}, {"slack", "custom-api-call", "oryxa-slack"},
		{"github", "create-issue", "oryxa-github"}, {"github", "comment-on-issue", "oryxa-github"},
		{"github", "get-issue", "oryxa-github"}, {"github", "list-pull-requests", "oryxa-github"},
		{"github", "create-pull-request", "oryxa-github"}, {"github", "get-repository", "oryxa-github"},
		{"github", "create-gist", "oryxa-github"}, {"github", "raw-graphql-query", "oryxa-github"},
		{"github", "dispatch-workflow", "oryxa-github"}, {"github", "custom-api-call", "oryxa-github"},
		{"google", "send-email", "oryxa-gmail"}, {"google", "reply-email", "oryxa-gmail"},
		{"google", "get-email-attachment", "oryxa-gmail"}, {"google", "get-email", "oryxa-gmail"},
		{"google", "find-email", "oryxa-gmail"}, {"google", "gmail-custom-api-call", "oryxa-gmail"},
		{"google", "upload-drive-file", "oryxa-google-drive"}, {"google", "upload-from-url", "oryxa-google-drive"},
		{"google", "drive-custom-api-call", "oryxa-google-drive"},
		{"google", "create-document", "oryxa-google-docs"},
		{"google", "export-as-pdf", "oryxa-google-docs"},
		{"google", "docs-custom-api-call", "oryxa-google-docs"},
		{"google", "sheets-create-spreadsheet", "oryxa-google-sheets"},
		{"google", "sheets-export-worksheet", "oryxa-google-sheets"},
		{"google", "create-calendar-event", "oryxa-google-calendar"},
		{"google", "calendar-custom-api-call", "oryxa-google-calendar"},
		{"google", "list-calendar-events", "oryxa-google-calendar"},
		{"google", "delete-calendar-event", "oryxa-google-calendar"},
		{"google", "list-drive-files", "oryxa-google-drive"},
		{"google", "copy-drive-file", "oryxa-google-drive"},
		{"google", "get-drive-file-metadata", "oryxa-google-drive"},
		{"microsoft", "send-email", "oryxa-microsoft-outlook"},
		{"microsoft", "reply-email", "oryxa-microsoft-outlook"},
		{"microsoft", "find-email", "oryxa-microsoft-outlook"},
		{"microsoft", "create-calendar-event", "oryxa-microsoft-calendar"},
		{"microsoft", "list-calendar-events", "oryxa-microsoft-calendar"},
		{"microsoft", "delete-calendar-event", "oryxa-microsoft-calendar"},
		{"microsoft", "list-files", "oryxa-microsoft-onedrive"},
		{"microsoft", "list-folders", "oryxa-microsoft-onedrive"},
		{"microsoft", "copy-file", "oryxa-microsoft-onedrive"},
		{"microsoft", "upload-onedrive-file", "oryxa-microsoft-onedrive"},
		{"microsoft", "download-file", "oryxa-microsoft-onedrive"},
		{"microsoft", "download-attachment", "oryxa-microsoft-outlook"},
		{"microsoft", "custom-api-call", "oryxa-microsoft-outlook"},
		{"microsoft", "create-channel", "oryxa-microsoft-teams"},
		{"microsoft", "teams-custom-api-call", "oryxa-microsoft-teams"},
		{"microsoft", "upload-sharepoint-file", "oryxa-microsoft-sharepoint"},
		{"microsoft", "sharepoint-custom-api-call", "oryxa-microsoft-sharepoint"},
	}
	for _, action := range actions {
		skill, ok := ForAction(action.provider, action.name)
		assertSkill(t, skill, ok)
		if skill.Name != action.skill {
			t.Fatalf("action %s/%s skill = %s, want %s", action.provider, action.name, skill.Name, action.skill)
		}
	}
	for _, trigger := range []struct{ provider, name, skill string }{
		{"slack", "new-message", "oryxa-slack"},
		{"slack", "new-direct-message", "oryxa-slack"},
		{"slack", "new-reaction", "oryxa-slack"},
		{"microsoft", "new-email", "oryxa-microsoft-outlook"},
		{"microsoft", "calendar-event-changed", "oryxa-microsoft-calendar"},
		{"github", "new-issue", "oryxa-github"},
		{"github", "new-pull-request", "oryxa-github"},
		{"github", "new-star", "oryxa-github"},
		{"github", "new-push", "oryxa-github"},
		{"github", "new-release", "oryxa-github"},
		{"github", "new-review", "oryxa-github"},
		{"google", "new-email", "oryxa-gmail"},
		{"google", "calendar-event-changed", "oryxa-google-calendar"},
		{"google", "drive-file-changed", "oryxa-google-drive"},
	} {
		skill, ok := ForTrigger(trigger.provider, trigger.name)
		assertSkill(t, skill, ok)
		if skill.Name != trigger.skill {
			t.Fatalf("trigger %s/%s skill = %s, want %s", trigger.provider, trigger.name, skill.Name, trigger.skill)
		}
	}
	raw, err := os.ReadFile("../../pieces.lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Pieces []struct {
			Package string `json:"package"`
		} `json:"pieces"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatal(err)
	}
	for _, piece := range lock.Pieces {
		skill, ok := ForPiece(piece.Package)
		assertSkill(t, skill, ok)
	}
}

func TestRegistryRejectsUnknownToolBindings(t *testing.T) {
	if _, ok := ForAction("slack", "request-approval-message"); ok {
		t.Fatal("unsupported action has a skill binding")
	}
	if _, ok := ForTrigger("google", "new-drive-change"); ok {
		t.Fatal("unsupported trigger has a skill binding")
	}
	if _, ok := ForConnection("unknown"); ok {
		t.Fatal("unsupported connection has a skill binding")
	}
}

func assertSkill(t *testing.T, skill Skill, ok bool) {
	t.Helper()
	if !ok || skill.Name == "" || skill.Description == "" || skill.ResourceURI == "" ||
		!strings.HasPrefix(skill.Digest, "sha256:") || !strings.Contains(skill.Content, "## Tool-call contract") {
		t.Fatalf("invalid tool-bound skill: %+v/%v", skill, ok)
	}
}
