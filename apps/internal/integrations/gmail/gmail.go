// Package gmail is Oryxa's own Gmail integration.
//
// It signs in through the shared Google client (see package google). So far
// it declares only that and how Gmail's push notifications are verified; its
// tools, readers and triggers arrive in phase 2, and these two sections are
// frozen from then on.
//
// Confirmed against Google's documentation on 2026-09-28:
//   - https://developers.google.com/workspace/gmail/api/auth/scopes:
//     gmail.modify reads, composes and sends, and cannot permanently delete;
//     it is restricted, as gmail.readonly is.
//   - https://developers.google.com/workspace/gmail/api/guides/push:
//     users.watch publishes to a Pub/Sub topic that
//     gmail-api-push@system.gserviceaccount.com may publish to, renewed at
//     least every 7 days; a notification's data is
//     {"emailAddress", "historyId"}.
//   - https://docs.cloud.google.com/pubsub/docs/authenticate-push-subscriptions:
//     a push carries a Bearer JWT issued by https://accounts.google.com, for
//     an audience that is the push endpoint's URL unless set otherwise,
//     naming the subscription's service account in a verified email claim.
//   - https://docs.cloud.google.com/pubsub/docs/push: the body's
//     message.messageId.
package gmail

import (
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/google"
)

// Toolkit is Gmail's slug, as Composio and apps spell it.
const Toolkit = "gmail"

// Integration is Gmail, as apps' registry holds it.
type Integration struct{}

// New answers Gmail's integration.
func New() Integration { return Integration{} }

// Manifest describes Gmail to the core.
func (Integration) Manifest() integration.Manifest {
	return integration.Manifest{
		Toolkit: Toolkit, Version: 1, Name: "Gmail", APIBase: "https://gmail.googleapis.com/gmail/v1",
		Auth: auth(), Webhook: webhook(),
	}
}

// Tools answers Gmail's tools: none until phase 2.
func (Integration) Tools() map[string]integration.ToolFunc { return nil }

func auth() integration.Auth {
	return integration.Auth{
		// One scope covers reading what knowledge indexes, watching for new
		// mail, and the drafts, sends and labels tools need.
		Methods: []integration.Method{google.Method("https://www.googleapis.com/auth/gmail.modify")},
		// A notification names its mailbox by address, so the address is
		// what routes it.
		Test: integration.Test{Path: "/users/me/profile", Label: "/emailAddress", AccountRef: "/emailAddress"},
	}
}

// webhook verifies Pub/Sub's push of Gmail's notifications: one topic and one
// push subscription for the deployment, each push signed by Google for the
// subscription's service account, which the deployment's configuration names
// as pubsub_push_account.
func webhook() *integration.Webhook {
	return &integration.Webhook{
		Scope: integration.HookScopeDeployment,
		Verify: integration.Verify{
			Type:     integration.VerifyJWTPush,
			Issuer:   "https://accounts.google.com",
			Audience: "{endpoint_url}",
			Email:    "{config:pubsub_push_account}",
		},
		DeliveryID: integration.Locator{JSON: "/message/messageId"},
	}
}
