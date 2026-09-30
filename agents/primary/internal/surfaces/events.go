package surfaces

import (
	"encoding/json"
	"fmt"
	"strings"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// The bus events the service consumes: the ledger's lifecycle events, of which
// it wants the ones about a request asked in Slack. It believes nothing in
// them: each only says which request to look at again, and a delivery that is
// late, repeated or out of order makes the same look at the same ledger.
//
// agents' queue already receives all three (inbound.Subscriptions), so no new
// subscription is needed: the consumer hands the service the events it is
// given, beside the delivery recorder.
//
// The lease-revoked and secret-viewed events are not consumed. A revoked lease
// says nothing in Slack until a follow-up asks, and a secret is opened only in
// Oryxa.
const (
	// RequestedDetailType is a request persisted: the owner's notice is due.
	RequestedDetailType = agentsv1.InteractionRequestedDetailType
	// DecidedDetailType is an approval or a rejection: the notice is settled.
	DecidedDetailType = agentsv1.InteractionDecidedDetailType
	// CompletedDetailType is a response: an answer, a held answer, a failure, or
	// the end of a review.
	CompletedDetailType = agentsv1.InteractionCompletedDetailType
)

// Subscriptions are the detail types the service consumes, by source, for the
// host that provisions the rule feeding agents' queue.
func Subscriptions() map[string][]string {
	return map[string][]string{agentsv1.Source: {RequestedDetailType, DecidedDetailType, CompletedDetailType}}
}

// Handles reports whether detailType, from agents, is an event for the service.
func Handles(detailType string) bool {
	switch detailType {
	case RequestedDetailType, DecidedDetailType, CompletedDetailType:
		return true
	}
	return false
}

// wakeFromEvent reads what an event wakes. It reports false for an event about
// a request that was not asked in Slack, which is nearly all of them and none of
// the service's; an event that cannot be read is an error, and the queue
// delivers it again.
func wakeFromEvent(detailType, eventID string, data []byte) (Wake, bool, error) {
	if !Handles(detailType) {
		return Wake{}, false, fmt.Errorf("event %s: %s is not an event for the Slack surfaces", eventID, detailType)
	}
	var changed agentsv1.InteractionChanged
	if err := json.Unmarshal(data, &changed); err != nil {
		return Wake{}, false, fmt.Errorf("event %s: decode interaction change: %w", eventID, err)
	}
	if strings.TrimSpace(changed.RequestID) == "" || strings.TrimSpace(changed.OrganizationID) == "" {
		return Wake{}, false, fmt.Errorf("event %s: an interaction change needs its request and organization ids", eventID)
	}
	// An event without an origin is one an older build published, which is an
	// agent's request.
	if changed.Origin != agentsv1.InteractionOriginSlack {
		return Wake{}, false, nil
	}
	return Wake{
		RequestID: changed.RequestID, OrganizationID: changed.OrganizationID, EventID: eventID,
		AskerUserID: changed.RequesterUserID, OwnerUserID: changed.TargetUserID,
	}, true, nil
}
