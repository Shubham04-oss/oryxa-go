package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// Connecting an app makes what it holds searchable.
//
// A member who connects GitHub means Krude to know about their repositories.
// They used to have to come back afterwards, find the indexing panel, and
// type a repository name into a box — and until they did, connecting an app
// made nothing searchable at all. Apps announces a connection; this registers
// what it holds.
//
// It is the member's own work, done while they are away: the sources are
// theirs, read as them, and the standing grant they gave auth is what makes
// that possible. A member who never granted gets nothing registered, and is
// no worse off than before.

const (
	// autoRegisterLimit bounds how many sources connecting an app registers.
	// Every one is a scheduled pass against the provider for as long as it
	// lives, so a member with four hundred repositories is choosing which
	// matter, not having all of them indexed on their behalf.
	autoRegisterLimit = 10
	// autoRegisterInterval is how often an automatically registered source
	// is swept. It is the middle of what knowledge accepts: often enough to
	// be useful, seldom enough that ten of them are not a burden.
	autoRegisterInterval = time.Hour
)

// ConnectionConnected registers what a newly connected app holds, so it is
// searchable without the member naming anything.
//
// Nothing here is essential to the connection, which is already made and
// already usable, so most of what can go wrong is reported and dropped
// rather than retried: the member can still register a source by hand, and
// an event retried for ever would be worse than one that did nothing.
func (s *Service) ConnectionConnected(ctx context.Context, event appsv1.ConnectionConnected) error {
	if s.grants == nil || s.apps == nil {
		// Without a way to act as the member there is nothing to do, and
		// saying so on every connection would be noise: the mount already
		// warned once.
		return nil
	}
	if event.OrganizationID == "" || event.MemberID == "" || event.ConnectionID == "" {
		return fmt.Errorf("a connected app needs an organization, a member and a connection: %w", domain.ErrInvalid)
	}
	logger := s.logger.With("organization_id", event.OrganizationID,
		"connection_id", event.ConnectionID, "toolkit", event.Toolkit)

	token, err := s.grants.MemberToken(ctx, event.OrganizationID, event.MemberID)
	if err != nil {
		// A member who has not granted standing access cannot have anything
		// registered for them, and no retry changes that.
		logger.InfoContext(ctx, "nothing indexed for a newly connected app: no standing grant", "error", err)
		return nil
	}
	credentials := authclient.Credentials{Authorization: "Bearer " + token}

	targets, err := s.apps.Targets(ctx, event.OrganizationID, event.ConnectionID, credentials)
	if err != nil {
		if errors.Is(err, domain.ErrProviderUnavailable) || errors.Is(err, domain.ErrRateLimited) {
			// Apps or the app itself may answer next time, and the bus will
			// redeliver this.
			return fmt.Errorf("read what the connection holds: %w", err)
		}
		logger.InfoContext(ctx, "nothing indexed for a newly connected app", "error", err)
		return nil
	}
	if len(targets) == 0 {
		// An app nothing can be indexed for. It still delivers events.
		return nil
	}

	registered, skipped := 0, 0
	for _, target := range targets {
		if registered == autoRegisterLimit {
			skipped = len(targets) - registered
			break
		}
		provider := providerFor(event.Toolkit)
		if provider == "" {
			return nil
		}
		_, err := s.RegisterSource(ctx, credentials, event.OrganizationID, RegisterInput{
			ConnectionID: event.ConnectionID, Provider: provider,
			Target: target.Target, Interval: autoRegisterInterval,
		})
		switch {
		case err == nil:
			registered++
		case errors.Is(err, domain.ErrConflict):
			// Already indexed — a redelivery of this event, or a member who
			// registered it themselves first. Either way it is done.
			registered++
		default:
			// One target failing says nothing about the rest: a repository
			// whose name the extractor will not take should not cost a
			// member every other one.
			logger.InfoContext(ctx, "one thing could not be indexed",
				"target", target.Name, "error", err)
		}
	}
	logger.InfoContext(ctx, "indexed what a newly connected app holds",
		"registered", registered, "found", len(targets), "left_for_the_member", skipped)
	return nil
}

// providerFor is the knowledge source that reads a Composio toolkit. They
// are spelled the same today; an app with no source reads as none rather
// than as a provider that does not exist.
func providerFor(toolkit string) string {
	switch toolkit {
	case "github", "notion", "slack":
		return toolkit
	default:
		return ""
	}
}
