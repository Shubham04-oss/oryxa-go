package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/actionflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/triggerflow"
)

const (
	SlackPieceName                = "@activepieces/piece-slack"
	SlackPieceVersion             = "0.17.10"
	GitHubPieceName               = "@activepieces/piece-github"
	GitHubPieceVersion            = "0.8.11"
	GmailPieceName                = "@activepieces/piece-gmail"
	GmailPieceVersion             = "0.15.0"
	GoogleCalendarPieceName       = "@activepieces/piece-google-calendar"
	GoogleCalendarPieceVersion    = "0.10.3"
	GoogleDrivePieceName          = "@activepieces/piece-google-drive"
	GoogleDrivePieceVersion       = "0.9.1"
	GoogleDocsPieceName           = "@activepieces/piece-google-docs"
	GoogleDocsPieceVersion        = "0.4.9"
	GoogleSheetsPieceName         = "@activepieces/piece-google-sheets"
	GoogleSheetsPieceVersion      = "0.16.11"
	MicrosoftOutlookPieceName     = "@activepieces/piece-microsoft-outlook"
	MicrosoftOutlookPieceVersion  = "0.5.3"
	MicrosoftCalendarPieceName    = "@activepieces/piece-microsoft-outlook-calendar"
	MicrosoftCalendarPieceVersion = "0.2.8"
	MicrosoftOneDrivePieceName      = "@activepieces/piece-microsoft-onedrive"
	MicrosoftOneDrivePieceVersion   = "0.4.7"
	MicrosoftTeamsPieceName         = "@activepieces/piece-microsoft-teams"
	MicrosoftTeamsPieceVersion      = "0.6.1"
	MicrosoftSharePointPieceName    = "@activepieces/piece-microsoft-sharepoint"
	MicrosoftSharePointPieceVersion = "0.3.8"
)

type Static struct {
	PublicBaseURL string
}

func (c Static) ActionDefinition(provider, actionName string) (actionflow.Definition, bool) {
	if c.PublicBaseURL == "" {
		return actionflow.Definition{}, false
	}
	if provider == "github" {
		definition, ok := githubActionDefinition(actionName)
		return bindActionSkill(provider, actionName, definition, ok)
	}
	if provider == "google" {
		definition, ok := googleActionDefinition(actionName)
		return bindActionSkill(provider, actionName, definition, ok)
	}
	if provider == "microsoft" {
		definition, ok := microsoftActionDefinition(actionName)
		return bindActionSkill(provider, actionName, definition, ok)
	}
	if provider != "slack" {
		return actionflow.Definition{}, false
	}
	definition, ok := slackActionDefinition(actionName)
	return bindActionSkill(provider, actionName, definition, ok)
}

