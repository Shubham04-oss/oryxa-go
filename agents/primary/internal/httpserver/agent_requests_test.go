package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/teamspaces"
)

// List is how the server reads every listing now: the sent side from Sent,
// the rest from Inbox, each recording the caller's actor.
func (service *fakeInteractions) List(ctx context.Context, actor primaryagents.InteractionActor, query interactions.Query) ([]primaryagents.Interaction, error) {
	if query.Box == interactions.BoxSent {
		return service.Sent(ctx, actor, query.Limit)
	}
	return service.Inbox(ctx, actor, query.Limit)
}

// DecideAsOwner is the one way the server decides: as the member themself,
// in their own session. It refuses any other actor, as the ledger does, and
// records the call apart from Decide's so a route that swapped them fails.
func (service *fakeInteractions) DecideAsOwner(_ context.Context, actor primaryagents.InteractionActor, _ string, input primaryagents.InteractionDecisionInput) (primaryagents.Interaction, error) {
	service.actors = append(service.actors, actor)
	if actor.Session != primaryagents.SessionMember {
		return primaryagents.Interaction{}, interactions.ErrForbidden
	}
	service.ownerDecisions++
	service.request.Status = input.Decision
	return service.request, nil
}

// teamspaceInteractions is fakeInteractions keeping the last request body it
// was asked to create and the last listing it was asked for.
type teamspaceInteractions struct {
	fakeInteractions
	input   primaryagents.InteractionCreateInput
	creates int
	query   interactions.Query
}

func (service *teamspaceInteractions) Create(ctx context.Context, actor primaryagents.InteractionActor, token, key string, input primaryagents.InteractionCreateInput) (primaryagents.Interaction, error) {
	service.input = input
	service.creates++
	return service.fakeInteractions.Create(ctx, actor, token, key, input)
}

func (service *teamspaceInteractions) List(ctx context.Context, actor primaryagents.InteractionActor, query interactions.Query) ([]primaryagents.Interaction, error) {
	service.query = query
	return service.fakeInteractions.List(ctx, actor, query)
}

func agentRequestServer(ledger AgentInteractions) *Server {
	managed := &fakeManagedAgents{definitions: []managedagents.Definition{{ID: "agt_primary", Kind: managedagents.KindPrimary, Name: "Carl", Status: managedagents.StatusActive}}}
	return New(member(), &fakeThreads{}, Config{
		AllowedOrigins: []string{"https://app.oryxa.test"}, RunTimeout: time.Minute,
		Agents: managed, Interactions: ledger,
	}, nil)
}

// TestARequestNamesItsTeamspaceButNeverWhoApprovesIt: the teamspace reaches
// the ledger, which decides who approves from the target's standing there; a
// body that tries to choose that itself is refused before it gets that far.
func TestARequestNamesItsTeamspaceButNeverWhoApprovesIt(t *testing.T) {
	requests := &teamspaceInteractions{}
	server := agentRequestServer(requests)
	created := serve(server, http.MethodPost, "/v1/organizations/org_1/agent-requests",
		`{"target_agent_id":"agt_b","teamspace_id":"tsp_1","purpose":"Launch","task":"Review the plan."}`, bearer)
	if created.Code != http.StatusCreated || requests.input.TeamspaceID != "tsp_1" || requests.input.TargetAgentID != "agt_b" {
		t.Fatalf("create = %d %s, input=%+v", created.Code, created.Body.String(), requests.input)
	}
	chosen := serve(server, http.MethodPost, "/v1/organizations/org_1/agent-requests",
		`{"target_agent_id":"agt_b","teamspace_id":"tsp_1","approval":"agent","purpose":"Launch","task":"Review the plan."}`, bearer)
	if chosen.Code != http.StatusBadRequest || errorCode(t, chosen) != "invalid_request" || requests.creates != 1 {
		t.Fatalf("a body choosing its approval = %d %s after %d creates, want 400 and no second create", chosen.Code, chosen.Body.String(), requests.creates)
	}
}

// heldRequests is a request ledger holding one request. Reading and deciding
// it is all a decision reaches; the embedded nil Repository fails loudly on
// anything else.
type heldRequests struct {
	interactions.Repository
	request primaryagents.Interaction
}

func (ledger *heldRequests) Get(_ context.Context, id string) (primaryagents.Interaction, error) {
	if id != ledger.request.ID {
		return primaryagents.Interaction{}, interactions.ErrNotFound
	}
	return ledger.request, nil
}

