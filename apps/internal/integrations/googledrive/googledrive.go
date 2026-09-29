// Package googledrive is Oryxa's own Google Drive integration.
//
// It signs in through the shared Google client and pushes through Google's
// channels (see package google). So far it declares only those; its tools,
// readers and triggers arrive in phase 2, and both sections are frozen from
// then on.
//
// Confirmed against Google's documentation on 2026-09-28:
//   - https://developers.google.com/workspace/drive/api/guides/api-specific-auth:
//     drive can "view and manage all your Drive files"; drive.readonly is
//     restricted as drive is, and drive.file reaches only files the app
//     created or was handed.
//   - https://developers.google.com/workspace/drive/api/reference/rest/v3/about/get:
//     about.get requires fields, and answers the user's emailAddress and
//     permissionId.
//   - https://developers.google.com/workspace/drive/api/guides/push: see
//     package google.
package googledrive

import (
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/google"
)

// Toolkit is Google Drive's slug, as Composio and apps spell it.
const Toolkit = "googledrive"

// Integration is Google Drive, as apps' registry holds it.
type Integration struct{}

// New answers Google Drive's integration.
func New() Integration { return Integration{} }

// Manifest describes Google Drive to the core.
func (Integration) Manifest() integration.Manifest {
	return integration.Manifest{
		Toolkit: Toolkit, Version: 1, Name: "Google Drive", APIBase: "https://www.googleapis.com/drive/v3",
		Auth: auth(), Webhook: google.ChannelWebhook(),
	}
}

// Tools answers Google Drive's tools: none until phase 2.
func (Integration) Tools() map[string]integration.ToolFunc { return nil }

func auth() integration.Auth {
	return integration.Auth{
		// Knowledge reads every file a member can, and tools create and
		// change them. Drive has no scope that edits without deleting.
		Methods: []integration.Method{google.Method("https://www.googleapis.com/auth/drive")},
		// The label is the address, as for every Google toolkit; the ref is
		// permissionId, which does not change when the address does.
		Test: integration.Test{Path: "/about?fields=user", Label: "/user/emailAddress", AccountRef: "/user/permissionId"},
	}
}
