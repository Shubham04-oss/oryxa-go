package catalog

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/triggerflow"
)

const (
	SlackPieceName    = "@activepieces/piece-slack"
	SlackPieceVersion = "0.17.10"
)

type Static struct {
	PublicBaseURL string
}

func (c Static) TriggerDefinition(pieceName, pieceVersion, triggerName string) (triggerflow.Definition, bool) {
	if c.PublicBaseURL == "" || pieceName != SlackPieceName || pieceVersion != SlackPieceVersion || triggerName != "new-message" {
		return triggerflow.Definition{}, false
	}
	return triggerflow.Definition{
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
	}, true
}

func (c Static) OAuthDefinition(provider string) (oauthflow.Definition, bool) {
	if provider != "slack" || c.PublicBaseURL == "" {
		return oauthflow.Definition{}, false
	}
	return oauthflow.Definition{
		Provider:       "slack",
		PieceName:      SlackPieceName,
		PieceVersion:   SlackPieceVersion,
		RedirectURI:    c.PublicBaseURL + "/v1/oauth/callback/slack",
		AllowedScopes:  []string{"channels:history", "groups:history"},
		RequiredScopes: []string{"channels:history", "groups:history"},
		// Slack's OAuth v2 flow currently relies on the confidential client
		// secret. PKCE remains enabled by catalog entry only for providers that
		// explicitly support it.
		SupportsPKCE: false,
	}, true
}
