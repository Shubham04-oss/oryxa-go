// Package events hears, on realtime's own queue, auth's word that a
// membership or an organization ended, and agents' that a person left a
// teamspace or it was deleted, and turns each into one purge by the Worker,
// recorded once done as realtime.purge.completed.v1. It hears agents' word
// that a request between two members moved too, and has the Worker note it in
// their direct message and inboxes. It also puts realtime's own events on the
// bus.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	agentseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	realtimev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/realtime/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/purge"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/requests"
)

// component identifies this consumer in its metrics and logs, low-cardinality
// and fixed regardless of which queue or detail type it is handling.
const component = "realtime"

// requestDetailTypes are the agents events that move a request between two
// members. Each carries the request's whole projection (InteractionChanged),
// so any of them says where it stands. The lease and secret events are not
// among them: they change no request's state, which is what a note says.
var requestDetailTypes = []string{
	agentseventsv1.InteractionRequestedDetailType,
	agentseventsv1.InteractionDecidedDetailType,
	agentseventsv1.InteractionCompletedDetailType,
}

// isRequestEvent reports whether detailType is one of requestDetailTypes.
func isRequestEvent(detailType string) bool {
	return slices.Contains(requestDetailTypes, detailType)
}

// Subscriptions are the events realtime consumes, by source: auth's word that
// a membership or an organization ended, agents' that a person left a
// teamspace or it was deleted, and agents' that a request between two members
// was made, decided or answered. The rule that delivers them to realtime's
// queue is built from this, so a type added here reaches the queue once the
// provisioner has run again, and not before.
func Subscriptions() map[string][]string {
	return map[string][]string{
		autheventsv1.Source: {
			autheventsv1.OrganizationDeletedDetailType,
			autheventsv1.MemberRemovedDetailType,
		},
		agentseventsv1.Source: slices.Concat([]string{
			agentseventsv1.TeamspaceDeletedDetailType,
			agentseventsv1.TeamspaceMemberRemovedDetailType,
		}, requestDetailTypes),
	}
}

// SignatureMaxAge is how old a signature on an event realtime still accepts,
// rather than the day other consumers do. A purge can wait on realtime's
// queue, and its dead-letter queue, for the 14 days they keep it (the
// provisioner's retention), and an event lost on the way can be replayed from
// the bus's archive for 7 days after it was published
// (deploy/production/events.tf). Enforcing signatures, a purge that waited or
// came back must still verify; acting on it late is harmless, since the
// Worker's purge is idempotent and knows each event by its id.
const SignatureMaxAge = (14 + 7) * 24 * time.Hour

// maxClockSkew is how far after an event was received the removal it names
// may lie. Auth's clock and this one agree far more closely than that, so a
// removal later still did not happen then, and a purge up to it would take
// what the member wrote after being invited back.
const maxClockSkew = 5 * time.Minute

// errFutureRemoval marks an event whose removal lies more than maxClockSkew
// after it was received. Its purge is dropped, not sent: retried until that
// time had passed, it would take whatever the member wrote in between.
var errFutureRemoval = errors.New("its removed_at is still to come")

// errUnrecorded marks a purge the Worker did whose completion could not be
// recorded. The message is kept, so its redelivery asks the Worker again,
// which answers 204 again, the purge being idempotent, and records it then.
var errUnrecorded = errors.New("the Worker purged the conversations, but their purge's completion was not recorded")

// Purger is what a purge is sent through; *purge.Client is one.
type Purger interface {
	Purge(context.Context, purge.Request) error
}

// Noter is what a request's movement is sent through, to have the Worker note
// it; *requests.Client is one.
type Noter interface {
	Note(context.Context, requests.Note) error
}

// Recorder keeps the audit trail of purges: realtime.purge.completed.v1 in
// realtime's outbox. *postgres.Store is one. Recording one again, for the
// same event, records nothing new.
type Recorder interface {
	RecordPurgeCompleted(context.Context, realtimev1.PurgeCompleted) (bool, error)
}

