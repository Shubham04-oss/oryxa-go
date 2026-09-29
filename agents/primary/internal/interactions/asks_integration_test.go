package interactions

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
)

// TestAnAskMakesARequestForEachOwnerItNames: one Slack message that names two
// members is one ask and two requests, filed as the asker's primary agent to
// each owner's, held for the owner to decide, asking for the organization's
// knowledge and for consent, and announced as requested with the ask and where
// it came from.
func TestAnAskMakesARequestForEachOwnerItNames(t *testing.T) {
	ledger := newSlackLedger(t)
	result := ledger.ask("1790000000.000100", raya, maya)

	if result.Ask.Origin != OriginSlack || result.Ask.AskerUserID != asker || result.Ask.Kind != AskQuestion || !result.Ask.Open() ||
		result.Ask.TeamID != "T1" || result.Ask.ChannelID != "C1" || result.Ask.ThreadTS != "1790000000.000100" || result.Ask.TriggerTS != "1790000000.000100" {
		t.Fatalf("ask = %+v", result.Ask)
	}
	if len(result.Requests) != 2 {
		t.Fatalf("requests = %+v, want one for each owner", result.Requests)
	}
	for _, owner := range []string{raya, maya} {
		request := requestTo(t, result, owner)
		if request.AskID != result.Ask.ID || request.Origin != OriginSlack || request.Status != StatusPending || request.Approval != ApprovalOwner ||
			request.Requester.UserID != asker || request.Requester.AgentID != "agt_"+asker ||
			request.Target.AgentID != "agt_"+owner || request.Target.UserEmail != owner+"@example.com" || request.Target.UserDisplayName != "Name of "+owner ||
			request.Task != "Where do the dev keys live?" || request.Purpose != slackPurpose || request.TeamspaceID != "" ||
			!slices.Equal(request.RequestedTools, []string{"knowledge"}) || request.ToolsJudgedBy != ToolsJudgedByRequester ||
			!slices.Equal(request.RequestedPermissions, []string{PermissionConsentSummary, PermissionConsentAccess}) ||
			request.Revision != 1 || !request.ExpiresAt.Equal(request.CreatedAt.Add(defaultExpiry)) || request.Decision != nil {
			t.Errorf("request to %s = %+v", owner, request)
		}
		if !HeldForOwner(request) {
			t.Errorf("request to %s is not held for its owner", owner)
		}
		if got := ledger.events(request.ID); !slices.Equal(got, []string{agentsv1.InteractionRequestedDetailType + ":pending"}) {
			t.Errorf("events of the request to %s = %v", owner, got)
		}
		stored := ledger.read(request.ID)
		if stored.AskID != result.Ask.ID || stored.Origin != OriginSlack {
			t.Errorf("stored request = %+v", stored)
		}
	}
	// The event says which ask, where from, and nothing else new.
	var event string
	if err := ledger.database.Pool.QueryRow(context.Background(), `SELECT detail::text FROM agents_primary.agents_outbox
		WHERE detail->>'request_id'=$1`, requestTo(t, result, raya).ID).Scan(&event); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(event, `"ask_id": "`+result.Ask.ID+`"`) || !strings.Contains(event, `"origin": "slack"`) || strings.Contains(event, "dev keys") {
		t.Fatalf("requested event = %s", event)
	}
	if open, err := ledger.service.CountOpenAsks(context.Background(), ledger.org, asker); err != nil || open != 1 {
		t.Fatalf("open asks = %d, %v; want the one", open, err)
	}
	got, err := ledger.service.Ask(context.Background(), result.Ask.ID)
	if err != nil || got.ID != result.Ask.ID || got.AskerUserID != asker {
		t.Fatalf("Ask = %+v, %v", got, err)
	}
	if _, err := ledger.service.Ask(context.Background(), "iak_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing ask: %v", err)
	}
}

