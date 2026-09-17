// Package v1 contains the stable version-one events emitted by rag.
//
// They carry identifiers, counts and outcomes. They never carry chunk text,
// embeddings or credentials: a subscriber that needs what a document says
// should ask rag, so there remains one answer to that question and one place
// that decides who may have it.
package v1

import "time"

const (
	Source = "oryxa.rag"

	CorpusCreatedDetailType    = "rag.corpus.created.v1"
	CorpusDeletedDetailType    = "rag.corpus.deleted.v1"
	DocumentReceivedDetailType = "rag.document.received.v1"
	DocumentIndexedDetailType  = "rag.document.indexed.v1"
	DocumentFailedDetailType   = "rag.document.failed.v1"
	DocumentRemovedDetailType  = "rag.document.removed.v1"
)

// CorpusCreated announces a searchable collection and the vector space it
// pinned. The spec is included because it cannot change afterwards, so a
// subscriber can rely on it.
type CorpusCreated struct {
	CorpusID       string    `json:"corpus_id"`
	OrganizationID string    `json:"organization_id"`
	Slug           string    `json:"slug"`
	Name           string    `json:"name"`
	Scope          string    `json:"scope"`
	TeamID         string    `json:"team_id,omitempty"`
	MemberID       string    `json:"member_id,omitempty"`
	Provider       string    `json:"embedding_provider"`
	Model          string    `json:"embedding_model"`
	Dimensions     int       `json:"embedding_dimensions"`
	Metric         string    `json:"embedding_metric"`
	Backend        string    `json:"backend"`
	CreatedBy      string    `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
}

type CorpusDeleted struct {
	CorpusID       string    `json:"corpus_id"`
	OrganizationID string    `json:"organization_id"`
	Scope          string    `json:"scope"`
	TeamID         string    `json:"team_id,omitempty"`
	MemberID       string    `json:"member_id,omitempty"`
	Reason         string    `json:"reason"`
	DeletedBy      string    `json:"deleted_by,omitempty"`
	DeletedAt      time.Time `json:"deleted_at"`
}

// DocumentReceived means a document was accepted and is queued for indexing.
// It is not yet searchable; DocumentIndexed says when it is.
type DocumentReceived struct {
	DocumentID     string    `json:"document_id"`
	CorpusID       string    `json:"corpus_id"`
	OrganizationID string    `json:"organization_id"`
	SourceKind     string    `json:"source_kind"`
	SourceRef      string    `json:"source_ref,omitempty"`
	ContentType    string    `json:"content_type"`
	ContentSHA256  string    `json:"content_sha256"`
	ByteSize       int64     `json:"byte_size"`
	CreatedBy      string    `json:"created_by"`
	ReceivedAt     time.Time `json:"received_at"`
}

// DocumentIndexed is what a subscriber waits for: the document is searchable.
//
// EmbeddedChunks is the part of the work that cost money; ReusedChunks is what
// an unchanged re-delivery saved. Both are here because the difference is the
// main thing worth watching about an ingest pipeline's bill.
type DocumentIndexed struct {
	DocumentID     string    `json:"document_id"`
	CorpusID       string    `json:"corpus_id"`
	OrganizationID string    `json:"organization_id"`
	ChunkCount     int       `json:"chunk_count"`
	EmbeddedChunks int       `json:"embedded_chunks"`
	ReusedChunks   int       `json:"reused_chunks"`
	RemovedChunks  int       `json:"removed_chunks"`
	Provider       string    `json:"embedding_provider"`
	Model          string    `json:"embedding_model"`
	Tokens         int       `json:"embedding_tokens"`
	IndexedAt      time.Time `json:"indexed_at"`
}

// DocumentFailed says a document could not be indexed and why. The row stays,
// so it can be seen and retried rather than quietly disappearing.
type DocumentFailed struct {
	DocumentID     string    `json:"document_id"`
	CorpusID       string    `json:"corpus_id"`
	OrganizationID string    `json:"organization_id"`
	Reason         string    `json:"reason"`
	Attempts       int       `json:"attempts"`
	FailedAt       time.Time `json:"failed_at"`
}

type DocumentRemoved struct {
	DocumentID     string    `json:"document_id"`
	CorpusID       string    `json:"corpus_id"`
	OrganizationID string    `json:"organization_id"`
	SourceKind     string    `json:"source_kind"`
	SourceRef      string    `json:"source_ref,omitempty"`
	Reason         string    `json:"reason"`
	DeletedBy      string    `json:"deleted_by,omitempty"`
	RemovedAt      time.Time `json:"removed_at"`
}

// Why a corpus or a document went away.
const (
	ReasonDeleted                     = "deleted"
	ReasonSourceDeleted               = "source_deleted"
	ReasonCorpusDeleted               = "corpus_deleted"
	ReasonOrganizationDeleted         = "organization_deleted"
	ReasonOrganizationMembershipEnded = "organization_membership_ended"
	ReasonTeamDeleted                 = "team_deleted"
)
