// Package slack is Oryxa's own Slack integration.
//
// A member connects Slack as themselves: with a user token from Slack's
// user-only flow, not a bot token. A workspace has one bot, which sees every
// channel it has been invited to, whoever asks through it; a member's own
// token sees what that member can.
//
// So far it declares how a member signs in and how Slack's events are
// verified. Its tools, readers and triggers arrive in phase 2, built on
// slack-go, and these two sections are frozen from then on.
//
// Confirmed against Slack's documentation on 2026-09-28:
//   - https://docs.slack.dev/authentication/installing-with-oauth#v2-user-access:
//     the v2_user authorize endpoint, taking user scopes in scope,
//     comma-separated; oauth.v2.user.access "is compliant to the OAuth 2.0
//     RFC", answering the user token as the top-level access_token, which
//     oauth.v2.access does not do for an install with user scopes only.
//   - https://docs.slack.dev/reference/methods/oauth.v2.user.access: the
//     client's credentials preferably in HTTP Basic; the refresh_token grant.
//   - https://docs.slack.dev/authentication/using-pkce: PKCE marks an app a
//     public client, "a one-way operation", after which its client must omit
//     client_secret; it is for desktop and mobile apps, not a server.
//   - https://docs.slack.dev/authentication/using-token-rotation: "Refresh
//     tokens are designed to be used once".
//   - https://docs.slack.dev/reference/methods/auth.revoke and
//     https://docs.slack.dev/reference/methods/auth.test, with
//     https://docs.slack.dev/apis/web-api (arguments as GET query parameters
//     or POST): auth.test names the team and the user, and, as every method,
//     answers HTTP 200 with "ok": false to refuse.
//   - https://docs.slack.dev/authentication/verifying-requests-from-slack:
//     X-Slack-Signature, "v0=" and a hex HMAC-SHA256 of
//     "v0:" + X-Slack-Request-Timestamp + ":" + the body; the timestamp in
//     seconds, within five minutes.
//   - https://docs.slack.dev/apis/events-api and
//     https://docs.slack.dev/reference/events/url_verification: event_id,
//     authorizations naming one member who can see the event, and the
//     signed url_verification challenge.
package slack

import "github.com/Shubham04-oss/oryxa-go/apps/internal/integration"

// Toolkit is Slack's slug, as Composio and apps spell it.
const Toolkit = "slack"

// Integration is Slack, as apps' registry holds it.
type Integration struct{}

// New answers Slack's integration.
func New() Integration { return Integration{} }

// Manifest describes Slack to the core.
func (Integration) Manifest() integration.Manifest {
	return integration.Manifest{
		Toolkit: Toolkit, Version: 1, Name: "Slack", APIBase: "https://slack.com/api",
		Auth: auth(), Webhook: webhook(),
	}
}

// Tools answers Slack's tools: none until phase 2.
func (Integration) Tools() map[string]integration.ToolFunc { return nil }

func auth() integration.Auth {
	return integration.Auth{
		Methods: []integration.Method{{
			Kind: integration.KindOAuth2Code,
			OAuth2: &integration.OAuth2{
				Client:      "slack",
				AuthURL:     "https://slack.com/oauth/v2_user/authorize",
				TokenURL:    "https://slack.com/api/oauth.v2.user.access",
				RevokeURL:   "https://slack.com/api/auth.revoke",
				RevokeStyle: integration.RevokeSlackAuthRevoke,
				// What the member's own access must cover: listing their
				// channels, public and private, reading them, posting as
				// them, and naming the people in them.
				Scopes: []string{
					"channels:read", "groups:read", "channels:history", "groups:history", "chat:write", "users:read",
				},
				ScopeSep: integration.ScopeSeparatorComma,
				// Slack takes PKCE only from an app marked a public client,
				// which cannot be undone and leaves it without its secret.
				// Oryxa's app is a server's, and keeps its secret. Reviewed
				// at G2.
				PKCE:       false,
				ClientAuth: integration.ClientAuthHeader,
				// Once an app turns token rotation on, a refresh token is
				// used once.
				Rotating: true,
			},
		}},
		// auth.test refuses with HTTP 200 and "ok": false, so the label
		// resolving is what passes it. Events are routed by the member's own
		// user id: Slack sends an event once, naming one member who can see
		// it, and routing it by workspace would show a private channel to
		// members who are not in it.
		Test: integration.Test{Path: "/auth.test", Label: "/team", AccountRef: "/user_id"},
	}
}

// webhook verifies Slack's Events API. An app has one request URL and one
// signing secret for every workspace that installs it, so the endpoint is the
// deployment's; an organization's own Slack app would bring its own.
func webhook() *integration.Webhook {
	return &integration.Webhook{
		Scope: integration.HookScopeDeployment,
		Verify: integration.Verify{
			Type:      integration.VerifyHMACSHA256,
			Signature: &integration.Locator{Header: "X-Slack-Signature", Prefix: "v0="},
			Encoding:  integration.EncodingHex,
			Signed:    "v0:{timestamp}:{body}",
			Timestamp: &integration.Locator{Header: "X-Slack-Request-Timestamp", Unit: integration.UnitSeconds},
			Tolerance: 300,
		},
		DeliveryID: integration.Locator{JSON: "/event_id"},
		Handshake:  integration.HandshakeSlackURLVerification,
	}
}
