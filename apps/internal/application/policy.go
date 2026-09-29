package application

import (
	"context"
	"fmt"
	"slices"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

// allowToolkit applies the organization's offer list at the point of use.
// Reading it for every action means a newly restricted app cannot continue
// through an old connection or a previously discovered tool.
func (s *Service) allowToolkit(ctx context.Context, organizationID, toolkit string) error {
	if s.policy == nil {
		return nil
	}
	settings, err := s.policy.Get(ctx, organizationID)
	if err != nil {
		return fmt.Errorf("read the organization's app policy: %w", domain.ErrUnavailable)
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
		return false, fmt.Errorf("read the organization's app policy: %w", domain.ErrUnavailable)
	}
	return settings.AutoSubscribe == nil || *settings.AutoSubscribe, nil
}
