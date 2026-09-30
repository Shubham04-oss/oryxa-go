package interactions

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// inAnotherThread is a message in a thread of the same channel that the usual
// one is not.
func inAnotherThread(trigger string) SlackThread {
	return SlackThread{TeamID: "T1", ChannelID: "C1", ThreadTS: "1790099999.000900", TriggerTS: trigger}
}

// givenAccess is an ask in the usual thread whose owner chose access, so that
// the asker holds a lease from them there; it returns the lease and the request
// that gave it.
func (ledger *slackLedger) givenAccess(owner string) (Lease, agents.Interaction) {
	ledger.t.Helper()
	request := requestTo(ledger.t, ledger.ask("1790000000.000100", owner), owner)
	result := ledger.decide(owner, request, ChoiceAccess)
	if result.Lease == nil {
		ledger.t.Fatal("access gave no lease")
	}
	return *result.Lease, result.Request
}

// TestAFollowUpUnderALeaseIsApprovedWithoutAClick: for as long as a lease
// lives, the asker's follow-ups to that owner in that thread are created
// approved, decided as the lease's, and answered like any request; a follow-up
// in another thread, to another owner, from another member, or after the hour,
// asks again.
func TestAFollowUpUnderALeaseIsApprovedWithoutAClick(t *testing.T) {
	ledger := newSlackLedger(t)
	lease, _ := ledger.givenAccess(raya)

	followUp := requestTo(t, ledger.ask("1790000100.000200", raya), raya)
	if followUp.Status != StatusApproved || followUp.Decision == nil || followUp.Decision.Consent != ConsentAccess ||
		followUp.Decision.Decision != DecisionApproved || followUp.Revision != 2 || followUp.Origin != OriginSlack ||
		!slices.Equal(followUp.Decision.GrantedPermissions, []string{PermissionConsentSummary, PermissionConsentAccess}) ||
		!slices.Equal(followUp.Decision.AllowedTools, []string{"knowledge"}) ||
		followUp.Decision.DecidedBy == nil || *followUp.Decision.DecidedBy != (agents.InteractionDecider{Kind: DecidedByLease, UserID: raya, Surface: SurfaceLease, Reason: lease.ID}) {
		t.Fatalf("follow-up = %+v, decision %+v", followUp, followUp.Decision)
	}
	if got := ledger.events(followUp.ID); !slices.Equal(got, []string{agentsv1.InteractionRequestedDetailType + ":pending", agentsv1.InteractionDecidedDetailType + ":approved"}) {
		t.Fatalf("events = %v, want requested and then decided", got)
	}
	// The lease's owner is not asked, and is not told they were: it is not
	// their decision to make again.
	if DecidedByItsOwner(followUp) {
		t.Error("a lease's decision reads as the owner's own, in person")
	}
	// It is answered like any approved request, and settles its ask.
	answered, err := ledger.answer(followUp)
	if err != nil || answered.Status != StatusCompleted {
		t.Fatalf("answering the follow-up: %+v, %v", answered, err)
	}
	if ask, err := ledger.service.Ask(context.Background(), followUp.AskID); err != nil || ask.WinnerRequestID != followUp.ID {
		t.Fatalf("the follow-up's ask = %+v, %v", ask, err)
	}

	// What the lease does not cover asks again.
	if got := requestTo(t, ledger.askIn(inAnotherThread("1790000200.000300"), raya), raya); got.Status != StatusPending {
		t.Errorf("a follow-up in another thread is %s, want pending", got.Status)
	}
	if got := requestTo(t, ledger.ask("1790000300.000400", maya), maya); got.Status != StatusPending {
		t.Errorf("a follow-up to another owner is %s, want pending", got.Status)
	}
	other, err := ledger.service.CreateAsk(context.Background(), AskInput{
		OrganizationID: ledger.org, AskerUserID: "user_other", Slack: inThread("1790000400.000500"),
		Question: "And me?", Owners: []AskOwner{{UserID: raya}},
	})
	if err != nil || requestTo(t, other, raya).Status != StatusPending {
		t.Errorf("a follow-up from another member = %+v, %v; want pending", other, err)
	}

	// The hour is the lease's: 59 minutes in it still covers, and past it does
	// not.
	ledger.clock.Advance(59*time.Minute - time.Second)
	if got := requestTo(t, ledger.ask("1790000500.000600", raya), raya); got.Status != StatusApproved {
		t.Errorf("a follow-up just inside the hour is %s, want approved", got.Status)
	}
	ledger.clock.Advance(time.Minute + 2*time.Second)
	if got := requestTo(t, ledger.ask("1790000600.000700", raya), raya); got.Status != StatusPending {
		t.Errorf("a follow-up after the hour is %s, want pending", got.Status)
	}
}

