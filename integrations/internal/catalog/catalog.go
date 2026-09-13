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
	MicrosoftOutlookPieceName     = "@activepieces/piece-microsoft-outlook"
	MicrosoftOutlookPieceVersion  = "0.5.3"
	MicrosoftCalendarPieceName    = "@activepieces/piece-microsoft-outlook-calendar"
	MicrosoftCalendarPieceVersion = "0.2.8"
	MicrosoftOneDrivePieceName    = "@activepieces/piece-microsoft-onedrive"
	MicrosoftOneDrivePieceVersion = "0.4.7"
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
	base := actionflow.Definition{Provider: "slack", PieceName: SlackPieceName,
		PieceVersion: SlackPieceVersion, ActionName: actionName}
	switch actionName {
	case "post-message":
		base.RunnerActionName = "slack_post_message"
		base.RequiredScopes = []string{"chat:write"}
		base.ValidateProps = validateSlackPostMessage
	case "add-reaction":
		base.RunnerActionName = "slack_add_reaction"
		base.RequiredScopes = []string{"reactions:write"}
		base.ValidateProps = validateSlackReaction
	case "find-channel":
		base.RunnerActionName = "slack_find_channel"
		base.RequiredScopes = []string{"channels:read", "groups:read"}
		base.ValidateProps = validateSlackFindChannel
	default:
		return actionflow.Definition{}, false
	}
	return bindActionSkill(provider, actionName, base, true)
}

func (c Static) TriggerDefinition(provider, triggerName string) (triggerflow.Definition, bool) {
	if c.PublicBaseURL == "" {
		return triggerflow.Definition{}, false
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
	if provider != "slack" || triggerName != "new-message" {
		return triggerflow.Definition{}, false
	}
	return bindTriggerSkill(provider, triggerName, triggerflow.Definition{
		Provider: "slack", PieceName: SlackPieceName, PieceVersion: SlackPieceVersion,
		TriggerName: "new-message", RouteMode: domain.WebhookRouteAppLevel,
		Event: "message", TimestampHeader: "x-slack-request-timestamp",
		TimestampWindow: 5 * time.Minute, MaxBodyBytes: 128 * 1024,
		ValidateProps: func(raw json.RawMessage) error {
			var props struct {
				IgnoreBots *bool `json:"ignoreBots"`
			}
			if err := json.Unmarshal(raw, &props); err != nil {
				return errors.New("trigger properties must be a JSON object")
			}
			return nil
		},
	}, true)
}

func (c Static) OAuthDefinition(provider string) (oauthflow.Definition, bool) {
	if c.PublicBaseURL == "" {
		return oauthflow.Definition{}, false
	}
	if provider == "github" {
		return bindConnectionSkill(provider, oauthflow.Definition{
			Provider: "github", PieceName: GitHubPieceName, PieceVersion: GitHubPieceVersion,
			RedirectURI:    c.PublicBaseURL + "/v1/oauth/callback/github",
			AllowedScopes:  []string{"admin:repo_hook", "offline_access", "public_repo", "read:user", "repo", "workflow"},
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
				"https://www.googleapis.com/auth/drive.metadata.readonly",
				"https://www.googleapis.com/auth/gmail.send",
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
				"Mail.Send", "Mail.Read", "Calendars.ReadWrite", "Calendars.Read", "Files.Read",
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
	return bindConnectionSkill(provider, oauthflow.Definition{
		Provider:     "slack",
		PieceName:    SlackPieceName,
		PieceVersion: SlackPieceVersion,
		RedirectURI:  c.PublicBaseURL + "/v1/oauth/callback/slack",
		AllowedScopes: []string{
			"channels:history", "channels:read", "chat:write",
			"groups:history", "groups:read", "reactions:write",
		},
		RequiredScopes: []string{"channels:history", "groups:history"},
		// Slack's OAuth v2 flow currently relies on the confidential client
		// secret. PKCE remains enabled by catalog entry only for providers that
		// explicitly support it.
		SupportsPKCE: false,
	}, true)
}

func validateSlackPostMessage(raw json.RawMessage) error {
	var props struct {
		Channel        string          `json:"channel"`
		Text           string          `json:"text"`
		ThreadTS       string          `json:"threadTs"`
		ReplyBroadcast bool            `json:"replyBroadcast"`
		SendAsUser     bool            `json:"sendAsUser"`
		UnfurlLinks    *bool           `json:"unfurlLinks"`
		Blocks         json.RawMessage `json:"blocks"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.Channel, 100) || len(props.Text) > 40_000 || len(props.ThreadTS) > 80 || props.SendAsUser {
		return errors.New("invalid Slack post-message properties")
	}
	if strings.TrimSpace(props.Text) == "" && len(props.Blocks) == 0 {
		return errors.New("message text or blocks are required")
	}
	if props.ReplyBroadcast && props.ThreadTS == "" {
		return errors.New("replyBroadcast requires threadTs")
	}
	if len(props.Blocks) > 0 {
		var blocks []any
		if len(props.Blocks) > 100*1024 || json.Unmarshal(props.Blocks, &blocks) != nil || len(blocks) == 0 || len(blocks) > 50 {
			return errors.New("invalid Slack blocks")
		}
	}
	return nil
}

func validateSlackReaction(raw json.RawMessage) error {
	var props struct {
		Channel     string `json:"channel"`
		TS          string `json:"ts"`
		Reaction    string `json:"reaction"`
		ReactAsUser bool   `json:"reactAsUser"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.Channel, 100) ||
		!boundedIdentifier(props.TS, 80) || props.ReactAsUser || len(props.Reaction) == 0 || len(props.Reaction) > 100 || strings.ContainsAny(props.Reaction, ": \t\r\n") {
		return errors.New("invalid Slack add-reaction properties")
	}
	return nil
}

func validateSlackFindChannel(raw json.RawMessage) error {
	var props struct {
		Name            string `json:"name"`
		ExcludeArchived *bool  `json:"excludeArchived"`
	}
	if err := decodeStrict(raw, &props); err != nil {
		return err
	}
	name := strings.TrimPrefix(strings.TrimSpace(props.Name), "#")
	if name == "" || len(name) > 80 || strings.ContainsAny(name, "\r\n\t") {
		return errors.New("invalid Slack channel name")
	}
	return nil
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
