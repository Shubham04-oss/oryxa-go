package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// toolCacheFor is how long a tool's description is trusted. A tool's
// read-only mark decides whether it may run unconfirmed, so it is not kept
// for long.
const toolCacheFor = 10 * time.Minute

type cachedTool struct {
	tool    composio.Tool
	expires time.Time
}

// ToolView is a tool as a caller chooses between them.
type ToolView struct {
	Tool        string          `json:"tool"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Toolkit     string          `json:"toolkit"`
	ReadOnly    bool            `json:"read_only"`
	Destructive bool            `json:"destructive"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

func toolView(tool composio.Tool) ToolView {
	return ToolView{
		Tool: tool.Slug, Name: tool.Name, Description: tool.Description, Toolkit: tool.Toolkit,
		ReadOnly: tool.ReadOnly(), Destructive: tool.Destructive(), InputSchema: tool.InputParameters,
	}
}

// maxToolSearch bounds one search.
const maxToolSearch = 50

// SearchTools finds a toolkit's tools for a member: its featured tools
// without a query, the tools matching one with it.
//
// The member's own connection decides where the answer comes from. An Oryxa
// connection is answered from the registry, in the same ToolView JSON, so
// nothing downstream can tell the two brokers apart; a Composio one is
// answered by Composio as it always was.
func (s *Service) SearchTools(ctx context.Context, credentials authclient.Credentials, organizationID, toolkit, query string, limit int) ([]ToolView, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return nil, err
	}
	toolkit, err = domain.CheckToolkit(toolkit)
	if err != nil {
		return nil, err
	}
	if err := s.allowToolkit(ctx, organizationID, toolkit); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > maxToolSearch {
		limit = maxToolSearch
	}
	if views, ok, err := s.pipedreamToolViews(ctx, organizationID, caller.UserID, toolkit, query, limit); ok || err != nil {
		return views, err
	}
	specs, own, err := s.ownSpecs(ctx, organizationID, caller.UserID, toolkit)
	if err != nil {
		return nil, err
	}
	if own {
		return ownToolViews(toolkit, specs, query, limit), nil
	}
	search := composio.ToolQuery{Toolkit: toolkit, Search: strings.TrimSpace(query), Limit: limit}
	if search.Search == "" {
		search.Important = true
	}
	page, err := s.composio.Tools(ctx, search)
	if err != nil {
		return nil, composioError("search the app's tools", err)
	}
	if len(page.Items) == 0 && search.Important {
		// Not every toolkit features any; list what it has instead.
		search.Important = false
		if page, err = s.composio.Tools(ctx, search); err != nil {
			return nil, composioError("list the app's tools", err)
		}
	}
	views := make([]ToolView, 0, len(page.Items))
	now := s.now()
	s.toolsMu.Lock()
	for _, tool := range page.Items {
		if tool.Deprecated {
			continue
		}
		s.tools[tool.Slug] = cachedTool{tool: tool, expires: now.Add(toolCacheFor)}
		views = append(views, toolView(tool))
	}
	s.toolsMu.Unlock()
	return views, nil
}

// RunToolInput is one tool run a caller asks for.
type RunToolInput struct {
	Tool string
	// ConnectionID picks the member's connection; without it, the member's
	// one active connection to the tool's app is used.
	ConnectionID string
	Arguments    json.RawMessage
	// IdempotencyKey is required for a tool that is not read-only: asked
	// again, it answers what happened the first time.
	IdempotencyKey string
	// OnlyReads refuses any tool Composio does not mark read-only, for a
	// caller that runs tools without asking the member first.
	OnlyReads bool
}

// ToolRun is what running a tool did.
type ToolRun struct {
	Action domain.Action
	// Data is what the app answered.
	Data json.RawMessage
	// Replayed says this answered an earlier run under the same key.
	Replayed bool
}