// TestAnAnswerUnderALeaseThatRanOutIsStillDelivered: a lease ends the asking
// without a click, not the work already approved under it.
func TestAnAnswerUnderALeaseThatRanOutIsStillDelivered(t *testing.T) {
	ledger := newSlackLedger(t)
	ledger.givenAccess(raya)
	followUp := requestTo(t, ledger.ask("1790000100.000200", raya), raya)
	ledger.clock.Advance(2 * time.Hour)
	if followUp.Status != StatusApproved {
		t.Fatalf("follow-up = %+v", followUp)
	}
	answered, err := ledger.answer(followUp)
	if err != nil || answered.Status != StatusCompleted {
		t.Fatalf("answering after the lease ran out: %+v, %v", answered, err)
	}
}

// TestRevokingALeaseEndsItAndSaysSo: the owner ends the lease, and their next
// follow-up asks again; the revoke is an event carrying identities and the
// thread; and ending a lease that already ended changes and says nothing.
func TestRevokingALeaseEndsItAndSaysSo(t *testing.T) {
	ledger := newSlackLedger(t)
	ctx := context.Background()
	lease, granting := ledger.givenAccess(raya)

	// Only the owner, in their own session, revokes it.
	for name, actor := range map[string]agents.InteractionActor{
		"the asker":         ledger.owner(asker),
		"another member":    ledger.owner(maya),
		"the owner's agent": ledger.agentOf(raya),
	} {
		if _, err := ledger.service.RevokeLease(ctx, actor, lease.ID); !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrForbidden) {
			t.Errorf("%s revoked it: %v, want ErrNotFound or ErrForbidden", name, err)
		}
	}
	if _, err := ledger.service.RevokeLease(ctx, ledger.owner(raya), "ils_missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoking a lease that is not there: %v", err)
	}
	elsewhere := ledger.owner(raya)
	elsewhere.OrganizationID = "org_other"
	if _, err := ledger.service.RevokeLease(ctx, elsewhere, lease.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoking it from another organization: %v", err)
	}
	if got := ledger.count(`SELECT count(*) FROM agents_primary.agents_outbox WHERE detail_type=$1 AND detail->>'organization_id'=$2`,
		agentsv1.InteractionLeaseRevokedDetailType, ledger.org); got != 0 {
		t.Fatalf("%d revoked events after refused revokes", got)
	}
	if got := requestTo(t, ledger.ask("1790000100.000200", raya), raya); got.Status != StatusApproved {
		t.Fatalf("a follow-up while the lease lives is %s", got.Status)
	}

	revoked, err := ledger.service.RevokeLease(ctx, ledger.owner(raya), lease.ID)
	if err != nil || revoked.RevokedAt == nil || !revoked.RevokedAt.Equal(ledger.clock.Now()) || revoked.Live(ledger.clock.Now()) || revoked.ID != lease.ID {
		t.Fatalf("revoked = %+v, %v", revoked, err)
	}
	if got := requestTo(t, ledger.ask("1790000200.000300", raya), raya); got.Status != StatusPending {
		t.Fatalf("a follow-up after the revoke is %s, want pending", got.Status)
	}

	var event string
	if err := ledger.database.Pool.QueryRow(ctx, `SELECT detail::text FROM agents_primary.agents_outbox
		WHERE detail_type=$1 AND detail->>'organization_id'=$2`, agentsv1.InteractionLeaseRevokedDetailType, ledger.org).Scan(&event); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"lease_id": "` + lease.ID + `"`, `"owner_user_id": "` + raya + `"`, `"asker_user_id": "` + asker + `"`,
		`"channel_id": "C1"`, `"thread_ts": "1790000000.000100"`, `"request_id": "` + granting.ID + `"`} {
		if !strings.Contains(event, want) {
			t.Errorf("the revoked event is missing %s: %s", want, event)
		}
	}
	if strings.Contains(event, "dev keys") {
		t.Errorf("the revoked event carries a question: %s", event)
	}

	// Again: nothing changes and nothing is said.
	again, err := ledger.service.RevokeLease(ctx, ledger.owner(raya), lease.ID)
	if err != nil || again.RevokedAt == nil || !again.RevokedAt.Equal(*revoked.RevokedAt) {
		t.Fatalf("revoking again = %+v, %v; want the lease as it was", again, err)
	}
	if got := ledger.count(`SELECT count(*) FROM agents_primary.agents_outbox WHERE detail_type=$1 AND detail->>'organization_id'=$2`,
		agentsv1.InteractionLeaseRevokedDetailType, ledger.org); got != 1 {
		t.Fatalf("%d revoked events, want the one", got)
	}
}

// Revoking a lease that had already run out changes nothing and says nothing:
// there was no access left to take back.
func TestRevokingALeaseThatRanOutIsNotNews(t *testing.T) {
	ledger := newSlackLedger(t)
	lease, _ := ledger.givenAccess(raya)
	ledger.clock.Advance(2 * time.Hour)
	got, err := ledger.service.RevokeLease(context.Background(), ledger.owner(raya), lease.ID)
	if err != nil || got.RevokedAt != nil || got.Live(ledger.clock.Now()) {
		t.Fatalf("revoking an expired lease = %+v, %v", got, err)
	}
	if n := ledger.count(`SELECT count(*) FROM agents_primary.agents_outbox WHERE detail_type=$1 AND detail->>'organization_id'=$2`,
		agentsv1.InteractionLeaseRevokedDetailType, ledger.org); n != 0 {
		t.Fatalf("%d revoked events for a lease that had run out", n)
	}
}

// TestAnotherAccessRenewsTheLeaseInsteadOfMakingAnother: two accesses given in
// one thread are one lease, which the second renews, keeping its id; a lease in
// another thread is another; and revoking the one takes back the access of both
// requests that were approved with it.
func TestAnotherAccessRenewsTheLeaseInsteadOfMakingAnother(t *testing.T) {
	ledger := newSlackLedger(t)
	ctx := context.Background()
	// Two asks in one thread, both made before any lease, so both wait.
	first := requestTo(t, ledger.ask("1790000000.000100", raya), raya)
	second := requestTo(t, ledger.ask("1790000050.000150", raya), raya)
	elsewhere := requestTo(t, ledger.askIn(inAnotherThread("1790000900.000100"), raya), raya)
	if first.Status != StatusPending || second.Status != StatusPending || elsewhere.Status != StatusPending {
		t.Fatalf("requests = %s %s %s, want them all waiting", first.Status, second.Status, elsewhere.Status)
	}

	given := ledger.decide(raya, first, ChoiceAccess)
	ledger.clock.Advance(20 * time.Minute)
	renewed := ledger.decide(raya, second, ChoiceAccess)
	if given.Lease == nil || renewed.Lease == nil || renewed.Lease.ID != given.Lease.ID || ledger.leases() != 1 ||
		!renewed.Lease.ExpiresAt.Equal(ledger.clock.Now().Add(time.Hour)) || renewed.Lease.RequestID != second.ID {
		t.Fatalf("lease = %+v then %+v with %d leases; want the same lease, renewed to an hour from now", given.Lease, renewed.Lease, ledger.leases())
	}
	// A lease in another thread is a lease of its own.
	other := ledger.decide(raya, elsewhere, ChoiceAccess)
	if other.Lease == nil || other.Lease.ID == given.Lease.ID || ledger.leases() != 2 {
		t.Fatalf("a lease in another thread = %+v with %d leases; want one of its own", other.Lease, ledger.leases())
	}

	// Revoking the one lease takes back what both of its requests were given, and
	// leaves the other thread's alone.
	if _, err := ledger.service.RevokeLease(ctx, ledger.owner(raya), given.Lease.ID); err != nil {
		t.Fatal(err)
	}
	for name, request := range map[string]agents.Interaction{"the first": given.Request, "the second": renewed.Request} {
		if _, err := ledger.answer(request); !errors.Is(err, ErrRevoked) {
			t.Errorf("%s request answered after its lease was revoked: %v, want ErrRevoked", name, err)
		}
	}
	if answered, err := ledger.answer(other.Request); err != nil || answered.Status != StatusCompleted {
		t.Errorf("the other thread's request = %+v, %v; want it delivered", answered, err)
	}

	// Chosen again once it has run out, an access renews the same lease: it is
	// the pair's lease in that thread, not one for each choice.
	ledger.clock.Advance(2 * time.Hour)
	later := requestTo(t, ledger.ask("1790001000.000100", raya), raya)
	if later.Status != StatusPending {
		t.Fatalf("a request after the lease ran out = %+v", later)
	}
	again := ledger.decide(raya, later, ChoiceAccess)
	if again.Lease == nil || again.Lease.ID == given.Lease.ID || again.Lease.RevokedAt != nil || ledger.leases() != 3 {
		t.Fatalf("access after a revoke = %+v with %d leases; want a new lease, as the revoked one is over", again.Lease, ledger.leases())
	}
}

// TestNoAnswerIsDeliveredAfterTheAccessItRodeOnWasRevoked: revoking a lease
// takes back the access of what was approved under it and not yet answered,
// the request that gave it included: the answer is not delivered, and the
// request is closed instead, saying why. What was approved after the revoke, or
// approved as a summary, is not touched.
func TestNoAnswerIsDeliveredAfterTheAccessItRodeOnWasRevoked(t *testing.T) {
	ledger := newSlackLedger(t)
	ctx := context.Background()
	lease, granting := ledger.givenAccess(raya)
	followUp := requestTo(t, ledger.ask("1790000100.000200", raya), raya)
	if followUp.Status != StatusApproved {
		t.Fatalf("follow-up = %+v", followUp)
	}
	ledger.clock.Advance(time.Minute)
	if _, err := ledger.service.RevokeLease(ctx, ledger.owner(raya), lease.ID); err != nil {
		t.Fatal(err)
	}

	for name, request := range map[string]agents.Interaction{"the request that gave the lease": granting, "a follow-up approved under it": followUp} {
		answered, err := ledger.answer(request)
		if !errors.Is(err, ErrRevoked) || answered.Status == StatusCompleted {
			t.Errorf("%s answered after the revoke: %+v, %v; want ErrRevoked", name, answered, err)
		}
		closed := ledger.read(request.ID)
		if closed.Status != StatusFailed || closed.Result == nil || closed.Result.Summary != accessRevokedReason || closed.Result.Status != StatusFailed {
			t.Errorf("%s = %+v, result %+v; want it closed, saying why", name, closed, closed.Result)
		}
		if got := ledger.events(request.ID); got[len(got)-1] != agentsv1.InteractionCompletedDetailType+":failed" {
			t.Errorf("%s: events = %v, want the failure told", name, got)
		}
		if again, err := ledger.answer(request); !errors.Is(err, ErrConflict) {
			t.Errorf("%s answered again: %+v, %v; want ErrConflict", name, again, err)
		}
	}
	if ask, err := ledger.service.Ask(ctx, followUp.AskID); err != nil || !ask.Open() {
		t.Errorf("an answer that was not delivered won the ask: %+v, %v", ask, err)
	}

	// Approved after the revoke, it is the owner's new word, and goes out.
	ledger.clock.Advance(time.Second)
	fresh := requestTo(t, ledger.ask("1790000300.000400", raya), raya)
	freshResult := ledger.decide(raya, fresh, ChoiceAccess)
	if answered, err := ledger.answer(freshResult.Request); err != nil || answered.Status != StatusCompleted {
		t.Errorf("an access chosen after the revoke: %+v, %v; want it delivered", answered, err)
	}
	// A summary rides on no lease.
	summary := requestTo(t, ledger.askIn(inAnotherThread("1790000400.000500"), raya), raya)
	summaryResult := ledger.decide(raya, summary, ChoiceSummary)
	if answered, err := ledger.answer(summaryResult.Request); err != nil || answered.Status != StatusCompleted {
		t.Errorf("a summary after a revoke elsewhere: %+v, %v; want it delivered", answered, err)
	}
}

// The lease is the owner's to give and to end, and only for the pair of members
// and the thread: revoking the lease of one asker does not stop another's.
func TestRevokingOneLeaseLeavesAnotherAskersAlone(t *testing.T) {
	ledger := newSlackLedger(t)
	ctx := context.Background()
	lease, _ := ledger.givenAccess(raya)
	other, err := ledger.service.CreateAsk(ctx, AskInput{
		OrganizationID: ledger.org, AskerUserID: "user_other", Slack: inThread("1790000050.000150"),
		Question: "And me?", Owners: []AskOwner{{UserID: raya}},
	})
	if err != nil {
		t.Fatal(err)
	}
	otherResult := ledger.decide(raya, requestTo(t, other, raya), ChoiceAccess)
	if otherResult.Lease == nil || otherResult.Lease.ID == lease.ID {
		t.Fatalf("the other asker's lease = %+v", otherResult.Lease)
	}
	if _, err := ledger.service.RevokeLease(ctx, ledger.owner(raya), lease.ID); err != nil {
		t.Fatal(err)
	}
	// The other asker's request rode on their own lease.
	if answered, err := ledger.answer(otherResult.Request); err != nil || answered.Status != StatusCompleted {
		t.Fatalf("the other asker's answer = %+v, %v; want it delivered", answered, err)
	}
}

// An ask to two owners, one of whom has a lease that covers it, is approved for
// that owner alone: the other still has to be asked, and is closed, saying who
// answered, when the first to answer does.
func TestAnAskToTwoOwnersIsApprovedOnlyWhereALeaseCoversIt(t *testing.T) {
	ledger := newSlackLedger(t)
	lease, _ := ledger.givenAccess(raya)
	result := ledger.ask("1790000100.000200", raya, maya)
	forRaya, forMaya := requestTo(t, result, raya), requestTo(t, result, maya)
	if forRaya.Status != StatusApproved || forRaya.Decision.DecidedBy.Reason != lease.ID || forMaya.Status != StatusPending {
		t.Fatalf("Raya's request = %+v, Maya's = %+v", forRaya, forMaya)
	}
	if answered, err := ledger.answer(forRaya); err != nil || answered.Status != StatusCompleted {
		t.Fatalf("Raya's answer = %+v, %v", answered, err)
	}
	closed := ledger.read(forMaya.ID)
	if closed.Status != StatusRejected || closed.Decision.Message != "Answered by Name of "+raya || closed.Decision.DecidedBy.Kind != DecidedBySystem {
		t.Fatalf("Maya's request = %+v, decision %+v", closed, closed.Decision)
	}
	// This ask is settled; the first one, whose approved request nobody has
	// answered, is still open.
	if open, err := ledger.service.CountOpenAsks(context.Background(), ledger.org, asker); err != nil || open != 1 {
		t.Fatalf("open asks = %d, %v", open, err)
	}
}
