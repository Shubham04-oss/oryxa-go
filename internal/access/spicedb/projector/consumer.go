package projector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	filesv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/files/v1"
	ragv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/rag/v1"
	teamsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

// component identifies this consumer in its metrics and logs.
const component = "spicedb-projector"

// mapper turns one event's data into a Change.
type mapper func(json.RawMessage) (Change, error)

func decode[T any](mapping func(T) (Change, error)) mapper {
	return func(data json.RawMessage) (Change, error) {
		var event T
		if err := json.Unmarshal(data, &event); err != nil {
			return Change{}, err
		}
		return mapping(event)
	}
}

// mappers are the events that move a relationship, by source and detail type.
// Knowledge sources, the org chart, team parents and file and connection
// grants have no event that says enough; the reconcile writes those.
var mappers = map[string]map[string]mapper{
	authv1.Source: {
		authv1.MemberJoinedDetailType: decode(func(event authv1.MemberJoined) (Change, error) {
			return MemberJoined(event.OrganizationID, event.UserID, event.Role)
		}),
		authv1.MemberRoleChangedDetailType: decode(func(event authv1.MemberRoleChanged) (Change, error) {
			return MemberRoleChanged(event.OrganizationID, event.UserID, event.Role)
		}),
		authv1.MemberRemovedDetailType: decode(func(event authv1.MemberRemoved) (Change, error) {
			return MemberRemoved(event.OrganizationID, event.UserID)
		}),
		authv1.OrganizationDeletedDetailType: decode(func(event authv1.OrganizationDeleted) (Change, error) {
			return OrganizationDeleted(event.OrganizationID)
		}),
	},
	teamsv1.Source: {
		teamsv1.TeamCreatedDetailType: decode(func(event teamsv1.TeamCreated) (Change, error) {
			return TeamCreated(event.OrganizationID, event.TeamID)
		}),
		teamsv1.TeamDeletedDetailType: decode(func(event teamsv1.TeamDeleted) (Change, error) {
			return TeamDeleted(event.OrganizationID, event.TeamID)
		}),
		teamsv1.MemberAddedDetailType: decode(func(event teamsv1.MemberAdded) (Change, error) {
			return TeamMemberSet(event.OrganizationID, event.TeamID, event.UserID, event.Role)
		}),
		teamsv1.MemberRoleChangedDetailType: decode(func(event teamsv1.MemberRoleChanged) (Change, error) {
			return TeamMemberSet(event.OrganizationID, event.TeamID, event.UserID, event.Role)
		}),
		teamsv1.MemberRemovedDetailType: decode(func(event teamsv1.MemberRemoved) (Change, error) {
			return TeamMemberRemoved(event.OrganizationID, event.TeamID, event.UserID)
		}),
	},
	ragv1.Source: {
		ragv1.CorpusCreatedDetailType: decode(func(event ragv1.CorpusCreated) (Change, error) {
			return Placed("corpus", event.CorpusID, Placement{Org: event.OrganizationID, Scope: event.Scope,
				Team: event.TeamID, Member: event.MemberID, CreatedBy: event.CreatedBy})
		}),
		ragv1.CorpusDeletedDetailType: decode(func(event ragv1.CorpusDeleted) (Change, error) {
			return Deleted("corpus", event.OrganizationID, event.CorpusID)
		}),
	},
	filesv1.Source: {
		filesv1.ObjectCreatedDetailType: decode(func(event filesv1.ObjectCreated) (Change, error) {
			return Placed("file", event.FileID, Placement{Org: event.OrganizationID, Scope: event.Scope,
				Team: event.TeamID, Member: event.MemberID, Access: event.Access, CreatedBy: event.CreatedBy})
		}),
		filesv1.ObjectDeletedDetailType: decode(func(event filesv1.ObjectDeleted) (Change, error) {
			return Deleted("file", event.OrganizationID, event.FileID)
		}),
	},
	appsv1.Source: {
		appsv1.ConnectionInitiatedDetailType: decode(func(event appsv1.ConnectionInitiated) (Change, error) {
			return ConnectionOwned(event.OrganizationID, event.ConnectionID, event.MemberID)
		}),
		appsv1.ConnectionConnectedDetailType: decode(func(event appsv1.ConnectionConnected) (Change, error) {
			return ConnectionOwned(event.OrganizationID, event.ConnectionID, event.MemberID)
		}),
		// A removed connection keeps its relationships: apps still shows it,
		// marked removed, to its member and admins, and acting through it is
		// refused by its status, not by SpiceDB.
		appsv1.ConnectionRemovedDetailType: decode(func(event appsv1.ConnectionRemoved) (Change, error) {
			return Change{Organization: event.OrganizationID}, nil
		}),
	},
	agentsv1.Source: {
		agentsv1.AgentCreatedDetailType: decode(agentChanged),
		agentsv1.AgentUpdatedDetailType: decode(agentChanged),
		agentsv1.AgentDeletedDetailType: decode(func(event agentsv1.AgentChanged) (Change, error) {
			return AgentDeleted(event.OrganizationID, event.AgentID)
		}),
		agentsv1.InteractionRequestedDetailType: decode(func(event agentsv1.InteractionChanged) (Change, error) {
			return RequestMade(Request{ID: event.RequestID, Org: event.OrganizationID,
				RequesterUser: event.RequesterUserID, RequesterAgent: event.RequesterAgentID,
				TargetUser: event.TargetUserID, TargetAgent: event.TargetAgentID})
		}),
	},
}

