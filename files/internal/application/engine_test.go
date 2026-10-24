package application

import (
	"context"
	"errors"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/files/internal/blob"
	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/access"
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
	created, deleted []string
	err              error
}

func (w *written) FileCreated(_ context.Context, object domain.Object) error {
	w.created = append(w.created, object.ID)
	return w.err
}

func (w *written) FileDeleted(_ context.Context, _, fileID string) error {
	w.deleted = append(w.deleted, fileID)
	return w.err
}

// With the engine on, SpiceDB can refuse what the code allows but not grant
// what it refuses, and the engine hears the code's answer both times.
func TestTheEngineOnlyNarrows(t *testing.T) {
	ctx := context.Background()
	store, blobs := newFakeStore(), blob.NewMemory()
	owner := testService(store, blobs)
	created, err := owner.Create(ctx, creds("user-owner"), "org_1", given(CreateInput{Filename: "a.txt", ContentType: "text/plain", Content: []byte("a")}))
	if err != nil {
		t.Fatal(err)
	}

	allow := &decider{answer: true}
	if _, err := testService(store, blobs).WithEngine(allow, nil).Get(ctx, creds("user-member"), "org_1", created.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SpiceDB alone granted what the code refuses: Get = %v", err)
	}
	if len(allow.code) != 1 || allow.code[0] {
		t.Fatalf("the engine was told the code allowed: %v", allow.code)
	}

	deny := &decider{answer: false}
	if _, err := testService(store, blobs).WithEngine(deny, nil).Get(ctx, creds("user-owner"), "org_1", created.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SpiceDB refused what the code allows, but Get = %v", err)
	}
}

// No answer from SpiceDB refuses; no model of the case falls back to the code.
func TestTheEngineFailsClosed(t *testing.T) {
	ctx := context.Background()
	store, blobs := newFakeStore(), blob.NewMemory()
	created, err := testService(store, blobs).Create(ctx, creds("user-owner"), "org_1", given(CreateInput{Filename: "a.txt", ContentType: "text/plain", Content: []byte("a")}))
	if err != nil {
		t.Fatal(err)
	}
	down := &decider{answer: true, err: errors.New("spicedb unavailable")}
	if _, err := testService(store, blobs).WithEngine(down, nil).Get(ctx, creds("user-owner"), "org_1", created.ID); err == nil {
		t.Fatal("a SpiceDB failure allowed the read")
	}
	unmodelled := &decider{err: access.ErrNoAnswer}
	service := testService(store, blobs).WithEngine(unmodelled, nil)
	if _, err := service.Get(ctx, creds("user-owner"), "org_1", created.ID); err != nil {
		t.Fatalf("with no answer the code's allow should stand: %v", err)
	}
	if _, err := service.Get(ctx, creds("user-member"), "org_1", created.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("with no answer the code's refusal should stand: %v", err)
	}
}

// A file's relationships are written as it is created and deleted, and a
// failure to write them fails neither.
func TestRelationshipsFollowTheFile(t *testing.T) {
	ctx := context.Background()
	rels := &written{err: errors.New("spicedb unavailable")}
	service := testService(newFakeStore(), blob.NewMemory()).WithEngine(&decider{answer: true}, rels)
	created, err := service.Create(ctx, creds("user-owner"), "org_1", given(CreateInput{Filename: "a.txt", ContentType: "text/plain", Content: []byte("a")}))
	if err != nil {
		t.Fatalf("create failed with SpiceDB down: %v", err)
	}
	if err := service.Delete(ctx, creds("user-owner"), "org_1", created.ID); err != nil {
		t.Fatalf("delete failed with SpiceDB down: %v", err)
	}
	if len(rels.created) != 1 || rels.created[0] != created.ID || len(rels.deleted) != 1 || rels.deleted[0] != created.ID {
		t.Fatalf("created %v, deleted %v", rels.created, rels.deleted)
	}
}
