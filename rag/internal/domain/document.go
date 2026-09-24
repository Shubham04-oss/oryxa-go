package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// SourceKind says where a document's text came from, which is what makes a
// citation checkable afterwards.
type SourceKind string

const (
	// SourceInline is text handed straight to this service. It is what an
	// upstream extract-and-transform step delivers once it has turned a PDF,
	// a page or a row into text.
	SourceInline SourceKind = "inline"
	// SourceFile is an object in the files service, fetched as the caller.
	// Rag never holds AWS credentials for that bucket; it asks files.
	SourceFile SourceKind = "file"
	// SourceExternal is text that came from somewhere outside Oryxa, kept
	// with the URI it came from so a citation can point back at it.
	SourceExternal SourceKind = "external"
)

func (kind SourceKind) Valid() bool {
	switch kind {
	case SourceInline, SourceFile, SourceExternal:
		return true
	default:
		return false
	}
}

// Source names one document's origin. Ref is the file id for SourceFile and
// the URI for SourceExternal; inline text has no ref.
type Source struct {
	Kind SourceKind `json:"kind"`
	Ref  string     `json:"ref,omitempty"`
}

func (source Source) Validate() error {
	if !source.Kind.Valid() {
		return fmt.Errorf("source kind is not allowed: %w", ErrInvalid)
	}
	ref := strings.TrimSpace(source.Ref)
	switch source.Kind {
	case SourceInline:
		if ref != "" {
			return fmt.Errorf("inline documents carry no source reference: %w", ErrInvalid)
		}
	case SourceFile:
		if !strings.HasPrefix(ref, "file_") {
			return fmt.Errorf("a file source must name a files object id: %w", ErrInvalid)
		}
	case SourceExternal:
		if ref == "" {
			return fmt.Errorf("an external source must name where it came from: %w", ErrInvalid)
		}
		if len(ref) > 2048 {
			return fmt.Errorf("an external source reference must be at most 2048 characters: %w", ErrInvalid)
		}
	}
	return nil
}

// IngestStatus is where a document has got to. It is what a caller polls and
// what makes a partially indexed corpus legible rather than mysterious.
type IngestStatus string

const (
	// StatusPending means the document is recorded and its work is queued.
	StatusPending IngestStatus = "pending"
	// StatusIngesting means a worker holds it: text fetched, chunks being
	// embedded and written.
	StatusIngesting IngestStatus = "ingesting"
	// StatusReady means every chunk is embedded and indexed.
	StatusReady IngestStatus = "ready"
	// StatusFailed means ingestion gave up. Failure is kept with its reason
	// rather than deleting the row, so it can be seen and retried.
	StatusFailed IngestStatus = "failed"
)

func (status IngestStatus) Valid() bool {
	switch status {
	case StatusPending, StatusIngesting, StatusReady, StatusFailed:
		return true
	default:
		return false
	}
}

func (status IngestStatus) Terminal() bool {
	return status == StatusReady || status == StatusFailed
}

// MaxDocumentBytes bounds one document's text. Beyond this the caller should
// be splitting the source up, not handing rag a book in one request.
const MaxDocumentBytes = 16 << 20

// textContentTypes are what this service can chunk.
//
// Extraction is deliberately not here. Turning a PDF, a spreadsheet or a web
// page into text is the upstream pipeline's job, and doing it in two places
// would mean two answers about what a document says. Rag takes text and is
// strict about it, so a caller finds out at ingest rather than discovering
// later that it indexed a binary header.
var textContentTypes = []string{
	"text/plain", "text/markdown", "text/html", "text/csv", "application/json",
}

func ContentType(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(strings.Split(raw, ";")[0]))
	if value == "" {
		value = "text/plain"
	}
	if !slices.Contains(textContentTypes, value) {
		return "", fmt.Errorf("content type %q cannot be indexed: rag takes extracted text (%s): %w",
			value, strings.Join(textContentTypes, ", "), ErrInvalid)
	}
	return value, nil
}

// TextContentTypes lists what can be indexed, for the API to advertise.
func TextContentTypes() []string { return slices.Clone(textContentTypes) }

