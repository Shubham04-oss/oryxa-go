// Package inbound handles events agents must act on: app batches that wake a
// member's primary agent, interaction lifecycle changes that wake persistent
// agents, shared publications that wake the organization synthesizer,
// membership removal that takes private state with it, a person leaving a
// human team, which takes their contributions to its teamspace and says they
// left it, and human-team deletion that removes the corresponding shared
// teamspace.
//
// Batching decides when app triggers make a batch, while interaction events
// carry routing identity and state only; authoritative request content stays
// in agents' own ledger.
package inbound

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	batchingv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/batching/v1"

	teamsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactiondelivery"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/organizationagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/wakeups"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

// component identifies this consumer in its metrics and logs, low-cardinality
// and fixed regardless of which queue or detail type it is handling.
const component = "agents"

// Subscriptions are the detail types agents consumes, by source, for a host
// provisioning the rule that feeds its queue.
func Subscriptions() map[string][]string {
	return map[string][]string{
		batchingv1.Source: {batchingv1.TriggerBatchReadyDetailType},
		authv1.Source:     {authv1.MemberRemovedDetailType, authv1.OrganizationDeletedDetailType},
		teamsv1.Source:    {teamsv1.TeamDeletedDetailType, teamsv1.MemberRemovedDetailType},
		agentsv1.Source: {
			agentsv1.InteractionRequestedDetailType,
			agentsv1.InteractionDecidedDetailType,
			agentsv1.InteractionCompletedDetailType,
			agentsv1.TeamspaceEntryPublishedDetailType,
		},
	}
}

// Recorder keeps a batch's trigger events until they wake Krude, all of them
// or none, so a wake-up never hands over part of a batch. It answers how many
// it had not seen before. wakeups.Store implements it.
type Recorder interface {
	RecordBatch(ctx context.Context, events []wakeups.Event) (int, error)
}

// InteractionRecorder keeps a content-minimal lifecycle event until the
// addressed persistent agent handles it. interactiondelivery.Store
// implements it.
type InteractionRecorder interface {
	Record(context.Context, interactiondelivery.Delivery) (bool, error)
}

type OrganizationSynthesisRecorder interface {
	Record(context.Context, organizationagents.Delivery) (bool, error)
}

// Forgetter removes what a service keeps for an organization — only one
// member's, when memberID is named. threads.Service and wakeups.Store each
// forget their own part; the threads service reports how many it removed.
type Forgetter func(ctx context.Context, organizationID, memberID string) error

// TeamForgetter removes state whose human-team authority has ended.
type TeamForgetter func(ctx context.Context, organizationID, teamID string) error

// TeamMemberForgetter withdraws private agent contributions when a person's
// place on a human team ends, and says they left the team's teamspace, when it
// has one. removedAt is when the membership ended.
type TeamMemberForgetter func(ctx context.Context, organizationID, teamID, userID string, removedAt time.Time) error

// Consumer dispatches agents' queue.
type Consumer struct {
	recorder             Recorder
	interactionRecorder  InteractionRecorder
	organizationRecorder OrganizationSynthesisRecorder
	forget               []Forgetter
	forgetTeam           TeamForgetter
	forgetTeamMember     TeamMemberForgetter
	logger               *slog.Logger
	metrics              observability.ConsumerMetrics
	// verifier proves who published each event. Nil is a build error caught
	// at mount: a consumer that cannot check signatures would run any
	// message a queue handed it.
	verifier *eventauth.Verifier
	// queueName is set once, at the top of Poll, before Poll's single
	// goroutine starts calling handle; it is not touched concurrently.
	queueName string
	// interactionRecorded is told each time an interaction delivery is newly
	// recorded, so its agent is woken at once rather than on the next poll.
	interactionRecorded func()
}

// WithOrganizationSynthesisRecorder records shared-entry events for the
// singleton organization agent. EventBridge content is never trusted as the
// synthesis source; the worker reloads the authoritative entry.
func (consumer *Consumer) WithOrganizationSynthesisRecorder(recorder OrganizationSynthesisRecorder) *Consumer {
	consumer.organizationRecorder = recorder
	return consumer
}

// WithTeamForgetter adds the cleanup used for teams.team.deleted.v1. Keeping
// it separate from member cleanup preserves the distinct team identity.
func (consumer *Consumer) WithTeamForgetter(forget TeamForgetter) *Consumer {
	consumer.forgetTeam = forget
	return consumer
}