// RunTool runs a tool in the caller's app, as the caller. A tool that runs
// and fails at the app is a ToolRun whose action failed, not an error: the
// caller, often a model, needs the app's reason to try again.
//
// The registry is consulted first, so a tool an integration declares is run
// by it; the member's connection for that toolkit then decides whether it is:
// an Oryxa connection runs the integration's own function, and a Composio one
// runs Composio's, which is never asked to act through a connection it holds
// no credential for.
func (s *Service) RunTool(ctx context.Context, credentials authclient.Credentials, organizationID string, input RunToolInput) (ToolRun, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return ToolRun{}, err
	}
	slug, err := domain.CheckTool(input.Tool)
	if err != nil {
		return ToolRun{}, err
	}
	arguments := input.Arguments
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(arguments, &object) != nil {
		return ToolRun{}, fmt.Errorf("a tool's arguments are a JSON object: %w", domain.ErrInvalid)
	}
	route, err := s.routeTool(ctx, organizationID, caller.UserID, slug)
	if err != nil {
		return ToolRun{}, err
	}
	if input.OnlyReads && !route.readOnly {
		return ToolRun{}, fmt.Errorf("%s changes things, so it runs only once the member confirms: %w", route.slug, domain.ErrNotReadOnly)
	}
	key := ""
	if !route.readOnly {
		if key, err = domain.CheckIdempotencyKey(input.IdempotencyKey); err != nil {
			return ToolRun{}, err
		}
	}
	connection, err := s.connectionFor(ctx, organizationID, caller.UserID, route.toolkit, input.ConnectionID)
	if err != nil {
		return ToolRun{}, err
	}
	if route.pipedream != nil {
		return s.runPipedreamTool(ctx, caller, connection, *route.pipedream, key, object)
	}
	if connection.Oryxa() {
		if !route.hasOwn {
			return ToolRun{}, notOwnTool(route.slug)
		}
		return s.runOwnTool(ctx, caller, connection, route.own, key, arguments, input.OnlyReads)
	}
	if !connection.Composio() {
		return ToolRun{}, errNotComposio(connection)
	}
	return s.runComposioTool(ctx, caller, organizationID, connection, route.slug, key, input.IdempotencyKey, input.OnlyReads, arguments)
}

// runComposioTool runs a tool the way it ran before Oryxa's own integrations
// carried it, and is reached only over a connection Composio holds the
// credential for. Its own name, version and account answer for the call,
// whatever the registry says the tool is.
func (s *Service) runComposioTool(
	ctx context.Context,
	caller domain.OrgMembership,
	organizationID string,
	connection domain.Connection,
	slug, key, idempotencyKey string,
	onlyReads bool,
	arguments json.RawMessage,
) (ToolRun, error) {
	tool, err := s.composioTool(ctx, slug)
	if err != nil {
		return ToolRun{}, err
	}
	// The call is Composio's, so Composio's own mark says whether it only
	// reads, whatever the registry says of a tool of the same name: a read in
	// the registry that Composio runs as a write still waits for the member,
	// and still needs its idempotency key.
	if onlyReads && !tool.ReadOnly() {
		return ToolRun{}, fmt.Errorf("%s changes things, so it runs only once the member confirms: %w", tool.Slug, domain.ErrNotReadOnly)
	}
	if !tool.ReadOnly() && key == "" {
		if key, err = domain.CheckIdempotencyKey(idempotencyKey); err != nil {
			return ToolRun{}, err
		}
	}
	id, err := newID("apa")
	if err != nil {
		return ToolRun{}, err
	}
	started, created, err := s.store.StartAction(ctx, domain.Action{
		ID: id, OrganizationID: organizationID, MemberID: caller.UserID, ConnectionID: connection.ID,
		Toolkit: tool.Toolkit, Tool: tool.Slug, ReadOnly: tool.ReadOnly(), Agent: caller.Agent, AgentID: caller.AgentID, AgentRole: caller.AgentRole,
		IdempotencyKey: key, Status: domain.ActionRunning, CreatedAt: s.now().UTC(),
	})
	if err != nil {
		return ToolRun{}, err
	}
	if !created {
		return replay(started, tool.Slug)
	}

	result, runErr := s.composio.ExecuteTool(ctx, tool.Slug, composio.ExecuteRequest{
		ConnectedAccountID: connection.ComposioAccountID, UserID: connection.ComposioUserID,
		Arguments: arguments, Version: tool.Version,
	})
	finished := started
	now := s.now().UTC()
	finished.FinishedAt = &now
	switch {
	case runErr != nil:
		finished.Status = domain.ActionFailed
		runErr = composioError("run "+tool.Slug, runErr)
		finished.Error = actionReason(runErr)
	case !result.Successful:
		finished.Status = domain.ActionFailed
		finished.Error = truncate(result.Error, maxComposioMessage)
		if finished.Error == "" {
			finished.Error = "the app refused the tool"
		}
	default:
		finished.Status = domain.ActionSucceeded
	}
	finished.LogID = result.LogID
	if !finished.ReadOnly && len(result.Data) <= domain.MaxResultBytes {
		// A write keeps what the app answered, so asking again under the same
		// key answers it; a read is simply run again.
		finished.Result = result.Data
	}

	event, err := s.actionEvent(finished)
	if err != nil {
		return ToolRun{}, err
	}
	// Finishing uses a context of its own: the run already happened at the
	// app, and a caller that went away must not leave it recorded as running.
	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.store.FinishAction(record, finished, event); err != nil {
		return ToolRun{}, err
	}
	if runErr != nil {
		return ToolRun{Action: finished}, runErr
	}
	return ToolRun{Action: finished, Data: result.Data}, nil
}

