package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/signin"
)

// A member who connected an app through Oryxa's own sign-in runs that app's
// tools here, on the app's own SDK, and Composio is not involved at any point.
//
// Which route a run takes is decided once, from two facts: whether the
// registry has the slug, and whether the member's connection for that app is
// one of Oryxa's own. A member keeps Composio until they reconnect, so a
// toolkit the registry ships tools for is still Composio's for every member
// who has not moved, and the two routes never overlap.

// ownTool is one tool as the registry declares it, with the function that
// runs it.
type ownTool struct {
	Toolkit string
	Spec    integration.ToolSpec
	Run     integration.ToolFunc
}

// indexTools is every tool the switched-on integrations declare, by slug.
//
// The registry is what a run consults first, so it has to answer by slug
// alone: the toolkit prefix in a tool's name is Composio's spelling, and a
// toolkit whose slug is not simply the name's first word — googledrive,
// googlecalendar — would not be found by reading it.
func indexTools(registry Integrations, enabled map[string]bool) map[string]ownTool {
	index := map[string]ownTool{}
	if registry == nil {
		return index
	}
	for _, toolkit := range registry.Toolkits() {
		registered, ok := registry.Lookup(toolkit)
		if !ok || !enabled[toolkit] {
			continue
		}
		functions := registered.Tools()
		for _, spec := range registered.Manifest().Tools {
			run, ok := functions[spec.Name]
			if !ok || run == nil {
				// Register refuses that pair; a registry that shrank must
				// not leave a tool behind that cannot be run.
				continue
			}
			index[spec.Name] = ownTool{Toolkit: toolkit, Spec: spec, Run: run}
		}
	}
	return index
}

// oryxaTool is the registry's own answer for a slug, and whether it has one.
func (s *Service) oryxaTool(slug string) (ownTool, bool) {
	if s.oryxa == nil {
		return ownTool{}, false
	}
	tool, ok := s.oryxa.tools[slug]
	return tool, ok
}

// ownSpecs answers a toolkit's own tools when the member's connection for
// that toolkit is one of Oryxa's own, and ok false when it is Composio's.
//
// The member's connection decides rather than the toolkit's place in a list:
// a toolkit may be switched on for Oryxa while this member is still on
// Composio. A toolkit whose integration this build ships declares no tools
// for answers an empty list rather than Composio's, because a member on the
// Oryxa route must never be offered a tool that cannot be run for them.
func (s *Service) ownSpecs(ctx context.Context, organizationID, memberID, toolkit string) ([]integration.ToolSpec, bool, error) {
	if s.oryxa == nil {
		return nil, false, nil
	}
	connected, err := s.store.Connections(ctx, organizationID, domain.ConnectionQuery{
		MemberID: memberID, Toolkit: toolkit, Statuses: []domain.ConnectionStatus{domain.ConnectionActive},
	})
	if err != nil {
		return nil, false, err
	}
	if !slices.ContainsFunc(connected, func(c domain.Connection) bool { return c.Oryxa() }) {
		return nil, false, nil
	}
	registered, ok := s.oryxa.Integrations.Lookup(toolkit)
	if !ok {
		return nil, true, nil
	}
	return registered.Manifest().Tools, true, nil
}

// ownToolViews answers a toolkit's own tools as a member chooses between
// them: a case-insensitive match on a tool's name and description, and every
// tool when there is no query, in the registry's own order and in the same
// ToolView JSON Composio's search answers, so nothing downstream — the meta
// tools, agents' skill files, web-next's Tools screen — can tell which broker
// answered.
func ownToolViews(toolkit string, specs []integration.ToolSpec, query string, limit int) []ToolView {
	search := strings.ToLower(strings.TrimSpace(query))
	views := make([]ToolView, 0, len(specs))
	for _, spec := range specs {
		if limit > 0 && len(views) == limit {
			break
		}
		if search != "" && !strings.Contains(strings.ToLower(spec.Name), search) &&
			!strings.Contains(strings.ToLower(spec.Description), search) {
			continue
		}
		views = append(views, ToolView{
			Tool: spec.Name, Name: spec.Name, Description: spec.Description, Toolkit: toolkit,
			ReadOnly: spec.ReadOnly, Destructive: spec.Destructive, InputSchema: spec.InputSchema,
		})
	}
	return views
}

