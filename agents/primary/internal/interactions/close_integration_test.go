package interactions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// approvedRequest stores a request and approves it, as B's agent would.
func approvedRequest(t *testing.T, store *Store, organizationID, id string, at time.Time) agents.Interaction {
	t.Helper()
	ctx := context.Background()
	if _, err := store.Create(ctx, storedRequest(organizationID, id, "user_a", "user_b", at), id); err != nil {
		t.Fatal(err)
	}
	approved, err := store.Decide(ctx, id, agents.InteractionDecisionInput{Decision: DecisionApproved, Message: "Go ahead."},
		agents.InteractionDecider{Kind: DecidedByAgent, UserID: "user_b", AgentID: "agt_user_b"}, at.Add(time.Minute))
	if err != nil || approved.Status != StatusApproved {
		t.Fatalf("approve %s = %+v, %v", id, approved, err)
	}
	return approved
}

// completedEvents counts the completed events a request has in the outbox,
// and says the status the newest carries.
func completedEvents(t *testing.T, database *postgreskit.Database, id string) (int, string) {
	t.Helper()
	var count int
	var status string
	err := database.Pool.QueryRow(context.Background(), `SELECT count(*), coalesce(max(detail->>'status'), '')
		FROM agents_primary.agents_outbox WHERE detail_type=$1 AND detail->>'request_id'=$2`,
		agentsv1.InteractionCompletedDetailType, id).Scan(&count, &status)
	if err != nil {
		t.Fatal(err)
	}
	return count, status
}

// TestCloseApprovedFailsOnlyAnApprovedRequestEvenPastItsExpiry: approved
// work that will not be done is failed, as Oryxa and saying why, with the
// completed event that tells the member who asked, whether or not it has
// expired; a pending request, and one already answered, are left as they
// are, with no event.
func TestCloseApprovedFailsOnlyAnApprovedRequestEvenPastItsExpiry(t *testing.T) {
	database, store := openInteractionStore(t)
	organizationID := testOrganization(t, database)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	approved := approvedRequest(t, store, organizationID, organizationID+"_approved", now)
	pastItsExpiry := approved.ExpiresAt.Add(time.Minute)
	reason := "Oryxa closed this request because the approved work could not be done after 3 attempts."
	closed, err := store.CloseApproved(ctx, approved.ID, reason, pastItsExpiry)
	if err != nil || closed.Status != StatusFailed || closed.Result == nil || closed.Result.Summary != reason ||
		closed.Revision != approved.Revision+1 || closed.Decision == nil || closed.Decision.DecidedBy == nil {
		t.Fatalf("closed = %+v, %v; want it failed saying why, its approval kept", closed, err)
	}
	if count, status := completedEvents(t, database, approved.ID); count != 1 || status != StatusFailed {
		t.Fatalf("%d completed events, the newest %q; want one, failed", count, status)
	}
	if _, err := store.CloseApproved(ctx, approved.ID, reason, pastItsExpiry); !errors.Is(err, ErrConflict) {
		t.Fatalf("closing it again: error = %v, want ErrConflict", err)
	}

	pending, err := store.Create(ctx, storedRequest(organizationID, organizationID+"_pending", "user_a", "user_b", now), "pending")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CloseApproved(ctx, pending.ID, reason, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("closing a pending request: error = %v, want ErrConflict", err)
	}
	if stored, err := store.Get(ctx, pending.ID); err != nil || stored.Status != StatusPending || stored.Revision != 1 {
		t.Fatalf("the pending request = %+v, %v; want it untouched", stored, err)
	}
	if _, err := store.CloseApproved(ctx, organizationID+"_missing", reason, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("closing a missing request: error = %v, want ErrNotFound", err)
	}
	if count, _ := completedEvents(t, database, approved.ID); count != 1 {
		t.Fatalf("%d completed events after the refusals, want still one", count)
	}
}

// TestCloseExpiredClosesOnlyApprovalsThatRanOutLately: an approved request
// whose expiry passed unanswered within the window is failed, saying so,
// with its completed event. One that ran out before the window, such as
// every approval that expired before this build, is left exactly as it was:
// still read as expired, with no event and its updated_at untouched. One
// still in time, and a pending one that ran out, are left too. The requests
// here expire in 2001, so a closing at 2001's times reaches no one else's.
func TestCloseExpiredClosesOnlyApprovalsThatRanOutLately(t *testing.T) {
	database, store := openInteractionStore(t)
	organizationID := testOrganization(t, database)
	ctx := context.Background()
	made := time.Date(2001, time.January, 1, 9, 0, 0, 0, time.UTC)

	ranOut := approvedRequest(t, store, organizationID, organizationID+"_ran_out", made)
	longAgo := approvedRequest(t, store, organizationID, organizationID+"_long_ago", made.Add(-48*time.Hour))
	inTime := approvedRequest(t, store, organizationID, organizationID+"_in_time", made.Add(3*time.Hour))
	pending, err := store.Create(ctx, storedRequest(organizationID, organizationID+"_pending", "user_a", "user_b", made), "pending")
	if err != nil {
		t.Fatal(err)
	}
	// ranOut and pending expired at 10:00, longAgo two days before, and
	// inTime expires at 13:00; the window is the hour before 10:30.
	at := made.Add(90 * time.Minute)
	since := at.Add(-time.Hour)
	reason := "Oryxa closed this request because it expired before the approved work was done."
	closed, err := store.CloseExpired(ctx, reason, since, at, 100)
	if err != nil || len(closed) != 1 || closed[0].ID != ranOut.ID || closed[0].Status != StatusFailed ||
		closed[0].Result == nil || closed[0].Result.Summary != reason {
		t.Fatalf("closed = %+v, %v; want only the approval that ran out within the window, failed saying so", closed, err)
	}
	if count, status := completedEvents(t, database, ranOut.ID); count != 1 || status != StatusFailed {
		t.Fatalf("%d completed events, the newest %q; want one, failed", count, status)
	}
	for _, left := range []agents.Interaction{longAgo, inTime, pending} {
		stored, err := store.Get(ctx, left.ID)
		if err != nil || stored.Status != left.Status || stored.Revision != left.Revision || !stored.UpdatedAt.Equal(left.UpdatedAt) {
			t.Fatalf("%s = %+v, %v; want it left %s, as it was", left.ID, stored, err, left.Status)
		}
		if count, _ := completedEvents(t, database, left.ID); count != 0 {
			t.Fatalf("%s has %d completed events, want none", left.ID, count)
		}
	}
	if again, err := store.CloseExpired(ctx, reason, since, at, 100); err != nil || len(again) != 0 {
		t.Fatalf("closing again = %+v, %v; want nothing left to close", again, err)
	}
}
