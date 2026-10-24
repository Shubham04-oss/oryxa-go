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
}

func (d *decider) Decide(_ context.Context, _ access.Ask, codeAllowed bool) (bool, error) {
	d.code = append(d.code, codeAllowed)
	return d.answer, d.err
}

type written struct {
	registered, removed []string
	err                 error
}

func (w *written) SourceRegistered(_ context.Context, source domain.Source) error {
	w.registered = append(w.registered, source.ID)
	return w.err
}

func (w *written) SourceRemoved(_ context.Context, _, sourceID string) error {
	w.removed = append(w.removed, sourceID)
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

// A source's relationships are written as it is registered and removed, and a
// failure to write them fails neither.
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
		t.Fatalf("remove failed with SpiceDB down: %v", err)
	}
	if len(rels.registered) != 1 || rels.registered[0] != source.ID || len(rels.removed) != 1 || rels.removed[0] != source.ID {
		t.Fatalf("registered %v, removed %v", rels.registered, rels.removed)
	}
}