func (ledger *heldRequests) Decide(_ context.Context, _ string, input primaryagents.InteractionDecisionInput, at time.Time) (primaryagents.Interaction, error) {
	ledger.request.Status = interactions.StatusRejected
	if input.Decision == interactions.DecisionApproved {
		ledger.request.Status = interactions.StatusApproved
	}
	ledger.request.Decision = &primaryagents.InteractionDecision{Decision: input.Decision, Message: input.Message, DecidedAt: at}
	ledger.request.Revision++
	return ledger.request, nil
}

// keyedLedger keeps new requests under their idempotency keys, as the store
// does: a key already used stores nothing and returns what it names.
type keyedLedger struct {
	interactions.Repository
	requests map[string]primaryagents.Interaction
}

func (ledger *keyedLedger) FindByKey(_ context.Context, organizationID, agentID, key string) (primaryagents.Interaction, error) {
	request, ok := ledger.requests[organizationID+"|"+agentID+"|"+key]
	if !ok {
		return primaryagents.Interaction{}, interactions.ErrNotFound
	}
	return request, nil
}

func (ledger *keyedLedger) Create(ctx context.Context, request primaryagents.Interaction, key string) (primaryagents.Interaction, error) {
	if stored, err := ledger.FindByKey(ctx, request.OrganizationID, request.Requester.AgentID, key); err == nil {
		return stored, nil
	}
	ledger.requests[request.OrganizationID+"|"+request.Requester.AgentID+"|"+key] = request
	return request, nil
}

// TestAReusedIdempotencyKeyIsAConflict: sent again unchanged, a request
// comes back as it was made; sent under the same key as something else, it
// is a 409 that says so, and never another request answered as 201.
func TestAReusedIdempotencyKeyIsAConflict(t *testing.T) {
	requests, err := interactions.New(interactions.Config{Repository: &keyedLedger{requests: map[string]primaryagents.Interaction{}}, Directory: graceDirectory{}})
	if err != nil {
		t.Fatal(err)
	}
	server := agentRequestServer(requests)
	withKey := map[string]string{"Authorization": "Bearer member-token", "Idempotency-Key": "side-sdc-1"}
	create := func(body string) (int, primaryagents.Interaction, string) {
		t.Helper()
		response := serve(server, http.MethodPost, "/v1/organizations/org_1/agent-requests", body, withKey)
		var created primaryagents.Interaction
		if response.Code == http.StatusCreated {
			if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			return response.Code, created, ""
		}
		return response.Code, created, errorCode(t, response)
	}
	body := `{"target_agent_id":"agt_grace","purpose":"Launch","task":"Review the plan."}`
	status, first, _ := create(body)
	if status != http.StatusCreated || first.ID == "" {
		t.Fatalf("the first create = %d %+v", status, first)
	}
	if status, again, _ := create(body); status != http.StatusCreated || again.ID != first.ID {
		t.Fatalf("the same create again = %d %+v, want 201 and %s", status, again, first.ID)
	}
	status, other, code := create(`{"target_agent_id":"agt_grace","purpose":"Launch","task":"Something else."}`)
	if status != http.StatusConflict || code != "idempotency_key_reused" || other.ID != "" {
		t.Fatalf("another request under the key = %d %s %+v, want 409 idempotency_key_reused", status, code, other)
	}
}

// unusedDirectory stands in for the organization directory, which deciding
// never reads.
type unusedDirectory struct{ primaryagents.Directory }

