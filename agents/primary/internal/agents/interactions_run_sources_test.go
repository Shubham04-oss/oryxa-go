package agents

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// sourcesInteractions keeps the sources each answer arrived with, as the ledger
// reads them for the answer guard's relevance check.
type sourcesInteractions struct {
	fakeInteractions
	mu      sync.Mutex
	sources [][]string
}

func (service *sourcesInteractions) Respond(ctx context.Context, _ InteractionActor, id string, input InteractionResponseInput) (Interaction, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.sources = append(service.sources, RunSources(ctx))
	return Interaction{ID: id, Status: input.Status}, nil
}

// answering is a run delivering irq_1 for its answer, with state as its state
// and a model that answers at once, saying it used nothing.
func answering(t *testing.T, service *sourcesInteractions, state map[string]any) {
	t.Helper()
	toolbox, err := InteractionToolbox(InteractionConfig{Service: service, Delivering: true})
	if err != nil {
		t.Fatal(err)
	}
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		if hasFunctionResponse(request) {
			return genai.NewContentFromText("Done.", genai.RoleModel)
		}
		return functionCalls(&genai.FunctionCall{ID: "answer", Name: "interactions_respond", Args: map[string]any{
			"request_id": "irq_1", "status": "completed", "summary": "Nothing private was read.", "content": "Just from the request.",
			"tools_used": []any{},
		}})
	})
	deliveringHarness(t, llm, toolbox).turn("user_b", state, "Deliver irq_1.")
}

// stateWithSources is the state a run delivering irq_1 for its answer starts
// with, as the waker starts it: with the sources its reach gives it.
func stateWithSources(sources []string) map[string]any {
	state := deliveryStateFor(DeliveryWorks)
	maps.Copy(state, DeliveryState(Delivery{RequestID: "irq_1", Purpose: DeliveryWorks, Sources: sources}))
	return state
}

// TestTheRespondToolTellsTheLedgerWhatTheRunReaches: the answer reaches the
// ledger with the toolboxes the run was given, which the waker recorded when it
// started the run, whatever the model says it used.
func TestTheRespondToolTellsTheLedgerWhatTheRunReaches(t *testing.T) {
	for name, testCase := range map[string]struct {
		recorded []string
		want     []string
	}{
		"the knowledge":               {[]string{KnowledgeToolboxName}, []string{KnowledgeToolboxName}},
		"the apps and the knowledge":  {[]string{AppsToolboxName, KnowledgeToolboxName}, []string{AppsToolboxName, KnowledgeToolboxName}},
		"nothing private":             {[]string{}, []string{}},
		"nothing recorded, as before": {nil, []string{SourcesUnrecorded}},
	} {
		t.Run(name, func(t *testing.T) {
			service := &sourcesInteractions{}
			answering(t, service, stateWithSources(testCase.recorded))
			if len(service.sources) != 1 || !slices.Equal(service.sources[0], testCase.want) {
				t.Fatalf("the ledger was given %v, want %v", service.sources, testCase.want)
			}
		})
	}
}

// A run's state cannot be made to say something else by what it was started
// with in another form: only a string under the one key is a record, and
// anything else is no record at all.
func TestOnlyARecordedStringIsARecordOfWhatTheRunReaches(t *testing.T) {
	state := deliveryStateFor(DeliveryWorks)
	state[deliverySourcesKey] = []any{"knowledge"}
	service := &sourcesInteractions{}
	answering(t, service, state)
	if len(service.sources) != 1 || !slices.Equal(service.sources[0], []string{SourcesUnrecorded}) {
		t.Fatalf("the ledger was given %v, want %v", service.sources, []string{SourcesUnrecorded})
	}
}

// DeliveryState leaves the sources out when none were recorded, as it left out
// everything it does not know, and writes them as one string otherwise.
func TestDeliveryStateRecordsSourcesOnlyWhenThereAreSome(t *testing.T) {
	bare := DeliveryState(Delivery{RequestID: "irq_1", Purpose: DeliveryWorks})
	if _, recorded := bare[deliverySourcesKey]; recorded || len(bare) != 2 {
		t.Fatalf("state = %v, want the request and its purpose alone", bare)
	}
	if got := DeliveryState(Delivery{RequestID: "irq_1", Purpose: DeliveryWorks, Sources: []string{}})[deliverySourcesKey]; got != "" {
		t.Fatalf("state records %v for no sources, want the empty string", got)
	}
	if got := DeliveryState(Delivery{RequestID: "irq_1", Purpose: DeliveryWorks, Sources: []string{"apps", "knowledge"}})[deliverySourcesKey]; got != "apps,knowledge" {
		t.Fatalf("state records %v, want apps,knowledge", got)
	}
}

// Sources are not something an answer says: the response has no field for them,
// so neither the model nor an API caller can leave a tool out.
func TestTheResponseHasNoFieldForItsSources(t *testing.T) {
	toolbox, err := InteractionToolbox(InteractionConfig{Service: &fakeInteractions{}, Delivering: true})
	if err != nil {
		t.Fatal(err)
	}
	llm := newTreeModel(func(string, *model.LLMRequest) *genai.Content {
		return genai.NewContentFromText("Read it.", genai.RoleModel)
	})
	deliveringHarness(t, llm, toolbox).turn("user_b", stateWithSources([]string{"knowledge"}), "Deliver irq_1.")
	for _, declaration := range llm.requestsFor(RootName)[0].Config.Tools {
		for _, function := range declaration.FunctionDeclarations {
			if function.Name != "interactions_respond" {
				continue
			}
			encoded, err := json.Marshal(function)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), "summary") {
				t.Fatalf("the declaration says nothing of an answer's summary, so it was not read as the schema: %s", encoded)
			}
			for _, field := range []string{"sources", "Sources", "run_sources", "delivery_sources"} {
				if strings.Contains(string(encoded), `"`+field+`"`) {
					t.Fatalf("interactions_respond has a %q parameter: %s", field, encoded)
				}
			}
			return
		}
	}
	t.Fatal("the run was not offered interactions_respond")
}

func TestRunSourcesTravelInTheContextAndAreCopied(t *testing.T) {
	if got := RunSources(context.Background()); got != nil {
		t.Fatalf("no sources = %v, want none", got)
	}
	given := []string{"a", "b"}
	ctx := WithRunSources(context.Background(), given)
	given[0] = "changed"
	got := RunSources(ctx)
	if !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("sources = %v", got)
	}
	got[1] = "changed"
	if again := RunSources(ctx); !slices.Equal(again, []string{"a", "b"}) {
		t.Fatalf("reading the sources changed them: %v", again)
	}
}
