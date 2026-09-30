package agents

import (
	"slices"
	"strings"
	"testing"

	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/ragclient"
)

// These fixtures use the real knowledge toolbox and ADK callbacks. They
// characterize source capture, not Bedrock's semantic score for the note.
func TestKnowledgeSearchProvenanceReachesRespondThroughTheDeliveryCallback(t *testing.T) {
	const note = "Project Falcon launches on 14 October. The v2 pricing plan's codename is Tern."
	for _, testCase := range []struct {
		name    string
		matches []ragclient.Match
		parts   []string
	}{
		{
			name:    "one-line note and its metadata",
			matches: []ragclient.Match{match("Falcon", "note:falcon", note, 0.91)},
			parts: []string{
				"passages.collection_id: private_falcon",
				"passages.document_title: Falcon",
				"passages.score: 0.91",
				"passages.source: note:falcon",
				"passages.text: " + note,
				"searched: 1",
			},
		},
		{
			name:  "empty search still records its searched count",
			parts: []string{"searched: 1"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			rag := &fakeRag{matches: map[string][]ragclient.Match{"private_falcon": testCase.matches}}
			knowledge, err := KnowledgeToolbox(KnowledgeConfig{Rag: rag, Tokens: fakeTokens{token: "fixture-token"}, ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			service := &resultsInteractions{}
			llm, results := delivering(t, service, []*genai.FunctionCall{{
				ID: "search", Name: "knowledge_search",
				Args: map[string]any{"query": "When does Project Falcon launch?", "collection_ids": []any{"private_falcon"}},
			}}, knowledge)
			if len(results) != 1 || results[0].Tool != "knowledge_search" || !slices.Equal(results[0].Parts, testCase.parts) {
				t.Fatalf("respond provenance = %+v, want knowledge_search parts %q", results, testCase.parts)
			}
			if searched := rag.searched(); len(searched) != 1 || !strings.HasPrefix(searched[0], "search org_1 private_falcon fixture-token ") {
				t.Fatalf("member-scoped search = %q", searched)
			}
			modelResult := functionResponse(llm.requestsFor(RootName)[1], "knowledge_search")
			if len(testCase.matches) > 0 {
				if !strings.Contains(modelResult, note) {
					t.Fatalf("the model did not receive the note: %s", modelResult)
				}
			} else if strings.Contains(modelResult, note) || !strings.Contains(modelResult, "passages:[]") {
				t.Fatalf("the empty search gave the model %s", modelResult)
			}
		})
	}
}
