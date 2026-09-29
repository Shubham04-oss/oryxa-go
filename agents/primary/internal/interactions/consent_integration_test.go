package interactions

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
)

// clickOn is an owner's click in Slack on the button of the choice: the ticket
// that button carries, the member Slack's user was matched to, and Slack.
func (ledger *slackLedger) clickOn(request agents.Interaction, choice, clicker string) (ConsentResult, error) {
	ledger.t.Helper()
	tickets, err := ledger.service.ConsentTickets(context.Background(), request.ID, "ntc_"+request.ID)
	if err != nil {
		ledger.t.Fatalf("ConsentTickets: %v", err)
	}
	ticket := map[string]string{ChoiceSummary: tickets.Summary, ChoiceAccess: tickets.Access, ChoiceDeny: tickets.Deny}[choice]
	return ledger.service.Consent(context.Background(), ticket, clicker, SurfaceSlack)
}

// TestTheThreeChoicesAreDecisions: each button is a decision like any other,
// granting a subset of what was asked, recorded with who made it and where,
// and announced with what it allowed.
func TestTheThreeChoicesAreDecisions(t *testing.T) {
	for _, testCase := range []struct {
		choice      string
		status      string
		decision    string
		granted     []string
		consent     string
		message     string
		wantsALease bool
	}{
		{ChoiceSummary, StatusApproved, DecisionApproved, []string{PermissionConsentSummary}, ConsentSummary, "Approved: AI summary only.", false},
		{ChoiceAccess, StatusApproved, DecisionApproved, []string{PermissionConsentSummary, PermissionConsentAccess}, ConsentAccess, "Approved: access for 1 hour.", true},
		{ChoiceDeny, StatusRejected, DecisionRejected, nil, "", "Denied.", false},
	} {
		t.Run(testCase.choice, func(t *testing.T) {
			ledger := newSlackLedger(t)
			request := requestTo(t, ledger.ask("1790000000.000100", raya), raya)
			result, err := ledger.clickOn(request, testCase.choice, raya)
			if err != nil {
				t.Fatal(err)
			}
			decided := result.Request
			if decided.Status != testCase.status || decided.Decision == nil || decided.Decision.Decision != testCase.decision ||
				decided.Decision.Message != testCase.message || decided.Decision.Consent != testCase.consent ||
				!slices.Equal(decided.Decision.GrantedPermissions, nonNilStrings(testCase.granted)) || decided.Revision != 2 ||
				decided.Decision.DecidedBy == nil || *decided.Decision.DecidedBy != (agents.InteractionDecider{Kind: DecidedByMember, UserID: raya, Surface: SurfaceSlack}) {
				t.Fatalf("decided = %+v, decision %+v", decided, decided.Decision)
			}
			wantTools := []string{"knowledge"}
			if testCase.choice == ChoiceDeny {
				wantTools = []string{}
			}
			if !slices.Equal(decided.Decision.AllowedTools, wantTools) {
				t.Errorf("allowed tools = %v, want %v", decided.Decision.AllowedTools, wantTools)
			}
			if result.Choice != testCase.choice || result.NoticeID != "ntc_"+request.ID || (result.Lease != nil) != testCase.wantsALease {
				t.Errorf("result = %+v", result)
			}
			stored := ledger.read(request.ID)
			if stored.Status != testCase.status || consentOf(stored) != testCase.consent || !DecidedByItsOwner(stored) {
				t.Errorf("stored = %+v", stored)
			}
			// The decided event says how far the answer may go, where it was decided,
			// and nothing of what was asked.
			var event string
			if err := ledger.database.Pool.QueryRow(context.Background(), `SELECT detail::text FROM agents_primary.agents_outbox
				WHERE detail->>'request_id'=$1 AND detail_type=$2`, request.ID, agentsv1.InteractionDecidedDetailType).Scan(&event); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(event, `"surface": "slack"`) || strings.Contains(event, "dev keys") || strings.Contains(event, testCase.message) ||
				strings.Contains(event, `"consent": `) != (testCase.consent != "") || !strings.Contains(event, fmt.Sprintf(`"status": "%s"`, testCase.status)) {
				t.Errorf("decided event = %s", event)
			}
			if testCase.consent != "" && !strings.Contains(event, `"consent": "`+testCase.consent+`"`) {
				t.Errorf("decided event = %s, want consent %q", event, testCase.consent)
			}
			if got := ledger.leases(); got != map[bool]int{true: 1, false: 0}[testCase.wantsALease] {
				t.Errorf("%d leases", got)
			}
		})
	}
}

