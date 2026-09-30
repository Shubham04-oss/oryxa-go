package slackbot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

// StatusView is workspace connection status any signed-in member may read.
// It carries no OAuth link, bot identity, scopes, installer or credentials.
type StatusView struct {
	Status    string `json:"status"`
	CanManage bool   `json:"can_manage"`
}

// Status reads only recorded install metadata. Unlike Install it creates no
// OAuth state and contacts no Slack API, so Home may re-read it safely.
func (s *Service) Status(ctx context.Context, credentials authclient.Credentials, organizationID string) (StatusView, error) {
	if strings.TrimSpace(organizationID) == "" {
		return StatusView{}, fmt.Errorf("an organization is required: %w", domain.ErrInvalid)
	}
	caller, err := s.directory.Membership(ctx, organizationID, credentials)
	if err != nil {
		return StatusView{}, err
	}
	if caller.Agent {
		return StatusView{}, fmt.Errorf("an agent cannot read a member's Slack setup: %w", domain.ErrForbidden)
	}
	status, err := s.store.InstallStatusForOrganization(ctx, organizationID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		status = StatusNotInstalled
	case err != nil:
		return StatusView{}, err
	case status != StatusActive && status != StatusEnded:
		return StatusView{}, fmt.Errorf("read Slack install status: %w", domain.ErrUnavailable)
	}
	return StatusView{Status: status, CanManage: caller.Role.IsOrgAdmin()}, nil
}
