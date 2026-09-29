// Package httpserver is the agents service's HTTP API: a member's threads
// with Krude, and the runs that answer them, streamed as server-sent events.
// It decides who the caller is by asking auth, and turns the threads
// service's errors into the documented error codes.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentteamrooms"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentteams"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentworkflows"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/organizationagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/rooms"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/teamspaces"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
	"github.com/Shubham04-oss/oryxa-go/internal/cleantext"
	"github.com/Shubham04-oss/oryxa-go/internal/cliscope"
)

const maxJSONBytes = 256 << 10

// keepAliveInterval is how often a quiet stream sends a comment, so a proxy
// that closes idle connections does not cut a run off while Krude works.
const keepAliveInterval = 15 * time.Second

// streamGrace is how much longer than a run its stream may stay open, for
// the run's last items to go out after the run itself stops.
const streamGrace = 30 * time.Second

// Directory is auth, deciding who a caller is in an organization.
// authclient.Client implements it.
type Directory interface {
	Membership(ctx context.Context, organizationID string, credentials authclient.Credentials) (authclient.Membership, error)
}

// Threads is the run API beneath HTTP. threads.Service implements it, through
// ForThreads, which maps Create and List to CreateFrom and ListFrom.
type Threads interface {
	Create(ctx context.Context, member threads.Member, origin string) (threads.Thread, error)
	List(ctx context.Context, member threads.Member, origin string) ([]threads.Thread, error)
	Get(ctx context.Context, member threads.Member, threadID string) (threads.Detail, error)
	Delete(ctx context.Context, member threads.Member, threadID string) error
	StartRun(ctx context.Context, member threads.Member, threadID string, input threads.RunInput) (Run, error)
	Artifact(ctx context.Context, member threads.Member, threadID, name string, version int64) (threads.File, error)
	SaveArtifact(ctx context.Context, member threads.Member, threadID, name string, expectedVersion int64, data []byte, mimeType string) (int64, error)
}

// Run is a started run. *threads.Run implements it.
type Run interface {
	ID() string
	Execute(ctx context.Context, emit func(threads.Item) error)
	Discard(ctx context.Context, reason string)
}

// ManagedAgents is the member-owned agent registry and its execution adapter.
// managedagents.Service implements it.
type ManagedAgents interface {
	Primary(context.Context, managedagents.Scope) (managedagents.Definition, error)
	List(context.Context, managedagents.Scope) ([]managedagents.Definition, error)
	Get(context.Context, managedagents.Scope, string) (managedagents.Definition, error)
	Create(context.Context, managedagents.Scope, managedagents.CreateInput) (managedagents.Definition, error)
	Update(context.Context, managedagents.Scope, string, managedagents.UpdateInput) (managedagents.Definition, error)
	Delete(context.Context, managedagents.Scope, string) error
	ListThreads(context.Context, threads.Member, string) ([]threads.Thread, error)
	GetThread(context.Context, threads.Member, string, string) (threads.Detail, error)
	StartRun(context.Context, threads.Member, string, string, threads.RunInput) (managedagents.StartedRun, error)
}

// AgentTeams is the member-owned primary-plus-specialists team catalogue.
type AgentTeams interface {
	List(context.Context, agentteams.Scope) ([]agentteams.Team, error)
	Get(context.Context, agentteams.Scope, string) (agentteams.Team, error)
	Create(context.Context, agentteams.Scope, agentteams.CreateInput) (agentteams.Team, error)
	Update(context.Context, agentteams.Scope, string, agentteams.UpdateInput) (agentteams.Team, error)
	Delete(context.Context, agentteams.Scope, string) error
}

type AgentTeamRooms interface {
	Read(context.Context, agentteams.Scope, string, int64, int) ([]agentteamrooms.Message, error)
	Threads(context.Context, agentteams.Scope, string) (map[string]string, error)
	Post(context.Context, threads.Member, string, string) ([]agentteamrooms.Message, error)
}

// AgentWorkflows is the durable Step Functions-backed workflow catalogue and
// execution ledger exposed to a member.
type AgentWorkflows interface {
	List(context.Context, agentworkflows.Scope) ([]agentworkflows.Workflow, error)
	Get(context.Context, agentworkflows.Scope, string) (agentworkflows.Workflow, error)
	Create(context.Context, agentworkflows.Scope, agentworkflows.CreateInput) (agentworkflows.Workflow, error)
	Update(context.Context, agentworkflows.Scope, string, agentworkflows.UpdateInput) (agentworkflows.Workflow, error)
	Delete(context.Context, agentworkflows.Scope, string) error
	Start(context.Context, agentworkflows.Scope, string) (agentworkflows.Execution, error)
	ListExecutions(context.Context, agentworkflows.Scope, string, int) ([]agentworkflows.Execution, error)
	GetExecution(context.Context, agentworkflows.Scope, string) (agentworkflows.Execution, error)
}

// AgentInteractions is the cross-user request ledger. interactions.Service
// implements it; the HTTP actor is always the member's primary agent. A
// decision made here is the member's own, made in their signed-in session
// (agent tokens are refused), so it goes through DecideAsOwner: the only way
// a request held for its owner is decided.
type AgentInteractions interface {
	Create(context.Context, primaryagents.InteractionActor, string, string, primaryagents.InteractionCreateInput) (primaryagents.Interaction, error)
	List(context.Context, primaryagents.InteractionActor, interactions.Query) ([]primaryagents.Interaction, error)
	Get(context.Context, primaryagents.InteractionActor, string) (primaryagents.Interaction, error)
	// DecideAsOwner takes the member's own token, which approving a request
	// made in a teamspace reads that teamspace with.
	DecideAsOwner(context.Context, primaryagents.InteractionActor, string, string, primaryagents.InteractionDecisionInput) (primaryagents.Interaction, error)
	Respond(context.Context, primaryagents.InteractionActor, string, primaryagents.InteractionResponseInput) (primaryagents.Interaction, error)
	// ReviewAsOwner is the member's review, in their own session, of an
	// answer the answer guard held for them: the only way one is reviewed.
	// It takes the member's own token, which sending the answer to a
	// request made in a teamspace reads that teamspace with.
	ReviewAsOwner(context.Context, primaryagents.InteractionActor, string, string, primaryagents.InteractionReviewInput) (primaryagents.Interaction, error)
}

// Teamspaces is the shared multi-user agent-space catalogue. Authorization is
// resolved from the live human-team directory on every call.
type Teamspaces interface {
	OrganizationAgent(context.Context, teamspaces.Actor) (teamspaces.OrganizationAgent, error)
	PreviewOrganizationAgent(context.Context, teamspaces.Actor) (teamspaces.OrganizationAgent, error)
	UpdateOrganizationAgent(context.Context, teamspaces.Actor, teamspaces.OrganizationAgentUpdate) (teamspaces.OrganizationAgent, error)
	List(context.Context, teamspaces.Actor) ([]teamspaces.Space, error)
	Get(context.Context, teamspaces.Actor, string) (teamspaces.Space, error)
	Create(context.Context, teamspaces.Actor, teamspaces.CreateInput) (teamspaces.Space, error)
	Update(context.Context, teamspaces.Actor, string, teamspaces.UpdateInput) (teamspaces.Space, error)
	Delete(context.Context, teamspaces.Actor, string) error
	AddContribution(context.Context, teamspaces.Actor, string, teamspaces.ContributionInput) (teamspaces.Space, error)
	UpdateContribution(context.Context, teamspaces.Actor, string, string, teamspaces.ContributionUpdate) (teamspaces.Space, error)
	DeleteContribution(context.Context, teamspaces.Actor, string, string) (teamspaces.Space, error)
	PublishEntry(context.Context, teamspaces.Actor, string, teamspaces.PublishEntryInput) (teamspaces.Entry, error)
	ListEntries(context.Context, teamspaces.Actor, string, int) ([]teamspaces.Entry, error)
}

