package catalog

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
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
	if err := post.ValidateProps(json.RawMessage(`{"channel":"C123","text":"hello","sendAsUser":true}`)); err != nil {
		t.Fatalf("sendAsUser post props: %v", err)
	}
	for _, invalid := range []string{
		`{"channel":"C123","text":""}`,
		`{"channel":"C123","text":"hello","unknown":true}`,
	} {
		if post.ValidateProps(json.RawMessage(invalid)) == nil {
			t.Fatalf("invalid post props accepted: %s", invalid)
		}
	}
	if _, ok := catalog.ActionDefinition("slack", "custom-api-call"); ok {
		t.Fatal("unapproved Slack action was accepted")
	}
	if _, ok := catalog.ActionDefinition("slack", "delete-message"); !ok {
		t.Fatal("approved Slack delete-message is missing")
	}
	if trigger, ok := catalog.TriggerDefinition("slack", "new-direct-message"); !ok || trigger.AppWebhookFamily != domain.SlackEventsRouteTrigger || trigger.Event != "message" {
		t.Fatalf("new-direct-message = %+v/%v", trigger, ok)
	}
	if len(definition.UserScopes) == 0 {
		t.Fatal("Slack user scopes are missing")
	}
	for name := range slackActions {
		if _, ok := catalog.ActionDefinition("slack", name); !ok {
			t.Fatalf("reviewed Slack action %q is unbound", name)
		}
	}
	for name := range slackTriggers {
		trigger, ok := catalog.TriggerDefinition("slack", name)
		if !ok || trigger.AppWebhookFamily != domain.SlackEventsRouteTrigger || trigger.ValidateProps == nil {
			t.Fatalf("reviewed Slack trigger %q = %+v/%v", name, trigger, ok)
		}
	}
	githubOAuth, ok := catalog.OAuthDefinition("github")
	if !ok || githubOAuth.PieceName != GitHubPieceName || githubOAuth.PieceVersion != GitHubPieceVersion || githubOAuth.RedirectURI != "https://connect.oryxa.test/v1/oauth/callback/github" {
		t.Fatalf("GitHub OAuth definition = %+v/%v", githubOAuth, ok)
	}
	if !slices.Contains(githubOAuth.AllowedScopes, "admin:org_hook") || !slices.Contains(githubOAuth.AllowedScopes, "admin:repo_hook") ||
		!slices.Contains(githubOAuth.AllowedScopes, "read:org") {
		t.Fatalf("GitHub hook scopes missing: %+v", githubOAuth.AllowedScopes)
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
	for _, scope := range []string{
		googleGmailSendScope, googleGmailMetadataScope, googleGmailReadScope, googleGmailComposeScope, googleGmailModifyScope,
		googleCalendarScope, googleCalendarReadScope, googleDriveReadScope, googleDriveWriteScope,
	} {
		if !slices.Contains(googleOAuth.AllowedScopes, scope) {
			t.Fatalf("Google scope %q is not allowed: %+v", scope, googleOAuth.AllowedScopes)
		}
	}
	for actionName, spec := range googleActions {
		action, found := catalog.ActionDefinition("google", actionName)
		if !found || action.RunnerActionName != spec.Runner || action.ValidateProps == nil {
			t.Fatalf("Google action %q = %+v/%v", actionName, action, found)
		}
		for _, scope := range spec.Scopes {
			if !slices.Contains(googleOAuth.AllowedScopes, scope) {
				t.Fatalf("Google action %q scope %q is not allowed", actionName, scope)
			}
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
	if action, _ := catalog.ActionDefinition("google", "reply-email"); action.ValidateProps(json.RawMessage(
		`{"message_id":"m-1","reply_type":"reply","body_type":"plain_text","body":"Hi","attachment":{}}`)) == nil {
		t.Fatal("Gmail reply attachment escaped the disabled file capability")
	}
	if action, _ := catalog.ActionDefinition("google", "search-drive-files"); action.ValidateProps(json.RawMessage(
		`{"query_term":"fullText","operator":"contains","value":"secret"}`)) == nil {
		t.Fatal("Drive fullText search was accepted")
	}
	if action, _ := catalog.ActionDefinition("google", "copy-drive-file"); action.ValidateProps(json.RawMessage(
		`{"file_id":"file-1","name":"copy","folder_id":"folder-1","convert_to":"application/vnd.google-apps.document"}`)) == nil {
		t.Fatal("Drive Docs convert_to was accepted")
	}
	label, _ := catalog.ActionDefinition("google", "add-label-to-email")
	if label.PrepareProps == nil {
		t.Fatal("Gmail add-label-to-email is missing PrepareProps")
	}
	prepared, err := label.PrepareProps(json.RawMessage(`{"message_id":"m-1","label":"INBOX"}`))
	if err != nil || !strings.Contains(string(prepared), `"id":"INBOX"`) {
		t.Fatalf("Gmail label prepare = %s / %v", prepared, err)
	}
	for _, name := range []string{
		"custom-api-call", "get-email", "find-email", "get-attachment", "stop-watch",
		"empty-trash", "set-drive-public-access", "delete-shared-drive",
	} {
		if _, ok := catalog.ActionDefinition("google", name); ok {
			t.Fatalf("unapproved Google action %q was accepted", name)
		}
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
	for _, scope := range []string{microsoftMailSendScope, microsoftMailReadWriteScope, microsoftCalendarReadScope, microsoftCalendarWriteScope, microsoftFilesReadScope, microsoftFilesReadWriteScope} {
		if !slices.Contains(oauth.AllowedScopes, scope) {
			t.Fatalf("Microsoft scope %q is not allowed: %+v", scope, oauth.AllowedScopes)
		}
	}
	actions := map[string]struct {
		runner string
		piece  string
		props  string
		scopes []string
	}{
		"send-email":              {"send-email", MicrosoftOutlookPieceName, `{"recipients":["recipient@example.com"],"subject":"Hello","bodyFormat":"text","body":"Body"}`, []string{microsoftMailSendScope}},
		"reply-email":             {"reply-email", MicrosoftOutlookPieceName, `{"messageId":"AAMk","replyBody":"Thanks","bodyFormat":"text","draft":false}`, []string{microsoftMailReadWriteScope, microsoftMailSendScope}},
		"create-draft-email":      {"createDraftEmail", MicrosoftOutlookPieceName, `{"recipients":["recipient@example.com"],"subject":"Hello","bodyFormat":"text","body":"Body"}`, []string{microsoftMailReadWriteScope}},
		"send-draft-email":        {"sendDraftEmail", MicrosoftOutlookPieceName, `{"messageId":"AAMk"}`, []string{microsoftMailSendScope}},
		"forward-email":           {"forwardEmail", MicrosoftOutlookPieceName, `{"messageId":"AAMk","recipients":["recipient@example.com"]}`, []string{microsoftMailReadWriteScope, microsoftMailSendScope}},
		"add-label-to-email":      {"addLabelToEmail", MicrosoftOutlookPieceName, `{"messageId":"AAMk","categories":["Follow up"]}`, []string{microsoftMailReadWriteScope}},
		"remove-label-from-email": {"removeLabelFromEmail", MicrosoftOutlookPieceName, `{"messageId":"AAMk","categories":["Follow up"]}`, []string{microsoftMailReadWriteScope}},
		"move-email-to-folder":    {"moveEmailToFolder", MicrosoftOutlookPieceName, `{"messageId":"AAMk","destinationFolderId":"folder-1"}`, []string{microsoftMailReadWriteScope}},
		"find-email":              {"findEmail", MicrosoftOutlookPieceName, `{"searchQuery":"from:boss@example.com"}`, []string{microsoftMailReadScope}},
		"create-calendar-event":   {"create_event", MicrosoftCalendarPieceName, `{"calendarId":"primary","title":"Planning","start":"2026-09-14T09:00:00Z","timezone":"UTC"}`, []string{microsoftCalendarWriteScope}},
		"list-calendar-events":    {"list_events", MicrosoftCalendarPieceName, `{"calendarId":"primary"}`, []string{microsoftCalendarReadScope}},
		"delete-calendar-event":   {"delete_event", MicrosoftCalendarPieceName, `{"calendarId":"primary","eventId":"event-1"}`, []string{microsoftCalendarWriteScope}},
		"list-files":              {"list_files", MicrosoftOneDrivePieceName, `{}`, []string{microsoftFilesReadScope}},
		"list-folders":            {"list_folders", MicrosoftOneDrivePieceName, `{}`, []string{microsoftFilesReadScope}},
		"copy-file":               {"copy_file", MicrosoftOneDrivePieceName, `{"fileId":"file-1","conflictBehavior":"rename"}`, []string{microsoftFilesReadWriteScope}},
	}
	for name, expected := range actions {
		action, found := catalog.ActionDefinition("microsoft", name)
		if !found || action.RunnerActionName != expected.runner || action.PieceName != expected.piece ||
			action.ValidateProps == nil || !slices.Equal(action.RequiredScopes, expected.scopes) {
			t.Fatalf("Microsoft action %q = %+v/%v", name, action, found)
		}
		if err := action.ValidateProps(json.RawMessage(expected.props)); err != nil {
			t.Fatalf("valid Microsoft %s properties: %v", name, err)
		}
	}
	for name := range microsoftActions {
		if _, ok := catalog.ActionDefinition("microsoft", name); !ok {
			t.Fatalf("reviewed Microsoft action %q is unbound", name)
		}
	}
	for _, invalid := range []struct{ action, props string }{
		{"send-email", `{"recipients":["recipient@example.com"],"subject":"Hello","bodyFormat":"text","body":"Body","attachments":[{}]}`},
		{"reply-email", `{"messageId":"AAMk","replyBody":"Thanks","bodyFormat":"text","attachments":[{}]}`},
		{"create-draft-email", `{"recipients":["recipient@example.com"],"subject":"Hello","bodyFormat":"text","body":"Body","attachments":[{}]}`},
		{"find-email", `{"searchQuery":"from:boss","unknown":true}`},
		{"find-email", `{"searchQuery":"from:\"quoted\""}`},
		{"copy-file", `{"fileId":"file-1","conflictBehavior":"upsert"}`},
		{"create-calendar-event", `{"calendarId":"primary","title":"Planning","start":"bad","timezone":"UTC"}`},
		{"list-calendar-events", `{"calendarId":"primary","unknown":true}`},
	} {
		action, _ := catalog.ActionDefinition("microsoft", invalid.action)
		if action.ValidateProps(json.RawMessage(invalid.props)) == nil {
			t.Fatalf("invalid Microsoft %s properties accepted: %s", invalid.action, invalid.props)
		}
	}
	for _, name := range []string{"custom-api-call", "download-attachment", "request-approval-in-mail", "download-file", "upload-onedrive-file"} {
		if _, ok := catalog.ActionDefinition("microsoft", name); ok {
			t.Fatalf("unapproved Microsoft action %q was accepted", name)
		}
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

func TestGitHubCatalogPinsInboundTriggersAndRejectsUnsafeTargets(t *testing.T) {
	t.Parallel()
	catalog := Static{PublicBaseURL: "https://connect.oryxa.test"}
	issue, ok := catalog.TriggerDefinition("github", "new-issue")
	if !ok || issue.PieceName != GitHubPieceName || issue.RouteMode != "per_connection" || issue.Event != "issues" ||
		issue.GitHubHookEvent != "issues" || issue.ValidateProps == nil {
		t.Fatalf("new-issue = %+v/%v", issue, ok)
	}
	pr, ok := catalog.TriggerDefinition("github", "new-pull-request")
	if !ok || pr.Event != "pull_request" || pr.GitHubHookEvent != "pull_request" {
		t.Fatalf("new-pull-request = %+v/%v", pr, ok)
	}
	if err := issue.ValidateProps(json.RawMessage(`{"scope":"repository","owner":"oryxa","repo":"api"}`)); err != nil {
		t.Fatalf("valid repository trigger props: %v", err)
	}
	if err := issue.ValidateProps(json.RawMessage(`{"scope":"organization","owner":"oryxa"}`)); err != nil {
		t.Fatalf("valid organization trigger props: %v", err)
	}
	for _, invalid := range []string{
		`{"scope":"repository","owner":"../bad","repo":"api"}`,
		`{"scope":"organization","owner":"oryxa","repo":"api"}`,
		`{"scope":"user","owner":"oryxa"}`,
		`{"scope":"repository","owner":"oryxa"}`,
		`{}`,
	} {
		if issue.ValidateProps(json.RawMessage(invalid)) == nil {
			t.Fatalf("invalid GitHub trigger props accepted: %s", invalid)
		}
	}
	if _, ok := catalog.TriggerDefinition("github", "new_star"); ok {
		t.Fatal("unapproved GitHub trigger was accepted")
	}
	for name, spec := range githubActions {
		action, ok := catalog.ActionDefinition("github", name)
		if !ok || action.RunnerActionName != spec.Runner || action.ValidateProps == nil {
			t.Fatalf("reviewed GitHub action %q = %+v/%v", name, action, ok)
		}
	}
	createPR, _ := catalog.ActionDefinition("github", "create-pull-request")
	if err := createPR.ValidateProps(json.RawMessage(`{"owner":"oryxa","repo":"api","title":"Ready","head":"feature","base":"main"}`)); err != nil {
		t.Fatalf("valid create-pull-request props: %v", err)
	}
	if createPR.ValidateProps(json.RawMessage(`{"owner":"oryxa","repo":"api","title":"Ready","head":"feature","base":"main","unknown":true}`)) == nil {
		t.Fatal("unknown create-pull-request field was accepted")
	}
	for _, name := range []string{"custom-api-call", "get-file-content", "create-or-update-file", "search-code", "raw-graphql-query"} {
		if _, ok := catalog.ActionDefinition("github", name); ok {
			t.Fatalf("unapproved GitHub action %q was accepted", name)
		}
	}
}

func TestGoogleCatalogPinsInboundWatches(t *testing.T) {
	t.Parallel()
	catalog := Static{PublicBaseURL: "https://connect.oryxa.test"}
	mail, ok := catalog.TriggerDefinition("google", "new-email")
	if !ok || mail.PieceName != GmailPieceName || mail.RouteMode != "per_connection" || mail.GoogleWatch != "gmail" ||
		!slices.Contains(mail.RequiredScopes, googleGmailMetadataScope) {
		t.Fatalf("google new-email = %+v/%v", mail, ok)
	}
	changed, ok := catalog.TriggerDefinition("google", "calendar-event-changed")
	if !ok || changed.PieceName != GoogleCalendarPieceName || changed.GoogleWatch != "calendar" ||
		!slices.Contains(changed.RequiredScopes, googleCalendarReadScope) || changed.GoogleTTL != 7*24*time.Hour {
		t.Fatalf("google calendar-event-changed = %+v/%v", changed, ok)
	}
	if mail.ValidateProps(json.RawMessage(`{}`)) != nil || mail.ValidateProps(json.RawMessage(`{"unexpected":true}`)) == nil {
		t.Fatal("Google trigger properties are not strict")
	}
}
