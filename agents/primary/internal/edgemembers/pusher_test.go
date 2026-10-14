package edgemembers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/edgeclient"
	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

type issuer struct {
	mu     sync.Mutex
	claims []edgev1.TokenClaims
}

func (i *issuer) EdgeToken(_ context.Context, claims edgev1.TokenClaims) (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.claims = append(i.claims, claims)
	return "token", nil
}

type pushed struct {
	Path string
	Body edgev1.MembersRequest
}

// memoryStore is Memberships in memory.
type memoryStore struct {
	rows []Membership
}

func (store *memoryStore) Record(_ context.Context, membership Membership) error {
	if !slices.Contains(store.rows, membership) {
		store.rows = append(store.rows, membership)
	}
	return nil
}

func (store *memoryStore) Forget(_ context.Context, membership Membership) error {
	store.rows = slices.DeleteFunc(store.rows, func(row Membership) bool {
		return row.Org == membership.Org && row.Room == membership.Room && row.Member == membership.Member
	})
	return nil
}

func (store *memoryStore) Rooms(_ context.Context, org, member string, teamspaces []string) ([]Membership, error) {
	var found []Membership
	for _, row := range store.rows {
		if row.Org == org && row.Member == member && (len(teamspaces) == 0 || slices.Contains(teamspaces, row.TeamspaceID)) {
			found = append(found, row)
		}
	}
	return found, nil
}

func (store *memoryStore) ForgetOrganization(_ context.Context, org string) error {
	store.rows = slices.DeleteFunc(store.rows, func(row Membership) bool { return row.Org == org })
	return nil
}

