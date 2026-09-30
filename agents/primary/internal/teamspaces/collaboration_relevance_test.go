package teamspaces

import (
	"context"
	"errors"
	"iter"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/guardpolicy"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/relevance"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

type publicationJudge struct {
	inputs []relevance.Input
	result relevance.Result
}

func (judge *publicationJudge) Judge(_ context.Context, input relevance.Input) relevance.Result {
	judge.inputs = append(judge.inputs, input)
	return judge.result
}

type publicationPolicy struct {
	effective guardpolicy.Effective
	err       error
	org, user string
}

func (policy *publicationPolicy) Effective(_ context.Context, org, user string) (guardpolicy.Effective, error) {
	policy.org, policy.user = org, user
	return policy.effective, policy.err
}

func TestAgentPublicationUsesTrustedSourcesAndCappedWorkspaceRelevance(t *testing.T) {
	for _, test := range []struct {
		name         string
		mode         guardpolicy.RelevanceMode
		cap          guardpolicy.Cap
		sources      []string
		missing      bool
		regulated    bool
		result       relevance.Result
		policyErr    error
		judged, held bool
	}{
		{name: "private read held", sources: []string{"apps_gmail_search"}, judged: true, held: true},
		{name: "shadow cap", sources: []string{"apps_gmail_search"}, cap: guardpolicy.Cap{Relevance: guardpolicy.RelevanceShadow}, judged: true},
		{name: "off cap", sources: []string{"apps_gmail_search"}, cap: guardpolicy.Cap{Relevance: guardpolicy.RelevanceOff}},
		{name: "workspace off", mode: guardpolicy.RelevanceOff, sources: []string{"apps_gmail_search"}},
		{name: "hold allowlist", sources: []string{"load_memory"}, cap: guardpolicy.Cap{Relevance: guardpolicy.RelevanceShadow, HoldOrgs: map[string]bool{"org_1": true}}, judged: true, held: true},
		{name: "fresh public answer"},
		{name: "only directory", sources: []string{"directory_load"}},
		{name: "private history", sources: []string{relevance.ThreadHistory}, judged: true, held: true},
		{name: "missing record", missing: true, held: true},
		{name: "new tool", sources: []string{"future_private_search"}, judged: true, held: true},
		{name: "judge unavailable", sources: []string{"load_memory"}, result: relevance.Result{Err: errors.New("unavailable")}, judged: true, held: true},
		{name: "regulated requested health", sources: []string{"load_memory"}, regulated: true, result: relevance.Result{Judged: true, Asked: []relevance.Category{relevance.Health}}, judged: true, held: true},
		{name: "policy fallback retains shadow cap", sources: []string{"load_memory"}, cap: guardpolicy.Cap{Relevance: guardpolicy.RelevanceShadow}, policyErr: errors.New("store unavailable"), judged: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := test.result
			if !result.Judged && result.Err == nil {
				result = relevance.Result{Judged: true, Extras: []relevance.Category{relevance.Health}}
			}
			judge := &publicationJudge{result: result}
			policy := &publicationPolicy{effective: guardpolicy.Resolve("org_1", guardpolicy.OrgPolicy{Relevance: test.mode, Regulated: test.regulated}, guardpolicy.Choices{}, test.cap).Effective(), err: test.policyErr}
			service, repo := guardedSpace(t, WithGuard(realGuard(t, &bedrockFinding{}, answerguard.WithRelevance(judge))), WithGuardPolicy(policy))
			ctx := context.Background()
			if !test.missing {
				ctx = primaryagents.WithRunGuard(ctx, primaryagents.RunGuardInput{Request: "When is the launch meeting?", Context: []string{"We are discussing the launch."}, Sources: test.sources})
			}
			shared, err := service.CollaborationPublish(ctx, ada, "token", "tsp_1", primaryagents.SharedEntryInput{Kind: "result", Title: "Launch", Summary: "Thursday. Ada is having surgery.", ToolsUsed: []string{"directory_load"}})
			if test.held {
				if !errors.Is(err, ErrUnchecked) || len(repo.entries) != 0 {
					t.Fatalf("held publication stored: shared=%+v err=%v entries=%d", shared, err, len(repo.entries))
				}
			} else if err != nil || len(repo.entries) != 1 || shared.Summary != "Thursday. Ada is having surgery." {
				t.Fatalf("shareable publication=%+v err=%v entries=%d", shared, err, len(repo.entries))
			}
			if (len(judge.inputs) > 0) != test.judged {
				t.Fatalf("judge calls=%d want judged=%t", len(judge.inputs), test.judged)
			}
			if policy.org != ada.OrganizationID || policy.user != ada.UserID {
				t.Fatalf("policy resolved for %s/%s", policy.org, policy.user)
			}
			if test.judged && !test.missing {
				input := judge.inputs[0]
				if input.Request != "When is the launch meeting?" || !slices.Equal(input.Context, []string{"We are discussing the launch."}) || !slices.Equal(input.Sources, test.sources) || input.Regulated != test.regulated {
					t.Fatalf("judge input=%+v", input)
				}
			}
		})
	}
}

