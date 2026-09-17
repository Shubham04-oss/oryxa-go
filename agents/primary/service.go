// Package primary assembles the agents service for a host: today, that is
// only its health check. adk-go, the agents themselves and everything they
// need to run a member's turn live under internal/ and cmd/devui, which is
// still how the agents actually run; the run and stream API this package
// will one day serve has not been built yet.
package primary

import (
	"context"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
)

// Name is how ORYXA_SERVICES selects agents primary.
const Name = "agents"

// Definition is how a host builds agents primary. Migrate is nil: primary
// keeps nothing in the shared database today. ADK sessions live in their own
// agents_primary schema, opened and auto-migrated by cmd/devui through
// adksession, which needs a PostgreSQL pool of its own rather than the
// host's shared one.
var Definition = host.Definition{Name: Name, Migrate: nil, Mount: Mount}

// Mount assembles primary on what the host shares. It reads primary's own
// configuration, so a misconfiguration still fails at start even though
// nothing here uses it yet, and serves only the health check its old
// cmd/primary entrypoint did: there is no run API for the host to carry
// until one is built.
func Mount(_ context.Context, platform host.Platform) (host.Service, error) {
	if _, err := serviceconfig.FromEnv(platform.Getenv); err != nil {
		return host.Service{}, err
	}
	server := httpserver.New()
	return host.Service{
		Handler: server,
		Routes:  server.Patterns(),
		Timeouts: host.Timeouts{
			Read: 15 * time.Second,
			// No write deadline: an agent run streams its answer, which can
			// outlive any fixed write deadline, so a streaming handler will
			// need to set its own through http.ResponseController.
			Write: 0,
		},
	}, nil
}