// WithTeamMemberForgetter adds cleanup for teams.member.removed.v1.
func (consumer *Consumer) WithTeamMemberForgetter(forget TeamMemberForgetter) *Consumer {
	consumer.forgetTeamMember = forget
	return consumer
}

// WithInteractionNudge tells nudge each time an interaction delivery is newly
// recorded, so the waker delivers it at once instead of on its next poll.
// interactiondelivery.Waker's Nudge is one; it must not wait.
func (consumer *Consumer) WithInteractionNudge(nudge func()) *Consumer {
	consumer.interactionRecorded = nudge
	return consumer
}

// NewConsumer returns a consumer that records triggers with recorder and,
// when a member or an organization is gone, runs every forgetter.
func NewConsumer(recorder Recorder, interactionRecorder InteractionRecorder, forget []Forgetter, verifier *eventauth.Verifier, logger *slog.Logger) (*Consumer, error) {
	if verifier == nil {
		return nil, errors.New("agents cannot consume unsigned events: a signature verifier is required")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	metrics, err := observability.NewConsumerMetrics()
	if err != nil {
		logger.Warn("consumer metrics unavailable; handling continues without them", "error", err)
	}
	return &Consumer{recorder: recorder, interactionRecorder: interactionRecorder, forget: forget, logger: logger, metrics: metrics, verifier: verifier}, nil
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
	// checked before any handler runs; a missing or wrong signature means
	// the message never came from the service it claims.
	Signature string `json:"sig"`
	// TraceCarrier decodes the envelope's optional traceparent/tracestate
	// fields; a message published before this rollout, or one that never
	// carried one, decodes to the zero TraceCarrier, and handling continues
	// under a fresh span rather than failing.
	observability.TraceCarrier
}

// Handle decodes one SQS message body as an EventBridge event and acts on
// it. The bus delivers at least once, and every handler is safe to repeat:
// app triggers, interaction deliveries, and organization synthesis deliveries
// use event ids, and forgetting what is already gone removes nothing more.
func (consumer *Consumer) Handle(ctx context.Context, body string) error {
	return consumer.handle(ctx, body, time.Now())
}

func (consumer *Consumer) handle(ctx context.Context, body string, receivedAt time.Time) error {
	var event eventBridgeEvent
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		return fmt.Errorf("decode EventBridge event: %w", err)
	}
	var envelope receivedEnvelope
	if err := json.Unmarshal(event.Detail, &envelope); err != nil {
		return fmt.Errorf("event %s: decode detail: %w", event.ID, err)
	}
	if envelope.Version != 1 || envelope.EventID == "" {
		return fmt.Errorf("event %s: unsupported envelope (version %d, event_id %q)", event.ID, envelope.Version, envelope.EventID)
	}
	// The signature is checked before anything in the event is used, so a
	// message that reached the queue without the named service having sent
	// it is refused whatever its payload says.
	if err := consumer.verifier.Verify(event.Source, event.DetailType, envelope.EventID, envelope.Version, envelope.OccurredAt, envelope.Data, envelope.Signature); err != nil {
		return fmt.Errorf("event %s: %w", envelope.EventID, err)
	}
	occurredAt, _ := time.Parse(time.RFC3339Nano, envelope.OccurredAt)

	ctx, span := observability.StartConsumerSpan(ctx, envelope.TraceCarrier, consumer.queueName, event.DetailType, envelope.EventID)
	started := time.Now()
	err := consumer.dispatch(ctx, event, envelope)
	observability.EndSpan(span, err)
	consumer.metrics.Record(ctx, component, event.DetailType, occurredAt, receivedAt, started, err)
	return err
}

