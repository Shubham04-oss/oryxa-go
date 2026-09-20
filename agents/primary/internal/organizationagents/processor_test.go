package organizationagents

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/teamspaces"
)

type fakeDeliveryStore struct {
	previous Synthesis
	result   Result
	applied  bool
	dropped  string
	setAside string
	released string
}

func (*fakeDeliveryStore) Claim(context.Context, time.Duration, int) ([]Delivery, error) {
	return nil, nil
}

func (store *fakeDeliveryStore) Synthesis(_ context.Context, _ string) (Synthesis, error) {
	return store.previous, nil
}

func (store *fakeDeliveryStore) Apply(_ context.Context, _ Delivery, _ string, result Result) (Synthesis, error) {
	store.applied = true
	store.result = result
	return store.previous, nil
}

func (store *fakeDeliveryStore) Release(_ context.Context, _ string, _ bool, _ int, _ time.Duration, reason string) (bool, error) {
	store.released = reason
	return false, nil
}

func (store *fakeDeliveryStore) Drop(_ context.Context, _ string, reason string) error {
	store.dropped = reason
	return nil
}

func (store *fakeDeliveryStore) SetAside(_ context.Context, _ string, reason string) error {
	store.setAside = reason
	return nil
}

func (*fakeDeliveryStore) Purge(context.Context, time.Duration) error { return nil }

type fakeSharedSources struct {
	definition teamspaces.OrganizationAgent
	space      teamspaces.Space
	entry      teamspaces.Entry
	entryErr   error
	spaceErr   error
	agentErr   error
}

func (source *fakeSharedSources) GetOrganizationAgent(context.Context, string) (teamspaces.OrganizationAgent, error) {
	return source.definition, source.agentErr
}

func (source *fakeSharedSources) Get(context.Context, string, string) (teamspaces.Space, error) {
	return source.space, source.spaceErr
}

func (source *fakeSharedSources) GetEntry(context.Context, string, string) (teamspaces.Entry, error) {
	return source.entry, source.entryErr
}

type fakeSynthesizer struct {
	result   Result
	err      error
	called   bool
	previous Synthesis
}

func (runtime *fakeSynthesizer) Synthesize(_ context.Context, _ teamspaces.OrganizationAgent, previous Synthesis, _ teamspaces.Space, _ teamspaces.Entry) (Result, error) {
	runtime.called = true
	runtime.previous = previous
	return runtime.result, runtime.err
}

func synthesisFixture() (Delivery, *fakeSharedSources) {
	publishedAt := time.Unix(100, 0).UTC()
	delivery := Delivery{
		EventID: "evt_1", EntryID: "tse_1", TeamspaceID: "tsp_1", OrganizationID: "org_1",
		AuthorUserID: "user_1", AuthorAgentID: "agt_author", Kind: "decision", PublishedAt: publishedAt,
	}
	sources := &fakeSharedSources{
		definition: teamspaces.OrganizationAgent{ID: "oag_1", OrganizationID: "org_1", Status: teamspaces.StatusActive},
		space: teamspaces.Space{
			ID: "tsp_1", OrganizationID: "org_1", Status: teamspaces.StatusActive,
			Contributions: []teamspaces.Contribution{
				{AgentID: "agt_specialist", Status: managedagents.StatusActive},
				{AgentID: "agt_disabled", Status: managedagents.StatusDisabled},
			},
		},
		entry: teamspaces.Entry{
			ID: "tse_1", TeamspaceID: "tsp_1", OrganizationID: "org_1",
			AuthorUserID: "user_1", AuthorAgentID: "agt_author", Kind: teamspaces.EntryDecision,
			CreatedAt: publishedAt,
		},
	}
	return delivery, sources
}

