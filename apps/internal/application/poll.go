package application

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
)

// An app that cannot push its events is asked for them instead, every
// pollEvery, and the answer carries a cursor: where the last poll got to, so
// the next one asks for what has happened since rather than for the app's
// whole history. The first poll of a new subscription has no cursor, and the
// contract is that it answers no events and a cursor: a member who subscribes
// to an event starts hearing about it from then on, rather than being handed
// everything the app has ever recorded.
//
// The worker is part of RunOryxa, so it runs only where Oryxa's own sign-in
// is configured, and every replica may run it: the claim is what keeps two of
// them off the same subscription.

const (
	// pollEvery is how often a polled subscription is asked again when it says
	// nothing about how long to wait. An app that names its own interval
	// answers a cursor, and a session follows it.
	pollEvery = 5 * time.Minute
	// pollBatch bounds one pass, so a burst of subscriptions cannot hold a
	// replica in the app's API for a whole pass.
	pollBatch = 20
	// pollLease is how long a claim owns a subscription. It is longer than
	// one call to an app takes, which is what the two replicas asking at once
	// would otherwise overlap on.
	pollLease = 2 * time.Minute
	// pollBackoffFrom and pollBackoffTo bound how far a failing subscription
	// is put off. An app that keeps failing is not asked every five minutes
	// for ever, and a member can still unsubscribe and subscribe again.
	pollBackoffFrom = 15 * time.Minute
	pollBackoffTo   = 6 * time.Hour
	// pollMaxEvents bounds what one pass will publish for one subscription, so
	// a subscription that has been away for a year does not deliver a year in
	// one go. The rest is picked up on the next pass, from the cursor.
	pollMaxEvents = 200
)

// pollTriggers is one pass over the polled subscriptions that are due. It is
// called from RunOryxa's upkeep and answers how many events it published.
func (s *Service) pollTriggers(ctx context.Context) {
	if s.oryxa == nil {
		return
	}
	now := s.now().UTC()
	claimed, err := s.oryxa.Store.ClaimPolls(ctx, now, now.Add(pollLease), pollBatch)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "claim polled app events", "error", err)
		}
		return
	}
	for _, subscription := range claimed {
		if ctx.Err() != nil {
			return
		}
		s.pollOne(ctx, subscription)
	}
}

// pollOne asks one subscription's app for what has happened since its cursor,
// publishes what came back, and records where to carry on. A poll that fails
// keeps its cursor, so nothing is lost and the next pass asks again from the
// same place.
func (s *Service) pollOne(ctx context.Context, subscription OwnSubscription) {
	logger := s.logger.With("trigger_id", subscription.ID, "toolkit", subscription.Toolkit,
		"trigger", subscription.Slug, "connection_id", subscription.ConnectionID)
	registered, ok := s.oryxa.Integrations.Lookup(subscription.Toolkit)
	if !ok {
		s.finishPoll(ctx, subscription, subscription.Cursor, pollBackoffTo)
		return
	}
	poller, ok := registered.(integration.Poller)
	if !ok {
		s.finishPoll(ctx, subscription, subscription.Cursor, pollBackoffTo)
		return
	}
	connection, err := s.store.Connection(ctx, subscription.OrganizationID, subscription.ConnectionID)
	if err != nil || connection.Status != domain.ConnectionActive {
		// A connection that has ended is not polled again; the row is
		// removed with it, and this pass simply lets it go.
		if ctx.Err() == nil && err != nil {
			logger.WarnContext(ctx, "read a polled subscription's connection", "error", err)
		}
		return
	}
	session, err := s.oryxa.Signin.Session(ctx, subscription.ConnectionID)
	if err != nil {
		s.finishPoll(ctx, subscription, subscription.Cursor, pollBackoffTo)
		return
	}
	batch, err := poller.Poll(ctx, session, subscription.Slug, subscription.Config, subscription.Cursor)
	if err != nil {
		logger.WarnContext(ctx, "poll an app for events", "error", ownTriggerError(subscription.Toolkit, subscription.Slug, err))
		s.finishPoll(ctx, subscription, subscription.Cursor, backoffAfter(subscription, s.now().UTC()))
		return
	}
	s.publishPolled(ctx, subscription, batch, logger)
	// A pass with events in it has probably left more waiting, so the next one
	// comes round at the interval rather than at the back of one; a pass with
	// nothing has nothing to catch up on, and the interval is right for it too.
	s.finishPoll(ctx, subscription, cursorAfter(subscription, batch), pollEvery)
}

