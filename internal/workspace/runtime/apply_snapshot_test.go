package runtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

func TestApplyReusesInitialAndFinalSnapshots(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*fakeState, *spec.Document)
		reads     int32
	}{
		{"noop", func(*fakeState, *spec.Document) {}, 0},
		{"invite", func(s *fakeState, d *spec.Document) {
			d.People = &spec.People{Members: []spec.Member{{Email: "new@acme.com"}}}
		}, 1},
		{"create_team", func(s *fakeState, d *spec.Document) {
			d.Teams.Items = []spec.Team{{Name: "Engineering", Members: []string{"owner@acme.com"}}}
		}, 2},
		{"prune", func(s *fakeState, d *spec.Document) {
			s.members = append(s.members, Member{UserID: "u2", Email: "old@acme.com", Role: "member"})
			d.People = &spec.People{Prune: true, Members: []spec.Member{{Email: "owner@acme.com"}}}
		}, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := &fakeState{orgExists: true, members: []Member{{UserID: "u1", Email: "owner@acme.com", Role: "owner"}}}
			document := spec.Document{APIVersion: spec.APIVersion, Kind: "Workspace", Metadata: spec.Metadata{Organization: "acme"}}
			test.configure(state, &document)
			var reads atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/organizations" {
					reads.Add(1)
				}
				state.serve(w, r)
			}))
			defer api.Close()
			client, err := NewClient(api.URL, "test-token")
			if err != nil {
				t.Fatal(err)
			}
			initial, err := client.Snapshot(context.Background(), "acme")
			if err != nil {
				t.Fatal(err)
			}
			approved, err := BuildPlan(document, initial)
			if err != nil {
				t.Fatal(err)
			}
			reads.Store(0)
			remaining, final, err := ApplyApprovedFromSnapshot(context.Background(), client, document, approved, initial)
			if err != nil || len(remaining.Changes) != 0 || reads.Load() != test.reads {
				t.Fatalf("apply=%+v err=%v reads=%d want=%d", remaining, err, reads.Load(), test.reads)
			}
			verified, err := BuildPlan(document, final)
			if err != nil || len(verified.Changes) != 0 {
				t.Fatalf("returned state is not final: %+v %v", verified, err)
			}
		})
	}
}

func TestApplyFinalSnapshotDetectsUnobservedWrites(t *testing.T) {
	state := &fakeState{orgExists: true, members: []Member{{UserID: "u1", Email: "owner@acme.com", Role: "owner"}}}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/organizations/org_1/invitations" {
			w.WriteHeader(http.StatusCreated) // accepted by upstream, but not yet observable
			return
		}
		state.serve(w, r)
	}))
	defer api.Close()
	client, _ := NewClient(api.URL, "test-token")
	initial, err := client.Snapshot(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	document := spec.Document{APIVersion: spec.APIVersion, Kind: "Workspace", Metadata: spec.Metadata{Organization: "acme"}, People: &spec.People{Members: []spec.Member{{Email: "new@acme.com"}}}}
	remaining, final, err := ApplyApprovedFromSnapshot(context.Background(), client, document, Plan{}, initial)
	if err != nil || len(remaining.Changes) != 1 || len(final.Invitations) != 0 {
		t.Fatalf("unobserved invitation was reported complete: %+v %+v %v", remaining, final, err)
	}
}

func TestReusedApplySnapshotStillRechecksBeforePrune(t *testing.T) {
	state := &fakeState{orgExists: true, members: []Member{{UserID: "u1", Email: "owner@acme.com", Role: "owner"}, {UserID: "u2", Email: "old@acme.com", Role: "member"}}}
	var inject atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/organizations" && inject.Swap(false) {
			state.members = append(state.members, Member{UserID: "u3", Email: "joined@acme.com", Role: "member"})
		}
		state.serve(w, r)
	}))
	defer api.Close()
	client, _ := NewClient(api.URL, "test-token")
	initial, err := client.Snapshot(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	document := spec.Document{APIVersion: spec.APIVersion, Kind: "Workspace", Metadata: spec.Metadata{Organization: "acme"}, People: &spec.People{Prune: true, Members: []spec.Member{{Email: "owner@acme.com"}}}}
	approved, err := BuildPlan(document, initial)
	if err != nil {
		t.Fatal(err)
	}
	inject.Store(true)
	_, _, err = ApplyApprovedFromSnapshot(context.Background(), client, document, approved, initial)
	if !errors.Is(err, ErrApprovalChanged) || state.removedMembers != 0 {
		t.Fatalf("new join was not protected before prune: %v removed=%d", err, state.removedMembers)
	}
}
