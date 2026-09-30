package agents

import (
	"context"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/ragclient"
)

func TestKnowledgePassagesAreCapturedByTheRealDeliveryCallback(t *testing.T) {
	const note = "Project Falcon launches on 14 October."
	for _, testCase := range []struct {
		name    string
		matches []ragclient.Match
		want    []string
	}{
		{"one-line note", []ragclient.Match{match("Falcon", "note:falcon", note, 0.91)}, []string{note}},
		{"empty search", nil, nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			rag := &fakeRag{matches: map[string][]ragclient.Match{"private_falcon": testCase.matches}}
			knowledge, err := KnowledgeToolbox(KnowledgeConfig{Rag: rag, Tokens: fakeTokens{token: "fixture-token"}, ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			service := &resultsInteractions{}
			_, results := delivering(t, service, []*genai.FunctionCall{{
				ID: "search", Name: "knowledge_search",
				Args: map[string]any{"query": "When does Project Falcon launch?", "collection_ids": []any{"private_falcon"}},
			}}, knowledge)
			if len(results) != 1 || !slices.Equal(results[0].KnowledgePassages, testCase.want) {
				t.Fatalf("captured = %+v, want passages %q", results, testCase.want)
			}
			if !slices.Contains(results[0].Parts, "searched: 1") {
				t.Fatalf("full capture lost search metadata: %+v", results)
			}
			if len(testCase.want) > 0 && (!slices.Contains(results[0].Parts, "passages.text: "+note) ||
				!slices.Contains(results[0].Parts, "passages.document_title: Falcon")) {
				t.Fatalf("full capture lost note text or metadata: %+v", results)
			}
		})
	}
}

func TestKnowledgePassageCaptureRejectsMalformedShapesAndMetadataLookalikes(t *testing.T) {
	const fact = "Project Falcon launches on 14 October."
	for _, testCase := range []struct {
		name   string
		result map[string]any
	}{
		{"flat field name", map[string]any{"passages.text": fact}},
		{"object instead of list", map[string]any{"passages": map[string]any{"text": fact}}},
		{"strings instead of objects", map[string]any{"passages": []any{fact}}},
		{"nested text object", map[string]any{"passages": []any{map[string]any{"text": map[string]any{"text": fact}}}}},
		{"text only in title", map[string]any{"passages": []any{map[string]any{"document_title": fact}}}},
		{"blank text", map[string]any{"passages": []any{map[string]any{"text": "  ", "document_title": fact}}}},
		{"mixed malformed list", map[string]any{"passages": []any{map[string]any{"text": fact}, map[string]any{"text": float64(14)}}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			captured := captureResult("knowledge_search", testCase.result, maxRunResultChars)
			if len(captured.KnowledgePassages) != 0 {
				t.Fatalf("malformed result supplied grounding text: %+v", captured)
			}
			if !slices.Equal(captured.Parts, resultParts(testCase.result, maxRunResultChars)) {
				t.Fatal("passage extraction changed full capture")
			}
		})
	}
	result := map[string]any{"passages": []map[string]any{{"text": fact}}}
	if got := captureResult("apps_read", result, maxRunResultChars); len(got.KnowledgePassages) != 0 {
		t.Fatalf("a non-knowledge result became knowledge passages: %+v", got)
	}
}

func TestKnowledgePassagesStayWithinTheFullCaptureBoundsAndAreCloned(t *testing.T) {
	result := map[string]any{"passages": []any{map[string]any{"text": strings.Repeat("é", 1000)}}}
	for _, limit := range []int{1, 15, 40, 100} {
		captured := captureResult("knowledge_search", result, limit)
		if charsOf(captured) > limit || !slices.Equal(captured.Parts, resultParts(result, limit)) {
			t.Fatalf("limit %d: changed or oversized full capture: %+v", limit, captured)
		}
		for _, text := range captured.KnowledgePassages {
			if !utf8.ValidString(text) || !slices.Contains(captured.Parts, "passages.text: "+text) {
				t.Fatalf("grounding text escaped full capture at limit %d: %+v", limit, captured)
			}
		}
	}
	captured := captureResult("knowledge_search", result, 100)
	ctx := WithRunResults(context.Background(), []ToolResult{captured})
	captured.Parts[0], captured.KnowledgePassages[0] = "changed", "changed"
	read := RunResults(ctx)
	if read[0].Parts[0] == "changed" || read[0].KnowledgePassages[0] == "changed" {
		t.Fatal("WithRunResults aliased full capture or passages")
	}
	read[0].KnowledgePassages[0] = "changed again"
	if RunResults(ctx)[0].KnowledgePassages[0] == "changed again" {
		t.Fatal("RunResults returned an aliased passage list")
	}
}
