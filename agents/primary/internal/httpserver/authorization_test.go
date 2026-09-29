package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentteamrooms"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentteams"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/rooms"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

// This file is the authorization sweep for every route the multi-agent
// workspaces change added: agents, agent teams, agent workflows, agent
// requests (interactions), teamspaces, organization context, and
// preferences. threads.go's own routes are included too, since the sweep is
// meant to cover the whole surface once and for all.
//
// Every route in authzRoutes is driven through five independent questions:
//  1. does it refuse a caller with no credentials at all (401)?
//  2. does it refuse an agent's own token, which may never hold a member's
//     conversation or act as the member itself (403)?
//  3. does a state-changing method refuse a cookie from a site that is not
//     an allowed origin (403)?
//  4. does it act only as the authenticated caller - never an identity
//     taken from the request's path or body (the IDOR check)?
//  5. does malformed input (an unknown JSON field, a bad limit or version)
//     get answered 400 rather than crashing into a 500?
//
// TestEveryServerPatternIsAuthorizationTested is the guard: it fails if a
// route the server registers is not named in authzRoutes and is not in the
// small, explicit publicRoutePatterns allowlist. That makes it impossible to
// add a new route to server.go without also deciding, here, how it is
// authorized.

const (
	authTestOrg  = "org_1"
	authTestUser = "user_1" // matches member()'s fakeDirectory membership.
)

// unknownField decodes against every input type this server reads, since
// none of them declares a field by this name; every body-taking route must
// answer 400 to it rather than pass it through or crash.
const unknownField = `{"__not_a_real_field__":true}`

// authHarness wires every optional sub-service into one Server, so the
// sweep can drive every non-public route through a single, fully configured
// instance without any handler falling back to a 503 "not configured" answer
// that would mask the authorization check.
type authHarness struct {
	directory    *fakeDirectory
	threadsSvc   *fakeThreads
	agents       *fakeManagedAgents
	teams        *fakeAgentTeams
	teamRooms    *fakeAgentTeamRooms
	workflows    *fakeAgentWorkflows
	interactions *fakeInteractions
	teamspaces   *fakeTeamspaces
	rooms        *fakeRooms
	orgContext   *fakeOrganizationContext
	preferences  *fakePreferences
	answerGuard  *fakeAnswerChecker
	guardPolicy  *fakeAnswerGuardPreferences
	server       *Server
}

func newAuthHarness() *authHarness {
	h := &authHarness{
		directory:  member(),
		threadsSvc: &fakeThreads{run: &fakeRun{}},
		agents: &fakeManagedAgents{
			definitions: []managedagents.Definition{{ID: "agt_primary", Kind: managedagents.KindPrimary, Name: "Carl", Status: managedagents.StatusActive}},
			run:         &fakeRun{},
		},
		teams:        &fakeAgentTeams{},
		teamRooms:    &fakeAgentTeamRooms{},
		workflows:    &fakeAgentWorkflows{},
		interactions: &fakeInteractions{},
		teamspaces:   &fakeTeamspaces{},
		rooms:        &fakeRooms{},
		orgContext:   &fakeOrganizationContext{},
		preferences:  &fakePreferences{},
		answerGuard:  &fakeAnswerChecker{},
		guardPolicy:  &fakeAnswerGuardPreferences{},
	}
	h.server = New(h.directory, h.threadsSvc, Config{
		AllowedOrigins: []string{"https://app.oryxa.test"},
		Documents: DocumentsConfig{
			PublicURL: "https://documents.oryxa.test", InternalURL: "https://documents.oryxa.test",
			APIURL: "https://api.oryxa.test", JWTSecret: "example-auth-test-secret-with-32-chars",
		},
		RunTimeout:          time.Minute,
		Agents:              h.agents,
		Teams:               h.teams,
		TeamRooms:           h.teamRooms,
		Workflows:           h.workflows,
		Interactions:        h.interactions,
		Teamspaces:          h.teamspaces,
		Rooms:               h.rooms,
		OrganizationContext: h.orgContext,
		Preferences:         h.preferences,
		AnswerGuard:         h.answerGuard,

		AnswerGuardPreferences: h.guardPolicy,
	}, nil)
	return h
}

type fakeAgentTeamRooms struct{ scopes []agentteams.Scope }

