package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

// Connecting an app subscribes it to everything it can deliver, so a member
// who connects GitHub starts hearing about GitHub instead of facing a list of
// forty-six events and picking.
//
// Not in the callback: a toolkit's events are subscribed one Composio call at
// a time, and the member's browser is waiting on that redirect. The
// connection records that it is owed, and the callback wakes the subscriber,
// which does it moments later and also survives a restart part-way through.
// It used to be the fifteen-minute maintenance sweep's job, and a member who
// had just connected an app saw no events on it for up to that long.

const (
	// autoSubscribeAttempts is how many passes a connection gets before it is
	// left alone. A toolkit Composio keeps refusing is not worth retrying
	// for ever, and a member can still subscribe by hand.
	autoSubscribeAttempts = 3
	// autoSubscribeConnections bounds how many connections one pass takes on,
	// since each is a walk of its app's catalog and a call per event.
	autoSubscribeConnections = 4
	// autoSubscribeLease is how long a claim owns a connection against
	// another pass, comfortably longer than one pass takes: a catalog walk
	// plus up to AutoSubscribeLimit Composio calls.
	autoSubscribeLease = 10 * time.Minute
)

// autoSubscribeEvery is how often the subscriber looks for connections owed
// their subscriptions when nothing has woken it: those another replica
// completed, and any a restart interrupted.
const autoSubscribeEvery = time.Minute

// RunAutoSubscribe subscribes newly connected apps until ctx is cancelled.
// A connection completing wakes it at once; otherwise it looks every
// autoSubscribeEvery.
func (s *Service) RunAutoSubscribe(ctx context.Context) {
	for {
		s.subscribeConnections(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.subscribeWake:
		case <-time.After(autoSubscribeEvery):
		}
	}
}

