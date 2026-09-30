package inbound

import (
	"context"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactiondelivery"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// TestAnOwnersReplyWakesOnlyTheAsker: a request its owner answered themself is
// approved and answered in one change, and its decision says so; it asks no
// work of the owner's agent, so no delivery is kept for it, and the answer
// that follows is the asker's to hear, as any answer is.
func TestAnOwnersReplyWakesOnlyTheAsker(t *testing.T) {
	recorder := &fakeInteractionRecorder{}
	consumer, err := NewConsumer(&fakeRecorder{}, recorder, nil, testVerifier(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	nudges := 0
	consumer.WithInteractionNudge(func() { nudges++ })
	changed := agentsv1.InteractionChanged{
		RequestID: "irq_1", OrganizationID: "org_1",
		RequesterUserID: "user_a", RequesterAgentID: "agt_a",
		TargetUserID: "user_b", TargetAgentID: "agt_b",
		Status: "approved", Revision: 2, ChangedAt: time.Unix(100, 0).UTC(),
		Consent:   agentsv1.InteractionConsentReply,
		DecidedBy: &agentsv1.InteractionDecider{Kind: agentsv1.DecidedByMember, UserID: "user_b", Surface: "slack"},
	}
	if err := consumer.Handle(context.Background(), delivery(t, agentsv1.Source, agentsv1.InteractionDecidedDetailType, "evt_replied", changed)); err != nil {
		t.Fatal(err)
	}
	if len(recorder.deliveries) != 0 || nudges != 0 {
		t.Fatalf("a reply's approval was kept for the owner's agent: %+v", recorder.deliveries)
	}
	changed.Status, changed.Revision = "completed", 3
	if err := consumer.Handle(context.Background(), delivery(t, agentsv1.Source, agentsv1.InteractionCompletedDetailType, "evt_answered", changed)); err != nil {
		t.Fatal(err)
	}
	if len(recorder.deliveries) != 1 || recorder.deliveries[0].Kind != interactiondelivery.KindRequesterCompleted ||
		recorder.deliveries[0].RecipientAgentID != "agt_a" {
		t.Fatalf("deliveries = %+v, want the asker's alone", recorder.deliveries)
	}
	// Any other approval is the owner's agent's to work on, as ever.
	changed.Status, changed.Revision, changed.Consent = "approved", 2, agentsv1.InteractionConsentSummary
	if err := consumer.Handle(context.Background(), delivery(t, agentsv1.Source, agentsv1.InteractionDecidedDetailType, "evt_summary", changed)); err != nil {
		t.Fatal(err)
	}
	if len(recorder.deliveries) != 2 || recorder.deliveries[1].Kind != interactiondelivery.KindTargetApproved {
		t.Fatalf("an approval with a summary kept %+v, want the owner's agent's", recorder.deliveries)
	}
}