// TestAWebChoiceDecidesLikeAClick: the owner's own session decides as a click
// does, recorded as made on the web, and only the owner's.
func TestAWebChoiceDecidesLikeAClick(t *testing.T) {
	ledger := newSlackLedger(t)
	result := ledger.ask("1790000000.000100", raya, maya)
	request := requestTo(t, result, raya)

	// Nobody but the addressed member's own session chooses.
	for name, actor := range map[string]agents.InteractionActor{
		"the asker":             ledger.owner(asker),
		"the other owner":       ledger.owner(maya),
		"the owner's agent":     ledger.agentOf(raya),
		"a member elsewhere":    {OrganizationID: "org_other", UserID: raya, AgentID: "agt_" + raya, AgentName: "Carl", AgentKind: "primary", Session: agents.SessionMember},
		"a session that is not": {OrganizationID: ledger.org, UserID: raya, AgentID: "agt_" + raya, AgentName: "Carl", AgentKind: "primary"},
	} {
		if _, err := ledger.service.ConsentAsOwner(context.Background(), actor, request.ID, ChoiceAccess); !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound) {
			t.Errorf("%s chose: %v, want ErrForbidden or ErrNotFound", name, err)
		}
	}
	if ledger.read(request.ID).Status != StatusPending {
		t.Fatal("a refused choice decided the request")
	}
	for _, choice := range []string{"", "everything", "ALL"} {
		if _, err := ledger.service.ConsentAsOwner(context.Background(), ledger.owner(raya), request.ID, choice); !errors.Is(err, ErrInvalid) {
			t.Errorf("choice %q: %v, want ErrInvalid", choice, err)
		}
	}
	if _, err := ledger.service.ConsentAsOwner(context.Background(), ledger.owner(raya), "irq_missing", ChoiceDeny); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing request: %v", err)
	}

	decided, err := ledger.service.ConsentAsOwner(context.Background(), ledger.owner(raya), request.ID, " Summary ")
	if err != nil || decided.Request.Status != StatusApproved || decided.Request.Decision.Consent != ConsentSummary ||
		*decided.Request.Decision.DecidedBy != (agents.InteractionDecider{Kind: DecidedByMember, UserID: raya, Surface: SurfaceWeb}) {
		t.Fatalf("decided = %+v, %v", decided, err)
	}
	// The other owner's request is theirs to decide, and is untouched.
	if got := ledger.read(requestTo(t, result, maya).ID); got.Status != StatusPending {
		t.Fatalf("the other owner's request = %+v", got)
	}
}

// TestAClickThatDidNotDecideItLearnsWhoDid: the second choice of a request,
// from either surface, is not applied: it answers ErrAlreadyDecided, and the
// request it carries is the winner's.
func TestAClickThatDidNotDecideItLearnsWhoDid(t *testing.T) {
	ledger := newSlackLedger(t)
	request := requestTo(t, ledger.ask("1790000000.000100", raya), raya)
	tickets, err := ledger.service.ConsentTickets(context.Background(), request.ID, "ntc_1")
	if err != nil {
		t.Fatal(err)
	}

	first, err := ledger.service.Consent(context.Background(), tickets.Summary, raya, SurfaceSlack)
	if err != nil {
		t.Fatal(err)
	}
	later, err := ledger.service.Consent(context.Background(), tickets.Access, raya, SurfaceSlack)
	if !errors.Is(err, ErrAlreadyDecided) || later.Request.ID != request.ID || later.Request.Decision == nil ||
		later.Request.Decision.Consent != ConsentSummary || later.Request.Revision != first.Request.Revision || later.Lease != nil {
		t.Fatalf("a second click = %+v, %v; want ErrAlreadyDecided with the first's decision", later, err)
	}
	webbed, err := ledger.service.ConsentAsOwner(context.Background(), ledger.owner(raya), request.ID, ChoiceDeny)
	if !errors.Is(err, ErrAlreadyDecided) || webbed.Request.Status != StatusApproved || webbed.Request.Decision.DecidedBy.Surface != SurfaceSlack {
		t.Fatalf("a web choice after a click = %+v, %v; want ErrAlreadyDecided, decided in slack", webbed, err)
	}
	if ledger.leases() != 0 || ledger.read(request.ID).Revision != 2 {
		t.Fatal("a choice that lost changed the request or made a lease")
	}
	if got := ledger.events(request.ID); len(got) != 2 {
		t.Fatalf("events = %v, want requested and decided alone", got)
	}
}