func (fake *fakeAgentTeamRooms) Read(_ context.Context, scope agentteams.Scope, _ string, _ int64, _ int) ([]agentteamrooms.Message, error) {
	fake.scopes = append(fake.scopes, scope)
	return []agentteamrooms.Message{}, nil
}

func (fake *fakeAgentTeamRooms) Threads(_ context.Context, scope agentteams.Scope, _ string) (map[string]string, error) {
	fake.scopes = append(fake.scopes, scope)
	return map[string]string{}, nil
}

func (fake *fakeAgentTeamRooms) Post(_ context.Context, member threads.Member, _, _ string) ([]agentteamrooms.Message, error) {
	fake.scopes = append(fake.scopes, agentteams.Scope{OrganizationID: member.OrganizationID, UserID: member.UserID})
	return []agentteamrooms.Message{}, nil
}

// authzRoute is one route's authorization contract.
type authzRoute struct {
	// pattern is copied verbatim from server.go's server.handle(...) call;
	// TestEveryServerPatternIsAuthorizationTested cross-checks it against
	// server.Patterns().
	pattern string
	method  string
	path    string
	// body is sent as the request body for every case that reaches it. It
	// always decodes cleanly against whatever this route reads (or is ""
	// where nothing is read), so cases 1-4 all reach exactly as far as the
	// route's own logic would let a well-formed request go.
	body string
	// malformedBody/malformedQuery, when set, is a case-5 request that must
	// be answered 400 rather than passed through or 500'd. At most one is
	// set. Neither is set for a route with no limit, version or body to
	// malform.
	malformedBody  string
	malformedQuery string
	// orgOnly is true only for the two organization-agent synthesis routes,
	// whose service interface takes an organization id and nothing else:
	// the synthesis and its follow-ups are shared, organization-wide state
	// by design (like a teamspace), not a member-owned resource, so there
	// is no per-user boundary for case 4 to check - only the organization
	// one.
	orgOnly bool
	// identity reads back, from the harness, the (organization, user) the
	// relevant fake most recently recorded - exactly what the handler
	// passed as the caller's own scope or actor on the last call.
	identity func(h *authHarness) (org, user string)
}

func lastThreadsMember(h *authHarness) (string, string) {
	m := h.threadsSvc.members[len(h.threadsSvc.members)-1]
	return m.OrganizationID, m.UserID
}

func lastManagedScope(h *authHarness) (string, string) {
	s := h.agents.scopes[len(h.agents.scopes)-1]
	return s.OrganizationID, s.UserID
}

func lastManagedMember(h *authHarness) (string, string) {
	m := h.agents.members[len(h.agents.members)-1]
	return m.OrganizationID, m.UserID
}

func lastTeamScope(h *authHarness) (string, string) {
	s := h.teams.seenScopes[len(h.teams.seenScopes)-1]
	return s.OrganizationID, s.UserID
}

func lastTeamRoomScope(h *authHarness) (string, string) {
	s := h.teamRooms.scopes[len(h.teamRooms.scopes)-1]
	return s.OrganizationID, s.UserID
}

func lastWorkflowScope(h *authHarness) (string, string) {
	s := h.workflows.scopes[len(h.workflows.scopes)-1]
	return s.OrganizationID, s.UserID
}

func lastInteractionActor(h *authHarness) (string, string) {
	a := h.interactions.actors[len(h.interactions.actors)-1]
	return a.OrganizationID, a.UserID
}

func lastTeamspaceActor(h *authHarness) (string, string) {
	a := h.teamspaces.actors[len(h.teamspaces.actors)-1]
	return a.OrganizationID, a.UserID
}

func lastRoomActor(h *authHarness) (string, string) {
	a := h.rooms.actors[len(h.rooms.actors)-1]
	return a.OrganizationID, a.UserID
}

func lastOrgContextOrg(h *authHarness) (string, string) {
	return h.orgContext.organizationIDs[len(h.orgContext.organizationIDs)-1], ""
}

func lastPreferencesCall(h *authHarness) (string, string) {
	c := h.preferences.calls[len(h.preferences.calls)-1]
	return c[0], c[1]
}

