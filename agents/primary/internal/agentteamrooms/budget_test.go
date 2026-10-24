package agentteamrooms

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentteams"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

// deadlineStore refuses a write on a context that is already done, as
// Postgres does.
type deadlineStore struct {
	*memoryStore
	failAgentLines bool
}

func (store *deadlineStore) Append(ctx context.Context, scope agentteams.Scope, message Message) (Message, error) {
	if err := ctx.Err(); err != nil {
		return Message{}, err
	}
	if store.failAgentLines && message.Kind == "agent" {
		return Message{}, errors.New("database unavailable")
	}
	return store.memoryStore.Append(ctx, scope, message)
}

// slowAgents runs the named agents until their context ends, then reports
// the run failed, as an engine does when its run is cut short.
type slowAgents struct {
	*fakeAgents
	slow map[string]bool
}

func (agents *slowAgents) StartUnattendedRun(ctx context.Context, member threads.Member, id, threadID string, input threads.RunInput, profile managedagents.UnattendedProfile) (managedagents.StartedRun, error) {
	started, err := agents.fakeAgents.StartUnattendedRun(ctx, member, id, threadID, input, profile)
	if err != nil || !agents.slow[id] {
		return started, err
	}
	started.Run = blockingRun{}
	return started, nil
}

type blockingRun struct{}

func (blockingRun) ID() string                      { return "run" }
func (blockingRun) Discard(context.Context, string) {}
func (blockingRun) Execute(ctx context.Context, emit func(threads.Item) error) {
	<-ctx.Done()
	_ = emit(threads.Item{Type: threads.ItemRunFailed, Error: ctx.Err().Error()})
}

func budgetFixture(t *testing.T, slow ...string) (*Service, *deadlineStore, *slowAgents) {
	t.Helper()
	_, memory, registry, fake := fixture(t)
	store := &deadlineStore{memoryStore: memory}
	agents := &slowAgents{fakeAgents: fake, slow: map[string]bool{}}
	for _, id := range slow {
		agents.slow[id] = true
	}
	service, err := New(store, registry, agents, nil)
	if err != nil {
		t.Fatal(err)
	}
	return service, store, agents
}

// TestARoomWhoseBudgetRanOutStillStoresItsLastLine: the coordinator's turn
// outlives the post's budget. The "could not answer" line is still stored,
// and Post reports what it stored rather than failing after the member's
// line was already kept.
func TestARoomWhoseBudgetRanOutStillStoresItsLastLine(t *testing.T) {
	service, store, _ := budgetFixture(t, "primary")
	service.roomTime, service.turnTime = 50*time.Millisecond, time.Minute
	said, err := service.Post(context.Background(), threads.Member{OrganizationID: "org", UserID: "user"}, "team", "Plan the launch")
	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}
	if len(said) != 2 || len(store.messages) != 2 || !strings.Contains(said[1].Body, "could not answer") {
		t.Fatalf("said=%+v stored=%+v", said, store.messages)
	}
}

// TestOneSlowTurnLeavesItsTeammatesTime: a teammate's turn is bounded on its
// own, so the next teammate still answers inside the post's budget.
func TestOneSlowTurnLeavesItsTeammatesTime(t *testing.T) {
	service, store, agents := budgetFixture(t, "research")
	service.roomTime, service.turnTime = 5*time.Second, 50*time.Millisecond
	agents.outputs["writing"] = `{"status":"completed","summary":"Here is the copy.","handoffs":[]}`
	said, err := service.Post(context.Background(), threads.Member{OrganizationID: "org", UserID: "user"}, "team", "@research-specialist and @writing-specialist, please help")
	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}
	if len(said) != 3 || !strings.Contains(said[1].Body, "could not answer") || said[2].Body != "Here is the copy." || len(store.messages) != 3 {
		t.Fatalf("said=%+v", said)
	}
}

// TestALineThatCannotBeStoredEndsThePostWithoutAnError: the member's line
// is kept, so Post returns it rather than an error the client would retry.
func TestALineThatCannotBeStoredEndsThePostWithoutAnError(t *testing.T) {
	service, store, _ := budgetFixture(t)
	store.failAgentLines = true
	said, err := service.Post(context.Background(), threads.Member{OrganizationID: "org", UserID: "user"}, "team", "hello")
	if err != nil || len(said) != 1 || said[0].Kind != "member" {
		t.Fatalf("said=%+v error=%v", said, err)
	}
}

// TestAnInactiveTeammateIsNotOfferedForAHandoff: the prompt lists only the
// teammates a handoff can reach.
func TestAnInactiveTeammateIsNotOfferedForAHandoff(t *testing.T) {
	service, _, registry, agents := fixture(t)
	registry.team.Members[1].Status = managedagents.StatusDisabled
	if _, err := service.Post(context.Background(), threads.Member{OrganizationID: "org", UserID: "user"}, "team", "hello"); err != nil {
		t.Fatal(err)
	}
	prompt := agents.calls[0].prompt
	if !strings.Contains(prompt, "@research-specialist") || strings.Contains(prompt, "@writing-specialist") {
		t.Fatalf("prompt teammates:\n%s", prompt)
	}
}
