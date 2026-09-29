package surfaces

import (
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/appsclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

// What the service is wired from is what the rest of Oryxa already has: these
// fail to compile if any of them drifts from the interface the service reads it
// through.
var (
	_ Requests   = (*interactions.Store)(nil)
	_ Asks       = (*interactions.Store)(nil)
	_ Asks       = (*interactions.Service)(nil)
	_ Tickets    = (*interactions.Service)(nil)
	_ Slack      = (*appsclient.Client)(nil)
	_ SlackUsers = (*slackrouter.Store)(nil)
	_ Notes      = (*threads.Service)(nil)
	_ links      = (*Store)(nil)
	_ jobQueue   = (*Store)(nil)
	_ jobHandler = (*Service)(nil)
)