// lastAnswerCheck is who the check route counted the call against. The guard
// it reads with is handed texts and nothing about who sent them, so the one
// place the caller's identity is used is the limit on how many checks a member
// makes: exactly one member, the caller, must hold a check.
func lastAnswerCheck(h *authHarness) (string, string) {
	members := h.server.checks.members()
	if len(members) != 1 {
		return fmt.Sprintf("%d members counted, want the caller alone", len(members)), ""
	}
	return members[0].organizationID, members[0].userID
}

// lastGuardPreferencesCall is who the answer guard's settings were read or
// changed for, as the fake recorded it before it did anything else.
func lastGuardPreferencesCall(h *authHarness) (string, string) {
	c := h.guardPolicy.calls[len(h.guardPolicy.calls)-1]
	return c[0], c[1]
}

// authzRoutes is every non-public route the server registers. See
// TestEveryServerPatternIsAuthorizationTested for the guard that keeps this
// list complete.
var authzRoutes = []authzRoute{
	// -- threads --------------------------------------------------------
	{
		pattern: "GET /v1/organizations/{organizationID}/threads", method: http.MethodGet,
		path: "/v1/organizations/org_1/threads", identity: lastThreadsMember,
	},
	{
		pattern: "POST /v1/organizations/{organizationID}/threads", method: http.MethodPost,
		path: "/v1/organizations/org_1/threads", body: "{}", malformedBody: unknownField, identity: lastThreadsMember,
	},
	{
		pattern: "GET /v1/organizations/{organizationID}/threads/{threadID}", method: http.MethodGet,
		path: "/v1/organizations/org_1/threads/thread_1", identity: lastThreadsMember,
	},
	{
		pattern: "DELETE /v1/organizations/{organizationID}/threads/{threadID}", method: http.MethodDelete,
		path: "/v1/organizations/org_1/threads/thread_1", identity: lastThreadsMember,
	},
	{
		pattern: "POST /v1/organizations/{organizationID}/threads/{threadID}/runs", method: http.MethodPost,
		path: "/v1/organizations/org_1/threads/thread_1/runs", body: "{}", malformedBody: unknownField, identity: lastThreadsMember,
	},
	{
		pattern: "GET /v1/organizations/{organizationID}/threads/{threadID}/artifacts/{name}", method: http.MethodGet,
		path: "/v1/organizations/org_1/threads/thread_1/artifacts/file_other.png", malformedQuery: "?version=0", identity: lastThreadsMember,
	},
	{
		pattern: "GET /v1/organizations/{organizationID}/threads/{threadID}/artifacts/{name}/editor", method: http.MethodGet,
		path: "/v1/organizations/org_1/threads/thread_1/artifacts/draft.docx/editor", identity: lastThreadsMember,
	},
	// -- managed agents ---------------------------------------------------
	{
		pattern: "GET /v1/organizations/{organizationID}/agents", method: http.MethodGet,
		path: "/v1/organizations/org_1/agents", identity: lastManagedScope,
	},
	{
		pattern: "POST /v1/organizations/{organizationID}/agents", method: http.MethodPost,
		path: "/v1/organizations/org_1/agents", body: "{}", malformedBody: unknownField, identity: lastManagedScope,
	},
	{
		pattern: "GET /v1/organizations/{organizationID}/agents/{agentID}", method: http.MethodGet,
		path: "/v1/organizations/org_1/agents/agt_other", identity: lastManagedScope,
	},
	{
		pattern: "PATCH /v1/organizations/{organizationID}/agents/{agentID}", method: http.MethodPatch,
		path: "/v1/organizations/org_1/agents/agt_other", body: "{}", malformedBody: unknownField, identity: lastManagedScope,
	},
	{
		pattern: "DELETE /v1/organizations/{organizationID}/agents/{agentID}", method: http.MethodDelete,
		path: "/v1/organizations/org_1/agents/agt_other", identity: lastManagedScope,
	},
	{
		pattern: "GET /v1/organizations/{organizationID}/agents/{agentID}/threads", method: http.MethodGet,
		path: "/v1/organizations/org_1/agents/agt_other/threads", identity: lastManagedMember,
	},
	{
		pattern: "GET /v1/organizations/{organizationID}/agents/{agentID}/threads/{threadID}", method: http.MethodGet,
		path: "/v1/organizations/org_1/agents/agt_other/threads/thread_other", identity: lastManagedMember,
	},
	{
		pattern: "POST /v1/organizations/{organizationID}/agents/{agentID}/runs", method: http.MethodPost,
		path: "/v1/organizations/org_1/agents/agt_other/runs", body: "{}", malformedBody: unknownField, identity: lastManagedMember,
	},
	// -- agent teams ------------------------------------------------------
	{
		pattern: "GET /v1/organizations/{organizationID}/agent-teams", method: http.MethodGet,
		path: "/v1/organizations/org_1/agent-teams", identity: lastTeamScope,
	},
	{
		pattern: "POST /v1/organizations/{organizationID}/agent-teams", method: http.MethodPost,
		path: "/v1/organizations/org_1/agent-teams", body: "{}", malformedBody: unknownField, identity: lastTeamScope,
	},
	{
		pattern: "GET /v1/organizations/{organizationID}/agent-teams/{teamID}", method: http.MethodGet,
		path: "/v1/organizations/org_1/agent-teams/atm_other", identity: lastTeamScope,
	},
	{
		pattern: "PATCH /v1/organizations/{organizationID}/agent-teams/{teamID}", method: http.MethodPatch,
		path: "/v1/organizations/org_1/agent-teams/atm_other", body: "{}", malformedBody: unknownField, identity: lastTeamScope,
	},
	{
		pattern: "DELETE /v1/organizations/{organizationID}/agent-teams/{teamID}", method: http.MethodDelete,
		path: "/v1/organizations/org_1/agent-teams/atm_other", identity: lastTeamScope,
	},
	{
		pattern: "GET /v1/organizations/{organizationID}/agent-teams/{teamID}/messages", method: http.MethodGet,
		path: "/v1/organizations/org_1/agent-teams/atm_other/messages", malformedQuery: "?after_seq=bad", identity: lastTeamRoomScope,
	},
	{
		pattern: "GET /v1/organizations/{organizationID}/agent-teams/{teamID}/agent-threads", method: http.MethodGet,
		path: "/v1/organizations/org_1/agent-teams/atm_other/agent-threads", identity: lastTeamRoomScope,
	},
	{
		pattern: "POST /v1/organizations/{organizationID}/agent-teams/{teamID}/messages", method: http.MethodPost,
		path: "/v1/organizations/org_1/agent-teams/atm_other/messages", body: `{"body":"Hello"}`, malformedBody: unknownField, identity: lastTeamRoomScope,
	},
	// -- agent workflows --------------------------------------------------
	{
		pattern: "GET /v1/organizations/{organizationID}/agent-workflows", method: http.MethodGet,
		path: "/v1/organizations/org_1/agent-workflows", identity: lastWorkflowScope,
	},
	{
		pattern: "POST /v1/organizations/{organizationID}/agent-workflows", method: http.MethodPost,
		path: "/v1/organizations/org_1/agent-workflows", body: "{}", malformedBody: unknownField, identity: lastWorkflowScope,
	},
	{
		pattern: "GET /v1/organizations/{organizationID}/agent-workflows/{workflowID}", method: http.MethodGet,
		path: "/v1/organizations/org_1/agent-workflows/awf_other", identity: lastWorkflowScope,
	},
	{
		pattern: "PATCH /v1/organizations/{organizationID}/agent-workflows/{workflowID}", method: http.MethodPatch,
		path: "/v1/organizations/org_1/agent-workflows/awf_other", body: "{}", malformedBody: unknownField, identity: lastWorkflowScope,
	},
	{
		pattern: "DELETE /v1/organizations/{organizationID}/agent-workflows/{workflowID}", method: http.MethodDelete,
		path: "/v1/organizations/org_1/agent-workflows/awf_other", identity: lastWorkflowScope,
	},
	{
		pattern: "POST /v1/organizations/{organizationID}/agent-workflows/{workflowID}/executions", method: http.MethodPost,
		path: "/v1/organizations/org_1/agent-workflows/awf_other/executions", body: "{}", malformedBody: unknownField, identity: lastWorkflowScope,
	},
	{
		pattern: "GET /v1/organizations/{organizationID}/agent-workflows/{workflowID}/executions", method: http.MethodGet,
		path: "/v1/organizations/org_1/agent-workflows/awf_other/executions", malformedQuery: "?limit=abc", identity: lastWorkflowScope,
	},
	{
		pattern: "GET /v1/organizations/{organizationID}/agent-workflow-executions/{executionID}", method: http.MethodGet,
		path: "/v1/organizations/org_1/agent-workflow-executions/awx_other", identity: lastWorkflowScope,
	},
	// -- agent requests (interactions) ------------------------------------
	{
		pattern: "POST /v1/organizations/{organizationID}/agent-requests", method: http.MethodPost,
		path: "/v1/organizations/org_1/agent-requests", body: "{}", malformedBody: unknownField, identity: lastInteractionActor,
	},
	{
		pattern: "GET /v1/organizations/{organizationID}/agent-requests", method: http.MethodGet,
		path: "/v1/organizations/org_1/agent-requests?teamspace_id=tsp_other&with_user=user_other", malformedQuery: "&limit=abc", identity: lastInteractionActor,
	},
	{
		pattern: "GET /v1/organizations/{organizationID}/agent-requests/inbox", method: http.MethodGet,
		path: "/v1/organizations/org_1/agent-requests/inbox", malformedQuery: "?limit=abc", identity: lastInteractionActor,
	},
	{
		pattern: "GET /v1/organizations/{organizationID}/agent-requests/sent", method: http.MethodGet,
		path: "/v1/organizations/org_1/agent-requests/sent", malformedQuery: "?limit=abc", identity: lastInteractionActor,
	},
	{
		pattern: "GET /v1/organizations/{organizationID}/agent-requests/{requestID}", method: http.MethodGet,
		path: "/v1/organizations/org_1/agent-requests/irq_other", identity: lastInteractionActor,
	},
	{
		pattern: "POST /v1/organizations/{organizationID}/agent-requests/{requestID}/decision", method: http.MethodPost,
		path: "/v1/organizations/org_1/agent-requests/irq_other/decision", body: "{}", malformedBody: unknownField, identity: lastInteractionActor,
	},
	{
		pattern: "POST /v1/organizations/{organizationID}/agent-requests/{requestID}/response", method: http.MethodPost,
		path: "/v1/organizations/org_1/agent-requests/irq_other/response", body: "{}", malformedBody: unknownField, identity: lastInteractionActor,
	},
	{
		pattern: "POST /v1/organizations/{organizationID}/agent-requests/{requestID}/review", method: http.MethodPost,
		path: "/v1/organizations/org_1/agent-requests/irq_other/review", body: `{"action":"send"}`, malformedBody: unknownField, identity: lastInteractionActor,
	},
	// -- organization agent / context -------------------------------------
	{
		pattern: "GET /v1/organizations/{organizationID}/organization-agent", method: http.MethodGet,
		path: "/v1/organizations/org_1/organization-agent", identity: lastTeamspaceActor,
	},
	{
		pattern: "PATCH /v1/organizations/{organizationID}/organization-agent", method: http.MethodPatch,
		path: "/v1/organizations/org_1/organization-agent", body: "{}", malformedBody: unknownField, identity: lastTeamspaceActor,
	},
	{
		pattern: "GET /v1/organizations/{organizationID}/organization-agent/context", method: http.MethodGet,
		path: "/v1/organizations/org_1/organization-agent/context", orgOnly: true, identity: lastOrgContextOrg,
	},
	{
		pattern: "GET /v1/organizations/{organizationID}/organization-agent/follow-ups", method: http.MethodGet,
		path: "/v1/organizations/org_1/organization-agent/follow-ups", malformedQuery: "?limit=abc", orgOnly: true, identity: lastOrgContextOrg,
	},
	// -- teamspaces ---------------------------------------------------------
	{
		pattern: "GET /v1/organizations/{organizationID}/teamspaces", method: http.MethodGet,
		path: "/v1/organizations/org_1/teamspaces", identity: lastTeamspaceActor,
	},
	{
		pattern: "POST /v1/organizations/{organizationID}/teamspaces", method: http.MethodPost,
		path: "/v1/organizations/org_1/teamspaces", body: "{}", malformedBody: unknownField, identity: lastTeamspaceActor,
	},
	{
		pattern: "GET /v1/organizations/{organizationID}/teamspaces/{teamspaceID}", method: http.MethodGet,
		path: "/v1/organizations/org_1/teamspaces/tsp_other", identity: lastTeamspaceActor,
	},
	{
		pattern: "PATCH /v1/organizations/{organizationID}/teamspaces/{teamspaceID}", method: http.MethodPatch,
		path: "/v1/organizations/org_1/teamspaces/tsp_other", body: "{}", malformedBody: unknownField, identity: lastTeamspaceActor,
	},
	{
		pattern: "DELETE /v1/organizations/{organizationID}/teamspaces/{teamspaceID}", method: http.MethodDelete,
		path: "/v1/organizations/org_1/teamspaces/tsp_other", identity: lastTeamspaceActor,
	},
	{
		pattern: "POST /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/agents", method: http.MethodPost,
		path: "/v1/organizations/org_1/teamspaces/tsp_other/agents", body: "{}", malformedBody: unknownField, identity: lastTeamspaceActor,
	},
	{
		pattern: "PATCH /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/agents/{agentID}", method: http.MethodPatch,
		path: "/v1/organizations/org_1/teamspaces/tsp_other/agents/agt_contrib_other", body: "{}", malformedBody: unknownField, identity: lastTeamspaceActor,
	},
	{
		pattern: "DELETE /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/agents/{agentID}", method: http.MethodDelete,
		path: "/v1/organizations/org_1/teamspaces/tsp_other/agents/agt_contrib_other", identity: lastTeamspaceActor,
	},
	{
		pattern: "GET /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/entries", method: http.MethodGet,
		path: "/v1/organizations/org_1/teamspaces/tsp_other/entries", malformedQuery: "?limit=abc", identity: lastTeamspaceActor,
	},
	{
		pattern: "POST /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/entries", method: http.MethodPost,
		path: "/v1/organizations/org_1/teamspaces/tsp_other/entries", body: "{}", malformedBody: unknownField, identity: lastTeamspaceActor,
	},
	// -- rooms: a teamspace read as a conversation ---------------------------
	{
		pattern: "POST /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/messages", method: http.MethodPost,
		path: "/v1/organizations/org_1/teamspaces/tsp_other/messages", body: `{"body":"hello"}`, malformedBody: unknownField, identity: lastRoomActor,
	},
	{
		pattern: "GET /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/messages", method: http.MethodGet,
		path: "/v1/organizations/org_1/teamspaces/tsp_other/messages", malformedQuery: "?limit=abc", identity: lastRoomActor,
	},
	{
		pattern: "GET /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/catchup", method: http.MethodGet,
		path: "/v1/organizations/org_1/teamspaces/tsp_other/catchup", malformedQuery: "?after=abc", identity: lastRoomActor,
	},
	// -- preferences --------------------------------------------------------
	{
		pattern: "GET /v1/organizations/{organizationID}/preferences", method: http.MethodGet,
		path: "/v1/organizations/org_1/preferences", identity: lastPreferencesCall,
	},
	{
		pattern: "PUT /v1/organizations/{organizationID}/preferences", method: http.MethodPut,
		path: "/v1/organizations/org_1/preferences", body: `{"app_updates":true}`, malformedBody: unknownField, identity: lastPreferencesCall,
	},
	// -- answer guard: reading an answer before a member is shown it ----------
	{
		pattern: "POST /v1/organizations/{organizationID}/answer-guard/check", method: http.MethodPost,
		path: "/v1/organizations/org_1/answer-guard/check", body: `{"texts":["Hello"]}`, malformedBody: unknownField, identity: lastAnswerCheck,
	},
	// -- answer guard: a member's own levels ----------------------------------
	{
		pattern: "GET /v1/organizations/{organizationID}/answer-guard/preferences", method: http.MethodGet,
		path: "/v1/organizations/org_1/answer-guard/preferences", identity: lastGuardPreferencesCall,
	},
	{
		pattern: "PUT /v1/organizations/{organizationID}/answer-guard/preferences", method: http.MethodPut,
		path: "/v1/organizations/org_1/answer-guard/preferences", body: `{"owner":"strict"}`, malformedBody: unknownField, identity: lastGuardPreferencesCall,
	},
}

