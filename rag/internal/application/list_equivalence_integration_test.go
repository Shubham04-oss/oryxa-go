package application

import (
	"context"
	"os"
	"testing"
	"time"

	rageventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/rag/v1"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/postgres"
)

// listEquivalenceOnPostgres runs a listing's equivalence check against a real
// PostgreSQL, so the SQL that pre-filters a listing is held to the single
// read too. Without RAG_TEST_DATABASE_URL it skips, as the store's own
// integration tests do.
func listEquivalenceOnPostgres(t *testing.T, check func(*testing.T, context.Context, *Service, string, listFixtures)) {
	databaseURL := os.Getenv("RAG_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RAG_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	store, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	organizationID := "org-list-" + time.Now().UTC().Format("20060102150405.000000000")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		corpora, err := store.CorporaForWipe(cleanupCtx, organizationID, "", "")
		if err != nil {
			t.Logf("cleanup: %v", err)
			return
		}
		for _, corpus := range corpora {
			event := listEvent(t)
			event.DetailType = rageventsv1.CorpusDeletedDetailType
			_ = store.DeleteCorpus(cleanupCtx, organizationID, corpus.ID, event)
		}
	})
	teams := newFakeTeams()
	teams.organization = organizationID
	service := New(store, fakeDirectory{organization: organizationID}, teams, &fakeFiles{}, &fakeDocuments{},
		newFakeVectors(), &fakeRetriever{}, &fakeGenerator{model: "test-model"}, &fakeKnowledgeBase{}, Defaults{}, nil)
	fixtures := seedListFixtures(t, ctx, store, organizationID)
	check(t, ctx, service, organizationID, fixtures)
}

func TestCorporaListsExactlyWhatCorpusReadsOnPostgres(t *testing.T) {
	listEquivalenceOnPostgres(t, checkCorporaMatchCorpus)
}

func TestDocumentsListsExactlyWhatDocumentReadsOnPostgres(t *testing.T) {
	listEquivalenceOnPostgres(t, checkDocumentsMatchDocument)
}
