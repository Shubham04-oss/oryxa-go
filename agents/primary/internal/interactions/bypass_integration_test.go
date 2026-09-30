package interactions

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// bypassLedger is a ledger whose log the test reads.
func bypassLedger(t *testing.T) (*slackLedger, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	ledger := newSlackLedger(t, func(config *Config) {
		config.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	return ledger, &logs
}

// TestABypassApprovesWithNoClick: the router says a request needs no owner's
// click, and the ledger records it as a decision by the bypass, with why, that
// allows excerpts: the request is approved, its decider is the bypass, nobody
// is named, the event carries it all, the ask says it was bypassed, and it is
// on record in the log. The answer that follows is an ordinary one.
func TestABypassApprovesWithNoClick(t *testing.T) {
	ledger, logs := bypassLedger(t)
	ctx := context.Background()
	result := ledger.ask("1790000000.000100", raya, maya)
	request := requestTo(t, result, raya)

	approved, err := ledger.service.Bypass(ctx, request.ID, "private_channel_marked")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != StatusApproved || approved.Revision != 2 || approved.Decision == nil || approved.Decision.Consent != ConsentAccess ||
		!slices.Equal(approved.Decision.GrantedPermissions, []string{PermissionConsentSummary, PermissionConsentAccess}) ||
		!slices.Equal(approved.Decision.AllowedTools, []string{"knowledge"}) ||
		approved.Decision.DecidedBy == nil || *approved.Decision.DecidedBy != (agents.InteractionDecider{Kind: DecidedByBypass, Surface: SurfaceBypass, Reason: "private_channel_marked"}) ||
		DecidedByItsOwner(approved) {
		t.Fatalf("approved = %+v, decision %+v", approved, approved.Decision)
	}
	if got := ledger.events(request.ID); !slices.Equal(got, []string{agentsv1.InteractionRequestedDetailType + ":pending", agentsv1.InteractionDecidedDetailType + ":approved"}) {
		t.Fatalf("events = %v", got)
	}
	var event string
	if err := ledger.database.Pool.QueryRow(ctx, `SELECT detail::text FROM agents_primary.agents_outbox
		WHERE detail->>'request_id'=$1 AND detail_type=$2`, request.ID, agentsv1.InteractionDecidedDetailType).Scan(&event); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"kind": "bypass"`, `"surface": "bypass"`, `"reason": "private_channel_marked"`, `"consent": "access"`} {
		if !strings.Contains(event, want) {
			t.Errorf("the decided event is missing %s: %s", want, event)
		}
	}
	if strings.Contains(event, `"user_id": "`+raya+`", "surface"`) && strings.Contains(event, `"kind": "bypass", "user_id"`) {
		t.Errorf("a bypass names a member as its decider: %s", event)
	}
	ask, err := ledger.service.Ask(ctx, result.Ask.ID)
	if err != nil || !ask.Bypass {
		t.Fatalf("the ask = %+v, %v; want it marked bypassed", ask, err)
	}
	// It is on record in the log too, by who it was between and why, and never
	// by what was asked.
	logged := logs.String()
	if !strings.Contains(logged, "approved by a bypass") || !strings.Contains(logged, "private_channel_marked") ||
		!strings.Contains(logged, `"owner_user_id":"`+raya+`"`) || strings.Contains(logged, "dev keys") {
		t.Fatalf("log = %s", logged)
	}

	// The other owner's request is not bypassed, and a decision is made once.
	if got := ledger.read(requestTo(t, result, maya).ID); got.Status != StatusPending {
		t.Fatalf("the other owner's request = %+v", got)
	}
	if _, err := ledger.service.Bypass(ctx, request.ID, "private_channel_marked"); !errors.Is(err, ErrConflict) {
		t.Errorf("a second bypass: %v, want ErrConflict", err)
	}
	if _, err := ledger.service.ConsentAsOwner(ctx, ledger.owner(raya), request.ID, ChoiceDeny); !errors.Is(err, ErrAlreadyDecided) {
		t.Errorf("the owner's choice after a bypass: %v, want ErrAlreadyDecided", err)
	}
	if answered, err := ledger.answer(approved); err != nil || answered.Status != StatusCompleted {
		t.Fatalf("the answer = %+v, %v", answered, err)
	}
}

// TestABypassNeedsAReasonAndARequestThatAsksForConsent.
func TestABypassNeedsAReasonAndARequestThatAsksForConsent(t *testing.T) {
	ledger, _ := bypassLedger(t)
	ctx := context.Background()
	request := requestTo(t, ledger.ask("1790000000.000100", raya), raya)
	for _, reason := range []string{"", "the boss said so", "Private", strings.Repeat("a", 65), "a b", "rea\nson"} {
		if _, err := ledger.service.Bypass(ctx, request.ID, reason); !errors.Is(err, ErrInvalid) {
			t.Errorf("reason %q: %v, want ErrInvalid", reason, err)
		}
	}
	if _, err := ledger.service.Bypass(ctx, "irq_missing", "outranks"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing request: %v, want ErrNotFound", err)
	}
	ordinary := storedRequest(ledger.org, ledger.org+"_ordinary", asker, raya, ledger.clock.Now())
	ordinary.Approval = ApprovalOwner
	if _, err := ledger.store.Create(ctx, ordinary, "ordinary"); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.service.Bypass(ctx, ordinary.ID, "outranks"); !errors.Is(err, ErrInvalid) {
		t.Errorf("a request that does not ask for consent: %v, want ErrInvalid", err)
	}
	if got := ledger.read(request.ID); got.Status != StatusPending {
		t.Fatalf("a refused bypass decided the request: %+v", got)
	}
	// A rejected request is not approved by one after the fact.
	rejected := ledger.decide(raya, request, ChoiceDeny).Request
	if _, err := ledger.service.Bypass(ctx, rejected.ID, "outranks"); !errors.Is(err, ErrConflict) {
		t.Errorf("a bypass of a rejected request: %v, want ErrConflict", err)
	}
}

// TestAnAskCanBeBypassedAsItIsMade: the router can name the owners it needs no
// click from, and their requests are created approved, in the same transaction,
// as a bypass; the others wait. Asking again changes nothing and logs nothing
// more.
func TestAnAskCanBeBypassedAsItIsMade(t *testing.T) {
	ledger, logs := bypassLedger(t)
	thread := inThread("1790000000.000100")
	owners := []AskOwner{{UserID: raya, DisplayName: "Raya"}, {UserID: maya, DisplayName: "Maya", Bypass: "outranks"}}
	result := ledger.askOwners(thread, owners...)
	forMaya := requestTo(t, result, maya)
	if forMaya.Status != StatusApproved || forMaya.Decision == nil || forMaya.Decision.DecidedBy == nil ||
		*forMaya.Decision.DecidedBy != (agents.InteractionDecider{Kind: DecidedByBypass, Surface: SurfaceBypass, Reason: "outranks"}) ||
		forMaya.Decision.Consent != ConsentAccess || forMaya.Revision != 2 {
		t.Fatalf("Maya's request = %+v, decision %+v", forMaya, forMaya.Decision)
	}
	if got := requestTo(t, result, raya); got.Status != StatusPending {
		t.Fatalf("Raya's request = %+v", got)
	}
	if got := ledger.events(forMaya.ID); !slices.Equal(got, []string{agentsv1.InteractionRequestedDetailType + ":pending", agentsv1.InteractionDecidedDetailType + ":approved"}) {
		t.Fatalf("Maya's events = %v", got)
	}
	if !result.Ask.Bypass {
		t.Fatalf("the ask = %+v, want it bypassed", result.Ask)
	}
	if again := ledger.askOwners(thread, owners...); requestTo(t, again, maya).ID != forMaya.ID || requestTo(t, again, maya).Revision != 2 || !again.Ask.Bypass {
		t.Fatalf("asking again = %+v", again)
	}
	if got := ledger.events(forMaya.ID); len(got) != 2 {
		t.Fatalf("asking again added events: %v", got)
	}
	if strings.Count(logs.String(), "approved by a bypass") != 1 {
		t.Fatalf("a bypass was logged %d times, want once:\n%s", strings.Count(logs.String(), "approved by a bypass"), logs.String())
	}
}

// A lease is the owner's own permission, and wins over a bypass: the follow-up
// is decided as the lease's, and the ask is not marked as bypassed.
func TestALeaseWinsOverABypass(t *testing.T) {
	ledger, _ := bypassLedger(t)
	lease, _ := ledger.givenAccess(raya)
	result := ledger.askOwners(inThread("1790000100.000200"), AskOwner{UserID: raya, Bypass: "outranks"})
	request := requestTo(t, result, raya)
	if request.Status != StatusApproved || request.Decision.DecidedBy.Kind != DecidedByLease || request.Decision.DecidedBy.Reason != lease.ID || result.Ask.Bypass {
		t.Fatalf("the request = %+v, decision %+v, ask %+v; want the lease's decision", request, request.Decision, result.Ask)
	}
}

// A request that a lease covers is still bypassed when the router asks for it
// with no lease: an expired lease is no lease.
func TestABypassStillAppliesWhenTheLeaseHasRunOut(t *testing.T) {
	ledger, _ := bypassLedger(t)
	ledger.givenAccess(raya)
	ledger.clock.Advance(2 * 3600 * 1e9)
	result := ledger.askOwners(inThread("1790000100.000200"), AskOwner{UserID: raya, Bypass: "outranks"})
	if got := requestTo(t, result, raya); got.Decision == nil || got.Decision.DecidedBy.Kind != DecidedByBypass {
		t.Fatalf("the request = %+v, decision %+v; want the bypass's decision", got, got.Decision)
	}
}
