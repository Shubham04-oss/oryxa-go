package agents

import (
	"cmp"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/ragclient"
)

// The knowledge toolbox ships with its skill, carried in Krude's system
// prompt while the toolbox is loaded, as every toolbox's is.
//
//go:embed toolboxes/knowledge.md
var knowledgeInstructions string

const (
	// defaultPassages and maxPassages bound what one search returns.
	defaultPassages = 8
	maxPassages     = 20
	// maxCollectionsSearched bounds how many collections one search reads
	// when the model names none, so a member with many does not make one
	// question cost many searches.
	maxCollectionsSearched = 8
	// maxPassageRunes bounds each passage's text, so a search's result stays
	// a small part of the model's context.
	maxPassageRunes = 1500
)

// Knowledge is what the knowledge toolbox asks rag. ragclient.Client
// implements it.
type Knowledge interface {
	Corpora(ctx context.Context, organizationID, token string) ([]ragclient.Corpus, error)
	Search(ctx context.Context, organizationID, corpusID, token, query string, topK int) ([]ragclient.Match, error)
}

// KnowledgeConfig configures KnowledgeToolbox.
type KnowledgeConfig struct {
	Rag Knowledge
	// Tokens mints an agent token for a session's user when a run carries no
	// member access token of its own. See memberauth.TokenSource.
	Tokens memberauth.TokenSource
}

// KnowledgeToolbox names the toolbox that searches the organization's own
// documents in rag — the member's own collections and their organization's —
// as the member, so rag decides what they may read. It answers with
// passages and where each came from, for Krude to cite.
func KnowledgeToolbox(config KnowledgeConfig) (Toolbox, error) {
	if config.Rag == nil {
		return Toolbox{}, errors.New("agents: KnowledgeToolbox requires rag")
	}
	knowledge := knowledgeTools{rag: config.Rag, tokens: config.Tokens}
	collections, err := functiontool.New(functiontool.Config{
		Name:        "knowledge_collections",
		Description: "Lists the collections of documents the member can search: their own and their organization's, with how many documents each holds.",
	}, knowledge.collections)
	if err != nil {
		return Toolbox{}, err
	}
	search, err := functiontool.New(functiontool.Config{
		Name:        "knowledge_search",
		Description: "Searches the organization's own documents — what the member and their organization indexed, including what their connected apps hold — and returns the passages that best match, each with its document's title and source to cite.",
	}, knowledge.search)
	if err != nil {
		return Toolbox{}, err
	}
	return Toolbox{
		Name:         "knowledge",
		Description:  "The organization's own documents — the member's and their organization's collections, including what their connected apps hold — searched for passages to answer from and cite.",
		Instructions: knowledgeInstructions,
		Toolset:      fixedTools{name: "knowledge", tools: []tool.Tool{collections, search}},
	}, nil
}

type knowledgeTools struct {
	rag    Knowledge
	tokens memberauth.TokenSource
}

type collectionsArgs struct{}

// Collection is one searchable collection, as the model sees it.
type Collection struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	Documents int    `json:"documents"`
}

type collectionsResult struct {
	Collections []Collection `json:"collections"`
}

func (knowledge knowledgeTools) collections(ctx agent.Context, _ collectionsArgs) (collectionsResult, error) {
	member, token, err := memberauth.Bearer(ctx, knowledge.tokens)
	if err != nil {
		return collectionsResult{}, explained(err)
	}
	corpora, err := knowledge.rag.Corpora(ctx, member.OrganizationID, token)
	if err != nil {
		return collectionsResult{}, explained(err)
	}
	listed := make([]Collection, 0, len(corpora))
	for _, corpus := range corpora {
		listed = append(listed, Collection{ID: corpus.ID, Name: corpus.Name, Scope: corpus.Scope, Documents: corpus.DocumentCount})
	}
	return collectionsResult{Collections: listed}, nil
}

type searchArgs struct {
	Query         string   `json:"query" jsonschema:"What to look for, in the words the documents would use."`
	CollectionIDs []string `json:"collection_ids,omitempty" jsonschema:"Collections to search, from knowledge_collections. Leave empty to search the member's own and their organization's."`
	Limit         int      `json:"limit,omitempty" jsonschema:"How many passages to return; 8 by default, at most 20."`
}

// Passage is one passage a search found, with where it came from.
type Passage struct {
	CollectionID  string  `json:"collection_id"`
	DocumentTitle string  `json:"document_title"`
	Source        string  `json:"source,omitempty"`
	Section       string  `json:"section,omitempty"`
	Text          string  `json:"text"`
	Score         float64 `json:"score"`
}

