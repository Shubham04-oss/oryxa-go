// Package github is Oryxa's own GitHub integration.
//
// So far it declares how a member signs in to GitHub and how GitHub's
// webhooks are verified. Its tools, readers and triggers arrive in phase 2,
// built on go-github, and these two sections are frozen from then on:
// connections are made against them.
//
// Confirmed against GitHub's documentation on 2026-09-28:
//   - https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps:
//     the authorize and token URLs; client_id and client_secret as POST
//     parameters; a space-delimited scope; PKCE, S256 only, "strongly
//     recommended"; and expiring tokens, whose refresh answers "a new access
//     token and a new refresh token" and retires the old pair.
//   - https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps:
//     repo includes "repository webhooks".
//   - https://docs.github.com/en/rest/apps/oauth-applications#delete-an-app-token,
//     with https://docs.github.com/en/rest/authentication/authenticating-to-the-rest-api#using-basic-authentication:
//     revoking one token with the client's own credentials, where deleting
//     the grant would "delete all OAuth tokens associated with the
//     application for the user".
//   - https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries:
//     X-Hub-Signature-256, "sha256=" and a hex HMAC-SHA256 of the raw body;
//     no timestamp.
//   - https://docs.github.com/en/webhooks/webhook-events-and-payloads#delivery-headers:
//     X-GitHub-Delivery, and the ping sent when a hook is created.
package github

import "github.com/Shubham04-oss/oryxa-go/apps/internal/integration"

// Toolkit is GitHub's slug, as Composio and apps spell it.
const Toolkit = "github"

// Integration is GitHub, as apps' registry holds it.
type Integration struct{}

// New answers GitHub's integration.
func New() Integration { return Integration{} }

// Manifest describes GitHub to the core.
func (Integration) Manifest() integration.Manifest {
	return integration.Manifest{
		Toolkit: Toolkit, Version: 1, Name: "GitHub", APIBase: "https://api.github.com",
		Auth: auth(), Webhook: webhook(),
	}
}

// Tools answers GitHub's tools: none until phase 2.
func (Integration) Tools() map[string]integration.ToolFunc { return nil }

// auth signs a member in through an OAuth app, Oryxa's or their
// organization's own, or with a personal access token instead.
func auth() integration.Auth {
	return integration.Auth{
		Methods: []integration.Method{
			{
				Kind: integration.KindOAuth2Code,
				OAuth2: &integration.OAuth2{
					Client:   "github",
					AuthURL:  "https://github.com/login/oauth/authorize",
					TokenURL: "https://github.com/login/oauth/access_token",
					// Revoking deletes this connection's token and nothing
					// else. Deleting the grant would revoke every token the
					// app holds for the GitHub user, and the same person may
					// be connected in another organization.
					RevokeStyle: integration.RevokeGitHubToken,
					// repo reaches private repositories and their webhooks;
					// read:org, the organizations whose repositories they
					// are.
					Scopes:     []string{"repo", "read:org"},
					PKCE:       true,
					ClientAuth: integration.ClientAuthBody,
					// An OAuth app's tokens last until revoked unless the app
					// opts in to expiring ones, and those replace their
					// refresh token on every refresh.
					Rotating: true,
				},
			},
			{
				// A personal access token, classic or fine-grained.
				Kind:  integration.KindAPIToken,
				Place: &integration.Place{In: integration.InHeader, Name: "Authorization", Template: "Bearer {token}"},
			},
		},
		Test: integration.Test{Path: "/user", Label: "/login", AccountRef: "/id"},
	}
}

// webhook verifies GitHub's repository hooks. Each hook's secret is one the
// core makes for the subscription. GitHub signs every delivery, the ping
// included, but sends no timestamp, so a replay is caught by dedupe alone.
func webhook() *integration.Webhook {
	return &integration.Webhook{
		Scope: integration.HookScopeConnection,
		Verify: integration.Verify{
			Type:      integration.VerifyHMACSHA256,
			Signature: &integration.Locator{Header: "X-Hub-Signature-256", Prefix: "sha256="},
			Encoding:  integration.EncodingHex,
			Signed:    "{body}",
		},
		DeliveryID: integration.Locator{Header: "X-GitHub-Delivery"},
		Handshake:  integration.HandshakeGitHubPing,
	}
}
