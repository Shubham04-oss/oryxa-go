package toolskills

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestRegistryBindsEveryApprovedToolFamily(t *testing.T) {
	if skills := All(); len(skills) != 8 {
		t.Fatalf("len(All()) = %d, want 8", len(skills))
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
		{"slack", "find-channel", "oryxa-slack"}, {"github", "create-issue", "oryxa-github"},
		{"github", "comment-on-issue", "oryxa-github"}, {"github", "get-issue", "oryxa-github"},
		{"github", "list-pull-requests", "oryxa-github"}, {"google", "send-email", "oryxa-gmail"},
		{"google", "create-calendar-event", "oryxa-google-calendar"},
		{"google", "list-calendar-events", "oryxa-google-calendar"},
		{"google", "list-drive-files", "oryxa-google-drive"},
		{"google", "get-drive-file-metadata", "oryxa-google-drive"},
		{"microsoft", "send-email", "oryxa-microsoft-outlook"},
		{"microsoft", "create-calendar-event", "oryxa-microsoft-calendar"},
		{"microsoft", "list-calendar-events", "oryxa-microsoft-calendar"},
		{"microsoft", "list-files", "oryxa-microsoft-onedrive"},
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
		{"microsoft", "new-email", "oryxa-microsoft-outlook"},
		{"microsoft", "calendar-event-changed", "oryxa-microsoft-calendar"},
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
	if _, ok := ForAction("slack", "delete-message"); ok {
		t.Fatal("unsupported action has a skill binding")
	}
	if _, ok := ForTrigger("google", "new-email"); ok {
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