// Unsearched is a collection a search could not read, and why.
type Unsearched struct {
	CollectionID string `json:"collection_id"`
	Reason       string `json:"reason"`
}

type searchResult struct {
	Passages []Passage `json:"passages"`
	// Searched is how many collections were read.
	Searched   int          `json:"searched"`
	Unsearched []Unsearched `json:"unsearched,omitempty"`
}

func (knowledge knowledgeTools) search(ctx agent.Context, args searchArgs) (searchResult, error) {
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return searchResult{}, errors.New("say what to look for")
	}
	limit := args.Limit
	if limit <= 0 {
		limit = defaultPassages
	}
	limit = min(limit, maxPassages)

	member, token, err := memberauth.Bearer(ctx, knowledge.tokens)
	if err != nil {
		return searchResult{}, explained(err)
	}
	collectionIDs := slices.Compact(slices.Sorted(slices.Values(args.CollectionIDs)))
	if len(collectionIDs) == 0 {
		corpora, err := knowledge.rag.Corpora(ctx, member.OrganizationID, token)
		if err != nil {
			return searchResult{}, explained(err)
		}
		// The fullest collections first, so a cap leaves out the ones least
		// likely to hold the answer.
		slices.SortStableFunc(corpora, func(a, b ragclient.Corpus) int { return cmp.Compare(b.DocumentCount, a.DocumentCount) })
		for _, corpus := range corpora {
			if corpus.DocumentCount > 0 {
				collectionIDs = append(collectionIDs, corpus.ID)
			}
		}
		if len(collectionIDs) == 0 {
			return searchResult{Passages: []Passage{}}, nil
		}
	}
	if len(collectionIDs) > maxCollectionsSearched {
		collectionIDs = collectionIDs[:maxCollectionsSearched]
	}

	type found struct {
		collectionID string
		matches      []ragclient.Match
		err          error
	}
	results := make([]found, len(collectionIDs))
	var group sync.WaitGroup
	for index, collectionID := range collectionIDs {
		group.Go(func() {
			matches, err := knowledge.rag.Search(ctx, member.OrganizationID, collectionID, token, query, limit)
			results[index] = found{collectionID: collectionID, matches: matches, err: err}
		})
	}
	group.Wait()

	var result searchResult
	var firstErr error
	for _, searched := range results {
		if searched.err != nil {
			firstErr = cmp.Or(firstErr, searched.err)
			result.Unsearched = append(result.Unsearched, Unsearched{CollectionID: searched.collectionID, Reason: explained(searched.err).Error()})
			continue
		}
		result.Searched++
		for _, match := range searched.matches {
			result.Passages = append(result.Passages, passageOf(searched.collectionID, match))
		}
	}
	if result.Searched == 0 && firstErr != nil {
		return searchResult{}, explained(firstErr)
	}
	// Rag ranks every corpus's matches the same way, so merging them by
	// score keeps the strongest passages overall.
	slices.SortStableFunc(result.Passages, func(a, b Passage) int { return cmp.Compare(b.Score, a.Score) })
	if len(result.Passages) > limit {
		result.Passages = result.Passages[:limit]
	}
	if result.Passages == nil {
		result.Passages = []Passage{}
	}
	return result, nil
}

func passageOf(collectionID string, match ragclient.Match) Passage {
	text := strings.TrimSpace(match.Chunk.Text)
	if utf8.RuneCountInString(text) > maxPassageRunes {
		text = string([]rune(text)[:maxPassageRunes]) + "…"
	}
	title := strings.TrimSpace(match.Document.Title)
	if title == "" {
		title = "Untitled document"
	}
	return Passage{
		CollectionID:  collectionID,
		DocumentTitle: title,
		Source:        match.Document.Source.Ref,
		Section:       strings.Join(match.Chunk.HeadingPath, " › "),
		Text:          text,
		Score:         match.Score,
	}
}

// explained is what the model is told about a failure: the plain words a
// known one carries, and a generic sentence for the rest.
func explained(err error) error {
	if known := memberauth.Explain(err); known != nil {
		return known
	}
	for _, known := range []error{ragclient.ErrDenied, ragclient.ErrNotFound, ragclient.ErrUnavailable, context.DeadlineExceeded, context.Canceled} {
		if errors.Is(err, known) {
			return known
		}
	}
	return fmt.Errorf("the knowledge search failed: %w", err)
}

// fixedTools is a toolset whose tools never change.
type fixedTools struct {
	name  string
	tools []tool.Tool
}

func (tools fixedTools) Name() string { return tools.name }

func (tools fixedTools) Tools(agent.ReadonlyContext) ([]tool.Tool, error) {
	return slices.Clone(tools.tools), nil
}