// publicRoutePatterns lists every server.Patterns() entry that is
// deliberately reachable without a member's own credentials, with why. Keep
// this list short: anything added here skips the entire sweep above.
var publicRoutePatterns = map[string]string{
	"GET /healthz": "a liveness probe the host's load balancer calls before any caller can be identified; it answers 204 with no body and touches no organization or member state.",
	"GET /v1/organizations/{organizationID}/threads/{threadID}/artifacts/{name}/editor-source":    "a document server fetch authenticated by a signed, expiring, resource-scoped ticket, tested in documents_test.go; it accepts no member cookie.",
	"POST /v1/organizations/{organizationID}/threads/{threadID}/artifacts/{name}/editor-callback": "a document server save authenticated by both a signed, expiring, resource-scoped ticket and an ONLYOFFICE JWT, tested in documents_test.go; it accepts no member cookie.",
}

// TestEveryServerPatternIsAuthorizationTested is the coverage guard: every
// pattern the server registers must either appear in authzRoutes (and so go
// through the full sweep below) or be named in publicRoutePatterns with a
// reason. A future route with neither fails this test, on purpose.
func TestEveryServerPatternIsAuthorizationTested(t *testing.T) {
	if len(authzRoutes) != 61 {
		t.Fatalf("authzRoutes has %d entries, want 61 - update this test if a route was intentionally added or removed", len(authzRoutes))
	}
	covered := map[string]bool{}
	for _, row := range authzRoutes {
		if covered[row.pattern] {
			t.Errorf("pattern %q is listed twice in authzRoutes", row.pattern)
		}
		covered[row.pattern] = true
	}
	for pattern, reason := range publicRoutePatterns {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("public pattern %q has no reason on record", pattern)
		}
		if covered[pattern] {
			t.Errorf("pattern %q is both authorization-tested and allowlisted as public - pick one", pattern)
		}
	}

	h := newAuthHarness()
	registered := map[string]bool{}
	for _, pattern := range h.server.Patterns() {
		registered[pattern] = true
		if covered[pattern] {
			continue
		}
		if _, ok := publicRoutePatterns[pattern]; ok {
			continue
		}
		t.Errorf("route %q has no authorization test and is not in publicRoutePatterns: add it to authzRoutes in authorization_test.go, or allowlist it with a reason", pattern)
	}
	// And the reverse: every table entry must name a route the server
	// actually registers, so a typo or a renamed pattern cannot silently
	// stop covering the real route it was meant to.
	for _, row := range authzRoutes {
		if !registered[row.pattern] {
			t.Errorf("authzRoutes has %q, which the server does not register - a typo, or a stale entry for a removed route", row.pattern)
		}
	}
}

