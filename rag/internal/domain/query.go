package domain

import (
	"fmt"
	"slices"
	"strings"
)

// SearchMode is which retrievers run for a query.
type SearchMode string

const (
	// ModeHybrid runs the vector search and the lexical search and fuses
	// their rankings. It is the default because the two fail differently:
	// vectors miss an exact identifier, error code or surname, and lexical
	// search misses a question worded nothing like the document.
	ModeHybrid SearchMode = "hybrid"
	// ModeDense is the vector search alone.
	ModeDense SearchMode = "dense"
	// ModeLexical is PostgreSQL full-text search alone, with no embedding
	// call. It is the one mode that costs nothing per query.
	ModeLexical SearchMode = "lexical"
)

func (mode SearchMode) Valid() bool {
	switch mode {
	case ModeHybrid, ModeDense, ModeLexical:
		return true
	default:
		return false
	}
}

// UsesDense reports whether the mode needs the query embedded.
func (mode SearchMode) UsesDense() bool { return mode == ModeHybrid || mode == ModeDense }

// UsesLexical reports whether the mode needs the text index.
func (mode SearchMode) UsesLexical() bool { return mode == ModeHybrid || mode == ModeLexical }

// Operator is how one metadata condition compares. The set is deliberately
// small: everything here maps onto both a PostgreSQL jsonb predicate and an
// S3 Vectors metadata filter, so a filtered query means the same thing
// whichever backend the corpus was written into.
type Operator string

const (
	OperatorEqual    Operator = "eq"
	OperatorNotEqual Operator = "ne"
	OperatorIn       Operator = "in"
	OperatorNotIn    Operator = "nin"
)

func (operator Operator) Valid() bool {
	switch operator {
	case OperatorEqual, OperatorNotEqual, OperatorIn, OperatorNotIn:
		return true
	default:
		return false
	}
}

// MaxFilterValues bounds one condition, so a filter cannot become a way to
// send an unbounded list into either backend.
const MaxFilterValues = 64

// Condition is one metadata test. Conditions are combined with AND.
type Condition struct {
	Key      string   `json:"key"`
	Operator Operator `json:"operator"`
	Values   []string `json:"values"`
}

func (condition Condition) Validate() error {
	if !metadataKeyPattern.MatchString(condition.Key) {
		return fmt.Errorf("filter key %q is not a metadata key: %w", condition.Key, ErrInvalid)
	}
	if !condition.Operator.Valid() {
		return fmt.Errorf("filter operator %q is not allowed: %w", condition.Operator, ErrInvalid)
	}
	if len(condition.Values) == 0 {
		return fmt.Errorf("filter on %q needs at least one value: %w", condition.Key, ErrInvalid)
	}
	if len(condition.Values) > MaxFilterValues {
		return fmt.Errorf("filter on %q may list at most %d values: %w", condition.Key, MaxFilterValues, ErrInvalid)
	}
	if condition.Operator == OperatorEqual || condition.Operator == OperatorNotEqual {
		if len(condition.Values) != 1 {
			return fmt.Errorf("filter %q on %q takes exactly one value: %w", condition.Operator, condition.Key, ErrInvalid)
		}
	}
	for _, value := range condition.Values {
		if len(value) > MaxMetadataValue {
			return fmt.Errorf("filter value on %q must be at most %d bytes: %w", condition.Key, MaxMetadataValue, ErrInvalid)
		}
	}
	return nil
}

// Filter narrows a search before ranking. Organization scoping is not in here
// on purpose: it is applied by the store from the caller's membership, so no
// caller can widen it by leaving a field out.
type Filter struct {
	DocumentIDs []string    `json:"document_ids,omitempty"`
	Conditions  []Condition `json:"conditions,omitempty"`
}

// MaxFilterDocuments bounds the document allowlist on a query.
const MaxFilterDocuments = 128