// route is where one tool will run: the registry's own function, or
// Composio, and the two facts every route needs — the toolkit, and whether
// the tool changes anything.
//
// The registry is asked first, so a tool an integration declares is run by
// it; a tool it does not have is Composio's whatever the slug says. Which of
// the two actually runs is settled by the member's connection, in runOwnTool
// and in RunTool.
type toolRoute struct {
	slug     string
	toolkit  string
	readOnly bool
	// own and hasOwn carry the registry's answer, which runOwnTool needs and
	// which names the toolkit a member's own connection may be checked for.
	own    ownTool
	hasOwn bool
	// pipedream is the Pipedream action a PD_ slug names, or nil.
	pipedream *pipedreamAction
}

// routeTool answers the route for a slug, for one member. A PD_ slug is
// resolved against the member's own Pipedream connections, since only they
// say which apps' actions the member can have. A slug the registry does not
// have is otherwise read from Composio, which is the same read a run without
// a route makes.
func (s *Service) routeTool(ctx context.Context, organizationID, memberID, slug string) (toolRoute, error) {
	if own, ok := s.oryxaTool(slug); ok {
		return toolRoute{
			slug: slug, toolkit: own.Toolkit, readOnly: own.Spec.ReadOnly, own: own, hasOwn: true,
		}, nil
	}
	if s.pipedream != nil && strings.HasPrefix(slug, pipedreamPrefix) {
		return s.pipedreamRoute(ctx, organizationID, memberID, slug)
	}
	tool, err := s.composioTool(ctx, slug)
	if err != nil {
		return toolRoute{}, err
	}
	return toolRoute{slug: tool.Slug, toolkit: tool.Toolkit, readOnly: tool.ReadOnly()}, nil
}

