package threads

import (
	"context"
	"slices"
	"sync"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
)

type subagentScopeKey struct{}

// fakeSubagents records what runs and threads ask of the sub-agents.
type fakeSubagents struct {
	mu        sync.Mutex
	runs      *fakeRuns
	begun     []string
	ended     []string
	finished  []bool
	forgotten []string
}

func (f *fakeSubagents) Begin(ctx context.Context, runID string) (context.Context, func()) {
	f.mu.Lock()
	f.begun = append(f.begun, runID)
	f.mu.Unlock()
	var once sync.Once
	return context.WithValue(ctx, subagentScopeKey{}, runID), func() {
		once.Do(func() {
			f.runs.mu.Lock()
			_, finished := f.runs.finished[runID]
			f.runs.mu.Unlock()
			f.mu.Lock()
			defer f.mu.Unlock()
			f.ended = append(f.ended, runID)
			f.finished = append(f.finished, finished)
		})
	}
}

func (f *fakeSubagents) ForgetThread(_ context.Context, organizationID, userID, app, threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgotten = append(f.forgotten, "thread "+organizationID+"/"+userID+"/"+app+"/"+threadID)
	return nil
}

func (f *fakeSubagents) Forget(_ context.Context, organizationID, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgotten = append(f.forgotten, "member "+organizationID+"/"+userID)
	return nil
}

// TestARunScopesItsSubAgents: each run opens its scope on the sub-agents
// and ends it once, as soon as the agent is done and before the run is
// priced and recorded, so a sub-agent never outlives the run and what it
// spent is on the run's usage.
func TestARunScopesItsSubAgents(t *testing.T) {
	h := newHarness(t, func(*model.LLMRequest) (*genai.Content, error) {
		return genai.NewContentFromText("Hello.", genai.RoleModel), nil
	})
	fake := &fakeSubagents{runs: h.runs}
	h.service.subagents = fake
	thread := newThread(t, h.service, asha)
	_, run := h.run(asha, thread, RunInput{Message: "hi"})
	if !slices.Equal(fake.begun, []string{run.ID()}) || !slices.Equal(fake.ended, []string{run.ID()}) {
		t.Fatalf("begun = %v, ended = %v, want the run once each", fake.begun, fake.ended)
	}
	if fake.finished[0] {
		t.Fatal("the run's sub-agents were ended after it was recorded and priced")
	}
}

// TestAFailedRunEndsItsSubAgentsToo: a run that fails ends its scope as one
// that succeeds does.
func TestAFailedRunEndsItsSubAgentsToo(t *testing.T) {
	h := newHarness(t, func(*model.LLMRequest) (*genai.Content, error) {
		return nil, context.DeadlineExceeded
	})
	fake := &fakeSubagents{runs: h.runs}
	h.service.subagents = fake
	thread := newThread(t, h.service, asha)
	_, run := h.run(asha, thread, RunInput{Message: "hi"})
	if !slices.Equal(fake.ended, []string{run.ID()}) || fake.finished[0] {
		t.Fatalf("ended = %v before finishing = %v", fake.ended, fake.finished)
	}
}

// TestDeletingAndForgettingTakeSubAgentsAlong: a deleted thread's
// sub-agents go with it, and forgetting a member forgets theirs.
func TestDeletingAndForgettingTakeSubAgentsAlong(t *testing.T) {
	h := newHarness(t, func(*model.LLMRequest) (*genai.Content, error) {
		return genai.NewContentFromText("Hello.", genai.RoleModel), nil
	})
	fake := &fakeSubagents{runs: h.runs}
	h.service.subagents = fake
	thread := newThread(t, h.service, asha)
	if err := h.service.Delete(context.Background(), asha, thread); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Forget(context.Background(), "org_1", ""); err != nil {
		t.Fatal(err)
	}
	want := []string{"thread org_1/user_asha/krude/" + thread, "member org_1/"}
	if !slices.Equal(fake.forgotten, want) {
		t.Fatalf("forgotten = %v, want %v", fake.forgotten, want)
	}
}