// replay answers an earlier run under the same key, or says it cannot yet.
func replay(action domain.Action, slug string) (ToolRun, error) {
	if action.Tool != slug {
		return ToolRun{}, fmt.Errorf("the idempotency key was used for %s: %w", action.Tool, domain.ErrConflict)
	}
	if action.Status == domain.ActionRunning {
		// Whether it happened at the app is not known, so it is not run again.
		return ToolRun{}, fmt.Errorf("a run under this key is still in flight or its outcome is unknown: %w", domain.ErrConflict)
	}
	return ToolRun{Action: action, Data: action.Result, Replayed: true}, nil
}

// composioTool describes a tool, from a short-lived cache or from Composio.
func (s *Service) composioTool(ctx context.Context, slug string) (composio.Tool, error) {
	now := s.now()
	s.toolsMu.Lock()
	cached, ok := s.tools[slug]
	s.toolsMu.Unlock()
	if ok && now.Before(cached.expires) {
		return cached.tool, nil
	}
	tool, err := s.composio.Tool(ctx, slug)
	if err != nil {
		if errors.Is(err, composio.ErrNotFound) {
			return composio.Tool{}, fmt.Errorf("no tool is called %s: %w", slug, domain.ErrNotFound)
		}
		return composio.Tool{}, composioError("read the tool", err)
	}
	if tool.Deprecated {
		return composio.Tool{}, fmt.Errorf("%s is deprecated: %w", slug, domain.ErrNotFound)
	}
	s.toolsMu.Lock()
	s.tools[slug] = cachedTool{tool: tool, expires: now.Add(toolCacheFor)}
	s.toolsMu.Unlock()
	return tool, nil
}

