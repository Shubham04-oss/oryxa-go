// Package googlecalendar is Oryxa's own Google Calendar integration.
//
// It signs in through the shared Google client and pushes through Google's
// channels (see package google). So far it declares only those; its tools,
// readers and triggers arrive in phase 2, and both sections are frozen from
// then on.
//
// Confirmed against Google's documentation on 2026-09-28:
//   - https://developers.google.com/workspace/calendar/api/auth: calendar
//     also shares and deletes calendars, which calendar.readonly and
//     calendar.events together do not.
//   - https://developers.google.com/workspace/calendar/api/v3/reference/calendars/get:
//     calendars.get takes calendar.readonly, and "primary" is the signed-in
//     account's own calendar, whose id is its address.
//   - https://developers.google.com/workspace/calendar/api/guides/push: the
//     same channels as Drive's; see package google.
package googlecalendar

import (
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/google"
)

// Toolkit is Google Calendar's slug, as Composio and apps spell it.
const Toolkit = "googlecalendar"

// Integration is Google Calendar, as apps' registry holds it.
type Integration struct{}

// New answers Google Calendar's integration.
func New() Integration { return Integration{} }

// Manifest describes Google Calendar to the core.
func (Integration) Manifest() integration.Manifest {
	return integration.Manifest{
		Toolkit: Toolkit, Version: 1, Name: "Google Calendar", APIBase: "https://www.googleapis.com/calendar/v3",
		Auth: auth(), Webhook: google.ChannelWebhook(),
	}
}

// Tools answers Google Calendar's tools: none until phase 2.
func (Integration) Tools() map[string]integration.ToolFunc { return nil }

func auth() integration.Auth {
	return integration.Auth{
		// Read every calendar the member can, and change events, without
		// the power to share or delete calendars.
		Methods: []integration.Method{google.Method(
			"https://www.googleapis.com/auth/calendar.readonly",
			"https://www.googleapis.com/auth/calendar.events",
		)},
		Test: integration.Test{Path: "/calendars/primary", Label: "/id", AccountRef: "/id"},
	}
}
