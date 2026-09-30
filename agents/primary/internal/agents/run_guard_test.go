package agents

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/relevance"
)

func TestRunGuardMissingBoundedAndCopied(t *testing.T) {
	if !relevance.NeedsJudging(GuardRun(context.Background()).Sources) {
		t.Fatal("missing record skipped judging")
	}
	lines := make([]string, 8)
	for i := range lines {
		lines[i] = fmt.Sprintf("%d%s", i, strings.Repeat("é", 1100))
	}
	ctx := WithRunGuard(context.Background(), RunGuardInput{Request: strings.Repeat("é", maxGuardRequestRunes+1), Context: lines, Sources: []string{"directory_load"}})
	got := GuardRun(ctx)
	if len(got.Context) != relevance.MaxContextLines || !strings.HasPrefix(got.Context[0], "2") || utf8.RuneCountInString(got.Context[0]) != relevance.MaxContextLineRunes || utf8.RuneCountInString(got.Request) != maxGuardRequestRunes {
		t.Fatalf("unbounded input: %+v", got)
	}
	lines[2] = "changed"
	got.Context[0], got.Sources[0] = "changed", "changed"
	if again := GuardRun(ctx); again.Context[0] == "changed" || again.Sources[0] == "changed" {
		t.Fatal("metadata aliases caller memory")
	}
	for _, sources := range [][]string{{""}, {strings.Repeat("x", 129)}} {
		if !relevance.NeedsJudging(GuardRun(WithRunGuard(context.Background(), RunGuardInput{Sources: sources})).Sources) {
			t.Fatal("malformed provenance skipped judging")
		}
	}
}

func TestRunGuardParallelAttemptsAndOverflowStayConservative(t *testing.T) {
	ctx := WithRunGuard(context.Background(), RunGuardInput{})
	var wg sync.WaitGroup
	for i := range 100 {
		name := fmt.Sprintf("read_%d", i)
		called := searchTool(t, name)
		wg.Go(func() {
			strict := agent.NewStrictContextMock(ctx)
			if result, err := recordRunSource(&strict, called, map[string]any{"private": "never retained"}); result != nil || err != nil {
				t.Error("callback changed execution")
			}
		})
	}
	wg.Wait()
	got := GuardRun(ctx)
	if len(got.Sources) != maxGuardSources || !slices.Contains(got.Sources, SourcesUnrecorded) {
		t.Fatalf("overflow sources = %v", got.Sources)
	}
	if got.Request != "" || len(got.Context) != 0 {
		t.Fatal("recorded tool arguments")
	}
	other := WithRunGuard(ctx, RunGuardInput{Request: "next"})
	if len(GuardRun(other).Sources) != 0 {
		t.Fatal("next invocation inherited sources")
	}
}

func TestRunGuardDoesNotMistakeTransfersOrGeneralRequestReadsForPublicTools(t *testing.T) {
	for _, name := range []string{"transfer_to_agent", "interactions_get"} {
		ctx := WithRunGuard(context.Background(), RunGuardInput{})
		strict := agent.NewStrictContextMock(ctx)
		_, _ = recordRunSource(&strict, searchTool(t, name), nil)
		if got := GuardRun(ctx).Sources; !slices.Contains(got, name) || !slices.Contains(got, SourcesUnrecorded) || !relevance.NeedsJudging(got) {
			t.Fatalf("%s sources=%v", name, got)
		}
	}
}

// This uses the real tool callback and publication invocation, while the
// model falsely claims that it used only a harmless directory lookup.
func TestPublicationReceivesActualRunProvenance(t *testing.T) {
	service := &fakeTeamspaceCollaboration{}
	box, err := TeamspaceToolbox(TeamspaceConfig{Service: service})
	if err != nil {
		t.Fatal(err)
	}
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		switch {
		case functionResponse(request, "teamspaces_publish") != "":
			return genai.NewContentFromText("Done.", genai.RoleModel)
		case functionResponse(request, "load_tools") != "":
			return functionCalls(&genai.FunctionCall{ID: "publish", Name: "teamspaces_publish", Args: map[string]any{"teamspace_id": "tsp_1", "kind": "result", "title": "Meeting", "summary": "Thursday.", "tools_used": []any{"directory_load"}}})
		case functionResponse(request, "load_artifacts") != "":
			return functionCalls(&genai.FunctionCall{ID: "load", Name: "load_tools", Args: map[string]any{"names": []any{"teamspaces"}}})
		default:
			return functionCalls(&genai.FunctionCall{ID: "read", Name: "load_artifacts"})
		}
	})
	ctx := WithRunGuard(context.Background(), RunGuardInput{Request: "What time?", Context: []string{"The launch is Thursday."}})
	ctx = memberauth.WithAccessToken(ctx, "member-token")
	newHarness(t, llm, box).turnWith(ctx, "user_a", memberState("agt_a", "Carl", "primary"), "What time?")
	if len(service.guardInputs) != 1 {
		t.Fatalf("publication calls = %d", len(service.guardInputs))
	}
	got := service.guardInputs[0]
	if got.Request != "What time?" || !slices.Equal(got.Context, []string{"The launch is Thursday."}) || !slices.Contains(got.Sources, "load_artifacts") || !slices.Contains(got.Sources, "teamspaces_publish") || slices.Contains(got.Sources, "directory_load") {
		t.Fatalf("publication metadata = %+v", got)
	}
}