// dispatch runs the handler for one decoded message. It is Handle's own
// switch, unchanged, pulled out so handle can wrap it in a span and record
// its outcome without the span covering decoding failures that never got far
// enough to know a detail type worth attributing them to.
func (consumer *Consumer) dispatch(ctx context.Context, event eventBridgeEvent, envelope receivedEnvelope) error {
	switch {
	case event.Source == batchingv1.Source && event.DetailType == batchingv1.TriggerBatchReadyDetailType:
		return consumer.batchReady(ctx, envelope)
	case event.Source == agentsv1.Source &&
		(event.DetailType == agentsv1.InteractionRequestedDetailType ||
			event.DetailType == agentsv1.InteractionDecidedDetailType ||
			event.DetailType == agentsv1.InteractionCompletedDetailType):
		return consumer.interactionChanged(ctx, event.DetailType, envelope)
	case event.Source == agentsv1.Source && event.DetailType == agentsv1.TeamspaceEntryPublishedDetailType:
		if consumer.organizationRecorder == nil {
			return fmt.Errorf("event %s: organization-agent synthesis is not configured", envelope.EventID)
		}
		var published agentsv1.TeamspaceEntryPublished
		if err := json.Unmarshal(envelope.Data, &published); err != nil {
			return fmt.Errorf("event %s: decode teamspace entry: %w", envelope.EventID, err)
		}
		_, err := consumer.organizationRecorder.Record(ctx, organizationagents.Delivery{
			EventID: envelope.EventID, EntryID: published.EntryID, TeamspaceID: published.TeamspaceID,
			OrganizationID: published.OrganizationID, AuthorUserID: published.AuthorUserID,
			AuthorAgentID: published.AuthorAgentID, Kind: published.Kind, PublishedAt: published.PublishedAt,
		})
		return err
	case event.Source == appsv1.Source && event.DetailType == appsv1.TriggerReceivedDetailType:
		// Delivered straight from apps, as agents' rule did before batching
		// took over; one can still be on the queue from then. Batching has
		// its own copy and hands it over in a batch, so it is left to that
		// rather than waking Krude for it alone.
		consumer.logger.InfoContext(ctx, "an app trigger arrived directly; batching delivers it", "event_id", envelope.EventID)
		return nil
	case event.Source == authv1.Source && event.DetailType == authv1.MemberRemovedDetailType:
		var removed authv1.MemberRemoved
		if err := json.Unmarshal(envelope.Data, &removed); err != nil {
			return fmt.Errorf("event %s: decode member removed: %w", envelope.EventID, err)
		}
		if removed.OrganizationID == "" || removed.UserID == "" {
			return fmt.Errorf("event %s: a removed member needs an organization and a user", envelope.EventID)
		}
		return consumer.forgetAll(ctx, envelope.EventID, removed.OrganizationID, removed.UserID)
	case event.Source == authv1.Source && event.DetailType == authv1.OrganizationDeletedDetailType:
		var deleted authv1.OrganizationDeleted
		if err := json.Unmarshal(envelope.Data, &deleted); err != nil {
			return fmt.Errorf("event %s: decode organization deleted: %w", envelope.EventID, err)
		}
		if deleted.OrganizationID == "" {
			return fmt.Errorf("event %s: a deleted organization needs its id", envelope.EventID)
		}
		return consumer.forgetAll(ctx, envelope.EventID, deleted.OrganizationID, "")
	case event.Source == teamsv1.Source && event.DetailType == teamsv1.TeamDeletedDetailType:
		var deleted teamsv1.TeamDeleted
		if err := json.Unmarshal(envelope.Data, &deleted); err != nil {
			return fmt.Errorf("event %s: decode team deleted: %w", envelope.EventID, err)
		}
		if deleted.OrganizationID == "" || deleted.TeamID == "" {
			return fmt.Errorf("event %s: a deleted team needs an organization and team id", envelope.EventID)
		}
		if consumer.forgetTeam == nil {
			return fmt.Errorf("event %s: teamspace cleanup is not configured", envelope.EventID)
		}
		return consumer.forgetTeam(ctx, deleted.OrganizationID, deleted.TeamID)
	case event.Source == teamsv1.Source && event.DetailType == teamsv1.MemberRemovedDetailType:
		var removed teamsv1.MemberRemoved
		if err := json.Unmarshal(envelope.Data, &removed); err != nil {
			return fmt.Errorf("event %s: decode team member removed: %w", envelope.EventID, err)
		}
		if removed.OrganizationID == "" || removed.TeamID == "" || removed.UserID == "" {
			return fmt.Errorf("event %s: a removed team member needs an organization, team and user id", envelope.EventID)
		}
		if consumer.forgetTeamMember == nil {
			return fmt.Errorf("event %s: teamspace member cleanup is not configured", envelope.EventID)
		}
		// When the membership ended bounds what the person's side
		// conversations in the teamspace lose. An event that does not say
		// ended when it happened.
		removedAt := removed.RemovedAt
		if removedAt.IsZero() {
			removedAt, _ = time.Parse(time.RFC3339Nano, envelope.OccurredAt)
		}
		if removedAt.IsZero() {
			return fmt.Errorf("event %s: a removed team member needs when the membership ended", envelope.EventID)
		}
		return consumer.forgetTeamMember(ctx, removed.OrganizationID, removed.TeamID, removed.UserID, removedAt)
	default:
		return fmt.Errorf("event %s: no handler for %s from %s", event.ID, event.DetailType, event.Source)
	}
}

