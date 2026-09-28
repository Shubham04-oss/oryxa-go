// Package google is what Oryxa's Google integrations share: one OAuth
// client, registered once in Google Cloud and named "google", which gmail,
// googledrive and googlecalendar all sign in through; and the push channel
// Drive and Calendar both deliver through.
//
// Revoking any token of the client "removes all OAuth 2.0 scopes previously
// granted to a project", for every toolkit. So the core must not revoke on
// disconnecting one Google app while the same member still has another
// connected with the same account. Every Google toolkit's test label is the
// account's email address, which is how the core can tell.
//
// Confirmed against Google's documentation on 2026-09-28:
//   - https://developers.google.com/identity/protocols/oauth2/web-server:
//     the endpoints; client_id and client_secret as POST body parameters;
//     space-delimited scopes; access_type=offline for a refresh token;
//     revoking with a form-encoded token, answered 200; refresh tokens that
//     last until revoked, and are not replaced on a refresh.
//   - https://accounts.google.com/.well-known/openid-configuration: S256 in
//     code_challenge_methods_supported, and the revocation endpoint.
//   - https://developers.google.com/workspace/drive/api/guides/push and
//     https://developers.google.com/workspace/calendar/api/guides/push: a
//     channel's token (at most 256 characters) comes back in
//     X-Goog-Channel-Token on every notification; X-Goog-Message-Number
//     numbers a channel's messages; the sync message that opens a channel is
//     always number 1; Drive's notifications have no body.
package google

import (
	"slices"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
)

// Client is the OAuth client registration every Google toolkit signs in
// through.
const Client = "google"

// Method is the sign-in every Google toolkit offers, asking for that
// toolkit's own scopes.
func Method(scopes ...string) integration.Method {
	return integration.Method{
		Kind: integration.KindOAuth2Code,
		OAuth2: &integration.OAuth2{
			Client:      Client,
			AuthURL:     "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL:    "https://oauth2.googleapis.com/token",
			RevokeURL:   "https://oauth2.googleapis.com/revoke",
			RevokeStyle: integration.RevokeRFC7009,
			Scopes:      slices.Clone(scopes),
			PKCE:        true,
			AuthParams: map[string]string{
				// A refresh token, so the connection works while the member
				// is away.
				"access_type": "offline",
				// Google hands out a refresh token only at a first consent.
				// One client serves three toolkits, so a member's second
				// Google app would otherwise get none.
				"prompt": "consent",
				// include_granted_scopes stays off, so a connection's token
				// carries its own toolkit's scopes and no others.
			},
			ClientAuth: integration.ClientAuthBody,
		},
	}
}

// ChannelWebhook is how Drive and Calendar push: a channel per subscription,
// whose token is the secret the core generates, sent back in a header on
// every notification.
//
// Message numbers belong to a channel, and start again at 1 on a new one, so
// a renewed channel needs an endpoint of its own; on the old one, its
// messages would be taken for repeats.
func ChannelWebhook() *integration.Webhook {
	return &integration.Webhook{
		Scope: integration.HookScopeConnection,
		Verify: integration.Verify{
			Type:  integration.VerifyStaticToken,
			Token: &integration.Locator{Header: "X-Goog-Channel-Token"},
		},
		DeliveryID: integration.Locator{Header: "X-Goog-Message-Number"},
		Handshake:  integration.HandshakeGoogleChannelSync,
	}
}
