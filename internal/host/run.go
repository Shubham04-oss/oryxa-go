package host

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-sdk-go-v2/otelaws"

	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/internal/gcpauth"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
	workspacestore "github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

// migrationLock serializes every service's migrations across replicas that
// start together. Several services install their schema without a lock of
// their own, and the rest lock only themselves.
const migrationLock int64 = 7263510400

// Run serves the selected services until ctx is cancelled, then takes them
// down in order.
func Run(ctx context.Context, config Config, definitions []Definition, logger *slog.Logger) error {
	selected, err := Select(definitions, config.Services)
	if err != nil {
		return err
	}
	if config.RequireEventQueues {
		if err := CheckEventQueues(selected, os.Getenv); err != nil {
			return err
		}
	}
	// Listening first tells the services where this process is, which a
	// service calling another one in the same process needs before it is
	// mounted, and lets ORYXA_ADDRESS pick a free port.
	listener, err := net.Listen("tcp", config.Address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", config.Address, err)
	}
	defer listener.Close()

	if config.AutoMigrate && config.DatabaseURL != "" {
		if err := migrate(ctx, config, selected, logger); err != nil {
			return err
		}
	}
	// The pool the services share opens only after every migration has run,
	// so each connection sees the extensions they installed: pgvector's types
	// are registered as a connection opens, and a connection can live an hour.
	platform, closePlatform, err := openPlatform(ctx, config, selected, selfURL(listener.Addr()), logger)
	if err != nil {
		return err
	}
	defer closePlatform()

	services, err := mount(ctx, platform, selected)
	if err != nil {
		return err
	}
	defer closeServices(services)
	reportEventSigning(platform.EventAuth, config.EnforceEventSignatures, logger)
	return serve(ctx, listener, services, config.ShutdownTimeout, config.DrainTimeout, logger)
}

// Migrate installs the selected services' schemas and returns.
func Migrate(ctx context.Context, config Config, definitions []Definition, logger *slog.Logger) error {
	selected, err := Select(definitions, config.Services)
	if err != nil {
		return err
	}
	if config.DatabaseURL == "" {
		return ErrDatabaseRequired
	}
	return migrate(ctx, config, selected, logger)
}

func migrate(ctx context.Context, config Config, selected []Definition, logger *slog.Logger) error {
	database, err := postgreskit.Open(ctx, postgreskit.Config{
		URL: config.DatabaseURL, ApplicationName: "oryxa-migrate", MaxConns: 4,
	})
	if err != nil {
		return fmt.Errorf("open postgres for migrations: %w", err)
	}
	defer database.Close()

	// A session lock on a connection of its own, so each service's
	// migrations still run however they choose to on the rest of the pool.
	holder, err := database.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration lock connection: %w", err)
	}
	defer holder.Release()
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLock); err != nil {
		return fmt.Errorf("take migration lock: %w", err)
	}
	defer func() {
		unlockContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := holder.Exec(unlockContext, `SELECT pg_advisory_unlock($1)`, migrationLock); err != nil {
			// Closing the pool ends the session, which releases it anyway.
			logger.Warn("release migration lock", "error", err)
		}
	}()

	platform := Platform{
		Logger:      logger,
		Database:    database,
		DatabaseURL: config.DatabaseURL,
		Getenv:      os.Getenv,
	}
	for _, definition := range selected {
		if definition.Migrate == nil {
			continue
		}
		started := time.Now()
		scoped := platform
		scoped.Logger = logger.With("component", definition.Name)
		if err := definition.Migrate(ctx, scoped); err != nil {
			return fmt.Errorf("migrate %s: %w", definition.Name, err)
		}
		scoped.Logger.Info("schema migrated", "duration_ms", time.Since(started).Milliseconds())
	}
	return nil
}

func openPlatform(ctx context.Context, config Config, selected []Definition, self string, logger *slog.Logger) (Platform, func(), error) {
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(config.AWSRegion))
	if err != nil {
		return Platform{}, nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	otelaws.AppendMiddlewares(&awsConfig.APIOptions)
	federation, err := gcpauth.ConfigFromEnv(os.Getenv)
	if err != nil {
		return Platform{}, nil, err
	}
	google, err := gcpauth.Credentials(federation, awsConfig)
	if err != nil {
		return Platform{}, nil, err
	}
	if google != nil {
		logger.Info("reaching Google Cloud through workload identity federation", "service_account", federation.ServiceAccount)
	}

	var database *postgreskit.Database
	if config.DatabaseURL != "" {
		database, err = postgreskit.Open(ctx, postgreskit.Config{
			URL:                config.DatabaseURL,
			ApplicationName:    "oryxa",
			RequiredExtensions: config.RequiredExtensions,
			MaxConns:           config.DatabaseMaxConns,
		})
		if err != nil {
			return Platform{}, nil, err
		}
		logger.Info("postgres connected", "extensions", database.ExtensionVersions(), "max_conns", database.Pool.Config().MaxConns)
	}
	platform := Platform{
		Logger:       logger,
		Database:     database,
		DatabaseURL:  config.DatabaseURL,
		AWS:          awsConfig,
		Google:       google,
		EventBusName: config.EventBusName,
		EventAuth:    eventRegistry(awsConfig, config.EnforceEventSignatures, eventSigningKeys(os.Getenv, selected), logger),
		Getenv:       Environment(os.Getenv, selected, self),
	}
	return platform, func() { database.Close() }, nil
}

