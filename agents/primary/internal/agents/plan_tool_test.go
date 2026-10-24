package agents

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
)

// scriptedPlanRun runs single-agent Krude through the real llmagent flow in
// one thread, answering with script's model turns in order and then with
// "Done.", and returns every plan response by call id, and the stored plan.
func scriptedPlanRun(t *testing.T, script ...[]*genai.FunctionCall) (map[string]string, []planStep) {
	t.Helper()
	var mu sync.Mutex
	responses := map[string]string{}
	next := 0
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		mu.Lock()
		defer mu.Unlock()
		for _, content := range request.Contents {
			for _, part := range content.Parts {
				if part.FunctionResponse != nil && part.FunctionResponse.Name == planName {
					encoded, _ := json.Marshal(part.FunctionResponse.Response)
					responses[part.FunctionResponse.ID] = string(encoded)
				}
			}
		}
		if next < len(script) {
			next++
			return functionCalls(script[next-1]...)
		}
		return genai.NewContentFromText("Done.", genai.RoleModel)
	})
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatal(err)
	}
	root, err := SingleAgentRoot(router, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	run := newRootRun(t, root)
	thread := run.newSession(nil)
	if answer := run.turn(thread, "Do the three things."); answer != "Done." {
		t.Fatalf("answer = %q", answer)
	}
	var steps []planStep
	if stored, ok := run.state(thread, planStateKey).(string); ok {
		if err := json.Unmarshal([]byte(stored), &steps); err != nil {
			t.Fatal(err)
		}
	}
	return responses, steps
}

func planUpdates(pairs ...any) []any {
	var updates []any
	for i := 0; i+1 < len(pairs); i += 2 {
		updates = append(updates, map[string]any{"step": pairs[i], "status": pairs[i+1]})
	}
	return updates
}

var threeSteps = []any{"one", "two", "three"}

// TestTwoPlanCallsInOneResponseLoseNothing: ADK runs the calls of one model
// response concurrently, each over the same committed state, and merges
// their state deltas last write wins, so two plan calls in one response
// would each save the whole plan and one change would vanish. The second
// is refused instead, saying so, and the stored plan is exactly the first
// call's: nothing is lost without the model being told.
func TestTwoPlanCallsInOneResponseLoseNothing(t *testing.T) {
	for range 20 {
		responses, steps := scriptedPlanRun(t,
			[]*genai.FunctionCall{{ID: "set", Name: planName, Args: map[string]any{"action": "set", "steps": threeSteps}}},
			[]*genai.FunctionCall{
				{ID: "first", Name: planName, Args: map[string]any{"action": "update", "updates": planUpdates(1, "done")}},
				{ID: "second", Name: planName, Args: map[string]any{"action": "update", "updates": planUpdates(2, "in_progress")}},
			},
		)
		if !strings.Contains(responses["second"], "second plan call in the same step") {
			t.Fatalf("the second plan call answered %s, want it refused", responses["second"])
		}
		if strings.Contains(responses["first"], "error") {
			t.Fatalf("the first plan call answered %s", responses["first"])
		}
		want := []planStep{{1, "one", stepDone}, {2, "two", stepPending}, {3, "three", stepPending}}
		if !slices.Equal(steps, want) {
			t.Fatalf("stored plan = %+v, want the first call's %+v", steps, want)
		}
	}
}

// TestASetAndAnUpdateInOneResponse: set and update in one response is the
// same race, and the update is refused rather than lost.
func TestASetAndAnUpdateInOneResponse(t *testing.T) {
	responses, steps := scriptedPlanRun(t, []*genai.FunctionCall{
		{ID: "set", Name: planName, Args: map[string]any{"action": "set", "steps": threeSteps}},
		{ID: "update", Name: planName, Args: map[string]any{"action": "update", "updates": planUpdates(1, "in_progress")}},
	})
	if !strings.Contains(responses["update"], "second plan call in the same step") {
		t.Fatalf("the update answered %s, want it refused", responses["update"])
	}
	if len(steps) != 3 || steps[0].Status != stepPending {
		t.Fatalf("stored plan = %+v, want the set's", steps)
	}
}

// TestOnePlanCallCarriesEveryChange: a set applies its updates, and one
// update applies several, in order, so one call per step is enough; plan
// calls in successive responses each apply.
func TestOnePlanCallCarriesEveryChange(t *testing.T) {
	responses, steps := scriptedPlanRun(t,
		[]*genai.FunctionCall{{ID: "set", Name: planName, Args: map[string]any{"action": "set", "steps": threeSteps, "updates": planUpdates(1, "in_progress")}}},
		[]*genai.FunctionCall{{ID: "update", Name: planName, Args: map[string]any{"action": "update", "updates": planUpdates(1, "done", 2, "in_progress", 3, "dropped")}}},
	)
	for id, response := range responses {
		if strings.Contains(response, "error") {
			t.Fatalf("plan call %s answered %s", id, response)
		}
	}
	if !strings.Contains(responses["set"], `"status":"in_progress","step":1`) {
		t.Fatalf("set with updates answered %s", responses["set"])
	}
	want := []planStep{{1, "one", stepDone}, {2, "two", stepInProgress}, {3, "three", stepDropped}}
	if !slices.Equal(steps, want) {
		t.Fatalf("stored plan = %+v, want %+v", steps, want)
	}
}

// TestAPlanCallThatFailsChangesNothing: updates apply all or none, and an
// update with none is refused.
func TestAPlanCallThatFailsChangesNothing(t *testing.T) {
	responses, steps := scriptedPlanRun(t,
		[]*genai.FunctionCall{{ID: "set", Name: planName, Args: map[string]any{"action": "set", "steps": threeSteps}}},
		[]*genai.FunctionCall{{ID: "bad", Name: planName, Args: map[string]any{"action": "update", "updates": planUpdates(1, "done", 9, "done")}}},
		[]*genai.FunctionCall{{ID: "empty", Name: planName, Args: map[string]any{"action": "update"}}},
	)
	if !strings.Contains(responses["bad"], "there is no step 9") || !strings.Contains(responses["empty"], "at least one") {
		t.Fatalf("bad update = %s, empty update = %s", responses["bad"], responses["empty"])
	}
	if len(steps) != 3 || steps[0].Status != stepPending {
		t.Fatalf("stored plan = %+v, want it unchanged by the failed update", steps)
	}
}

// TestPlanCallsForgetsWhatItRefused: a refusal is for one call id, once,
// and a response with one plan call, or none, marks nothing.
func TestPlanCallsForgetsWhatItRefused(t *testing.T) {
	calls := newPlanCalls()
	response := &model.LLMResponse{Content: functionCalls(
		&genai.FunctionCall{ID: "a", Name: planName},
		&genai.FunctionCall{ID: "x", Name: "list_tools"},
		&genai.FunctionCall{ID: "b", Name: planName},
	)}
	if got, err := calls.afterModel(nil, response, nil); got != nil || err != nil {
		t.Fatalf("afterModel changed the response: %v, %v", got, err)
	}
	if calls.refused("a") || calls.refused("x") || !calls.refused("b") || calls.refused("b") || calls.refused("") {
		t.Fatal("refused the wrong calls")
	}
	calls.afterModel(nil, &model.LLMResponse{Content: functionCalls(&genai.FunctionCall{ID: "c", Name: planName})}, nil)
	calls.afterModel(nil, &model.LLMResponse{Partial: true, Content: response.Content}, nil)
	if len(calls.pending) != 0 {
		t.Fatalf("pending = %v, want nothing marked", calls.pending)
	}
}
