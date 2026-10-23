// Package application is what apps does: connect a member's third-party apps
// through Composio, or through Oryxa's own sign-in where it is switched on,
// run tools in them as that member, subscribe to the events they deliver, and
// turn Composio's webhooks into events on the bus.
//
// Composio holds every credential of its connections and answers to one
// project API key, which reaches every user in the project. So nothing outside
// this package ever chooses whose account a call uses: every call is decided
// here, from auth's answer about the caller, and made with the Composio user
// this organization files that member under. An Oryxa connection's credential
// is kept by signin, sealed, and every Composio path refuses one.
package application

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/access"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
)

// Config is what the application needs to know about where it runs.
type Config struct {
	// PublicBaseURL is where a member's browser and Composio reach this
	// process, such as https://api.oryxa.example. Callbacks are built on it.
	PublicBaseURL string
	// ReturnOrigins are where a member's browser may be sent back to after
	// connecting an app.
	ReturnOrigins []string
	// WebhookSecret verifies Composio's webhook deliveries. Without one,
	// every delivery is refused.
	WebhookSecret string
	// NoAutoSubscribe stops a newly connected app from being subscribed to
	// everything it can deliver. Connecting subscribes by default, because a
	// member who connects GitHub means to hear about GitHub; a deployment
	// that would rather they chose sets this, and a member can always say so
	// per connection.
	NoAutoSubscribe bool
	// ToolkitBrokers pins a toolkit's new connects to a broker, whatever
	// brokerFor would otherwise choose: APPS_TOOLKIT_BROKERS.
	ToolkitBrokers map[string]string
	// PipedreamDeny are Pipedream action keys agents are never offered nor
	// allowed to run: APPS_PIPEDREAM_DENY_ACTIONS.
	PipedreamDeny []string
	// PipedreamMinPoll is the shortest interval a polling Pipedream trigger
	// is deployed with, since every poll costs credits:
	// APPS_PIPEDREAM_MIN_POLL_SECONDS. Zero is the default, fifteen minutes.
	PipedreamMinPoll time.Duration
	// NativeToolkits are the toolkits new connects send to the integrations
	// service: APPS_NATIVE_TOOLKITS. Empty leaves every broker as it was.
	NativeToolkits []string
}

type Dependencies struct {
	Store     Store
	Directory Directory
	Composio  Composio
	// Pipedream is Pipedream Connect, or nil when this deployment has none.
	Pipedream Pipedream
	// Sealer seals Pipedream triggers' signing keys. Without one, a Pipedream
	// connection cannot subscribe to its app's events.
	Sealer Sealer
	Policy PolicySource
	// Native is the integrations service, required when Config.NativeToolkits
	// names a toolkit.
	Native Native
	// Oryxa is Oryxa's own sign-in, when this deployment has one: signin,
	// hooks and the integrations. Without it every Oryxa request is refused.
	Oryxa *Oryxa
	// Shadow compares who may reach a connection with SpiceDB. Nil
	// compares nothing.
	Shadow access.Comparer
	Logger *slog.Logger
	Clock  func() time.Time
}

// PolicySource reads the organization's last applied workspace file.
type PolicySource = policy.Provider

type Service struct {
	store     Store
	directory Directory
	composio  Composio
	pipedream Pipedream
	policy    PolicySource
	// toolkitBrokers are the deployment's per-toolkit broker overrides.
	toolkitBrokers map[string]string
	// native is the integrations service, and nativeToolkits the toolkits it
	// serves.
	native         Native
	nativeToolkits []string
	shadow         access.Comparer
	logger         *slog.Logger
	now            func() time.Time
	publicBaseURL  string
	returnOrigins  []*url.URL
	webhookSecret  string
	// autoSubscribe says a newly connected app is subscribed to everything it
	// can deliver.
	autoSubscribe bool
	// wait paces the short poll a connection's callback makes for Composio
	// to finish; a var-like field so tests need not sleep.
	wait func(context.Context, time.Duration) error

	toolsMu sync.Mutex
	tools   map[string]cachedTool
	// pipedreamActions holds each Pipedream app's agent-ready actions for a
	// while, under toolsMu, so resolving a PD_ slug costs no listing.
	pipedreamActions map[string]cachedPipedreamActions
	pipedreamDeny    map[string]bool
	// pipedreamTriggers is pipedreamActions for each app's triggers.
	pipedreamTriggers map[string]cachedPipedreamActions
	pipedreamMinPoll  time.Duration
	sealer            Sealer
	// pipedreamKeys holds opened signing keys briefly, so a burst of
	// deliveries, forged or not, does not make a KMS call each.
	pipedreamKeys openedKeys
	// triggers holds each app's trigger definitions for a while, so checking
	// a subscription against the event it names costs no catalog walk.
	triggers *triggerCatalog
	// subscribeWake wakes RunAutoSubscribe when a connection completes.
	subscribeWake chan struct{}

	// oryxa is Oryxa's own sign-in, or nil when this deployment has none.
	oryxa *oryxaBroker
	// connects bounds how many connects and key replacements a member starts.
	connects *RateLimiter
	// defaultsSaved says Oryxa's own OAuth apps have all been saved once.
	defaultsMu    sync.Mutex
	defaultsSaved bool

	// pipedreamAppCatalog is Pipedream's catalog, held for a day.
	pipedreamAppCatalog pipedreamCatalog
	// pipedreamCheckedAt is when the maintenance sweep last checked every
	// Pipedream account's health; it does so once a day.
	pipedreamCheckedAt time.Time
	// nativeReconciledAt is when the maintenance sweep last reconciled native
	// connections with the integrations service; it does so once an hour.
	nativeReconciledAt time.Time
}