// publishPolled writes each event a poll answered as an
// apps.trigger.received.v1, deduped per trigger against the event's own id,
// and answers how many were published. An event that fails the trigger's own
// payload schema is dropped rather than published, because the contract every
// consumer reads says the payload validates.
func (s *Service) publishPolled(ctx context.Context, subscription OwnSubscription, batch integration.Batch, logger *slog.Logger) int {
	spec, err := s.ownSpec(subscription.Toolkit, subscription.Slug)
	if err != nil {
		return 0
	}
	now := s.now().UTC()
	published := 0
	for _, event := range batch.Events {
		if published >= pollMaxEvents {
			logger.InfoContext(ctx, "a poll's events are left for the next pass",
				"left_over", len(batch.Events)-published, "batch", len(batch.Events))
			break
		}
		if event.Trigger != "" && event.Trigger != subscription.Slug {
			logger.WarnContext(ctx, "a poll answered an event for another trigger", "trigger", event.Trigger)
			continue
		}
		if !json.Valid(event.Data) {
			logger.WarnContext(ctx, "a poll answered an event whose data is not JSON")
			continue
		}
		if problem := integration.CheckBytes(spec.PayloadSchema, event.Data); problem != nil {
			// A payload that breaks its own schema is this integration's to
			// fix, and publishing it would break every consumer's assumption
			// at once rather than in one place.
			logger.WarnContext(ctx, "a poll answered an event that does not match its payload schema",
				"problem", integration.CheckDescription(problem))
			continue
		}
		received := appsv1.TriggerReceived{
			TriggerID: subscription.ID, ConnectionID: subscription.ConnectionID,
			OrganizationID: subscription.OrganizationID, MemberID: subscription.MemberID,
			Toolkit: subscription.Toolkit, Trigger: subscription.Slug, ReceivedAt: now,
		}
		if len(event.Data) > appsv1.MaxTriggerDataBytes {
			received.DataOmitted = true
		} else {
			received.Data = event.Data
		}
		outbox, err := s.event(appsv1.TriggerReceivedDetailType, received)
		if err != nil {
			logger.ErrorContext(ctx, "announce a polled app event", "error", err)
			return published
		}
		recorded, err := s.store.RecordTriggerDelivery(ctx, polledEventKey(subscription.ID, event.ID),
			appsv1.TriggerReceivedDetailType, subscription.ID, now, outbox)
		if err != nil {
			logger.ErrorContext(ctx, "record a polled app event", "error", err)
			return published
		}
		if !recorded {
			// The same event, already published for this trigger: an app that
			// pages over its own history, or a poll that overlapped.
			continue
		}
		published++
	}
	return published
}

// finishPoll records where the next pass starts and how long until it, which
// releases the claim as well: a subscription whose due time has moved is not
// due again however this worker ends. It uses a context of its own, because
// the poll has already happened at the app and a worker shutting down must
// not leave the subscription claimed until the lease runs out.
func (s *Service) finishPoll(ctx context.Context, subscription OwnSubscription, cursor string, in time.Duration) {
	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.oryxa.Store.SavePoll(record, subscription.ID, cursor, s.now().UTC().Add(in)); err != nil {
		// Left unrecorded, the subscription is claimed again on the next tick,
		// and the same events are deduped against what was published.
		s.logger.ErrorContext(record, "record a polled subscription's cursor",
			"trigger_id", subscription.ID, "toolkit", subscription.Toolkit, "error", err)
	}
}

// cursorAfter is where the next poll starts from. The app's own cursor wins,
// since that is the only thing that knows where it got to; otherwise the pass
// leaves the subscription where it was, because inventing one is how a poll
// would skip or repeat events.
func cursorAfter(subscription OwnSubscription, batch integration.Batch) string {
	if strings.TrimSpace(batch.Cursor) != "" {
		return batch.Cursor
	}
	return subscription.Cursor
}

// keyPart is a part of a dedupe key, bounded and stripped of what a key may
// not hold, so an app's own id cannot forge one of its own.
func keyPart(value string) string {
	value = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return -1
		}
	}, value)
	if len(value) > 200 {
		value = value[:200]
	}
	return value
}

// polledEventKey dedupes an event for its trigger across passes, as the
// webhook path dedupes across deliveries: an app that pages over its own
// history, or two passes that overlap, answer the same event twice and the
// second is dropped. An event with no id has none to dedupe on.
func polledEventKey(triggerID, eventID string) string {
	if eventID == "" {
		return ""
	}
	return "trigger:" + triggerID + ":" + keyPart(eventID)
}

// backoffAfter is how long a failing subscription waits before it is asked
// again: each failure puts it further off, up to six hours, and a pass that
// works puts it back on the interval.
func backoffAfter(subscription OwnSubscription, now time.Time) time.Duration {
	if subscription.NextPollAt == nil {
		return pollBackoffFrom
	}
	// The previous due time says how long it has already been waiting: a
	// subscription that has been failing for a while waits longer, without
	// keeping a counter of its own.
	grown := subscription.NextPollAt.Sub(now) * 2
	if grown < pollBackoffFrom {
		return pollBackoffFrom
	}
	if grown > pollBackoffTo {
		return pollBackoffTo
	}
	return grown
}
