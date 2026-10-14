package edgedispatch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/edgeclient"
	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

type fakeIssuer struct{ claims []edgev1.TokenClaims }

func (issuer *fakeIssuer) EdgeToken(_ context.Context, claims edgev1.TokenClaims) (string, error) {
	issuer.claims = append(issuer.claims, claims)
	return "edge-token", nil
}

func TestDeliverPostsTheResultToTheRoomsVoiceAgent(t *testing.T) {
	result := Result{DispatchID: "dsp_1", Organization: "org_1", Team: "team_1", Room: Room{Kind: edgev1.RoomTeamspace, ID: "room_1"},
		Call: "call_1", Member: "usr_1", ThreadID: "thr_1", RunID: "run_1", Status: StatusCompleted, Text: "hello"}
	calls := 0
	var got Result
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/rooms/teamspace/room_1/voice/result" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer edge-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("body: %v", err)
		}
		if calls == 1 {
			// The first attempt fails as an outage would; a result is safe to
			// post again.
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	issuer := &fakeIssuer{}
	client, err := edgeclient.New(edgeclient.Config{BaseURL: server.URL, Tokens: issuer, Backoff: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := (EdgeResults{Edge: client}).Deliver(context.Background(), result); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if calls != 2 || got != result {
		t.Fatalf("after %d calls the edge got %+v", calls, got)
	}
	claims := issuer.claims[len(issuer.claims)-1]
	if claims.Org != "org_1" || claims.Team != "team_1" || claims.Room != result.Room || claims.Member != "usr_1" ||
		claims.Run != "run_1" || len(claims.Scopes) != 1 || claims.Scopes[0] != edgev1.ScopeCallSpeak {
		t.Fatalf("token claims = %+v", claims)
	}
}

func TestDeliverRefusesARoomTheEdgeCannotName(t *testing.T) {
	err := (EdgeResults{Edge: nil}).Deliver(context.Background(), Result{Room: Room{Kind: "channel", ID: "x"}})
	if err == nil {
		t.Fatal("a result for an unknown room kind was sent")
	}
}
