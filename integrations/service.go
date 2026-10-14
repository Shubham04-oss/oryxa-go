// Package integrations assembles the integrations service for a host: it
// connects members' apps and runs what follows from that (see
// docs/design/integrations/README.md).
//
// It ships off. Every merge to main deploys and production runs every
// service by default, so until INTEGRATIONS_ENABLED=true the service does not
// mount, migrate or start workers, and needs none of its configuration.
package integrations

import (
	"context"
	"embed"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// Name is how ORYXA_SERVICES selects the service.
const Name = "integrations"

// Definition is how a host builds the service. Nothing calls it by a URL
// variable yet, and it consumes no events.
var Definition = host.Definition{Name: Name, Enabled: serviceconfig.Enabled, Migrate: Migrate, Mount: Mount}

//go:embed migrations/*.sql
var migrations embed.FS

// migrationsAdvisoryLock keeps replicas that start together from installing
// the schema at once. The other services take theirs from 7263510400 to
// realtime's 7263511000.
const migrationsAdvisoryLock int64 = 7263511100

// Migrate installs the service's schema, once it is switched on. Off, it
// touches nothing, so a release that carries the service inert leaves the
// database as it found it.
func Migrate(ctx context.Context, platform host.Platform) error {
	if !serviceconfig.Enabled(platform.Getenv) {
		return nil
	}
	if platform.Database == nil {
		return host.ErrDatabaseRequired
	}
	return platform.Database.Migrate(ctx, Name, migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

// Mount assembles the service on what the host shares. Off, it says so once
// and serves nothing, unless ORYXA_SERVICES names it: asked for by name and
// not switched on, it refuses to start rather than quietly run without it.
func Mount(_ context.Context, platform host.Platform) (host.Service, error) {
	if !serviceconfig.Enabled(platform.Getenv) {
		if namedIn(platform.Getenv("ORYXA_SERVICES")) {
			return host.Service{}, fmt.Errorf("ORYXA_SERVICES names %s, but INTEGRATIONS_ENABLED is not true; set it, or leave %s out of ORYXA_SERVICES", Name, Name)
		}
		platform.Logger.Info("integrations is off: set INTEGRATIONS_ENABLED=true to run it")
		return host.Service{}, nil
	}
	config, err := serviceconfig.FromEnv(platform.Getenv)
	if err != nil {
		return host.Service{}, err
	}
	if platform.Database == nil {
		return host.Service{}, host.ErrDatabaseRequired
	}
	verifier, err := serviceauth.NewVerifier(serviceauth.VerifierConfig{
		Keys: kms.NewFromConfig(platform.AWS), Audience: Name, Callers: config.ServiceCallers,
	})
	if err != nil {
		return host.Service{}, err
	}
	if len(config.ServiceCallers) == 0 {
		platform.Logger.Warn("INTEGRATIONS_SERVICE_CALLERS is not set: every internal call is refused")
	}
	server := httpserver.New(verifier, platform.Logger)
	return host.Service{
		Handler:  server,
		Routes:   server.Patterns(),
		Timeouts: host.Timeouts{Read: 15 * time.Second, Write: 30 * time.Second},
	}, nil
}

// namedIn reports whether services, ORYXA_SERVICES's value, names this
// service itself rather than leaving the process to run every service.
func namedIn(services string) bool {
	for _, name := range strings.Split(services, ",") {
		if strings.TrimSpace(name) == Name {
			return true
		}
	}
	return false
}
