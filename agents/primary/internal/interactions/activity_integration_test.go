package interactions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
)

// kindsOf is the kind of each entry, newest first.
func kindsOf(entries []ActivityEntry) []string {
	kinds := make([]string, len(entries))
	for index, entry := range entries {
		kinds[index] = entry.Kind + "/" + entry.Role
	}
	return kinds
}

// TestActivityIsWhatHappenedToAMembersSlackAsks: an ask made and received, how
// each request was decided and where, the answer, a lease given and revoked and
// a secret opened, newest first, from each member's own side, and never a word of
// what was asked or answered.
func TestActivityIsWhatHappenedToAMembersSlackAsks(t *testing.T) {
	ledger := newSlackLedger(t)
	ctx := context.Background()
	start := ledger.clock.Now()

	result := ledger.ask("1790000000.000100", raya, maya)
	forRaya := requestTo(t, result, raya)
	ledger.clock.Advance(time.Minute)
	approved := ledger.decide(raya, forRaya, ChoiceAccess)
	ledger.clock.Advance(time.Minute)
	if _, err := ledger.answer(approved.Request); err != nil {
		t.Fatal(err)
	}
	ledger.clock.Advance(time.Minute)
	tx, err := ledger.database.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordSecretViewed(ctx, tx, forRaya.ID, asker, ledger.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	ledger.clock.Advance(time.Minute)
	if _, err := ledger.service.RevokeLease(ctx, ledger.owner(raya), approved.Lease.ID); err != nil {
		t.Fatal(err)
	}

	// Raya, who was asked, gave a lease and shared a secret.
	entries, err := ledger.service.Activity(ctx, ledger.owner(raya), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(kindsOf(entries), " "); got != "lease_revoked/owner secret_viewed/owner answered/owner decided/owner lease_given/owner asked/owner" &&
		got != "lease_revoked/owner secret_viewed/owner answered/owner lease_given/owner decided/owner asked/owner" {
		t.Fatalf("Raya's activity = %s", got)
	}
	byKind := map[string]ActivityEntry{}
	for _, entry := range entries {
		byKind[entry.Kind] = entry
		if entry.WithUserID != asker || entry.Role != ActivityOwner {
			t.Errorf("entry %+v is not with the asker, as owner", entry)
		}
	}
	decided := byKind[ActivityDecided]
	if decided.RequestID != forRaya.ID || decided.AskID != result.Ask.ID || decided.Decision != DecisionApproved || decided.Consent != ConsentAccess ||
		decided.DecidedBy == nil || *decided.DecidedBy != (agents.InteractionDecider{Kind: DecidedByMember, UserID: raya, Surface: SurfaceWeb}) ||
		!decided.At.Equal(start.Add(time.Minute)) {
		t.Errorf("decided = %+v, decided by %+v", decided, decided.DecidedBy)
	}
	if got := byKind[ActivityAnswered]; !got.At.Equal(start.Add(2*time.Minute)) || got.Status != StatusCompleted {
		t.Errorf("answered = %+v", got)
	}
	if got := byKind[ActivitySecretViewed]; !got.At.Equal(start.Add(3 * time.Minute)) {
		t.Errorf("secret viewed = %+v", got)
	}
	if got := byKind[ActivityLeaseRevoked]; got.Lease == nil || got.Lease.ID != approved.Lease.ID || got.Lease.RevokedAt == nil || !got.At.Equal(start.Add(4*time.Minute)) {
		t.Errorf("lease revoked = %+v", got)
	}
	if got := byKind[ActivityLeaseGiven]; got.Lease == nil || got.Lease.ID != approved.Lease.ID || got.RequestID != forRaya.ID {
		t.Errorf("lease given = %+v", got)
	}

	// The asker made both requests, and holds the lease.
	asAsker, err := ledger.service.Activity(ctx, ledger.owner(asker), 0)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, entry := range asAsker {
		counts[entry.Kind+"/"+entry.Role]++
	}
	want := map[string]int{
		"asked/asker": 2, "decided/asker": 2, "answered/asker": 1, "secret_viewed/asker": 1,
		"lease_given/asker": 1, "lease_revoked/asker": 1,
	}
	for kind, n := range want {
		if counts[kind] != n {
			t.Errorf("the asker's activity has %d %s, want %d: %v", counts[kind], kind, n, kindsOf(asAsker))
		}
	}
	if len(asAsker) != 8 {
		t.Errorf("the asker's activity = %v, want 8 entries", kindsOf(asAsker))
	}
	for index := 1; index < len(asAsker); index++ {
		if asAsker[index].At.After(asAsker[index-1].At) {
			t.Fatalf("entry %d is newer than the one before it: %v", index, kindsOf(asAsker))
		}
	}

	// Maya was asked and Raya answered first: her request was closed.
	asMaya, err := ledger.service.Activity(ctx, ledger.owner(maya), 0)
	if err != nil || len(asMaya) != 2 || asMaya[0].Kind != ActivityDecided || asMaya[0].Decision != DecisionRejected ||
		asMaya[0].DecidedBy == nil || asMaya[0].DecidedBy.Kind != DecidedBySystem || asMaya[0].DecidedBy.Reason != ReasonAnsweredElsewhere || asMaya[1].Kind != ActivityAsked {
		t.Fatalf("Maya's activity = %+v, %v", asMaya, err)
	}

	// A limit keeps the newest.
	newest, err := ledger.service.Activity(ctx, ledger.owner(raya), 2)
	if err != nil || len(newest) != 2 || newest[0].Kind != ActivityLeaseRevoked || newest[1].Kind != ActivitySecretViewed {
		t.Fatalf("the newest two = %+v, %v", newest, err)
	}

	// It is names and states, not a question or an answer.
	encoded, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"dev keys", "vault", "Where do"} {
		if strings.Contains(string(encoded), leaked) {
			t.Errorf("activity carries %q: %s", leaked, encoded)
		}
	}
	// Somebody with no part in any of it sees nothing.
	if none, err := ledger.service.Activity(ctx, ledger.owner("user_stranger"), 0); err != nil || len(none) != 0 {
		t.Fatalf("a stranger's activity = %+v, %v", none, err)
	}
	if _, err := ledger.service.Activity(ctx, agents.InteractionActor{}, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("activity of nobody: %v", err)
	}
}

// The asker reads an answer that was held only as a request in review: no
// answered entry, until it is sent.
func TestActivityDoesNotShowTheAskerAHeldAnswerAsAnswered(t *testing.T) {
	ledger := newSlackLedger(t)
	ctx := context.Background()
	request := ledger.decide(raya, requestTo(t, ledger.ask("1790000000.000100", raya), raya), ChoiceSummary).Request
	ledger.service.policy = fakeWorkspacePolicy{settings: policy.Settings{HoldForReview: true}}
	if held, err := ledger.answer(request); err != nil || held.Status != StatusReview {
		t.Fatalf("the answer = %+v, %v", held, err)
	}
	asAsker, err := ledger.service.Activity(ctx, ledger.owner(asker), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range asAsker {
		if entry.Kind == ActivityAnswered {
			t.Fatalf("the asker's activity shows the held answer as answered: %+v", entry)
		}
	}
}

// A request whose ledger is not set up for leases still has an activity; it just
// has none of them.
func TestActivityNeedsNoLeases(t *testing.T) {
	service, err := New(Config{Repository: newFakeRepository(), Directory: fakeDirectory{}})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := service.Activity(context.Background(), requester, 0)
	if err != nil || entries == nil || len(entries) != 0 {
		t.Fatalf("Activity = %+v, %v; want an empty list", entries, err)
	}
}