func New(dependencies Dependencies, config Config) (*Service, error) {
	service := &Service{
		store:          dependencies.Store,
		directory:      dependencies.Directory,
		composio:       dependencies.Composio,
		pipedream:      dependencies.Pipedream,
		policy:         dependencies.Policy,
		shadow:         dependencies.Shadow,
		toolkitBrokers: config.ToolkitBrokers,
		logger:         dependencies.Logger,
		now:            dependencies.Clock,
		publicBaseURL:  strings.TrimRight(config.PublicBaseURL, "/"),
		webhookSecret:  config.WebhookSecret,
		autoSubscribe:  !config.NoAutoSubscribe,
		wait:           sleep,
		tools:          map[string]cachedTool{},
		subscribeWake:  make(chan struct{}, 1),

		pipedreamActions: map[string]cachedPipedreamActions{},
		pipedreamDeny:    map[string]bool{},

		pipedreamTriggers: map[string]cachedPipedreamActions{},
		pipedreamMinPoll:  config.PipedreamMinPoll,
		sealer:            dependencies.Sealer,
	}
	var err error
	if service.nativeToolkits, err = checkNativeToolkits(config.NativeToolkits, dependencies.Native); err != nil {
		return nil, err
	}
	service.native = dependencies.Native
	if service.pipedreamMinPoll <= 0 {
		service.pipedreamMinPoll = DefaultPipedreamMinPoll
	}
	for _, key := range config.PipedreamDeny {
		service.pipedreamDeny[strings.TrimSpace(key)] = true
	}
	if service.logger == nil {
		service.logger = slog.New(slog.DiscardHandler)
	}
	if service.now == nil {
		service.now = time.Now
	}
	service.triggers = newTriggerCatalog(service.composio, func() time.Time { return service.now() })
	origins, err := parseReturnOrigins(config.ReturnOrigins)
	if err != nil {
		return nil, err
	}
	service.returnOrigins = origins
	service.connects = NewRateLimiter(ConnectBudget, ConnectWindow, func() time.Time { return service.now() })
	if service.oryxa, err = newOryxaBroker(dependencies.Oryxa); err != nil {
		return nil, err
	}
	if service.oryxa != nil {
		// The webhook ingress is handed a zero HookSubscriptions before this
		// runs, so this is what lets a verified delivery find the member who
		// subscribed it.
		installPushRouting(service.oryxa.Store)
	}
	return service, nil
}

func parseReturnOrigins(origins []string) ([]*url.URL, error) {
	parsed := make([]*url.URL, 0, len(origins))
	for _, origin := range origins {
		value, err := url.Parse(strings.TrimSpace(origin))
		if err != nil || value.Scheme == "" || value.Host == "" {
			return nil, fmt.Errorf("return origin %q must be absolute", origin)
		}
		parsed = append(parsed, value)
	}
	return parsed, nil
}

// ReturnChecker answers the rule a member's browser is held to before it is
// sent back to a page: the page must be under one of origins, as apps'
// returnAllowed checks it. Oryxa's sign-in checks its return pages with it
// too, at the start and again at the callback.
func ReturnChecker(origins []string) (func(string) bool, error) {
	parsed, err := parseReturnOrigins(origins)
	if err != nil {
		return nil, err
	}
	return func(target string) bool { return returnAllowed(parsed, target) }, nil
}

// caller is who is asking, checked against auth on every call rather than
// trusted from anything held here.
func (s *Service) caller(ctx context.Context, credentials authclient.Credentials, organizationID string) (domain.OrgMembership, error) {
	if strings.TrimSpace(organizationID) == "" {
		return domain.OrgMembership{}, fmt.Errorf("an organization is required: %w", domain.ErrInvalid)
	}
	return s.directory.Membership(ctx, organizationID, credentials)
}

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newID is a prefixed random identifier, such as apc_…
func newID(prefix string) (string, error) {
	value := make([]byte, 15)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return prefix + "_" + strings.ToLower(idEncoding.EncodeToString(value)), nil
}

// event packages what happened for the outbox, in the envelope every Oryxa
// publisher uses.
func (s *Service) event(detailType string, data any) (domain.Event, error) {
	id, err := newID("evt")
	if err != nil {
		return domain.Event{}, err
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return domain.Event{}, fmt.Errorf("encode %s: %w", detailType, err)
	}
	return domain.Event{ID: id, DetailType: detailType, OccurredAt: s.now().UTC(), Data: encoded}, nil
}

func sleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
