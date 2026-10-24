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

// A list asks the engine once, for every file on it: SpiceDB narrows it and
// its failure fails it.
func TestAListIsDecidedInOneCall(t *testing.T) {
	ctx := context.Background()
	store, blobs := newFakeStore(), blob.NewMemory()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if _, err := testService(store, blobs).Create(ctx, creds("user-owner"), "org_1", given(CreateInput{Filename: name, ContentType: "text/plain", Content: []byte("a")})); err != nil {
			t.Fatal(err)
		}
	}
	list := func(engine *decider) ([]domain.Object, error) {
		return testService(store, blobs).WithEngine(engine, nil).List(ctx, creds("user-owner"), "org_1", ListFilter{})
	}
	allow := &decider{answer: true}
	if listed, err := list(allow); err != nil || len(listed) != 3 {
		t.Fatalf("List = %d files, %v; want 3", len(listed), err)
	}
	if allow.batches != 1 || allow.singles != 0 || len(allow.code) != 3 {
		t.Fatalf("engine asked %d batches, %d singles, about %d files; want 1 batch of 3", allow.batches, allow.singles, len(allow.code))
	}
	if listed, err := list(&decider{answer: false}); err != nil || len(listed) != 0 {
		t.Fatalf("SpiceDB refused every file, but List = %d files, %v", len(listed), err)
	}
	if listed, err := list(&decider{err: access.ErrNoAnswer}); err != nil || len(listed) != 3 {
		t.Fatalf("with no answer the code's list should stand: %d files, %v", len(listed), err)
	}
	if _, err := list(&decider{answer: true, err: errors.New("spicedb unavailable")}); err == nil {
		t.Fatal("a SpiceDB failure listed files")
	}
}
