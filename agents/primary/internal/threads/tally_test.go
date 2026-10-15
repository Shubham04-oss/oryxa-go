package threads

import (
	"context"
	"errors"
	"iter"
	"maps"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/s3artifact"
)

// filesSetup changes a filesHarness: the model its agent calls, and its
// service's config. Either may be nil.
type filesSetup struct {
	model  func(model.LLM) model.LLM
	config func(*Config)
}

// filesHarness is a service whose agent keeps the files each message names,
// one tool call per message, comma-separated.
func filesHarness(t *testing.T, setups ...filesSetup) (*harness, session.Service) {
	t.Helper()
	keep, err := functiontool.New(functiontool.Config{Name: "keep_files", Description: "Keeps files."},
		func(ctx agent.Context, args struct {
			Names []string `json:"names"`
		}) (map[string]any, error) {
			for _, name := range args.Names {
				if _, err := ctx.Artifacts().Save(ctx, name, genai.NewPartFromBytes([]byte(name), "application/octet-stream")); err != nil {
					return nil, err
				}
			}
			return map[string]any{"kept": len(args.Names)}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	llm := &scriptedModel{respond: func(request *model.LLMRequest) (*genai.Content, error) {
		if lastFunctionResponse(request) != nil {
			return genai.NewContentFromText("Kept.", genai.RoleModel), nil
		}
		var names []any
		for _, part := range request.Contents[len(request.Contents)-1].Parts {
			for name := range strings.SplitSeq(part.Text, ",") {
				if name != "" {
					names = append(names, name)
				}
			}
		}
		return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "call_keep", Name: "keep_files", Args: map[string]any{"names": names},
		}}}}, nil
	}}
	var calling model.LLM = llm
	for _, setup := range setups {
		if setup.model != nil {
			calling = setup.model(calling)
		}
	}
	krude, err := llmagent.New(llmagent.Config{Name: "krude", Model: calling, Instruction: "You are Krude.", Tools: []tool.Tool{keep}})
	if err != nil {
		t.Fatal(err)
	}
	prices, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.InMemoryService()
	runs := newFakeRuns()
	config := Config{
		AppName: "krude", Agent: krude, Sessions: sessions, Artifacts: s3artifact.ByOrganization(artifact.InMemoryService()),
		Runs: runs, Prices: prices, RunTimeout: time.Minute,
	}
	for _, setup := range setups {
		if setup.config != nil {
			setup.config(&config)
		}
	}
	service, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, service: service, runs: runs, model: llm, sessions: sessions}, sessions
}

