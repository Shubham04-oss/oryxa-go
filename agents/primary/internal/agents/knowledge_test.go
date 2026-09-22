package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/knowledgeclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/ragclient"
)

// fakeRag stands in for rag: a few corpora, each answering a search with
// passages scored by the corpus it came from.
type fakeRag struct {
	mu        sync.Mutex
	corpora   []ragclient.Corpus
	matches   map[string][]ragclient.Match
	failures  map[string]error
	calls     []string
	minScores []float64
}

func (rag *fakeRag) record(call string) {
	rag.mu.Lock()
	defer rag.mu.Unlock()
	rag.calls = append(rag.calls, call)
}

func (rag *fakeRag) Corpora(_ context.Context, organizationID, token string) ([]ragclient.Corpus, error) {
	rag.record("corpora " + organizationID + " " + token)
	return rag.corpora, nil
}

func (rag *fakeRag) Search(_ context.Context, organizationID, corpusID, token, query string, topK int, minScore float64) ([]ragclient.Match, error) {
	rag.record(fmt.Sprintf("search %s %s %s %q %d", organizationID, corpusID, token, query, topK))
	rag.mu.Lock()
	rag.minScores = append(rag.minScores, minScore)
	rag.mu.Unlock()
	if err := rag.failures[corpusID]; err != nil {
		return nil, err
	}
	return rag.matches[corpusID], nil
}

func (rag *fakeRag) searched() []string {
	rag.mu.Lock()
	defer rag.mu.Unlock()
	var searched []string
	for _, call := range rag.calls {
		if strings.HasPrefix(call, "search ") {
			searched = append(searched, call)
		}
	}
	return searched
}

func match(title, source, text string, score float64, headings ...string) ragclient.Match {
	var found ragclient.Match
	found.Chunk.Text = text
	found.Chunk.HeadingPath = headings
	found.Document.Title = title
	found.Document.Source.Kind = "external"
	found.Document.Source.Ref = source
	found.Score = score
	return found
}

type fakeTokens struct{ token string }

type fakeKnowledgeSources struct {
	listed []knowledgeclient.Source
	called string
}

type syncableKnowledgeSources struct{ fakeKnowledgeSources }

func (*syncableKnowledgeSources) SyncSource(context.Context, string, string, string) (knowledgeclient.Source, error) {
	return knowledgeclient.Source{ID: "s1", Status: "active"}, nil
}

func (source *fakeKnowledgeSources) Sources(_ context.Context, org, token, provider, connection string) ([]knowledgeclient.Source, error) {
	source.called = org + " " + token + " " + provider + " " + connection
	return source.listed, nil
}

func (tokens fakeTokens) AgentToken(context.Context, string) (string, time.Time, error) {
	return tokens.token, time.Now().Add(time.Hour), nil
}

// searchesKnowledgeLLM has Krude load the knowledge toolbox, search it with
// query, and answer with whatever the search returned.
func searchesKnowledgeLLM(query string, collectionIDs ...string) model.LLM {
	return newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		switch {
		case functionResponse(request, "knowledge_search") != "":
			return genai.NewContentFromText("Found: "+functionResponse(request, "knowledge_search"), genai.RoleModel)
		case functionResponse(request, "load_tools") != "":
			args := map[string]any{"query": query}
			if len(collectionIDs) > 0 {
				ids := make([]any, 0, len(collectionIDs))
				for _, id := range collectionIDs {
					ids = append(ids, id)
				}
				args["collection_ids"] = ids
			}
			return functionCalls(&genai.FunctionCall{ID: "call_search", Name: "knowledge_search", Args: args})
		default:
			return functionCalls(&genai.FunctionCall{ID: "call_load", Name: "load_tools", Args: map[string]any{"names": []any{"knowledge"}}})
		}
	})
}

func TestKnowledgeToolboxNeedsRag(t *testing.T) {
	if _, err := KnowledgeToolbox(KnowledgeConfig{}); err == nil {
		t.Fatal("KnowledgeToolbox() error = nil with no rag")
	}
	box, err := KnowledgeToolbox(KnowledgeConfig{Rag: &fakeRag{}})
	if err != nil {
		t.Fatal(err)
	}
	if box.Name != "knowledge" || box.Description == "" || box.Instructions == "" {
		t.Fatalf("box = %+v", box)
	}
	if _, err := newToolCatalog([]Toolbox{box}); err != nil {
		t.Fatalf("newToolCatalog() error = %v", err)
	}
}

