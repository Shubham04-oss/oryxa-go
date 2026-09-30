package interactions

import (
	"context"
	"errors"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

func TestRequestKeepsItsOriginatingConversationOnReplay(t *testing.T) {
	service, repo := testService(t)
	actor := requester
	actor.ThreadID = "thread_origin"
	actor.ThreadAppName = agents.RootName
	input := agents.InteractionCreateInput{TargetAgentID: "agt_engineering", Purpose: "Question", Task: "When is launch?"}
	created, err := service.Create(context.Background(), actor, "token", "same-call", input)
	if err != nil || created.RequesterThreadID != actor.ThreadID {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	if created.RequesterThreadAppName != agents.RootName {
		t.Fatalf("origin namespace=%q", created.RequesterThreadAppName)
	}
	replayed, err := service.Create(context.Background(), actor, "token", "same-call", input)
	if err != nil || replayed.RequesterThreadID != "thread_origin" || replayed.ID != created.ID {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
	actor.ThreadID = "thread_other"
	if _, err := service.Create(context.Background(), actor, "token", "same-call", input); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("replay from another conversation error=%v", err)
	}
	stored, err := repo.Get(context.Background(), created.ID)
	if err != nil || stored.RequesterThreadID != "thread_origin" {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	// A target's reads never expose the asking member's private chat pointer.
	view, err := service.Get(context.Background(), targetPrimary, created.ID)
	if err != nil || view.RequesterThreadID != "" {
		t.Fatalf("target view=%+v err=%v", view, err)
	}
}

func TestRequestFiledOutsideARunHasNoOriginatingConversation(t *testing.T) {
	service, _ := testService(t)
	created, err := service.Create(context.Background(), requester, "token", "manual-call", agents.InteractionCreateInput{
		TargetAgentID: "agt_engineering", Purpose: "Question", Task: "When is launch?",
	})
	if err != nil || created.RequesterThreadID != "" {
		t.Fatalf("created=%+v err=%v", created, err)
	}
}
