package postgres

import (
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// A chunk keeps its vector exactly when its text and metadata are both
// unchanged, and is relabelled only when no chunk of the same text can: the
// exact matches for a whole document are settled before any relabel, so a
// repeated paragraph never costs a chunk its vector for want of order.
func TestSyncChunksSettlesExactMatchesBeforeRelabelling(t *testing.T) {
	store, ctx := openTestStore(t)
	corpus := testCorpus(t, store, ctx, uniqueOrganization(), titanSpec())
	body := "boilerplate"
	document, _, err := store.UpsertDocument(ctx,
		newDocument(corpus, domain.Source{Kind: domain.SourceExternal, Ref: "https://example.com/boilerplate"}, "Boilerplate", body),
		body, testEvent(t, "rag.document.received.v1"))
	if err != nil {
		t.Fatal(err)
	}
	chunk := func(ordinal int, category string) domain.Chunk {
		return domain.Chunk{Ordinal: ordinal, Text: body, TokenCount: 1, SHA256: domain.Hash(body),
			StartByte: 0, EndByte: len(body), Metadata: domain.Metadata{"category": category}}
	}
	first, err := store.SyncChunks(ctx, document, []domain.Chunk{chunk(0, "old")}, time.Now().UTC())
	if err != nil || len(first.Fresh) != 1 {
		t.Fatalf("first sync = %+v, %v", first, err)
	}
	if err := store.MarkChunksEmbedded(ctx, []string{first.Fresh[0].ID}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// The same text twice: first under new metadata, then under the old.
	second, err := store.SyncChunks(ctx, document, []domain.Chunk{chunk(0, "new"), chunk(1, "old")}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Reused) != 1 || second.Reused[0].ID != first.Fresh[0].ID || second.Reused[0].Ordinal != 1 {
		t.Fatalf("reused = %+v, want the old-metadata chunk to keep its vector", second.Reused)
	}
	if len(second.Fresh) != 1 || second.Fresh[0].ID == first.Fresh[0].ID || len(second.Removed) != 0 {
		t.Fatalf("fresh = %+v, removed = %v; want the new-metadata chunk new and nothing removed", second.Fresh, second.Removed)
	}
}
