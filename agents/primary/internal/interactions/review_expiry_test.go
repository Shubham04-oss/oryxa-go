package interactions

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

// TestAnAnswerLeftInReviewPastItsExpiryReadsAsWithheld: once its request
// has expired, an answer still in review reads, to either member, as the
// withholding the next sweep records, whether or not a sweep runs; and it is
// no longer the member's to send.
func TestAnAnswerLeftInReviewPastItsExpiryReadsAsWithheld(t *testing.T) {
	verdict := heldVerdict
	service, repo, approved := guardedService(t, &fakeGuard{verdict: &verdict}, nil)
	if _, err := service.Respond(context.Background(), target, approved.ID, answerGiven); err != nil {
		t.Fatal(err)
	}
	expired := repo.requests[approved.ID]
	expired.ExpiresAt = testNow.Add(-time.Minute)
	repo.requests[approved.ID] = expired

	for name, reader := range map[string]agents.InteractionActor{"the member who asked": requester, "the member asked": inOwnSession(targetPrimary)} {
		read, err := service.Get(context.Background(), reader, approved.ID)
		if err != nil {
			t.Fatal(err)
		}
		if read.Status != StatusWithheld || read.Result != nil || read.Held != nil || read.Review == nil ||
			read.Review.Reason != ExpiredBeforeReview || read.Review.DecidedBy.Kind != DecidedBySystem || !read.Review.DecidedAt.Equal(expired.ExpiresAt) {
			t.Fatalf("%s reads %+v with review %+v, want it withheld, expired before review", name, read, read.Review)
		}
	}
	for _, action := range []string{ReviewSend, ReviewSendMasked, ReviewWithhold} {
		if _, err := service.ReviewAsOwner(context.Background(), inOwnSession(targetPrimary), "member-token", approved.ID, agents.InteractionReviewInput{Action: action}); !errors.Is(err, ErrExpired) {
			t.Fatalf("%s past its expiry: error=%v, want ErrExpired", action, err)
		}
	}
	if repo.reviews != 0 {
		t.Fatalf("%d reviews recorded past the expiry", repo.reviews)
	}
}

// sweptReviews records every sweep it is asked for.
type sweptReviews struct {
	mu     sync.Mutex
	sweeps []string
}

func (reviews *sweptReviews) WithholdExpired(_ context.Context, reason string, _ time.Time, limit int) ([]agents.Interaction, error) {
	reviews.mu.Lock()
	defer reviews.mu.Unlock()
	reviews.sweeps = append(reviews.sweeps, reason)
	if limit <= 0 {
		return nil, errors.New("no limit")
	}
	return []agents.Interaction{{ID: "irq_1"}}, nil
}

func (reviews *sweptReviews) count() int {
	reviews.mu.Lock()
	defer reviews.mu.Unlock()
	return len(reviews.sweeps)
}

// TestExpiredReviewsAreSweptWithoutTheQueue: where no events queue runs the
// interaction waker, answers left in review past their request's expiry are
// still withheld, on every interval, until the process stops.
func TestExpiredReviewsAreSweptWithoutTheQueue(t *testing.T) {
	reviews := &sweptReviews{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		SweepExpiredReviews(ctx, reviews, 5*time.Millisecond, nil)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for reviews.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the sweep did not stop with its context")
	}
	if reviews.count() < 2 {
		t.Fatalf("swept %d times, want one every interval", reviews.count())
	}
	for _, reason := range reviews.sweeps {
		if reason != ExpiredBeforeReview {
			t.Fatalf("swept with the reason %q, want %q", reason, ExpiredBeforeReview)
		}
	}
}
