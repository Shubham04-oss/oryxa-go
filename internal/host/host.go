// Package host runs Oryxa's services in one process.
//
// Each service describes itself: the routes it serves, the background work it
// needs, how to install its schema, and what it opened for itself. The host
// builds what they share once — the PostgreSQL pool, the AWS configuration,
// the logger — serves every route from one listener, runs every worker, and
// takes it all down in order: requests drain first, then the workers that
// finish what those requests started, then whatever the services opened.
//
// A service stays reachable over HTTP by the others, exactly as when each ran
// alone. When the service a URL variable names runs in this process, that
// variable defaults to this process, so a single binary needs no addresses
// for itself while a split deployment still names each one.
package host

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/auth"
	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
)

// Worker is background work a service needs running. It returns once its
// context is cancelled, so the host can wait for it on the way down.
type Worker func(context.Context)

// Timeouts bound how long one request may take to arrive and to be answered.
// Zero leaves that side without a deadline, which a handler that streams
// needs.
type Timeouts struct {
	Read  time.Duration
	Write time.Duration
}

// Service is one service, assembled and ready to run.
type Service struct {
	// Handler serves Routes. A service without an API leaves both empty.
	Handler http.Handler
	// Routes are the patterns Handler registered on its own mux, such as
	// "GET /v1/organizations/{organizationID}/teams". The host sends every
	// request for one of those paths to Handler whatever its method, so the
	// service still answers a method it does not serve, and a preflight, as
	// it always has.
	Routes   []string
	Timeouts Timeouts
	Workers  []Worker
	// Close releases what the service opened for itself. It runs after the
	// service's workers have stopped.
	Close func()
	// Drain, when set, is the first thing the host calls on the way down,
	// alongside draining requests: the service starts no new long-running
	// work, such as an agent run, and lets what is going finish until ctx
	// ends, then stops what is left. It returns once nothing is going. Its
	// workers keep running meanwhile, and are stopped after it returns.
	Drain func(ctx context.Context)
}

// Definition is how the host builds one service.
type Definition struct {
	// Name is how ORYXA_SERVICES selects the service.
	Name string
	// URLVariable names the variable other services read this service's base
	// URL from, such as AUTH_URL. Empty when nothing calls it.
	URLVariable string
	// EventsQueueVariable names the variable holding the SQS queue the
	// service consumes its events from, such as RAG_EVENTS_QUEUE_URL. Empty
	// when it consumes none. See Config.RequireEventQueues.
	EventsQueueVariable string
	// Enabled reports whether the configuration getenv reads switches the
	// service on, for a service that ships off and runs only once it is
	// configured. Nil is always on. A service that is off hears no events,
	// so CheckEventQueues does not require its queue, and a release can carry
	// it before its queue exists.
	Enabled func(getenv func(string) string) bool
	// Migrate installs the service's schema. It needs nothing but the
	// database, so migrating never waits on the rest of a service's
	// configuration. Nil when the service keeps nothing in PostgreSQL.
	Migrate func(context.Context, Platform) error
	// Mount assembles the service. It must not start anything: the host
	// starts Workers once every service is mounted and the listener serves.
	Mount func(context.Context, Platform) (Service, error)
}

// Platform is what the host builds once and every service borrows.
type Platform struct {
	// WorkspacePolicy is shared by services so version memoization is shared.
	WorkspacePolicy policy.Provider
	// Logger names the service it was handed to.
	Logger *slog.Logger
	// Database is the pool the services share, or nil when DATABASE_URL is
	// not set. The host opens and closes it.
	Database *postgreskit.Database
	// DatabaseURL is for a service that must hold a pool of its own, such as
	// one with its own search_path.
	DatabaseURL string
	// AWS carries the region and the tracing middleware. A client built from
	// it still honours AWS_ENDPOINT_URL_<SERVICE> for a local emulator.
	AWS aws.Config
	// Google reaches Google Cloud as the service account the AWS identity
	// federates to, when GOOGLE_WORKLOAD_IDENTITY_PROVIDER and
	// GOOGLE_SERVICE_ACCOUNT say which (internal/gcpauth). It is nil
	// otherwise, which every Google client takes to mean Application Default
	// Credentials. The services share it, so its token is exchanged once.
	Google       *auth.Credentials
	EventBusName string
	// EventAuth holds the event signing identities: each mounted service's
	// signer and the verifier that trusts them all. A publisher without a
	// signing key cannot publish; a consumer never runs a handler on an
	// unsigned or wrongly-signed event.
	EventAuth *eventauth.Registry
	// Getenv reads configuration. A URL variable naming a service that runs
	// in this process defaults to this process.
	Getenv func(string) string
}