type OrganizationAgentContext interface {
	Synthesis(context.Context, string) (organizationagents.Synthesis, error)
	ListFollowUps(context.Context, string, int) ([]organizationagents.FollowUp, error)
}

// Preferences keeps what a member chose about Krude's updates from their
// apps. wakeups.Store implements it.
type Preferences interface {
	AppUpdates(ctx context.Context, organizationID, memberID string) (bool, error)
	SetAppUpdates(ctx context.Context, organizationID, memberID string, on bool) error
}

// Rooms is a teamspace read and written as a conversation: people and their
// agents talking in one place. rooms.Service implements it.
type Rooms interface {
	Post(ctx context.Context, actor rooms.Actor, roomID, body string) ([]rooms.Message, error)
	Read(ctx context.Context, actor rooms.Actor, roomID string, afterSeq int64, limit int) ([]rooms.Message, []rooms.Seat, error)
	Catchup(ctx context.Context, actor rooms.Actor, roomID string, afterSeq int64) (rooms.Brief, []rooms.Message, error)
}

// Config is how the server runs.
type Config struct {
	// Documents configures the external ONLYOFFICE Docs editing service.
	Documents DocumentsConfig
	// AllowedOrigins may send cookie-authenticated requests that change
	// something, such as starting a run.
	AllowedOrigins []string
	// RunTimeout bounds one run, so a stream's own deadline can outlast it.
	RunTimeout time.Duration
	// Unavailable, when set, is why the service cannot run agents here: every
	// thread route answers 503 with it, and the health check still passes.
	Unavailable string
	// Preferences keeps what a member chose about updates from their apps;
	// nil answers that none are kept here.
	Preferences Preferences
	// Agents enables persistent member-owned agent routes. It may be nil only
	// when Unavailable is set or in a host that intentionally exposes only the
	// legacy primary-thread API.
	Agents ManagedAgents
	// Teams enables persistent single-user multi-agent team routes.
	Teams AgentTeams
	// TeamRooms is the shared conversation for each member-owned agent team.
	TeamRooms AgentTeamRooms
	// Workflows enables durable manual and scheduled team workflows.
	Workflows AgentWorkflows
	// Interactions enables the durable cross-user request API.
	Interactions AgentInteractions
	// Teamspaces enables the organization agent, member whisperers, shared
	// team workspaces, and explicit specialist contributions.
	Teamspaces Teamspaces
	// Rooms serves the conversation inside a teamspace. Nil leaves the room
	// routes answering that they are not configured.
	Rooms Rooms
	// OrganizationContext exposes the durable shared synthesis and proposed
	// follow-ups produced by the singleton organization agent.
	OrganizationContext OrganizationAgentContext
	// AnswerGuard reads the texts of an answer a member's side conversation is
	// about to show them, for the answer-guard check route. Nil means no
	// guardrail is configured: the route then checks nothing, and says so. It
	// is set only when there is one; a nil *answerguard.Guard in the interface
	// would not read as none.
	AnswerGuard AnswerChecker
	// AnswerGuardPreferences keeps each member's own answer guard levels, and
	// tells what applies to their answers once their organization's minimum is
	// counted in. Nil answers that none are kept here. It is set only when
	// there is one; a nil pointer in the interface is read as none, but a
	// caller should not lean on that.
	AnswerGuardPreferences AnswerGuardPreferences
}

// Server serves the API.
type Server struct {
	documents           DocumentsConfig
	directory           Directory
	threads             Threads
	agents              ManagedAgents
	teams               AgentTeams
	teamRooms           AgentTeamRooms
	workflows           AgentWorkflows
	interactions        AgentInteractions
	teamspaces          Teamspaces
	rooms               Rooms
	organizationContext OrganizationAgentContext
	answerGuard         AnswerChecker
	guardPreferences    AnswerGuardPreferences
	checks              *checkLimiter
	allowedOrigins      map[string]struct{}
	runTimeout          time.Duration
	unavailable         string
	preferences         Preferences
	logger              *slog.Logger
	mux                 *http.ServeMux
	patterns            []string
}

