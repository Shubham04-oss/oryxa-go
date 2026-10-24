package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// planName is the tool single-agent Krude keeps its todo list with.
const planName = "plan"

// planStateKey is the session state key holding the thread's plan. An ADK
// session is a thread, so the plan is the thread's.
const planStateKey = "krude_plan"

const (
	// maxPlanSteps bounds a plan, and maxPlanStepRunes one step's text.
	maxPlanSteps     = 20
	maxPlanStepRunes = 200
)

// A step's status. A plan starts with every step pending; at most one is in
// progress at a time.
const (
	stepPending    = "pending"
	stepInProgress = "in_progress"
	stepDone       = "done"
	stepDropped    = "dropped"
)

// planStep is one step of Krude's plan.
type planStep struct {
	Step   int    `json:"step"`
	Text   string `json:"text"`
	Status string `json:"status"`
}

// planStore keeps a thread's plan. Today the session's state holds it
// (sessionPlanStore); it is an interface so that the plan can move to a
// table of agents primary's own without the tool changing.
type planStore interface {
	load(ctx agent.Context) ([]planStep, error)
	save(ctx agent.Context, steps []planStep) error
}

// sessionPlanStore keeps the plan in session state under planStateKey, as a
// JSON string, so it reads back the same from memory and from storage.
type sessionPlanStore struct{}

func (sessionPlanStore) load(ctx agent.Context) ([]planStep, error) {
	value, err := ctx.ReadonlyState().Get(planStateKey)
	if err != nil {
		// No plan yet.
		return nil, nil
	}
	text, ok := value.(string)
	if !ok || text == "" {
		return nil, nil
	}
	var steps []planStep
	if err := json.Unmarshal([]byte(text), &steps); err != nil {
		return nil, fmt.Errorf("the stored plan cannot be read: %w", err)
	}
	return steps, nil
}

func (sessionPlanStore) save(ctx agent.Context, steps []planStep) error {
	encoded, err := json.Marshal(steps)
	if err != nil {
		return err
	}
	return ctx.State().Set(planStateKey, string(encoded))
}

type planArgs struct {
	Action string   `json:"action" jsonschema:"set: replace the plan with steps, all pending (an empty list clears it). update: change one step's status. read: return the plan."`
	Steps  []string `json:"steps,omitempty" jsonschema:"For set: the steps in order, each one short line."`
	Step   int      `json:"step,omitempty" jsonschema:"For update: the step's number, counting from 1."`
	Status string   `json:"status,omitempty" jsonschema:"For update: pending, in_progress, done or dropped. Marking a step in_progress moves any other in-progress step back to pending."`
}

type planResult struct {
	Steps []planStep `json:"steps"`
	// Open counts the steps neither done nor dropped.
	Open int    `json:"open"`
	Note string `json:"note"`
}

// planTool is Krude's own todo list for the thread: it sets the steps, marks
// one in progress, done or dropped, and reads the list back. It is
// deterministic and calls no model.
func planTool(store planStore) func() (tool.Tool, error) {
	return func() (tool.Tool, error) {
		return functiontool.New(functiontool.Config{
			Name:        planName,
			Description: "Your own todo list for this conversation, for a request with several real steps. set replaces it with new steps, update marks one step in_progress, done or dropped, and read returns it. Finish or drop every step before you give your answer.",
		}, func(ctx agent.Context, args planArgs) (planResult, error) {
			return runPlan(ctx, store, args)
		})
	}
}

func runPlan(ctx agent.Context, store planStore, args planArgs) (planResult, error) {
	switch strings.TrimSpace(args.Action) {
	case "set":
		steps, err := newPlan(args.Steps)
		if err != nil {
			return planResult{}, err
		}
		if err := store.save(ctx, steps); err != nil {
			return planResult{}, err
		}
		return describePlan(steps), nil
	case "update":
		steps, err := store.load(ctx)
		if err != nil {
			return planResult{}, err
		}
		if steps, err = updatePlan(steps, args.Step, args.Status); err != nil {
			return planResult{}, err
		}
		if err := store.save(ctx, steps); err != nil {
			return planResult{}, err
		}
		return describePlan(steps), nil
	case "read":
		steps, err := store.load(ctx)
		if err != nil {
			return planResult{}, err
		}
		return describePlan(steps), nil
	}
	return planResult{}, fmt.Errorf("action %q is not set, update or read", args.Action)
}

// newPlan is the plan set makes of texts: every step pending, each text one
// line within its bound.
func newPlan(texts []string) ([]planStep, error) {
	if len(texts) > maxPlanSteps {
		return nil, fmt.Errorf("a plan has at most %d steps; this one has %d", maxPlanSteps, len(texts))
	}
	steps := make([]planStep, 0, len(texts))
	for i, raw := range texts {
		text := strings.Join(strings.Fields(raw), " ")
		if text == "" {
			return nil, fmt.Errorf("step %d is empty", i+1)
		}
		if utf8.RuneCountInString(text) > maxPlanStepRunes {
			return nil, fmt.Errorf("step %d is longer than %d characters", i+1, maxPlanStepRunes)
		}
		steps = append(steps, planStep{Step: i + 1, Text: text, Status: stepPending})
	}
	return steps, nil
}

// updatePlan sets one step's status in a copy of steps.
func updatePlan(steps []planStep, number int, status string) ([]planStep, error) {
	if len(steps) == 0 {
		return nil, errors.New("there is no plan; set one first")
	}
	if number < 1 || number > len(steps) {
		return nil, fmt.Errorf("there is no step %d; the plan has steps 1 to %d", number, len(steps))
	}
	status = strings.TrimSpace(status)
	switch status {
	case stepPending, stepInProgress, stepDone, stepDropped:
	default:
		return nil, fmt.Errorf("status %q is not pending, in_progress, done or dropped", status)
	}
	updated := append([]planStep{}, steps...)
	if status == stepInProgress {
		for i := range updated {
			if updated[i].Status == stepInProgress {
				updated[i].Status = stepPending
			}
		}
	}
	updated[number-1].Status = status
	return updated, nil
}

func describePlan(steps []planStep) planResult {
	result := planResult{Steps: append([]planStep{}, steps...)}
	for _, step := range steps {
		if step.Status != stepDone && step.Status != stepDropped {
			result.Open++
		}
	}
	switch {
	case len(steps) == 0:
		result.Note = "There is no plan."
	case result.Open == 0:
		result.Note = "Every step is done or dropped."
	default:
		result.Note = fmt.Sprintf("%d of %d steps are open. Finish or drop each before you answer.", result.Open, len(steps))
	}
	return result
}
