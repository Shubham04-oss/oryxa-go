package edgedispatch

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

type roomCheck struct {
	organization string
	room         Room
	member       string
}

type fakeRooms struct {
	mu     sync.Mutex
	in     bool
	err    error
	checks []roomCheck
}

func (fake *fakeRooms) InRoom(_ context.Context, organizationID string, room Room, member string) (bool, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.checks = append(fake.checks, roomCheck{organizationID, room, member})
	return fake.in, fake.err
}

func completedRun() *fakeRun {
	return &fakeRun{id: "run_1", items: []threads.Item{
		{Type: threads.ItemMessage, Author: "krude", Text: "Done."}, {Type: threads.ItemRunCompleted},
	}}
}

// TestATeamspaceRoomDispatchesOnlyForAMemberOfItsRoom: an organization
// member who is not in the teamspace's room is refused before any thread or
// run is made, as is every teamspace dispatch when nothing can say who is in
// the room.
func TestATeamspaceRoomDispatchesOnlyForAMemberOfItsRoom(t *testing.T) {
	for name, rooms := range map[string]RoomMembers{
		"not in the room": &fakeRooms{in: false},
		"no room members": nil,
	} {
		t.Run(name, func(t *testing.T) {
			fakes := &fakeThreads{run: completedRun()}
			service := New(fakes, fakeMembers{member: true}, rooms, &fakeEdge{results: make(chan Result, 1)}, nil)
			_, err := service.Dispatch(context.Background(), validRequest())
			if !errors.Is(err, ErrNotInRoom) || !errors.Is(err, ErrNotMember) {
				t.Fatalf("Dispatch = %v, want ErrNotInRoom", err)
			}
			if len(fakes.created) != 0 || len(fakes.inputs) != 0 {
				t.Fatal("a refused dispatch made a thread or a run")
			}
		})
	}

	fakes := &fakeThreads{run: completedRun()}
	broken := &fakeRooms{err: errors.New("database down")}
	service := New(fakes, fakeMembers{member: true}, broken, &fakeEdge{results: make(chan Result, 1)}, nil)
	if _, err := service.Dispatch(context.Background(), validRequest()); err == nil || errors.Is(err, ErrNotMember) || len(fakes.created) != 0 {
		t.Fatalf("Dispatch when the room check fails = %v, created %d", err, len(fakes.created))
	}
}

// TestATeamspaceRoomsRunIsPinnedToItsTeamspace: a member in the room gets a
// run pinned to the room's teamspace, checked against that room.
func TestATeamspaceRoomsRunIsPinnedToItsTeamspace(t *testing.T) {
	fakes := &fakeThreads{run: completedRun()}
	rooms := &fakeRooms{in: true}
	edge := &fakeEdge{results: make(chan Result, 1)}
	service := New(fakes, fakeMembers{member: true}, rooms, edge, nil)
	if _, err := service.Dispatch(context.Background(), validRequest()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	receive(t, edge)
	want := roomCheck{"org_1", Room{Kind: edgev1.RoomTeamspace, ID: "room_1"}, "usr_1"}
	if len(rooms.checks) != 1 || rooms.checks[0] != want {
		t.Fatalf("room checks = %+v, want %+v", rooms.checks, want)
	}
	if got := fakes.inputs[0].TeamspaceID; got != "room_1" {
		t.Fatalf("run pinned to %q, want the room's teamspace", got)
	}
	if fakes.inputs[0].PublicationRequest == nil {
		t.Fatal("the pinned run lost its publication guard")
	}
}

// TestOtherRoomsAreNeitherCheckedNorPinned: a thread, DM or side room
// dispatches as it always has.
func TestOtherRoomsAreNeitherCheckedNorPinned(t *testing.T) {
	for _, room := range []Room{
		{Kind: edgev1.RoomThread, ID: "thr_9"},
		{Kind: edgev1.RoomDM, ID: "dm_1"},
		{Kind: edgev1.RoomSide, ID: "side_1"},
	} {
		t.Run(string(room.Kind), func(t *testing.T) {
			fakes := &fakeThreads{run: completedRun()}
			rooms := &fakeRooms{in: false}
			edge := &fakeEdge{results: make(chan Result, 1)}
			service := New(fakes, fakeMembers{member: true}, rooms, edge, nil)
			request := validRequest()
			request.Room = room
			if _, err := service.Dispatch(context.Background(), request); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			receive(t, edge)
			if len(rooms.checks) != 0 || fakes.inputs[0].TeamspaceID != "" {
				t.Fatalf("checks=%+v pin=%q", rooms.checks, fakes.inputs[0].TeamspaceID)
			}
		})
	}
}