func (consumer *Consumer) interactionChanged(ctx context.Context, detailType string, envelope receivedEnvelope) error {
	if consumer.interactionRecorder == nil {
		return fmt.Errorf("event %s: interaction delivery recorder is not configured", envelope.EventID)
	}
	var changed agentsv1.InteractionChanged
	if err := json.Unmarshal(envelope.Data, &changed); err != nil {
		return fmt.Errorf("event %s: decode interaction change: %w", envelope.EventID, err)
	}
	if changed.RequestID == "" || changed.OrganizationID == "" || changed.RequesterUserID == "" ||
		changed.RequesterAgentID == "" || changed.TargetUserID == "" || changed.TargetAgentID == "" ||
		changed.Status == "" || changed.Revision < 1 || changed.ChangedAt.IsZero() {
		return fmt.Errorf("event %s: interaction change is missing routing identity, status, revision, or timestamp", envelope.EventID)
	}
	delivery := interactiondelivery.Delivery{
		EventID: envelope.EventID, RequestID: changed.RequestID, OrganizationID: changed.OrganizationID,
		Status: changed.Status, Revision: changed.Revision, ChangedAt: changed.ChangedAt,
	}
	switch detailType {
	case agentsv1.InteractionRequestedDetailType:
		if changed.Status != "pending" {
			return fmt.Errorf("event %s: requested interaction has status %q", envelope.EventID, changed.Status)
		}
		delivery.RecipientUserID = changed.TargetUserID
		delivery.RecipientAgentID = changed.TargetAgentID
		delivery.Kind = interactiondelivery.KindTargetRequested
	case agentsv1.InteractionDecidedDetailType:
		switch changed.Status {
		case "approved":
			delivery.RecipientUserID = changed.TargetUserID
			delivery.RecipientAgentID = changed.TargetAgentID
			delivery.Kind = interactiondelivery.KindTargetApproved
		case "rejected":
			delivery.RecipientUserID = changed.RequesterUserID
			delivery.RecipientAgentID = changed.RequesterAgentID
			delivery.Kind = interactiondelivery.KindRequesterRejected
		default:
			return fmt.Errorf("event %s: decided interaction has status %q", envelope.EventID, changed.Status)
		}
	case agentsv1.InteractionCompletedDetailType:
		if changed.Status != "completed" && changed.Status != "failed" {
			return fmt.Errorf("event %s: completed interaction has status %q", envelope.EventID, changed.Status)
		}
		delivery.RecipientUserID = changed.RequesterUserID
		delivery.RecipientAgentID = changed.RequesterAgentID
		delivery.Kind = interactiondelivery.KindRequesterCompleted
	}
	recorded, err := consumer.interactionRecorder.Record(ctx, delivery)
	if err != nil {
		return fmt.Errorf("event %s: %w", envelope.EventID, err)
	}
	if recorded && consumer.interactionRecorded != nil {
		consumer.interactionRecorded()
	}
	consumer.logger.InfoContext(ctx, "interaction delivery recorded",
		"event_id", envelope.EventID, "request_id", changed.RequestID,
		"recipient_agent_id", delivery.RecipientAgentID, "kind", delivery.Kind, "new", recorded)
	return nil
}

