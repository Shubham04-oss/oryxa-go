package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/nativeclient"
)

// brokerNative is the broker of a toolkit the integrations service serves
// (APPS_NATIVE_TOOLKITS). It is not a domain.Broker yet: no connection is
// recorded under it until the integrations service reports its connections.
const brokerNative = "native"

// Native is the integrations service's internal API, as apps uses it.
// nativeclient.Client satisfies it.
type Native interface {
	Connect(ctx context.Context, request nativeclient.ConnectRequest) (string, error)
}

// nativeLevel is the level of every connect made here: the org-level
// connection arrives with the admin screens.
const nativeLevel = "personal"

// nativeServes says a new connect to toolkit goes to the integrations
// service. A toolkit pinned by APPS_TOOLKIT_BROKERS stays where it is pinned.
func (s *Service) nativeServes(toolkit string) bool {
	if _, pinned := s.toolkitBrokers[toolkit]; pinned {
		return false
	}
	return slices.Contains(s.nativeToolkits, toolkit)
}

// connectNative starts a member's sign-in at the integrations service and
// answers the URL their browser goes to. It checks what every connect checks,
// and records nothing: the connection itself is the integrations service's.
func (s *Service) connectNative(ctx context.Context, credentials authclient.Credentials, organizationID string, input ConnectInput) (string, error) {
	start, err := s.startConnect(ctx, credentials, organizationID, input)
	if err != nil {
		return "", err
	}
	url, err := s.native.Connect(ctx, nativeclient.ConnectRequest{
		Org: organizationID, Member: start.memberID, App: start.toolkit, Level: nativeLevel, ReturnTo: start.returnTo,
	})
	switch {
	case err == nil:
		s.logger.InfoContext(ctx, "native app connection started",
			"organization_id", organizationID, "member_id", start.memberID, "toolkit", start.toolkit)
		return url, nil
	case errors.Is(err, nativeclient.ErrNotSetUp):
		return "", fmt.Errorf("an admin needs to set up %s first: %w", start.toolkit, domain.ErrNotConnectable)
	case errors.Is(err, nativeclient.ErrBroken):
		return "", fmt.Errorf("the organization's %s app is not working; an admin needs to fix it: %w", start.toolkit, domain.ErrNotConnectable)
	case errors.Is(err, nativeclient.ErrNotFound):
		return "", fmt.Errorf("%s: %w", start.toolkit, domain.ErrNotConnectable)
	case errors.Is(err, nativeclient.ErrInvalid):
		return "", fmt.Errorf("start the connection: %w", domain.ErrInvalid)
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return "", err
	}
	s.logger.ErrorContext(ctx, "start a native app connection", "toolkit", start.toolkit, "error", err)
	return "", fmt.Errorf("start the connection: the integrations service did not answer: %w", domain.ErrUnavailable)
}

// checkNativeToolkits is what New requires of the settings: toolkits are
// slugs, and with any named there is a service to send them to.
func checkNativeToolkits(toolkits []string, native Native) ([]string, error) {
	checked := make([]string, 0, len(toolkits))
	for _, toolkit := range toolkits {
		value, err := domain.CheckToolkit(strings.TrimSpace(toolkit))
		if err != nil {
			return nil, fmt.Errorf("native toolkit %q: %w", toolkit, err)
		}
		checked = append(checked, value)
	}
	if len(checked) > 0 && native == nil {
		return nil, errors.New("native toolkits are named, and the integrations service is not configured")
	}
	return checked, nil
}
