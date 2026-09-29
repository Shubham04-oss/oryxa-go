package organizationagents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/teamspaces"
)

type fakeDeliveryStore struct {
	previous     Synthesis
	synthesisErr error
	applyErr     error
	result       Result
	applied      bool
	dropped      string
	setAside     string
	released     string
	// releases is how many times a delivery was released, and releasedFailed
	// whether the last of them counted an attempt against it.
	releases       int
	releasedFailed bool
	// releaseContext is what the context the last release was made under said.
	releaseContext error
}

func (*fakeDeliveryStore) Claim(context.Context, time.Duration, int) ([]Delivery, error) {
	return nil, nil
}

func (store *fakeDeliveryStore) Synthesis(_ context.Context, _ string) (Synthesis, error) {
	return store.previous, store.synthesisErr
}

func (store *fakeDeliveryStore) Apply(_ context.Context, _ Delivery, _ string, result Result) (Synthesis, error) {
	if store.applyErr != nil {
		return Synthesis{}, store.applyErr
	}
	store.applied = true
	store.result = result
	return store.previous, nil
}

func (store *fakeDeliveryStore) Release(ctx context.Context, _ string, failed bool, _ int, _ time.Duration, reason string) (bool, error) {
	store.released = reason
	store.releases++
	store.releasedFailed = failed
	store.releaseContext = ctx.Err()
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
			if !store.releasedFailed {
				t.Fatal("a synthesis that failed on its own was released without counting an attempt")
			}
		})
	}
}

// erringRelevance is a Relevance that cannot say.
type erringRelevance struct{ failure error }

func (relevance erringRelevance) OrganizationRelevant(context.Context, teamspaces.Entry) (bool, string, error) {
	return false, "", relevance.failure
}

// A deploy stops the processor while a synthesis runs. The run was cut short,
// and reports it as any run does, with an error of its own: it was not the
// synthesis that failed. Its delivery is released to be tried again by whoever
// runs next, and the attempt does not count toward dropping it, or three
// deploys in a row would drop a publication nothing was wrong with. Whatever
// fails while the processor goes on is what it always was: a failure, counted.
func TestProcessorRetriesASynthesisCutShortByShutdownWithoutUsingAnAttempt(t *testing.T) {
	type breakage func(*fakeSharedSources, *fakeDeliveryStore, *Processor)
	tests := []struct {
		name   string
		break_ breakage
	}{
		{"reading the entry", func(sources *fakeSharedSources, _ *fakeDeliveryStore, _ *Processor) {
			sources.entryErr = errors.New("database unavailable")
		}},
		{"reading the teamspace", func(sources *fakeSharedSources, _ *fakeDeliveryStore, _ *Processor) {
			sources.spaceErr = errors.New("database unavailable")
		}},
		{"reading the organization agent", func(sources *fakeSharedSources, _ *fakeDeliveryStore, _ *Processor) {
			sources.agentErr = errors.New("database unavailable")
		}},
		{"judging whether it is relevant", func(_ *fakeSharedSources, _ *fakeDeliveryStore, processor *Processor) {
			processor.relevance = erringRelevance{errors.New("Jev unreachable")}
		}},
		{"reading the earlier synthesis", func(_ *fakeSharedSources, store *fakeDeliveryStore, _ *Processor) {
			store.synthesisErr = errors.New("database unavailable")
		}},
		// The run itself reports a run it was cut short in as an error with no
		// trace of the context that ended it.
		{"running the synthesis", func(_ *fakeSharedSources, _ *fakeDeliveryStore, processor *Processor) {
			processor.runtime = &fakeSynthesizer{err: errors.New("The run was cancelled.")}
		}},
	}
	for _, test := range tests {
		for _, stopping := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s, stopping=%v", test.name, stopping), func(t *testing.T) {
				delivery, sources := synthesisFixture()
				store := &fakeDeliveryStore{}
				processor, err := NewProcessor(store, sources, &fakeSynthesizer{result: Result{Summary: "ok"}}, ProcessorConfig{}, nil)
				if err != nil {
					t.Fatal(err)
				}
				test.break_(sources, store, processor)
				ctx, stop := context.WithCancel(context.Background())
				defer stop()
				if stopping {
					stop()
				}
				processor.process(ctx, delivery)

				if store.applied || store.dropped != "" || store.releases != 1 {
					t.Fatalf("applied=%v dropped=%q releases=%d, want one release and nothing else", store.applied, store.dropped, store.releases)
				}
				if store.releasedFailed == stopping {
					t.Fatalf("released with failed=%v while stopping=%v: only a synthesis that was not cut short counts an attempt", store.releasedFailed, stopping)
				}
				if stopping && !strings.HasPrefix(store.released, "interrupted: ") {
					t.Fatalf("released with reason %q, want it recorded as an interruption", store.released)
				}
				if store.releaseContext != nil {
					t.Fatalf("released under a context that said %v, so releasing it could be cut short", store.releaseContext)
				}
			})
		}
	}
}

// stoppingSynthesizer stops the processor as its synthesis finishes.
type stoppingSynthesizer struct {
	stop   context.CancelFunc
	result Result
}

func (runtime stoppingSynthesizer) Synthesize(context.Context, teamspaces.OrganizationAgent, Synthesis, teamspaces.Space, teamspaces.Entry) (Result, error) {
	runtime.stop()
	return runtime.result, nil
}

// What goes wrong recording a finished synthesis was not the processor
// stopping, which recording is not cut short by: it counts, whether or not the
// processor is stopping when it does.
func TestProcessorCountsAFailureToRecordASynthesisEvenWhileStopping(t *testing.T) {
	delivery, sources := synthesisFixture()
	store := &fakeDeliveryStore{applyErr: errors.New("database unavailable")}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	// The processor stops after the synthesis ran, before it is recorded.
	processor, err := NewProcessor(store, sources, stoppingSynthesizer{stop: stop, result: Result{Summary: "ok"}}, ProcessorConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	processor.process(ctx, delivery)
	if ctx.Err() == nil {
		t.Fatal("the test never stopped the processor")
	}
	if store.releases != 1 || !store.releasedFailed || store.applied {
		t.Fatalf("releases=%d failed=%v applied=%v, want one release that counts an attempt", store.releases, store.releasedFailed, store.applied)
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
