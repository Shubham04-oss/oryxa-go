package slackbot

import _ "embed"

// manifest is the Slack app's manifest, as shipped. It is the only place the
// app's scopes, events and URLs are written, and manifest_test.go pins it.
//
//go:embed manifest.yaml
var manifest []byte

// Manifest is the Slack app's manifest, for creating or updating the app from
// it.
func Manifest() []byte { return append([]byte(nil), manifest...) }
