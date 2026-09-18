package domain

import "time"

// KnowledgeDocumentMapping is what maps one document knowledge published to
// the rag document it became, keyed by knowledge's own document id.
//
// knowledge and rag mint their ids independently, and a later published or
// removed event only ever names knowledge's. Without this a content change
// could not find the rag document to update in place, and a removal could
// not find it to delete: both would have to guess, or leave a stale copy
// searchable forever.
type KnowledgeDocumentMapping struct {
	KnowledgeDocumentID string
	OrganizationID      string
	CorpusID            string
	DocumentID          string
	// ContentHash is knowledge's own hash of the document it last published —
	// over its normalized body, at knowledge — not rag's ContentSHA256 of the
	// bytes actually indexed. It is what a re-delivered, unchanged event is
	// recognised by, before rag ever normalizes or hashes anything itself.
	ContentHash string
	// ExtractedAt is knowledge's timestamp for the version this mapping
	// reflects, kept so an event older than what is already mapped is
	// recognised and ignored rather than regressing a newer version.
	ExtractedAt time.Time
	UpdatedAt   time.Time
}
