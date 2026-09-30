// Package inbound handles events agents must act on: app batches that wake a
// member's primary agent, interaction lifecycle changes that wake persistent
// agents, shared publications that wake the organization synthesizer,
// membership removal that takes private state with it, a person leaving a
// human team, which takes their contributions to its teamspace and says they
// left it, and human-team deletion that removes the corresponding shared
// teamspace. When the Slack consent loop is on it also hears what happens in
// Slack, from apps: a message that mentioned the bot or a click on one of its
// buttons is a job for the router, and a workspace uninstalling the app has
// what the loop holds for it removed. The ledger's request events reach the
// service that says what became of a Slack request before the delivery recorder
// sees them.
//
// Batching decides when app triggers make a batch, while interaction events
// carry routing identity and state only; authoritative request content stays
// in agents' own ledger.
//
// Messages are handled side by side, a few at a time, so one that takes long,
// such as a member's cleanup, holds up none of the others. Nothing here relies
// on the order messages come in: the queue is a standard one, which promises
// none, and every handler is safe to repeat and to run beside another.
package inbound

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
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
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/surfaces"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/wakeups"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

// component identifies this consumer in its metrics and logs, low-cardinality
// and fixed regardless of which queue or detail type it is handling.
const component = "agents"

// Subscriptions are the detail types agents consumes, by source, for a host
// provisioning the rule that feeds its queue.
//
// The Slack consent loop adds apps' three Slack events: the two the router turns
// into jobs, and a workspace's install ending, which is what tells agents to
// forget the workspace. They are subscribed to whether the loop is on or not,
// since what a rule matches is provisioned apart from any one process's
// switches; a process with the loop off acknowledges them and does nothing.
// Provisioning widens the rule only after the release that knows the types,
// which the runbook says: a consumer that did not know one would fail it, and
// the queue would move it to its dead-letter queue.
func Subscriptions() map[string][]string {
	apps := append(slices.Clone(slackrouter.Subscriptions()[appsv1.Source]), appsv1.SlackInstallChangedDetailType)
	return map[string][]string{
		batchingv1.Source: {batchingv1.TriggerBatchReadyDetailType},
		authv1.Source:     {authv1.MemberRemovedDetailType, authv1.OrganizationDeletedDetailType},
		teamsv1.Source:    {teamsv1.TeamDeletedDetailType, teamsv1.MemberRemovedDetailType},
		appsv1.Source:     apps,
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

// EventHandler is a service that turns a bus event it was handed into its own
// durable work: slackrouter.Worker and surfaces.Worker. It never calls another
// service while handling: an error is an event that cannot be read, or a
// database that did not answer, and the queue delivers it again.
type EventHandler interface {
	HandleEvent(ctx context.Context, detailType, eventID string, data []byte) error
}

// WorkspaceForgetter removes what a service holds for a Slack workspace an
// organization's install of the app ended in (Slack calls a workspace a team).
type WorkspaceForgetter func(ctx context.Context, organizationID, teamID string) error

// Consumer dispatches agents' queue.
type Consumer struct {
	recorder             Recorder
	interactionRecorder  InteractionRecorder
	organizationRecorder OrganizationSynthesisRecorder
	// forget is the ordered forgetters, which stop at the first that fails;
	// forgetIndependently is the ones that do not depend on any other.
	forget              []Forgetter
	forgetIndependently []Forgetter
	forgetTeam          TeamForgetter
	forgetTeamMember    TeamMemberForgetter
	logger              *slog.Logger
	metrics             observability.ConsumerMetrics
	// verifier proves who published each event. Nil is a build error caught
	// at mount: a consumer that cannot check signatures would run any
	// message a queue handed it.
	verifier *eventauth.Verifier
	// concurrency is how many messages Poll handles at once.
	concurrency int
	// queueName is set once, at the top of Poll, before any goroutine that
	// calls handle starts; it is only read after that.
	queueName string
	// interactionRecorded is told each time an interaction delivery is newly
	// recorded, so its agent is woken at once rather than on the next poll.
	// It is called from whichever goroutine handled the message, so it must
	// be safe to call at the same time as itself.
	interactionRecorded func()
	// slackRouter takes the events apps publishes for a delivery from Slack,
	// surfaces the ledger's request events, and forgetWorkspace what the loop
	// holds for a workspace whose install ended. All are unset while the Slack
	// consent loop is off.
	slackRouter     EventHandler
	surfaces        EventHandler
	forgetWorkspace []WorkspaceForgetter
}

// WithSlackRouter hands apps' Slack delivery events, the two slackrouter.Handles
// names, to the router. Without it they are acknowledged and dropped: the loop
// is off, and nothing would act on them.
func (consumer *Consumer) WithSlackRouter(router EventHandler) *Consumer {
	consumer.slackRouter = router
	return consumer
}

// WithSurfaces hands the ledger's request events, the three surfaces.Handles
// names, to the service that says in Slack what became of a request asked there.
// It hears each before the delivery recorder does, which stops at an answer held
// for review, since a held answer is a status in the thread.
func (consumer *Consumer) WithSurfaces(handler EventHandler) *Consumer {
	consumer.surfaces = handler
	return consumer
}

// WithWorkspaceForgetters adds the cleanup run when apps says a workspace's
// install ended. Each runs whatever became of the rest, and the event fails,
// to be delivered again, while any of them does.
func (consumer *Consumer) WithWorkspaceForgetters(forget ...WorkspaceForgetter) *Consumer {
	consumer.forgetWorkspace = append(consumer.forgetWorkspace, forget...)
	return consumer
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

// WithIndependentForgetters adds cleanup that depends on no other and that no
// other depends on, for the events NewConsumer's forgetters run for. Each runs
// whatever became of the rest, and the rest run whatever became of it, so one
// that is slow or keeps failing, such as memory kept in an outside service,
// cannot keep the others from removing what they hold. The event still fails
// while any of them does, and the queue delivers it again.
func (consumer *Consumer) WithIndependentForgetters(forget ...Forgetter) *Consumer {
	consumer.forgetIndependently = append(consumer.forgetIndependently, forget...)
	return consumer
}

// WithConcurrency sets how many messages are handled at once: at least one.
// Without it, defaultConcurrency.
func (consumer *Consumer) WithConcurrency(handlers int) *Consumer {
	consumer.concurrency = max(handlers, 1)
	return consumer
}

// WithInteractionNudge tells nudge each time an interaction delivery is newly
// recorded, so the waker delivers it at once instead of on its next poll.
// interactiondelivery.Waker's Nudge is one; it must not wait, and it is
// called from the goroutines handling messages side by side.
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
	return &Consumer{
		recorder: recorder, interactionRecorder: interactionRecorder, forget: forget, logger: logger, metrics: metrics,
		verifier: verifier, concurrency: defaultConcurrency,
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
// Poll calls it for several messages at once, so it is safe to call
// concurrently too.
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
		// What became of a request asked in Slack is said in Slack first, and
		// for every status: the delivery recorder below returns early for a
		// held answer, which is the very one the thread needs to say.
		if err := consumer.slackSurfaces(ctx, event.DetailType, envelope); err != nil {
			return err
		}
		return consumer.interactionChanged(ctx, event.DetailType, envelope)
	case event.Source == appsv1.Source && slackrouter.Handles(event.DetailType):
		return consumer.slackInbound(ctx, event.DetailType, envelope)
	case event.Source == appsv1.Source && event.DetailType == appsv1.SlackInstallChangedDetailType:
		return consumer.slackInstallChanged(ctx, envelope)
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

// slackSurfaces hands a request event to the service that says what became of
// a request asked in Slack. It does nothing while the loop is off, and for the
// nearly all of the ledger's events that are not about a Slack request the
// service returns before reading anything.
func (consumer *Consumer) slackSurfaces(ctx context.Context, detailType string, envelope receivedEnvelope) error {
	if consumer.surfaces == nil || !surfaces.Handles(detailType) {
		return nil
	}
	return consumer.surfaces.HandleEvent(ctx, detailType, envelope.EventID, envelope.Data)
}

// slackInbound hands a delivery from Slack, which apps has verified and kept and
// whose event carries ids alone, to the router as a job. With the loop off it is
// acknowledged and dropped: apps purges what it kept after seven days, and a
// mention from before the loop was switched on is not one to answer later.
func (consumer *Consumer) slackInbound(ctx context.Context, detailType string, envelope receivedEnvelope) error {
	if consumer.slackRouter == nil {
		consumer.logger.InfoContext(ctx, "a Slack delivery arrived, and the Slack consent loop is off; it is dropped",
			"event_id", envelope.EventID, "detail_type", detailType)
		return nil
	}
	return consumer.slackRouter.HandleEvent(ctx, detailType, envelope.EventID, envelope.Data)
}

// slackInstallChanged forgets what the loop holds for a workspace when apps says
// its install ended: an admin removing the app, Slack revoking the bot's token,
// the organization disconnecting it. An install coming or going active again
// needs nothing done: whoever asks next is matched again.
func (consumer *Consumer) slackInstallChanged(ctx context.Context, envelope receivedEnvelope) error {
	var changed appsv1.SlackInstallChanged
	if err := json.Unmarshal(envelope.Data, &changed); err != nil {
		return fmt.Errorf("event %s: decode Slack install change: %w", envelope.EventID, err)
	}
	if changed.OrganizationID == "" || changed.TeamID == "" {
		return fmt.Errorf("event %s: a Slack install change needs an organization and a workspace", envelope.EventID)
	}
	if changed.State != appsv1.SlackInstallEnded {
		return nil
	}
	if len(consumer.forgetWorkspace) == 0 {
		consumer.logger.InfoContext(ctx, "a Slack install ended, and the Slack consent loop is off; nothing to forget",
			"event_id", envelope.EventID, "organization_id", changed.OrganizationID, "reason", changed.Reason)
		return nil
	}
	var failures []error
	for index, forget := range consumer.forgetWorkspace {
		if err := forget(ctx, changed.OrganizationID, changed.TeamID); err != nil {
			failures = append(failures, fmt.Errorf("workspace forgetter %d of %d: %w", index+1, len(consumer.forgetWorkspace), err))
		}
	}
	if err := errors.Join(failures...); err != nil {
		return fmt.Errorf("event %s: %w", envelope.EventID, err)
	}
	consumer.logger.InfoContext(ctx, "forgot what agents kept for a workspace whose Slack install ended",
		"event_id", envelope.EventID, "organization_id", changed.OrganizationID, "reason", changed.Reason)
	return nil
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
		switch changed.Status {
		case "completed", "failed", "withheld":
			// The member who asked hears how it ended: an answer, a
			// failure, or an answer its addressed member withheld.
		case "review":
			// An answer held for its addressed member's review wakes no
			// agent: the member reviews it in person, in Oryxa or, for a
			// request asked in Slack, in the direct message the surfaces
			// sent them (handed this event above), and their review, or its
			// expiry, is the event that tells the member who asked.
			consumer.logger.InfoContext(ctx, "interaction answer held for review; no agent is woken",
				"event_id", envelope.EventID, "request_id", changed.RequestID)
			return nil
		default:
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

// forgetAll runs every forgetter for a member, or an organization, that is
// gone: the ordered ones, then the independent ones, each of which runs
// whatever becomes of the rest. The ordered ones stop at the first that fails,
// instead of running every one and joining the errors:
// consumer.forget is built in a deliberate order (see service.go) where a
// later forgetter's delete can remove rows an earlier, failed one still needs
// to find its own state on retry - for example interactiondelivery.Cleaner's
// AffectedThreads LEFT JOINs agent_interactions rows that a later forgetter
// deletes outright. Since the whole event is retried on error anyway, running
// the remaining ordered forgetters here would only risk destroying state the
// retry depends on.
//
// Every failure is returned, so the queue delivers the event again and, when
// it keeps failing, moves it to the dead-letter queue rather than losing it.
func (consumer *Consumer) forgetAll(ctx context.Context, eventID, organizationID, memberID string) error {
	var failures []error
	for index, forget := range consumer.forget {
		if err := forget(ctx, organizationID, memberID); err != nil {
			failures = append(failures, fmt.Errorf("forgetter %d of %d: %w", index+1, len(consumer.forget), err))
			break
		}
	}
	// Whatever the ordered ones did: these depend on none of them. They are
	// the ones that can be slow, such as memory kept in an outside service, so
	// they come last, and what is quick to remove is removed first.
	for index, forget := range consumer.forgetIndependently {
		if err := forget(ctx, organizationID, memberID); err != nil {
			failures = append(failures, fmt.Errorf("independent forgetter %d of %d: %w", index+1, len(consumer.forgetIndependently), err))
		}
	}
	if err := errors.Join(failures...); err != nil {
		return fmt.Errorf("event %s: %w", eventID, err)
	}
	consumer.logger.InfoContext(ctx, "forgot what agents kept", "event_id", eventID, "organization_id", organizationID, "member", memberID != "")
	return nil
}

// queueAPI is the slice of *sqs.Client that Poll calls, so a test can hand it
// a queue instead of reaching AWS.
type queueAPI interface {
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(context.Context, *sqs.ChangeMessageVisibilityInput, ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
}

const (
	// receiveSize is how many messages one receive asks for at most: the most
	// SQS gives.
	receiveSize = 10
	// defaultConcurrency is how many messages Poll handles at once. Most only
	// record what they were told and are done in milliseconds, while a
	// member's cleanup can take minutes, so this leaves room for a few of
	// those at once without holding up the rest. It stays small because
	// handlers share the database pool with the rest of the service.
	defaultConcurrency = 8
	// deleteTimeout bounds acknowledging one handled message.
	deleteTimeout = 10 * time.Second
)

// The queue hands a message to one consumer for a minute before it hands it
// to another (see infra/events/internal/provision), and moves one it has
// handed out five times to the dead-letter queue. A handler that takes longer
// than a minute, such as a member's cleanup, would be run again beside itself
// and use up those five, so while a message is being handled its visibility is
// extended. These are vars so tests can shorten them.
var (
	// visibilityBeat is how often a message being handled is kept from coming
	// back.
	visibilityBeat = 20 * time.Second
	// visibilityExtension is how long from each beat the message is kept.
	visibilityExtension = time.Minute
	// handlingTimeout is the longest a message is kept from coming back, and
	// its handler is allowed to run: past it the message is left to be
	// delivered again, so one that hangs cannot hold a place for ever.
	handlingTimeout = 5 * time.Minute
)

// receiveBackoff is how long Poll waits after a failed receive before trying
// again. A var so tests can shorten it.
var receiveBackoff = 5 * time.Second

// Poll long-polls the agents-events queue until ctx is cancelled, handling up
// to the consumer's concurrency at once, and deleting each message it handled
// as soon as it has. A message that takes long holds one place and nothing
// else: the next receive goes on beside it. Poll returns once every message it
// received has been handled or given up on.
func (consumer *Consumer) Poll(ctx context.Context, client queueAPI, queueURL string) {
	// queueName is written here, before any handler goroutine starts, and only
	// read after.
	consumer.queueName = observability.QueueName(queueURL)
	places := make(chan struct{}, max(consumer.concurrency, 1))
	var handling sync.WaitGroup
	defer handling.Wait()
	for {
		// Receive no more than there are places to handle at once: a message
		// received with nobody free to handle it would sit out its visibility
		// timeout, and come back to be handled twice.
		asked := reserve(ctx, places, receiveSize)
		if asked == 0 {
			return
		}
		output, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(queueURL),
			MaxNumberOfMessages: int32(asked),
			WaitTimeSeconds:     20,
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			release(places, asked)
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
		release(places, asked-len(output.Messages))
		for _, message := range output.Messages {
			handling.Go(func() {
				defer release(places, 1)
				consumer.process(ctx, client, queueURL, message, receivedAt)
			})
		}
	}
}

// reserve waits for a free place, and takes as many more as are free, up to
// limit. It answers how many it took: none once ctx has ended.
func reserve(ctx context.Context, places chan struct{}, limit int) int {
	select {
	case places <- struct{}{}:
	case <-ctx.Done():
		return 0
	}
	taken := 1
	for taken < limit {
		select {
		case places <- struct{}{}:
			taken++
		default:
			return taken
		}
	}
	return taken
}

// release gives back count places.
func release(places chan struct{}, count int) {
	for range count {
		<-places
	}
}

// process handles one received message, and deletes it once it has been
// handled. One that failed is left on the queue, to be delivered again once
// its visibility timeout runs out.
func (consumer *Consumer) process(ctx context.Context, client queueAPI, queueURL string, message sqstypes.Message, receivedAt time.Time) {
	handlingContext, cancel := context.WithTimeout(ctx, handlingTimeout)
	defer cancel()
	stopExtending := consumer.extendVisibility(handlingContext, client, queueURL, message)
	err := consumer.handle(handlingContext, aws.ToString(message.Body), receivedAt)
	stopExtending()
	switch {
	case err == nil:
	case ctx.Err() != nil:
		// Stopping, a deploy say, cut it short: it did not fail, and the queue
		// delivers it again to whoever runs next.
		consumer.logger.InfoContext(ctx, "event interrupted; the queue delivers it again", "sqs_message_id", aws.ToString(message.MessageId), "error", err)
		return
	default:
		consumer.logger.ErrorContext(ctx, "event failed", "sqs_message_id", aws.ToString(message.MessageId), "error", err)
		return
	}
	// Deleted even as the consumer stops, for what was handled and not deleted
	// is handled again.
	deleteContext, cancelDelete := context.WithTimeout(context.WithoutCancel(ctx), deleteTimeout)
	defer cancelDelete()
	if _, err := client.DeleteMessage(deleteContext, &sqs.DeleteMessageInput{
		QueueUrl: aws.String(queueURL), ReceiptHandle: message.ReceiptHandle,
	}); err != nil {
		consumer.logger.ErrorContext(ctx, "delete handled message failed", "sqs_message_id", aws.ToString(message.MessageId), "error", err)
	}
}

// extendVisibility keeps a message from coming back to the queue while it is
// being handled, until the function it returns is called or ctx ends. One
// that is handled in less than a beat, which is nearly all of them, costs no
// call. A beat that fails is tried again on the next: the message is kept for
// longer than a beat.
func (consumer *Consumer) extendVisibility(ctx context.Context, client queueAPI, queueURL string, message sqstypes.Message) func() {
	beating, stop := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(visibilityBeat)
		defer ticker.Stop()
		for {
			select {
			case <-beating.Done():
				return
			case <-ticker.C:
				_, err := client.ChangeMessageVisibility(beating, &sqs.ChangeMessageVisibilityInput{
					QueueUrl: aws.String(queueURL), ReceiptHandle: message.ReceiptHandle,
					VisibilityTimeout: int32(visibilityExtension / time.Second),
				})
				if err != nil && beating.Err() == nil {
					consumer.logger.WarnContext(ctx, "keep a message being handled from coming back failed",
						"sqs_message_id", aws.ToString(message.MessageId), "error", err)
				}
			}
		}
	}()
	return func() {
		stop()
		<-stopped
	}
}
