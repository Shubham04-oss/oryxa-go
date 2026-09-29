package application

import (
	"context"
	"fmt"
	"slices"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

// OfferPolicy is the member-visible part of the applied tools policy.
type OfferPolicy struct {
	Offer                *[]string `json:"offer"`
	EmployeeConnections  bool      `json:"employee_connections"`
	CanConnect           bool      `json:"can_connect"`
	AutoSubscribeDefault bool      `json:"auto_subscribe_default"`
}

// OfferedTools tells a signed-in member what this organization currently
// offers. Nil Offer means the full catalog; [] means no toolkit is offered.
func (s *Service) OfferedTools(ctx context.Context, credentials authclient.Credentials, organizationID string) (OfferPolicy, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return OfferPolicy{}, err
	}
	if s.policy == nil {
		return OfferPolicy{EmployeeConnections: true, CanConnect: !caller.Agent, AutoSubscribeDefault: s.autoSubscribe}, nil
	}
	settings, err := s.policy.Get(ctx, organizationID)
	if err != nil {
		return OfferPolicy{}, fmt.Errorf("read the organization's app policy: %w: %w", domain.ErrUnavailable, err)
	}
	autoSubscribe := s.autoSubscribe && (settings.AutoSubscribe == nil || *settings.AutoSubscribe)
	employeeConnections := settings.EmployeeConnections == nil || *settings.EmployeeConnections
	return OfferPolicy{Offer: settings.Offer, EmployeeConnections: employeeConnections, CanConnect: !caller.Agent && (employeeConnections || caller.Role.IsOrgAdmin()), AutoSubscribeDefault: autoSubscribe}, nil
}

// allowEmployeeConnect checks the organization's self-service connection
// preference after authentication. Admins retain the ability to connect apps
// for themselves, but a member cannot create a connection when it is off.
func (s *Service) allowEmployeeConnect(ctx context.Context, organizationID string, admin bool) error {
	if admin || s.policy == nil {
		return nil
	}
	settings, err := s.policy.Get(ctx, organizationID)
	if err != nil {
		return fmt.Errorf("read the organization's app policy: %w: %w", domain.ErrUnavailable, err)
	}
	if settings.EmployeeConnections != nil && !*settings.EmployeeConnections {
		return fmt.Errorf("employee app connections are disabled for this organization: %w", domain.ErrForbidden)
	}
	return nil
}

// allowToolkit applies the organization's offer list at the point of use.
// Reading it for every action means a newly restricted app cannot continue
// through an old connection or a previously discovered tool.
func (s *Service) allowToolkit(ctx context.Context, organizationID, toolkit string) error {
	if s.policy == nil {
		return nil
	}
	settings, err := s.policy.Get(ctx, organizationID)
	if err != nil {
		return fmt.Errorf("read the organization's app policy: %w: %w", domain.ErrUnavailable, err)
	}
	if settings.Offer != nil && !slices.Contains(*settings.Offer, toolkit) {
		return fmt.Errorf("%s is not offered by this organization: %w", toolkit, domain.ErrForbidden)
	}
	return nil
}

// defaultAutoSubscribe is used only when a member has not made a choice on
// the connection request. The deployment's off switch remains authoritative.
func (s *Service) defaultAutoSubscribe(ctx context.Context, organizationID string) (bool, error) {
	if !s.autoSubscribe || s.policy == nil {
		return s.autoSubscribe, nil
	}
	settings, err := s.policy.Get(ctx, organizationID)
	if err != nil {
		return false, fmt.Errorf("read the organization's app policy: %w: %w", domain.ErrUnavailable, err)
	}
	return settings.AutoSubscribe == nil || *settings.AutoSubscribe, nil
}
