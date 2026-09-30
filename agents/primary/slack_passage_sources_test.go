package primary

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/consentguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
)

type passageSourceBedrock struct{ source string }

func (fixture *passageSourceBedrock) ApplyGuardrail(_ context.Context, input *bedrockruntime.ApplyGuardrailInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ApplyGuardrailOutput, error) {
	for _, block := range input.Content {
		if text, ok := block.(*types.GuardrailContentBlockMemberText); ok && len(text.Value.Qualifiers) == 1 &&
			text.Value.Qualifiers[0] == types.GuardrailContentQualifierGroundingSource {
			fixture.source = aws.ToString(text.Value.Text)
		}
	}
	// Fixed fixture assessments prove the service wiring, not semantic accuracy.
	return &bedrockruntime.ApplyGuardrailOutput{Assessments: []types.GuardrailAssessment{{
		ContextualGroundingPolicy: &types.GuardrailContextualGroundingPolicyAssessment{Filters: []types.GuardrailContextualGroundingFilter{
			{Type: types.GuardrailContextualGroundingFilterTypeGrounding, Score: aws.Float64(0.95), Threshold: aws.Float64(0.5)},
			{Type: types.GuardrailContextualGroundingFilterTypeRelevance, Score: aws.Float64(0.87), Threshold: aws.Float64(0.3)},
		}},
	}}}, nil
}

func TestConsentPassageSourcesFlagReachesTheConfiguredBedrockRequest(t *testing.T) {
	const note = "Project Falcon launches on 14 October."
	source := consentguard.Source{Tool: "knowledge_search", Parts: []string{
		"passages.document_title: Falcon", "passages.text: " + note, "searched: 1",
	}, KnowledgePassages: []string{note}}
	for _, testCase := range []struct {
		name, value, want string
	}{
		{"unset defaults off", "", strings.Join(source.Parts, "\n")},
		{"explicitly off", "false", strings.Join(source.Parts, "\n")},
		{"explicitly on", "true", note},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			env := map[string]string{
				"AGENTS_PRIMARY_CONSENT_GUARDRAIL_ID": "fixture-consent", "AGENTS_PRIMARY_CONSENT_GUARDRAIL_VERSION": "1",
				"AGENTS_PRIMARY_CONSENT_KNOWLEDGE_PASSAGES_ONLY": testCase.value,
			}
			settings, err := serviceconfig.FromEnv(func(key string) string { return env[key] })
			if err != nil {
				t.Fatal(err)
			}
			bedrock := &passageSourceBedrock{}
			guard, err := newConsentGuard(settings, bedrock, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			result := guard.Check(t.Context(), consentguard.Input{
				Level: consentguard.LevelAccess, Question: "When does Project Falcon launch?", Texts: []string{note}, Sources: []consentguard.Source{source},
			})
			if result.Action != consentguard.ActionPass || bedrock.source != testCase.want {
				t.Fatalf("action = %s, grounding source = %q, want %q", result.Action, bedrock.source, testCase.want)
			}
		})
	}
}