// TestOnlyTheMembersOwnSessionDecidesARequestHeldForThem runs the real
// ledger service behind the API. Grace asked user_1's primary agent for
// something in a teamspace, so user_1 decides: the agent's own tool path is
// refused, while user_1, signed in, approves it through the decision route.
func TestOnlyTheMembersOwnSessionDecidesARequestHeldForThem(t *testing.T) {
	ledger := &heldRequests{request: primaryagents.Interaction{
		ID: "irq_held", OrganizationID: "org_1", TeamspaceID: "tsp_1", Approval: interactions.ApprovalOwner,
		Requester: primaryagents.InteractionParty{UserID: "user_2", AgentID: "agt_grace", AgentName: "Cleo"},
		Target:    primaryagents.InteractionParty{UserID: "user_1", AgentID: "agt_primary", AgentName: "Carl"},
		Purpose:   "Launch", Task: "Review the plan.", Status: interactions.StatusPending, Revision: 1,
		ExpiresAt: time.Now().Add(time.Hour),
	}}
	requests, err := interactions.New(interactions.Config{Repository: ledger, Directory: unusedDirectory{}})
	if err != nil {
		t.Fatal(err)
	}

	agent := primaryagents.InteractionActor{OrganizationID: "org_1", UserID: "user_1", AgentID: "agt_primary", AgentName: "Carl", AgentKind: "primary"}
	if _, err := requests.Decide(context.Background(), agent, "irq_held", primaryagents.InteractionDecisionInput{
		Decision: interactions.DecisionApproved, Message: "Approved.",
	}); !errors.Is(err, interactions.ErrForbidden) || ledger.request.Status != interactions.StatusPending {
		t.Fatalf("the agent's own decision: error=%v status=%s, want ErrForbidden and nothing decided", err, ledger.request.Status)
	}

	decided := serve(agentRequestServer(requests), http.MethodPost, "/v1/organizations/org_1/agent-requests/irq_held/decision",
		`{"decision":"approved","message":"Go ahead."}`, bearer)
	var body primaryagents.Interaction
	if err := json.Unmarshal(decided.Body.Bytes(), &body); err != nil {
		t.Fatalf("decision = %d %s: %v", decided.Code, decided.Body.String(), err)
	}
	if decided.Code != http.StatusOK || body.Status != interactions.StatusApproved || body.Approval != interactions.ApprovalOwner || body.TeamspaceID != "tsp_1" {
		t.Fatalf("the member's own decision = %d %s, want 200 and an approved request held for its owner in tsp_1", decided.Code, decided.Body.String())
	}
}

// TestEveryListingNarrowsByTeamspaceAndTheOtherMember: the new listing reads
// both directions, the inbox and sent requests keep their own side, and all
// three pass ?teamspace_id= and ?with_user= through, acting as the caller.
func TestEveryListingNarrowsByTeamspaceAndTheOtherMember(t *testing.T) {
	for path, box := range map[string]string{
		"/v1/organizations/org_1/agent-requests":       interactions.BoxAll,
		"/v1/organizations/org_1/agent-requests/inbox": interactions.BoxInbox,
		"/v1/organizations/org_1/agent-requests/sent":  interactions.BoxSent,
	} {
		requests := &teamspaceInteractions{}
		listed := serve(agentRequestServer(requests), http.MethodGet, path+"?teamspace_id=tsp_1&with_user=user_2&limit=5", "", bearer)
		want := interactions.Query{Box: box, TeamspaceID: "tsp_1", WithUserID: "user_2", Limit: 5}
		if listed.Code != http.StatusOK || requests.query != want {
			t.Fatalf("GET %s = %d %s with query %+v, want 200 and %+v", path, listed.Code, listed.Body.String(), requests.query, want)
		}
		if actor := requests.actors[len(requests.actors)-1]; actor.UserID != "user_1" || actor.AgentID != "agt_primary" {
			t.Fatalf("GET %s listed as %+v, want the caller's own primary agent", path, actor)
		}
	}
}

// TestAListingTheLedgerRefusesIsABadRequest: with the real ledger service
// behind the API, a listing with the caller as the other member, or with an
// id no teamspace could have, is the caller's mistake and never reaches the
// ledger's storage.
func TestAListingTheLedgerRefusesIsABadRequest(t *testing.T) {
	requests, err := interactions.New(interactions.Config{Repository: &heldRequests{}, Directory: unusedDirectory{}})
	if err != nil {
		t.Fatal(err)
	}
	server := agentRequestServer(requests)
	for _, query := range []string{"?with_user=user_1", "?teamspace_id=" + strings.Repeat("t", 129)} {
		listed := serve(server, http.MethodGet, "/v1/organizations/org_1/agent-requests"+query, "", bearer)
		if listed.Code != http.StatusBadRequest || errorCode(t, listed) != "invalid_request" {
			t.Fatalf("GET agent-requests%s = %d %s, want 400 invalid_request", query, listed.Code, listed.Body.String())
		}
	}
}

// TestABlankFilterIsABadRequest: a filter sent blank would read as no filter
// and list every request the caller has, so on every listing it is refused
// before the ledger is asked anything.
func TestABlankFilterIsABadRequest(t *testing.T) {
	for _, path := range []string{
		"/v1/organizations/org_1/agent-requests",
		"/v1/organizations/org_1/agent-requests/inbox",
		"/v1/organizations/org_1/agent-requests/sent",
	} {
		for _, query := range []string{"?teamspace_id=", "?teamspace_id=%20", "?with_user=", "?with_user=%20%09", "?teamspace_id=tsp_1&with_user=", "?teamspace_id"} {
			requests := &teamspaceInteractions{}
			listed := serve(agentRequestServer(requests), http.MethodGet, path+query, "", bearer)
			if listed.Code != http.StatusBadRequest || errorCode(t, listed) != "invalid_request" || len(requests.actors) != 0 {
				t.Fatalf("GET %s%s = %d %s after %d ledger calls, want 400 invalid_request and none", path, query, listed.Code, listed.Body.String(), len(requests.actors))
			}
		}
	}
}

