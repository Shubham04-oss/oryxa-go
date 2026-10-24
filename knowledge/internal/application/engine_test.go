package application

import (
	"context"
	"errors"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/access"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
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
	registered []string
	err        error
}

func (w *written) SourceRegistered(_ context.Context, source domain.Source) error {
	w.registered = append(w.registered, source.ID)
	return w.err
}

func engineStore() *fakeStore {
	store := newFakeStore()
	store.sources["src_1"] = domain.Source{
		ID: "src_1", OrganizationID: "org_1", Provider: "files", Scope: domain.ScopeOrganization,
		CreatedBy: "user-1", Status: domain.StatusActive,
	}
	return store
}

var engineCreds = authclient.Credentials{Authorization: "Bearer user-1"}

// With the engine on, SpiceDB can refuse what the code allows but not grant
// what it refuses.
func TestTheEngineOnlyNarrows(t *testing.T) {
	ctx := context.Background()
	stranger := member()
	stranger.UserID = "user-2"
	allow := &decider{answer: true}
	service := registrar(t, engineStore(), stranger)
	service.engine = allow
	if _, err := service.Source(ctx, engineCreds, "org_1", "src_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SpiceDB alone granted another member's source: %v", err)
	}
	if len(allow.code) != 1 || allow.code[0] {
		t.Fatalf("the engine was told the code allowed: %v", allow.code)
	}
	creator := registrar(t, engineStore(), member())
	creator.engine = &decider{answer: false}
	if _, err := creator.Source(ctx, engineCreds, "org_1", "src_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SpiceDB refused the creator's own source, but Source = %v", err)
	}
	if err := creator.RemoveSource(ctx, engineCreds, "org_1", "src_1"); err == nil {
		t.Fatal("SpiceDB refused, but the source was removed")
	}
}

// No answer from SpiceDB refuses; no model of the case falls back to the code.
func TestTheEngineFailsClosed(t *testing.T) {
	ctx := context.Background()
	service := registrar(t, engineStore(), member())
	service.engine = &decider{answer: true, err: errors.New("spicedb unavailable")}
	if _, err := service.Source(ctx, engineCreds, "org_1", "src_1"); err == nil {
		t.Fatal("a SpiceDB failure allowed the read")
	}
	service.engine = &decider{err: access.ErrNoAnswer}
	if _, err := service.Source(ctx, engineCreds, "org_1", "src_1"); err != nil {
		t.Fatalf("with no answer the code's allow should stand: %v", err)
	}
}

// A source's relationships are written as it is registered, a failure to
// write them fails nothing, and removing it keeps them: its creator can still
// read a removed source, as before.
func TestRelationshipsFollowTheSource(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	service := registrar(t, store, member())
	rels := &written{err: errors.New("spicedb unavailable")}
	service.engine, service.relationships = &decider{answer: true}, rels
	source, err := service.RegisterSource(ctx, engineCreds, "org_1", RegisterInput{Provider: "files"})
	if err != nil {
		t.Fatalf("register failed with SpiceDB down: %v", err)
	}
	if err := service.RemoveSource(ctx, engineCreds, "org_1", source.ID); err != nil {
		t.Fatalf("remove failed: %v", err)
	}
	if len(rels.registered) != 1 || rels.registered[0] != source.ID {
		t.Fatalf("registered %v", rels.registered)
	}
	if _, err := service.Source(ctx, engineCreds, "org_1", source.ID); err != nil {
		t.Fatalf("the creator can't read their removed source: %v", err)
	}
}

// A listing asks the engine once, for every source on it: SpiceDB narrows it
// and its failure fails it rather than quietly shortening it.
func TestAListingIsDecidedInOneCall(t *testing.T) {
	ctx := context.Background()
	store := engineStore()
	store.sources["src_2"] = domain.Source{
		ID: "src_2", OrganizationID: "org_1", Provider: "files", Scope: domain.ScopeOrganization,
		CreatedBy: "user-1", Status: domain.StatusActive,
	}
	list := func(engine *decider) ([]domain.Source, error) {
		service := registrar(t, store, member())
		service.engine = engine
		return service.Sources(ctx, engineCreds, "org_1", domain.SourceQuery{})
	}
	allow := &decider{answer: true}
	if listed, err := list(allow); err != nil || len(listed) != 2 {
		t.Fatalf("Sources = %d, %v; want 2", len(listed), err)
	}
	if allow.batches != 1 || allow.singles != 0 {
		t.Fatalf("engine asked %d batches and %d singles; want 1 batch", allow.batches, allow.singles)
	}
	if listed, err := list(&decider{answer: false}); err != nil || len(listed) != 0 {
		t.Fatalf("SpiceDB refused every source, but Sources = %d, %v", len(listed), err)
	}
	if listed, err := list(&decider{err: access.ErrNoAnswer}); err != nil || len(listed) != 2 {
		t.Fatalf("with no answer the code's listing should stand: %d, %v", len(listed), err)
	}
	if _, err := list(&decider{answer: true, err: errors.New("spicedb unavailable")}); err == nil {
		t.Fatal("a SpiceDB failure listed sources")
	}
}
