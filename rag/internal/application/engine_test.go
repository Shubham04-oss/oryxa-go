package application

import (
	"context"
	"errors"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/access"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// decider answers every decision with answer and err, and remembers what the
// code would have said.
type decider struct {
	answer bool
	err    error
	code   []bool
	// singles and batches count Decide and DecideMany calls.
	singles, batches int
}

func (d *decider) Decide(_ context.Context, _ access.Ask, codeAllowed bool) (bool, error) {
	d.singles++
	d.code = append(d.code, codeAllowed)
	return d.answer, d.err
}

// DecideMany answers as *shadow.Shadow does: answer narrows the code's, no
// answer leaves the code's, and any other error fails the batch.
func (d *decider) DecideMany(_ context.Context, asks []access.Ask, codeAllowed []bool) ([]bool, error) {
	d.batches++
	d.code = append(d.code, codeAllowed...)
	if d.err != nil && !errors.Is(d.err, access.ErrNoAnswer) {
		return nil, d.err
	}
	allowed := make([]bool, len(asks))
	for i := range asks {
		allowed[i] = codeAllowed[i] && (d.answer || d.err != nil)
	}
	return allowed, nil
}

type written struct {
	created, deleted []string
	err              error
}

func (w *written) CorpusCreated(_ context.Context, corpus domain.Corpus) error {
	w.created = append(w.created, corpus.ID)
	return w.err
}

func (w *written) CorpusDeleted(_ context.Context, _, corpusID string) error {
	w.deleted = append(w.deleted, corpusID)
	return w.err
}

// With the engine on, SpiceDB can refuse what the code allows but not grant
// what it refuses, and the engine hears the code's answer both times.
func TestTheEngineOnlyNarrows(t *testing.T) {
	ctx := context.Background()
	h := newDecisionsHarness()
	allow := &decider{answer: true}
	h.service.WithEngine(allow, nil)
	if _, err := h.service.Corpus(ctx, creds("user-outsider"), testOrganization, h.member.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SpiceDB alone granted another member's corpus: %v", err)
	}
	if len(allow.code) != 1 || allow.code[0] {
		t.Fatalf("the engine was told the code allowed: %v", allow.code)
	}
	h.service.WithEngine(&decider{answer: false}, nil)
	if _, err := h.service.Corpus(ctx, creds("user-creator"), testOrganization, h.member.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SpiceDB refused the creator's own corpus, but Corpus = %v", err)
	}
	if err := h.service.DeleteCorpus(ctx, creds("user-creator"), testOrganization, h.member.ID); err == nil {
		t.Fatal("SpiceDB refused, but the delete went ahead")
	}
}

// No answer from SpiceDB refuses; no model of the case falls back to the code.
func TestTheEngineFailsClosed(t *testing.T) {
	ctx := context.Background()
	h := newDecisionsHarness()
	h.service.WithEngine(&decider{answer: true, err: errors.New("spicedb unavailable")}, nil)
	if _, err := h.service.Corpus(ctx, creds("user-creator"), testOrganization, h.member.ID); err == nil {
		t.Fatal("a SpiceDB failure allowed the read")
	}
	h.service.WithEngine(&decider{err: access.ErrNoAnswer}, nil)
	if _, err := h.service.Corpus(ctx, creds("user-creator"), testOrganization, h.member.ID); err != nil {
		t.Fatalf("with no answer the code's allow should stand: %v", err)
	}
	if _, err := h.service.Corpus(ctx, creds("user-outsider"), testOrganization, h.member.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("with no answer the code's refusal should stand: %v", err)
	}
}

// A corpus's relationships are written as it is created and deleted, and a
// failure to write them fails neither.
func TestRelationshipsFollowTheCorpus(t *testing.T) {
	ctx := context.Background()
	h := newHarness()
	rels := &written{err: errors.New("spicedb unavailable")}
	h.service.WithEngine(&decider{answer: true}, rels)
	created, err := h.service.CreateCorpus(ctx, creds("user-creator"), testOrganization, CreateCorpusInput{Name: "Notes"})
	if err != nil {
		t.Fatalf("create failed with SpiceDB down: %v", err)
	}
	if err := h.service.DeleteCorpus(ctx, creds("user-creator"), testOrganization, created.ID); err != nil {
		t.Fatalf("delete failed with SpiceDB down: %v", err)
	}
	if len(rels.created) != 1 || rels.created[0] != created.ID || len(rels.deleted) != 1 || rels.deleted[0] != created.ID {
		t.Fatalf("created %v, deleted %v", rels.created, rels.deleted)
	}
}

// A listing asks the engine once, for every corpus on it: SpiceDB narrows it
// and its failure fails it.
func TestAListingIsDecidedInOneCall(t *testing.T) {
	ctx := context.Background()
	h := newDecisionsHarness()
	list := func(engine *decider) ([]domain.Corpus, error) {
		h.service.WithEngine(engine, nil)
		return h.service.Corpora(ctx, creds("user-creator"), testOrganization, ListFilter{})
	}
	code, err := list(&decider{err: access.ErrNoAnswer})
	if err != nil || len(code) < 2 {
		t.Fatalf("the code's listing = %d corpora, %v; want at least 2", len(code), err)
	}
	allow := &decider{answer: true}
	if listed, err := list(allow); err != nil || len(listed) != len(code) {
		t.Fatalf("Corpora = %d, %v; want the code's %d", len(listed), err, len(code))
	}
	if allow.batches != 1 || allow.singles != 0 {
		t.Fatalf("engine asked %d batches and %d singles; want 1 batch", allow.batches, allow.singles)
	}
	if listed, err := list(&decider{answer: false}); err != nil || len(listed) != 0 {
		t.Fatalf("SpiceDB refused every corpus, but Corpora = %d, %v", len(listed), err)
	}
	if _, err := list(&decider{answer: true, err: errors.New("spicedb unavailable")}); err == nil {
		t.Fatal("a SpiceDB failure listed corpora")
	}
}