func TestProcessorSynthesizesAuthoritativeEntryAndBoundsFollowUps(t *testing.T) {
	delivery, sources := synthesisFixture()
	store := &fakeDeliveryStore{previous: Synthesis{OrganizationID: "org_1", Revision: 4, Summary: "Earlier context"}}
	runtime := &fakeSynthesizer{result: Result{
		Summary:       "  Current context  ",
		Decisions:     []string{" ship ", "ship", ""},
		Risks:         []string{" capacity "},
		OpenQuestions: nil,
		FollowUps: []FollowUpResult{
			{TargetAgentID: "agt_specialist", Task: " check capacity ", Rationale: " owns service "},
			{TargetAgentID: "agt_outsider", Task: " ask privately ", Rationale: " maybe knows "},
		},
	}}
	processor, err := NewProcessor(store, sources, runtime, ProcessorConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	processor.process(context.Background(), delivery)

	if !runtime.called || runtime.previous.Revision != 4 || !store.applied || store.released != "" || store.dropped != "" {
		t.Fatalf("runtime called=%v previous=%+v applied=%v released=%q dropped=%q", runtime.called, runtime.previous, store.applied, store.released, store.dropped)
	}
	if store.result.Summary != "Current context" || len(store.result.Decisions) != 1 || store.result.Decisions[0] != "ship" {
		t.Fatalf("cleaned result=%+v", store.result)
	}
	if len(store.result.FollowUps) != 2 || store.result.FollowUps[0].TargetAgentID != "agt_specialist" || store.result.FollowUps[1].TargetAgentID != "" {
		t.Fatalf("bounded follow-ups=%+v", store.result.FollowUps)
	}
	if store.result.FollowUps[1].Task != "ask privately" {
		t.Fatalf("follow-up task=%q", store.result.FollowUps[1].Task)
	}
}

func TestProcessorDropsStaleOrDisabledSourcesWithoutCallingModel(t *testing.T) {
	tests := []struct {
		name   string
		change func(Delivery, *fakeSharedSources) (Delivery, *fakeSharedSources)
		want   string
	}{
		{
			name: "missing entry",
			change: func(delivery Delivery, sources *fakeSharedSources) (Delivery, *fakeSharedSources) {
				sources.entryErr = teamspaces.ErrNotFound
				return delivery, sources
			},
			want: "entry no longer exists",
		},
		{
			name: "routing mismatch",
			change: func(delivery Delivery, sources *fakeSharedSources) (Delivery, *fakeSharedSources) {
				delivery.Kind = "result"
				return delivery, sources
			},
			want: "does not match",
		},
		{
			name: "disabled organization agent",
			change: func(delivery Delivery, sources *fakeSharedSources) (Delivery, *fakeSharedSources) {
				sources.definition.Status = teamspaces.StatusDisabled
				return delivery, sources
			},
			want: ErrDisabled.Error(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			delivery, sources := synthesisFixture()
			delivery, sources = test.change(delivery, sources)
			store := &fakeDeliveryStore{}
			runtime := &fakeSynthesizer{}
			processor, err := NewProcessor(store, sources, runtime, ProcessorConfig{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			processor.process(context.Background(), delivery)
			if runtime.called || store.applied || !strings.Contains(store.dropped, test.want) || store.released != "" {
				t.Fatalf("called=%v applied=%v dropped=%q released=%q", runtime.called, store.applied, store.dropped, store.released)
			}
		})
	}
}

func TestProcessorRetriesModelAndValidationFailures(t *testing.T) {
	tests := []struct {
		name    string
		runtime *fakeSynthesizer
		want    string
	}{
		{name: "model", runtime: &fakeSynthesizer{err: errors.New("model unavailable")}, want: "model unavailable"},
		{name: "invalid result", runtime: &fakeSynthesizer{result: Result{Summary: "ok", FollowUps: []FollowUpResult{{Task: "", Rationale: "why"}}}}, want: "every follow-up"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			delivery, sources := synthesisFixture()
			store := &fakeDeliveryStore{}
			processor, err := NewProcessor(store, sources, test.runtime, ProcessorConfig{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			processor.process(context.Background(), delivery)
			if !test.runtime.called || store.applied || store.dropped != "" || !strings.Contains(store.released, test.want) {
				t.Fatalf("called=%v applied=%v dropped=%q released=%q", test.runtime.called, store.applied, store.dropped, store.released)
			}
		})
	}
}

func TestNewProcessorRequiresEveryBoundary(t *testing.T) {
	_, sources := synthesisFixture()
	store := &fakeDeliveryStore{}
	runtime := &fakeSynthesizer{}
	for _, input := range []struct {
		store   deliveryStore
		sources sharedSources
		runtime synthesizer
	}{{nil, sources, runtime}, {store, nil, runtime}, {store, sources, nil}} {
		if _, err := NewProcessor(input.store, input.sources, input.runtime, ProcessorConfig{}, nil); err == nil {
			t.Fatal("missing processor boundary was accepted")
		}
	}
}

// TestProcessorLeavesChatterOutOfTheSharedContextWithoutCallingModel: a
// teamspace fills with people talking. Only what the organization would want
// to read costs a synthesis run; the rest is recorded as read and set aside,
// which is neither a failure nor a retry.
func TestProcessorLeavesChatterOutOfTheSharedContextWithoutCallingModel(t *testing.T) {
	delivery, sources := synthesisFixture()
	delivery.Kind = "update"
	sources.entry.Kind = teamspaces.EntryUpdate
	sources.entry.Summary = "Thanks, nice work everyone."
	store := &fakeDeliveryStore{}
	runtime := &fakeSynthesizer{result: Result{Summary: "unused"}}
	processor, err := NewProcessor(store, sources, runtime, ProcessorConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	processor.process(context.Background(), delivery)
	if runtime.called {
		t.Error("chatter cost a synthesis run")
	}
	if store.applied || store.dropped != "" || store.released != "" {
		t.Errorf("applied=%v dropped=%q released=%q: chatter is set aside, not applied, dropped or retried", store.applied, store.dropped, store.released)
	}
	if store.setAside == "" {
		t.Error("nothing recorded about why the publication was set aside")
	}

	// The same teamspace's decisions still advance it.
	delivery, sources = synthesisFixture()
	store, runtime = &fakeDeliveryStore{}, &fakeSynthesizer{result: Result{Summary: "Northwind chosen."}}
	processor, err = NewProcessor(store, sources, runtime, ProcessorConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	processor.process(context.Background(), delivery)
	if !runtime.called || !store.applied || store.setAside != "" {
		t.Errorf("called=%v applied=%v setAside=%q: a decision must still advance the synthesis", runtime.called, store.applied, store.setAside)
	}
}
