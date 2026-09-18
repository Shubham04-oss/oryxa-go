package application

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

// Connecting an app subscribes it to everything it can deliver, so a member
// who connects GitHub starts hearing about GitHub instead of facing a list of
// forty-six events and picking.
//
// Not in the callback: a toolkit's events are subscribed one Composio call at
// a time, and the member's browser is waiting on that redirect. The
// connection records that it is owed and the maintenance worker does it,
// which also survives a restart part-way through.

const (
	// autoSubscribeAttempts is how many passes a connection gets before it is
	// left alone. A toolkit Composio keeps refusing is not worth retrying
	// for ever, and a member can still subscribe by hand.
	autoSubscribeAttempts = 3
	// autoSubscribeConnections bounds how many connections one pass takes on,
	// since each is a walk of its app's catalog and a call per event.
	autoSubscribeConnections = 4
)

// AutoSubscribeLimit is how many events one connection is subscribed to
// without being asked. Composio's larger toolkits publish hundreds, and every
// one of them that fires wakes Krude for the member — which costs a run — so
// this is a ceiling on what connecting an app can commit someone to, not a
// paging size.
const AutoSubscribeLimit = 25

// subscribeConnections subscribes the connections owed it. One connection
// failing does not stop the others: each is claimed with its own attempt
// count and left for the next pass.
func (s *Service) subscribeConnections(ctx context.Context) {
	if !s.autoSubscribe {
		return
	}
	owed, err := s.store.ConnectionsAwaitingSubscribe(ctx, autoSubscribeAttempts, autoSubscribeConnections, s.now().UTC())
	if err != nil {
		if ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "claim connections to subscribe", "error", err)
		}
		return
	}
	for _, connection := range owed {
		if ctx.Err() != nil {
			return
		}
		s.subscribeEverything(ctx, connection)
	}
}

// subscribeEverything subscribes one connection to every event its app can
// deliver to it, and marks it done. Done means the pass finished, not that
// every event was subscribed: what could not be is reported and not tried
// again, because nothing about it will be different next time.
func (s *Service) subscribeEverything(ctx context.Context, connection domain.Connection) {
	logger := s.logger.With("connection_id", connection.ID, "toolkit", connection.Toolkit)
	definitions, err := s.triggers.definitions(ctx, connection.Toolkit)
	if err != nil {
		// Composio not answering is worth another pass; the claim already
		// counted this one.
		if ctx.Err() == nil {
			logger.WarnContext(ctx, "read an app's events to subscribe to them", "error", err)
		}
		return
	}

	wanted, skipped := autoSubscribable(definitions)
	subscribed := 0
	for _, definition := range wanted {
		if ctx.Err() != nil {
			return
		}
		if _, err := s.subscribe(ctx, connection, definition, json.RawMessage(`{}`)); err != nil {
			// An app that refuses one of its own events is Composio's
			// business, not the member's: it is recorded and the rest go on.
			if errors.Is(err, domain.ErrUnavailable) || errors.Is(err, domain.ErrRateLimited) {
				logger.WarnContext(ctx, "subscribing was cut short", "trigger", definition.Slug, "error", err)
				return
			}
			logger.InfoContext(ctx, "an event could not be subscribed to",
				"trigger", definition.Slug, "error", err)
			continue
		}
		subscribed++
	}

	if err := s.store.MarkSubscribed(ctx, connection.ID, s.now().UTC()); err != nil {
		// Left unmarked, it is claimed again; subscribing is idempotent, so
		// the worst of that is a wasted pass.
		logger.ErrorContext(ctx, "mark a connection subscribed", "error", err)
		return
	}
	logger.InfoContext(ctx, "subscribed a connection to its app's events",
		"subscribed", subscribed, "skipped", len(skipped), "available", len(definitions))
	if len(skipped) > 0 {
		// Named rather than counted: these are the events a member has to
		// add themselves, and they cannot ask for what they cannot see.
		logger.InfoContext(ctx, "events left for the member to set up",
			"triggers", strings.Join(skipped, ", "))
	}
}

// autoSubscribable splits an app's events into those connecting it can
// subscribe to unasked and those it cannot, in a stable order so two
// deployments of the same app commit a member to the same things.
//
// An event needing configuration is left out: its schema says a repository,
// a channel or a database, and there is nothing to put there that would not
// be a guess at what a member meant. Those are what the member adds.
func autoSubscribable(definitions map[string]domain.TriggerDefinition) ([]domain.TriggerDefinition, []string) {
	var wanted []domain.TriggerDefinition
	var skipped []string
	for _, definition := range definitions {
		if domain.CheckTriggerConfig(definition.ConfigSchema, json.RawMessage(`{}`)) != nil {
			skipped = append(skipped, definition.Slug)
			continue
		}
		wanted = append(wanted, definition)
	}
	slices.SortFunc(wanted, func(a, b domain.TriggerDefinition) int { return strings.Compare(a.Slug, b.Slug) })
	slices.Sort(skipped)
	if len(wanted) > AutoSubscribeLimit {
		// Over the ceiling the rest are skipped rather than truncated
		// silently, since a member reading the list is how they find out
		// what they are not getting.
		skipped = append(skipped, slugs(wanted[AutoSubscribeLimit:])...)
		slices.Sort(skipped)
		wanted = wanted[:AutoSubscribeLimit]
	}
	return wanted, skipped
}

func slugs(definitions []domain.TriggerDefinition) []string {
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, definition.Slug)
	}
	return names
}
