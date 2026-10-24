package shadow

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"testing"
	"time"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/access"
	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb"
)

// TestListLatency times a 50-file list three ways against a real, empty
// SpiceDB: a Decide per row (before), one DecideMany (after), and a
// LookupResources of every file the member reads. It writes the schema, so
// point it only at a throwaway SpiceDB: SPICEDB_LATENCY=1 with SPICEDB_*.
func TestListLatency(t *testing.T) {
	config := spicedb.FromEnv()
	if os.Getenv("SPICEDB_LATENCY") != "1" || !config.Enabled() {
		t.Skip("SPICEDB_LATENCY=1 and SPICEDB_ENDPOINT are not set")
	}
	client, err := spicedb.Dial(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	schema, err := os.ReadFile("../spicedb/schema.zed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.WriteSchema(ctx, &v1.WriteSchemaRequest{Schema: string(schema)}); err != nil {
		t.Fatal(err)
	}
	touch := func(resource, id, relation, subject, subjectID string) *v1.RelationshipUpdate {
		return &v1.RelationshipUpdate{Operation: v1.RelationshipUpdate_OPERATION_TOUCH, Relationship: &v1.Relationship{
			Resource: &v1.ObjectReference{ObjectType: resource, ObjectId: id}, Relation: relation,
			Subject: &v1.SubjectReference{Object: &v1.ObjectReference{ObjectType: subject, ObjectId: subjectID}},
		}}
	}
	// 50 team files the member reads through the team, 450 others' files it
	// doesn't: the list's page, inside a larger org.
	updates := []*v1.RelationshipUpdate{
		touch("org", "org_l", "member", "member", "u_list"),
		touch("team", "team_l", "org", "org", "org_l"),
		touch("team", "team_l", "member", "member", "u_list"),
	}
	asks := make([]access.Ask, 50)
	for i := range 500 {
		id := fmt.Sprintf("file_l%03d", i)
		updates = append(updates, touch("file", id, "org", "org", "org_l"))
		if i < len(asks) {
			updates = append(updates, touch("file", id, "team", "team", "team_l"))
			asks[i] = access.Ask{OrganizationID: "org_l", Service: "files", Action: access.ActionRead,
				ResourceKind: "file", ResourceID: id, UserID: "u_list", Actor: access.Actor{Role: access.Member}}
		} else {
			updates = append(updates, touch("file", id, "owner", "member", "u_other"))
		}
	}
	for chunk := range slices.Chunk(updates, 500) {
		if _, err := client.WriteRelationships(ctx, &v1.WriteRelationshipsRequest{Updates: chunk}); err != nil {
			t.Fatal(err)
		}
	}
	shadow := New(Client{SpiceDB: client}, nil, nil)
	t.Cleanup(shadow.Close)
	code := make([]bool, len(asks))
	for i := range code {
		code[i] = true
	}

	measure := func(name string, run func() int) {
		const rounds = 30
		run() // warm the connection and SpiceDB's cache
		took := make([]time.Duration, rounds)
		for i := range took {
			began := time.Now()
			if got := run(); got != len(asks) {
				t.Fatalf("%s: %d readable, want %d", name, got, len(asks))
			}
			took[i] = time.Since(began)
		}
		slices.Sort(took)
		t.Logf("%-40s p50 %8s  p95 %8s", name, took[rounds/2].Round(10*time.Microsecond), took[rounds*95/100].Round(10*time.Microsecond))
	}
	measure("before: Decide per row (50 calls)", func() int {
		readable := 0
		for _, ask := range asks {
			if allowed, err := shadow.Decide(ctx, ask, true); err != nil {
				t.Fatal(err)
			} else if allowed {
				readable++
			}
		}
		return readable
	})
	measure("after: DecideMany (1 call)", func() int {
		allowed, err := shadow.DecideMany(ctx, asks, code)
		if err != nil {
			t.Fatal(err)
		}
		readable := 0
		for _, ok := range allowed {
			if ok {
				readable++
			}
		}
		return readable
	})
	// A list longer than one bulk request still answers, in bulkLimit chunks.
	long := make([]spicedb.Check, 2*bulkLimit+1)
	for i := range long {
		long[i] = spicedb.Check{Resource: spicedb.Object{Type: "file", ID: asks[i%len(asks)].ResourceID}, Permission: "read",
			Subject: spicedb.Object{Type: "member", ID: "u_list"}}
	}
	if answers, err := (Client{SpiceDB: client}).CheckEach(ctx, "org_l", long); err != nil || slices.Contains(answers, false) {
		t.Fatalf("CheckEach of %d: %v", len(long), err)
	}
	measure("LookupResources file#read (500 in org)", func() int {
		stream, err := client.LookupResources(ctx, &v1.LookupResourcesRequest{
			Consistency:        &v1.Consistency{Requirement: &v1.Consistency_MinimizeLatency{MinimizeLatency: true}},
			ResourceObjectType: "file", Permission: "read",
			Subject: &v1.SubjectReference{Object: &v1.ObjectReference{ObjectType: "member", ObjectId: "u_list"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		readable := 0
		for {
			_, err := stream.Recv()
			if err == io.EOF {
				return readable
			}
			if err != nil {
				t.Fatal(err)
			}
			readable++
		}
	})
}
