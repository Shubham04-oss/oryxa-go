// Package application is what apps does: connect a member's third-party apps
// through Composio, run tools in them as that member, subscribe to the events
// they deliver, and turn Composio's webhooks into events on the bus.
//
// Composio holds every credential and answers to one project API key, which
// reaches every user in the project. So nothing outside this package ever
// chooses whose account a call uses: every call is decided here, from auth's
// answer about the caller, and made with the Composio user this organization
// files that member under.
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
}

type Dependencies struct {
	Store     Store
	Directory Directory
	Composio  Composio
	Logger    *slog.Logger
	Clock     func() time.Time
}

type Service struct {
	store         Store
	directory     Directory
	composio      Composio
	logger        *slog.Logger
	now           func() time.Time
	publicBaseURL string
	returnOrigins []*url.URL
	webhookSecret string
	// wait paces the short poll a connection's callback makes for Composio
	// to finish; a var-like field so tests need not sleep.
	wait func(context.Context, time.Duration) error

	toolsMu sync.Mutex
	tools   map[string]cachedTool
}

func New(dependencies Dependencies, config Config) (*Service, error) {
	service := &Service{
		store:         dependencies.Store,
		directory:     dependencies.Directory,
		composio:      dependencies.Composio,
		logger:        dependencies.Logger,
		now:           dependencies.Clock,
		publicBaseURL: strings.TrimRight(config.PublicBaseURL, "/"),
		webhookSecret: config.WebhookSecret,
		wait:          sleep,
		tools:         map[string]cachedTool{},
	}
	if service.logger == nil {
		service.logger = slog.New(slog.DiscardHandler)
	}
	if service.now == nil {
		service.now = time.Now
	}
	for _, origin := range config.ReturnOrigins {
		parsed, err := url.Parse(strings.TrimSpace(origin))
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("return origin %q must be absolute", origin)
		}
		service.returnOrigins = append(service.returnOrigins, parsed)
	}
	return service, nil
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
