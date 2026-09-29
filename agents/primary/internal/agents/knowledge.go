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
	"time"
	"unicode/utf8"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/knowledgeclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/ragclient"
	workspacepolicy "github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
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
	Search(ctx context.Context, organizationID, corpusID, token, query string, topK int, minScore float64) ([]ragclient.Match, error)
}

// KnowledgeSources is the member-scoped read-only view of what knowledge is
// indexing. It is separate from RAG, which owns searching, not extraction.
type KnowledgeSources interface {
	Sources(ctx context.Context, organizationID, token, provider, connectionID string) ([]knowledgeclient.Source, error)
}

type KnowledgeSyncer interface {
	SyncSource(ctx context.Context, organizationID, sourceID, token string) (knowledgeclient.Source, error)
}

// KnowledgeConfig configures KnowledgeToolbox.
type KnowledgeConfig struct {
	Rag Knowledge
	// Policy reads the last applied workspace version for every tool call.
	Policy interface {
		Get(context.Context, string) (workspacepolicy.Settings, error)
	}
	// Sources is optional when the knowledge service is not configured.
	Sources KnowledgeSources
	// ReadOnly excludes scheduling an extraction in unattended runs.
	ReadOnly bool
	// Tokens mints an agent token for a session's user when a run carries no
	// member access token of its own. See memberauth.TokenSource.
	Tokens memberauth.TokenSource
	// MinRelevance leaves out passages rag's reranker scored below it, so a
	// question the documents cannot answer comes back empty and Krude says so
	// rather than answering from a near miss. Zero keeps every passage.
	//
	// A reranker's scores are on its own model's scale, so this is set for the
	// reranker rag runs, never carried from one to another. Measured over this
	// repository's own documentation with Google's semantic-ranker-default,
	// unanswerable questions topped out at 0.01-0.23 and answerable ones had a
	// median of 0.68: at 0.2, nine in ten unanswerable questions came back
	// empty, one answerable question in sixty did too, and Krude read a
	// quarter of the text. Without a reranker it never bites: fused scores in
	// the top results sit far above any such floor.
	MinRelevance float64
}

// KnowledgeToolbox names the toolbox that searches the organization's own
// documents in rag — the member's own collections and their organization's —
// as the member, so rag decides what they may read. It answers with
// passages and where each came from, for Krude to cite.
func KnowledgeToolbox(config KnowledgeConfig) (Toolbox, error) {
	if config.Rag == nil {
		return Toolbox{}, errors.New("agents: KnowledgeToolbox requires rag")
	}
	knowledge := knowledgeTools{rag: config.Rag, sources: config.Sources, tokens: config.Tokens, policy: config.Policy, minRelevance: config.MinRelevance}
	collections, err := functiontool.New(functiontool.Config{
		Name:        "knowledge_collections",
		Description: "Lists the collections of documents the member can search: their own and their organization's, with how many documents each holds.",
	}, knowledge.collections)
	if err != nil {
		return Toolbox{}, err
	}
	search, err := functiontool.New(functiontool.Config{
		Name:        "knowledge_search",
		Description: "Searches the organization's own documents — what the member and their organization indexed, including what their connected apps hold — and returns the passages that best match, each with its document's title, source, provider and classification to cite.",
	}, knowledge.search)
	if err != nil {
		return Toolbox{}, err
	}
	tools := []tool.Tool{collections, search}
	if config.Sources != nil {
		status, err := functiontool.New(functiontool.Config{
			Name:        "knowledge_sources",
			Description: "Lists connected-app targets and files registered for indexing, with each source's status and last completed extraction. Read this to assess coverage and freshness before relying on the index for recent events.",
		}, knowledge.sourcesStatus)
		if err != nil {
			return Toolbox{}, err
		}
		tools = append(tools, status)
		if syncer, ok := config.Sources.(KnowledgeSyncer); ok && !config.ReadOnly {
			knowledge.syncer = syncer
			syncTool, err := functiontool.New(functiontool.Config{
				Name: "knowledge_sync_source", RequireConfirmation: true,
				Description: "After member confirmation, schedules a new extraction for one existing source the member may manage; does not wait for extraction or RAG ingestion.",
			}, knowledge.syncSource)
			if err != nil {
				return Toolbox{}, err
			}
			tools = append(tools, syncTool)
		}
	}
	return Toolbox{
		Name:         KnowledgeToolboxName,
		Description:  "The organization's own documents — the member's and their organization's collections, including what their connected apps hold — searched for passages to answer from and cite.",
		Instructions: knowledgeInstructions,
		Toolset:      fixedTools{name: KnowledgeToolboxName, tools: tools},
	}, nil
}

type knowledgeTools struct {
	rag     Knowledge
	sources KnowledgeSources
	syncer  KnowledgeSyncer
	tokens  memberauth.TokenSource
	policy  interface {
		Get(context.Context, string) (workspacepolicy.Settings, error)
	}
	minRelevance float64
}

func (knowledge knowledgeTools) allowSearch(ctx context.Context, organizationID string) error {
	if knowledge.policy == nil {
		return nil
	}
	settings, err := knowledge.policy.Get(ctx, organizationID)
	if err != nil {
		return fmt.Errorf("workspace search policy unavailable: %w", err)
	}
	if settings.SearchMode == "none" {
		return errors.New("knowledge tools are disabled for this organization")
	}
	return nil
}