// New returns the server. directory and service may be nil only when
// config.Unavailable says why.
func New(directory Directory, service Threads, config Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	server := &Server{
		documents:           config.Documents,
		directory:           directory,
		threads:             service,
		agents:              config.Agents,
		teams:               config.Teams,
		teamRooms:           config.TeamRooms,
		workflows:           config.Workflows,
		interactions:        config.Interactions,
		teamspaces:          config.Teamspaces,
		rooms:               config.Rooms,
		organizationContext: config.OrganizationContext,
		answerGuard:         config.AnswerGuard,
		guardPreferences:    keptGuardPreferences(config.AnswerGuardPreferences),
		checks:              newCheckLimiter(checksPerWindow, checkWindow),
		allowedOrigins:      map[string]struct{}{},
		runTimeout:          config.RunTimeout,
		unavailable:         config.Unavailable,
		preferences:         config.Preferences,
		logger:              logger,
		mux:                 http.NewServeMux(),
	}
	for _, origin := range config.AllowedOrigins {
		if parsed, err := url.Parse(origin); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			server.allowedOrigins[parsed.Scheme+"://"+parsed.Host] = struct{}{}
		}
	}

	server.handle("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	server.handle("GET /v1/organizations/{organizationID}/threads", server.listThreads)
	server.handle("POST /v1/organizations/{organizationID}/threads", server.createThread)
	server.handle("GET /v1/organizations/{organizationID}/threads/{threadID}", server.getThread)
	server.handle("DELETE /v1/organizations/{organizationID}/threads/{threadID}", server.deleteThread)
	server.handle("POST /v1/organizations/{organizationID}/threads/{threadID}/runs", server.run)
	server.handle("GET /v1/organizations/{organizationID}/threads/{threadID}/artifacts/{name}", server.artifact)
	server.handle("GET /v1/organizations/{organizationID}/threads/{threadID}/artifacts/{name}/editor", server.documentEditor)
	server.handle("GET /v1/organizations/{organizationID}/threads/{threadID}/artifacts/{name}/editor-source", server.documentSource)
	server.handle("POST /v1/organizations/{organizationID}/threads/{threadID}/artifacts/{name}/editor-callback", server.documentCallback)
	server.handle("GET /v1/organizations/{organizationID}/agents", server.listAgents)
	server.handle("POST /v1/organizations/{organizationID}/agents", server.createAgent)
	server.handle("GET /v1/organizations/{organizationID}/agents/{agentID}", server.getAgent)
	server.handle("PATCH /v1/organizations/{organizationID}/agents/{agentID}", server.updateAgent)
	server.handle("DELETE /v1/organizations/{organizationID}/agents/{agentID}", server.deleteAgent)
	server.handle("GET /v1/organizations/{organizationID}/agents/{agentID}/threads", server.listAgentThreads)
	server.handle("GET /v1/organizations/{organizationID}/agents/{agentID}/threads/{threadID}", server.getAgentThread)
	server.handle("POST /v1/organizations/{organizationID}/agents/{agentID}/runs", server.runAgent)
	server.handle("GET /v1/organizations/{organizationID}/agent-teams", server.listAgentTeams)
	server.handle("POST /v1/organizations/{organizationID}/agent-teams", server.createAgentTeam)
	server.handle("GET /v1/organizations/{organizationID}/agent-teams/{teamID}", server.getAgentTeam)
	server.handle("PATCH /v1/organizations/{organizationID}/agent-teams/{teamID}", server.updateAgentTeam)
	server.handle("DELETE /v1/organizations/{organizationID}/agent-teams/{teamID}", server.deleteAgentTeam)
	server.handle("GET /v1/organizations/{organizationID}/agent-teams/{teamID}/messages", server.readAgentTeamRoom)
	server.handle("GET /v1/organizations/{organizationID}/agent-teams/{teamID}/agent-threads", server.agentTeamRoomThreads)
	server.handle("POST /v1/organizations/{organizationID}/agent-teams/{teamID}/messages", server.postAgentTeamRoom)
	server.handle("GET /v1/organizations/{organizationID}/agent-workflows", server.listAgentWorkflows)
	server.handle("POST /v1/organizations/{organizationID}/agent-workflows", server.createAgentWorkflow)
	server.handle("GET /v1/organizations/{organizationID}/agent-workflows/{workflowID}", server.getAgentWorkflow)
	server.handle("PATCH /v1/organizations/{organizationID}/agent-workflows/{workflowID}", server.updateAgentWorkflow)
	server.handle("DELETE /v1/organizations/{organizationID}/agent-workflows/{workflowID}", server.deleteAgentWorkflow)
	server.handle("POST /v1/organizations/{organizationID}/agent-workflows/{workflowID}/executions", server.startAgentWorkflow)
	server.handle("GET /v1/organizations/{organizationID}/agent-workflows/{workflowID}/executions", server.listAgentWorkflowExecutions)
	server.handle("GET /v1/organizations/{organizationID}/agent-workflow-executions/{executionID}", server.getAgentWorkflowExecution)
	server.handle("POST /v1/organizations/{organizationID}/agent-requests", server.createInteraction)
	server.handle("GET /v1/organizations/{organizationID}/agent-requests", server.listAgentRequests)
	server.handle("GET /v1/organizations/{organizationID}/agent-requests/inbox", server.interactionInbox)
	server.handle("GET /v1/organizations/{organizationID}/agent-requests/sent", server.interactionSent)
	server.handle("GET /v1/organizations/{organizationID}/agent-requests/{requestID}", server.getInteraction)
	server.handle("POST /v1/organizations/{organizationID}/agent-requests/{requestID}/decision", server.decideInteraction)
	server.handle("POST /v1/organizations/{organizationID}/agent-requests/{requestID}/response", server.respondInteraction)
	server.handle("POST /v1/organizations/{organizationID}/agent-requests/{requestID}/review", server.reviewInteraction)
	server.handle("GET /v1/organizations/{organizationID}/organization-agent", server.getOrganizationAgent)
	server.handle("PATCH /v1/organizations/{organizationID}/organization-agent", server.updateOrganizationAgent)
	server.handle("GET /v1/organizations/{organizationID}/organization-agent/context", server.getOrganizationAgentContext)
	server.handle("GET /v1/organizations/{organizationID}/organization-agent/follow-ups", server.listOrganizationAgentFollowUps)
	server.handle("GET /v1/organizations/{organizationID}/teamspaces", server.listTeamspaces)
	server.handle("POST /v1/organizations/{organizationID}/teamspaces", server.createTeamspace)
	server.handle("GET /v1/organizations/{organizationID}/teamspaces/{teamspaceID}", server.getTeamspace)
	server.handle("PATCH /v1/organizations/{organizationID}/teamspaces/{teamspaceID}", server.updateTeamspace)
	server.handle("DELETE /v1/organizations/{organizationID}/teamspaces/{teamspaceID}", server.deleteTeamspace)
	server.handle("POST /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/agents", server.addTeamspaceContribution)
	server.handle("PATCH /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/agents/{agentID}", server.updateTeamspaceContribution)
	server.handle("DELETE /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/agents/{agentID}", server.deleteTeamspaceContribution)
	server.handle("GET /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/entries", server.listTeamspaceEntries)
	server.handle("POST /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/entries", server.publishTeamspaceEntry)
	server.handle("POST /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/messages", server.postRoomMessage)
	server.handle("GET /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/messages", server.readRoom)
	server.handle("GET /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/catchup", server.catchUpOnRoom)
	server.handle("GET /v1/organizations/{organizationID}/preferences", server.getPreferences)
	server.handle("PUT /v1/organizations/{organizationID}/preferences", server.setPreferences)
	server.handle("POST /v1/organizations/{organizationID}/answer-guard/check", server.checkAnswer)
	guardPreferences := newGuardPreferenceHandlers(server.guardPreferences, server.member, logger)
	server.handle("GET /v1/organizations/{organizationID}/answer-guard/preferences", guardPreferences.get)
	server.handle("PUT /v1/organizations/{organizationID}/answer-guard/preferences", guardPreferences.put)
	return server
}

// preferencesView is what a member chose about Krude, in this organization.
type preferencesView struct {
	// AppUpdates says whether Krude tells the member what their apps report.
	AppUpdates bool `json:"app_updates"`
}

// getPreferences reads the calling member's own preferences.
func (server *Server) getPreferences(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.preferences == nil {
		writeError(writer, http.StatusServiceUnavailable, "preferences_unavailable", "Preferences are not kept on this server.")
		return
	}
	on, err := server.preferences.AppUpdates(request.Context(), member.OrganizationID, member.UserID)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, preferencesView{AppUpdates: on})
}