func newPusher(t *testing.T, failRooms ...string) (*Pusher, *memoryStore, *issuer, *[]pushed) {
	t.Helper()
	var mu sync.Mutex
	var seen []pushed
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body edgev1.MembersRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		seen = append(seen, pushed{Path: r.URL.Path, Body: body})
		mu.Unlock()
		for _, room := range failRooms {
			if r.URL.Path == "/v1/rooms/"+room+"/members" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":"invalid_request","message":"no"}}`))
				return
			}
		}
		_ = json.NewEncoder(w).Encode(edgev1.MembersResponse{Members: []string{}})
	}))
	t.Cleanup(server.Close)
	tokens := &issuer{}
	client, err := edgeclient.New(edgeclient.Config{BaseURL: server.URL, Tokens: tokens, Attempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{}
	teamspaces := func(_ context.Context, org, team string) ([]string, error) {
		if org == "org_1" && team == "team_eng" {
			return []string{"tsp_eng"}, nil
		}
		return nil, nil
	}
	pusher, err := New(client, store, teamspaces, nil)
	if err != nil {
		t.Fatal(err)
	}
	return pusher, store, tokens, &seen
}

var (
	teamspaceRoom = edgev1.Room{Kind: edgev1.RoomTeamspace, ID: "tsp_eng"}
	sideRoom      = edgev1.Room{Kind: edgev1.RoomSide, ID: "tsp_eng_a_b"}
	threadRoom    = edgev1.Room{Kind: edgev1.RoomThread, ID: "thr_1"}
)

func TestAdmitPushesAnAddWithAMemberlessToken(t *testing.T) {
	pusher, store, tokens, seen := newPusher(t)
	if err := pusher.Admit(context.Background(), Membership{Org: "org_1", Room: teamspaceRoom, Member: "usr_a", TeamspaceID: "tsp_eng"}); err != nil {
		t.Fatal(err)
	}
	if got := (*seen)[0]; got.Path != "/v1/rooms/teamspace/tsp_eng/members" || !slices.Equal(got.Body.Add, []string{"usr_a"}) || got.Body.Members != nil {
		t.Errorf("pushed %+v", got)
	}
	claims := tokens.claims[0]
	if claims.Member != "" || claims.Org != "org_1" || claims.Room != teamspaceRoom || !slices.Equal(claims.Scopes, []edgev1.Scope{edgev1.ScopeRoomMembers}) {
		t.Errorf("claims = %+v", claims)
	}
	if len(store.rows) != 1 {
		t.Errorf("remembered %v", store.rows)
	}
}

func TestLeavingATeamRemovesItsTeamspaceRoomsOnly(t *testing.T) {
	pusher, store, _, seen := newPusher(t)
	ctx := context.Background()
	for _, membership := range []Membership{
		{Org: "org_1", Room: teamspaceRoom, Member: "usr_a", TeamspaceID: "tsp_eng"},
		{Org: "org_1", Room: sideRoom, Member: "usr_a", TeamspaceID: "tsp_eng"},
		{Org: "org_1", Room: threadRoom, Member: "usr_a"},
		{Org: "org_1", Room: teamspaceRoom, Member: "usr_b", TeamspaceID: "tsp_eng"},
	} {
		if err := pusher.Admit(ctx, membership); err != nil {
			t.Fatal(err)
		}
	}
	*seen = nil
	if err := pusher.MemberLeftTeam(ctx, "org_1", "team_eng", "usr_a"); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, push := range *seen {
		if !slices.Equal(push.Body.Remove, []string{"usr_a"}) {
			t.Errorf("pushed %+v", push)
		}
		paths = append(paths, push.Path)
	}
	slices.Sort(paths)
	if !slices.Equal(paths, []string{"/v1/rooms/side/tsp_eng_a_b/members", "/v1/rooms/teamspace/tsp_eng/members"}) {
		t.Errorf("paths = %v", paths)
	}
	left, _ := store.Rooms(ctx, "org_1", "usr_a", nil)
	if len(left) != 1 || left[0].Room != threadRoom {
		t.Errorf("still remembered %v", left)
	}
	if err := pusher.MemberLeftTeam(ctx, "org_1", "team_other", "usr_b"); err != nil {
		t.Fatal(err)
	}
}

func TestLeavingTheOrganizationRemovesEveryRoom(t *testing.T) {
	pusher, store, _, seen := newPusher(t)
	ctx := context.Background()
	_ = pusher.Admit(ctx, Membership{Org: "org_1", Room: threadRoom, Member: "usr_a"})
	_ = pusher.Admit(ctx, Membership{Org: "org_1", Room: teamspaceRoom, Member: "usr_a", TeamspaceID: "tsp_eng"})
	*seen = nil
	if err := pusher.MemberLeftOrganization(ctx, "org_1", "usr_a"); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 2 || len(store.rows) != 0 {
		t.Errorf("pushed %d, remembered %v", len(*seen), store.rows)
	}
}

// A room the edge refused stays remembered, so the event's redelivery
// tries it again; the rest are done.
func TestAFailedRemovalIsReportedAndKept(t *testing.T) {
	pusher, store, _, _ := newPusher(t, "thread/thr_1")
	ctx := context.Background()
	_ = store.Record(ctx, Membership{Org: "org_1", Room: threadRoom, Member: "usr_a"})
	_ = store.Record(ctx, Membership{Org: "org_1", Room: teamspaceRoom, Member: "usr_a", TeamspaceID: "tsp_eng"})
	err := pusher.MemberLeftOrganization(ctx, "org_1", "usr_a")
	var edgeErr *edgeclient.Error
	if !errors.As(err, &edgeErr) {
		t.Fatalf("err = %v", err)
	}
	if len(store.rows) != 1 || store.rows[0].Room != threadRoom {
		t.Errorf("remembered %v", store.rows)
	}
}

func TestAdmitRefusesWhatIsntAMembership(t *testing.T) {
	pusher, _, _, seen := newPusher(t)
	for _, membership := range []Membership{
		{Org: "", Room: threadRoom, Member: "usr_a"},
		{Org: "org_1", Room: threadRoom},
		{Org: "org_1", Room: edgev1.Room{Kind: "lobby", ID: "x"}, Member: "usr_a"},
	} {
		if err := pusher.Admit(context.Background(), membership); err == nil {
			t.Errorf("admitted %+v", membership)
		}
	}
	if len(*seen) != 0 {
		t.Error("pushed an invalid membership")
	}
}