func (consumer *Consumer) batchReady(ctx context.Context, envelope receivedEnvelope) error {
	var batch batchingv1.TriggerBatchReady
	if err := json.Unmarshal(envelope.Data, &batch); err != nil {
		return fmt.Errorf("event %s: decode trigger batch: %w", envelope.EventID, err)
	}
	if batch.OrganizationID == "" || batch.MemberID == "" {
		// Nobody to wake: dropping it is the only thing to do, and retrying
		// would not change that.
		consumer.logger.WarnContext(ctx, "trigger batch names no member; dropped",
			"event_id", envelope.EventID, "batch_id", batch.BatchID)
		return nil
	}
	events := make([]wakeups.Event, 0, len(batch.Triggers))
	for _, trigger := range batch.Triggers {
		if trigger.EventID == "" {
			return fmt.Errorf("event %s: batch %s carries a trigger with no event id", envelope.EventID, batch.BatchID)
		}
		// Each trigger keeps its own app event's id, the one it was recorded
		// by when agents read triggers straight from apps, so a trigger seen
		// then is recognised now rather than told twice.
		events = append(events, wakeups.Event{
			ID:             trigger.EventID,
			OrganizationID: batch.OrganizationID,
			MemberID:       batch.MemberID,
			Toolkit:        trigger.Toolkit,
			Trigger:        trigger.Trigger,
			Data:           trigger.Data,
			DataOmitted:    trigger.DataOmitted,
			ReceivedAt:     trigger.ReceivedAt,
		})
	}
	if len(events) == 0 {
		consumer.logger.WarnContext(ctx, "trigger batch carries no triggers; dropped",
			"event_id", envelope.EventID, "batch_id", batch.BatchID)
		return nil
	}
	recorded, err := consumer.recorder.RecordBatch(ctx, events)
	if err != nil {
		return fmt.Errorf("event %s: %w", envelope.EventID, err)
	}
	consumer.logger.InfoContext(ctx, "trigger batch recorded to wake Krude",
		"event_id", envelope.EventID, "batch_id", batch.BatchID, "triggers", len(events), "new", recorded,
		"lane", batch.Lane, "flush_reason", batch.FlushReason)
	return nil
}

func (consumer *Consumer) forgetAll(ctx context.Context, eventID, organizationID, memberID string) error {
	// Stop at the first failing forgetter instead of running every one and
	// joining the errors. consumer.forget is built in a deliberate order
	// (see service.go) where a later forgetter's delete can remove rows an
	// earlier, failed one still needs to find its own state on retry - for
	// example interactiondelivery.Cleaner's AffectedThreads LEFT JOINs
	// agent_interactions rows that a later forgetter deletes outright. Since
	// the whole event is retried on error anyway, running the remaining
	// forgetters here would only risk destroying state the retry depends on.
	for _, forget := range consumer.forget {
		if err := forget(ctx, organizationID, memberID); err != nil {
			return fmt.Errorf("event %s: %w", eventID, err)
		}
	}
	consumer.logger.InfoContext(ctx, "forgot what agents kept", "event_id", eventID, "organization_id", organizationID, "member", memberID != "")
	return nil
}

type queueAPI interface {
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessageBatch(context.Context, *sqs.DeleteMessageBatchInput, ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error)
}

// receiveBackoff is how long Poll waits after a failed receive before trying
// again. A var so tests can shorten it.
var receiveBackoff = 5 * time.Second

// Poll long-polls the agents-events queue until ctx is cancelled, deleting
// only the messages it handled.
func (consumer *Consumer) Poll(ctx context.Context, client queueAPI, queueURL string) {
	// Poll runs as this consumer's only goroutine for its whole life, calling
	// handle sequentially, so setting this once up front and never touching
	// it again needs no lock.
	consumer.queueName = observability.QueueName(queueURL)
	for {
		output, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(queueURL),
			MaxNumberOfMessages: 10,
			WaitTimeSeconds:     20,
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			consumer.logger.ErrorContext(ctx, "receive from agents queue failed", "error", err)
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

		var handled []sqstypes.DeleteMessageBatchRequestEntry
		for index, message := range output.Messages {
			if err := consumer.handle(ctx, aws.ToString(message.Body), receivedAt); err != nil {
				consumer.logger.ErrorContext(ctx, "event failed", "sqs_message_id", aws.ToString(message.MessageId), "error", err)
				continue
			}
			handled = append(handled, sqstypes.DeleteMessageBatchRequestEntry{
				Id:            aws.String(strconv.Itoa(index)),
				ReceiptHandle: message.ReceiptHandle,
			})
		}
		if len(handled) == 0 {
			continue
		}
		deleted, err := client.DeleteMessageBatch(ctx, &sqs.DeleteMessageBatchInput{QueueUrl: aws.String(queueURL), Entries: handled})
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				consumer.logger.ErrorContext(ctx, "delete handled messages failed", "error", err)
			}
			continue
		}
		for _, failure := range deleted.Failed {
			consumer.logger.ErrorContext(ctx, "delete handled message failed",
				"code", aws.ToString(failure.Code), "message", aws.ToString(failure.Message))
		}
	}
}