// TestConcurrentChoicesDecideOnce: an owner clicking Slack's button and the web
// card's at the same moment, or an impatient double click, decides the request
// once, by the row lock: one choice is recorded, one lease at most is given,
// one decided event is sent, and every other call learns the winner.
func TestConcurrentChoicesDecideOnce(t *testing.T) {
	ledger := newSlackLedger(t)
	ctx := context.Background()
	const attempts = 24
	choices := []string{ChoiceSummary, ChoiceAccess, ChoiceDeny}
	accessWins := 0
	for round := range 3 {
		// Each round is a thread of its own: an access chosen in one would
		// otherwise approve the next round's ask, as a lease does.
		thread := SlackThread{TeamID: "T1", ChannelID: "C1", ThreadTS: fmt.Sprintf("179000000%d.000100", round), TriggerTS: fmt.Sprintf("179000000%d.000100", round)}
		request := requestTo(t, ledger.askIn(thread, raya), raya)
		tickets, err := ledger.service.ConsentTickets(ctx, request.ID, "ntc_1")
		if err != nil {
			t.Fatal(err)
		}
		byChoice := map[string]string{ChoiceSummary: tickets.Summary, ChoiceAccess: tickets.Access, ChoiceDeny: tickets.Deny}

		type outcome struct {
			choice string
			result ConsentResult
			err    error
		}
		outcomes := make([]outcome, attempts)
		var start, done sync.WaitGroup
		start.Add(1)
		for index := range attempts {
			done.Add(1)
			go func() {
				defer done.Done()
				choice := choices[(index+round)%len(choices)]
				start.Wait()
				if index%2 == 0 {
					result, err := ledger.service.Consent(ctx, byChoice[choice], raya, SurfaceSlack)
					outcomes[index] = outcome{choice, result, err}
					return
				}
				result, err := ledger.service.ConsentAsOwner(ctx, ledger.owner(raya), request.ID, choice)
				outcomes[index] = outcome{choice, result, err}
			}()
		}
		start.Done()
		done.Wait()

		var winners []outcome
		for _, got := range outcomes {
			switch {
			case got.err == nil:
				winners = append(winners, got)
			case !errors.Is(got.err, ErrAlreadyDecided):
				t.Fatalf("round %d: a choice failed with %v, want it to win or learn who did", round, got.err)
			}
		}
		if len(winners) != 1 {
			t.Fatalf("round %d: %d choices won, want exactly one", round, len(winners))
		}
		winner := winners[0]
		stored := ledger.read(request.ID)
		wantConsent := map[string]string{ChoiceSummary: ConsentSummary, ChoiceAccess: ConsentAccess, ChoiceDeny: ""}[winner.choice]
		if consentOf(stored) != wantConsent || stored.Revision != 2 ||
			(stored.Status == StatusRejected) != (winner.choice == ChoiceDeny) {
			t.Fatalf("round %d: the winner chose %s, and the request is %+v", round, winner.choice, stored)
		}
		for _, got := range outcomes {
			if got.err != nil && (got.result.Request.Decision == nil || got.result.Request.Decision.Consent != wantConsent || got.result.Request.Revision != 2) {
				t.Fatalf("round %d: a loser was told %+v, want the winner's decision", round, got.result.Request.Decision)
			}
		}
		if events := ledger.events(request.ID); len(events) != 2 {
			t.Fatalf("round %d: events = %v, want requested and one decided", round, events)
		}
		// The winner's lease, if it chose access, and no other choice's.
		if (winner.result.Lease != nil) != (winner.choice == ChoiceAccess) {
			t.Fatalf("round %d: a choice of %s left the lease %+v", round, winner.choice, winner.result.Lease)
		}
		if winner.choice == ChoiceAccess {
			accessWins++
		}
		// Each round is a thread of its own, so the leases are one for each
		// winning access: the losers gave none.
		if got := ledger.leases(); got != accessWins {
			t.Fatalf("round %d: %d leases after %d winning accesses", round, got, accessWins)
		}
	}
}

