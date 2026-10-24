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
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/integrations/egress"
	connectorhost "github.com/Shubham04-oss/oryxa-go/integrations/host"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/connect"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/events"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/orgapps"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/tools"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/vault"
	"github.com/Shubham04-oss/oryxa-go/integrations/manifest"
	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/outbox"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// Name is how ORYXA_SERVICES selects the service.
const Name = "integrations"

// Definition is how a host builds the service. Nothing calls it by a URL
// variable yet. It consumes auth's departures from its events queue, which
// is required only once the service is switched on.
var Definition = host.Definition{
	Name: Name, EventsQueueVariable: "INTEGRATIONS_EVENTS_QUEUE_URL",
	Enabled: serviceconfig.Enabled, Migrate: Migrate, Mount: Mount,
}

// Subscriptions are the detail types integrations consumes, by source, for a
// host or provisioning to route to its queue.
func Subscriptions() map[string][]string { return events.Subscriptions() }

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
func Mount(ctx context.Context, platform host.Platform) (host.Service, error) {
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
	connectors, err := startConnectors(ctx, platform, config.ConnectorsDir, config.ConnectorLauncher)
	if err != nil {
		return host.Service{}, err
	}
	service := host.Service{
		Handler:  server,
		Timeouts: host.Timeouts{Read: 15 * time.Second, Write: 30 * time.Second},
		Close:    func() { _ = connectors.Close() },
	}
	connections, workers, err := mountOrgApps(ctx, server, platform, config, connectors.Manifest)
	if err == nil {
		var toolWorkers []host.Worker
		toolWorkers, err = mountTools(server, platform, config, connectors, connections)
		service.Workers = append(workers, toolWorkers...)
	}
	if err != nil {
		service.Close()
		return host.Service{}, err
	}
	service.Routes = server.Patterns()
	return service, nil
}

// startConnectors starts every connector binary in dir, each named by its
// manifest id. One that does not come up stops the start: a connector that is
// shipped is meant to run.
func startConnectors(ctx context.Context, platform host.Platform, dir, launcher string) (*connectorhost.Host, error) {
	connectors, err := connectorhost.New(connectorhost.Config{Logger: platform.Logger, Launcher: launcher})
	if err != nil {
		return nil, err
	}
	if dir == "" {
		platform.Logger.Warn("INTEGRATIONS_CONNECTORS_DIR is not set: no connector runs")
		return connectors, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		_ = connectors.Close()
		return nil, fmt.Errorf("INTEGRATIONS_CONNECTORS_DIR: %w", err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		spec := connectorhost.Spec{ID: entry.Name(), Path: filepath.Join(dir, entry.Name())}
		if err := connectors.Start(ctx, spec); err != nil {
			_ = connectors.Close()
			return nil, fmt.Errorf("start the %s connector: %w", spec.ID, err)
		}
		platform.Logger.Info("integrations connector started", "connector", spec.ID)
	}
	return connectors, nil
}

// mountTools serves tool calls and runs the egress proxy connectors reach
// apps through. Both need signed-in connections and the keys handles are
// signed with; without either, tool calls are off and it says why.
func mountTools(server *httpserver.Server, platform host.Platform, config serviceconfig.Config, connectors *connectorhost.Host, connections *connect.Service) ([]host.Worker, error) {
	switch {
	case connections == nil:
		platform.Logger.Warn("tool calls are off: they need connections (INTEGRATIONS_KMS_KEY_ID and INTEGRATIONS_PUBLIC_URL)")
		return nil, nil
	case config.EgressKeys.Current == "":
		platform.Logger.Warn("INTEGRATIONS_EGRESS_KEYS is not set: tool calls are off")
		return nil, nil
	}
	// Listening here, not in the worker, so an address in use stops the start
	// instead of leaving every tool call to fail.
	listener, err := net.Listen("tcp", config.BrokerAddress)
	if err != nil {
		return nil, fmt.Errorf("INTEGRATIONS_BROKER_ADDRESS: %w", err)
	}
	tools.Routes(server, tools.New(platform.Database.Pool, connectors, tools.Config{
		Keys: config.EgressKeys, BrokerOrigin: "http://" + listener.Addr().String(),
	}), platform.Logger)
	broker := &http.Server{
		Handler:           &egress.Proxy{Keys: config.EgressKeys, Connections: connections},
		ReadHeaderTimeout: 10 * time.Second,
	}
	return []host.Worker{func(ctx context.Context) {
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = broker.Shutdown(shutdown)
		}()
		if err := broker.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			platform.Logger.Error("integrations egress proxy stopped", "error", err)
		}
	}}, nil
}

