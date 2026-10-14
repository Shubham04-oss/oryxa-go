package httpserver

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/edgemembers"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/teamspaces"
	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

type fakeEdgeMembers struct {
	admitted []edgemembers.Membership
	err      error
	// recheck is what the access check answered after the admission.
	recheck []bool
	// deleted is the teamspaces emptied, and deleteErr what that answers.
	deleted   []string
	deleteErr error
	// beforeRecheck runs between the admission and the re-check.
	beforeRecheck func()
}

func (members *fakeEdgeMembers) Admit(ctx context.Context, membership edgemembers.Membership, stillMay func(context.Context) bool) error {
	if members.err != nil {
		return members.err
	}
	members.admitted = append(members.admitted, membership)
	if members.beforeRecheck != nil {
		members.beforeRecheck()
	}
	still := stillMay(ctx)
	members.recheck = append(members.recheck, still)
	if !still {
		return edgemembers.ErrNoLongerMember
	}
	return nil
}

func (members *fakeEdgeMembers) TeamspaceDeleted(_ context.Context, _ string, id string) error {
	if members.deleteErr != nil {
		return members.deleteErr
	}
	members.deleted = append(members.deleted, id)
	return nil
}

const (
	other = "0a1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f"
	// Member ids are UUIDs; a conversation room's id is built from them.
	me = "7f2d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f"
)

func admitServer(edge *fakeEdgeMembers) *Server {
	server, _ := admitServerWith(edge)
	return server
}

func admitServerWith(edge *fakeEdgeMembers) (*Server, *fakeTeamspaces) {
	directory := &fakeDirectory{membership: authclient.Membership{UserID: me, Role: "member"}}
	spaces := &fakeTeamspaces{space: teamspaces.Space{ID: "tsp_eng", CanManage: true}}
	return New(directory, &fakeThreads{}, Config{
		RunTimeout: time.Minute, EdgeMembers: edge, Teamspaces: spaces,
	}, nil), spaces
}

func admit(server *Server, kind, id string) int {
	return serve(server, http.MethodPost, "/v1/organizations/org_1/edge/rooms/"+kind+"/"+id+"/admit", "", map[string]string{"Authorization": "Bearer member-token"}).Code
}

// Each room kind is checked the way Go's own routes check it, and only then
// is the member let in on the edge.
func TestEdgeAdmitLetsAMemberIntoRoomsTheyMayBeIn(t *testing.T) {
	edge := &fakeEdgeMembers{}
	server := admitServer(edge)
	dm, _ := edgev1.DirectRoomID(me, other)
	side, _ := edgev1.SideRoomID("tsp_eng", me, other)
	for _, room := range []edgev1.Room{
		{Kind: edgev1.RoomThread, ID: "thread_1"},
		{Kind: edgev1.RoomTeamspace, ID: "tsp_eng"},
		{Kind: edgev1.RoomDM, ID: dm},
		{Kind: edgev1.RoomSide, ID: side},
	} {
		if code := admit(server, string(room.Kind), room.ID); code != http.StatusNoContent {
			t.Errorf("%s: %d", room.Kind, code)
		}
	}
	if len(edge.admitted) != 4 {
		t.Fatalf("admitted %+v", edge.admitted)
	}
	for _, membership := range edge.admitted {
		if membership.Org != "org_1" || membership.Member != me {
			t.Errorf("membership %+v", membership)
		}
	}
	if edge.admitted[1].TeamspaceID != "tsp_eng" || edge.admitted[3].TeamspaceID != "tsp_eng" || edge.admitted[0].TeamspaceID != "" {
		t.Errorf("teamspaces = %q %q %q", edge.admitted[0].TeamspaceID, edge.admitted[1].TeamspaceID, edge.admitted[3].TeamspaceID)
	}
}

func TestEdgeAdmitRefusesRoomsTheMemberCantBeIn(t *testing.T) {
	edge := &fakeEdgeMembers{}
	server := admitServer(edge)
	theirs, _ := edgev1.DirectRoomID(other, "1b2c5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f")
	unseenSide, _ := edgev1.SideRoomID("tsp_other", me, other)
	for name, room := range map[string][2]string{
		"another's thread":             {"thread", "thread_9"},
		"a teamspace they can't see":   {"teamspace", "tsp_other"},
		"someone else's DM":            {"dm", theirs},
		"a DM id out of order":         {"dm", me + "_" + other},
		"a side in a hidden teamspace": {"side", unseenSide},
		"an unknown kind":              {"lobby", "x"},
	} {
		if code := admit(server, room[0], room[1]); code != http.StatusNotFound {
			t.Errorf("%s: %d", name, code)
		}
	}
	if len(edge.admitted) != 0 {
		t.Errorf("admitted %+v", edge.admitted)
	}
}

func TestEdgeAdmitAnswersUnavailableWhenTheEdgeFails(t *testing.T) {
	if code := admit(admitServer(&fakeEdgeMembers{err: errors.New("edge down")}), "thread", "thread_1"); code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", code)
	}
}

func TestEdgeAdmitIsAbsentWithoutTheEdge(t *testing.T) {
	server := New(member(), &fakeThreads{}, Config{RunTimeout: time.Minute}, nil)
	if code := admit(server, "thread", "thread_1"); code != http.StatusNotFound {
		t.Fatalf("status = %d", code)
	}
}

// The access check runs again once the member is in, so one who lost access
// meanwhile is taken out again and told the room isn't theirs.
func TestEdgeAdmitChecksAgainAfterTheAdmission(t *testing.T) {
	edge := &fakeEdgeMembers{}
	server, spaces := admitServerWith(edge)
	edge.beforeRecheck = func() { spaces.space.ID = "tsp_gone" }
	if code := admit(server, "teamspace", "tsp_eng"); code != http.StatusNotFound {
		t.Fatalf("status = %d", code)
	}
	if len(edge.recheck) != 1 || edge.recheck[0] {
		t.Errorf("recheck = %v", edge.recheck)
	}
}

func TestDeletingATeamspaceEmptiesItsEdgeRoomsFirst(t *testing.T) {
	edge := &fakeEdgeMembers{}
	server := admitServer(edge)
	if code := serve(server, http.MethodDelete, "/v1/organizations/org_1/teamspaces/tsp_eng", "", bearer).Code; code != http.StatusNoContent {
		t.Fatalf("status = %d", code)
	}
	if len(edge.deleted) != 1 || edge.deleted[0] != "tsp_eng" {
		t.Errorf("emptied %v", edge.deleted)
	}
}

// When the edge can't be told, nothing is deleted, so the delete can be
// tried again.
func TestATeamspaceIsKeptWhenItsEdgeRoomsCantBeEmptied(t *testing.T) {
	edge := &fakeEdgeMembers{deleteErr: errors.New("edge down")}
	server, spaces := admitServerWith(edge)
	if code := serve(server, http.MethodDelete, "/v1/organizations/org_1/teamspaces/tsp_eng", "", bearer).Code; code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", code)
	}
	// Delete was never called: only the access check's Get reached teamspaces.
	if len(spaces.actors) != 1 {
		t.Errorf("teamspaces was called %d times, want only the check", len(spaces.actors))
	}
}
