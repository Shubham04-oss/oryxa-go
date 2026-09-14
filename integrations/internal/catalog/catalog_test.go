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
	for _, scope := range []string{"chat:write", "reactions:write", "channels:read", "groups:read", "files:write"} {
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
	if _, ok := catalog.ActionDefinition("slack", "request-approval-message"); ok {
		t.Fatal("unapproved Slack action was accepted")
	}
	custom, ok := catalog.ActionDefinition("slack", "custom-api-call")
	if !ok || custom.RunnerActionName != "custom_api_call" || custom.PrepareProps == nil ||
		custom.ValidateProps(json.RawMessage(`{"url":"/chat.postMessage","method":"POST"}`)) != nil {
		t.Fatalf("Slack custom-api-call = %+v/%v", custom, ok)
	}
	if custom.ValidateProps(json.RawMessage(`{"url":"/chat.postMessage","method":"POST","headers":{"Authorization":"Bearer x"}}`)) == nil {
		t.Fatal("Slack custom API accepted an Authorization header")
	}
	preparedCustom, err := custom.PrepareProps(json.RawMessage(`{"url":"/chat.postMessage","method":"post","use_user_token":true}`))
	if err != nil || !strings.Contains(string(preparedCustom), `"url":{"url":"/chat.postMessage"}`) ||
		!strings.Contains(string(preparedCustom), `"useUserToken":true`) {
		t.Fatalf("Slack custom API prepare = %s / %v", preparedCustom, err)
	}
	if _, ok := catalog.ActionDefinition("slack", "delete-message"); !ok {
		t.Fatal("approved Slack delete-message is missing")
	}
	upload, ok := catalog.ActionDefinition("slack", "upload-file")
	if !ok || !upload.NeedsFiles || upload.ValidateProps(json.RawMessage(`{"file":{"file_id":"file_abc"}}`)) != nil {
		t.Fatalf("upload-file = %+v/%v", upload, ok)
	}
	if upload.ValidateProps(json.RawMessage(`{"file":{"file_id":"not-a-file"}}`)) == nil {
		t.Fatal("Slack upload accepted a non-Oryxa file id")
	}
	getFile, ok := catalog.ActionDefinition("slack", "get-file")
	if !ok || !getFile.NeedsFiles || getFile.ValidateProps(json.RawMessage(`{"fileId":"F123"}`)) != nil {
		t.Fatalf("get-file = %+v/%v", getFile, ok)
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
		!slices.Contains(githubOAuth.AllowedScopes, "read:org") || !slices.Contains(githubOAuth.AllowedScopes, "gist") {
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
		googleCalendarScope, googleCalendarReadScope, googleDriveReadScope, googleDriveFileReadScope, googleDriveWriteScope,
		googleDocsWriteScope, googleDocsReadScope, googleSheetsWriteScope, googleSheetsReadScope,
	} {
		if !slices.Contains(googleOAuth.AllowedScopes, scope) {
			t.Fatalf("Google scope %q is not allowed: %+v", scope, googleOAuth.AllowedScopes)
		}
	}
	for _, group := range []map[string]googleActionSpec{googleActions, googleDocsActions, googleSheetsActions} {
		for actionName, spec := range group {
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
	}
	if action, _ := catalog.ActionDefinition("google", "send-email"); action.ValidateProps(json.RawMessage(
		`{"receiver":["recipient@example.com"],"subject":"Hello","body_type":"plain_text","body":"Body"}`)) != nil {
		t.Fatal("valid Gmail send properties were rejected")
	}
	send, _ := catalog.ActionDefinition("google", "send-email")
	if !send.NeedsFiles {
		t.Fatal("Gmail send-email should open the file broker")
	}
	if send.ValidateProps(json.RawMessage(
		`{"receiver":["recipient@example.com"],"subject":"Hello","body_type":"plain_text","body":"Body","attachments":[{"file_id":"file_abc"}]}`)) != nil {
		t.Fatal("valid Gmail attachment was rejected")
	}
	prepared, err := send.PrepareProps(json.RawMessage(
		`{"receiver":["recipient@example.com"],"subject":"Hello","body_type":"plain_text","body":"Body","attachments":[{"file_id":"file_abc"}]}`))
	if err != nil || !strings.Contains(string(prepared), `"file":{"file_id":"file_abc"}`) {
		t.Fatalf("Gmail attachment prepare = %s / %v", prepared, err)
	}
	if action, _ := catalog.ActionDefinition("google", "send-email"); action.ValidateProps(json.RawMessage(
		`{"receiver":["recipient@example.com"],"subject":"Hello","body_type":"plain_text","body":"Body","attachments":[{}]}`)) == nil {
		t.Fatal("Gmail empty attachment object was accepted")
	}
	if action, _ := catalog.ActionDefinition("google", "reply-email"); action.ValidateProps(json.RawMessage(
		`{"message_id":"m-1","reply_type":"reply","body_type":"plain_text","body":"Hi","attachment":{}}`)) == nil {
		t.Fatal("Gmail empty reply attachment was accepted")
	}
	if action, _ := catalog.ActionDefinition("google", "reply-email"); action.ValidateProps(json.RawMessage(
		`{"message_id":"m-1","reply_type":"reply","body_type":"plain_text","body":"Hi","attachment":{"file_id":"file_abc"}}`)) != nil {
		t.Fatal("valid Gmail reply attachment was rejected")
	}
	uploadDrive, ok := catalog.ActionDefinition("google", "upload-drive-file")
	if !ok || !uploadDrive.NeedsFiles || uploadDrive.ValidateProps(json.RawMessage(
		`{"file_name":"note.txt","file":{"file_id":"file_abc"}}`)) != nil {
		t.Fatalf("upload-drive-file = %+v/%v", uploadDrive, ok)
	}
	if uploadDrive.ValidateProps(json.RawMessage(`{"file_name":"note.txt","file":{}}`)) == nil {
		t.Fatal("Drive upload accepted an empty file object")
	}
	if action, _ := catalog.ActionDefinition("google", "export-drive-file"); action.ValidateProps(json.RawMessage(
		`{"file_id":"drive-1","mime_type":"application/pdf"}`)) != nil {
		t.Fatal("valid Drive export properties were rejected")
	}
	if action, _ := catalog.ActionDefinition("google", "export-drive-file"); action.ValidateProps(json.RawMessage(
		`{"file_id":"drive-1","mime_type":"application/zip"}`)) == nil {
		t.Fatal("Drive export accepted an unlisted MIME type")
	}
	if action, _ := catalog.ActionDefinition("google", "search-drive-files"); action.ValidateProps(json.RawMessage(
		`{"query_term":"fullText","operator":"contains","value":"secret"}`)) != nil {
		t.Fatal("valid Drive fullText search was rejected")
	}
	if action, _ := catalog.ActionDefinition("google", "copy-drive-file"); action.ValidateProps(json.RawMessage(
		`{"file_id":"file-1","name":"copy","folder_id":"folder-1","convert_to":"application/vnd.google-apps.document"}`)) != nil {
		t.Fatal("valid Drive Docs convert_to was rejected")
	}
	if action, _ := catalog.ActionDefinition("google", "copy-drive-file"); action.ValidateProps(json.RawMessage(
		`{"file_id":"file-1","name":"copy","folder_id":"folder-1","convert_to":"application/pdf"}`)) == nil {
		t.Fatal("Drive copy accepted an unlisted convert_to MIME type")
	}
	publicAccess, ok := catalog.ActionDefinition("google", "set-drive-public-access")
	if !ok || !publicAccess.NeedsFiles || publicAccess.ValidateProps(json.RawMessage(
		`{"file_id":"file-1","role":"reader"}`)) != nil {
		t.Fatalf("set-drive-public-access = %+v/%v", publicAccess, ok)
	}
	if action, _ := catalog.ActionDefinition("google", "create-file-from-text"); action.ValidateProps(json.RawMessage(
		`{"file_name":"note.txt","text":"hello","content_type":"plain/text"}`)) != nil {
		t.Fatal("valid create-file-from-text properties were rejected")
	}
	if action, _ := catalog.ActionDefinition("google", "create-file-from-text"); action.ValidateProps(json.RawMessage(
		`{"file_name":"note.txt","text":"hello","content_type":"text/plain"}`)) == nil {
		t.Fatal("Drive create-file-from-text accepted text/plain")
	}
	label, _ := catalog.ActionDefinition("google", "add-label-to-email")
	if label.PrepareProps == nil {
		t.Fatal("Gmail add-label-to-email is missing PrepareProps")
	}
	prepared, err = label.PrepareProps(json.RawMessage(`{"message_id":"m-1","label":"INBOX"}`))
	if err != nil || !strings.Contains(string(prepared), `"id":"INBOX"`) {
		t.Fatalf("Gmail label prepare = %s / %v", prepared, err)
	}
	getEmail, ok := catalog.ActionDefinition("google", "get-email")
	if !ok || !getEmail.NeedsFiles || getEmail.RunnerActionName != "gmail_get_message" ||
		getEmail.ValidateProps(json.RawMessage(`{"message_id":"m-1"}`)) != nil {
		t.Fatalf("get-email = %+v/%v", getEmail, ok)
	}
	findEmail, ok := catalog.ActionDefinition("google", "find-email")
	if !ok || !findEmail.NeedsFiles || findEmail.PrepareProps == nil ||
		findEmail.ValidateProps(json.RawMessage(`{"from":"boss@example.com","max_results":20}`)) != nil {
		t.Fatalf("find-email = %+v/%v", findEmail, ok)
	}
	if findEmail.ValidateProps(json.RawMessage(`{"max_results":101}`)) == nil {
		t.Fatal("Gmail find-email accepted more than 100 results")
	}
	preparedLabel, err := findEmail.PrepareProps(json.RawMessage(`{"label":"INBOX","max_results":10}`))
	if err != nil || !strings.Contains(string(preparedLabel), `"name":"INBOX"`) {
		t.Fatalf("Gmail find-email label prepare = %s / %v", preparedLabel, err)
	}
	uploadURL, ok := catalog.ActionDefinition("google", "upload-from-url")
	if !ok || uploadURL.NeedsFiles || uploadURL.ValidateProps(json.RawMessage(
		`{"source_url":"https://example.com/file.pdf","file_name":"file.pdf"}`)) != nil {
		t.Fatalf("upload-from-url = %+v/%v", uploadURL, ok)
	}
	for _, invalid := range []string{
		`{"source_url":"http://example.com/file.pdf","file_name":"file.pdf"}`,
		`{"source_url":"https://127.0.0.1/file.pdf","file_name":"file.pdf"}`,
		`{"source_url":"https://localhost/file.pdf","file_name":"file.pdf"}`,
		`{"source_url":"https://169.254.169.254/latest","file_name":"file.pdf"}`,
		`{"source_url":"https://user:pass@example.com/file.pdf","file_name":"file.pdf"}`,
	} {
		if uploadURL.ValidateProps(json.RawMessage(invalid)) == nil {
			t.Fatalf("upload-from-url accepted a private URL: %s", invalid)
		}
	}
	gmailAPI, ok := catalog.ActionDefinition("google", "gmail-custom-api-call")
	if !ok || gmailAPI.PrepareProps == nil || gmailAPI.ValidateProps(json.RawMessage(
		`{"url":"/users/me/profile","method":"GET"}`)) != nil {
		t.Fatalf("gmail-custom-api-call = %+v/%v", gmailAPI, ok)
	}
	if gmailAPI.ValidateProps(json.RawMessage(`{"url":"https://evil.example/users/me/profile","method":"GET"}`)) == nil {
		t.Fatal("Gmail custom API accepted a host outside gmail.googleapis.com")
	}
	createDoc, ok := catalog.ActionDefinition("google", "create-document")
	if !ok || createDoc.PieceName != GoogleDocsPieceName || createDoc.ValidateProps(json.RawMessage(
		`{"title":"Plan","body":"Hello"}`)) != nil {
		t.Fatalf("create-document = %+v/%v", createDoc, ok)
	}
	exportPDF, ok := catalog.ActionDefinition("google", "export-as-pdf")
	if !ok || !exportPDF.NeedsFiles || exportPDF.ValidateProps(json.RawMessage(`{"documentId":"doc-1"}`)) != nil {
		t.Fatalf("export-as-pdf = %+v/%v", exportPDF, ok)
	}
	docsAPI, ok := catalog.ActionDefinition("google", "docs-custom-api-call")
	if !ok || docsAPI.ValidateProps(json.RawMessage(`{"url":"https://docs.googleapis.com/v1/documents/doc-1","method":"GET"}`)) != nil {
		t.Fatalf("docs-custom-api-call = %+v/%v", docsAPI, ok)
	}
	if docsAPI.ValidateProps(json.RawMessage(`{"url":"https://evil.example/v1/documents/doc-1","method":"GET"}`)) == nil {
		t.Fatal("Docs custom API accepted a host outside docs.googleapis.com")
	}
	createSheet, ok := catalog.ActionDefinition("google", "sheets-create-spreadsheet")
	if !ok || createSheet.PieceName != GoogleSheetsPieceName || createSheet.ValidateProps(json.RawMessage(
		`{"spreadsheet_title":"Budget"}`)) != nil {
		t.Fatalf("sheets-create-spreadsheet = %+v/%v", createSheet, ok)
	}
	exportSheet, ok := catalog.ActionDefinition("google", "sheets-export-worksheet")
	if !ok || !exportSheet.NeedsFiles || exportSheet.ValidateProps(json.RawMessage(
		`{"spreadsheet_id":"sheet-1","sheet_id":0,"format":"csv"}`)) != nil {
		t.Fatalf("sheets-export-worksheet = %+v/%v", exportSheet, ok)
	}
	if exportSheet.ValidateProps(json.RawMessage(
		`{"spreadsheet_id":"sheet-1","sheet_id":0,"format":"csv","return_as_text":true}`)) == nil {
		t.Fatal("Sheets export accepted return_as_text")
	}
	for _, name := range []string{
		"custom-api-call", "get-attachment", "stop-watch", "gmail_get_mail", "gmail_search_mail",
		"request-approval-in-email", "google-docs-find-document", "insert_row", "sheets_get_next_rows",
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
	for _, scope := range []string{
		microsoftMailSendScope, microsoftMailReadWriteScope, microsoftCalendarReadScope, microsoftCalendarWriteScope,
		microsoftFilesReadScope, microsoftFilesReadWriteScope, microsoftChannelCreateScope, microsoftSitesReadScope,
		microsoftFilesReadWriteAll,
	} {
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
		"download-attachment":     {"downloadAttachment", MicrosoftOutlookPieceName, `{"messageId":"AAMk"}`, []string{microsoftMailReadScope}},
		"upload-onedrive-file":    {"upload_onedrive_file", MicrosoftOneDrivePieceName, `{"fileName":"note.txt","file":{"file_id":"file_abc"}}`, []string{microsoftFilesReadWriteScope}},
		"download-file":           {"download_file", MicrosoftOneDrivePieceName, `{"lookupBy":"id","fileIdentifier":"item-1"}`, []string{microsoftFilesReadScope}},
		"custom-api-call":         {"custom_api_call", MicrosoftOutlookPieceName, `{"url":"/me","method":"GET"}`, []string{"User.Read"}},
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
	send, _ := catalog.ActionDefinition("microsoft", "send-email")
	if !send.NeedsFiles {
		t.Fatal("Outlook send-email should open the file broker")
	}
	if send.ValidateProps(json.RawMessage(
		`{"recipients":["recipient@example.com"],"subject":"Hello","bodyFormat":"text","body":"Body","attachments":[{"file_id":"file_abc"}]}`)) != nil {
		t.Fatal("valid Outlook attachment was rejected")
	}
	prepared, err := send.PrepareProps(json.RawMessage(
		`{"recipients":["recipient@example.com"],"subject":"Hello","bodyFormat":"text","body":"Body","attachments":[{"file_id":"file_abc"}]}`))
	if err != nil || !strings.Contains(string(prepared), `"file":{"file_id":"file_abc"}`) {
		t.Fatalf("Outlook attachment prepare = %s / %v", prepared, err)
	}
	for _, group := range []map[string]microsoftActionSpec{microsoftActions, microsoftTeamsActions, microsoftSharePointActions} {
		for name := range group {
			if _, ok := catalog.ActionDefinition("microsoft", name); !ok {
				t.Fatalf("reviewed Microsoft action %q is unbound", name)
			}
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
	for _, name := range []string{"request-approval-in-mail", "request-approval-in-channel", "request-approval-direct-message"} {
		if _, ok := catalog.ActionDefinition("microsoft", name); ok {
			t.Fatalf("unapproved Microsoft action %q was accepted", name)
		}
	}
	outlookAPI, _ := catalog.ActionDefinition("microsoft", "custom-api-call")
	if outlookAPI.ValidateProps(json.RawMessage(`{"url":"https://graph.microsoft.com/v1.0/me","method":"GET"}`)) != nil {
		t.Fatal("valid Microsoft Graph custom API URL was rejected")
	}
	if outlookAPI.ValidateProps(json.RawMessage(`{"url":"https://evil.example/v1.0/me","method":"GET"}`)) == nil {
		t.Fatal("Microsoft custom API accepted a host outside graph.microsoft.com")
	}
	createChannel, ok := catalog.ActionDefinition("microsoft", "create-channel")
	if !ok || createChannel.PieceName != MicrosoftTeamsPieceName || createChannel.ValidateProps(json.RawMessage(
		`{"teamId":"team-1","channelDisplayName":"alerts"}`)) != nil {
		t.Fatalf("create-channel = %+v/%v", createChannel, ok)
	}
	sendChannel, ok := catalog.ActionDefinition("microsoft", "send-channel-message")
	if !ok || sendChannel.ValidateProps(json.RawMessage(
		`{"teamId":"team-1","channelId":"channel-1","contentType":"text","content":"hello"}`)) != nil {
		t.Fatalf("send-channel-message = %+v/%v", sendChannel, ok)
	}
	uploadSharePoint, ok := catalog.ActionDefinition("microsoft", "upload-sharepoint-file")
	if !ok || !uploadSharePoint.NeedsFiles || uploadSharePoint.PieceName != MicrosoftSharePointPieceName ||
		uploadSharePoint.ValidateProps(json.RawMessage(
			`{"siteId":"site-1","driveId":"drive-1","file":{"file_id":"file_abc"},"parentFolder":"/","fileName":"note.txt"}`)) != nil {
		t.Fatalf("upload-sharepoint-file = %+v/%v", uploadSharePoint, ok)
	}
	if uploadSharePoint.ValidateProps(json.RawMessage(
		`{"siteId":"site-1","driveId":"drive-1","file":{},"parentFolder":"/","fileName":"note.txt"}`)) == nil {
		t.Fatal("SharePoint upload accepted an empty file object")
	}
	teamsAPI, ok := catalog.ActionDefinition("microsoft", "teams-custom-api-call")
	if !ok || teamsAPI.ValidateProps(json.RawMessage(`{"url":"/teams","method":"GET"}`)) != nil {
		t.Fatalf("teams-custom-api-call = %+v/%v", teamsAPI, ok)
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
	star, ok := catalog.TriggerDefinition("github", "new-star")
	if !ok || star.Event != "watch" || star.GitHubHookEvent != "watch" {
		t.Fatalf("new-star = %+v/%v", star, ok)
	}
	push, ok := catalog.TriggerDefinition("github", "new-push")
	if !ok || push.Event != "push" || push.GitHubHookEvent != "push" {
		t.Fatalf("new-push = %+v/%v", push, ok)
	}
	release, ok := catalog.TriggerDefinition("github", "new-release")
	if !ok || release.Event != "release" || release.GitHubHookEvent != "release" {
		t.Fatalf("new-release = %+v/%v", release, ok)
	}
	review, ok := catalog.TriggerDefinition("github", "new-review")
	if !ok || review.Event != "pull_request_review" || review.GitHubHookEvent != "pull_request_review" {
		t.Fatalf("new-review = %+v/%v", review, ok)
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
	createFile, ok := catalog.ActionDefinition("github", "create-or-update-file")
	if !ok || !createFile.NeedsFiles || createFile.PrepareProps == nil || createFile.ValidateProps(json.RawMessage(
		`{"owner":"oryxa","repo":"api","path":"docs/readme.md","message":"Add readme","file":{"file_id":"file_abc"}}`)) != nil {
		t.Fatalf("create-or-update-file = %+v/%v", createFile, ok)
	}
	preparedFile, err := createFile.PrepareProps(json.RawMessage(
		`{"owner":"oryxa","repo":"api","path":"docs/readme.md","message":"Add readme","file":{"file_id":"file_abc"}}`))
	if err != nil || !strings.Contains(string(preparedFile), `"content":{"file_id":"file_abc"}`) ||
		strings.Contains(string(preparedFile), `"file":`) {
		t.Fatalf("GitHub file prepare = %s / %v", preparedFile, err)
	}
	getFile, ok := catalog.ActionDefinition("github", "get-file-content")
	if !ok || getFile.NeedsFiles || getFile.ValidateProps(json.RawMessage(
		`{"owner":"oryxa","repo":"api","path":"docs/readme.md"}`)) != nil {
		t.Fatalf("get-file-content = %+v/%v", getFile, ok)
	}
	if getFile.ValidateProps(json.RawMessage(`{"owner":"oryxa","repo":"api","path":"/etc/passwd"}`)) == nil {
		t.Fatal("GitHub get-file-content accepted a rooted path")
	}
	deleteFile, ok := catalog.ActionDefinition("github", "delete-file")
	if !ok || deleteFile.NeedsFiles || deleteFile.ValidateProps(json.RawMessage(
		`{"owner":"oryxa","repo":"api","path":"docs/readme.md","message":"Remove readme","sha":"abc1234"}`)) != nil {
		t.Fatalf("delete-file = %+v/%v", deleteFile, ok)
	}
	if deleteFile.ValidateProps(json.RawMessage(
		`{"owner":"oryxa","repo":"api","path":"docs/readme.md","message":"Remove readme"}`)) == nil {
		t.Fatal("GitHub delete-file accepted a missing blob sha")
	}
	searchCode, ok := catalog.ActionDefinition("github", "search-code")
	if !ok || searchCode.NeedsFiles || searchCode.ValidateProps(json.RawMessage(`{"q":"repo:oryxa/api filename:readme"}`)) != nil {
		t.Fatalf("search-code = %+v/%v", searchCode, ok)
	}
	gist, ok := catalog.ActionDefinition("github", "create-gist")
	if !ok || gist.RunnerActionName != "github_create_gist" || !slices.Contains(gist.RequiredScopes, "gist") ||
		gist.ValidateProps(json.RawMessage(`{"public":false,"filename":"note.txt","content":"hello"}`)) != nil {
		t.Fatalf("create-gist = %+v/%v", gist, ok)
	}
	graphql, ok := catalog.ActionDefinition("github", "raw-graphql-query")
	if !ok || graphql.RunnerActionName != "rawGraphqlQuery" ||
		graphql.ValidateProps(json.RawMessage(`{"query":"query { viewer { login } }"}`)) != nil {
		t.Fatalf("raw-graphql-query = %+v/%v", graphql, ok)
	}
	dispatch, ok := catalog.ActionDefinition("github", "dispatch-workflow")
	if !ok || dispatch.PrepareProps == nil || dispatch.ValidateProps(json.RawMessage(
		`{"owner":"oryxa","repo":"api","workflow_id":"ci.yml","ref":"main"}`)) != nil {
		t.Fatalf("dispatch-workflow = %+v/%v", dispatch, ok)
	}
	if dispatch.ValidateProps(json.RawMessage(
		`{"owner":"oryxa","repo":"api","workflow_id":"ci.yml","ref":"main","wait_for_completion":true}`)) == nil {
		t.Fatal("GitHub dispatch-workflow accepted wait_for_completion")
	}
	preparedDispatch, err := dispatch.PrepareProps(json.RawMessage(
		`{"owner":"oryxa","repo":"api","workflow_id":"ci.yml","ref":"main"}`))
	if err != nil || !strings.Contains(string(preparedDispatch), `"waitForCompletion":false`) ||
		!strings.Contains(string(preparedDispatch), `"owner":"oryxa"`) {
		t.Fatalf("GitHub workflow prepare = %s / %v", preparedDispatch, err)
	}
	githubAPI, ok := catalog.ActionDefinition("github", "custom-api-call")
	if !ok || githubAPI.PrepareProps == nil || githubAPI.ValidateProps(json.RawMessage(
		`{"url":"/user","method":"GET"}`)) != nil {
		t.Fatalf("GitHub custom-api-call = %+v/%v", githubAPI, ok)
	}
	if githubAPI.ValidateProps(json.RawMessage(`{"url":"https://api.github.com/user","method":"GET"}`)) != nil {
		t.Fatal("GitHub custom API rejected api.github.com")
	}
	if githubAPI.ValidateProps(json.RawMessage(`{"url":"https://evil.example/user","method":"GET"}`)) == nil {
		t.Fatal("GitHub custom API accepted a host outside api.github.com")
	}
	for _, name := range []string{"create-commit-comment", "find-issue"} {
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
	drive, ok := catalog.TriggerDefinition("google", "drive-file-changed")
	if !ok || drive.PieceName != GoogleDrivePieceName || drive.GoogleWatch != "drive" ||
		!slices.Contains(drive.RequiredScopes, googleDriveFileReadScope) || drive.GoogleTTL != 24*time.Hour {
		t.Fatalf("google drive-file-changed = %+v/%v", drive, ok)
	}
	if mail.ValidateProps(json.RawMessage(`{}`)) != nil || mail.ValidateProps(json.RawMessage(`{"unexpected":true}`)) == nil {
		t.Fatal("Google trigger properties are not strict")
	}
}