func agentChanged(event agentsv1.AgentChanged) (Change, error) {
	return AgentChanged(event.OrganizationID, event.AgentID, event.UserID)
}

// grantsFollow are the events after which an agent's grants are read again.
var grantsFollow = []string{agentsv1.AgentCreatedDetailType, agentsv1.AgentUpdatedDetailType}

// Subscriptions is every event the projector consumes, by source; the
// queue's EventBridge rule is built from it.
func Subscriptions() map[string][]string {
	result := map[string][]string{}
	for source, types := range mappers {
		for detailType := range types {
			result[source] = append(result[source], detailType)
		}
		slices.Sort(result[source])
	}
	return result
}

// grantSyncer re-reads one agent's grants; the Reconciler is one.
type grantSyncer interface {
	SyncGrants(ctx context.Context, org, agent string) error
}

type applier interface {
	Apply(context.Context, Change) (*v1.ZedToken, error)
}

// orderer is tokens.Seen: it runs apply unless a newer event already touched
// one of check.
type orderer interface {
	Guard(ctx context.Context, check, record []string, at time.Time, apply func(context.Context) error) (bool, error)
}

type Consumer struct {
	writer applier
	grants grantSyncer
	// seen skips an event older than one already applied to the same
	// relationship; nil applies every event as it comes.
	seen    orderer
	logger  *slog.Logger
	metrics observability.ConsumerMetrics
	// verifier proves who published each event; nil is refused at build.
	verifier *eventauth.Verifier
	// queueName is set once, at the top of Poll, before Poll's single
	// goroutine starts calling handle.
	queueName string
}

func NewConsumer(writer applier, grants grantSyncer, verifier *eventauth.Verifier, logger *slog.Logger) (*Consumer, error) {
	if verifier == nil {
		return nil, errors.New("the spicedb projector cannot consume unsigned events: a signature verifier is required")
	}
	metrics, err := observability.NewConsumerMetrics()
	if err != nil {
		logger.Warn("consumer metrics unavailable; handling continues without them", "error", err)
	}
	return &Consumer{writer: writer, grants: grants, logger: logger, metrics: metrics, verifier: verifier}, nil
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
	Signature  string          `json:"sig"`
	observability.TraceCarrier
}

// Handle processes one event, receivedAt being now.
func (consumer *Consumer) Handle(ctx context.Context, body string) error {
	return consumer.handle(ctx, body, time.Now())
}

func (consumer *Consumer) handle(ctx context.Context, body string, receivedAt time.Time) error {
	var event eventBridgeEvent
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		return fmt.Errorf("decode EventBridge event: %w", err)
	}
	mapping, ok := mappers[event.Source][event.DetailType]
	if !ok {
		return fmt.Errorf("event %s: unsupported source/type", event.ID)
	}
	var detail receivedEnvelope
	if err := json.Unmarshal(event.Detail, &detail); err != nil {
		return fmt.Errorf("event %s: decode detail: %w", event.ID, err)
	}
	if detail.Version != 1 || detail.EventID == "" {
		return fmt.Errorf("event %s: unsupported envelope (version %d, event_id %q)", event.ID, detail.Version, detail.EventID)
	}
	if err := consumer.verifier.Verify(event.Source, event.DetailType, detail.EventID, detail.Version, detail.OccurredAt, detail.Data, detail.Signature); err != nil {
		return fmt.Errorf("event %s: %w", detail.EventID, err)
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, detail.OccurredAt)
	if err != nil {
		return fmt.Errorf("invalid event time: %w", err)
	}

	ctx, span := observability.StartConsumerSpan(ctx, detail.TraceCarrier, consumer.queueName, event.DetailType, detail.EventID)
	started := time.Now()
	err = consumer.project(ctx, event.DetailType, mapping, detail.Data, occurredAt)
	observability.EndSpan(span, err)
	consumer.metrics.Record(ctx, component, event.DetailType, occurredAt, receivedAt, started, err)
	if err != nil {
		return fmt.Errorf("event %s: %s: %w", detail.EventID, event.DetailType, err)
	}
	return nil
}

