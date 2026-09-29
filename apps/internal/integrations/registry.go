// Package integrations is Oryxa's own integrations, one package per toolkit,
// and the registry of the ones this build ships.
//
// Each toolkit's package declares its app's sign-in and webhook, and from
// phase 2 its tools, readers and triggers, all through the contract in
// apps/internal/integration. Shipping one is not switching it on: a toolkit
// takes the Oryxa route only once APPS_ORYXA_TOOLKITS names it.
package integrations

import (
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/github"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/gmail"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/googlecalendar"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/googledrive"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/notion"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/slack"
)

// All is every integration this build ships, in the order phase 2 builds
// them.
func All() []integration.Integration {
	return []integration.Integration{
		github.New(),
		slack.New(),
		gmail.New(),
		googledrive.New(),
		googlecalendar.New(),
		notion.New(),
	}
}

// NewRegistry registers every integration this build ships. It fails if any
// manifest does not validate, so a broken one stops the service at start
// rather than a member at connect.
func NewRegistry() (*integration.Registry, error) {
	registry := integration.NewRegistry()
	for _, shipped := range All() {
		if err := registry.Register(shipped); err != nil {
			return nil, err
		}
	}
	return registry, nil
}