// TestEveryRouteRefusesAnUnauthenticatedCaller is case 1: no Authorization
// header and no access cookie must answer 401 on every route, regardless of
// method or what it acts on.
func TestEveryRouteRefusesAnUnauthenticatedCaller(t *testing.T) {
	h := newAuthHarness()
	for _, row := range authzRoutes {
		t.Run(row.pattern, func(t *testing.T) {
			response := serve(h.server, row.method, row.path, "", nil)
			if response.Code != http.StatusUnauthorized || errorCode(t, response) != "unauthorized" {
				t.Errorf("%s %s with no credentials = %d %s, want 401 unauthorized", row.method, row.path, response.Code, response.Body.String())
			}
		})
	}
}

// TestEveryRouteRefusesAnAgentActingForAMember is case 2: an agent's own
// token identifies it as acting for a member, not as the member - and every
// one of these routes acts for the member (or the member's own persistent
// agent identity), never for an arbitrary agent caller directly.
func TestEveryRouteRefusesAnAgentActingForAMember(t *testing.T) {
	h := newAuthHarness()
	h.directory.membership.Agent = true
	for _, row := range authzRoutes {
		t.Run(row.pattern, func(t *testing.T) {
			response := serve(h.server, row.method, row.path, row.body, bearer)
			if response.Code != http.StatusForbidden || errorCode(t, response) != "forbidden" {
				t.Errorf("%s %s with an agent's own token = %d %s, want 403 forbidden", row.method, row.path, response.Code, response.Body.String())
			}
		})
	}
}