// mountOrgApps serves the org apps API and loads Oryxa's own apps, then the
// sign-ins, whose service it returns (nil when they are off). Sign-ins bring
// the connections API and the outbox dispatcher that publishes their events,
// which it answers as workers for the host to run. Without a KMS key nothing
// can be sealed, so it serves nothing and says so; apps that were configured
// but cannot be loaded stop the start.
func mountOrgApps(ctx context.Context, server *httpserver.Server, platform host.Platform, config serviceconfig.Config, manifestOf func(string) (*manifest.Manifest, bool)) (*connect.Service, []host.Worker, error) {
	if config.KMSKeyID == "" {
		platform.Logger.Warn("INTEGRATIONS_KMS_KEY_ID is not set: the org apps API is off")
		return nil, nil, nil
	}
	sealer, err := vault.New(secrets.KMSDataKeys{Client: kms.NewFromConfig(platform.AWS), KeyID: config.KMSKeyID}, config.KMSKeyID)
	if err != nil {
		return nil, nil, err
	}
	apps := orgapps.New(platform.Database.Pool, sealer)
	for _, app := range config.OryxaApps {
		if _, _, err := apps.Load(ctx, app.App, orgapps.Input{
			ClientID: app.ClientID, Secret: app.Secret, WebhookSecret: app.WebhookSecret, Extra: app.Extra,
		}); err != nil {
			return nil, nil, fmt.Errorf("load Oryxa's %s app: %w", app.App, err)
		}
	}
	orgapps.Routes(server, apps, platform.Logger)
	if config.PublicURL == "" {
		platform.Logger.Warn("INTEGRATIONS_PUBLIC_URL is not set: connecting apps is off")
		return nil, nil, nil
	}
	connections := connect.New(platform.Database.Pool, sealer, apps, connect.Config{
		RedirectURI: config.PublicURL + connect.CallbackPath, ReturnOrigins: config.ReturnOrigins,
		Manifest: manifestOf, Logger: platform.Logger,
	})
	connect.Routes(server, connections, platform.Logger)
	signer, err := platform.EventAuth.Signer(Name)
	if err != nil {
		return nil, nil, err
	}
	publisher, err := events.NewPublisher(eventbridge.NewFromConfig(platform.AWS), platform.EventBusName, signer)
	if err != nil {
		return nil, nil, err
	}
	table, err := outbox.NewTable(platform.Database.Pool, events.OutboxTable)
	if err != nil {
		return nil, nil, err
	}
	workers := []host.Worker{outbox.NewDispatcher(table, publisher, platform.Logger).Run}
	consumers, err := mountDepartures(platform, config, connections)
	if err != nil {
		return nil, nil, err
	}
	return connections, append(workers, consumers...), nil
}

// mountDepartures consumes auth's departures from the events queue: a member
// leaving an organization, or the organization deleted, revokes the
// connections they made there. It answers the consumer and the sweep of
// handled events as workers, or none, and says so, without a queue.
func mountDepartures(platform host.Platform, config serviceconfig.Config, connections *connect.Service) ([]host.Worker, error) {
	if config.EventsQueueURL == "" {
		platform.Logger.Warn("INTEGRATIONS_EVENTS_QUEUE_URL is not set: a member who leaves an organization keeps their connections")
		return nil, nil
	}
	handled := events.NewHandled(platform.Database.Pool, platform.Logger)
	consumer, err := events.NewConsumer(departures{connections, handled},
		platform.EventAuth.Verifier(eventauth.MaxAge(events.SignatureMaxAge)), platform.Logger)
	if err != nil {
		return nil, err
	}
	queue := sqs.NewFromConfig(platform.AWS)
	return []host.Worker{
		func(ctx context.Context) { consumer.Poll(ctx, queue, config.EventsQueueURL) },
		handled.RunForget,
	}, nil
}

// departures is what the consumer hands auth's events to: the connections,
// and the record of the events already handled.
type departures struct {
	*connect.Service
	*events.Handled
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