// TestAForgedOrExpiredTicketDecidesNothing: a click decides only with a ticket
// this ledger made and that has not run out, and a ticket no other member than
// its owner may use.
func TestAForgedOrExpiredTicketDecidesNothing(t *testing.T) {
	ledger := newSlackLedger(t)
	request := requestTo(t, ledger.ask("1790000000.000100", raya, maya), raya)
	tickets, err := ledger.service.ConsentTickets(context.Background(), request.ID, "ntc_1")
	if err != nil {
		t.Fatal(err)
	}
	if !tickets.ExpiresAt.Equal(request.ExpiresAt) {
		t.Fatalf("tickets expire at %s, want the request's %s", tickets.ExpiresAt, request.ExpiresAt)
	}
	other, err := NewTickets([]byte("another-key-of-at-least-32-bytes-long!!"))
	if err != nil {
		t.Fatal(err)
	}
	forge := func(claims TicketClaims) string {
		ticket, err := other.Mint(claims)
		if err != nil {
			t.Fatal(err)
		}
		return ticket
	}
	claims := TicketClaims{RequestID: request.ID, OwnerUserID: raya, NoticeID: "ntc_1", Choice: ChoiceAccess, ExpiresAt: request.ExpiresAt}
	altered := []byte(tickets.Deny)
	altered[len(altered)-3] ^= 1
	for name, ticket := range map[string]string{
		"a ticket signed under another key": forge(claims),
		"a ticket with a bit of it changed": string(altered),
		"a ticket for a choice made up":     strings.Replace(tickets.Summary, tickets.Summary[3:10], "AAAAAAA", 1),
		"nothing":                           "",
		"the secret in place of the ticket": "t1.e30.e30",
	} {
		if _, err := ledger.service.Consent(context.Background(), ticket, raya, SurfaceSlack); !errors.Is(err, ErrTicketInvalid) {
			t.Errorf("%s: %v, want ErrTicketInvalid", name, err)
		}
	}
	if ledger.read(request.ID).Status != StatusPending {
		t.Fatal("a forged ticket decided the request")
	}

	// A ticket that ran out is expired, and the request, which may live on, is
	// asked again with new ones.
	ledger.clock.Advance(defaultExpiry - time.Minute)
	short, err := NewTickets(ticketKey)
	if err != nil {
		t.Fatal(err)
	}
	brief, err := short.Mint(TicketClaims{RequestID: request.ID, OwnerUserID: raya, NoticeID: "ntc_1", Choice: ChoiceAccess, ExpiresAt: ledger.clock.Now().Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.service.Consent(context.Background(), brief, raya, SurfaceSlack); !errors.Is(err, ErrTicketExpired) {
		t.Errorf("an expired ticket: %v, want ErrTicketExpired", err)
	}
	// The ticket of a request that has itself expired reads as expired, both
	// ways: it made no decision, and tickets are not made for it either.
	ledger.clock.Advance(2 * time.Minute)
	if _, err := ledger.service.Consent(context.Background(), tickets.Access, raya, SurfaceSlack); !errors.Is(err, ErrTicketExpired) {
		t.Errorf("a ticket after its request expired: %v, want ErrTicketExpired", err)
	}
	if _, err := ledger.service.ConsentTickets(context.Background(), request.ID, "ntc_2"); !errors.Is(err, ErrExpired) {
		t.Errorf("tickets for an expired request: %v, want ErrExpired", err)
	}
	if ledger.read(request.ID).Status != StatusPending || ledger.leases() != 0 {
		t.Fatal("an expired ticket decided the request")
	}
}

// TestAWebChoiceOfAnExpiredRequestIsRefused: a request that ran out cannot be
// decided in either place.
func TestAWebChoiceOfAnExpiredRequestIsRefused(t *testing.T) {
	ledger := newSlackLedger(t)
	request := requestTo(t, ledger.ask("1790000000.000100", raya), raya)
	ledger.clock.Advance(defaultExpiry + time.Second)
	if _, err := ledger.service.ConsentAsOwner(context.Background(), ledger.owner(raya), request.ID, ChoiceSummary); !errors.Is(err, ErrExpired) {
		t.Fatalf("choosing on an expired request: %v, want ErrExpired", err)
	}
}

// TestTheWrongClickerDecidesNothing: the ticket is for one owner. Someone else
// holding it, another member Slack matched the click to, or nobody, is refused,
// and the request is left pending.
func TestTheWrongClickerDecidesNothing(t *testing.T) {
	ledger := newSlackLedger(t)
	result := ledger.ask("1790000000.000100", raya, maya)
	request := requestTo(t, result, raya)
	for name, clicker := range map[string]string{
		"the other owner": maya, "the asker": asker, "nobody": "", "a member with a similar id": raya + " ",
	} {
		if _, err := ledger.clickOn(request, ChoiceAccess, clicker); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s clicked: %v, want ErrForbidden", name, err)
		}
	}
	if got := ledger.read(request.ID); got.Status != StatusPending || ledger.leases() != 0 {
		t.Fatalf("a wrong clicker decided the request: %+v", got)
	}
	// A ticket made for one request decides no other, even of the same owner:
	// the ticket names its request.
	sibling := requestTo(t, ledger.ask("1790000001.000100", raya), raya)
	tickets, err := ledger.service.ConsentTickets(context.Background(), request.ID, "ntc_1")
	if err != nil {
		t.Fatal(err)
	}
	if decided, err := ledger.service.Consent(context.Background(), tickets.Summary, raya, SurfaceSlack); err != nil || decided.Request.ID != request.ID {
		t.Fatalf("the ticket decided %+v, %v", decided.Request.ID, err)
	}
	if ledger.read(sibling.ID).Status != StatusPending {
		t.Fatal("a ticket decided a request it was not made for")
	}
	// The surface a ticket is used on is Slack or the web.
	next := requestTo(t, ledger.ask("1790000002.000100", raya), raya)
	nextTickets, err := ledger.service.ConsentTickets(context.Background(), next.ID, "ntc_1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.service.Consent(context.Background(), nextTickets.Summary, raya, "email"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a ticket used somewhere else: %v, want ErrInvalid", err)
	}
	// A ticket made for a request whose owner is someone else is refused
	// even when it is signed: tickets are minted from the ledger, so this is a
	// key that leaked.
	leaked, err := testTickets(t).Mint(TicketClaims{RequestID: next.ID, OwnerUserID: maya, NoticeID: "ntc_1", Choice: ChoiceAccess, ExpiresAt: next.ExpiresAt})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.service.Consent(context.Background(), leaked, maya, SurfaceSlack); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a signed ticket naming the wrong owner: %v, want ErrForbidden", err)
	}
	if ledger.read(next.ID).Status != StatusPending {
		t.Fatal("a ticket naming the wrong owner decided the request")
	}
}

