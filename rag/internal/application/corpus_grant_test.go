package application

import (
	"context"
	"errors"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// TestASpecialistReadsOnlyTheCorporaItsGrantNames: with a grant naming some
// corpora, every read of another one is the same not found a missing corpus
// gets, and the list leaves it out. All ("*") and a primary or member are
// not limited; an empty grant sees nothing.
func TestASpecialistReadsOnlyTheCorporaItsGrantNames(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	make := func(name string) domain.Corpus {
		corpus, err := h.service.CreateCorpus(ctx, creds("user-creator"), testOrganization, CreateCorpusInput{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return corpus
	}
	c1, c2 := make("One"), make("Two")
	missing := "cor_doesnotexist0000"
	query := domain.Query{Text: "plans"}

	reads := func(who authclient.Credentials, corpusID string) []error {
		_, corpusErr := h.service.Corpus(ctx, who, testOrganization, corpusID)
		_, documentsErr := h.service.Documents(ctx, who, testOrganization, corpusID, "", "", 10)
		_, documentErr := h.service.Document(ctx, who, testOrganization, corpusID, "doc_x")
		_, searchErr := h.service.Search(ctx, who, testOrganization, corpusID, query)
		_, answerErr := h.service.Answer(ctx, who, testOrganization, corpusID, query)
		return []error{corpusErr, documentsErr, documentErr, searchErr, answerErr}
	}
	listed := func(who authclient.Credentials) int {
		corpora, err := h.service.Corpora(ctx, who, testOrganization, ListFilter{Scope: domain.ScopeMember})
		if err != nil {
			t.Fatal(err)
		}
		return len(corpora)
	}

	// Reads of the corpus it is given go through (a document that is not
	// there is a not found of its own, so only the corpus-level reads count).
	one := grantedCreds("user-creator", c1.ID)
	for i, err := range reads(one, c1.ID) {
		if i != 2 && err != nil {
			t.Errorf("granted read %d: %v", i, err)
		}
	}
	// Outside the grant, each read is exactly what a missing corpus gives.
	for i, err := range reads(one, c2.ID) {
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("read %d of an ungranted corpus: %v, want not found", i, err)
		}
	}
	for i, err := range reads(creds("user-creator"), missing) {
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("read %d of a missing corpus: %v", i, err)
		}
	}
	// Writes into it are refused the same way, and it cannot make new ones.
	if err := h.service.DeleteCorpus(ctx, one, testOrganization, c2.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("delete of an ungranted corpus: %v, want not found", err)
	}
	if _, err := h.service.CreateCorpus(ctx, one, testOrganization, CreateCorpusInput{Name: "Three"}); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("create by a limited specialist: %v, want forbidden", err)
	}
	// The slug is no way round the grant.
	if _, err := h.service.Corpus(ctx, one, testOrganization, c2.Slug); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("read by slug of an ungranted corpus: %v, want not found", err)
	}

	if got := listed(one); got != 1 {
		t.Errorf("a specialist granted one corpus lists %d", got)
	}
	if got := listed(grantedCreds("user-creator")); got != 0 {
		t.Errorf("a specialist granted nothing lists %d", got)
	}
	for name, who := range map[string]authclient.Credentials{
		"a specialist granted all": grantedCreds("user-creator", "*"),
		"a member":                 creds("user-creator"),
	} {
		if got := listed(who); got != 2 {
			t.Errorf("%s lists %d, want 2", name, got)
		}
		for i, err := range reads(who, c2.ID) {
			if i != 2 && err != nil {
				t.Errorf("%s read %d: %v", name, i, err)
			}
		}
	}
	// A primary (no role in its claims is the primary) is not limited even if
	// a grant somehow rode along.
	primary := domain.OrgMembership{Agent: true, AgentRole: "primary", AgentCorpora: []string{}}
	if err := primary.MayReadCorpus(c2.ID); err != nil {
		t.Errorf("primary: %v", err)
	}
}
