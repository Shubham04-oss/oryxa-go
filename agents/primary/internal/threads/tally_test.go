package threads

import (
	"context"
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
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/s3artifact"
)

// filesHarness is a service whose agent keeps the files each message names,
// one tool call per message, comma-separated.
func filesHarness(t *testing.T) (*harness, session.Service) {
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
	krude, err := llmagent.New(llmagent.Config{Name: "krude", Model: llm, Instruction: "You are Krude.", Tools: []tool.Tool{keep}})
	if err != nil {
		t.Fatal(err)
	}
	prices, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.InMemoryService()
	runs := newFakeRuns()
	service, err := New(Config{
		AppName: "krude", Agent: krude, Sessions: sessions, Artifacts: s3artifact.ByOrganization(artifact.InMemoryService()),
		Runs: runs, Prices: prices, RunTimeout: time.Minute,
	})
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