// WorkspaceSettings also gives individually mounted services safe defaults.
func (platform Platform) WorkspaceSettings() policy.Provider {
	if platform.WorkspacePolicy == nil {
		return policy.Noop{}
	}
	return platform.WorkspacePolicy
}

// ErrDatabaseRequired is what a service that keeps data in PostgreSQL answers
// when DATABASE_URL is not set.
var ErrDatabaseRequired = errors.New("DATABASE_URL is required")

// Config is how the process is run, as opposed to how any one service is.
type Config struct {
	// Address is where the listener binds.
	Address string
	// Services selects which definitions run. Empty runs all of them.
	Services           []string
	DatabaseURL        string
	RequiredExtensions []string
	// DatabaseMaxConns sizes the shared pool unless DATABASE_URL sets
	// pool_max_conns itself.
	DatabaseMaxConns int32
	AutoMigrate      bool
	AWSRegion        string
	EventBusName     string
	// RequireEventQueues refuses to start while a selected service that is
	// on (Definition.Enabled) has its events queue unset. A service without
	// its queue does not fail: it warns once and then never hears the events
	// that remove what people deleted, which in a deployment is a quiet
	// data-retention gap. It is on where the deployment environment
	// (DEPLOYMENT_ENVIRONMENT, else ENVIRONMENT) is production, and
	// ORYXA_REQUIRE_EVENT_QUEUES sets it either way.
	RequireEventQueues bool
	// EnforceEventSignatures refuses an event whose signature does not check
	// out. Off, every event is still signed and checked, and a failure is
	// logged while the event is handled as it was before events were signed.
	// It waits for every service to sign with a KMS key: a key minted at boot
	// dies with the process, so enforcing with one refuses whatever was in
	// flight across each restart and each rolling deploy.
	// ORYXA_EVENT_SIGNATURES=enforce turns it on; report, the default, is off.
	EnforceEventSignatures bool
	// ShutdownTimeout bounds each stage of the way down: draining requests,
	// then waiting for workers.
	ShutdownTimeout time.Duration
	// DrainTimeout is how long work in flight (Service.Drain), such as an
	// agent run, has to finish once the process is told to stop, before what
	// is left is interrupted. Requests drain alongside it, so a run streamed
	// in a request ends with it. ORYXA_DRAIN_TIMEOUT; the whole way down must
	// fit in what the platform allows before it kills the process (ECS's
	// stopTimeout).
	DrainTimeout time.Duration
}

// ConfigFromEnv reads the process configuration.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	config := Config{
		Address:            valueOr(getenv, "ORYXA_ADDRESS", ":8080"),
		Services:           splitList(getenv("ORYXA_SERVICES")),
		DatabaseURL:        strings.TrimSpace(getenv("DATABASE_URL")),
		RequiredExtensions: splitList(getenv("POSTGRES_REQUIRED_EXTENSIONS")),
		AWSRegion:          valueOr(getenv, "AWS_REGION", "ap-south-1"),
		EventBusName:       valueOr(getenv, "EVENT_BUS_NAME", "default"),
	}
	if slices.Equal(config.Services, []string{"all"}) {
		config.Services = nil
	}
	var err error
	if config.AutoMigrate, err = boolValue(getenv, "ORYXA_AUTO_MIGRATE", true); err != nil {
		return Config{}, err
	}
	environment := strings.ToLower(strings.TrimSpace(getenv("DEPLOYMENT_ENVIRONMENT")))
	if environment == "" {
		environment = strings.ToLower(strings.TrimSpace(getenv("ENVIRONMENT")))
	}
	production := environment == "production" || environment == "prod"
	if config.RequireEventQueues, err = boolValue(getenv, "ORYXA_REQUIRE_EVENT_QUEUES", production); err != nil {
		return Config{}, err
	}
	switch signatures := strings.ToLower(strings.TrimSpace(getenv("ORYXA_EVENT_SIGNATURES"))); signatures {
	case "", "report":
	case "enforce":
		config.EnforceEventSignatures = true
	default:
		return Config{}, fmt.Errorf("ORYXA_EVENT_SIGNATURES is %q; want report or enforce", signatures)
	}
	maxConns, err := intValue(getenv, "ORYXA_DATABASE_MAX_CONNS", 32)
	if err != nil {
		return Config{}, err
	}
	config.DatabaseMaxConns = int32(maxConns)
	if config.ShutdownTimeout, err = durationValue(getenv, "ORYXA_SHUTDOWN_TIMEOUT", 30*time.Second); err != nil {
		return Config{}, err
	}
	// 20 seconds, with a run's interruption and the workers after it, fits in
	// ECS's default 30 second stopTimeout.
	if config.DrainTimeout, err = durationValue(getenv, "ORYXA_DRAIN_TIMEOUT", 20*time.Second); err != nil {
		return Config{}, err
	}
	return config, nil
}

