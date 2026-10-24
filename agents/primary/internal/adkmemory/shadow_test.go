package adkmemory

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// recording is a backend that notes each call and fails with err.
type recording struct {
	name  string
	calls *[]string
	err   error
}

func (r recording) Write(_ context.Context, scope Scope, _ session.Session) error {
	*r.calls = append(*r.calls, r.name+" write "+scope.UserID)
	return r.err
}

func (r recording) Search(_ context.Context, _ Scope, query string) ([]memory.Entry, error) {
	*r.calls = append(*r.calls, r.name+" search "+query)
	return []memory.Entry{{ID: r.name, Content: genai.NewContentFromText(r.name, genai.RoleUser)}}, r.err
}

func (r recording) Forget(_ context.Context, organizationID, _ string) error {
	*r.calls = append(*r.calls, r.name+" forget "+organizationID)
	return r.err
}

func TestShadowWritesBothAndAnswersFromThePrimary(t *testing.T) {
	var calls []string
	backend, err := Shadow(recording{name: "vertex", calls: &calls}, recording{name: "agentcore", calls: &calls}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	scope := Scope{OrganizationID: "org_acme", UserID: "usr_1"}
	if err := backend.Write(ctx, scope, nil); err != nil {
		t.Fatal(err)
	}
	found, err := backend.Search(ctx, scope, "tea")
	if err != nil || len(found) != 1 || found[0].ID != "vertex" {
		t.Fatalf("Search = %+v, %v; want the primary's answer alone", found, err)
	}
	if err := backend.Forget(ctx, "org_acme", ""); err != nil {
		t.Fatal(err)
	}
	want := []string{"vertex write usr_1", "agentcore write usr_1", "vertex search tea", "vertex forget org_acme", "agentcore forget org_acme"}
	if !slices.Equal(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestShadowNeverFailsOnTheSecondary(t *testing.T) {
	var calls []string
	var logged bytes.Buffer
	backend, err := Shadow(recording{name: "vertex", calls: &calls}, recording{name: "agentcore", calls: &calls, err: errors.New("throttled")},
		slog.New(slog.NewTextHandler(&logged, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := backend.Write(ctx, Scope{OrganizationID: "org_acme", UserID: "usr_1"}, nil); err != nil {
		t.Fatalf("Write = %v, want the secondary's failure only logged", err)
	}
	if err := backend.Forget(ctx, "org_acme", "usr_1"); err != nil {
		t.Fatalf("Forget = %v, want the secondary's failure only logged", err)
	}
	if strings.Count(logged.String(), "throttled") != 2 {
		t.Fatalf("logged %q, want both failures", logged.String())
	}
}

func TestShadowFailsWithThePrimaryAndStillWritesTheSecondary(t *testing.T) {
	var calls []string
	down := errors.New("vertex is down")
	backend, err := Shadow(recording{name: "vertex", calls: &calls, err: down}, recording{name: "agentcore", calls: &calls}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := backend.Write(ctx, Scope{OrganizationID: "org_acme", UserID: "usr_1"}, nil); !errors.Is(err, down) {
		t.Fatalf("Write = %v, want the primary's failure", err)
	}
	if err := backend.Forget(ctx, "org_acme", ""); !errors.Is(err, down) {
		t.Fatalf("Forget = %v, want the primary's failure, so the caller retries", err)
	}
	if !slices.Equal(calls, []string{"vertex write usr_1", "agentcore write usr_1", "vertex forget org_acme", "agentcore forget org_acme"}) {
		t.Fatalf("calls = %v", calls)
	}
	if _, err := Shadow(nil, recording{name: "agentcore", calls: &calls}, nil); err == nil {
		t.Fatal("a shadow with no primary was built")
	}
}
