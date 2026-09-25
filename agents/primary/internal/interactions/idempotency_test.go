package interactions

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/teamspaces"
)

// TestARetryGetsItsRequestBackWithoutItsSeatCheckedAgain: a retry under the
// same key is answered from the ledger before anything is looked up, so it
// succeeds with the request it made even once the seat it was made from is
// gone — which used to refuse the retry of a request that exists.
func TestARetryGetsItsRequestBackWithoutItsSeatCheckedAgain(t *testing.T) {
	spaces := teamspaceSeats()
	service, repo := teamspaceService(t, spaces)
	key := "side-sdc-0123456789abcdef01234567"
	first, err := service.Create(context.Background(), requester, "member-token", key, askInTeamspace("agt_b"))
	if err != nil {
		t.Fatal(err)
	}
	spaces.err = fmt.Errorf("%w: only a current member of this teamspace's team can ask its agents for help there", teamspaces.ErrForbidden)
	again, err := service.Create(context.Background(), requester, "member-token", key, askInTeamspace("agt_b"))
	if err != nil || again.ID != first.ID || again.Approval != ApprovalOwner || again.TeamspaceID != "tsp_1" {
		t.Fatalf("the retry = %+v, %v; want %s back", again, err, first.ID)
	}
	if len(spaces.calls) != 1 || repo.creates != 1 || len(repo.requests) != 1 {
		t.Fatalf("seat lookups=%d creates=%d stored=%d, want the retry to reach neither", len(spaces.calls), repo.creates, len(repo.requests))
	}
}

// TestAKeyReusedForAnotherRequestIsAConflict: a key names one request. Sent
// again with another target, teamspace or task, it is refused rather than
// answered with the first request as if it were the new one.
func TestAKeyReusedForAnotherRequestIsAConflict(t *testing.T) {
	service, repo := teamspaceService(t, teamspaceSeats())
	key := "side-sdc-aaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := service.Create(context.Background(), requester, "member-token", key, askInTeamspace("agt_b")); err != nil {
		t.Fatal(err)
	}
	outside := askInTeamspace("agt_b")
	outside.TeamspaceID = ""
	otherTask := askInTeamspace("agt_b")
	otherTask.Task = "A different task."
	otherPurpose := askInTeamspace("agt_b")
	otherPurpose.Purpose = "Another purpose"
	for name, input := range map[string]agents.InteractionCreateInput{
		"another target":  askInTeamspace("agt_auto"),
		"no teamspace":    outside,
		"another task":    otherTask,
		"another purpose": otherPurpose,
		"another teamspace": func() agents.InteractionCreateInput {
			input := askInTeamspace("agt_b")
			input.TeamspaceID = "tsp_2"
			return input
		}(),
	} {
		reused, err := service.Create(context.Background(), requester, "member-token", key, input)
		if !errors.Is(err, ErrIdempotencyConflict) || reused.ID != "" {
			t.Errorf("%s under a used key = %+v, %v; want ErrIdempotencyConflict", name, reused, err)
		}
	}
	if len(repo.requests) != 1 {
		t.Fatalf("stored %d requests, want only the first", len(repo.requests))
	}
}

// racingRepository is a ledger where another create under the same key lands
// between the service's look-up and its own insert: the look-up finds
// nothing, and the insert conflicts and returns what the other one stored.
type racingRepository struct{ *fakeRepository }

func (racingRepository) FindByKey(context.Context, string, string, string) (agents.Interaction, error) {
	return agents.Interaction{}, ErrNotFound
}

// TestAKeyReusedConcurrentlyIsStillAConflict: the insert's own replay is
// compared too, so a race cannot hand back another request either.
func TestAKeyReusedConcurrentlyIsStillAConflict(t *testing.T) {
	repo := racingRepository{newFakeRepository()}
	service, err := New(Config{Repository: repo, Directory: fakeDirectory{directory: teammateDirectory}, Teamspaces: teamspaceSeats(), Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Create(context.Background(), requester, "member-token", "raced", askInTeamspace("agt_b"))
	if err != nil {
		t.Fatal(err)
	}
	if same, err := service.Create(context.Background(), requester, "member-token", "raced", askInTeamspace("agt_b")); err != nil || same.ID != first.ID {
		t.Fatalf("the same request, raced = %+v, %v; want %s", same, err, first.ID)
	}
	if other, err := service.Create(context.Background(), requester, "member-token", "raced", askInTeamspace("agt_ask")); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("another request, raced = %+v, %v; want ErrIdempotencyConflict", other, err)
	}
}
