package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// BrokerFor answers the broker a new connect to toolkit goes through, for a
// connect that does not name one. Each toolkit is served by exactly one
// broker, preferring Oryxa's own sign-in, then Composio, then Pipedream,
// unless APPS_TOOLKIT_BROKERS pins it. It decides only new connects: an
// existing connection keeps the broker it was made through, whatever this
// says today.
func (s *Service) BrokerFor(ctx context.Context, toolkit string) (string, error) {
	toolkit, err := domain.CheckToolkit(toolkit)
	if err != nil {
		return "", err
	}
	return s.brokerFor(ctx, toolkit)
}

func (s *Service) brokerFor(ctx context.Context, toolkit string) (string, error) {
	if broker, ok := s.toolkitBrokers[toolkit]; ok {
		return broker, nil
	}
	if s.oryxa != nil && s.oryxa.enabled[toolkit] {
		return domain.BrokerOryxa, nil
	}
	// With no Pipedream there is nothing to fall back to, so Composio's own
	// connect says whether it can connect the app, and no catalog is read.
	if s.pipedream == nil {
		return domain.BrokerComposio, nil
	}
	found, err := s.composio.Toolkit(ctx, toolkit)
	switch {
	case err == nil && toolkitView(found).Connectable:
		return domain.BrokerComposio, nil
	case err != nil && !errors.Is(err, composio.ErrNotFound):
		// Composio being down is not Composio lacking the app: routing it to
		// Pipedream now would split one app across two brokers.
		return "", composioError("read the app", err)
	}
	apps, err := s.pipedream.ListApps(ctx, toolkit)
	if err != nil {
		return "", fmt.Errorf("read Pipedream's apps: %w", err)
	}
	for _, app := range apps {
		if app.Slug == toolkit {
			return domain.BrokerPipedream, nil
		}
	}
	// Neither has it: Composio's connect refuses it as not connectable.
	return domain.BrokerComposio, nil
}

// errPipedreamOff is what a connect routed to Pipedream answers while this
// deployment has no Pipedream, or none that can connect yet.
func errPipedreamOff(toolkit string) error {
	return fmt.Errorf("%s connects through Pipedream, which is not available here yet: %w", toolkit, domain.ErrNotConnectable)
}

// ConnectPipedream starts a connect through Pipedream. Phase 0 has no
// Pipedream connect yet, so every one is refused as not connectable rather
// than falling through to another broker.
func (s *Service) ConnectPipedream(_ context.Context, toolkit string) error {
	return errPipedreamOff(toolkit)
}

// errNotComposio is what a Composio path answers for a connection Composio
// holds no account of, so a connection made through any other broker can
// never reach Composio with Composio's key.
func errNotComposio(connection domain.Connection) error {
	if connection.Oryxa() {
		return errOryxaNotYet(connection.Toolkit)
	}
	return fmt.Errorf("%s is connected through %s, whose tools, reads, targets and triggers are not available yet: %w",
		connection.Toolkit, connection.Broker, domain.ErrNotConnectable)
}
