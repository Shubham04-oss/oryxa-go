package application

import (
	"context"
	"errors"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/access"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
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

type started struct {
	ids []string
	err error
}

func (s *started) ConnectionStarted(_ context.Context, connection domain.Connection) error {
	s.ids = append(s.ids, connection.ID)
	return s.err
}

// With the engine on, SpiceDB can refuse what the code allows but not grant
// what it refuses, for a read, a list and a removal.
func TestTheEngineOnlyNarrows(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.activeConnection("con_1", "user-1", "slack", "ca_1")

	allow := &decider{answer: true}
	h.service.engine = allow
	if _, err := h.service.Connection(ctx, otherToken, "org_1", "con_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SpiceDB alone granted another member's connection: %v", err)
	}
	if len(allow.code) != 1 || allow.code[0] {
		t.Fatalf("the engine was told the code allowed: %v", allow.code)
	}

	h.service.engine = &decider{answer: false}
	if _, err := h.service.Connection(ctx, memberToken, "org_1", "con_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SpiceDB refused the member's own connection, but Connection = %v", err)
	}
	if listed, err := h.service.Connections(ctx, memberToken, "org_1", "", false); err != nil || len(listed) != 0 {
		t.Fatalf("SpiceDB refused, but the list showed %d, %v", len(listed), err)
	}
	if err := h.service.RemoveConnection(ctx, memberToken, "org_1", "con_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SpiceDB refused, but RemoveConnection = %v", err)
	}
}

// No answer from SpiceDB refuses; no model of the case falls back to the code.
func TestTheEngineFailsClosed(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.activeConnection("con_1", "user-1", "slack", "ca_1")
	h.service.engine = &decider{answer: true, err: errors.New("spicedb unavailable")}
	if _, err := h.service.Connection(ctx, memberToken, "org_1", "con_1"); err == nil {
		t.Fatal("a SpiceDB failure allowed the read")
	}
	if _, err := h.service.Connections(ctx, memberToken, "org_1", "", false); err == nil {
		t.Fatal("a SpiceDB failure allowed the list")
	}
	h.service.engine = &decider{err: access.ErrNoAnswer}
	if _, err := h.service.Connection(ctx, memberToken, "org_1", "con_1"); err != nil {
		t.Fatalf("with no answer the code's allow should stand: %v", err)
	}
}

// A connection's relationships are written as it starts, and a failure to
// write them fails nothing.
func TestRelationshipsFollowTheConnection(t *testing.T) {
	h := newHarness(t)
	rels := &started{err: errors.New("spicedb unavailable")}
	h.service.engine, h.service.relationships = &decider{answer: true}, rels
	h.composio.toolkits["github"] = composio.Toolkit{
		Slug: "github", AuthSchemes: []string{"OAUTH2"}, ComposioManagedAuthSchemes: []string{"OAUTH2"},
	}
	connection, _, err := h.service.Connect(context.Background(), memberToken, "org_1", ConnectInput{Toolkit: "github"})
	if err != nil {
		t.Fatalf("connect failed with SpiceDB down: %v", err)
	}
	if len(rels.ids) != 1 || rels.ids[0] != connection.ID {
		t.Fatalf("started %v, want %s", rels.ids, connection.ID)
	}
}
