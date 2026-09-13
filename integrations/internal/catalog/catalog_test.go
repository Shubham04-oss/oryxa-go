package catalog

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

func TestSlackCatalogPinsPieceScopesAndCallback(t *testing.T) {
	t.Parallel()
	catalog := Static{PublicBaseURL: "https://connect.oryxa.test"}
	definition, ok := catalog.OAuthDefinition("slack")
	if !ok {
		t.Fatal("Slack definition is missing")
	}
	if definition.PieceName != SlackPieceName || definition.PieceVersion != SlackPieceVersion {
		t.Fatalf("piece = %s@%s", definition.PieceName, definition.PieceVersion)
	}
	if definition.RedirectURI != "https://connect.oryxa.test/v1/oauth/callback/slack" {
		t.Fatalf("redirect URI = %q", definition.RedirectURI)
	}
	if len(definition.RequiredScopes) != 2 || definition.SupportsPKCE {
		t.Fatalf("unexpected Slack policy: %+v", definition)
	}
	for _, scope := range []string{"chat:write", "reactions:write", "channels:read", "groups:read"} {
		if !slices.Contains(definition.AllowedScopes, scope) {
			t.Fatalf("action scope %q is not allowed: %+v", scope, definition.AllowedScopes)
		}
	}
	post, ok := catalog.ActionDefinition("slack", "post-message")
	if !ok || post.RunnerActionName != "slack_post_message" || !slices.Contains(post.RequiredScopes, "chat:write") {
		t.Fatalf("post action = %+v/%v", post, ok)
	}
	if err := post.ValidateProps(json.RawMessage(`{"channel":"C123","text":"hello"}`)); err != nil {
		t.Fatalf("valid post props: %v", err)
	}
	for _, invalid := range []string{
		`{"channel":"C123","text":""}`,
		`{"channel":"C123","text":"hello","sendAsUser":true}`,
		`{"channel":"C123","text":"hello","unknown":true}`,
	} {
		if post.ValidateProps(json.RawMessage(invalid)) == nil {
			t.Fatalf("invalid post props accepted: %s", invalid)
		}
	}
	if _, ok := catalog.ActionDefinition("slack", "delete-message"); ok {
		t.Fatal("unapproved Slack action was accepted")
	}
	githubOAuth, ok := catalog.OAuthDefinition("github")
	if !ok || githubOAuth.PieceName != GitHubPieceName || githubOAuth.PieceVersion != GitHubPieceVersion || githubOAuth.RedirectURI != "https://connect.oryxa.test/v1/oauth/callback/github" {
		t.Fatalf("GitHub OAuth definition = %+v/%v", githubOAuth, ok)
	}
	createIssue, ok := catalog.ActionDefinition("github", "create-issue")
	if !ok || createIssue.RunnerActionName != "create_issue_ai" {
		t.Fatalf("GitHub create issue = %+v/%v", createIssue, ok)
	}
	if err := createIssue.ValidateProps(json.RawMessage(`{"owner":"oryxa","repo":"api","title":"Test","labels":["bug"]}`)); err != nil {
		t.Fatalf("valid GitHub props: %v", err)
	}
	if createIssue.ValidateProps(json.RawMessage(`{"owner":"../bad","repo":"api","title":"Test"}`)) == nil {
		t.Fatal("unsafe GitHub owner was accepted")
	}
	googleOAuth, ok := catalog.OAuthDefinition("google")
	if !ok || googleOAuth.RedirectURI != "https://connect.oryxa.test/v1/oauth/callback/google" || !googleOAuth.SupportsPKCE {
		t.Fatalf("Google OAuth definition = %+v/%v", googleOAuth, ok)
	}
	for _, scope := range []string{googleGmailSendScope, googleCalendarScope, googleDriveReadScope} {
		if !slices.Contains(googleOAuth.AllowedScopes, scope) {
			t.Fatalf("Google scope %q is not allowed: %+v", scope, googleOAuth.AllowedScopes)
		}
	}
	googleActions := map[string]string{
		"send-email": "gmail_send_email", "create-calendar-event": "google_calendar_create_event",
		"list-calendar-events": "google_calendar_list_events", "list-drive-files": "drive_list_files",
		"get-drive-file-metadata": "drive_get_file",
	}
	for actionName, runnerName := range googleActions {
		action, found := catalog.ActionDefinition("google", actionName)
		if !found || action.RunnerActionName != runnerName || action.ValidateProps == nil {
			t.Fatalf("Google action %q = %+v/%v", actionName, action, found)
		}
	}
	if action, _ := catalog.ActionDefinition("google", "send-email"); action.ValidateProps(json.RawMessage(
		`{"receiver":["recipient@example.com"],"subject":"Hello","body_type":"plain_text","body":"Body"}`)) != nil {
		t.Fatal("valid Gmail send properties were rejected")
	}
	if action, _ := catalog.ActionDefinition("google", "send-email"); action.ValidateProps(json.RawMessage(
		`{"receiver":["recipient@example.com"],"subject":"Hello","body_type":"plain_text","body":"Body","attachments":[{}]}`)) == nil {
		t.Fatal("Gmail attachment escaped the disabled file capability")
	}
	if _, ok := catalog.OAuthDefinition("unapproved"); ok {
		t.Fatal("unknown provider was accepted")
	}
}

