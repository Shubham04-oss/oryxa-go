package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/edgedispatch"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/rooms"
	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

func notesClaims() edgev1.TokenClaims {
	return edgev1.TokenClaims{Issuer: edgev1.IssuerEdge, Audience: edgev1.AudienceAgents, Org: "org_1", Team: "team_1",
		Room: edgev1.Room{Kind: edgev1.RoomTeamspace, ID: "tsp_1"}, Member: "usr_1", ID: "jti_1",
		Scopes: []edgev1.Scope{edgev1.ScopeLogWrite}}
}

const notesBody = `{"call_id":"call_1","duration_ms":754000,"decisions":["Ship on the 14th"],"actions":[{"text":"Send the price list","owner_user_id":"usr_2"}]}`

func postNotes(server *Server, path, authorization, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder
}

func notesServer(tokens *fakeEdgeTokens, roomService *fakeRooms) *Server {
	return New(nil, nil, Config{EdgeDispatch: &EdgeDispatch{Tokens: tokens, Dispatcher: &fakeDispatcher{}}, Rooms: roomService}, nil)
}

func TestTheEdgeLeavesAParleysNotesInTheTokensRoom(t *testing.T) {
	roomService := &fakeRooms{}
	server := notesServer(&fakeEdgeTokens{claims: notesClaims()}, roomService)
	first := postNotes(server, "/v1/edge/teamspaces/tsp_1/parley-notes", "Bearer t1", notesBody)
	if first.Code != http.StatusCreated || !strings.Contains(first.Body.String(), `"kind":"parley_notes"`) {
		t.Fatalf("first = %d %s", first.Code, first.Body)
	}
	again := postNotes(server, "/v1/edge/teamspaces/tsp_1/parley-notes", "Bearer t2", notesBody)
	if again.Code != http.StatusOK {
		t.Fatalf("the same notes again = %d %s, want 200", again.Code, again.Body)
	}
	if roomService.notesFor[0] != "org_1/tsp_1" || roomService.notes[0].CallID != "call_1" || roomService.notes[0].Actions[0].OwnerUserID != "usr_2" {
		t.Fatalf("posted %+v in %v", roomService.notes, roomService.notesFor)
	}
}

func TestTheEdgesParleyNotesAreRefused(t *testing.T) {
	other := func(mutate func(*edgev1.TokenClaims)) edgev1.TokenClaims {
		claims := notesClaims()
		mutate(&claims)
		return claims
	}
	for name, test := range map[string]struct {
		authorization string
		tokens        *fakeEdgeTokens
		path, body    string
		status        int
	}{
		"no token":         {"", &fakeEdgeTokens{claims: notesClaims()}, "/v1/edge/teamspaces/tsp_1/parley-notes", notesBody, http.StatusUnauthorized},
		"a member's token": {"Bearer member-token", &fakeEdgeTokens{err: edgedispatch.ErrUnauthorized}, "/v1/edge/teamspaces/tsp_1/parley-notes", notesBody, http.StatusUnauthorized},
		"no log:write": {"Bearer x", &fakeEdgeTokens{claims: other(func(c *edgev1.TokenClaims) {
			c.Scopes = []edgev1.Scope{edgev1.ScopeCallSpeak}
		})}, "/v1/edge/teamspaces/tsp_1/parley-notes", notesBody, http.StatusForbidden},
		"another teamspace": {"Bearer x", &fakeEdgeTokens{claims: notesClaims()}, "/v1/edge/teamspaces/tsp_2/parley-notes", notesBody, http.StatusForbidden},
		"a thread room of the same id": {"Bearer x", &fakeEdgeTokens{claims: other(func(c *edgev1.TokenClaims) {
			c.Room.Kind = edgev1.RoomThread
		})}, "/v1/edge/teamspaces/tsp_1/parley-notes", notesBody, http.StatusForbidden},
		"an unknown field": {"Bearer x", &fakeEdgeTokens{claims: notesClaims()}, "/v1/edge/teamspaces/tsp_1/parley-notes", `{"call_id":"call_1","author":"usr_1"}`, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			roomService := &fakeRooms{}
			recorder := postNotes(notesServer(test.tokens, roomService), test.path, test.authorization, test.body)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.status, recorder.Body)
			}
			if len(roomService.notes) != 0 {
				t.Fatal("refused notes reached the room")
			}
		})
	}
}

func TestTheEdgesParleyNotesAreAbsentUnlessConfigured(t *testing.T) {
	recorder := postNotes(New(nil, nil, Config{Rooms: &fakeRooms{}}, nil), "/v1/edge/teamspaces/tsp_1/parley-notes", "Bearer x", notesBody)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d", recorder.Code)
	}
	if status, code := ErrorCode(rooms.ErrPaused); status != http.StatusConflict || code != "teamspace_disabled" {
		t.Fatalf("a paused room's refusal = %d %s", status, code)
	}
}
