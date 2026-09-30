package consentguard

import (
	"slices"
	"testing"
)

// Findings below are supplied by a fake grounder. They verify source selection
// and holds, not Bedrock's semantic score for any example.
func TestPassageOnlyGroundingKeepsCopyProtectionAndOtherVerifiedSources(t *testing.T) {
	const note = "Project Falcon launches on 14 October."
	search := Source{Tool: "knowledge_search", Parts: []string{
		"passages.document_title: Falcon", "passages.text: " + note, "searched: 1",
	}, KnowledgePassages: []string{note}}
	copied := firstWords(passage, 25)
	for _, testCase := range []struct {
		name    string
		sources []Source
		answer  string
		score   float64
		want    []string
		kinds   []string
	}{
		{"correct one-line answer", []Source{search}, note, 0.95, []string{note}, nil},
		{"unsupported extra claim", []Source{search}, note + " It is related to the newest AI launch.", 0.23, []string{note}, []string{KindUngrounded}},
		{"copied summary still uses full capture", []Source{{
			Tool: "knowledge_search", Parts: []string{"passages.document_title: " + copied, "passages.text: " + note},
			KnowledgePassages: []string{note},
		}}, copied, 0.95, nil, []string{KindCopied}},
		{"empty search cannot bypass grounding", []Source{{Tool: "knowledge_search", Parts: []string{"searched: 1"}}}, note, 0.95, nil, []string{KindNoSources}},
		{"flattened metadata is not an explicit passage", []Source{{Tool: "knowledge_search", Parts: []string{"passages.text: " + note}}}, note, 0.95, nil, []string{KindNoSources}},
		{"knowledge status cannot supply passages", []Source{{Tool: "knowledge_sources", Parts: []string{"status: active"}, KnowledgePassages: []string{note}}}, note, 0.95, nil, []string{KindNoSources}},
		{"empty knowledge plus substantive app source", []Source{
			{Tool: "knowledge_search", Parts: []string{"searched: 1"}},
			{Tool: "apps_read", Parts: []string{"body: " + note}},
		}, note, 0.95, []string{"body: " + note}, nil},
		{"mixed verified sources retain their order", []Source{search, {Tool: "apps_read", Parts: []string{"body: The launch review is complete."}}}, note, 0.95,
			[]string{note, "body: The launch review is complete."}, nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			grounder := &scoring{grounding: Grounding{Grounded: Score{Value: testCase.score}, Relevant: Score{Value: 0.87}}}
			result := New(grounder, Config{KnowledgePassagesOnly: true}).Check(t.Context(), Input{
				Level: LevelSummary, Question: "When does Project Falcon launch?", Texts: []string{testCase.answer}, Sources: testCase.sources,
			})
			if !slices.Equal(reasonKinds(result), testCase.kinds) {
				t.Fatalf("held for %v, want %v", result.Reasons, testCase.kinds)
			}
			if wantHold := len(testCase.kinds) > 0; (result.Action == ActionHold) != wantHold {
				t.Fatalf("action = %s, want held = %v", result.Action, wantHold)
			}
			if testCase.want == nil {
				if len(grounder.asked) != 0 {
					t.Fatal("an already held or sourceless answer called the grounder")
				}
			} else if len(grounder.asked) != 1 || !slices.Equal(grounder.asked[0].Sources, testCase.want) {
				t.Fatalf("grounder inputs = %+v, want sources %q", grounder.asked, testCase.want)
			}
		})
	}
}