func TestMicrosoftCatalogPinsReviewedPiecesScopesAndStrictProps(t *testing.T) {
	t.Parallel()
	catalog := Static{PublicBaseURL: "https://connect.oryxa.test"}
	oauth, ok := catalog.OAuthDefinition("microsoft")
	if !ok || oauth.RedirectURI != "https://connect.oryxa.test/v1/oauth/callback/microsoft" || !oauth.SupportsPKCE {
		t.Fatalf("Microsoft OAuth definition = %+v/%v", oauth, ok)
	}
	for _, scope := range []string{microsoftMailSendScope, microsoftCalendarReadScope, microsoftCalendarWriteScope, microsoftFilesReadScope} {
		if !slices.Contains(oauth.AllowedScopes, scope) {
			t.Fatalf("Microsoft scope %q is not allowed: %+v", scope, oauth.AllowedScopes)
		}
	}
	actions := map[string]struct {
		runner string
		piece  string
		props  string
		scope  string
	}{
		"send-email":            {"send-email", MicrosoftOutlookPieceName, `{"recipients":["recipient@example.com"],"subject":"Hello","bodyFormat":"text","body":"Body"}`, microsoftMailSendScope},
		"create-calendar-event": {"create_event", MicrosoftCalendarPieceName, `{"calendarId":"primary","title":"Planning","start":"2026-09-14T09:00:00Z","timezone":"UTC"}`, microsoftCalendarWriteScope},
		"list-calendar-events":  {"list_events", MicrosoftCalendarPieceName, `{"calendarId":"primary"}`, microsoftCalendarReadScope},
		"list-files":            {"list_files", MicrosoftOneDrivePieceName, `{}`, microsoftFilesReadScope},
	}
	for name, expected := range actions {
		action, found := catalog.ActionDefinition("microsoft", name)
		if !found || action.RunnerActionName != expected.runner || action.PieceName != expected.piece ||
			action.ValidateProps == nil || !slices.Equal(action.RequiredScopes, []string{expected.scope}) {
			t.Fatalf("Microsoft action %q = %+v/%v", name, action, found)
		}
		if err := action.ValidateProps(json.RawMessage(expected.props)); err != nil {
			t.Fatalf("valid Microsoft %s properties: %v", name, err)
		}
	}
	for _, invalid := range []struct{ action, props string }{
		{"send-email", `{"recipients":["recipient@example.com"],"subject":"Hello","bodyFormat":"text","body":"Body","attachments":[{}]}`},
		{"create-calendar-event", `{"calendarId":"primary","title":"Planning","start":"bad","timezone":"UTC"}`},
		{"list-calendar-events", `{"calendarId":"primary","unknown":true}`},
	} {
		action, _ := catalog.ActionDefinition("microsoft", invalid.action)
		if action.ValidateProps(json.RawMessage(invalid.props)) == nil {
			t.Fatalf("invalid Microsoft %s properties accepted: %s", invalid.action, invalid.props)
		}
	}
	if _, ok := catalog.ActionDefinition("microsoft", "custom-api-call"); ok {
		t.Fatal("unapproved Microsoft custom API action was accepted")
	}
	triggers := map[string]struct {
		piece, resource, changeType, scope string
	}{
		"new-email":              {MicrosoftOutlookPieceName, "/me/mailFolders('Inbox')/messages", "created", "Mail.Read"},
		"calendar-event-changed": {MicrosoftCalendarPieceName, "/me/events", "created,updated,deleted", "Calendars.Read"},
	}
	for name, expected := range triggers {
		trigger, found := catalog.TriggerDefinition("microsoft", name)
		if !found || trigger.PieceName != expected.piece || trigger.RouteMode != "per_connection" || trigger.Event != "change-notification" ||
			trigger.GraphResource != expected.resource || trigger.GraphChangeType != expected.changeType || trigger.GraphTTL != 72*time.Hour || !slices.Contains(trigger.RequiredScopes, expected.scope) {
			t.Fatalf("Microsoft trigger %q = %+v/%v", name, trigger, found)
		}
		if trigger.ValidateProps(json.RawMessage(`{}`)) != nil || trigger.ValidateProps(json.RawMessage(`{"unexpected":true}`)) == nil {
			t.Fatalf("Microsoft trigger %q properties are not strict", name)
		}
	}
}
