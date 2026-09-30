package slackfake

import (
	"slices"
	"testing"
)

func TestReactionsBelongToTheBotAndRequireScopeAndAnExistingMessage(t *testing.T) {
	fake := New(t)
	workspace := fake.Workspace(DefaultTeamID)
	workspace.Scopes = append(slices.Clone(workspace.Scopes), "reactions:write")
	fake.AddWorkspace(workspace)
	_ = fake.Mention("", Mention{Channel: "C0GENERAL", TS: "1780000700.000200"})
	args := map[string]any{"channel": "C0GENERAL", "timestamp": "1780000700.000200", "name": "speech_balloon"}
	for attempt := range 2 {
		_, _, response := call(t, fake, "reactions.add", DefaultBotToken, args)
		if attempt == 0 && response["ok"] != true || attempt == 1 && response["error"] != "already_reacted" {
			t.Fatalf("reaction %d=%v", attempt, response)
		}
	}
	if reactions := fake.Reactions(); len(reactions) != 1 || reactions[0].UserID != DefaultBotUserID || reactions[0].TS != "1780000700.000200" {
		t.Fatalf("reactions=%v", reactions)
	}
	args["timestamp"] = "1780000000.000001"
	if _, _, response := call(t, fake, "reactions.add", DefaultBotToken, args); response["error"] != "message_not_found" {
		t.Fatalf("unseen message=%v", response)
	}
	workspace = fake.Workspace(DefaultTeamID)
	workspace.Scopes = slices.DeleteFunc(slices.Clone(workspace.Scopes), func(scope string) bool { return scope == "reactions:write" })
	fake.AddWorkspace(workspace)
	if _, _, response := call(t, fake, "reactions.add", DefaultBotToken, args); response["error"] != "missing_scope" {
		t.Fatalf("scope missing=%v", response)
	}
}