func (c Static) TriggerDefinition(provider, triggerName string) (triggerflow.Definition, bool) {
	if c.PublicBaseURL == "" {
		return triggerflow.Definition{}, false
	}
	if provider == "github" {
		base := triggerflow.Definition{Provider: "github", PieceName: GitHubPieceName, PieceVersion: GitHubPieceVersion,
			TriggerName: triggerName, RouteMode: domain.WebhookRoutePerConnection, MaxBodyBytes: 256 * 1024,
			ValidateProps: validateGitHubTrigger}
		switch triggerName {
		case "new-issue":
			base.Event, base.GitHubHookEvent = "issues", "issues"
		case "new-pull-request":
			base.Event, base.GitHubHookEvent = "pull_request", "pull_request"
		case "new-star":
			base.Event, base.GitHubHookEvent = "watch", "watch"
		case "new-push":
			base.Event, base.GitHubHookEvent = "push", "push"
		case "new-release":
			base.Event, base.GitHubHookEvent = "release", "release"
		case "new-review":
			base.Event, base.GitHubHookEvent = "pull_request_review", "pull_request_review"
		default:
			return triggerflow.Definition{}, false
		}
		return bindTriggerSkill(provider, triggerName, base, true)
	}
	if provider == "microsoft" {
		base := triggerflow.Definition{Provider: "microsoft", RouteMode: domain.WebhookRoutePerConnection,
			Event: "change-notification", MaxBodyBytes: 128 * 1024, GraphTTL: 72 * time.Hour,
			ValidateProps: func(raw json.RawMessage) error {
				var props struct{}
				if err := decodeStrict(raw, &props); err != nil {
					return errors.New("Microsoft Graph trigger properties must be an empty object")
				}
				return nil
			}}
		switch triggerName {
		case "new-email":
			base.PieceName, base.PieceVersion = MicrosoftOutlookPieceName, MicrosoftOutlookPieceVersion
			base.TriggerName, base.GraphResource, base.GraphChangeType = triggerName, "/me/mailFolders('Inbox')/messages", "created"
			base.RequiredScopes = []string{"Mail.Read"}
		case "calendar-event-changed":
			base.PieceName, base.PieceVersion = MicrosoftCalendarPieceName, MicrosoftCalendarPieceVersion
			base.TriggerName, base.GraphResource, base.GraphChangeType = triggerName, "/me/events", "created,updated,deleted"
			base.RequiredScopes = []string{"Calendars.Read"}
		default:
			return triggerflow.Definition{}, false
		}
		return bindTriggerSkill(provider, triggerName, base, true)
	}
	if provider == "google" {
		base := triggerflow.Definition{Provider: "google", RouteMode: domain.WebhookRoutePerConnection,
			MaxBodyBytes: 64 * 1024, GoogleTTL: 7 * 24 * time.Hour, ValidateProps: validateGoogleTrigger}
		switch triggerName {
		case "new-email":
			base.PieceName, base.PieceVersion = GmailPieceName, GmailPieceVersion
			base.TriggerName, base.Event, base.GoogleWatch = triggerName, "mailbox-change", "gmail"
			base.RequiredScopes = []string{googleGmailMetadataScope}
		case "calendar-event-changed":
			base.PieceName, base.PieceVersion = GoogleCalendarPieceName, GoogleCalendarPieceVersion
			base.TriggerName, base.Event, base.GoogleWatch = triggerName, "channel-notification", "calendar"
			base.RequiredScopes = []string{googleCalendarReadScope}
		case "drive-file-changed":
			base.PieceName, base.PieceVersion = GoogleDrivePieceName, GoogleDrivePieceVersion
			base.TriggerName, base.Event, base.GoogleWatch = triggerName, "channel-notification", "drive"
			base.RequiredScopes = []string{googleDriveFileReadScope}
			base.GoogleTTL = 24 * time.Hour
		default:
			return triggerflow.Definition{}, false
		}
		return bindTriggerSkill(provider, triggerName, base, true)
	}
	if provider != "slack" {
		return triggerflow.Definition{}, false
	}
	definition, ok := slackTriggerDefinition(triggerName)
	return bindTriggerSkill(provider, triggerName, definition, ok)
}