// TestAnAskMadeAgainIsTheSameAsk: a Slack delivery is retried until it is
// acknowledged, so an ask made again from the same message stores nothing new
// and returns what it made, whatever state that is in, and one that asks
// something else under the same message is refused.
func TestAnAskMadeAgainIsTheSameAsk(t *testing.T) {
	ledger := newSlackLedger(t)
	first := ledger.ask("1790000000.000100", raya, maya)
	decided := ledger.decide(raya, requestTo(t, first, raya), ChoiceSummary)

	again := ledger.ask("1790000000.000100", raya, maya)
	if again.Ask.ID != first.Ask.ID || len(again.Requests) != 2 {
		t.Fatalf("the retry made %+v, want the first ask", again)
	}
	for _, owner := range []string{raya, maya} {
		if requestTo(t, again, owner).ID != requestTo(t, first, owner).ID {
			t.Errorf("the retry made another request to %s", owner)
		}
	}
	if got := requestTo(t, again, raya); got.Status != StatusApproved || got.Revision != decided.Request.Revision {
		t.Errorf("the retry read the decided request as %+v", got)
	}
	if got := ledger.count(`SELECT count(*) FROM agents_primary.interaction_asks WHERE organization_id=$1`, ledger.org); got != 1 {
		t.Errorf("%d asks stored", got)
	}
	if got := ledger.count(`SELECT count(*) FROM agents_primary.agent_interactions WHERE organization_id=$1`, ledger.org); got != 2 {
		t.Errorf("%d requests stored", got)
	}
	if got := ledger.count(`SELECT count(*) FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1
		AND detail_type=$2`, ledger.org, agentsv1.InteractionRequestedDetailType); got != 2 {
		t.Errorf("%d requested events, want the two of the first ask", got)
	}

	// The same message asking something else is a different request, not a
	// retry: it is refused rather than answered with the first.
	_, err := ledger.service.CreateAsk(context.Background(), AskInput{
		OrganizationID: ledger.org, AskerUserID: asker, Slack: inThread("1790000000.000100"),
		Question: "Something else entirely?", Owners: []AskOwner{{UserID: raya}, {UserID: maya}},
	})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("a different question under the same message: %v, want ErrIdempotencyConflict", err)
	}

	// Another message in the thread is another ask.
	other := ledger.ask("1790000005.000200", raya)
	if other.Ask.ID == first.Ask.ID || requestTo(t, other, raya).ID == requestTo(t, first, raya).ID {
		t.Fatalf("another message made %+v", other)
	}
}

// A retry that names one more owner adds that owner's request to the ask, and
// leaves the others as they are.
func TestAnAskMadeAgainWithAnotherOwnerAddsThem(t *testing.T) {
	ledger := newSlackLedger(t)
	first := ledger.ask("1790000000.000100", raya)
	again := ledger.ask("1790000000.000100", raya, maya)
	if again.Ask.ID != first.Ask.ID || len(again.Requests) != 2 || requestTo(t, again, raya).ID != requestTo(t, first, raya).ID {
		t.Fatalf("the retry made %+v", again)
	}
	if requestTo(t, again, maya).AskID != first.Ask.ID {
		t.Fatalf("the added request is in another ask: %+v", requestTo(t, again, maya))
	}
}

// TestAnAskIsRefusedWhenItCannotBeRight: what the router should never send is
// refused before anything is stored.
func TestAnAskIsRefusedWhenItCannotBeRight(t *testing.T) {
	ledger := newSlackLedger(t)
	valid := AskInput{
		OrganizationID: ledger.org, AskerUserID: asker, Slack: inThread("1790000000.000100"),
		Question: "Where do the dev keys live?", Owners: []AskOwner{{UserID: raya}},
	}
	for name, change := range map[string]func(*AskInput){
		"no organization":       func(in *AskInput) { in.OrganizationID = "" },
		"no asker":              func(in *AskInput) { in.AskerUserID = "" },
		"no question":           func(in *AskInput) { in.Question = "   " },
		"a question too long":   func(in *AskInput) { in.Question = strings.Repeat("q", maxTaskRunes+1) },
		"a NUL in the question": func(in *AskInput) { in.Question = "keys\x00" },
		"no owners":             func(in *AskInput) { in.Owners = nil },
		"eleven owners": func(in *AskInput) {
			in.Owners = nil
			for index := range 11 {
				in.Owners = append(in.Owners, AskOwner{UserID: "user_" + string(rune('a'+index))})
			}
		},
		"the asker as an owner":     func(in *AskInput) { in.Owners = []AskOwner{{UserID: asker}} },
		"an owner with no id":       func(in *AskInput) { in.Owners = []AskOwner{{UserID: ""}} },
		"a bypass that is a phrase": func(in *AskInput) { in.Owners = []AskOwner{{UserID: raya, Bypass: "the boss said so"}} },
		"no team":                   func(in *AskInput) { in.Slack.TeamID = "" },
		"a channel with a colon":    func(in *AskInput) { in.Slack.ChannelID = "C1:2" },
		"no thread":                 func(in *AskInput) { in.Slack.ThreadTS = "" },
		"no trigger":                func(in *AskInput) { in.Slack.TriggerTS = "" },
		"an unknown kind":           func(in *AskInput) { in.Kind = "poll" },
		"an expiry of a second":     func(in *AskInput) { in.ExpiresIn = time.Second },
		"an expiry of a year":       func(in *AskInput) { in.ExpiresIn = 365 * 24 * time.Hour },
	} {
		input := valid
		input.Owners = slices.Clone(valid.Owners)
		change(&input)
		if _, err := ledger.service.CreateAsk(context.Background(), input); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: CreateAsk = %v, want ErrInvalid", name, err)
		}
	}
	if got := ledger.count(`SELECT count(*) FROM agents_primary.interaction_asks WHERE organization_id=$1`, ledger.org); got != 0 {
		t.Fatalf("%d asks stored by refused calls", got)
	}
	// The same owner named twice is asked once.
	duplicated := valid
	duplicated.Owners = []AskOwner{{UserID: raya}, {UserID: raya}}
	if result, err := ledger.service.CreateAsk(context.Background(), duplicated); err != nil || len(result.Requests) != 1 {
		t.Fatalf("an owner named twice: %+v, %v", result, err)
	}
}