// Consumer purges the realtime conversations of members who left an
// organization, and of organizations that were deleted, and the side
// conversations of members who left a teamspace, and of teamspaces that were
// deleted. It has the Worker note each move of a request between two members.
type Consumer struct {
	purger Purger
	// noter has the Worker note a request's move. NewConsumer refuses a nil
	// one: the queue's rule delivers those events, so a consumer without one
	// would fail every one of them.
	noter Noter
	// recorder records each purge once the Worker has done it. NewConsumer
	// refuses a nil one, so no purge goes unrecorded by omission.
	recorder Recorder
	// verifier checks who published each event before anything in it is
	// used. NewConsumer refuses a nil one, so the check is never left out by
	// omission. Whether an event that fails it is refused or only reported is
	// the verifier's registry's to say, and by default it is only reported
	// (eventauth.Registry.ReportOnly).
	verifier *eventauth.Verifier
	logger   *slog.Logger
	metrics  observability.ConsumerMetrics
	purged   *recent
	// noted remembers the requests' moves the Worker took lately, by request
	// and revision, as purged does the purges.
	noted *recent
	// queueName is set once, at the top of Poll, before Poll's single
	// goroutine starts calling handle; it is not touched concurrently.
	queueName string
}

// NewConsumer returns a consumer that purges through purger, has noter note
// the moves of requests, and records each purge done through recorder.
func NewConsumer(purger Purger, noter Noter, recorder Recorder, verifier *eventauth.Verifier, logger *slog.Logger) (*Consumer, error) {
	if purger == nil {
		return nil, errors.New("realtime needs the Worker to purge through")
	}
	if noter == nil {
		return nil, errors.New("realtime needs the Worker to note requests through")
	}
	if recorder == nil {
		return nil, errors.New("realtime needs somewhere to record the purges it has done")
	}
	if verifier == nil {
		return nil, errors.New("realtime needs a verifier to check its events' signatures with")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	metrics, err := observability.NewConsumerMetrics()
	if err != nil {
		logger.Warn("consumer metrics unavailable; handling continues without them", "error", err)
	}
	return &Consumer{
		purger: purger, noter: noter, recorder: recorder, verifier: verifier, logger: logger, metrics: metrics,
		purged: newRecent(recentSize), noted: newRecent(recentSize),
	}, nil
}

type eventBridgeEvent struct {
	ID         string          `json:"id"`
	Source     string          `json:"source"`
	DetailType string          `json:"detail-type"`
	Detail     json.RawMessage `json:"detail"`
}

type receivedEnvelope struct {
	EventID    string          `json:"event_id"`
	Version    int             `json:"version"`
	OccurredAt string          `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
	// Signature is the publisher's proof over the envelope's content. It is
	// checked before anything in the event is used.
	Signature string `json:"sig"`
	// TraceCarrier decodes the envelope's optional traceparent/tracestate; an
	// envelope without them is handled under a fresh span.
	observability.TraceCarrier
}

// Handle processes one SQS message body: an EventBridge event from auth or
// agents. It returns nil once the message is done with: purged and the purge
// recorded, or purged already; or a request's move noted, or noted already,
// or one there is nothing to note. An error wrapping purge.ErrPermanent, or
// requests.ErrPermanent, means the Worker refused for good, and one for an
// event whose removed_at lies more than maxClockSkew ahead means no purge
// should be sent; Poll drops the message for either. Any other error leaves it
// for the queue to deliver again, including a purge done but not recorded.
func (consumer *Consumer) Handle(ctx context.Context, body string) error {
	_, err := consumer.handle(ctx, body, time.Now())
	return err
}

// handled is what an event asked for, as far as it got to saying, so that
// one given up for good can be logged whole: a purge, or a request's move.
type handled struct {
	purge purge.Request
	note  requests.Note
	// isNote is set once the event is known to move a request, whether or
	// not it could be read as one.
	isNote bool
}

// describe names what an event that failed was about, when it got as far as
// saying: for a purge, its organization and teamspace, and for a move of a
// request, its organization and the request.
func (result handled) describe() string {
	switch {
	case result.note.RequestID != "":
		return " for organization " + result.note.OrganizationID + ", request " + result.note.RequestID
	case result.isNote:
		return ""
	}
	return scope(result.purge)
}

// handle is Handle, answering also with what the event asked for.
func (consumer *Consumer) handle(ctx context.Context, body string, receivedAt time.Time) (handled, error) {
	var event eventBridgeEvent
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		return handled{}, fmt.Errorf("decode EventBridge event: %w", err)
	}
	if !slices.Contains(Subscriptions()[event.Source], event.DetailType) {
		return handled{}, fmt.Errorf("event %s: no handler for %s from %s", event.ID, event.DetailType, event.Source)
	}
	var detail receivedEnvelope
	if err := json.Unmarshal(event.Detail, &detail); err != nil {
		return handled{}, fmt.Errorf("event %s: decode detail: %w", event.ID, err)
	}
	if detail.Version != 1 || detail.EventID == "" {
		return handled{}, fmt.Errorf("event %s: unsupported envelope (version %d, event_id %q)", event.ID, detail.Version, detail.EventID)
	}
	// The signature is checked before anything in the event is used, so a
	// message that reached the queue without the service it names as its
	// source having sent it purges and notes nothing, whatever it names.
	if err := consumer.verifier.Verify(event.Source, event.DetailType, detail.EventID, detail.Version, detail.OccurredAt, detail.Data, detail.Signature); err != nil {
		return handled{}, fmt.Errorf("event %s: %w", detail.EventID, err)
	}
	occurredAt, _ := time.Parse(time.RFC3339Nano, detail.OccurredAt)

	ctx, span := observability.StartConsumerSpan(ctx, detail.TraceCarrier, consumer.queueName, event.DetailType, detail.EventID)
	started := time.Now()
	var (
		result handled
		err    error
	)
	if isRequestEvent(event.DetailType) {
		result.isNote = true
		result.note, err = consumer.handleRequest(ctx, detail.Data)
	} else {
		result.purge, err = consumer.handlePurge(ctx, event.DetailType, detail.EventID, detail.Data, receivedAt)
	}
	observability.EndSpan(span, err)
	consumer.metrics.Record(ctx, component, event.DetailType, occurredAt, receivedAt, started, err)
	if err != nil {
		return result, fmt.Errorf("event %s: %s%s: %w", detail.EventID, event.DetailType, result.describe(), err)
	}
	return result, nil
}

// handlePurge purges what an event that ended a membership, an organization or
// a teamspace asks for, and records the purge once the Worker has done it.
func (consumer *Consumer) handlePurge(ctx context.Context, detailType, eventID string, data json.RawMessage, receivedAt time.Time) (purge.Request, error) {
	request, err := requestFor(detailType, eventID, data)
	if err == nil && request.RemovedAt.After(receivedAt.Add(maxClockSkew)) {
		err = fmt.Errorf("%w: %s is more than %s after the event was received", errFutureRemoval,
			request.RemovedAt.UTC().Format(time.RFC3339Nano), maxClockSkew)
	}
	outcome := "realtime conversations purged"
	switch {
	case err != nil:
	case consumer.purged.has(eventID):
		outcome = "realtime purge already done"
	default:
		if err = consumer.purger.Purge(ctx, request); err == nil {
			// Only a purge that is done and recorded is remembered, so one
			// whose record failed is purged again when it comes back, and
			// recorded then.
			if err = consumer.record(ctx, request, time.Now()); err == nil {
				consumer.purged.add(eventID)
			}
		}
	}
	if err != nil {
		return request, err
	}
	consumer.logger.InfoContext(ctx, outcome, "event_id", eventID, "detail_type", detailType,
		"organization_id", request.OrganizationID, "scope", string(request.Scope), "teamspace_id", request.TeamspaceID)
	return request, nil
}

// handleRequest has the Worker note the move of a request an event says, once:
// the Worker keeps a request's highest revision, so a note sent again does
// nothing, and the consumer remembers, as it does for purges, what it has
// sent lately, to save the call. An event with nothing to note, which is a
// request between one member's own agents, or a status or an origin no note
// says, is logged and done with: sending it again would say no more.
func (consumer *Consumer) handleRequest(ctx context.Context, data json.RawMessage) (requests.Note, error) {
	note, skipped, err := noteFor(data)
	if err != nil {
		return note, err
	}
	fields := []any{
		"organization_id", note.OrganizationID, "request_id", note.RequestID, "revision", note.Revision, "state", string(note.State),
	}
	if skipped != "" {
		consumer.logger.WarnContext(ctx, "realtime request not noted: "+skipped, fields...)
		return note, nil
	}
	key := note.RequestID + ":" + strconv.FormatInt(note.Revision, 10)
	if consumer.noted.has(key) {
		consumer.logger.InfoContext(ctx, "realtime request already noted", fields...)
		return note, nil
	}
	if err := consumer.noter.Note(ctx, note); err != nil {
		return note, err
	}
	consumer.noted.add(key)
	consumer.logger.InfoContext(ctx, "realtime request noted", fields...)
	return note, nil
}

// recordTimeout bounds recording a purge the Worker has done. The record goes
// ahead once the context it was asked in is cancelled, as a deletion does,
// well within the host's shutdown: a purge done while the process stops would
// otherwise be unrecorded, and come back to the next process to be sent again.
const recordTimeout = 10 * time.Second

// record keeps the audit trail of a purge the Worker has done: when it was
// done, what it took, and the event that asked for it.
func (consumer *Consumer) record(ctx context.Context, request purge.Request, completedAt time.Time) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()
	_, err := consumer.recorder.RecordPurgeCompleted(ctx, realtimev1.PurgeCompleted{
		OrganizationID: request.OrganizationID, Scope: string(request.Scope), UserID: request.UserID,
		TeamspaceID: request.TeamspaceID, RemovedAt: request.RemovedAt.UTC(), SourceEventID: request.EventID,
		CompletedAt: completedAt.UTC(),
	})
	if err != nil {
		return fmt.Errorf("%w: %w", errUnrecorded, err)
	}
	return nil
}

// scope names the organization a failed purge was for, and the teamspace,
// when the event got as far as saying.
func scope(request purge.Request) string {
	if request.OrganizationID == "" {
		return ""
	}
	if request.TeamspaceID != "" {
		return " for organization " + request.OrganizationID + ", teamspace " + request.TeamspaceID
	}
	return " for organization " + request.OrganizationID
}

// requestFor is the purge an event asks for, bounded by when the membership,
// the organization or the teamspace ended. A member removed because their
// organization was deleted is purged like any other, although the
// organization's own purge takes everything it kept: that purge reaches only
// the conversations the Worker's registry knows of, which nothing guarantees
// is all of them, and a member's purge that finds nothing left is still
// answered 204. So is a teamspace member's, when the teamspace's own purge,
// for a team that was deleted, took their side conversations first.
func requestFor(detailType, eventID string, data json.RawMessage) (purge.Request, error) {
	switch detailType {
	case autheventsv1.MemberRemovedDetailType:
		var removed autheventsv1.MemberRemoved
		if err := json.Unmarshal(data, &removed); err != nil {
			return purge.Request{}, fmt.Errorf("decode event data: %w", err)
		}
		if removed.OrganizationID == "" || removed.UserID == "" {
			return purge.Request{}, errors.New("member event has no organization_id or user_id")
		}
		// Without the time, the Worker could not tell what the member wrote
		// before from what they wrote after being invited back.
		if removed.RemovedAt.IsZero() {
			return purge.Request{}, errors.New("member event has no removed_at")
		}
		return purge.Request{
			EventID: eventID, OrganizationID: removed.OrganizationID,
			Scope: purge.ScopeMember, UserID: removed.UserID, RemovedAt: removed.RemovedAt,
		}, nil
	case autheventsv1.OrganizationDeletedDetailType:
		var deleted autheventsv1.OrganizationDeleted
		if err := json.Unmarshal(data, &deleted); err != nil {
			return purge.Request{}, fmt.Errorf("decode event data: %w", err)
		}
		if deleted.OrganizationID == "" || deleted.DeletedAt.IsZero() {
			return purge.Request{}, errors.New("organization event has no organization_id or deleted_at")
		}
		return purge.Request{
			EventID: eventID, OrganizationID: deleted.OrganizationID,
			Scope: purge.ScopeOrganization, RemovedAt: deleted.DeletedAt,
		}, nil
	case agentseventsv1.TeamspaceMemberRemovedDetailType:
		var removed agentseventsv1.TeamspaceMemberRemoved
		if err := json.Unmarshal(data, &removed); err != nil {
			return purge.Request{}, fmt.Errorf("decode event data: %w", err)
		}
		if removed.OrganizationID == "" || removed.TeamspaceID == "" || removed.UserID == "" {
			return purge.Request{}, errors.New("teamspace member event has no organization_id, teamspace_id or user_id")
		}
		// As for a member leaving the organization: without the time, the
		// Worker could not tell what they wrote before from what they wrote
		// after being added back.
		if removed.RemovedAt.IsZero() {
			return purge.Request{}, errors.New("teamspace member event has no removed_at")
		}
		return purge.Request{
			EventID: eventID, OrganizationID: removed.OrganizationID, Scope: purge.ScopeTeamspaceMember,
			UserID: removed.UserID, TeamspaceID: removed.TeamspaceID, RemovedAt: removed.RemovedAt,
		}, nil
	case agentseventsv1.TeamspaceDeletedDetailType:
		var deleted agentseventsv1.TeamspaceChanged
		if err := json.Unmarshal(data, &deleted); err != nil {
			return purge.Request{}, fmt.Errorf("decode event data: %w", err)
		}
		if deleted.OrganizationID == "" || deleted.TeamspaceID == "" || deleted.ChangedAt.IsZero() {
			return purge.Request{}, errors.New("teamspace event has no organization_id, teamspace_id or changed_at")
		}
		return purge.Request{
			EventID: eventID, OrganizationID: deleted.OrganizationID, Scope: purge.ScopeTeamspace,
			TeamspaceID: deleted.TeamspaceID, RemovedAt: deleted.ChangedAt,
		}, nil
	}
	return purge.Request{}, fmt.Errorf("no purge for %s", detailType)
}

// The statuses of a request the ledger publishes (InteractionChanged.Status),
// as the contract words them, and the state a note says for each. Where the
// contract names a status that no note says, or a build publishes one this
// build has not heard of, the request is not noted.
var stateOfStatus = map[string]requests.State{
	"pending":   requests.StateWaiting,
	"approved":  requests.StateApproved,
	"rejected":  requests.StateDeclined,
	"completed": requests.StateAnswered,
	"review":    requests.StateHeld,
	"failed":    requests.StateFailed,
	"withheld":  requests.StateWithheld,
}

// noteFor is the note an interaction event asks for: who asked and whose
// agent was asked, where the request was made, the revision it reached and the
// state that revision is in, and when. Nothing of what was asked or answered
// is in the event, so none is in the note. It is empty, with skipped saying
// why, for an event there is nothing to note for; an event it cannot read at
// all is an error, which stays on the queue and ends in the dead-letter queue,
// as any consumer's does.
func noteFor(data json.RawMessage) (note requests.Note, skipped string, err error) {
	var changed agentseventsv1.InteractionChanged
	if err := json.Unmarshal(data, &changed); err != nil {
		return requests.Note{}, "", fmt.Errorf("decode event data: %w", err)
	}
	if changed.OrganizationID == "" || changed.RequestID == "" || changed.RequesterUserID == "" ||
		changed.TargetUserID == "" || changed.Revision < 1 || changed.ChangedAt.IsZero() {
		return requests.Note{}, "", errors.New("interaction event has no organization_id, request_id, requester_user_id, target_user_id, revision or changed_at")
	}
	// An event an older build published, before the field existed, has no
	// origin, which is an agent's request.
	origin := changed.Origin
	if origin == "" {
		origin = requests.OriginAgent
	}
	note = requests.Note{
		OrganizationID: changed.OrganizationID, RequestID: changed.RequestID, Revision: changed.Revision,
		AskerUserID: changed.RequesterUserID, OwnerUserID: changed.TargetUserID, Origin: origin,
		ChangedAt: changed.ChangedAt.UTC(),
	}
	state, known := stateOfStatus[changed.Status]
	note.State = state
	switch {
	case changed.RequesterUserID == changed.TargetUserID:
		// A conversation is between two people.
		return note, "it is between one member's own agents", nil
	case !known:
		return note, "no note says its status " + strconv.Quote(changed.Status), nil
	case !requests.ValidOrigin(origin):
		return note, "its origin " + strconv.Quote(origin) + " is not a word a note carries", nil
	case !requests.IsID(changed.OrganizationID) || !requests.IsID(changed.RequestID) ||
		!requests.IsID(changed.RequesterUserID) || !requests.IsID(changed.TargetUserID):
		// The Worker names the conversation after these, and would refuse
		// what its names cannot hold.
		return note, "an id in it cannot stand in a conversation's name", nil
	}
	return note, "", nil
}

type queueAPI interface {
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessageBatch(context.Context, *sqs.DeleteMessageBatchInput, ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error)
}

// ReceiveBatch is how many messages Poll takes at once. Each may wait on the
// Worker, one after another, for up to purge.OrganizationTimeout, so the
// queue's visibility timeout, which its provisioner sets, must outlast that
// many.
const ReceiveBatch = 5

// receiveBackoff is how long Poll waits after a failed receive before trying
// again. A var so tests can shorten it instead of waiting for real time.
var receiveBackoff = 5 * time.Second

// deleteTimeout bounds deleting what a batch is done with. The deletion goes
// ahead once Poll's context is cancelled, well within the host's shutdown: a
// purge the Worker has done would otherwise come back to the next process and
// be sent again.
const deleteTimeout = 10 * time.Second

// Poll consumes the queue until ctx is cancelled. A message is deleted once
// it is done with, or once its purge is given up for good, which is logged as
// dropped. Any other failure leaves the message to come back after its
// visibility timeout, and the redrive policy moves one that keeps failing to
// the dead-letter queue.
func (consumer *Consumer) Poll(ctx context.Context, client queueAPI, queueURL string) {
	// Poll runs as this consumer's only goroutine for its whole life, calling
	// handle sequentially, so setting this once up front and never touching
	// it again needs no lock.
	consumer.queueName = observability.QueueName(queueURL)
	for {
		output, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(queueURL),
			MaxNumberOfMessages: ReceiveBatch,
			WaitTimeSeconds:     20,
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			consumer.logger.ErrorContext(ctx, "receive from realtime queue failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(receiveBackoff):
			}
			continue
		}
		// One timestamp for the whole batch: ReceiveMessage already returned,
		// so every message in it was equally "received" as of now.
		receivedAt := time.Now()

		var done []sqstypes.DeleteMessageBatchRequestEntry
		for index, message := range output.Messages {
			if ctx.Err() != nil {
				// Stopping: what is left goes back to the queue.
				break
			}
			result, err := consumer.handle(ctx, aws.ToString(message.Body), receivedAt)
			request, note := result.purge, result.note
			switch {
			case err == nil:
			case errors.Is(err, requests.ErrPermanent):
				// The note whole, as the Worker was sent it, so it can be sent
				// by hand once whatever stopped it is put right.
				consumer.logger.ErrorContext(ctx, "realtime request note dropped: sending it again would not make it right",
					"sqs_message_id", aws.ToString(message.MessageId), "organization_id", note.OrganizationID,
					"request_id", note.RequestID, "revision", note.Revision, "asker_user_id", note.AskerUserID,
					"owner_user_id", note.OwnerUserID, "origin", note.Origin, "state", string(note.State),
					"changed_at", note.ChangedAt.UTC().Format(time.RFC3339Nano), "error", err)
			case errors.Is(err, purge.ErrPermanent), errors.Is(err, errFutureRemoval):
				// The purge whole, as the Worker was or would have been sent it,
				// so it can be sent by hand once whatever stopped it is put
				// right.
				consumer.logger.ErrorContext(ctx, "realtime purge dropped: sending it again would not make it right",
					"sqs_message_id", aws.ToString(message.MessageId), "event_id", request.EventID, "scope", string(request.Scope),
					"organization_id", request.OrganizationID, "user_id", request.UserID, "teamspace_id", request.TeamspaceID,
					"removed_at", request.RemovedAt.UTC().Format(time.RFC3339Nano), "error", err)
			case errors.Is(err, errUnrecorded):
				// The purge is done. The redelivery purges again, which the
				// Worker answers 204 as before, and records it then.
				consumer.logger.ErrorContext(ctx, "realtime purge done but not recorded; the queue will deliver it again",
					"sqs_message_id", aws.ToString(message.MessageId), "event_id", request.EventID, "scope", string(request.Scope),
					"organization_id", request.OrganizationID, "teamspace_id", request.TeamspaceID, "error", err)
				continue
			default:
				if ctx.Err() == nil {
					failed := "realtime purge failed; the queue will deliver it again"
					if result.isNote {
						failed = "realtime request note failed; the queue will deliver it again"
					}
					consumer.logger.ErrorContext(ctx, failed, "sqs_message_id", aws.ToString(message.MessageId), "error", err)
				}
				continue
			}
			done = append(done, sqstypes.DeleteMessageBatchRequestEntry{
				Id:            aws.String(strconv.Itoa(index)),
				ReceiptHandle: message.ReceiptHandle,
			})
		}
		if len(done) == 0 {
			continue
		}
		deleteContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), deleteTimeout)
		deleted, err := client.DeleteMessageBatch(deleteContext, &sqs.DeleteMessageBatchInput{QueueUrl: aws.String(queueURL), Entries: done})
		cancel()
		if err != nil {
			consumer.logger.ErrorContext(ctx, "delete handled messages failed", "error", err)
			continue
		}
		for _, failure := range deleted.Failed {
			consumer.logger.ErrorContext(ctx, "delete handled message failed", "code", aws.ToString(failure.Code), "message", aws.ToString(failure.Message))
		}
	}
}

// recentSize is how many purged events recent remembers.
const recentSize = 1024

// recent remembers the events purged lately, so a message the queue hands
// over again — SQS delivers at least once, and a message whose deletion
// failed comes back — is deleted without asking the Worker twice. It is a
// saving, not a guarantee: it holds only the last recentSize events, and
// forgets them all when the process stops. The Worker's purge is idempotent,
// which is the guarantee.
type recent struct {
	mu    sync.Mutex
	ids   map[string]struct{}
	order []string
	next  int
}

func newRecent(size int) *recent {
	return &recent{ids: make(map[string]struct{}, size), order: make([]string, size)}
}

func (seen *recent) has(id string) bool {
	seen.mu.Lock()
	defer seen.mu.Unlock()
	_, ok := seen.ids[id]
	return ok
}

// add remembers id, forgetting the oldest once it holds as many as it can.
func (seen *recent) add(id string) {
	seen.mu.Lock()
	defer seen.mu.Unlock()
	if _, ok := seen.ids[id]; ok {
		return
	}
	if oldest := seen.order[seen.next]; oldest != "" {
		delete(seen.ids, oldest)
	}
	seen.order[seen.next] = id
	seen.ids[id] = struct{}{}
	seen.next = (seen.next + 1) % len(seen.order)
}