func (c Static) OAuthDefinition(provider string) (oauthflow.Definition, bool) {
	if c.PublicBaseURL == "" {
		return oauthflow.Definition{}, false
	}
	if provider == "github" {
		return bindConnectionSkill(provider, oauthflow.Definition{
			Provider: "github", PieceName: GitHubPieceName, PieceVersion: GitHubPieceVersion,
			RedirectURI:    c.PublicBaseURL + "/v1/oauth/callback/github",
			AllowedScopes:  []string{"admin:org_hook", "admin:repo_hook", "gist", "offline_access", "public_repo", "read:org", "read:user", "repo", "workflow"},
			RequiredScopes: []string{"read:user"}, SupportsPKCE: false,
		}, true)
	}
	if provider == "google" {
		return bindConnectionSkill(provider, oauthflow.Definition{
			Provider: "google", PieceName: GmailPieceName, PieceVersion: GmailPieceVersion,
			RedirectURI: c.PublicBaseURL + "/v1/oauth/callback/google",
			AllowedScopes: []string{
				"email", "openid", "profile",
				"https://www.googleapis.com/auth/calendar.events",
				"https://www.googleapis.com/auth/calendar.readonly",
				"https://www.googleapis.com/auth/drive",
				"https://www.googleapis.com/auth/drive.readonly",
				"https://www.googleapis.com/auth/drive.metadata.readonly",
				"https://www.googleapis.com/auth/gmail.compose",
				"https://www.googleapis.com/auth/gmail.metadata",
				"https://www.googleapis.com/auth/gmail.modify",
				"https://www.googleapis.com/auth/gmail.readonly",
				"https://www.googleapis.com/auth/gmail.send",
				"https://www.googleapis.com/auth/documents",
				"https://www.googleapis.com/auth/documents.readonly",
				"https://www.googleapis.com/auth/spreadsheets",
				"https://www.googleapis.com/auth/spreadsheets.readonly",
			},
			RequiredScopes: []string{"email", "openid", "profile"}, SupportsPKCE: true,
		}, true)
	}
	if provider == "microsoft" {
		return bindConnectionSkill(provider, oauthflow.Definition{
			Provider: "microsoft", PieceName: MicrosoftOutlookPieceName, PieceVersion: MicrosoftOutlookPieceVersion,
			RedirectURI: c.PublicBaseURL + "/v1/oauth/callback/microsoft",
			AllowedScopes: []string{
				"openid", "profile", "email", "offline_access", "User.Read",
				"Mail.Send", "Mail.Read", "Mail.ReadWrite", "Calendars.ReadWrite", "Calendars.Read",
				"Files.Read", "Files.ReadWrite", "Files.ReadWrite.All",
				"Channel.Create", "Channel.ReadBasic.All", "ChannelMessage.Send", "ChannelMessage.Read.All",
				"Team.ReadBasic.All", "Chat.ReadWrite", "TeamMember.Read.All", "User.ReadBasic.All",
				"Presence.Read.All", "OnlineMeetingTranscript.Read.All", "OnlineMeetingRecording.Read.All",
				"Sites.Read.All", "Sites.ReadWrite.All", "Sites.Manage.All",
			},
			// Microsoft may omit offline_access from the returned scope string even
			// when it issued a refresh token. It is still requested by the OAuth
			// adapter, but cannot be a persisted-grant authorization requirement.
			RequiredScopes: []string{"openid", "profile", "User.Read"},
			SupportsPKCE:   true,
		}, true)
	}
	if provider != "slack" {
		return oauthflow.Definition{}, false
	}
	return bindConnectionSkill(provider, slackOAuthDefinition(c.PublicBaseURL), true)
}

func decodeStrict(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("properties must contain one JSON object")
	}
	return nil
}

func boundedIdentifier(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && strings.TrimSpace(value) == value && !strings.ContainsAny(value, " \t\r\n")
}

type fileRef struct {
	FileID string `json:"file_id"`
}

func oryxaFileID(value string) bool {
	return strings.HasPrefix(value, "file_") && boundedIdentifier(value, 64)
}

func optionalFileRefs(values []fileRef) bool {
	if len(values) > 5 {
		return false
	}
	for _, value := range values {
		if !oryxaFileID(value.FileID) {
			return false
		}
	}
	return true
}

func optionalFileRef(value *fileRef) bool {
	return value == nil || oryxaFileID(value.FileID)
}

func boundedFilename(value string) bool {
	return value != "" && len(value) <= 255 && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\r\n/\\") && value != "." && value != ".."
}

func optionalFilename(value string) bool {
	return value == "" || boundedFilename(value)
}

func prepareFileAttachments(raw json.RawMessage) (json.RawMessage, error) {
	var props map[string]any
	if err := json.Unmarshal(raw, &props); err != nil {
		return nil, err
	}
	items, ok := props["attachments"].([]any)
	if !ok || len(items) == 0 {
		return raw, nil
	}
	wrapped := make([]any, 0, len(items))
	for _, item := range items {
		object, _ := item.(map[string]any)
		if object == nil {
			return nil, errors.New("invalid attachment")
		}
		if _, ok := object["file"]; ok {
			wrapped = append(wrapped, object)
			continue
		}
		wrapped = append(wrapped, map[string]any{"file": object})
	}
	props["attachments"] = wrapped
	return json.Marshal(props)
}