// TestAnAskSaysWhatItIsAndWhenItLasts: a secret is an ask of its own kind, an
// expiry the organization allows is the one the ask gets, and what it does not
// allow is refused.
func TestAnAskSaysWhatItIsAndWhenItLasts(t *testing.T) {
	ledger := newSlackLedger(t, func(config *Config) {
		config.Policy = fakeWorkspacePolicy{settings: policy.Settings{ExpireAfter: 2 * time.Hour}}
	})
	capped := ledger.ask("1790000000.000100", raya)
	if got := requestTo(t, capped, raya); !got.ExpiresAt.Equal(got.CreatedAt.Add(2 * time.Hour)) {
		t.Errorf("an ask under a two hour expiry expires at %s after %s", got.ExpiresAt, got.CreatedAt)
	}
	_, err := ledger.service.CreateAsk(context.Background(), AskInput{
		OrganizationID: ledger.org, AskerUserID: asker, Slack: inThread("1790000001.000100"),
		Question: "Keys?", Owners: []AskOwner{{UserID: raya}}, ExpiresIn: 3 * time.Hour,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("an expiry past the organization's = %v, want ErrInvalid", err)
	}
	shorter, err := ledger.service.CreateAsk(context.Background(), AskInput{
		OrganizationID: ledger.org, AskerUserID: asker, Slack: inThread("1790000002.000100"),
		Question: "Keys?", Owners: []AskOwner{{UserID: raya}}, ExpiresIn: 30 * time.Minute, Kind: AskSecret,
	})
	if err != nil || shorter.Ask.Kind != AskSecret || !requestTo(t, shorter, raya).ExpiresAt.Equal(requestTo(t, shorter, raya).CreatedAt.Add(30*time.Minute)) {
		t.Fatalf("a secret ask of 30 minutes = %+v, %v", shorter, err)
	}
}

// A ledger that is not set up for asks says so, and never half of one.
func TestAnAskNeedsTheLedgerToBeSetUpForIt(t *testing.T) {
	service, err := New(Config{Repository: newFakeRepository(), Directory: fakeDirectory{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateAsk(context.Background(), AskInput{}); !errors.Is(err, ErrConsentUnavailable) {
		t.Errorf("CreateAsk on a ledger with no asks = %v", err)
	}
	if _, err := service.CountOpenAsks(context.Background(), "org_1", "user_a"); !errors.Is(err, ErrConsentUnavailable) {
		t.Errorf("CountOpenAsks on a ledger with no asks = %v", err)
	}
	if _, err := service.Consent(context.Background(), "t1.a.b", "user_b", SurfaceSlack); !errors.Is(err, ErrConsentUnavailable) {
		t.Errorf("Consent with no ticket key = %v", err)
	}
	if _, err := service.ConsentTickets(context.Background(), "irq_1", "ntc_1"); !errors.Is(err, ErrConsentUnavailable) {
		t.Errorf("ConsentTickets with no ticket key = %v", err)
	}
	if _, err := New(Config{Repository: newFakeRepository(), Directory: fakeDirectory{}, TicketKey: []byte("short")}); err == nil {
		t.Error("New accepted a ticket key shorter than its hash")
	}
}

// CountOpenAsks counts the asks still waiting: no winner and a request that
// is still waiting and unexpired, for the asker alone, in their organization.
func TestOpenAsksAreCountedForTheirAsker(t *testing.T) {
	ledger := newSlackLedger(t)
	count := func() int {
		t.Helper()
		open, err := ledger.service.CountOpenAsks(context.Background(), ledger.org, asker)
		if err != nil {
			t.Fatal(err)
		}
		return open
	}
	if got := count(); got != 0 {
		t.Fatalf("open asks before any = %d", got)
	}
	var asks []AskResult
	for _, trigger := range []string{"1790000001.000100", "1790000002.000100", "1790000003.000100"} {
		asks = append(asks, ledger.ask(trigger, raya))
	}
	if got := count(); got != 3 {
		t.Fatalf("open asks = %d, want 3", got)
	}
	// Approved and unanswered is still open; answered is not.
	approved := ledger.decide(raya, requestTo(t, asks[0], raya), ChoiceSummary)
	if got := count(); got != 3 {
		t.Fatalf("open asks with one approved = %d, want 3", got)
	}
	if _, err := ledger.answer(approved.Request); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != 2 {
		t.Fatalf("open asks with one answered = %d, want 2", got)
	}
	// Denied is over: nothing of it is waiting.
	ledger.decide(raya, requestTo(t, asks[1], raya), ChoiceDeny)
	if got := count(); got != 1 {
		t.Fatalf("open asks with one denied = %d, want 1", got)
	}
	// Expired is over too.
	ledger.clock.Advance(defaultExpiry + time.Minute)
	if got := count(); got != 0 {
		t.Fatalf("open asks a day later = %d, want 0", got)
	}
	// It is the asker's own count.
	if open, err := ledger.service.CountOpenAsks(context.Background(), ledger.org, raya); err != nil || open != 0 {
		t.Fatalf("another member's open asks = %d, %v", open, err)
	}
	if open, err := ledger.service.CountOpenAsks(context.Background(), "org_other", asker); err != nil || open != 0 {
		t.Fatalf("another organization's open asks = %d, %v", open, err)
	}
}

// Requests an agent made the ordinary way are not an ask's, and a listing by
// ask or by where they were made finds the right ones.
func TestListingByOriginAndAsk(t *testing.T) {
	ledger := newSlackLedger(t)
	slack := ledger.ask("1790000000.000100", raya, maya)
	other := ledger.ask("1790000001.000100", raya)
	// One an agent made in the ordinary way, filed in the store directly.
	request := storedRequest(ledger.org, ledger.org+"_ordinary", asker, raya, ledger.clock.Now())
	if _, err := ledger.store.Create(context.Background(), request, "ordinary"); err != nil {
		t.Fatal(err)
	}
	if stored := ledger.read(request.ID); stored.Origin != OriginAgent || stored.AskID != "" {
		t.Fatalf("an ordinary request is stored as %+v", stored)
	}

	asAsker := ledger.owner(asker)
	list := func(query Query) []string {
		t.Helper()
		query.Box = BoxAll
		listed, err := ledger.service.List(context.Background(), asAsker, query)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(listed))
		for _, request := range listed {
			ids = append(ids, request.ID)
		}
		slices.Sort(ids)
		return ids
	}
	sorted := func(ids ...string) []string { slices.Sort(ids); return ids }
	slackIDs := sorted(requestTo(t, slack, raya).ID, requestTo(t, slack, maya).ID, requestTo(t, other, raya).ID)
	if got := list(Query{Origin: OriginSlack}); !slices.Equal(got, slackIDs) {
		t.Errorf("origin=slack = %v, want %v", got, slackIDs)
	}
	if got := list(Query{Origin: OriginAgent}); !slices.Equal(got, []string{request.ID}) {
		t.Errorf("origin=agent = %v, want the ordinary request", got)
	}
	if got := list(Query{AskID: slack.Ask.ID}); !slices.Equal(got, sorted(requestTo(t, slack, raya).ID, requestTo(t, slack, maya).ID)) {
		t.Errorf("ask_id = %v, want the two requests of the ask", got)
	}
	if got := list(Query{AskID: slack.Ask.ID, Origin: OriginAgent}); len(got) != 0 {
		t.Errorf("ask_id of a slack ask with origin=agent = %v, want none", got)
	}
	if got := list(Query{}); len(got) != 4 {
		t.Errorf("everything = %v, want the four requests", got)
	}
	if _, err := ledger.service.List(context.Background(), asAsker, Query{Box: BoxAll, Origin: "email"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("an unknown origin = %v, want ErrInvalid", err)
	}
	// A member sees only the asks they are part of.
	if listed, err := ledger.service.List(context.Background(), ledger.owner(maya), Query{Box: BoxInbox, Origin: OriginSlack}); err != nil || len(listed) != 1 || listed[0].ID != requestTo(t, slack, maya).ID {
		t.Errorf("the other owner's slack inbox = %+v, %v", listed, err)
	}
}
