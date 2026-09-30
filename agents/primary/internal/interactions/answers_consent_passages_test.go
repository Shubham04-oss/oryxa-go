package interactions

import (
	"context"
	"slices"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/consentguard"
)

func TestBothSlackConsentLevelsForwardTrustedPassagesSeparatelyFromFullCapture(t *testing.T) {
	const note = "Project Falcon launches on 14 October."
	results := []agents.ToolResult{{Tool: "knowledge_search", Parts: []string{
		"passages.document_title: Falcon", "passages.text: " + note, "searched: 1",
	}, KnowledgePassages: []string{note}}}
	for _, choice := range []string{ConsentSummary, ConsentAccess} {
		t.Run(choice, func(t *testing.T) {
			for _, testCase := range []struct {
				name    string
				results []agents.ToolResult
				status  string
			}{
				{"explicit passage", results, StatusCompleted},
				{"empty search", []agents.ToolResult{{Tool: "knowledge_search", Parts: []string{"searched: 1"}}}, StatusReview},
			} {
				t.Run(testCase.name, func(t *testing.T) {
					var asked []consentguard.GroundingInput
					grounder := consentguard.GrounderFunc(func(_ context.Context, input consentguard.GroundingInput) (consentguard.Grounding, error) {
						asked = append(asked, input)
						return consentguard.Grounding{Grounded: consentguard.Score{Value: 0.95}, Relevant: consentguard.Score{Value: 0.87}}, nil
					})
					service, _, request := slackService(t, &fakeGuard{}, consentguard.New(grounder, consentguard.Config{KnowledgePassagesOnly: true}), choice)
					answered, err := service.Respond(withResults(testCase.results), target, request.ID, agents.InteractionResponseInput{
						Status: StatusCompleted, Summary: note,
					})
					if err != nil || answered.Status != testCase.status {
						t.Fatalf("answer = %+v, err = %v; want %s", answered, err, testCase.status)
					}
					if testCase.status == StatusReview {
						if answered.Result != nil || answered.Guard == nil ||
							!slices.Equal(answered.Guard.Kinds, []string{consentguard.KindNoSources}) || len(asked) != 0 {
							t.Fatalf("empty search weakened a hold: answer = %+v, grounder inputs = %+v", answered, asked)
						}
					} else if len(asked) != 1 || !slices.Equal(asked[0].Sources, []string{note}) {
						t.Fatalf("grounder inputs = %+v, want only the explicit note", asked)
					}
				})
			}
		})
	}
}