// setPreferences records the calling member's own preferences. An agent
// acting for them cannot: member refuses it.
func (server *Server) setPreferences(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.preferences == nil {
		writeError(writer, http.StatusServiceUnavailable, "preferences_unavailable", "Preferences are not kept on this server.")
		return
	}
	var body struct {
		AppUpdates *bool `json:"app_updates"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	if body.AppUpdates == nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", "app_updates is required.")
		return
	}
	if err := server.preferences.SetAppUpdates(request.Context(), member.OrganizationID, member.UserID, *body.AppUpdates); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, preferencesView{AppUpdates: *body.AppUpdates})
}

func scopeOf(member threads.Member) managedagents.Scope {
	return managedagents.Scope{OrganizationID: member.OrganizationID, UserID: member.UserID}
}

// interactionActorOf is the member acting in their own signed-in session,
// through their primary agent's identity. It is the only place an actor is
// marked SessionMember: member refuses agent tokens before it is reached.
func interactionActorOf(member threads.Member) primaryagents.InteractionActor {
	return primaryagents.InteractionActor{
		OrganizationID: member.OrganizationID, UserID: member.UserID,
		AgentID: member.AgentID, AgentName: member.AgentName, AgentKind: member.AgentKind,
		Session: primaryagents.SessionMember,
	}
}

func teamspaceActorOf(member threads.Member) teamspaces.Actor {
	return teamspaces.Actor{OrganizationID: member.OrganizationID, UserID: member.UserID, AccessToken: member.AccessToken}
}

func (server *Server) createInteraction(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.memberWithAgent(writer, request)
	if !ok {
		return
	}
	if server.interactions == nil {
		writeError(writer, http.StatusServiceUnavailable, "interactions_unavailable", "agent interactions are not configured")
		return
	}
	var input primaryagents.InteractionCreateInput
	if !server.decode(writer, request, &input) {
		return
	}
	created, err := server.interactions.Create(request.Context(), interactionActorOf(member), member.AccessToken, request.Header.Get("Idempotency-Key"), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, created)
}

// listAgentRequests lists the requests the member's agents sent and were
// sent, together.
func (server *Server) listAgentRequests(writer http.ResponseWriter, request *http.Request) {
	server.listInteractions(writer, request, interactions.BoxAll)
}

func (server *Server) interactionInbox(writer http.ResponseWriter, request *http.Request) {
	server.listInteractions(writer, request, interactions.BoxInbox)
}

func (server *Server) interactionSent(writer http.ResponseWriter, request *http.Request) {
	server.listInteractions(writer, request, interactions.BoxSent)
}

// listInteractions lists one side of the member's requests, or both. Each
// listing takes an optional ?teamspace_id= and ?with_user=, which together
// are how a side conversation reads what two teammates asked each other in
// one teamspace, as either of them.
func (server *Server) listInteractions(writer http.ResponseWriter, request *http.Request, box string) {
	member, ok := server.memberWithAgent(writer, request)
	if !ok {
		return
	}
	if server.interactions == nil {
		writeError(writer, http.StatusServiceUnavailable, "interactions_unavailable", "agent interactions are not configured")
		return
	}
	limit, ok := server.limit(writer, request)
	if !ok {
		return
	}
	query := request.URL.Query()
	// A filter left out lists everything, so one sent blank is a mistake to
	// answer, never a quiet way of asking for everything.
	for _, name := range []string{"teamspace_id", "with_user"} {
		if query.Has(name) && strings.TrimSpace(query.Get(name)) == "" {
			writeError(writer, http.StatusBadRequest, "invalid_request", name+" must not be blank; leave it out to list every request")
			return
		}
	}
	listed, err := server.interactions.List(request.Context(), interactionActorOf(member), interactions.Query{
		Box: box, TeamspaceID: query.Get("teamspace_id"), WithUserID: query.Get("with_user"), Limit: limit,
	})
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"requests": listed})
}

func (server *Server) getInteraction(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.memberWithAgent(writer, request)
	if !ok {
		return
	}
	if server.interactions == nil {
		writeError(writer, http.StatusServiceUnavailable, "interactions_unavailable", "agent interactions are not configured")
		return
	}
	found, err := server.interactions.Get(request.Context(), interactionActorOf(member), request.PathValue("requestID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, found)
}

// decideInteraction is the member deciding a request addressed to one of
// their agents, themselves: the one way a request held for its owner is
// decided.
func (server *Server) decideInteraction(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.memberWithAgent(writer, request)
	if !ok {
		return
	}
	if server.interactions == nil {
		writeError(writer, http.StatusServiceUnavailable, "interactions_unavailable", "agent interactions are not configured")
		return
	}
	var input primaryagents.InteractionDecisionInput
	if !server.decode(writer, request, &input) {
		return
	}
	updated, err := server.interactions.DecideAsOwner(request.Context(), interactionActorOf(member), member.AccessToken, request.PathValue("requestID"), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, updated)
}

func (server *Server) respondInteraction(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.memberWithAgent(writer, request)
	if !ok {
		return
	}
	if server.interactions == nil {
		writeError(writer, http.StatusServiceUnavailable, "interactions_unavailable", "agent interactions are not configured")
		return
	}
	var input primaryagents.InteractionResponseInput
	if !server.decode(writer, request, &input) {
		return
	}
	updated, err := server.interactions.Respond(request.Context(), interactionActorOf(member), request.PathValue("requestID"), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, updated)
}

// reviewInteraction is the member reviewing, in their own session, an
// answer the answer guard held for them: sending it, sending it masked, or
// withholding it.
func (server *Server) reviewInteraction(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.memberWithAgent(writer, request)
	if !ok {
		return
	}
	if server.interactions == nil {
		writeError(writer, http.StatusServiceUnavailable, "interactions_unavailable", "agent interactions are not configured")
		return
	}
	var input primaryagents.InteractionReviewInput
	if !server.decode(writer, request, &input) {
		return
	}
	updated, err := server.interactions.ReviewAsOwner(request.Context(), interactionActorOf(member), member.AccessToken, request.PathValue("requestID"), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, updated)
}

func (server *Server) listAgents(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.agents == nil {
		writeError(writer, http.StatusServiceUnavailable, "agents_unavailable", "managed agents are not configured")
		return
	}
	listed, err := server.agents.List(request.Context(), scopeOf(member))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"agents": listed})
}

func (server *Server) createAgent(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.agents == nil {
		writeError(writer, http.StatusServiceUnavailable, "agents_unavailable", "managed agents are not configured")
		return
	}
	var input managedagents.CreateInput
	if !server.decode(writer, request, &input) {
		return
	}
	created, err := server.agents.Create(request.Context(), scopeOf(member), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, created)
}

func (server *Server) getAgent(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.agents == nil {
		writeError(writer, http.StatusServiceUnavailable, "agents_unavailable", "managed agents are not configured")
		return
	}
	definition, err := server.agents.Get(request.Context(), scopeOf(member), request.PathValue("agentID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, definition)
}

func (server *Server) updateAgent(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.agents == nil {
		writeError(writer, http.StatusServiceUnavailable, "agents_unavailable", "managed agents are not configured")
		return
	}
	var input managedagents.UpdateInput
	if !server.decode(writer, request, &input) {
		return
	}
	updated, err := server.agents.Update(request.Context(), scopeOf(member), request.PathValue("agentID"), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, updated)
}

func (server *Server) deleteAgent(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.agents == nil {
		writeError(writer, http.StatusServiceUnavailable, "agents_unavailable", "managed agents are not configured")
		return
	}
	if err := server.agents.Delete(request.Context(), scopeOf(member), request.PathValue("agentID")); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) runAgent(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.agents == nil {
		writeError(writer, http.StatusServiceUnavailable, "agents_unavailable", "managed agents are not configured")
		return
	}
	var body struct {
		ThreadID      string                       `json:"thread_id"`
		Message       string                       `json:"message"`
		ScreenContext *primaryagents.ScreenContext `json:"screen_context"`
		Confirmations []struct {
			ID        string `json:"id"`
			Confirmed *bool  `json:"confirmed"`
		} `json:"confirmations"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	if body.ScreenContext != nil && !body.ScreenContext.Valid() {
		writeError(writer, http.StatusBadRequest, "invalid_request", "screen context is invalid")
		return
	}
	input := threads.RunInput{Message: body.Message, ScreenContext: body.ScreenContext}
	for _, answer := range body.Confirmations {
		if answer.ID == "" || answer.Confirmed == nil {
			writeError(writer, http.StatusBadRequest, "invalid_request", "every confirmation needs an id and whether it is confirmed")
			return
		}
		input.Confirmations = append(input.Confirmations, threads.Answer{ID: answer.ID, Confirmed: *answer.Confirmed})
	}
	started, err := server.agents.StartRun(request.Context(), member, request.PathValue("agentID"), body.ThreadID, input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.Header().Set("X-Oryxa-Thread", started.ThreadID)
	server.streamRun(writer, request, started.Run)
}

func (server *Server) listAgentThreads(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.agents == nil {
		writeError(writer, http.StatusServiceUnavailable, "agents_unavailable", "managed agents are not configured")
		return
	}
	listed, err := server.agents.ListThreads(request.Context(), member, request.PathValue("agentID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"threads": listed})
}

func (server *Server) getAgentThread(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.agents == nil {
		writeError(writer, http.StatusServiceUnavailable, "agents_unavailable", "managed agents are not configured")
		return
	}
	detail, err := server.agents.GetThread(request.Context(), member, request.PathValue("agentID"), request.PathValue("threadID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, detail)
}

func (server *Server) listAgentTeams(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.teams == nil {
		writeError(writer, http.StatusServiceUnavailable, "agent_teams_unavailable", "agent teams are not configured")
		return
	}
	listed, err := server.teams.List(request.Context(), scopeOf(member))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"teams": listed})
}

func (server *Server) createAgentTeam(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.teams == nil {
		writeError(writer, http.StatusServiceUnavailable, "agent_teams_unavailable", "agent teams are not configured")
		return
	}
	var input agentteams.CreateInput
	if !server.decode(writer, request, &input) {
		return
	}
	created, err := server.teams.Create(request.Context(), scopeOf(member), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, created)
}

func (server *Server) getAgentTeam(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.teams == nil {
		writeError(writer, http.StatusServiceUnavailable, "agent_teams_unavailable", "agent teams are not configured")
		return
	}
	team, err := server.teams.Get(request.Context(), scopeOf(member), request.PathValue("teamID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, team)
}

func (server *Server) updateAgentTeam(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.teams == nil {
		writeError(writer, http.StatusServiceUnavailable, "agent_teams_unavailable", "agent teams are not configured")
		return
	}
	var input agentteams.UpdateInput
	if !server.decode(writer, request, &input) {
		return
	}
	updated, err := server.teams.Update(request.Context(), scopeOf(member), request.PathValue("teamID"), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, updated)
}

func (server *Server) deleteAgentTeam(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.teams == nil {
		writeError(writer, http.StatusServiceUnavailable, "agent_teams_unavailable", "agent teams are not configured")
		return
	}
	if err := server.teams.Delete(request.Context(), scopeOf(member), request.PathValue("teamID")); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) readAgentTeamRoom(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.teamRooms == nil {
		writeError(writer, http.StatusServiceUnavailable, "agent_team_rooms_unavailable", "agent team rooms are not configured")
		return
	}
	after, err := strconv.ParseInt(request.URL.Query().Get("after_seq"), 10, 64)
	if request.URL.Query().Get("after_seq") == "" {
		after = 0
		err = nil
	}
	if err != nil || after < 0 {
		writeError(writer, http.StatusBadRequest, "invalid_request", "after_seq must be nonnegative")
		return
	}
	listed, err := server.teamRooms.Read(request.Context(), scopeOf(member), request.PathValue("teamID"), after, 200)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"messages": listed})
}

func (server *Server) agentTeamRoomThreads(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.teamRooms == nil {
		writeError(writer, http.StatusServiceUnavailable, "agent_team_rooms_unavailable", "agent team rooms are not configured")
		return
	}
	listed, err := server.teamRooms.Threads(request.Context(), scopeOf(member), request.PathValue("teamID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"threads": listed})
}

func (server *Server) postAgentTeamRoom(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.teamRooms == nil {
		writeError(writer, http.StatusServiceUnavailable, "agent_team_rooms_unavailable", "agent team rooms are not configured")
		return
	}
	var input struct {
		Body string `json:"body"`
	}
	if !server.decode(writer, request, &input) {
		return
	}
	added, err := server.teamRooms.Post(request.Context(), member, request.PathValue("teamID"), input.Body)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"messages": added})
}

func (server *Server) listTeamspaces(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.teamspaces == nil {
		writeError(writer, http.StatusServiceUnavailable, "teamspaces_unavailable", "teamspaces are not configured")
		return
	}
	spaces, err := server.teamspaces.List(request.Context(), teamspaceActorOf(member))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"teamspaces": spaces})
}

func (server *Server) getOrganizationAgent(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.teamspaces == nil {
		writeError(writer, http.StatusServiceUnavailable, "teamspaces_unavailable", "teamspaces are not configured")
		return
	}
	var agent teamspaces.OrganizationAgent
	var err error
	if request.URL.Query().Get("preview") == "1" {
		agent, err = server.teamspaces.PreviewOrganizationAgent(request.Context(), teamspaceActorOf(member))
	} else {
		agent, err = server.teamspaces.OrganizationAgent(request.Context(), teamspaceActorOf(member))
	}
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, agent)
}

func (server *Server) updateOrganizationAgent(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.teamspaces == nil {
		writeError(writer, http.StatusServiceUnavailable, "teamspaces_unavailable", "teamspaces are not configured")
		return
	}
	var input teamspaces.OrganizationAgentUpdate
	if !server.decode(writer, request, &input) {
		return
	}
	agent, err := server.teamspaces.UpdateOrganizationAgent(request.Context(), teamspaceActorOf(member), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, agent)
}

func (server *Server) getOrganizationAgentContext(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.organizationContext == nil {
		writeError(writer, http.StatusServiceUnavailable, "organization_agent_unavailable", "organization-agent synthesis is not configured")
		return
	}
	synthesis, err := server.organizationContext.Synthesis(request.Context(), member.OrganizationID)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, synthesis)
}