func TestAgentPublicationUsesPeerGuardIntensity(t *testing.T) {
	for _, level := range []guardpolicy.Level{guardpolicy.LevelStrict, guardpolicy.LevelSecrets} {
		t.Run(string(level), func(t *testing.T) {
			policy := &publicationPolicy{effective: guardpolicy.Effective{Peer: level, Relevance: guardpolicy.RelevanceOff}}
			service, _ := guardedSpace(t, WithGuard(realGuard(t, &bedrockFinding{})), WithGuardPolicy(policy))
			shared, err := service.CollaborationPublish(context.Background(), ada, "token", "tsp_1", primaryagents.SharedEntryInput{Kind: "result", Title: "Launch", Summary: "Contact priya@example.com; the password is hunter2."})
			if err != nil {
				t.Fatal(err)
			}
			want := "Contact priya@example.com; the password is {PASSWORD}."
			if level == guardpolicy.LevelStrict {
				want = "Contact {EMAIL}; the password is {PASSWORD}."
			}
			if shared.Summary != want {
				t.Fatalf("summary=%q want=%q", shared.Summary, want)
			}
		})
	}
}

func TestAgentPublicationCannotShareAnUnjudgedMiddle(t *testing.T) {
	judge := &publicationJudge{result: relevance.Result{Judged: true}}
	policy := &publicationPolicy{effective: guardpolicy.Effective{Peer: guardpolicy.LevelStrict, Relevance: guardpolicy.RelevanceHold}}
	service, repo := guardedSpace(t, WithGuard(realGuard(t, &bedrockFinding{}, answerguard.WithRelevance(judge))), WithGuardPolicy(policy))
	ctx := primaryagents.WithRunGuard(context.Background(), primaryagents.RunGuardInput{Request: "When is launch?", Sources: []string{"load_memory"}})
	summary := strings.Repeat("a", 9_900) + "Ada is having surgery." + strings.Repeat("b", 9_900)
	_, err := service.CollaborationPublish(ctx, ada, "token", "tsp_1", primaryagents.SharedEntryInput{
		Kind: "result", Title: "Launch", Summary: summary,
		Artifacts: []primaryagents.SharedArtifact{
			{Name: "First", URI: "https://example.com/" + strings.Repeat("c", 3_900)},
			{Name: "Second", URI: "https://example.com/" + strings.Repeat("d", 3_900)},
		},
	})
	if !errors.Is(err, ErrUnchecked) || len(repo.entries) != 0 {
		t.Fatalf("an oversized answer shared an unjudged middle: err=%v entries=%d", err, len(repo.entries))
	}
}

// One isolated run has no concurrent lease owner or persistence under test.
type publicationRuns struct{ outcome runstore.Outcome }

func (*publicationRuns) Start(context.Context, runstore.Run, time.Duration) error { return nil }
func (*publicationRuns) Renew(context.Context, string, time.Duration) error       { return nil }
func (runs *publicationRuns) Finish(_ context.Context, _ string, outcome runstore.Outcome, _ events.Event) error {
	runs.outcome = outcome
	return nil
}
func (*publicationRuns) Cancel(context.Context, string) error { return nil }

type firstSidePublicationModel struct{ summary string }