func (filter Filter) Validate() error {
	if len(filter.DocumentIDs) > MaxFilterDocuments {
		return fmt.Errorf("a query may name at most %d documents: %w", MaxFilterDocuments, ErrInvalid)
	}
	for _, id := range filter.DocumentIDs {
		if !strings.HasPrefix(id, DocumentIDPrefix) {
			return fmt.Errorf("%q is not a document id: %w", id, ErrInvalid)
		}
	}
	if len(filter.Conditions) > MaxMetadataKeys {
		return fmt.Errorf("a query may carry at most %d conditions: %w", MaxMetadataKeys, ErrInvalid)
	}
	for _, condition := range filter.Conditions {
		if err := condition.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (filter Filter) Empty() bool {
	return len(filter.DocumentIDs) == 0 && len(filter.Conditions) == 0
}

// Query bounds. TopK is what comes back; the over-fetch is how many
// candidates each retriever is asked for before fusion, reranking and
// diversification cut them down. Fusing two lists of exactly TopK would throw
// away the documents that only one retriever ranked well, which is the whole
// point of running both.
const (
	MaxTopK            = 100
	DefaultTopK        = 8
	DefaultOverFetch   = 4
	MaxCandidates      = 400
	DefaultMMRLambda   = 0.5
	MaxQueryCharacters = 4096
)

// Query is one search against one corpus.
type Query struct {
	// IncludeAdminOnly is assigned by the application from the caller's
	// verified membership (see OrgMembership.ActsAsAdmin): an org admin's
	// own session, never an agent acting for one. It is never accepted from
	// a request body.
	IncludeAdminOnly bool       `json:"-"`
	Text             string     `json:"text"`
	TopK             int        `json:"top_k,omitempty"`
	Mode             SearchMode `json:"mode,omitempty"`
	// Filter narrows the candidates before ranking.
	Filter Filter `json:"filter,omitempty"`
	// MinScore drops weak matches. It is compared against the fused score, so
	// it is a knob for "answer nothing rather than answer badly".
	MinScore float64 `json:"min_score,omitempty"`
	// Diversity is the MMR trade-off: 1 keeps pure relevance, 0 maximises
	// spread. It matters when a corpus holds near-duplicates, where the top
	// results would otherwise be the same passage from five documents.
	//
	// It is a pointer because 0 is a meaningful setting and would otherwise be
	// indistinguishable from not asking, which Normalize turns into 1 — the
	// opposite of what was asked for. Normalize always leaves it set.
	Diversity *float64 `json:"diversity,omitempty"`
	// Rerank sends the fused candidates through a cross-encoder before the
	// cut to TopK. It is the largest quality gain available per query, and
	// also the largest latency cost, so it is opt-in.
	Rerank bool `json:"rerank,omitempty"`
	// OverFetch multiplies TopK to size the candidate pool.
	OverFetch int `json:"over_fetch,omitempty"`
}

// Normalize fills in the defaults and refuses a query that cannot be served.
func (query Query) Normalize() (Query, error) {
	query.Text = strings.TrimSpace(query.Text)
	if query.Text == "" {
		return Query{}, fmt.Errorf("query text is required: %w", ErrInvalid)
	}
	if len(query.Text) > MaxQueryCharacters {
		return Query{}, fmt.Errorf("query text must be at most %d characters: %w", MaxQueryCharacters, ErrInvalid)
	}
	if query.Mode == "" {
		query.Mode = ModeHybrid
	}
	if !query.Mode.Valid() {
		return Query{}, fmt.Errorf("search mode is not allowed: %w", ErrInvalid)
	}
	if query.TopK == 0 {
		query.TopK = DefaultTopK
	}
	if query.TopK < 1 || query.TopK > MaxTopK {
		return Query{}, fmt.Errorf("top_k must be between 1 and %d: %w", MaxTopK, ErrInvalid)
	}
	if query.OverFetch == 0 {
		query.OverFetch = DefaultOverFetch
	}
	if query.OverFetch < 1 || query.OverFetch > 20 {
		return Query{}, fmt.Errorf("over_fetch must be between 1 and 20: %w", ErrInvalid)
	}
	if query.Diversity == nil {
		relevance := 1.0
		query.Diversity = &relevance
	}
	if *query.Diversity < 0 || *query.Diversity > 1 {
		return Query{}, fmt.Errorf("diversity must be between 0 and 1: %w", ErrInvalid)
	}
	if query.MinScore < 0 || query.MinScore > 1 {
		return Query{}, fmt.Errorf("min_score must be between 0 and 1: %w", ErrInvalid)
	}
	if err := query.Filter.Validate(); err != nil {
		return Query{}, err
	}
	return query, nil
}

// Candidates is how many rows each retriever is asked for.
func (query Query) Candidates() int {
	return min(query.TopK*query.OverFetch, MaxCandidates)
}

// ScoredChunk is one retriever's opinion: a chunk and how well it did. It is
// deliberately thin, because the retrievers disagree about what a score means
// and fusion ranks them rather than adding them up.
type ScoredChunk struct {
	ChunkID string
	Score   float64
}

// Match is one retrieved chunk and why it is here. The ranks are kept so a
// result can be explained: a chunk that only the lexical side found looks
// very different from one both retrievers agreed on, and that difference is
// the first thing anyone debugging retrieval wants to see.
type Match struct {
	Chunk    Chunk    `json:"chunk"`
	Document Ref      `json:"document"`
	Score    float64  `json:"score"`
	Dense    *float64 `json:"dense_score,omitempty"`
	Lexical  *float64 `json:"lexical_score,omitempty"`
	Rerank   *float64 `json:"rerank_score,omitempty"`
	// DenseRank and LexicalRank are one-based positions in each retriever's
	// list, or zero when that retriever did not return this chunk.
	DenseRank   int `json:"dense_rank,omitempty"`
	LexicalRank int `json:"lexical_rank,omitempty"`
}

// Ref is the little about a document that travels with every match, so a
// citation can be rendered without a second lookup.
type Ref struct {
	ID       string   `json:"id"`
	Title    string   `json:"title,omitempty"`
	Source   Source   `json:"source"`
	Metadata Metadata `json:"metadata,omitempty"`
}

// Citation is one passage an answer leaned on.
type Citation struct {
	Index       int      `json:"index"`
	ChunkID     string   `json:"chunk_id"`
	DocumentID  string   `json:"document_id"`
	Title       string   `json:"title,omitempty"`
	Source      Source   `json:"source"`
	Metadata    Metadata `json:"metadata,omitempty"`
	HeadingPath []string `json:"heading_path,omitempty"`
	StartByte   int      `json:"start_byte"`
	EndByte     int      `json:"end_byte"`
	Score       float64  `json:"score"`
}

// Answer is a grounded response: the text, what it cited, and what it cost.
type Answer struct {
	Text      string     `json:"text"`
	Citations []Citation `json:"citations"`
	Model     string     `json:"model"`
	Usage     Usage      `json:"usage"`
	// Grounded is false when retrieval found nothing worth answering from.
	// The service says so rather than letting the model answer from memory,
	// because an ungrounded answer in a retrieval product is indistinguishable
	// from a correct one until someone checks it.
	Grounded bool `json:"grounded"`
}

// Usage is what one provider call consumed, for cost accounting.
type Usage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
	// EmbeddedTexts and EmbeddingTokens account for embedding work, which is
	// billed per token but not per completion.
	EmbeddedTexts   int `json:"embedded_texts,omitempty"`
	EmbeddingTokens int `json:"embedding_tokens,omitempty"`
}

// Add accumulates usage across the calls one request made.
func (usage *Usage) Add(other Usage) {
	usage.InputTokens += other.InputTokens
	usage.OutputTokens += other.OutputTokens
	usage.EmbeddedTexts += other.EmbeddedTexts
	usage.EmbeddingTokens += other.EmbeddingTokens
}

// Citations turns matches into citations in the order they were given to the
// model, which is the order their indexes refer to.
func Citations(matches []Match) []Citation {
	citations := make([]Citation, 0, len(matches))
	for position, match := range matches {
		citations = append(citations, Citation{
			Index:       position + 1,
			ChunkID:     match.Chunk.ID,
			DocumentID:  match.Chunk.DocumentID,
			Title:       match.Document.Title,
			Source:      match.Document.Source,
			Metadata:    match.Document.Metadata,
			HeadingPath: slices.Clone(match.Chunk.HeadingPath),
			StartByte:   match.Chunk.StartByte,
			EndByte:     match.Chunk.EndByte,
			Score:       match.Score,
		})
	}
	return citations
}