func (consumer *Consumer) project(ctx context.Context, detailType string, mapping mapper, data json.RawMessage, occurredAt time.Time) error {
	change, err := mapping(data)
	if err != nil {
		return err
	}
	apply := func(ctx context.Context) error {
		_, err := consumer.writer.Apply(ctx, change)
		return err
	}
	if consumer.seen == nil {
		err = apply(ctx)
	} else {
		check, record := orderingKeys(change)
		var applied bool
		applied, err = consumer.seen.Guard(ctx, check, record, occurredAt, apply)
		if err == nil && !applied {
			consumer.logger.InfoContext(ctx, "spicedb projector: skipped an event older than one already applied", "detail_type", detailType, "occurred_at", occurredAt)
		}
	}
	if err != nil {
		return err
	}
	if slices.Contains(grantsFollow, detailType) {
		var event agentsv1.AgentChanged
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		return consumer.grants.SyncGrants(ctx, event.OrganizationID, event.AgentID)
	}
	return nil
}

type queueAPI interface {
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessageBatch(context.Context, *sqs.DeleteMessageBatchInput, ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error)
	ChangeMessageVisibilityBatch(context.Context, *sqs.ChangeMessageVisibilityBatchInput, ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityBatchOutput, error)
}

// handleTimeout bounds one message's handling, which outlives the shutdown
// that stops Poll from receiving: the message in hand finishes, and the rest
// of its batch goes back to the queue.
const handleTimeout = 10 * time.Second

// Poll consumes the queue until ctx is cancelled. Handled messages are
// deleted; failed ones return after their visibility timeout, and the redrive
// policy moves a message that keeps failing aside. Once ctx is cancelled it
// receives no more, and every message it received but did not handle is made
// visible at once, for another task, instead of a visibility timeout later.
func (consumer *Consumer) Poll(ctx context.Context, client queueAPI, queueURL string) {
	consumer.queueName = observability.QueueName(queueURL)
	work := context.WithoutCancel(ctx)
	for ctx.Err() == nil {
		output, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(queueURL),
			MaxNumberOfMessages: 10,
			WaitTimeSeconds:     20,
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			consumer.logger.ErrorContext(ctx, "receive from spicedb projector queue failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		receivedAt := time.Now()
		var handled []sqstypes.DeleteMessageBatchRequestEntry
		var unhandled []sqstypes.ChangeMessageVisibilityBatchRequestEntry
		for index, message := range output.Messages {
			if ctx.Err() != nil {
				unhandled = append(unhandled, sqstypes.ChangeMessageVisibilityBatchRequestEntry{
					Id: aws.String(strconv.Itoa(index)), ReceiptHandle: message.ReceiptHandle, VisibilityTimeout: 0,
				})
				continue
			}
			handling, cancel := context.WithTimeout(work, handleTimeout)
			err := consumer.handle(handling, aws.ToString(message.Body), receivedAt)
			cancel()
			if err != nil {
				consumer.logger.ErrorContext(ctx, "spicedb projection failed", "sqs_message_id", aws.ToString(message.MessageId), "error", err)
				continue
			}
			handled = append(handled, sqstypes.DeleteMessageBatchRequestEntry{
				Id:            aws.String(strconv.Itoa(index)),
				ReceiptHandle: message.ReceiptHandle,
			})
		}
		consumer.settle(work, client, queueURL, handled, unhandled)
	}
}

// settle deletes the handled messages and returns the unhandled ones to the
// queue, within handleTimeout, shutdown or not.
func (consumer *Consumer) settle(ctx context.Context, client queueAPI, queueURL string, handled []sqstypes.DeleteMessageBatchRequestEntry, unhandled []sqstypes.ChangeMessageVisibilityBatchRequestEntry) {
	ctx, cancel := context.WithTimeout(ctx, handleTimeout)
	defer cancel()
	if len(handled) > 0 {
		deleted, err := client.DeleteMessageBatch(ctx, &sqs.DeleteMessageBatchInput{QueueUrl: aws.String(queueURL), Entries: handled})
		if err != nil {
			consumer.logger.ErrorContext(ctx, "delete handled messages failed", "error", err)
		} else {
			for _, failure := range deleted.Failed {
				consumer.logger.ErrorContext(ctx, "delete handled message failed", "code", aws.ToString(failure.Code), "message", aws.ToString(failure.Message))
			}
		}
	}
	if len(unhandled) > 0 {
		released, err := client.ChangeMessageVisibilityBatch(ctx, &sqs.ChangeMessageVisibilityBatchInput{QueueUrl: aws.String(queueURL), Entries: unhandled})
		if err != nil {
			consumer.logger.WarnContext(ctx, "returning unhandled messages at shutdown failed; they come back after their visibility timeout", "error", err)
			return
		}
		for _, failure := range released.Failed {
			consumer.logger.WarnContext(ctx, "returning an unhandled message at shutdown failed", "code", aws.ToString(failure.Code), "message", aws.ToString(failure.Message))
		}
		consumer.logger.InfoContext(ctx, "returned unhandled messages to the queue at shutdown", "count", len(unhandled)-len(released.Failed))
	}
}
