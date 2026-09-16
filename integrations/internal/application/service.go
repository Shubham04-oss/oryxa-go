// Package application is integrations' use cases: subscriptions members make
// and stop, the deliveries providers send them, the workers that keep
// registrations alive and remove them, and the events that end subscriptions.
package application

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/connectionsclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

type Store interface {
	CreateSubscription(context.Context, domain.Subscription, *domain.Event) error
	ActivateSubscription(ctx context.Context, organizationID, subscriptionID, registrationID string, expiresAt, renewAt *time.Time, at time.Time, eventFor func(domain.Subscription) (domain.Event, error)) (domain.Subscription, error)
	DeletePendingSubscription(ctx context.Context, organizationID, subscriptionID string) error
	Subscription(ctx context.Context, organizationID, subscriptionID string) (domain.Subscription, error)
	SubscriptionByRoute(ctx context.Context, route string) (domain.Subscription, error)
	Subscriptions(ctx context.Context, organizationID string, filter domain.SubscriptionFilter) ([]domain.Subscription, error)
	AccountSubscriptions(ctx context.Context, appID, accountKey string) ([]domain.Subscription, error)
	StopSubscriptions(ctx context.Context, selector domain.SubscriptionSelector, reason, stoppedBy string, at time.Time, eventFor func(domain.Subscription) (domain.Event, error)) ([]domain.Subscription, error)
	FailSubscription(ctx context.Context, organizationID, subscriptionID, leaseToken, reason string, cleanup bool, at time.Time, eventFor func(domain.Subscription) (domain.Event, error)) (domain.Subscription, error)
	RecordDelivery(ctx context.Context, subscriptionID, key string, at time.Time, event domain.Event) (bool, error)
	ScheduleRenewal(ctx context.Context, subscriptionID string, at time.Time) error
	DeleteDeliveriesBefore(ctx context.Context, before time.Time) (int64, error)
	ClaimRenewals(ctx context.Context, now time.Time, limit int, leaseToken string, leaseUntil time.Time) ([]domain.Subscription, error)
	SaveRenewal(ctx context.Context, subscriptionID, leaseToken string, expiresAt, renewAt *time.Time, at time.Time) error
	RetryRenewal(ctx context.Context, subscriptionID, leaseToken string, next time.Time) error
	ClaimCleanups(ctx context.Context, now time.Time, limit int, leaseToken string, leaseUntil time.Time) ([]domain.Subscription, error)
	FinishCleanup(ctx context.Context, subscriptionID, leaseToken string, at time.Time) error
	RetryCleanup(ctx context.Context, subscriptionID, leaseToken string, next time.Time) error
	HoldsConnection(ctx context.Context, organizationID, connectionID string) (bool, error)
	PutAppWebhook(context.Context, domain.AppWebhook) (domain.AppWebhook, error)
	AppWebhook(ctx context.Context, organizationID, appID string) (domain.AppWebhook, error)
	AppWebhookByRoute(ctx context.Context, route string) (domain.AppWebhook, error)
	DeleteAppWebhooks(ctx context.Context, organizationID, appID string) error
}

// Directory tells who the caller is in an organization.
type Directory interface {
	Membership(ctx context.Context, organizationID string, credentials authclient.Credentials) (domain.OrgMembership, error)
}

// Connections is what integrations asks connections.
type Connections interface {
	Connection(ctx context.Context, organizationID, connectionID, accessToken string) (domain.Connection, error)
	Token(ctx context.Context, request connectionsclient.TokenRequest) (domain.Credential, error)
	AddHold(ctx context.Context, organizationID, connectionID string) error
	ReleaseHold(ctx context.Context, organizationID, connectionID string) error
	App(ctx context.Context, organizationID, appID, accessToken string) (connectionsclient.App, error)
}

// Sealer envelope-encrypts secrets.
type Sealer interface {
	Seal(ctx context.Context, binding secrets.Binding, plaintext []byte) (domain.SealedData, error)
	Open(ctx context.Context, binding secrets.Binding, sealed domain.SealedData) ([]byte, error)
}

type Dependencies struct {
	Store       Store
	Directory   Directory
	Connections Connections
	Sealer      Sealer
	Providers   providers.Registry
	HTTP        *http.Client
	Logger      *slog.Logger
	Clock       func() time.Time
}

type Config struct {
	// PublicBaseURL is where providers reach integrations; webhook URLs are
	// built on it.
	PublicBaseURL string
}

type Service struct {
	store         Store
	directory     Directory
	connections   Connections
	sealer        Sealer
	providers     providers.Registry
	http          *http.Client
	logger        *slog.Logger
	clock         func() time.Time
	publicBaseURL string
}

func New(dependencies Dependencies, config Config) *Service {
	service := &Service{
		store:         dependencies.Store,
		directory:     dependencies.Directory,
		connections:   dependencies.Connections,
		sealer:        dependencies.Sealer,
		providers:     dependencies.Providers,
		http:          dependencies.HTTP,
		logger:        dependencies.Logger,
		clock:         dependencies.Clock,
		publicBaseURL: strings.TrimRight(config.PublicBaseURL, "/"),
	}
	if service.http == nil {
		service.http = providers.NewHTTPClient()
	}
	if service.logger == nil {
		service.logger = slog.New(slog.DiscardHandler)
	}
	if service.clock == nil {
		service.clock = time.Now
	}
	return service
}

func (s *Service) now() time.Time { return s.clock().UTC() }

// WebhookURL is where a provider delivers for a route.
func (s *Service) WebhookURL(provider, route string) string {
	return s.publicBaseURL + "/v1/webhooks/" + provider + "/" + route
}

func (s *Service) provider(name string) (providers.Provider, error) {
	provider, ok := s.providers[name]
	if !ok {
		return providers.Provider{}, fmt.Errorf("%s delivers no events to integrations: %w", name, domain.ErrInvalid)
	}
	return provider, nil
}

// member is who the caller is in an organization.
func (s *Service) member(ctx context.Context, credentials authclient.Credentials, organizationID string) (domain.OrgMembership, error) {
	if strings.TrimSpace(organizationID) == "" {
		return domain.OrgMembership{}, domain.ErrNotFound
	}
	if !credentials.Present() {
		return domain.OrgMembership{}, domain.ErrUnauthorized
	}
	return s.directory.Membership(ctx, organizationID, credentials)
}

func (s *Service) event(detailType string, data any, occurredAt time.Time) (domain.Event, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return domain.Event{}, fmt.Errorf("encode %s: %w", detailType, err)
	}
	id, err := newID("evt")
	if err != nil {
		return domain.Event{}, err
	}
	return domain.Event{ID: id, DetailType: detailType, OccurredAt: occurredAt, Data: encoded}, nil
}

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newID is a prefixed random identifier, such as sub_…
func newID(prefix string) (string, error) {
	value := make([]byte, 15)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return prefix + "_" + strings.ToLower(idEncoding.EncodeToString(value)), nil
}

// newRoute is the opaque token a webhook URL names.
func newRoute() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate route: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

// newSecret is what a registered webhook is signed or marked with.
func newSecret() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return hex.EncodeToString(value), nil
}
