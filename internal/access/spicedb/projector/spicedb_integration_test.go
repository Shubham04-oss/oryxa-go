package projector

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"slices"
	"testing"
	"time"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb"
)

type memoryTokens map[string]string

func (store memoryTokens) Save(_ context.Context, org string, token *v1.ZedToken) error {
	store[org] = token.GetToken()
	return nil
}

func (store memoryTokens) SaveAll(_ context.Context, orgs []string, token *v1.ZedToken, _ time.Time) error {
	for _, org := range orgs {
		store[org] = token.GetToken()
	}
	return nil
}

func (store memoryTokens) Latest(_ context.Context, org string) (*v1.ZedToken, error) {
	return &v1.ZedToken{Token: store[org]}, nil
}

// TestAgainstSpiceDB writes to a real SpiceDB. Point SPICEDB_ENDPOINT at
// `spicedb serve-testing`: with SPICEDB_PRESHARED_KEY unset it picks a random
// key, which serve-testing answers with a datastore of its own.
func TestAgainstSpiceDB(t *testing.T) {
	config := spicedb.FromEnv()
	if !config.Enabled() {
		t.Skip("SPICEDB_ENDPOINT is not set")
	}
	if config.PresharedKey == "" {
		random := make([]byte, 8)
		_, _ = rand.Read(random)
		config.PresharedKey = "projector-" + hex.EncodeToString(random)
	}
	client, err := spicedb.Dial(config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()
	schema, err := os.ReadFile("../schema.zed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.WriteSchema(ctx, &v1.WriteSchemaRequest{Schema: string(schema)}); err != nil {
		t.Fatal(err)
	}
	store := memoryTokens{}
	writer := NewWriter(client, store)
	apply := func(change Change, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Apply(ctx, change); err != nil {
			t.Fatal(err)
		}
	}
	read := func(filter Filter) []string {
		t.Helper()
		rels, err := writer.Read(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		return strs(rels)
	}

	// On change: a role change replaces the role; a removal takes all.
	apply(MemberJoined("acme", "sam", "admin"))
	apply(MemberRoleChanged("acme", "sam", "member"))
	if got := read(Filter{ResourceType: "org", ResourceID: "acme"}); !slices.Equal(got, []string{"org:acme#member@member:sam"}) {
		t.Fatalf("after role change: %v", got)
	}
	if store["acme"] == "" {
		t.Fatal("no ZedToken kept for acme")
	}
	apply(Placed("corpus", "cor_sam", Placement{Org: "acme", Scope: "member", Member: "sam", CreatedBy: "sam"}))
	apply(AgentChanged("acme", "agt_r", "sam"))
	apply(Change{Organization: "acme", Touch: []Rel{rel("corpus", "cor_sam", "granted", "agent", "agt_r")}}, nil)
	apply(AgentDeleted("acme", "agt_r"))
	if got := read(Filter{ResourceType: "corpus", SubjectType: "agent", SubjectID: "agt_r"}); len(got) != 0 {
		t.Fatalf("grants outlived their agent: %v", got)
	}
	apply(MemberRemoved("acme", "sam"))
	if got := read(Filter{ResourceType: "org", ResourceID: "acme"}); len(got) != 0 {
		t.Fatalf("after removal: %v", got)
	}

	// Reconcile: a stale relationship goes, a missing one is written, and a
	// slice whose source was not read is left alone.
	apply(Change{Touch: []Rel{rel("org", "acme", "owner", "member", "ghost"), rel("team", "eng", "org", "org", "acme")}}, nil)
	reconciler := NewReconciler(writer, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	world := World{Read: map[string]bool{"auth": true}, Memberships: []Membership{{Org: "acme", User: "olu", Role: "owner"}}}
	result, _, err := reconciler.fix(ctx, world, Filter{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 1 || result.Written != 1 {
		t.Fatalf("reconcile = %+v", result)
	}
	if got := read(Filter{ResourceType: "org", ResourceID: "acme"}); !slices.Equal(got, []string{"org:acme#owner@member:olu"}) {
		t.Fatalf("org after reconcile: %v", got)
	}
	if got := read(Filter{ResourceType: "team"}); !slices.Equal(got, []string{"team:eng#org@org:acme"}) {
		t.Fatalf("teams were not read, yet the reconcile changed them: %v", got)
	}

	// A check sees the reconciled owner, at least as fresh as the last write.
	token, _ := store.Latest(ctx, "acme")
	check, err := client.CheckPermission(ctx, &v1.CheckPermissionRequest{
		Consistency: &v1.Consistency{Requirement: &v1.Consistency_FullyConsistent{FullyConsistent: true}},
		Resource:    objectRef("org", "acme"), Permission: "administer",
		Subject: &v1.SubjectReference{Object: objectRef("member", "olu")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if check.GetPermissionship() != v1.CheckPermissionResponse_PERMISSIONSHIP_HAS_PERMISSION || token.GetToken() == "" {
		t.Fatalf("check = %v, token %q", check.GetPermissionship(), token.GetToken())
	}
}
