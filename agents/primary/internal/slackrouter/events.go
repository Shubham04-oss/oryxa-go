package slackrouter

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
)

// The bus events apps publishes when it has kept a verified Slack delivery,
// which are apps' contract (contracts/events/apps/v1) and named here only so
// that this package's callers do not import it for two strings. They carry ids
// and nothing else: the delivery itself, what a member wrote or clicked, is read
// back from apps when the job runs.
const (
	// MessageReceivedDetailType is a message that mentioned the bot.
	MessageReceivedDetailType = appsv1.SlackMessageReceivedDetailType
	// ActionReceivedDetailType is a click on one of the bot's buttons.
	ActionReceivedDetailType = appsv1.SlackActionReceivedDetailType
)

// Subscriptions are the detail types the router consumes, by source, for the
// host that provisions the rule feeding agents' queue.
func Subscriptions() map[string][]string {
	return map[string][]string{appsv1.Source: {MessageReceivedDetailType, ActionReceivedDetailType}}
}

// Handles reports whether detailType, from apps, is an event for the router.
func Handles(detailType string) bool {
	return detailType == MessageReceivedDetailType || detailType == ActionReceivedDetailType
}

// jobFromEvent reads the job a received event asks for.
func jobFromEvent(detailType, eventID string, data []byte, now time.Time) (Job, error) {
	var kind string
	switch detailType {
	case MessageReceivedDetailType:
		kind = KindMessage
	case ActionReceivedDetailType:
		kind = KindAction
	default:
		return Job{}, fmt.Errorf("event %s: %s is not a Slack inbound event", eventID, detailType)
	}
	// A message and a click have the one shape, and apps' contract gives each
	// its own type; a change to either that the other does not follow shows here.
	var received appsv1.SlackMessageReceived
	if kind == KindAction {
		var action appsv1.SlackActionReceived
		if err := json.Unmarshal(data, &action); err != nil {
			return Job{}, fmt.Errorf("event %s: decode Slack inbound event: %w", eventID, err)
		}
		received = appsv1.SlackMessageReceived(action)
	} else if err := json.Unmarshal(data, &received); err != nil {
		return Job{}, fmt.Errorf("event %s: decode Slack inbound event: %w", eventID, err)
	}
	if strings.TrimSpace(received.InboundID) == "" || strings.TrimSpace(received.OrganizationID) == "" || strings.TrimSpace(received.TeamID) == "" {
		return Job{}, fmt.Errorf("event %s: a Slack inbound event needs its inbound, organization and team ids", eventID)
	}
	if received.ReceivedAt.IsZero() {
		received.ReceivedAt = now
	}
	return Job{
		InboundID: received.InboundID, Kind: kind, OrganizationID: received.OrganizationID,
		TeamID: received.TeamID, EventID: eventID, ReceivedAt: received.ReceivedAt,
	}, nil
}

// Job is one inbound Slack delivery waiting to be routed.
type Job struct {
	// InboundID is apps' inbound row, and the job's identity: recording the
	// same one twice records one job.
	InboundID      string
	Kind           string
	OrganizationID string
	TeamID         string
	// EventID is the envelope that first recorded it.
	EventID    string
	ReceivedAt time.Time
	// Attempts is how many times running it has failed.
	Attempts int
	// AskID is the ask a mention was turned into, when an earlier attempt got
	// that far.
	AskID string
}

func validateJob(job Job) error {
	switch {
	case strings.TrimSpace(job.InboundID) == "":
		return errors.New("a Slack job needs its inbound id")
	case job.Kind != KindMessage && job.Kind != KindAction:
		return fmt.Errorf("unknown Slack job kind %q", job.Kind)
	case strings.TrimSpace(job.OrganizationID) == "", strings.TrimSpace(job.TeamID) == "":
		return errors.New("a Slack job needs its organization and team ids")
	case job.ReceivedAt.IsZero():
		return errors.New("a Slack job needs when it was received")
	default:
		return nil
	}
}