func (server *Server) listOrganizationAgentFollowUps(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.organizationContext == nil {
		writeError(writer, http.StatusServiceUnavailable, "organization_agent_unavailable", "organization-agent synthesis is not configured")
		return
	}
	limit, ok := server.limit(writer, request)
	if !ok {
		return
	}
	followUps, err := server.organizationContext.ListFollowUps(request.Context(), member.OrganizationID, limit)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"follow_ups": followUps})
}

func (server *Server) createTeamspace(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.teamspaces == nil {
		writeError(writer, http.StatusServiceUnavailable, "teamspaces_unavailable", "teamspaces are not configured")
		return
	}
	var input teamspaces.CreateInput
	if !server.decode(writer, request, &input) {
		return
	}
	created, err := server.teamspaces.Create(request.Context(), teamspaceActorOf(member), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, created)
}

func (server *Server) getTeamspace(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.teamspaces == nil {
		writeError(writer, http.StatusServiceUnavailable, "teamspaces_unavailable", "teamspaces are not configured")
		return
	}
	space, err := server.teamspaces.Get(request.Context(), teamspaceActorOf(member), request.PathValue("teamspaceID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, space)
}

func (server *Server) updateTeamspace(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.teamspaces == nil {
		writeError(writer, http.StatusServiceUnavailable, "teamspaces_unavailable", "teamspaces are not configured")
		return
	}
	var input teamspaces.UpdateInput
	if !server.decode(writer, request, &input) {
		return
	}
	space, err := server.teamspaces.Update(request.Context(), teamspaceActorOf(member), request.PathValue("teamspaceID"), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, space)
}

func (server *Server) deleteTeamspace(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.teamspaces == nil {
		writeError(writer, http.StatusServiceUnavailable, "teamspaces_unavailable", "teamspaces are not configured")
		return
	}
	if err := server.teamspaces.Delete(request.Context(), teamspaceActorOf(member), request.PathValue("teamspaceID")); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) addTeamspaceContribution(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.teamspaces == nil {
		writeError(writer, http.StatusServiceUnavailable, "teamspaces_unavailable", "teamspaces are not configured")
		return
	}
	var input teamspaces.ContributionInput
	if !server.decode(writer, request, &input) {
		return
	}
	space, err := server.teamspaces.AddContribution(request.Context(), teamspaceActorOf(member), request.PathValue("teamspaceID"), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, space)
}

func (server *Server) updateTeamspaceContribution(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.teamspaces == nil {
		writeError(writer, http.StatusServiceUnavailable, "teamspaces_unavailable", "teamspaces are not configured")
		return
	}
	var input teamspaces.ContributionUpdate
	if !server.decode(writer, request, &input) {
		return
	}
	space, err := server.teamspaces.UpdateContribution(request.Context(), teamspaceActorOf(member), request.PathValue("teamspaceID"), request.PathValue("agentID"), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, space)
}

func (server *Server) deleteTeamspaceContribution(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.teamspaces == nil {
		writeError(writer, http.StatusServiceUnavailable, "teamspaces_unavailable", "teamspaces are not configured")
		return
	}
	space, err := server.teamspaces.DeleteContribution(request.Context(), teamspaceActorOf(member), request.PathValue("teamspaceID"), request.PathValue("agentID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, space)
}

func (server *Server) listTeamspaceEntries(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.teamspaces == nil {
		writeError(writer, http.StatusServiceUnavailable, "teamspaces_unavailable", "teamspaces are not configured")
		return
	}
	limit, ok := server.limit(writer, request)
	if !ok {
		return
	}
	entries, err := server.teamspaces.ListEntries(request.Context(), teamspaceActorOf(member), request.PathValue("teamspaceID"), limit)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"entries": entries})
}

func (server *Server) publishTeamspaceEntry(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.teamspaces == nil {
		writeError(writer, http.StatusServiceUnavailable, "teamspaces_unavailable", "teamspaces are not configured")
		return
	}
	var input teamspaces.PublishEntryInput
	if !server.decode(writer, request, &input) {
		return
	}
	entry, err := server.teamspaces.PublishEntry(request.Context(), teamspaceActorOf(member), request.PathValue("teamspaceID"), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, entry)
}

func (server *Server) listAgentWorkflows(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.workflows == nil {
		writeError(writer, http.StatusServiceUnavailable, "agent_workflows_unavailable", "agent workflows are not configured")
		return
	}
	listed, err := server.workflows.List(request.Context(), scopeOf(member))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"workflows": listed})
}