// TestEveryStateChangingRouteRefusesACrossSiteCookie is case 3: a cookie
// alone must never let another site change something in the member's name.
// Reading needs no origin (a GET is exempt, like every other route in this
// server), but every POST/PUT/PATCH/DELETE below must refuse it.
func TestEveryStateChangingRouteRefusesACrossSiteCookie(t *testing.T) {
	h := newAuthHarness()
	headers := map[string]string{"Cookie": "cookie-token", "Origin": "https://evil.test"}
	tested := 0
	for _, row := range authzRoutes {
		if row.method == http.MethodGet {
			continue
		}
		tested++
		t.Run(row.pattern, func(t *testing.T) {
			response := serve(h.server, row.method, row.path, row.body, headers)
			if response.Code != http.StatusForbidden || errorCode(t, response) != "forbidden" {
				t.Errorf("%s %s from a cross-site cookie = %d %s, want 403 forbidden", row.method, row.path, response.Code, response.Body.String())
			}
		})
	}
	if tested != 31 {
		t.Fatalf("tested %d state-changing routes, want 31 - a route's method changed without updating this expectation", tested)
	}
}

// TestEveryRoutePassesOnlyTheAuthenticatedCallersIdentity is case 4, the
// IDOR check: no matter what resource id sits in the path or what a body
// contains, the scope or actor reaching the underlying service must always
// be the authenticated caller's own organization and user id - never one
// taken from the request itself. Every fake in this package records the
// scope or actor it was called with before it does anything else (even
// before deciding a resource is not found), so this holds regardless of
// whether the dummy ids used below happen to name a real fixture.
func TestEveryRoutePassesOnlyTheAuthenticatedCallersIdentity(t *testing.T) {
	h := newAuthHarness()
	for _, row := range authzRoutes {
		t.Run(row.pattern, func(t *testing.T) {
			response := serve(h.server, row.method, row.path, row.body, bearer)
			if response.Code >= 500 {
				t.Fatalf("%s %s = %d %s, a 500 means the call never reached the fake to record an identity", row.method, row.path, response.Code, response.Body.String())
			}
			org, user := row.identity(h)
			if org != authTestOrg {
				t.Errorf("%s %s scoped its call to organization %q, want the caller's own %q - an id leaked in from the path or body", row.method, row.path, org, authTestOrg)
			}
			if !row.orgOnly && user != authTestUser {
				t.Errorf("%s %s scoped its call to user %q, want the authenticated caller's own %q - an id leaked in from the path or body (IDOR)", row.method, row.path, user, authTestUser)
			}
		})
	}
}

