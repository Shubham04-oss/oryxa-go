package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
)

func (service *fakeInteractions) ActiveLeases(_ context.Context, actor primaryagents.InteractionActor, query interactions.LeaseQuery) (interactions.LeasePage, error) {
	service.actors = append(service.actors, actor)
	service.limit = query.Limit
	return interactions.LeasePage{Leases: []interactions.ActiveLease{}, ObservedAt: time.Unix(0, 0).UTC()}, nil
}

type homeLeaseLedger struct {
	fakeInteractions
	query interactions.LeaseQuery
	err   error
}

func (service *homeLeaseLedger) ActiveLeases(ctx context.Context, actor primaryagents.InteractionActor, query interactions.LeaseQuery) (interactions.LeasePage, error) {
	service.query = query
	page, _ := service.fakeInteractions.ActiveLeases(ctx, actor, query)
	page.HasMore, page.NextCursor = true, "next_position"
	return page, service.err
}

func TestHomeAccessReadUsesTrustedMemberAndPageCoverage(t *testing.T) {
	ledger := &homeLeaseLedger{}
	response := serve(slackServer(ledger, nil), http.MethodGet, "/v1/organizations/org_1/agent-leases?limit=2&cursor=position&owner_user_id=someone_else", "", bearer)
	if response.Code != http.StatusOK {
		t.Fatalf("leases=%d %s", response.Code, response.Body)
	}
	var page interactions.LeasePage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || !page.HasMore || page.NextCursor != "next_position" || page.Leases == nil {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if ledger.query.Limit != 2 || ledger.query.Cursor != "position" || len(ledger.actors) != 1 || ledger.actors[0].UserID != "user_1" || ledger.actors[0].OrganizationID != "org_1" || ledger.actors[0].Session != primaryagents.SessionMember {
		t.Fatalf("query=%+v actors=%+v", ledger.query, ledger.actors)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("member access metadata was cacheable")
	}
}

func TestHomeAccessReadFailureDoesNotClaimNoWindows(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
	}{
		{interactions.ErrConsentUnavailable, http.StatusServiceUnavailable},
		{interactions.ErrInvalid, http.StatusBadRequest},
		{errors.New("database down"), http.StatusInternalServerError},
	} {
		response := serve(slackServer(&homeLeaseLedger{err: test.err}, nil), http.MethodGet, "/v1/organizations/org_1/agent-leases", "", bearer)
		if response.Code != test.status {
			t.Fatalf("%v=%d %s", test.err, response.Code, response.Body)
		}
	}
	response := serve(slackServer(&fakeInteractions{}, nil), http.MethodGet, "/v1/organizations/org_1/agent-leases?limit=0", "", bearer)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("bad limit=%d %s", response.Code, response.Body)
	}
}