func (firstSidePublicationModel) Name() string { return "first-side-publication" }
func (llm firstSidePublicationModel) GenerateContent(_ context.Context, input *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	var response *genai.Content
	last := ""
	for _, content := range input.Contents {
		for _, part := range content.Parts {
			if part.FunctionResponse != nil {
				last = part.FunctionResponse.Name
			}
		}
	}
	switch last {
	case "teamspaces_publish":
		response = genai.NewContentFromText("Finished.", genai.RoleModel)
	case "load_tools":
		response = &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "publish", Name: "teamspaces_publish", Args: map[string]any{
			"teamspace_id": "tsp_1", "kind": "result", "title": "Launch", "summary": llm.summary,
		}}}}}
	default:
		response = &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "load", Name: "load_tools", Args: map[string]any{"names": []any{"teamspaces"}}}}}}
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: response, TurnComplete: true}, nil)
	}
}

func TestFirstSideRunJudgesSharedContextBeforePublishingToTheTeam(t *testing.T) {
	for _, sharedContext := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh public-only run", true: "private fact from side context"}[sharedContext], func(t *testing.T) {
			judge := &publicationJudge{result: relevance.Result{Judged: true, Hold: true, Extras: []relevance.Category{relevance.Health}}}
			policy := &publicationPolicy{effective: guardpolicy.Effective{Peer: guardpolicy.LevelStrict, Relevance: guardpolicy.RelevanceHold}}
			ledger, repo := guardedSpace(t, WithGuard(realGuard(t, &bedrockFinding{}, answerguard.WithRelevance(judge))), WithGuardPolicy(policy))
			summary := "Launch is Friday."
			question := &threads.PublicationRequest{Request: "Publish launch timing"}
			if sharedContext {
				summary += " Ada is having surgery."
				question.Context = []string{"Bo: Ada is having surgery."}
			}
			box, err := primaryagents.TeamspaceToolbox(primaryagents.TeamspaceConfig{Service: ledger})
			if err != nil {
				t.Fatal(err)
			}
			llm := firstSidePublicationModel{summary: summary}
			router, err := modelrouter.New(llm, llm)
			if err != nil {
				t.Fatal(err)
			}
			root, err := primaryagents.Root(router, []primaryagents.Toolbox{box}, false)
			if err != nil {
				t.Fatal(err)
			}
			prices, _ := pricing.NewTable(nil)
			runs := &publicationRuns{}
			service, err := threads.New(threads.Config{AppName: "krude", Agent: root, Sessions: session.InMemoryService(), Artifacts: artifact.InMemoryService(), Runs: runs, Prices: prices})
			if err != nil {
				t.Fatal(err)
			}
			member := threads.Member{OrganizationID: ada.OrganizationID, UserID: ada.UserID, AgentID: ada.AgentID, AgentName: ada.AgentName, AgentKind: ada.AgentKind, AccessToken: "member-token"}
			thread, err := service.CreateFrom(context.Background(), member, threads.OriginSideConversation)
			if err != nil {
				t.Fatal(err)
			}
			run, err := service.StartRun(context.Background(), member, thread.ID, threads.RunInput{Message: strings.Join(question.Context, "\n") + "\nMember asks: " + question.Request, PublicationRequest: question, TeamspaceID: "tsp_1"})
			if err != nil {
				t.Fatal(err)
			}
			published := false
			var steps []string
			run.Execute(context.Background(), func(item threads.Item) error {
				steps = append(steps, string(item.Type)+":"+item.Tool+":"+item.Error)
				if item.Type == threads.ItemToolCall && item.Tool == "teamspaces_publish" {
					published = true
				}
				return nil
			})
			if !published {
				t.Fatalf("the real Root never invoked teamspaces_publish: %v (%s)", steps, runs.outcome.Error)
			}
			if sharedContext {
				if len(judge.inputs) != 1 || len(repo.entries) != 0 {
					t.Fatalf("shared private context bypassed relevance: judge calls=%d stored entries=%d", len(judge.inputs), len(repo.entries))
				}
				input := judge.inputs[0]
				if input.Request != question.Request || !slices.Equal(input.Context, question.Context) || !slices.Contains(input.Sources, relevance.ThreadHistory) {
					t.Fatalf("first-side publication input=%+v", input)
				}
			} else if len(judge.inputs) != 0 || len(repo.entries) != 1 {
				t.Fatalf("fresh public-only publication changed: judge calls=%d entries=%d", len(judge.inputs), len(repo.entries))
			}
		})
	}
}