func TestKnowledgeSourcesReportsIndexedCoverageAsMember(t *testing.T) {
	last := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	source := &fakeKnowledgeSources{listed: []knowledgeclient.Source{{ID: "s1", Provider: "github", ConnectionID: "c1", Target: map[string]string{"repository": "acme/app"}, Status: "active", LastSyncedAt: &last}}}
	box, err := KnowledgeToolbox(KnowledgeConfig{Rag: &fakeRag{}, Sources: source})
	if err != nil {
		t.Fatal(err)
	}
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		switch {
		case functionResponse(request, "knowledge_sources") != "":
			return genai.NewContentFromText(functionResponse(request, "knowledge_sources"), genai.RoleModel)
		case functionResponse(request, "load_tools") != "":
			return functionCalls(&genai.FunctionCall{ID: "status", Name: "knowledge_sources", Args: map[string]any{"provider": "github", "connection_id": "c1"}})
		default:
			return functionCalls(&genai.FunctionCall{ID: "load", Name: "load_tools", Args: map[string]any{"names": []any{"knowledge"}}})
		}
	})
	h := newHarness(t, llm, box)
	answer := h.turnWith(memberauth.WithAccessToken(context.Background(), "own-token"), "user-1", map[string]any{OrganizationIDKey: "org-1"}, "What is indexed?")
	if source.called != "org-1 own-token github c1" || !strings.Contains(answer, "2026-09-20T10:00:00Z") || !strings.Contains(answer, "acme/app") {
		t.Fatalf("called=%q answer=%q", source.called, answer)
	}
}

func TestKnowledgeRefreshNeedsConfirmationAndIsAbsentUnattended(t *testing.T) {
	sources := &syncableKnowledgeSources{}
	for _, readonly := range []bool{false, true} {
		box, err := KnowledgeToolbox(KnowledgeConfig{Rag: &fakeRag{}, Sources: sources, ReadOnly: readonly})
		if err != nil {
			t.Fatal(err)
		}
		sessions := session.InMemoryService()
		created, err := sessions.Create(context.Background(), &session.CreateRequest{AppName: "test", UserID: "u", State: map[string]any{OrganizationIDKey: "o"}})
		if err != nil {
			t.Fatal(err)
		}
		invocation := &fakeInvocationContext{base: context.Background(), session: created.Session}
		available, err := box.Toolset.Tools(agent.NewContext(invocation))
		if err != nil {
			t.Fatal(err)
		}
		var refresh tool.Tool
		for _, candidate := range available {
			if candidate.Name() == "knowledge_sync_source" {
				refresh = candidate
			}
		}
		if readonly {
			if refresh != nil {
				t.Fatal("unattended knowledge can schedule indexing")
			}
			continue
		}
		if refresh == nil {
			t.Fatal("attended knowledge cannot schedule indexing")
		}
		runnable := refresh.(interface {
			Run(agent.Context, any) (map[string]any, error)
		})
		ctx := agent.NewToolContext(invocation, "call", &session.EventActions{StateDelta: map[string]any{}}, nil)
		if _, err := runnable.Run(ctx, map[string]any{"source_id": "s1"}); !errors.Is(err, tool.ErrConfirmationRequired) {
			t.Fatalf("sync before confirmation = %v", err)
		}
	}
}

func TestKnowledgeSearchesAsTheMemberAndMergesTheBestPassages(t *testing.T) {
	rag := &fakeRag{
		corpora: []ragclient.Corpus{
			{ID: "corpus_empty", Name: "Empty", DocumentCount: 0},
			{ID: "corpus_member", Name: "Mine", DocumentCount: 3},
			{ID: "corpus_org", Name: "Ours", DocumentCount: 9},
		},
		matches: map[string][]ragclient.Match{
			"corpus_member": {match("Launch plan", "https://notion.so/launch", "Launch is on the 3rd.", 0.4, "Plan", "Dates")},
			"corpus_org":    {match("Board notes", "knowledge:doc_9", "The board approved the launch.", 0.9)},
		},
	}
	box, err := KnowledgeToolbox(KnowledgeConfig{Rag: rag, Tokens: fakeTokens{token: "should-not-be-minted"}, MinRelevance: 0.2})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, searchesKnowledgeLLM("launch date"), box)
	ctx := memberauth.WithAccessToken(context.Background(), "member-own-token")
	answer := h.turnWith(ctx, "user-1", map[string]any{OrganizationIDKey: "org-1"}, "When is the launch?")

	searched := rag.searched()
	if len(searched) != 2 {
		t.Fatalf("searches = %v, want the two corpora holding documents", searched)
	}
	for _, call := range searched {
		if !strings.Contains(call, " org-1 ") || !strings.Contains(call, "member-own-token") || !strings.Contains(call, `"launch date"`) {
			t.Fatalf("search = %q, want the member's own token, organization and query", call)
		}
	}
	// The configured floor rides on every search, so a question the
	// documents cannot answer comes back with nothing.
	for _, floor := range rag.minScores {
		if floor != 0.2 {
			t.Fatalf("searched with min_score %v, want the configured 0.2", floor)
		}
	}
	// The organization's passage scored higher, so it comes first; the
	// member's keeps its section and source.
	board := strings.Index(answer, "Board notes")
	launch := strings.Index(answer, "Launch plan")
	if board < 0 || launch < 0 || board > launch {
		t.Fatalf("answer = %q, want both passages, the higher score first", answer)
	}
	if !strings.Contains(answer, "https://notion.so/launch") || !strings.Contains(answer, "Plan › Dates") {
		t.Fatalf("answer = %q, want the passage's source and section", answer)
	}
}