// eventRegistry builds the process's event signing identities. An event whose
// signature does not check out is refused when enforce is set, and otherwise
// logged and handled anyway.
func eventRegistry(awsConfig aws.Config, enforce bool, keys map[string]string, logger *slog.Logger) *eventauth.Registry {
	registry := eventauth.NewRegistry(newKMSResolver(awsConfig, keys), time.Now)
	if !enforce {
		registry.ReportOnly(func(source, detailType, eventID string, err error) {
			logger.Warn("event signature did not check out; handled anyway, since ORYXA_EVENT_SIGNATURES is not enforce",
				"source", source, "detail_type", detailType, "event_id", eventID, "error", err)
		})
	}
	return registry
}

// reportEventSigning says at boot how the process treats an event whose
// signature does not check out, and which services sign with a key minted at
// boot. It runs once every service has mounted, since only a service that
// publishes takes a signer: one that only consumes mints no key.
func reportEventSigning(registry *eventauth.Registry, enforce bool, logger *slog.Logger) {
	minted := registry.Minted()
	switch {
	case !enforce:
		logger.Info("event signatures are checked and reported, not enforced", "keys_minted_at_boot", minted)
	case len(minted) > 0:
		logger.Warn("event signatures are enforced, but these services sign with keys minted at boot: "+
			"whatever they published that is still queued when this process stops will be refused by the next one",
			"services", minted)
	default:
		logger.Info("event signatures are enforced")
	}
}

// eventSigningKeyVariable names the setting that gives a service a KMS key to
// sign its events with: AGENTS_EVENT_SIGNING_KEY_ID for agents. It is not the
// <SERVICE>_SERVICE_KEY_ID a service proves itself to another with, so the two
// keys are provisioned, granted and replaced apart.
func eventSigningKeyVariable(service string) string {
	return strings.ToUpper(strings.ReplaceAll(service, "-", "_")) + "_EVENT_SIGNING_KEY_ID"
}

// eventSigningKeys maps each selected service that has one to its event
// signing key.
func eventSigningKeys(getenv func(string) string, selected []Definition) map[string]string {
	keys := map[string]string{}
	for _, definition := range selected {
		if keyID := strings.TrimSpace(getenv(eventSigningKeyVariable(definition.Name))); keyID != "" {
			keys[definition.Name] = keyID
		}
	}
	return keys
}

// kmsResolver adapts the <SERVICE>_EVENT_SIGNING_KEY_ID settings to
// eventauth: a service that names a key signs and is verified through KMS,
// and a service that does not gets a process-local key instead, so events
// are signed and checked everywhere without configuration being required.
type kmsResolver struct {
	client kms.Client
	keys   map[string]string
}

func newKMSResolver(config aws.Config, keys map[string]string) eventauth.KMSResolver {
	if len(keys) == 0 {
		return nil
	}
	return &kmsResolver{client: *kms.NewFromConfig(config), keys: keys}
}

// Signer returns the service's KMS signer, or that none is configured.
func (resolver *kmsResolver) Signer(service string) (*eventauth.Signer, bool, error) {
	keyID, ok := resolver.keys[service]
	if !ok {
		return nil, false, nil
	}
	signer, err := eventauth.NewKMSSigner(&resolver.client, keyID, time.Now)
	if err != nil {
		return nil, false, err
	}
	return signer, true, nil
}

// Keys is what a verifier resolves the configured services' keys with.
func (resolver *kmsResolver) Keys() *eventauth.KMSKeys {
	return &eventauth.KMSKeys{Client: &resolver.client, KeyIDs: resolver.keys}
}

