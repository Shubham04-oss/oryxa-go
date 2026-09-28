// Package notion is Oryxa's own Notion integration.
//
// So far it declares how a member signs in and how Notion's webhooks are
// verified. Its tools, readers and triggers arrive in phase 2, and these two
// sections are frozen from then on.
//
// Confirmed against Notion's documentation on 2026-09-28:
//   - https://developers.notion.com/docs/authorization: the authorize URL,
//     with owner=user; the token request authenticated with HTTP Basic;
//     "Refreshing an access token will generate a new access token and a new
//     refresh token". It documents no scopes, since what a connection reaches
//     is the pages the member shares with it, and no PKCE.
//   - https://developers.notion.com/reference/revoke-token: POST
//     /v1/oauth/revoke, HTTP Basic, and the token in a JSON body.
//   - https://developers.notion.com/reference/versioning: "The Notion-Version
//     header must be included in all REST API requests".
//   - https://developers.notion.com/reference/get-self: GET /v1/users/me
//     answers the connection's bot user, its id and bot.workspace_name.
//   - https://developers.notion.com/reference/webhooks: a subscription is
//     created in the integration's settings, which sends one unsigned
//     {"verification_token": …}; that token keys X-Notion-Signature,
//     "sha256=" and a hex HMAC-SHA256 of the raw body. No timestamp.
//   - https://developers.notion.com/reference/webhooks-events-delivery: each
//     event's id; up to eight attempts over about 24 hours; accessible_by,
//     the users and bots that can see the entity, for a public connection.
package notion

import "github.com/Shubham04-oss/oryxa-go/apps/internal/integration"

// Toolkit is Notion's slug, as Composio and apps spell it.
const Toolkit = "notion"

// APIVersion is the Notion-Version the test call sends. Notion refuses a
// request without one.
const APIVersion = "2026-03-11"

// Integration is Notion, as apps' registry holds it.
type Integration struct{}

// New answers Notion's integration.
func New() Integration { return Integration{} }

// Manifest describes Notion to the core.
func (Integration) Manifest() integration.Manifest {
	return integration.Manifest{
		Toolkit: Toolkit, Version: 1, Name: "Notion", APIBase: "https://api.notion.com/v1",
		Auth: auth(), Webhook: webhook(),
	}
}

// Tools answers Notion's tools: none until phase 2.
func (Integration) Tools() map[string]integration.ToolFunc { return nil }

func auth() integration.Auth {
	return integration.Auth{
		Methods: []integration.Method{{
			Kind: integration.KindOAuth2Code,
			OAuth2: &integration.OAuth2{
				Client:      "notion",
				AuthURL:     "https://api.notion.com/v1/oauth/authorize",
				TokenURL:    "https://api.notion.com/v1/oauth/token",
				RevokeURL:   "https://api.notion.com/v1/oauth/revoke",
				RevokeStyle: integration.RevokeRFC7009JSON,
				// Notion has no scopes: the integration's capabilities are
				// set in its settings, and a connection reaches the pages the
				// member picks while connecting.
				Scopes: []string{},
				// Notion's OAuth documents no PKCE, and always takes the
				// client secret. Reviewed at G2.
				PKCE:       false,
				AuthParams: map[string]string{"owner": "user"},
				ClientAuth: integration.ClientAuthHeader,
				Rotating:   true,
			},
		}},
		// The label is the workspace's name. Events are routed by the
		// connection's bot user: an event's accessible_by lists the bots
		// that can see its page, where routing by workspace would show
		// members pages they were never given.
		Test: integration.Test{
			Path: "/users/me", Label: "/bot/workspace_name", AccountRef: "/id",
			Headers: map[string]string{"Notion-Version": APIVersion},
		},
	}
}

// webhook verifies Notion's events. A subscription belongs to the
// integration, not to a workspace, so the endpoint is the deployment's, and
// its secret is the verification token Notion sends it once.
func webhook() *integration.Webhook {
	return &integration.Webhook{
		Scope: integration.HookScopeDeployment,
		Verify: integration.Verify{
			Type:      integration.VerifyHMACSHA256,
			Signature: &integration.Locator{Header: "X-Notion-Signature", Prefix: "sha256="},
			Encoding:  integration.EncodingHex,
			Signed:    "{body}",
		},
		DeliveryID: integration.Locator{JSON: "/id"},
		Handshake:  integration.HandshakeNotionVerification,
	}
}