func TestKnowledgeSearchUsesAnAgentTokenWhenTheMemberIsAway(t *testing.T) {
	rag := &fakeRag{
		corpora: []ragclient.Corpus{{ID: "corpus_member", DocumentCount: 1}},
		matches: map[string][]ragclient.Match{"corpus_member": {match("Doc", "", "text", 0.5)}},
	}
	box, err := KnowledgeToolbox(KnowledgeConfig{Rag: rag, Tokens: fakeTokens{token: "agent-token"}})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, searchesKnowledgeLLM("anything"), box)
	h.turn("user-1", map[string]any{OrganizationIDKey: "org-1"}, "Search.")
	if searched := rag.searched(); len(searched) != 1 || !strings.Contains(searched[0], "agent-token") {
		t.Fatalf("searches = %v, want one with the minted agent token", searched)
	}
	// Unset, the floor keeps every passage: nothing is left out on a scale
	// no one calibrated.
	if len(rag.minScores) != 1 || rag.minScores[0] != 0 {
		t.Fatalf("searched with min_score %v, want none by default", rag.minScores)
	}
}

func TestKnowledgeSearchReportsWhatItCouldNotRead(t *testing.T) {
	rag := &fakeRag{
		matches: map[string][]ragclient.Match{"corpus_a": {match("A", "", "found it", 0.7)}},
		failures: map[string]error{
			"corpus_b": ragclient.ErrDenied,
		},
	}
	box, err := KnowledgeToolbox(KnowledgeConfig{Rag: rag})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, searchesKnowledgeLLM("x", "corpus_a", "corpus_b"), box)
	ctx := memberauth.WithAccessToken(context.Background(), "t")
	answer := h.turnWith(ctx, "user-1", map[string]any{OrganizationIDKey: "org-1"}, "Search.")
	if !strings.Contains(answer, "found it") || !strings.Contains(answer, "corpus_b") || !strings.Contains(answer, ragclient.ErrDenied.Error()) {
		t.Fatalf("answer = %q, want the passage found and why the other collection was not read", answer)
	}
}

func TestKnowledgeSearchDisclosesCollectionsBeyondItsCap(t *testing.T) {
	ids := []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9"}
	box, err := KnowledgeToolbox(KnowledgeConfig{Rag: &fakeRag{}})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, searchesKnowledgeLLM("launch", ids...), box)
	answer := h.turnWith(memberauth.WithAccessToken(context.Background(), "t"), "user-1", map[string]any{OrganizationIDKey: "org-1"}, "Search.")
	if !strings.Contains(answer, "c9") || !strings.Contains(answer, "collection limit reached") {
		t.Fatalf("answer = %q, want to disclose the collection not searched", answer)
	}
}

func TestKnowledgeWithoutAMemberSaysSoInPlainWords(t *testing.T) {
	box, err := KnowledgeToolbox(KnowledgeConfig{Rag: &fakeRag{}})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, searchesKnowledgeLLM("x"), box)
	// No organization in the session: there is no member to act for.
	answer := h.turn("user-1", nil, "Search.")
	if !strings.Contains(answer, memberauth.ErrNoMember.Error()) {
		t.Fatalf("answer = %q, want the member error in plain words", answer)
	}
}

func TestPassagesAreBoundedAndTitled(t *testing.T) {
	long := strings.Repeat("a", maxPassageRunes+50)
	passage := passageOf("c", match("", "", long, 1))
	if passage.DocumentTitle != "Untitled document" {
		t.Fatalf("title = %q", passage.DocumentTitle)
	}
	if len([]rune(passage.Text)) != maxPassageRunes+1 || !strings.HasSuffix(passage.Text, "…") {
		t.Fatalf("text runes = %d", len([]rune(passage.Text)))
	}
}

func TestExplainedKeepsKnownWordsAndWrapsTheRest(t *testing.T) {
	if got := explained(fmt.Errorf("wrapped: %w", ragclient.ErrUnavailable)); !errors.Is(got, ragclient.ErrUnavailable) || got.Error() != ragclient.ErrUnavailable.Error() {
		t.Fatalf("explained(unavailable) = %v", got)
	}
	if got := explained(errors.New("rag refused the request: top_k must be at most 100")); !strings.Contains(got.Error(), "top_k") {
		t.Fatalf("explained(refusal) = %v", got)
	}
}