// Assemble mounts the definitions on platform and builds the handler Run
// would serve, without listening or starting any worker. It lets a test prove
// that the services a binary carries can share one listener. The returned
// function closes what they opened.
func Assemble(ctx context.Context, platform Platform, definitions []Definition) (http.Handler, func(), error) {
	selected, err := Select(definitions, nil)
	if err != nil {
		return nil, nil, err
	}
	services, err := mount(ctx, platform, selected)
	if err != nil {
		return nil, nil, err
	}
	handler, err := routes(services)
	if err != nil {
		closeServices(services)
		return nil, nil, err
	}
	return handler, func() { closeServices(services) }, nil
}

func mount(ctx context.Context, platform Platform, selected []Definition) ([]mounted, error) {
	if platform.WorkspacePolicy == nil {
		platform.WorkspacePolicy = policy.Noop{}
		if platform.Database != nil {
			provider, err := workspacePolicyProvider(ctx, workspacestore.New(platform.Database), platform.Logger)
			if err != nil {
				return nil, err
			}
			platform.WorkspacePolicy = provider
		}
	}
	services := make([]mounted, 0, len(selected))
	for _, definition := range selected {
		scoped := platform
		scoped.Logger = platform.Logger.With("component", definition.Name)
		service, err := definition.Mount(ctx, scoped)
		if err != nil {
			closeServices(services)
			return nil, fmt.Errorf("mount %s: %w", definition.Name, err)
		}
		services = append(services, mounted{name: definition.Name, service: service})
	}
	return services, nil
}

// serve runs the services on listener until ctx is cancelled. On the way
// down, the services' work in flight drains (Service.Drain) alongside the
// requests, within drainTimeout, and both before the workers stop, because a
// request may have left work behind for one of them, such as an event in an
// outbox, and a run in flight still needs the workers that record it.
func serve(ctx context.Context, listener net.Listener, services []mounted, shutdownTimeout, drainTimeout time.Duration, logger *slog.Logger) error {
	handler, err := routes(services)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		// Read and write deadlines are each service's to choose; see
		// withDeadlines.
		IdleTimeout: 60 * time.Second,
		ErrorLog:    slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	workersContext, stopWorkers := context.WithCancel(context.WithoutCancel(ctx))
	defer stopWorkers()
	var workers sync.WaitGroup
	names := make([]string, 0, len(services))
	for _, entry := range services {
		names = append(names, entry.name)
		for index, worker := range entry.service.Workers {
			workers.Go(func() {
				worker(workersContext)
				if workersContext.Err() == nil {
					logger.Error("worker stopped before shutdown", "component", entry.name, "worker", index)
				}
			})
		}
	}

	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	logger.Info("oryxa listening", "address", listener.Addr().String(), "services", names)

	var serveErr error
	select {
	case <-ctx.Done():
	case err := <-served:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = fmt.Errorf("serve: %w", err)
		}
	}

	logger.Info("shutting down", "timeout", shutdownTimeout.String(), "drain_timeout", drainTimeout.String())
	drained := drainServices(context.WithoutCancel(ctx), services, drainTimeout, logger)
	// A request streaming a run ends when the run does, so requests are given
	// the drain's time as well as their own.
	drainContext, cancelDrain := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout+shutdownTimeout)
	if err := server.Shutdown(drainContext); err != nil {
		logger.Error("requests did not drain in time", "error", err)
	}
	cancelDrain()
	<-drained

	stopWorkers()
	stopped := make(chan struct{})
	go func() {
		workers.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(shutdownTimeout):
		logger.Error("workers did not stop in time", "timeout", shutdownTimeout.String())
	}
	return serveErr
}

// drainServices calls every service's Drain at once, each given drainTimeout,
// and closes the channel it returns once all of them have returned.
func drainServices(ctx context.Context, services []mounted, drainTimeout time.Duration, logger *slog.Logger) <-chan struct{} {
	drained := make(chan struct{})
	var draining sync.WaitGroup
	for _, entry := range services {
		if entry.service.Drain == nil {
			continue
		}
		draining.Go(func() {
			started := time.Now()
			deadline, cancel := context.WithTimeout(ctx, drainTimeout)
			defer cancel()
			entry.service.Drain(deadline)
			logger.Info("work in flight drained", "component", entry.name, "took", time.Since(started).Round(time.Millisecond).String())
		})
	}
	go func() {
		draining.Wait()
		close(drained)
	}()
	return drained
}

func closeServices(services []mounted) {
	for index := len(services) - 1; index >= 0; index-- {
		if closer := services[index].service.Close; closer != nil {
			closer()
		}
	}
}

// selfURL is how this process reaches its own listener.
func selfURL(address net.Addr) string {
	tcp, ok := address.(*net.TCPAddr)
	if !ok {
		return "http://" + address.String()
	}
	host := "127.0.0.1"
	if tcp.IP != nil && !tcp.IP.IsUnspecified() {
		host = tcp.IP.String()
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(tcp.Port))
}