// CheckEventQueues refuses selected services whose events queue is unset,
// naming every one, so a deployment finds them all at once rather than one
// start at a time. A service that is off (Definition.Enabled) is skipped.
func CheckEventQueues(selected []Definition, getenv func(string) string) error {
	var missing []string
	for _, definition := range selected {
		if definition.EventsQueueVariable == "" || (definition.Enabled != nil && !definition.Enabled(getenv)) {
			continue
		}
		if strings.TrimSpace(getenv(definition.EventsQueueVariable)) == "" {
			missing = append(missing, definition.EventsQueueVariable+" ("+definition.Name+")")
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("these services consume events but their queues are not set: %s; "+
			"without them they never hear what others deleted. Set them, or ORYXA_REQUIRE_EVENT_QUEUES=false to start anyway",
			strings.Join(missing, ", "))
	}
	return nil
}

// Select returns the definitions config.Services names, in the order they
// were defined, refusing a name no definition has.
func Select(definitions []Definition, names []string) ([]Definition, error) {
	known := make(map[string]bool, len(definitions))
	for _, definition := range definitions {
		if definition.Name == "" || definition.Mount == nil {
			return nil, fmt.Errorf("service definition %q is incomplete", definition.Name)
		}
		if known[definition.Name] {
			return nil, fmt.Errorf("service %q is defined twice", definition.Name)
		}
		known[definition.Name] = true
	}
	if len(names) == 0 {
		return slices.Clone(definitions), nil
	}
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		if !known[name] {
			return nil, fmt.Errorf("ORYXA_SERVICES names %q, which is not a service; the services are %s", name, strings.Join(Names(definitions), ", "))
		}
		wanted[name] = true
	}
	var selected []Definition
	for _, definition := range definitions {
		if wanted[definition.Name] {
			selected = append(selected, definition)
		}
	}
	return selected, nil
}

// Names lists the definitions' names in order.
func Names(definitions []Definition) []string {
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, definition.Name)
	}
	return names
}

// Environment layers this process's own URL under the URL variables of the
// services it runs. A variable that is set always wins, so one service can
// still be reached somewhere else.
func Environment(getenv func(string) string, running []Definition, self string) func(string) string {
	defaults := map[string]string{}
	for _, definition := range running {
		if definition.URLVariable != "" {
			defaults[definition.URLVariable] = self
		}
	}
	return func(name string) string {
		if value := getenv(name); strings.TrimSpace(value) != "" {
			return value
		}
		return defaults[name]
	}
}

func valueOr(getenv func(string) string, name, fallback string) string {
	if value := strings.TrimSpace(getenv(name)); value != "" {
		return value
	}
	return fallback
}

func splitList(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func boolValue(getenv func(string) string, name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return parsed, nil
}

func intValue(getenv func(string) string, name string, fallback int) (int, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 || parsed > 10_000 {
		return 0, fmt.Errorf("%s must be a whole number between 1 and 10000", name)
	}
	return parsed, nil
}

func durationValue(getenv func(string) string, name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration such as 30s", name)
	}
	return parsed, nil
}