// TestEveryRouteWithInputRefusesMalformedInputWith400 is case 5: an unknown
// JSON field, or an unparsable limit or version, must be answered 400
// invalid_request rather than passed through or crashing into a 500. Routes
// with neither a body nor a limit/version query parameter have nothing to
// malform and are skipped (case 4 above still covers their identity, and
// case 1-3 still cover their authorization).
func TestEveryRouteWithInputRefusesMalformedInputWith400(t *testing.T) {
	h := newAuthHarness()
	tested := 0
	for _, row := range authzRoutes {
		if row.malformedBody == "" && row.malformedQuery == "" {
			continue
		}
		tested++
		t.Run(row.pattern, func(t *testing.T) {
			path, body := row.path, row.body
			if row.malformedQuery != "" {
				path = row.path + row.malformedQuery
			} else {
				body = row.malformedBody
			}
			response := serve(h.server, row.method, path, body, bearer)
			if response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_request" {
				t.Errorf("%s %s with malformed input = %d %s, want 400 invalid_request", row.method, path, response.Code, response.Body.String())
			}
		})
	}
	if tested != 35 {
		t.Fatalf("tested %d routes for malformed input, want 35 - a route gained or lost a body/limit/version without updating this expectation", tested)
	}
}

// fakeRooms records the actor every room call was made with, which is what
// the sweep asserts: a room acts for the caller the directory resolved, never
// for an id taken from the path or the body.
type fakeRooms struct {
	actors []rooms.Actor
}

func (f *fakeRooms) Post(_ context.Context, actor rooms.Actor, _, _ string) ([]rooms.Message, error) {
	f.actors = append(f.actors, actor)
	return []rooms.Message{{Seq: 1, Body: "hello"}}, nil
}

func (f *fakeRooms) Read(_ context.Context, actor rooms.Actor, _ string, _ int64, _ int) ([]rooms.Message, []rooms.Seat, error) {
	f.actors = append(f.actors, actor)
	return nil, nil, nil
}

func (f *fakeRooms) Catchup(_ context.Context, actor rooms.Actor, _ string, _ int64) (rooms.Brief, []rooms.Message, error) {
	f.actors = append(f.actors, actor)
	return rooms.Brief{}, nil, nil
}
