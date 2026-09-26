package interactions

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

// heldInTeamspaceAnswer is A's request of B's primary agent in a teamspace,
// approved by B in person and answered by B's agent with what the guard held.
func heldInTeamspaceAnswer(t *testing.T) (*Service, *fakeRepository, *fakeTeamspaces, *tokenDirectory, string) {
	t.Helper()
	spaces := teamspaceSeats()
	directory := &tokenDirectory{fakeDirectory: fakeDirectory{directory: teammateDirectory}}
	verdict := heldVerdict
	repo := newFakeRepository()
	service, err := New(Config{
		Repository: repo, Directory: directory, Teamspaces: spaces, Tokens: &fakeTokens{}, Guard: &fakeGuard{verdict: &verdict},
		Now: func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	created := createInTeamspace(t, service, "agt_b")
	if _, err := service.DecideAsOwner(context.Background(), inOwnSession(targetPrimary), "member-token", created.ID, approve); err != nil {
		t.Fatal(err)
	}
	if held, err := service.Respond(context.Background(), targetPrimary, created.ID, answerGiven); err != nil || held.Status != StatusReview {
		t.Fatalf("held=%+v err=%v", held, err)
	}
	directory.tokens = nil
	return service, repo, spaces, directory, created.ID
}

// TestSendingAHeldAnswerReadsItsTeamspaceAgain: before the member sends the
// answer to a request made in a teamspace, the teamspace is read again, as
// them, with their own token, as approving it was. One that no longer stands
// there has its answer withheld by Oryxa, saying why, and the member is told
// it was closed; withholding needs nothing to remain; and sending needs the
// member's token.
func TestSendingAHeldAnswerReadsItsTeamspaceAgain(t *testing.T) {
	service, repo, _, directory, id := heldInTeamspaceAnswer(t)
	sent, err := service.ReviewAsOwner(context.Background(), inOwnSession(targetPrimary), "member-token", id, agents.InteractionReviewInput{Action: ReviewSend})
	if err != nil || sent.Status != StatusCompleted {
		t.Fatalf("sending while it stands = %+v, %v", sent, err)
	}
	if !slices.Equal(directory.tokens, []string{"member-token"}) {
		t.Fatalf("the teamspace was read with %v, want the member's own token once", directory.tokens)
	}

	lapsed := "the member who asked is no longer on the teamspace's team"
	for _, action := range []string{ReviewSend, ReviewSendMasked} {
		service, repo, spaces, _, id := heldInTeamspaceAnswer(t)
		spaces.lapsed = lapsed
		_, err := service.ReviewAsOwner(context.Background(), inOwnSession(targetPrimary), "member-token", id, agents.InteractionReviewInput{Action: action})
		if !errors.Is(err, ErrClosed) || !strings.Contains(err.Error(), lapsed) {
			t.Fatalf("%s once it lapsed: error=%v, want ErrClosed saying why", action, err)
		}
		withheld := repo.requests[id]
		if withheld.Status != StatusWithheld || withheld.Result != nil || withheld.Review == nil || withheld.Review.Action != ReviewWithhold ||
			withheld.Review.DecidedBy.Kind != DecidedBySystem || !strings.Contains(withheld.Review.Reason, lapsed) {
			t.Fatalf("%s once it lapsed left %+v with review %+v, want it withheld by Oryxa saying why", action, withheld, withheld.Review)
		}
	}

	service, repo, spaces, _, id := heldInTeamspaceAnswer(t)
	spaces.lapsed = lapsed
	if withheld, err := service.ReviewAsOwner(context.Background(), inOwnSession(targetPrimary), "member-token", id, agents.InteractionReviewInput{Action: ReviewWithhold}); err != nil ||
		withheld.Status != StatusWithheld || withheld.Review.DecidedBy.Kind != DecidedByMember {
		t.Fatalf("withholding once it lapsed = %+v, %v; want it withheld by the member", withheld, err)
	}

	service, repo, _, _, id = heldInTeamspaceAnswer(t)
	if _, err := service.ReviewAsOwner(context.Background(), inOwnSession(targetPrimary), " ", id, agents.InteractionReviewInput{Action: ReviewSend}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("sending without a token: error=%v, want ErrInvalid", err)
	}
	if repo.requests[id].Status != StatusReview {
		t.Fatalf("a refused send left the request %s", repo.requests[id].Status)
	}
}