// runOwnTool runs one tool through the member's own integration, and records
// it exactly as a Composio run is recorded: the row in apps_actions, the
// idempotency key a write carries, the replay that key answers, and the
// apps.action.* events a write announces.
//
// The arguments are checked against the tool's own input_schema first, and a
// read tool's session refuses to send anything but a read, so a tool wrongly
// marked read-only cannot quietly change something.
func (s *Service) runOwnTool(
	ctx context.Context,
	caller domain.OrgMembership,
	connection domain.Connection,
	tool ownTool,
	key string,
	arguments json.RawMessage,
	onlyReads bool,
) (ToolRun, error) {
	if problem := integration.CheckBytes(tool.Spec.InputSchema, arguments); problem != nil {
		return ToolRun{}, fmt.Errorf("%s does not take these arguments (%s): %w",
			tool.Spec.Name, integration.CheckDescription(problem), domain.ErrInvalid)
	}
	if onlyReads && !tool.Spec.ReadOnly {
		return ToolRun{}, fmt.Errorf("%s changes things, so it runs only once the member confirms: %w",
			tool.Spec.Name, domain.ErrNotReadOnly)
	}
	// The session is built before the run is recorded: a credential that
	// cannot be had has run nothing, so there is nothing to record.
	var options []signin.SessionOption
	if tool.Spec.ReadOnly {
		options = append(options, signin.ReadOnly(tool.Spec.ReadPOSTs...))
	}
	session, err := s.oryxa.Signin.Session(ctx, connection.ID, options...)
	if err != nil {
		return ToolRun{}, ownError(tool.Spec.Name, err)
	}
	if key != "" {
		// For a provider that takes one, a write's own key: so asking again
		// under it is the same write and the app can answer for itself.
		ctx = integration.WithIdempotencyKey(ctx, key)
	}

	id, err := newID("apa")
	if err != nil {
		return ToolRun{}, err
	}
	started, created, err := s.store.StartAction(ctx, domain.Action{
		ID: id, OrganizationID: connection.OrganizationID, MemberID: caller.UserID, ConnectionID: connection.ID,
		Toolkit: tool.Toolkit, Tool: tool.Spec.Name, ReadOnly: tool.Spec.ReadOnly, Agent: caller.Agent,
		IdempotencyKey: key, Status: domain.ActionRunning, CreatedAt: s.now().UTC(),
	})
	if err != nil {
		return ToolRun{}, err
	}
	if !created {
		return replay(started, tool.Spec.Name)
	}

	result, runErr := tool.Run(ctx, session, arguments)
	callErr := ownError(tool.Spec.Name, runErr)
	finished := started
	now := s.now().UTC()
	finished.FinishedAt = &now
	if runErr == nil {
		finished.Status = domain.ActionSucceeded
	} else {
		finished.Status = domain.ActionFailed
		finished.Error = ownReason(runErr)
	}
	if !tool.Spec.ReadOnly && len(result) <= domain.MaxResultBytes {
		// A write keeps what the app answered, so asking again under the same
		// key answers it; a read is simply run again.
		finished.Result = result
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
	if callErr != nil {
		return ToolRun{Action: finished}, callErr
	}
	return ToolRun{Action: finished, Data: result}, nil
}

// ownError is what an integration's own failure becomes in apps' terms: the
// same reasons Composio's are given, so a caller reads one set of words
// whichever broker ran the tool.
//
// It answers nil for an *AppError, which is not a failure of the call but the
// app's own answer. That is recorded on the action and given to the caller,
// which answers successful: false with the reason, as Composio's own refusal
// is answered.
func ownError(slug string, err error) error {
	if err == nil {
		return nil
	}
	var refused *integration.AppError
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.As(err, &refused):
		return nil
	case errors.Is(err, integration.ErrNeedsReconnect):
		return fmt.Errorf("%s: the app no longer accepts this connection: %w", slug, domain.ErrNeedsReconnect)
	case errors.Is(err, integration.ErrNotFound):
		return fmt.Errorf("%s: the app has no such thing: %w", slug, domain.ErrNotFound)
	case isRateLimited(err):
		return fmt.Errorf("%s: the app is rate limiting: %w", slug, domain.ErrRateLimited)
	case errors.Is(err, signin.ErrNotActive):
		// The connection ended between choosing it and calling through it.
		return fmt.Errorf("%s: the connection is no longer active: %w", slug, domain.ErrNotConnected)
	default:
		return fmt.Errorf("%s: the app did not answer: %w", slug, domain.ErrUnavailable)
	}
}

// isRateLimited says whether an integration reported a 429. RateLimited is
// an error with a duration in it and no sentinel to match, so it is asked
// for by kind.
func isRateLimited(err error) bool {
	var limited *integration.RateLimited
	return errors.As(err, &limited)
}

// ownReason is the short reason a failed own-tool run is recorded with. An
// app's own words are kept for a refusal, because they are what the caller
// needs to try again differently, and are bounded as Composio's are. Every
// other kind keeps only its name: an event travels further than the member
// who ran the tool.
func ownReason(err error) string {
	if err == nil {
		return ""
	}
	var refused *integration.AppError
	switch {
	case errors.As(err, &refused):
		if reason := truncate(refused.Error(), maxComposioMessage); reason != "" {
			return reason
		}
		return "the app refused the tool"
	case isRateLimited(err):
		return "rate_limited"
	case errors.Is(err, integration.ErrNeedsReconnect):
		return "needs_reconnect"
	case errors.Is(err, integration.ErrNotFound):
		return "not_found"
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return "unavailable"
	default:
		return truncate(err.Error(), maxComposioMessage)
	}
}

// notOwnTool is what a member is answered for a slug the registry does not
// have, over their own connection: Composio's key cannot act through a
// connection it holds no credential for, and this build's integrations do not
// carry that tool.
func notOwnTool(slug string) error {
	return fmt.Errorf("%s is not a tool Oryxa's own integrations carry, so it cannot run on an Oryxa connection: %w",
		slug, domain.ErrNotConnectable)
}