func TestTheThreadListSaysWhatFilesEachThreadKept(t *testing.T) {
	h, _ := filesHarness(t)
	ctx := context.Background()
	created, err := h.service.Create(ctx, asha)
	if err != nil {
		t.Fatal(err)
	}
	if created.ArtifactCount != 0 || created.LatestArtifact != nil {
		t.Fatalf("a new thread = %+v, want no files", created)
	}
	h.run(asha, created.ID, RunInput{Message: "plan.docx,Budget.XLSX"})
	// A second version of a file is the same file; a new one is the latest.
	h.run(asha, created.ID, RunInput{Message: "plan.docx,shot.png"})

	listed, err := h.service.List(ctx, asha)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ArtifactCount != 3 || listed[0].LatestArtifact == nil ||
		*listed[0].LatestArtifact != (ArtifactRef{Name: "shot.png", Kind: "png"}) {
		t.Fatalf("listed = %+v, want three files, shot.png the latest", listed)
	}
	detail, err := h.service.Get(ctx, asha, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ArtifactCount != 3 || detail.LatestArtifact == nil || detail.LatestArtifact.Name != "shot.png" {
		t.Fatalf("detail = %+v", detail.Thread)
	}
	// What the runs recorded shows nothing in the history.
	if got := len(find(detail.Items, ItemArtifact)); got != 4 {
		t.Fatalf("artifact items = %d, want one per version kept", got)
	}
	if ref := artifactRef("README"); ref == nil || ref.Kind != "" {
		t.Fatalf("artifactRef(README) = %+v, want no kind", ref)
	}
}

// A thread whose runs ended before runs recorded their files is counted on
// read and in full by its next run.
func TestAThreadFromBeforeTheTallyIsCountedByItsNextRun(t *testing.T) {
	h, sessions := filesHarness(t)
	ctx := context.Background()
	created, err := h.service.Create(ctx, asha)
	if err != nil {
		t.Fatal(err)
	}
	h.run(asha, created.ID, RunInput{Message: "old.pdf"})
	// Forget the tally, as a thread from before it would have none.
	got, err := sessions.Get(ctx, &session.GetRequest{AppName: "krude", UserID: asha.UserID, SessionID: created.ID})
	if err != nil {
		t.Fatal(err)
	}
	forget := session.NewEvent(ctx, "test")
	forget.Author = adkUserAuthor
	forget.Actions.StateDelta = map[string]any{stateArtifactCount: nil, stateLatestArtifact: nil}
	if err := sessions.AppendEvent(ctx, got.Session, forget); err != nil {
		t.Fatal(err)
	}
	listed, err := h.service.List(ctx, asha)
	if err != nil {
		t.Fatal(err)
	}
	if listed[0].ArtifactCount != 0 || listed[0].LatestArtifact != nil {
		t.Fatalf("listed before the next run = %+v, want nothing recorded", listed[0])
	}
	if detail, err := h.service.Get(ctx, asha, created.ID); err != nil || detail.ArtifactCount != 1 {
		t.Fatalf("detail = %+v, %v; want the history counted", detail.Thread, err)
	}

	h.run(asha, created.ID, RunInput{Message: "new.md"})
	listed, err = h.service.List(ctx, asha)
	if err != nil {
		t.Fatal(err)
	}
	if listed[0].ArtifactCount != 2 || listed[0].LatestArtifact.Name != "new.md" || listed[0].LatestArtifact.Kind != "md" {
		t.Fatalf("listed after the next run = %+v, want both files", listed[0])
	}
}

// A run that fails after keeping a file still records it: what it kept, it
// kept.
func TestAFailedRunRecordsTheFilesItKept(t *testing.T) {
	h, _ := filesHarness(t)
	ctx := context.Background()
	created, err := h.service.Create(ctx, asha)
	if err != nil {
		t.Fatal(err)
	}
	keep := h.model.respond
	h.model.respond = func(request *model.LLMRequest) (*genai.Content, error) {
		if lastFunctionResponse(request) != nil {
			return nil, errors.New("the model was unreachable")
		}
		return keep(request)
	}
	items, _ := h.run(asha, created.ID, RunInput{Message: "draft.pdf"})
	if last := items[len(items)-1]; last.Type != ItemRunFailed {
		t.Fatalf("last item = %+v, want the run failed", last)
	}
	listed, err := h.service.List(ctx, asha)
	if err != nil {
		t.Fatal(err)
	}
	if listed[0].ArtifactCount != 1 || listed[0].LatestArtifact == nil || listed[0].LatestArtifact.Name != "draft.pdf" {
		t.Fatalf("listed = %+v, want the file the failed run kept", listed[0])
	}
}

// cutShortModel answers as its model does until a tool has answered, then
// waits for the run to stop and reports a plain cancellation, as a call cut
// short does: it does not carry why the run was stopped.
type cutShortModel struct{ model.LLM }

func (m cutShortModel) GenerateContent(ctx context.Context, request *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if lastFunctionResponse(request) == nil {
		return m.LLM.GenerateContent(ctx, request, stream)
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		<-ctx.Done()
		yield(nil, context.Canceled)
	}
}

// A run that lost its hold on the thread writes nothing more to it, though
// the call it cut short reports only that it was cancelled: another run may
// be writing the thread now.
func TestARunThatLostItsHoldRecordsNothing(t *testing.T) {
	h, sessions := filesHarness(t, filesSetup{
		model:  func(llm model.LLM) model.LLM { return cutShortModel{llm} },
		config: func(config *Config) { config.Lease = 3 * time.Second },
	})
	ctx := context.Background()
	created, err := h.service.Create(ctx, asha)
	if err != nil {
		t.Fatal(err)
	}
	h.runs.renewErr = runstore.ErrNotRunning
	items, _ := h.run(asha, created.ID, RunInput{Message: "draft.pdf"})
	if last := items[len(items)-1]; last.Type != ItemRunFailed || last.Error != "Another run took over this thread." {
		t.Fatalf("last item = %+v, want the run taken over", last)
	}
	if got := len(find(items, ItemArtifact)); got != 1 {
		t.Fatalf("artifact items = %d, want the file kept before the hold was lost", got)
	}
	got, err := sessions.Get(ctx, &session.GetRequest{AppName: "krude", UserID: asha.UserID, SessionID: created.ID})
	if err != nil {
		t.Fatal(err)
	}
	for event := range got.Session.Events().All() {
		if bracket, ok := bracketOf(event); ok && bracket.Type != ItemRunStarted {
			t.Fatalf("a run that lost its hold recorded %+v", bracket)
		}
	}
	if _, err := got.Session.State().Get(stateArtifactCount); !errors.Is(err, session.ErrStateKeyNotExist) {
		t.Fatalf("the thread's count = %v, want none recorded", err)
	}
}

// Why a run ended is the context's cause when the run was stopped for a lost
// hold or a drain, though the call it cut short says only that it was
// cancelled, so a run that lost its hold never records.
func TestARunEndsForWhyItsContextStopped(t *testing.T) {
	stopped := func(cause error) context.Context {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(cause)
		return ctx
	}
	cases := []struct {
		name    string
		ctx     context.Context
		failure error
		want    error
	}{
		{"lost hold, plain cancellation", stopped(errLeaseLost), context.Canceled, errLeaseLost},
		{"lost hold, no error", stopped(errLeaseLost), nil, errLeaseLost},
		{"drain, plain cancellation", stopped(ErrInterrupted), context.Canceled, ErrInterrupted},
		{"member left, the call's own error", stopped(context.Canceled), errors.New("the model was unreachable"), nil},
		{"running, the call's own error", context.Background(), context.Canceled, context.Canceled},
	}
	for _, c := range cases {
		got := endedBy(c.ctx, c.failure)
		want := c.want
		if want == nil {
			want = c.failure
		}
		if !errors.Is(got, want) {
			t.Errorf("%s: endedBy = %v, want %v", c.name, got, want)
		}
	}
}

func TestATallyThatHasNotChangedWritesNothing(t *testing.T) {
	tally := tallyArtifacts(nil)
	state := map[string]any{stateArtifactCount: float64(0), stateLatestArtifact: ""}
	if changes := tally.changes(mapState(state)); changes != nil {
		t.Fatalf("changes = %v, want none for a count read back as a JSON number", changes)
	}
	if changes := tally.changes(mapState(map[string]any{})); changes == nil {
		t.Fatal("a thread with no count recorded was not given one")
	}
}

// mapState is state as a session reads it.
type mapState map[string]any

func (state mapState) Get(key string) (any, error) {
	value, ok := state[key]
	if !ok {
		return nil, session.ErrStateKeyNotExist
	}
	return value, nil
}

func (state mapState) All() iter.Seq2[string, any] { return maps.All(state) }
