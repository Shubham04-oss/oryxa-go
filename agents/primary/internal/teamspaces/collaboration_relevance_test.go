package teamspaces

import (
	"context"
	"errors"
	"slices"
	"testing"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/guardpolicy"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/relevance"
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
		{name: "missing record", missing: true, judged: true, held: true},
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