func (server *Server) createAgentWorkflow(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.workflows == nil {
		writeError(writer, http.StatusServiceUnavailable, "agent_workflows_unavailable", "agent workflows are not configured")
		return
	}
	var input agentworkflows.CreateInput
	if !server.decode(writer, request, &input) {
		return
	}
	created, err := server.workflows.Create(request.Context(), scopeOf(member), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, created)
}

func (server *Server) getAgentWorkflow(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.workflows == nil {
		writeError(writer, http.StatusServiceUnavailable, "agent_workflows_unavailable", "agent workflows are not configured")
		return
	}
	workflow, err := server.workflows.Get(request.Context(), scopeOf(member), request.PathValue("workflowID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, workflow)
}

func (server *Server) updateAgentWorkflow(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.workflows == nil {
		writeError(writer, http.StatusServiceUnavailable, "agent_workflows_unavailable", "agent workflows are not configured")
		return
	}
	var input agentworkflows.UpdateInput
	if !server.decode(writer, request, &input) {
		return
	}
	updated, err := server.workflows.Update(request.Context(), scopeOf(member), request.PathValue("workflowID"), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, updated)
}

func (server *Server) deleteAgentWorkflow(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.workflows == nil {
		writeError(writer, http.StatusServiceUnavailable, "agent_workflows_unavailable", "agent workflows are not configured")
		return
	}
	if err := server.workflows.Delete(request.Context(), scopeOf(member), request.PathValue("workflowID")); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) startAgentWorkflow(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.workflows == nil {
		writeError(writer, http.StatusServiceUnavailable, "agent_workflows_unavailable", "agent workflows are not configured")
		return
	}
	var input struct{}
	// ContentLength is -1 for a chunked or otherwise unknown-length body, not
	// 0, so a plain != 0 check treats a bodyless request sent that way as
	// having a body and answers 400 instead of starting the workflow.
	if request.ContentLength > 0 && !server.decode(writer, request, &input) {
		return
	}
	execution, err := server.workflows.Start(request.Context(), scopeOf(member), request.PathValue("workflowID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, execution)
}

func (server *Server) listAgentWorkflowExecutions(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.workflows == nil {
		writeError(writer, http.StatusServiceUnavailable, "agent_workflows_unavailable", "agent workflows are not configured")
		return
	}
	limit, ok := server.limit(writer, request)
	if !ok {
		return
	}
	listed, err := server.workflows.ListExecutions(request.Context(), scopeOf(member), request.PathValue("workflowID"), limit)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"executions": listed})
}

func (server *Server) getAgentWorkflowExecution(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.workflows == nil {
		writeError(writer, http.StatusServiceUnavailable, "agent_workflows_unavailable", "agent workflows are not configured")
		return
	}
	execution, err := server.workflows.GetExecution(request.Context(), scopeOf(member), request.PathValue("executionID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, execution)
}

// handle registers a route and remembers its pattern, so a host serving
// several services knows which paths are this one's.
func (server *Server) handle(pattern string, handler http.HandlerFunc) {
	server.mux.Handle(pattern, cliscope.Wrap(pattern, handler))
	server.patterns = append(server.patterns, pattern)
}

// Patterns lists every route the server registered.
func (server *Server) Patterns() []string { return slices.Clone(server.patterns) }

func (server *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if strings.HasPrefix(request.URL.Path, "/v1/") {
		writer.Header().Set("Cache-Control", "no-store")
	}
	server.mux.ServeHTTP(writer, request)
}

// listThreads defaults to the member's chat threads. ?origin=side_conversation
// or ?origin=all reaches the threads the web app opened to run the member's
// own agent on an internal prompt, kept out of the default listing so they
// never show as the member's own chat history. ?origin= present but blank
// (after trimming whitespace) is the caller's mistake, answered here rather
// than sent on to the service; an unknown, non-blank value is answered by the
// service's own ErrInvalid.
func (server *Server) listThreads(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	origin := threads.OriginChat
	if values, present := request.URL.Query()["origin"]; present {
		origin = strings.TrimSpace(values[0])
		if origin == "" {
			writeError(writer, http.StatusBadRequest, "invalid_request", "origin must be chat, side_conversation or all")
			return
		}
	}
	listed, err := server.threads.List(request.Context(), member, origin)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"threads": listed})
}

func (server *Server) createThread(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	// The body is optional; when sent, it may carry the thread's origin.
	// Omitted or "" means a plain chat thread. An object with fields this
	// version does not know is refused rather than silently dropped.
	var body struct {
		Origin string `json:"origin"`
	}
	if request.ContentLength != 0 && !server.decode(writer, request, &body) {
		return
	}
	created, err := server.threads.Create(request.Context(), member, body.Origin)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, created)
}

func (server *Server) getThread(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	detail, err := server.threads.Get(request.Context(), member, request.PathValue("threadID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, detail)
}

func (server *Server) deleteThread(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if err := server.threads.Delete(request.Context(), member, request.PathValue("threadID")); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// shownInline are the kinds of file a browser may show in place. Everything
// else a run kept — HTML, scripts, anything a page could run — is only ever
// downloaded, since it is served from the app's own origin.
var shownInline = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
	"application/pdf": true, "text/plain": true,
}

// artifact serves a file a thread's runs kept.
func (server *Server) artifact(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	var version int64
	if raw := request.URL.Query().Get("version"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 1 {
			writeError(writer, http.StatusBadRequest, "invalid_request", "version must be a positive whole number")
			return
		}
		version = parsed
	}
	file, err := server.threads.Artifact(request.Context(), member, request.PathValue("threadID"), request.PathValue("name"), version)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	mediaType, _, err := mime.ParseMediaType(file.MIMEType)
	if err != nil || mediaType == "" {
		mediaType = "application/octet-stream"
	}
	header := writer.Header()
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Type", mediaType)
	if mediaType == "text/plain" {
		header.Set("Content-Type", "text/plain; charset=utf-8")
	}
	// Nothing a run kept may run as a page of this origin, even if a
	// browser is talked into rendering it.
	header.Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; sandbox")
	disposition := "attachment"
	if shownInline[mediaType] {
		disposition = "inline"
	}
	// A "user:" file is the member's own; the prefix is how ADK scopes it,
	// not part of its name, and no file system would take the colon.
	header.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{
		"filename": path.Base(strings.TrimPrefix(file.Name, "user:")),
	}))
	header.Set("Content-Length", strconv.Itoa(len(file.Data)))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(file.Data)
}

// run starts a run on a thread and streams it. Everything that can refuse the
// run happens before the stream opens, so a refusal is an ordinary JSON
// error; once the stream is open, the run's own end is its last event.
func (server *Server) run(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	// teamspace_id is optional and pins this run to one teamspace (see
	// threads.RunInput.TeamspaceID). It is a pointer so that one sent blank is
	// told apart from one left out: a pin the caller meant, and did not get,
	// would leave the run free to work in any teamspace the model names.
	var body struct {
		Message       string                       `json:"message"`
		ScreenContext *primaryagents.ScreenContext `json:"screen_context"`
		TeamspaceID   *string                      `json:"teamspace_id"`
		Confirmations []struct {
			ID        string `json:"id"`
			Confirmed *bool  `json:"confirmed"`
		} `json:"confirmations"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	if body.ScreenContext != nil && !body.ScreenContext.Valid() {
		writeError(writer, http.StatusBadRequest, "invalid_request", "screen context is invalid")
		return
	}
	input := threads.RunInput{Message: body.Message, ScreenContext: body.ScreenContext}
	for _, answer := range body.Confirmations {
		if answer.ID == "" || answer.Confirmed == nil {
			writeError(writer, http.StatusBadRequest, "invalid_request", "every confirmation needs an id and whether it is confirmed")
			return
		}
		input.Confirmations = append(input.Confirmations, threads.Answer{ID: answer.ID, Confirmed: *answer.Confirmed})
	}
	if body.TeamspaceID != nil {
		pinned, err := primaryagents.CleanTeamspacePin(*body.TeamspaceID)
		if err != nil {
			writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		input.TeamspaceID = pinned
	}

	started, err := server.threads.StartRun(request.Context(), member, request.PathValue("threadID"), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}

	server.streamRun(writer, request, started)
}

func (server *Server) streamRun(writer http.ResponseWriter, request *http.Request, started Run) {
	controller := http.NewResponseController(writer)
	// The host sets no write deadline for this service; a stream sets its
	// own, just past the longest a run may take.
	if err := controller.SetWriteDeadline(time.Now().Add(server.runTimeout + streamGrace)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		started.Discard(request.Context(), "the stream could not be opened")
		writeError(writer, http.StatusInternalServerError, "internal", "internal server error")
		return
	}
	header := writer.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("X-Accel-Buffering", "no")
	header.Set("X-Oryxa-Run", started.ID())
	writer.WriteHeader(http.StatusOK)
	if err := controller.Flush(); err != nil {
		started.Discard(request.Context(), "the stream could not be opened")
		return
	}

	stream := newEventStream(writer, controller)
	stopKeepAlive := stream.keepAlive(keepAliveInterval)
	defer stopKeepAlive()
	started.Execute(request.Context(), stream.item)
}

// member decides who the caller is in the path's organization. An agent
// acting for a member is refused: a thread is the member's own conversation,
// and an agent reaches Krude from inside a run, never through this API.
//
// It also attaches the caller's persistent agent identity when one resolves,
// but only as a best effort: most routes use it merely to stamp state (for
// example a new thread's initial state), so a lookup problem here leaves it
// empty rather than failing a request that never needed it. memberWithAgent
// is for the routes that do need it.
func (server *Server) member(writer http.ResponseWriter, request *http.Request) (threads.Member, bool) {
	if server.unavailable != "" {
		writeError(writer, http.StatusServiceUnavailable, "agents_unavailable", server.unavailable)
		return threads.Member{}, false
	}
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return threads.Member{}, false
	}
	organizationID := request.PathValue("organizationID")
	membership, err := server.directory.Membership(request.Context(), organizationID, credentials)
	if err != nil {
		server.handleError(writer, request, err)
		return threads.Member{}, false
	}
	if membership.Agent {
		writeError(writer, http.StatusForbidden, "forbidden", "an agent cannot hold a member's conversation")
		return threads.Member{}, false
	}
	member := threads.Member{
		UserID:         membership.UserID,
		OrganizationID: organizationID,
		AccessToken:    credentials.AccessToken(),
	}
	if server.agents != nil {
		if primary, err := server.agents.Primary(request.Context(), scopeOf(member)); err != nil {
			server.logger.WarnContext(request.Context(), "resolve primary agent identity", "error", err)
		} else {
			member.AgentID = primary.ID
			member.AgentName = primary.Name
			member.AgentKind = string(primary.Kind)
		}
	}
	return member, true
}

// memberWithAgent is member, but for routes that act as the caller's
// persistent agent rather than just the member themselves: an interaction is
// between two agents, not two members, so proceeding with an empty identity
// would misattribute or wrongly deny it, not merely leave state unstamped.
// Where member leaves a failed lookup as "no identity", this fails the
// request instead.
func (server *Server) memberWithAgent(writer http.ResponseWriter, request *http.Request) (threads.Member, bool) {
	member, ok := server.member(writer, request)
	if !ok {
		return threads.Member{}, false
	}
	if server.agents != nil && member.AgentID == "" {
		writeError(writer, http.StatusInternalServerError, "internal", "could not resolve the calling agent's identity")
		return threads.Member{}, false
	}
	return member, true
}

// credentials reads how the caller signs in. A cookie alone cannot change
// anything unless the request comes from an allowed origin, so another site
// cannot start a run in a member's name.
func (server *Server) credentials(writer http.ResponseWriter, request *http.Request) (authclient.Credentials, bool) {
	credentials := authclient.Credentials{Authorization: request.Header.Get("Authorization")}
	if credentials.Authorization == "" {
		if cookie, err := request.Cookie(authclient.AccessCookieName); err == nil {
			credentials.AccessCookie = cookie.Value
		}
	}
	if !credentials.Present() {
		writeError(writer, http.StatusUnauthorized, "unauthorized", "access token is required")
		return credentials, false
	}
	if credentials.Authorization == "" && request.Method != http.MethodGet && request.Method != http.MethodHead {
		if _, ok := server.allowedOrigins[request.Header.Get("Origin")]; !ok {
			writeError(writer, http.StatusForbidden, "forbidden", "origin is required for cookie authentication")
			return credentials, false
		}
	}
	return credentials, true
}

// limit reads how many results a listing was asked for. No limit is the
// caller leaving it out; anything else has to be a positive number, on every
// route that takes one, rather than a typo quietly meaning "everything".
// roomActorOf is who is acting in a room: the member themself, carrying
// their own token, because what happens in the room on their behalf is bound
// by what they may reach.
func roomActorOf(member threads.Member) rooms.Actor {
	return rooms.Actor{OrganizationID: member.OrganizationID, UserID: member.UserID, AccessToken: member.AccessToken}
}

// postRoomMessage says something in a room and answers with everything the
// room added: the member's own message, then whatever agents answered it.
func (server *Server) postRoomMessage(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.rooms == nil {
		writeError(writer, http.StatusServiceUnavailable, "rooms_unavailable", "teamspace conversations are not configured")
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	said, err := server.rooms.Post(request.Context(), roomActorOf(member), request.PathValue("teamspaceID"), body.Body)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"messages": said})
}

// readRoom returns what was said after ?after, oldest first, and records that
// the member is reading it — which is how the room knows they are here, and
// therefore that their agent must not answer over them.
func (server *Server) readRoom(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.rooms == nil {
		writeError(writer, http.StatusServiceUnavailable, "rooms_unavailable", "teamspace conversations are not configured")
		return
	}
	after, ok := server.sequence(writer, request, "after")
	if !ok {
		return
	}
	limit, ok := server.limit(writer, request)
	if !ok {
		return
	}
	said, seats, err := server.rooms.Read(request.Context(), roomActorOf(member), request.PathValue("teamspaceID"), after, limit)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"messages": said, "seats": seats})
}

// catchUpOnRoom is for somebody who has been away: what the room amounts to,
// and what they have not seen.
func (server *Server) catchUpOnRoom(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if server.rooms == nil {
		writeError(writer, http.StatusServiceUnavailable, "rooms_unavailable", "teamspace conversations are not configured")
		return
	}
	after, ok := server.sequence(writer, request, "after")
	if !ok {
		return
	}
	brief, said, err := server.rooms.Catchup(request.Context(), roomActorOf(member), request.PathValue("teamspaceID"), after)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"brief": brief, "messages": said})
}

// sequence reads a message sequence from the query, which is how a reader
// says where they got to. Absent means from the beginning; anything that is
// not a whole number at or above zero is the caller's mistake rather than
// silently the beginning again.
func (server *Server) sequence(writer http.ResponseWriter, request *http.Request, name string) (int64, bool) {
	value := request.URL.Query().Get(name)
	if value == "" {
		return 0, true
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		writeError(writer, http.StatusBadRequest, "invalid_request", name+" must be a message sequence")
		return 0, false
	}
	return parsed, true
}

// limit reads how many results a listing was asked for.
func (server *Server) limit(writer http.ResponseWriter, request *http.Request) (int, bool) {
	value := request.URL.Query().Get("limit")
	if value == "" {
		return 0, true
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		writeError(writer, http.StatusBadRequest, "invalid_request", "limit must be a positive integer")
		return 0, false
	}
	return parsed, true
}

// decode reads a JSON body with no unknown fields, answering 400 itself when
// it cannot.
func (server *Server) decode(writer http.ResponseWriter, request *http.Request, target any) bool {
	return server.decodeUpTo(writer, request, target, maxJSONBytes)
}

// decodeUpTo is decode for a route whose body may be larger than the 256 KiB
// the rest read. A body past limit is cut off there, and answered 400 as the
// malformed JSON it has become.
func (server *Server) decodeUpTo(writer http.ResponseWriter, request *http.Request, target any, limit int64) bool {
	decoder := json.NewDecoder(io.LimitReader(request.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.More() {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request body must be one JSON object with known fields")
		return false
	}
	if err := cleantext.Check(target); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return false
	}
	return true
}

// ErrorCode names a failure the way the API does.
func ErrorCode(err error) (int, string) {
	switch {
	case errors.Is(err, teamspaces.ErrInvalid):
		return http.StatusBadRequest, "invalid_request"
	case errors.Is(err, teamspaces.ErrForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, teamspaces.ErrDisabled):
		return http.StatusConflict, "teamspace_disabled"
	case errors.Is(err, teamspaces.ErrConflict):
		return http.StatusConflict, "teamspace_conflict"
	case errors.Is(err, teamspaces.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, rooms.ErrInvalid):
		return http.StatusBadRequest, "invalid_request"
	case errors.Is(err, agentteamrooms.ErrInvalid):
		return http.StatusBadRequest, "invalid_request"
	case errors.Is(err, rooms.ErrForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, rooms.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, agentworkflows.ErrInvalid):
		return http.StatusBadRequest, "invalid_request"
	case errors.Is(err, agentworkflows.ErrDisabled):
		return http.StatusConflict, "workflow_disabled"
	case errors.Is(err, agentworkflows.ErrConflict):
		return http.StatusConflict, "name_conflict"
	case errors.Is(err, agentworkflows.ErrInProgress):
		return http.StatusConflict, "workflow_in_progress"
	case errors.Is(err, agentworkflows.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, agentworkflows.ErrUnavailable):
		return http.StatusServiceUnavailable, "agent_workflows_unavailable"
	case errors.Is(err, agentteams.ErrInvalid):
		return http.StatusBadRequest, "invalid_request"
	case errors.Is(err, agentteams.ErrForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, agentteams.ErrDisabled):
		return http.StatusConflict, "team_disabled"
	case errors.Is(err, agentteams.ErrInUse):
		return http.StatusConflict, "team_in_use"
	case errors.Is(err, agentteams.ErrConflict):
		return http.StatusConflict, "name_conflict"
	case errors.Is(err, agentteams.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, interactions.ErrInvalid):
		return http.StatusBadRequest, "invalid_request"
	case errors.Is(err, interactions.ErrForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, interactions.ErrExpired):
		return http.StatusConflict, "request_expired"
	case errors.Is(err, interactions.ErrIdempotencyConflict):
		return http.StatusConflict, "idempotency_key_reused"
	case errors.Is(err, interactions.ErrClosed):
		return http.StatusConflict, "request_closed"
	case errors.Is(err, interactions.ErrConflict):
		return http.StatusConflict, "invalid_state"
	case errors.Is(err, interactions.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, managedagents.ErrInvalid):
		return http.StatusBadRequest, "invalid_request"
	case errors.Is(err, managedagents.ErrConflict):
		return http.StatusConflict, "name_conflict"
	case errors.Is(err, managedagents.ErrInUse):
		return http.StatusConflict, "agent_in_use"
	case errors.Is(err, managedagents.ErrDisabled):
		return http.StatusConflict, "agent_disabled"
	case errors.Is(err, threads.ErrInvalid):
		return http.StatusBadRequest, "invalid_request"
	case errors.Is(err, threads.ErrConflict):
		return http.StatusConflict, "artifact_conflict"
	case errors.Is(err, authclient.ErrUnauthorized):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, authclient.ErrForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, threads.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, managedagents.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, threads.ErrRunInProgress):
		return http.StatusConflict, "run_in_progress"
	case errors.Is(err, threads.ErrConfirmationPending):
		return http.StatusConflict, "confirmation_pending"
	case errors.Is(err, authclient.ErrRateLimited):
		return http.StatusTooManyRequests, "rate_limited"
	case errors.Is(err, authclient.ErrUnavailable):
		return http.StatusServiceUnavailable, "unavailable"
	default:
		return http.StatusInternalServerError, "internal"
	}
}

// message is what a caller is told about a failure: the service's own words
// for what they can act on, and nothing for the rest.
func message(err error) string {
	status, code := ErrorCode(err)
	switch {
	case status == http.StatusInternalServerError:
		return "internal server error"
	case code == "invalid_request":
		// The wording after the sentinel says what to fix.
		text := strings.TrimPrefix(err.Error(), threads.ErrInvalid.Error()+": ")
		text = strings.TrimPrefix(text, managedagents.ErrInvalid.Error()+": ")
		text = strings.TrimPrefix(text, interactions.ErrInvalid.Error()+": ")
		text = strings.TrimPrefix(text, agentworkflows.ErrInvalid.Error()+": ")
		text = strings.TrimPrefix(text, teamspaces.ErrInvalid.Error()+": ")
		text = strings.TrimPrefix(text, rooms.ErrInvalid.Error()+": ")
		text = strings.TrimPrefix(text, agentteamrooms.ErrInvalid.Error()+": ")
		return strings.TrimSpace(text)
	default:
		return err.Error()
	}
}

// handleError answers a failed call with its error code. Errors never carry
// tokens or conversation content, so they can be logged.
func (server *Server) handleError(writer http.ResponseWriter, request *http.Request, err error) {
	status, code := ErrorCode(err)
	switch {
	case status >= 500:
		server.logger.ErrorContext(request.Context(), "agents request failed",
			"method", request.Method, "path", request.URL.Path, "error", err)
	case code == "rate_limited":
		server.logger.WarnContext(request.Context(), "agents request rate limited", "path", request.URL.Path)
	}
	writeError(writer, status, code, message(err))
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]map[string]string{"error": {"code": code, "message": message}})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