// TestOnlyARequestThatAsksForConsentHasButtons: the choices are for the
// requests the Slack app makes. Any other is decided with a decision, and a
// request that was decided has no buttons left.
func TestOnlyARequestThatAsksForConsentHasButtons(t *testing.T) {
	ledger := newSlackLedger(t)
	ordinary := storedRequest(ledger.org, ledger.org+"_ordinary", asker, raya, ledger.clock.Now())
	ordinary.Approval = ApprovalOwner
	if _, err := ledger.store.Create(context.Background(), ordinary, "ordinary"); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.service.ConsentTickets(context.Background(), ordinary.ID, "ntc_1"); !errors.Is(err, ErrInvalid) {
		t.Errorf("tickets for an ordinary request: %v, want ErrInvalid", err)
	}
	if _, err := ledger.service.ConsentAsOwner(context.Background(), ledger.owner(raya), ordinary.ID, ChoiceSummary); !errors.Is(err, ErrInvalid) {
		t.Errorf("a choice of an ordinary request: %v, want ErrInvalid", err)
	}
	asked := requestTo(t, ledger.ask("1790000000.000100", raya), raya)
	ledger.decide(raya, asked, ChoiceDeny)
	if _, err := ledger.service.ConsentTickets(context.Background(), asked.ID, "ntc_1"); !errors.Is(err, ErrConflict) {
		t.Errorf("tickets for a decided request: %v, want ErrConflict", err)
	}
	if _, err := ledger.service.ConsentTickets(context.Background(), "irq_missing", "ntc_1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("tickets for a missing request: %v, want ErrNotFound", err)
	}
	if _, err := ledger.service.ConsentTickets(context.Background(), asked.ID, ""); err == nil {
		t.Error("tickets for no notice")
	}
}