const maxSourcesListed = 50

type sourcesArgs struct {
	Provider     string `json:"provider,omitempty" jsonschema:"Optional source provider, such as github, notion, slack, googledrive, gmail or files."`
	ConnectionID string `json:"connection_id,omitempty" jsonschema:"Optional connection ID, when checking coverage for a particular connected app."`
}

type sourceStatus struct {
	ID           string            `json:"id"`
	Provider     string            `json:"provider"`
	ConnectionID string            `json:"connection_id,omitempty"`
	Target       map[string]string `json:"target"`
	Status       string            `json:"status"`
	Reason       string            `json:"reason,omitempty"`
	SyncAt       *time.Time        `json:"sync_at,omitempty"`
	LastSyncedAt *time.Time        `json:"last_synced_at,omitempty"`
}

type sourcesResult struct {
	Sources   []sourceStatus `json:"sources"`
	Total     int            `json:"total"`
	Truncated bool           `json:"truncated"`
}

type syncSourceArgs struct {
	SourceID string `json:"source_id" jsonschema:"The existing indexing source ID returned by knowledge_sources."`
}

func (knowledge knowledgeTools) syncSource(ctx agent.Context, args syncSourceArgs) (sourceStatus, error) {
	if strings.TrimSpace(args.SourceID) == "" {
		return sourceStatus{}, errors.New("name a source returned by knowledge_sources")
	}
	member, token, err := memberauth.Bearer(ctx, knowledge.tokens)
	if err != nil {
		return sourceStatus{}, explained(err)
	}
	if err := knowledge.allowSearch(ctx, member.OrganizationID); err != nil {
		return sourceStatus{}, err
	}
	source, err := knowledge.syncer.SyncSource(ctx, member.OrganizationID, strings.TrimSpace(args.SourceID), token)
	if err != nil {
		return sourceStatus{}, explained(err)
	}
	return statusOf(source), nil
}

func (knowledge knowledgeTools) sourcesStatus(ctx agent.Context, args sourcesArgs) (sourcesResult, error) {
	member, token, err := memberauth.Bearer(ctx, knowledge.tokens)
	if err != nil {
		return sourcesResult{}, explained(err)
	}
	if err := knowledge.allowSearch(ctx, member.OrganizationID); err != nil {
		return sourcesResult{}, err
	}
	listed, err := knowledge.sources.Sources(ctx, member.OrganizationID, token, strings.TrimSpace(args.Provider), strings.TrimSpace(args.ConnectionID))
	if err != nil {
		return sourcesResult{}, explained(err)
	}
	result := sourcesResult{Sources: make([]sourceStatus, 0, min(len(listed), maxSourcesListed)), Total: len(listed), Truncated: len(listed) > maxSourcesListed}
	for _, source := range listed[:min(len(listed), maxSourcesListed)] {
		result.Sources = append(result.Sources, statusOf(source))
	}
	return result, nil
}

func statusOf(source knowledgeclient.Source) sourceStatus {
	return sourceStatus{
		ID: source.ID, Provider: source.Provider, ConnectionID: source.ConnectionID,
		Target: source.Target, Status: source.Status, Reason: source.Reason,
		SyncAt: source.SyncAt, LastSyncedAt: source.LastSyncedAt,
	}
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
	if err := knowledge.allowSearch(ctx, member.OrganizationID); err != nil {
		return collectionsResult{}, err
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
	Provider      string  `json:"provider,omitempty"`
	DocumentKind  string  `json:"document_kind,omitempty"`
	Category      string  `json:"category,omitempty"`
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
	if err := knowledge.allowSearch(ctx, member.OrganizationID); err != nil {
		return searchResult{}, err
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
	var result searchResult
	if len(collectionIDs) > maxCollectionsSearched {
		for _, id := range collectionIDs[maxCollectionsSearched:] {
			// A cap is not proof that these collections held no answer.
			// Surface it to the agent so it can narrow and search again.
			result.Unsearched = append(result.Unsearched, Unsearched{CollectionID: id, Reason: "collection limit reached; search this collection explicitly"})
		}
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
			matches, err := knowledge.rag.Search(ctx, member.OrganizationID, collectionID, token, query, limit, knowledge.minRelevance)
			results[index] = found{collectionID: collectionID, matches: matches, err: err}
		})
	}
	group.Wait()

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
	// Reranked, every corpus's scores come from one model reading the same
	// question, so merging by score keeps the strongest passages overall.
	// Without a reranker a score is a position within its own corpus, and the
	// merge interleaves the corpora by rank.
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
		Provider:      match.Document.Metadata["provider"],
		DocumentKind:  match.Document.Metadata["document_kind"],
		Category:      match.Document.Metadata["category"],
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
	for _, known := range []error{ragclient.ErrDenied, ragclient.ErrNotFound, ragclient.ErrUnavailable, knowledgeclient.ErrDenied, knowledgeclient.ErrUnavailable, knowledgeclient.ErrNeedsReconnect, knowledgeclient.ErrNotFound, knowledgeclient.ErrConflict, context.DeadlineExceeded, context.Canceled} {
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