// wakeSubscriber tells RunAutoSubscribe a connection is owed its
// subscriptions now. It never blocks: a wake already pending covers this one.
func (s *Service) wakeSubscriber() {
	select {
	case s.subscribeWake <- struct{}{}:
	default:
	}
}

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
	now := s.now().UTC()
	leaseUntil := now.Add(autoSubscribeLease)
	owed, err := s.store.ConnectionsAwaitingSubscribe(ctx, autoSubscribeAttempts, autoSubscribeConnections, now, leaseUntil)
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
		if !connection.Composio() {
			// The store claims only Composio's; subscribing is a Composio
			// call, which no other broker's connection has an account for.
			continue
		}
		if err := s.allowToolkit(ctx, connection.OrganizationID, connection.Toolkit); err != nil {
			s.logger.WarnContext(ctx, "skip automatic subscriptions under workspace app policy", "connection_id", connection.ID, "error", err)
			// Keep the lease as a backoff, but do not spend the provider retry
			// budget on a disabled app or an unavailable policy store.
			refund, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			if refundErr := s.store.RefundAutoSubscribeAttempt(refund, connection.ID, connection.AutoSubscribeAttempts, leaseUntil); refundErr != nil {
				s.logger.ErrorContext(ctx, "refund policy-blocked subscription attempt", "connection_id", connection.ID, "error", refundErr)
			}
			cancel()
			continue
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
			s.warnIfGivingUp(ctx, logger, connection)
		}
		return
	}

	// Composio keys an instance by account, trigger and config, so
	// subscribing blind risks two mistakes: a second, unfiltered instance
	// beside one the member already tuned (say, Gmail with query:"from:boss"),
	// and bringing back a slug the member deliberately removed. Every row
	// ever on this connection - live or removed - answers both at once, so it
	// is read before anything is subscribed.
	onConnection, err := s.store.Triggers(ctx, connection.OrganizationID,
		TriggerQuery{ConnectionID: connection.ID, IncludeRemoved: true})
	if err != nil {
		if ctx.Err() == nil {
			logger.WarnContext(ctx, "read a connection's own triggers before subscribing it", "error", err)
		}
		return
	}
	present := make(map[string]bool, len(onConnection))
	for _, trigger := range onConnection {
		present[trigger.Slug] = true
	}

	wanted, needsConfig := autoSubscribable(definitions)
	var alreadyPresent []string
	subscribed, refused := 0, 0
	for _, definition := range wanted {
		if ctx.Err() != nil {
			return
		}
		if present[definition.Slug] {
			// Live already means the member has it, maybe filtered, maybe
			// from an earlier partial pass; removed means they opted out.
			// Either way, connecting again must leave it exactly as it is.
			alreadyPresent = append(alreadyPresent, definition.Slug)
			continue
		}
		if _, err := s.subscribe(ctx, connection, definition, json.RawMessage(`{}`)); err != nil {
			if errors.Is(err, domain.ErrNotConnected) {
				// A disconnect raced this pass and won: the store refused to
				// insert a trigger on a connection that is no longer active.
				// Nothing left on it will succeed, and marking it subscribed
				// would say otherwise.
				logger.InfoContext(ctx, "the connection was removed while subscribing it", "trigger", definition.Slug)
				return
			}
			// An app that refuses one of its own events is Composio's
			// business, not the member's: it is recorded and the rest go on.
			if errors.Is(err, domain.ErrUnavailable) || errors.Is(err, domain.ErrRateLimited) {
				logger.WarnContext(ctx, "subscribing was cut short", "trigger", definition.Slug, "error", err)
				s.warnIfGivingUp(ctx, logger, connection)
				return
			}
			logger.InfoContext(ctx, "an event could not be subscribed to",
				"trigger", definition.Slug, "error", err)
			refused++
			continue
		}
		subscribed++
	}

	if subscribed == 0 && refused > 0 {
		// One event refused is that event's business; every one refused
		// says something about the request or the project, not the events —
		// the first live run met exactly that, a project that wanted a field
		// every subscription left out. It is a failed pass, tried again
		// within the attempts a connection gets, rather than marked done
		// with nothing subscribed.
		logger.WarnContext(ctx, "every event this connection could subscribe to was refused",
			"refused", refused)
		s.warnIfGivingUp(ctx, logger, connection)
		return
	}
	if err := s.store.MarkSubscribed(ctx, connection.ID, s.now().UTC()); err != nil {
		// Left unmarked, it is claimed again; subscribing is idempotent, so
		// the worst of that is a wasted pass.
		logger.ErrorContext(ctx, "mark a connection subscribed", "error", err)
		return
	}
	logger.InfoContext(ctx, "subscribed a connection to its app's events",
		"subscribed", subscribed, "already_present", len(alreadyPresent),
		"needs_configuration", len(needsConfig), "available", len(definitions))
	if len(alreadyPresent) > 0 {
		// Distinct from needsConfig below: these are not missing anything,
		// the member already decided them one way or the other.
		slices.Sort(alreadyPresent)
		logger.InfoContext(ctx, "events already on this connection were left as they are",
			"triggers", strings.Join(alreadyPresent, ", "))
	}
	if len(needsConfig) > 0 {
		// Named rather than counted: these are the events a member has to
		// add themselves, and they cannot ask for what they cannot see.
		logger.InfoContext(ctx, "events left for the member to set up",
			"triggers", strings.Join(needsConfig, ", "))
	}
}

// warnIfGivingUp says, once, that a failed pass was this connection's last
// allowed attempt. Without it, auto_subscribed_at simply stays NULL forever:
// nothing else distinguishes "still being worked on" from "never going to
// happen", and the member is left wondering why an app never sent anything.
func (s *Service) warnIfGivingUp(ctx context.Context, logger *slog.Logger, connection domain.Connection) {
	if connection.AutoSubscribeAttempts >= autoSubscribeAttempts {
		logger.WarnContext(ctx, "auto-subscribe gave up on this connection after repeated failures; the member can still subscribe to its events by hand",
			"attempts", connection.AutoSubscribeAttempts)
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