// refusingSeats refuses every seat lookup the way the teamspace does.
type refusingSeats struct{ err error }

func (seats refusingSeats) RequestSeat(context.Context, primaryagents.TeamspaceActor, primaryagents.OrganizationDirectory, string, string) (primaryagents.TeamspaceSeat, error) {
	return primaryagents.TeamspaceSeat{}, seats.err
}

// graceDirectory is user_1's view of the organization: Grace and her
// primary agent. Loading it is all a new request asks of the directory.
type graceDirectory struct{ primaryagents.Directory }

func (graceDirectory) Load(context.Context, string, string) (primaryagents.OrganizationDirectory, error) {
	return primaryagents.OrganizationDirectory{People: []primaryagents.DirectoryPerson{{
		UserID: "user_2", DisplayName: "Grace",
		Agents: []primaryagents.DirectoryAgent{{ID: "agt_grace", OwnerUserID: "user_2", Kind: "primary", Name: "Cleo"}},
	}}}, nil
}

// TestATeamspaceRefusalReachesTheCallerAsTheTeamspaceSaidIt: a request the
// teamspace refuses answers with the teamspace's own code and words, and is
// never recorded — heldRequests has no Create, so reaching it would panic.
func TestATeamspaceRefusalReachesTheCallerAsTheTeamspaceSaidIt(t *testing.T) {
	for _, testCase := range []struct {
		refusal       error
		status        int
		code, message string
	}{
		{fmt.Errorf("%w: only a current member of this teamspace's team can ask its agents for help there", teamspaces.ErrForbidden), http.StatusForbidden, "forbidden", "only a current member"},
		{fmt.Errorf("%w: that agent has no seat in this teamspace", teamspaces.ErrInvalid), http.StatusBadRequest, "invalid_request", "that agent has no seat in this teamspace"},
		{teamspaces.ErrNotFound, http.StatusNotFound, "not_found", "teamspace not found"},
		{teamspaces.ErrDisabled, http.StatusConflict, "teamspace_disabled", "teamspace is disabled"},
	} {
		requests, err := interactions.New(interactions.Config{
			Repository: &heldRequests{}, Directory: graceDirectory{}, Teamspaces: refusingSeats{err: testCase.refusal},
		})
		if err != nil {
			t.Fatal(err)
		}
		refused := serve(agentRequestServer(requests), http.MethodPost, "/v1/organizations/org_1/agent-requests",
			`{"target_agent_id":"agt_grace","teamspace_id":"tsp_1","purpose":"Launch","task":"Review the plan."}`, bearer)
		if refused.Code != testCase.status || errorCode(t, refused) != testCase.code || !strings.Contains(refused.Body.String(), testCase.message) {
			t.Errorf("%v: = %d %s, want %d %s saying %q", testCase.refusal, refused.Code, refused.Body.String(), testCase.status, testCase.code, testCase.message)
		}
	}
}

// TestTheDecisionRouteIsTheMembersOwnSession: the route decides through
// DecideAsOwner, never Decide, and every interaction route acts in the
// member's own session — the one place an actor is marked so.
func TestTheDecisionRouteIsTheMembersOwnSession(t *testing.T) {
	requests := &teamspaceInteractions{}
	server := agentRequestServer(requests)
	decided := serve(server, http.MethodPost, "/v1/organizations/org_1/agent-requests/irq_1/decision", `{"decision":"approved","message":"Go ahead."}`, bearer)
	if decided.Code != http.StatusOK || requests.ownerDecisions != 1 || requests.agentDecisions != 0 {
		t.Fatalf("decision = %d %s with %d owner and %d agent decisions, want 200 through DecideAsOwner alone",
			decided.Code, decided.Body.String(), requests.ownerDecisions, requests.agentDecisions)
	}
	serve(server, http.MethodGet, "/v1/organizations/org_1/agent-requests?limit=5", "", bearer)
	serve(server, http.MethodPost, "/v1/organizations/org_1/agent-requests", `{"target_agent_id":"agt_b","purpose":"Launch","task":"Review."}`, bearer)
	for _, actor := range requests.actors {
		if actor.Session != primaryagents.SessionMember || actor.UserID != "user_1" {
			t.Fatalf("a route acted as %+v, want user_1 in their own session", actor)
		}
	}
}