// Text checks a document body is text this service can actually index, and
// returns it unchanged. Invalid UTF-8 is refused rather than replaced: a
// mojibake chunk embeds to a vector that matches nothing and is never found
// again, which is worse than a rejected upload.
func Text(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("document text is required: %w", ErrInvalid)
	}
	if len(raw) > MaxDocumentBytes {
		return "", fmt.Errorf("document text must be at most %d bytes: %w", MaxDocumentBytes, ErrInvalid)
	}
	if !utf8.ValidString(raw) {
		return "", fmt.Errorf("document text must be valid UTF-8: %w", ErrInvalid)
	}
	return raw, nil
}

// Title bounds a document title, falling back to nothing rather than to a
// truncated guess.
func Title(raw string) (string, error) {
	title := strings.TrimSpace(raw)
	if utf8.RuneCountInString(title) > 200 {
		return "", fmt.Errorf("title must be at most 200 characters: %w", ErrInvalid)
	}
	return title, nil
}

// Hash is the content hash this service is idempotent on. Re-ingesting the
// same bytes into the same corpus is a no-op, which is what lets an upstream
// pipeline re-deliver freely.
func Hash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// Document is one source item inside a corpus.
type Document struct {
	ID             string         `json:"id"`
	CorpusID       string         `json:"corpus_id"`
	OrganizationID string         `json:"organization_id"`
	Access         DocumentAccess `json:"access"`
	Source         Source         `json:"source"`
	Title          string         `json:"title,omitempty"`
	ContentType    string         `json:"content_type"`
	ContentSHA256  string         `json:"content_sha256"`
	ByteSize       int64          `json:"byte_size"`
	Metadata       Metadata       `json:"metadata,omitempty"`
	Status         IngestStatus   `json:"status"`
	FailureReason  string         `json:"failure_reason,omitempty"`
	ChunkCount     int            `json:"chunk_count"`
	Attempts       int            `json:"attempts"`
	CreatedBy      string         `json:"created_by"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	IngestedAt     *time.Time     `json:"ingested_at,omitempty"`

	// OriginalBucket and OriginalKey locate a document's own bytes in S3, for
	// a document indexed with no extracted text of its own — a PDF, an
	// Office file — because indexing it means handing those bytes to a
	// knowledge base that parses them itself, not chunking text rag never
	// has. Both empty, the ordinary case, means the document was indexed
	// from text kept in its body instead. Internal bookkeeping for the
	// ingest worker, not part of the public API.
	OriginalBucket string `json:"-"`
	OriginalKey    string `json:"-"`
	// KBSyncedHash is the ContentSHA256 that was last handed to a Bedrock
	// knowledge base for this document. Ingestion there is asynchronous, so
	// the ingest worker claims a document many times before the knowledge
	// base reports it indexed; comparing this to the current ContentSHA256
	// is how a claim knows whether to submit again — never submitted, or the
	// content changed since it last was — or only to poll for the outcome of
	// a submission already running. Meaningless outside a bedrock-kb corpus,
	// and not part of the public API.
	KBSyncedHash string `json:"-"`
	// KBSubmittedAt is when KBSyncedHash was handed over, so the ingest
	// worker can tell a submission the knowledge base is still working on
	// from one it will never finish. Nil when nothing is outstanding.
	KBSubmittedAt *time.Time `json:"-"`
}

// DocumentAccess is the organization-level ACL on an individual document.
// Corpus placement still applies first; an admins document is visible only
// to organization owners and admins within that placement.
type DocumentAccess string

const (
	AccessMembers DocumentAccess = "members"
	AccessAdmins  DocumentAccess = "admins"
)

func (access DocumentAccess) Valid() bool { return access == AccessMembers || access == AccessAdmins }

// VisibleTo reports whether a caller already allowed into this document's
// corpus may read the document itself. An access label this service does not
// know is refused rather than guessed at.
func (document Document) VisibleTo(member OrgMembership) bool {
	switch document.Access {
	case "", AccessMembers:
		return true
	case AccessAdmins:
		return member.SeesAdminOnly()
	default:
		return false
	}
}

// HasOriginal reports whether a document was indexed with no extracted text
// of its own — its bytes live in S3 instead, for a backend that parses them
// itself to read.
func (document Document) HasOriginal() bool {
	return document.OriginalBucket != "" && document.OriginalKey != ""
}

// Chunk is one embeddable piece of a document, with enough provenance to cite
// it: which document, where in it, and under which headings.
type Chunk struct {
	ID             string `json:"id"`
	DocumentID     string `json:"document_id"`
	CorpusID       string `json:"corpus_id"`
	OrganizationID string `json:"organization_id"`
	Ordinal        int    `json:"ordinal"`
	Text           string `json:"text"`
	TokenCount     int    `json:"token_count"`
	// HeadingPath is the trail of headings above this chunk. It is prefixed
	// to the text that gets embedded, so a chunk deep in a document still
	// carries what it is about, and it is shown with a citation.
	HeadingPath []string `json:"heading_path,omitempty"`
	// StartByte and EndByte locate the chunk in the document text, so a
	// citation can be checked against the original rather than trusted.
	StartByte int      `json:"start_byte"`
	EndByte   int      `json:"end_byte"`
	SHA256    string   `json:"sha256"`
	Metadata  Metadata `json:"metadata,omitempty"`
}

// EmbedText is what actually gets embedded: the heading trail, then the text.
//
// This is the cheapest retrieval win there is. A chunk that reads "It must be
// rotated every 90 days" is unfindable on its own; the same chunk under
// "Security > Credentials" is found by a question about credential rotation.
func (chunk Chunk) EmbedText() string {
	if len(chunk.HeadingPath) == 0 {
		return chunk.Text
	}
	return strings.Join(chunk.HeadingPath, " > ") + "\n\n" + chunk.Text
}

// Provenance is the metadata the pipeline writes onto every vector itself.
// It is kept apart from a caller's metadata, and the reserved-key rule above
// is what stops a caller from forging it.
func (chunk Chunk) Provenance(document Document) Metadata {
	provenance := Metadata{
		"chunk_id":        chunk.ID,
		"corpus_id":       chunk.CorpusID,
		"document_id":     chunk.DocumentID,
		"organization_id": chunk.OrganizationID,
		"source_kind":     string(document.Source.Kind),
	}
	if document.Source.Ref != "" {
		provenance["source_ref"] = document.Source.Ref
	}
	if document.Title != "" {
		provenance["title"] = document.Title
	}
	if len(chunk.HeadingPath) > 0 {
		provenance["heading_path"] = strings.Join(chunk.HeadingPath, " > ")
	}
	return provenance
}

// Vector is one chunk's embedding on its way to or from a store.
//
// DocumentID is carried alongside rather than read back out of Metadata: a
// store filters and deletes by it, and a field that load-bearing should not
// depend on a map key still being present.
type Vector struct {
	ChunkID    string
	DocumentID string
	Values     []float32
	Metadata   Metadata
}

// Validate checks a vector against the spec its corpus pinned. Doing this
// above the store means both backends are protected by the same rule, and a
// provider that quietly returned a short vector is caught at the boundary
// rather than at query time.
func (vector Vector) Validate(spec EmbeddingSpec) error {
	if vector.ChunkID == "" || vector.DocumentID == "" {
		return fmt.Errorf("a vector must name its chunk and its document: %w", ErrInvalid)
	}
	if len(vector.Values) != spec.Dimensions {
		return fmt.Errorf("%s returned %d dimensions but the corpus is %d: %w",
			spec.Model, len(vector.Values), spec.Dimensions, ErrConflict)
	}
	return nil
}

// ChunkSync says what re-cutting a document changed.
//
// The split is the whole point of re-cutting rather than rebuilding: only
// Fresh costs an embedding call, so a one-word edit to a long document is one
// call rather than one per chunk.
type ChunkSync struct {
	// Fresh are the chunks that need embedding: new text, or text that moved
	// into a chunk that did not exist before.
	Fresh []Chunk
	// Reused are the chunks whose text is byte-for-byte what it was, so the
	// vector already written for them is still correct.
	Reused []Chunk
	// Removed are the chunk ids that no longer exist. Their pgvector rows go
	// by cascade; an S3 Vectors corpus needs them deleted by key.
	Removed []string
}

// SourceContent is a document's bytes as fetched from the files service, with
// the little that files knows about them. It lives here rather than in the
// client so the application depends on the shape rather than on the transport.
type SourceContent struct {
	ID          string
	Filename    string
	ContentType string
	Body        string
	Scope       Scope
	TeamID      string
	MemberID    string
	Purpose     string
	Access      DocumentAccess
}
