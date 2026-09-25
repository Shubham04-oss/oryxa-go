package interactions

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// heldAnswer is B's agent's answer as a guard holds it: an email address and
// a phone number in it.
func heldAnswer() Answer {
	return Answer{
		Held: &agents.InteractionHeld{
			Result: agents.InteractionResult{Status: StatusCompleted, Summary: "Reach Priya at priya@example.com.", Content: "Call +1 415 555 0100.",
				ToolsUsed: []string{}, Artifacts: []string{}},
			MaskedResult: &agents.InteractionResult{Status: StatusCompleted, Summary: "Reach Priya at {EMAIL}.", Content: "Call {PHONE}.",
				ToolsUsed: []string{}, Artifacts: []string{}},
		},
		Guard: &agents.InteractionGuard{State: "held", Kinds: []string{"EMAIL", "PHONE"}},
	}
}

// eventFields are every field an InteractionChanged event may carry.
var eventFields = []string{
	"request_id", "organization_id", "requester_user_id", "requester_agent_id", "target_user_id", "target_agent_id",
	"status", "revision", "changed_at", "teamspace_id", "approval", "decided_by",
}

// eventsCarryNothingOfTheAnswer fails when any event the outbox holds for a
// request carries a field InteractionChanged does not have, or any of texts.
func eventsCarryNothingOfTheAnswer(t *testing.T, database *postgreskit.Database, requestID string, texts ...string) {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(), `SELECT detail::text, (SELECT array_agg(key) FROM jsonb_object_keys(detail) AS key)
		FROM agents_primary.agents_outbox WHERE detail->>'request_id'=$1`, requestID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var detail string
		var keys []string
		if err := rows.Scan(&detail, &keys); err != nil {
			t.Fatal(err)
		}
		for _, key := range keys {
			if !slices.Contains(eventFields, key) {
				t.Errorf("an event carries %q: %s", key, detail)
			}
		}
		for _, text := range texts {
			if strings.Contains(detail, text) {
				t.Errorf("an event carries %q: %s", text, detail)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// TestTheLedgerHoldsAnAnswerForReviewAndReleasesItAsReviewed: a held answer
// leaves its request in review with no result, keeping both versions and
// what the guard found; the member's review delivers one version, or none,
// drops what was held, and records the review; each step is a completed
// event carrying the new status and nothing of the answer or of what the
// guard found.
func TestTheLedgerHoldsAnAnswerForReviewAndReleasesItAsReviewed(t *testing.T) {
	database, store := openInteractionStore(t)
	ctx := context.Background()
	organizationID := testOrganization(t, database)
	now := time.Now().UTC().Truncate(time.Microsecond)
	member := agents.InteractionDecider{Kind: DecidedByMember, UserID: "user_b"}
	for action, want := range map[string]struct{ status, summary string }{
		ReviewSend:       {StatusCompleted, "Reach Priya at priya@example.com."},
		ReviewSendMasked: {StatusCompleted, "Reach Priya at {EMAIL}."},
		ReviewWithhold:   {StatusWithheld, ""},
	} {
		approved := approvedRequest(t, store, organizationID, organizationID+"_"+action, now)
		answeredAt := now.Add(2 * time.Minute)
		held, err := store.Respond(ctx, approved.ID, heldAnswer(), answeredAt)
		if err != nil || held.Status != StatusReview || held.Result != nil || held.Held == nil || held.Held.MaskedResult == nil ||
			!held.Held.Result.CompletedAt.Equal(answeredAt) || held.Guard == nil || !slices.Equal(held.Guard.Kinds, []string{"EMAIL", "PHONE"}) {
			t.Fatalf("%s: held=%+v err=%v, want in review holding both versions", action, held, err)
		}
		if read, err := store.Get(ctx, approved.ID); err != nil || read.Held == nil || read.Held.MaskedResult.Content != "Call {PHONE}." || read.Guard.State != "held" {
			t.Fatalf("%s: read back %+v, %v", action, read, err)
		}

		reviewedAt := now.Add(3 * time.Minute)
		reviewed, err := store.Review(ctx, approved.ID, action, member, reviewedAt)
		if err != nil || reviewed.Status != want.status || reviewed.Held != nil || reviewed.Review == nil ||
			reviewed.Review.Action != action || reviewed.Review.DecidedBy != member || !reviewed.Review.DecidedAt.Equal(reviewedAt) {
			t.Fatalf("%s: reviewed=%+v err=%v, want %s, the review recorded", action, reviewed, err, want.status)
		}
		if want.summary == "" && reviewed.Result != nil || want.summary != "" && (reviewed.Result == nil || reviewed.Result.Summary != want.summary) {
			t.Fatalf("%s: result=%+v, want summary %q", action, reviewed.Result, want.summary)
		}
		var heldGone bool
		if err := database.Pool.QueryRow(ctx, `SELECT held_result IS NULL FROM agents_primary.agent_interactions WHERE id=$1`, approved.ID).Scan(&heldGone); err != nil || !heldGone {
			t.Fatalf("%s: the held answer is still stored (%v)", action, err)
		}
		if events := eventsOf(t, database, approved.ID); !slices.Equal(events, []string{
			requestedEvent, approvedEvent,
			agentsv1.InteractionCompletedDetailType + ":" + StatusReview, agentsv1.InteractionCompletedDetailType + ":" + want.status,
		}) {
			t.Fatalf("%s: events=%v", action, events)
		}
		eventsCarryNothingOfTheAnswer(t, database, approved.ID, "priya", "415 555", "{EMAIL}", "EMAIL", "PHONE", "Reach Priya", "kinds")
		if _, err := store.Review(ctx, approved.ID, ReviewSend, member, reviewedAt); !errors.Is(err, ErrConflict) {
			t.Fatalf("%s, reviewed again: error=%v, want ErrConflict", action, err)
		}
	}
}

// TestTheLedgerDeliversAnAnswerAsTheGuardLeftIt: a masked answer completes
// its request with the masked result and what the guard found, and an
// answer no guard read completes it as given, with no guard.
func TestTheLedgerDeliversAnAnswerAsTheGuardLeftIt(t *testing.T) {
	database, store := openInteractionStore(t)
	ctx := context.Background()
	organizationID := testOrganization(t, database)
	now := time.Now().UTC().Truncate(time.Microsecond)

	masked := approvedRequest(t, store, organizationID, organizationID+"_masked", now)
	answered, err := store.Respond(ctx, masked.ID, Answer{
		Result: &agents.InteractionResult{Status: StatusCompleted, Summary: "The PIN is {PIN}.", ToolsUsed: []string{}, Artifacts: []string{}},
		Guard:  &agents.InteractionGuard{State: "masked", Kinds: []string{"PIN"}},
	}, now.Add(time.Minute))
	if err != nil || answered.Status != StatusCompleted || answered.Result.Summary != "The PIN is {PIN}." || answered.Held != nil ||
		answered.Guard == nil || answered.Guard.State != "masked" || !slices.Equal(answered.Guard.Kinds, []string{"PIN"}) {
		t.Fatalf("masked=%+v err=%v", answered, err)
	}
	unguarded := approvedRequest(t, store, organizationID, organizationID+"_unguarded", now)
	answered, err = store.Respond(ctx, unguarded.ID, Unguarded(agents.InteractionResponseInput{Status: StatusFailed, Summary: "Could not."}), now.Add(time.Minute))
	if err != nil || answered.Status != StatusFailed || answered.Result == nil || answered.Guard != nil || answered.Held != nil {
		t.Fatalf("unguarded=%+v err=%v", answered, err)
	}
	both := heldAnswer()
	both.Result = both.Held.MaskedResult
	neither := Answer{Guard: &agents.InteractionGuard{State: "clear"}}
	for name, answer := range map[string]Answer{"delivered and held": both, "neither": neither} {
		request := approvedRequest(t, store, organizationID, organizationID+"_"+strings.ReplaceAll(name, " ", "_"), now)
		if _, err := store.Respond(ctx, request.ID, answer, now.Add(time.Minute)); !errors.Is(err, ErrInvalid) {
			t.Errorf("an answer %s: error=%v, want ErrInvalid", name, err)
		}
	}
}

// TestTheLedgerWithholdsAnAnswerWhoseRequestExpiredInReview: Oryxa withholds
// an answer still in review once its request expires, as the system and
// saying why, with the completed event; one still in time is left for its
// member; and nothing that ends approved work touches a request in review.
// The requests expire in 2001, so a sweep at 2001's times reaches no one
// else's.
func TestTheLedgerWithholdsAnAnswerWhoseRequestExpiredInReview(t *testing.T) {
	database, store := openInteractionStore(t)
	organizationID := testOrganization(t, database)
	ctx := context.Background()
	made := time.Date(2001, time.January, 1, 9, 0, 0, 0, time.UTC)

	ranOut := approvedRequest(t, store, organizationID, organizationID+"_ran_out", made)
	inTime := approvedRequest(t, store, organizationID, organizationID+"_in_time", made.Add(3*time.Hour))
	stillApproved := approvedRequest(t, store, organizationID, organizationID+"_approved", made)
	for _, request := range []agents.Interaction{ranOut, inTime} {
		if _, err := store.Respond(ctx, request.ID, heldAnswer(), made.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	// ranOut and stillApproved expired at 10:00, and inTime expires at 13:00.
	at := made.Add(90 * time.Minute)

	closed, err := store.CloseExpired(ctx, "expired", at.Add(-time.Hour), at, 100)
	if err != nil || len(closed) != 1 || closed[0].ID != stillApproved.ID {
		t.Fatalf("closing expired approvals = %+v, %v; want the approval alone, never a request in review", closed, err)
	}
	if _, err := store.CloseApproved(ctx, inTime.ID, "given up", at); !errors.Is(err, ErrConflict) {
		t.Fatalf("giving up on a request in review: error=%v, want ErrConflict", err)
	}

	withheld, err := store.WithholdExpired(ctx, ExpiredBeforeReview, at, 100)
	if err != nil || len(withheld) != 1 || withheld[0].ID != ranOut.ID {
		t.Fatalf("withheld = %+v, %v; want the answer whose request expired", withheld, err)
	}
	got := withheld[0]
	if got.Status != StatusWithheld || got.Result != nil || got.Held != nil || got.Review == nil || got.Review.Action != ReviewWithhold ||
		got.Review.DecidedBy.Kind != DecidedBySystem || got.Review.Reason != "expired before review" || !got.Review.DecidedAt.Equal(at) {
		t.Fatalf("withheld=%+v review=%+v, want withheld by Oryxa, expired before review", got, got.Review)
	}
	if count, status := completedEvents(t, database, ranOut.ID); count != 2 || status != StatusWithheld {
		t.Fatalf("%d completed events, the newest %q; want review then withheld", count, status)
	}
	eventsCarryNothingOfTheAnswer(t, database, ranOut.ID, "priya", "expired before review", "EMAIL")
	if left, err := store.Get(ctx, inTime.ID); err != nil || left.Status != StatusReview || left.Held == nil {
		t.Fatalf("the request still in time = %+v, %v; want it in review", left, err)
	}
	if again, err := store.WithholdExpired(ctx, ExpiredBeforeReview, at, 100); err != nil || len(again) != 0 {
		t.Fatalf("withholding again = %+v, %v; want nothing left", again, err)
	}
	if _, err := store.Review(ctx, inTime.ID, ReviewSend, agents.InteractionDecider{Kind: DecidedByMember, UserID: "user_b"}, made.Add(4*time.Hour)); !errors.Is(err, ErrExpired) {
		t.Fatalf("reviewing past the request's expiry: error=%v, want ErrExpired", err)
	}
}

// TestTheLedgerKeepsAHeldAnswerOnlyInReview: the database itself refuses a
// held answer outside review, a result beside one, a withheld request with
// no review, a status or a guard state nobody knows.
func TestTheLedgerKeepsAHeldAnswerOnlyInReview(t *testing.T) {
	database, store := openInteractionStore(t)
	ctx := context.Background()
	organizationID := testOrganization(t, database)
	now := time.Now().UTC().Truncate(time.Microsecond)
	completed := approvedRequest(t, store, organizationID, organizationID+"_completed", now)
	if _, err := store.Respond(ctx, completed.ID, Unguarded(agents.InteractionResponseInput{Status: StatusCompleted, Summary: "Done."}), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	inReview := approvedRequest(t, store, organizationID, organizationID+"_review", now)
	if _, err := store.Respond(ctx, inReview.ID, heldAnswer(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	for name, update := range map[string]struct{ statement, id string }{
		"a held answer outside review":  {`UPDATE agents_primary.agent_interactions SET held_result='{}' WHERE id=$1`, completed.ID},
		"a result beside a held answer": {`UPDATE agents_primary.agent_interactions SET result='{"status":"completed"}' WHERE id=$1`, inReview.ID},
		"withheld with no review":       {`UPDATE agents_primary.agent_interactions SET status='withheld', held_result=NULL, result=NULL WHERE id=$1`, inReview.ID},
		"a status nobody knows":         {`UPDATE agents_primary.agent_interactions SET status='archived' WHERE id=$1`, completed.ID},
		"a guard state nobody knows":    {`UPDATE agents_primary.agent_interactions SET guard='{"state":"fine"}' WHERE id=$1`, completed.ID},
		"a review nobody knows":         {`UPDATE agents_primary.agent_interactions SET review='{"action":"forward"}' WHERE id=$1`, completed.ID},
	} {
		if _, err := database.Pool.Exec(ctx, update.statement, update.id); err == nil {
			t.Errorf("the database stored %s", name)
		}
	}
}

// TestTheAnswerGuardMigrationReplacesTheStatusCheck: once migrated, 001's
// status check is gone, its replacement and the answer guard's checks are
// validated, and the review expiry index is built.
func TestTheAnswerGuardMigrationReplacesTheStatusCheck(t *testing.T) {
	database, _ := openInteractionStore(t)
	ctx := context.Background()
	var old, validated, index int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conname='agent_interactions_status_check' AND conrelid='agents_primary.agent_interactions'::regclass`).Scan(&old); err != nil {
		t.Fatal(err)
	}
	checks := []string{"agent_interactions_status_values_check", "agent_interactions_held_check", "agent_interactions_review_check", "agent_interactions_guard_check"}
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conname = ANY($1) AND conrelid='agents_primary.agent_interactions'::regclass AND convalidated`, checks).Scan(&validated); err != nil {
		t.Fatal(err)
	}
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes
		WHERE schemaname='agents_primary' AND indexname='agent_interactions_review_expiry_idx'`).Scan(&index); err != nil {
		t.Fatal(err)
	}
	if old != 0 || validated != len(checks) || index != 1 {
		t.Fatalf("old status checks=%d validated checks=%d review indexes=%d, want 0, %d and 1", old, validated, len(checks), index)
	}
	var fields []string
	for _, field := range []string{"guard", "held_result", "review"} {
		var nullable string
		if err := database.Pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns
			WHERE table_schema='agents_primary' AND table_name='agent_interactions' AND column_name=$1`, field).Scan(&nullable); err != nil {
			t.Fatalf("%s: %v", field, err)
		}
		fields = append(fields, fmt.Sprintf("%s nullable=%s", field, nullable))
	}
	if !slices.Equal(fields, []string{"guard nullable=YES", "held_result nullable=YES", "review nullable=YES"}) {
		t.Fatalf("columns=%v", fields)
	}
}