// TestAccessLastsAnHourOrWhatTheOrganizationAllows: the lease access gives is
// an hour long, or as long as the organization lets consent last when that is
// less, and the decision says so.
func TestAccessLastsAnHourOrWhatTheOrganizationAllows(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		policy time.Duration
		want   time.Duration
		says   string
	}{
		{"no policy", 0, time.Hour, "Approved: access for 1 hour."},
		{"a longer policy", 4 * time.Hour, time.Hour, "Approved: access for 1 hour."},
		{"a policy of half an hour", 30 * time.Minute, 30 * time.Minute, "Approved: access for 30 minutes."},
		{"a policy of ten minutes", 10 * time.Minute, 10 * time.Minute, "Approved: access for 10 minutes."},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ledger := newSlackLedger(t, func(config *Config) {
				config.Policy = fakeWorkspacePolicy{settings: policy.Settings{ExpireAfter: testCase.policy}}
			})
			request := requestTo(t, ledger.ask("1790000000.000100", raya), raya)
			result := ledger.decide(raya, request, ChoiceAccess)
			if result.Lease == nil || !result.Lease.ExpiresAt.Equal(ledger.clock.Now().Add(testCase.want)) || result.Request.Decision.Message != testCase.says {
				t.Fatalf("lease = %+v, message %q; want %s and %q", result.Lease, result.Request.Decision.Message, testCase.want, testCase.says)
			}
			if result.Lease.OwnerUserID != raya || result.Lease.AskerUserID != asker || result.Lease.ChannelID != "C1" ||
				result.Lease.ThreadTS != "1790000000.000100" || result.Lease.RequestID != request.ID || result.Lease.RevokedAt != nil {
				t.Fatalf("lease = %+v", result.Lease)
			}
		})
	}
}

// A policy that cannot be read fails the choice of access, rather than giving
// an hour nobody said may be given, and the request is left for the owner to
// choose again; a choice that gives no lease does not need it.
func TestAChoiceOfAccessFailsWhenThePolicyCannotBeRead(t *testing.T) {
	ledger := newSlackLedger(t)
	request := requestTo(t, ledger.ask("1790000000.000100", raya), raya)
	ledger.service.policy = fakeWorkspacePolicy{err: errors.New("policy storage unavailable")}

	if _, err := ledger.service.ConsentAsOwner(context.Background(), ledger.owner(raya), request.ID, ChoiceAccess); err == nil || errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("access with no policy to read: %v, want the failure", err)
	}
	if ledger.read(request.ID).Status != StatusPending || ledger.leases() != 0 {
		t.Fatal("a failed choice of access decided the request or gave a lease")
	}
	if result, err := ledger.service.ConsentAsOwner(context.Background(), ledger.owner(raya), request.ID, ChoiceSummary); err != nil || result.Request.Status != StatusApproved {
		t.Fatalf("a summary needs no policy: %+v, %v", result, err)
	}
}