// connectionFor is the member's own connection a tool runs with: the one
// named, or their only active connection to the app. Which broker holds it
// is not decided here — the route is, and the run goes to that broker's own
// path, so an Oryxa connection is answered by the run rather than refused
// here.
func (s *Service) connectionFor(ctx context.Context, organizationID, memberID, toolkit, connectionID string) (domain.Connection, error) {
	if err := s.allowToolkit(ctx, organizationID, toolkit); err != nil {
		return domain.Connection{}, err
	}
	if connectionID != "" {
		connection, err := s.store.Connection(ctx, organizationID, connectionID)
		if err != nil {
			return domain.Connection{}, err
		}
		// A member acts only through their own connections; an org admin
		// can see others', never act as them.
		if connection.MemberID != memberID {
			return domain.Connection{}, domain.ErrNotFound
		}
		if connection.Toolkit != toolkit {
			return domain.Connection{}, fmt.Errorf("the connection is to %s, not %s: %w", connection.Toolkit, toolkit, domain.ErrInvalid)
		}
		switch connection.Status {
		case domain.ConnectionActive:
			return connection, nil
		case domain.ConnectionExpired:
			return domain.Connection{}, fmt.Errorf("the %s connection expired: %w", toolkit, domain.ErrNeedsReconnect)
		default:
			return domain.Connection{}, fmt.Errorf("the %s connection is %s: %w", toolkit, connection.Status, domain.ErrNotConnected)
		}
	}
	connections, err := s.store.Connections(ctx, organizationID, domain.ConnectionQuery{
		MemberID: memberID, Toolkit: toolkit, Statuses: []domain.ConnectionStatus{domain.ConnectionActive, domain.ConnectionExpired},
	})
	if err != nil {
		return domain.Connection{}, err
	}
	var active []domain.Connection
	expired := false
	for _, connection := range connections {
		switch connection.Status {
		case domain.ConnectionActive:
			active = append(active, connection)
		case domain.ConnectionExpired:
			expired = true
		}
	}
	switch {
	case len(active) == 1:
		return active[0], nil
	case len(active) > 1:
		// connection_id used to disambiguate this; it is gone from the tool
		// schema and the toolbox instructions, and it is not coming back, so
		// the only way out is telling the member what to do about it.
		return domain.Connection{}, fmt.Errorf("%d %s connections are active; disconnect the extra one in Apps & tools so only one remains: %w", len(active), toolkit, domain.ErrChooseConnection)
	case expired:
		return domain.Connection{}, fmt.Errorf("the %s connection expired: %w", toolkit, domain.ErrNeedsReconnect)
	default:
		return domain.Connection{}, fmt.Errorf("%s is not connected: %w", toolkit, domain.ErrNotConnected)
	}
}

// actionEvent is what a finished run announces. A read announces nothing: an
// agent looking around is not news, and a write is.
func (s *Service) actionEvent(action domain.Action) (*domain.Event, error) {
	if action.ReadOnly {
		return nil, nil
	}
	finishedAt := s.now().UTC()
	if action.FinishedAt != nil {
		finishedAt = *action.FinishedAt
	}
	var event domain.Event
	var err error
	if action.Status == domain.ActionSucceeded {
		event, err = s.event(appsv1.ActionCompletedDetailType, appsv1.ActionCompleted{
			ActionID: action.ID, ConnectionID: action.ConnectionID, OrganizationID: action.OrganizationID,
			MemberID: action.MemberID, Toolkit: action.Toolkit, Tool: action.Tool, ReadOnly: action.ReadOnly,
			Agent: action.Agent, AgentID: action.AgentID, AgentRole: action.AgentRole, FinishedAt: finishedAt,
		})
	} else {
		event, err = s.event(appsv1.ActionFailedDetailType, appsv1.ActionFailed{
			ActionID: action.ID, ConnectionID: action.ConnectionID, OrganizationID: action.OrganizationID,
			MemberID: action.MemberID, Toolkit: action.Toolkit, Tool: action.Tool, ReadOnly: action.ReadOnly,
			Agent: action.Agent, AgentID: action.AgentID, AgentRole: action.AgentRole, Error: eventReason(action), FinishedAt: finishedAt,
		})
	}
	if err != nil {
		return nil, err
	}
	return &event, nil
}

// actionReason is the short reason a failed run is recorded with.
func actionReason(err error) string {
	switch {
	case errors.Is(err, domain.ErrRateLimited):
		return "rate_limited"
	case errors.Is(err, domain.ErrUnavailable):
		return "unavailable"
	case errors.Is(err, domain.ErrForbidden):
		return "forbidden"
	case errors.Is(err, domain.ErrNotFound):
		return "not_found"
	default:
		return truncate(err.Error(), maxComposioMessage)
	}
}

// eventReason keeps an app's own words out of an event, which travels
// further than the member who ran the tool.
func eventReason(action domain.Action) string {
	switch action.Error {
	case "rate_limited", "unavailable", "forbidden", "not_found":
		return action.Error
	default:
		return "app_error"
	}
}
