package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

// pipedreamPrefix marks a tool as a Pipedream action. Pipedream's keys are
// lowercase with dashes, so they are carried as PD_ and the key in uppercase
// with underscores, which keeps them inside the tool slug rule every caller
// already checks and apart from every Composio and registry slug.
const pipedreamPrefix = "PD_"

// pipedreamSlug is the tool slug a Pipedream action key is offered under, and
// false for a key that cannot be one, such as one too long or with characters
// the slug rule refuses.
func pipedreamSlug(key string) (string, bool) {
	slug := pipedreamPrefix + strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
	if checked, err := domain.CheckTool(slug); err != nil || checked != slug {
		return "", false
	}
	return slug, true
}

// pipedreamAction is one Pipedream action as an agent is offered it.
type pipedreamAction struct {
	key         string
	slug        string
	name        string
	description string
	readOnly    bool
	destructive bool
	schema      json.RawMessage
	// props are the props an agent may set, and appProp the one apps fills
	// with the member's own account, which the agent never sees nor sets.
	props   map[string]bool
	appProp string
}

type cachedPipedreamActions struct {
	actions []pipedreamAction
	expires time.Time
}

// pipedreamProp is one of a component's configurable props, as far as
// translating it needs.
type pipedreamProp struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	Label         string `json:"label"`
	Description   string `json:"description"`
	Optional      bool   `json:"optional"`
	Hidden        bool   `json:"hidden"`
	RemoteOptions bool   `json:"remoteOptions"`
	ReloadProps   bool   `json:"reloadProps"`
}

// pipedreamTypes are the prop types an agent can fill in one call, as JSON
// Schema.
var pipedreamTypes = map[string]map[string]any{
	"string":   {"type": "string"},
	"integer":  {"type": "integer"},
	"boolean":  {"type": "boolean"},
	"object":   {"type": "object"},
	"string[]": {"type": "array", "items": map[string]any{"type": "string"}},
}

// translatePipedreamAction makes a component an agent's tool, or answers why
// it cannot be one. An action whose props reload or must be chosen from a
// list Pipedream fetches cannot be configured in a single call, so it is not
// offered at all rather than offered and failing.
func translatePipedreamAction(component PipedreamComponent) (pipedreamAction, string) {
	slug, ok := pipedreamSlug(component.Key)
	if !ok {
		return pipedreamAction{}, "its key cannot be a tool slug"
	}
	var props []pipedreamProp
	if len(component.Props) > 0 {
		if err := json.Unmarshal(component.Props, &props); err != nil {
			return pipedreamAction{}, "its props cannot be read"
		}
	}
	action := pipedreamAction{
		key: component.Key, slug: slug, name: component.Name, description: component.Description,
		// Only an explicit read-only mark is a read: a missing one is a
		// write, which waits for the member and needs its idempotency key.
		readOnly:    component.ReadOnlyHint != nil && *component.ReadOnlyHint,
		destructive: component.DestructiveHint != nil && *component.DestructiveHint,
		props:       map[string]bool{},
	}
	properties := map[string]any{}
	required := []string{}
	for _, prop := range props {
		switch {
		case prop.ReloadProps:
			return pipedreamAction{}, "a prop reloads the others"
		case prop.Type == "app":
			action.appProp = prop.Name
			continue
		case prop.Hidden || prop.Type == "alert" || strings.HasPrefix(prop.Type, "$."):
			// Notes to a human and props Pipedream fills itself.
			continue
		case prop.RemoteOptions && !prop.Optional:
			return pipedreamAction{}, "a required prop's options are fetched remotely"
		}
		base, known := pipedreamTypes[prop.Type]
		if !known {
			if !prop.Optional {
				return pipedreamAction{}, "a required prop is of type " + prop.Type
			}
			continue
		}
		schema := map[string]any{}
		for name, value := range base {
			schema[name] = value
		}
		if description := firstNonEmpty(prop.Description, prop.Label); description != "" {
			schema["description"] = description
		}
		properties[prop.Name] = schema
		action.props[prop.Name] = true
		if !prop.Optional {
			required = append(required, prop.Name)
		}
	}
	schema, err := json.Marshal(map[string]any{"type": "object", "properties": properties, "required": required})
	if err != nil {
		return pipedreamAction{}, "its schema cannot be written"
	}
	action.schema = schema
	return action, ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// pipedreamActionsFor answers an app's actions as agents are offered them,
// from a short-lived cache or from Pipedream. The deny list is applied here,
// so a denied action is neither listed nor resolvable.
func (s *Service) pipedreamActionsFor(ctx context.Context, app string) ([]pipedreamAction, error) {
	now := s.now()
	s.toolsMu.Lock()
	cached, ok := s.pipedreamActions[app]
	s.toolsMu.Unlock()
	if ok && now.Before(cached.expires) {
		return cached.actions, nil
	}
	components, err := s.pipedream.ListActions(ctx, app)
	if err != nil {
		return nil, pipedreamError("list the app's actions", err)
	}
	actions := make([]pipedreamAction, 0, len(components))
	slugs := map[string]int{}
	dropped := map[string]int{}
	for _, component := range components {
		if s.pipedreamDeny[component.Key] {
			continue
		}
		action, reason := translatePipedreamAction(component)
		if reason != "" {
			dropped[reason]++
			continue
		}
		slugs[action.slug]++
		actions = append(actions, action)
	}
	// Two keys that differ only in a dash and an underscore would be one
	// slug, so neither is offered rather than one running as the other.
	actions = slices.DeleteFunc(actions, func(action pipedreamAction) bool {
		if slugs[action.slug] > 1 {
			dropped["its slug collides with another action's"]++
			return true
		}
		return false
	})
	if len(dropped) > 0 {
		total := 0
		for _, count := range dropped {
			total += count
		}
		s.logger.Info("pipedream actions not offered to agents", "app", app, "dropped", total, "kept", len(actions), "reasons", dropped)
	}
	s.toolsMu.Lock()
	s.pipedreamActions[app] = cachedPipedreamActions{actions: actions, expires: now.Add(toolCacheFor)}
	s.toolsMu.Unlock()
	return actions, nil
}

// pipedreamApps are the apps a member has an active Pipedream connection to,
// in a stable order.
func (s *Service) pipedreamApps(ctx context.Context, organizationID, memberID, toolkit string) ([]string, error) {
	connections, err := s.store.Connections(ctx, organizationID, domain.ConnectionQuery{
		MemberID: memberID, Toolkit: toolkit, Statuses: []domain.ConnectionStatus{domain.ConnectionActive},
	})
	if err != nil {
		return nil, err
	}
	var apps []string
	for _, connection := range connections {
		if connection.Broker == domain.BrokerPipedream && !slices.Contains(apps, connection.Toolkit) {
			apps = append(apps, connection.Toolkit)
		}
	}
	sort.Strings(apps)
	return apps, nil
}

// pipedreamRoute resolves a PD_ slug against the member's own Pipedream
// apps. Nothing is remembered between calls about which app a slug belongs
// to: the member's connections answer it each time, so a slug for an app the
// member has not connected is refused however it was learned.
func (s *Service) pipedreamRoute(ctx context.Context, organizationID, memberID, slug string) (toolRoute, error) {
	apps, err := s.pipedreamApps(ctx, organizationID, memberID, "")
	if err != nil {
		return toolRoute{}, err
	}
	for _, app := range apps {
		actions, err := s.pipedreamActionsFor(ctx, app)
		if err != nil {
			return toolRoute{}, err
		}
		for _, action := range actions {
			if action.slug == slug {
				return toolRoute{slug: slug, toolkit: app, readOnly: action.readOnly, pipedream: &action}, nil
			}
		}
	}
	return toolRoute{}, fmt.Errorf("%s is not an action of any app you have connected: %w", slug, domain.ErrNotFound)
}

// pipedreamToolViews answers a toolkit's Pipedream actions when the member's
// connection to it is Pipedream's, and ok false otherwise. The organization's
// offer list was already applied by the caller.
func (s *Service) pipedreamToolViews(ctx context.Context, organizationID, memberID, toolkit, query string, limit int) ([]ToolView, bool, error) {
	if s.pipedream == nil {
		return nil, false, nil
	}
	apps, err := s.pipedreamApps(ctx, organizationID, memberID, toolkit)
	if err != nil || len(apps) == 0 {
		return nil, false, err
	}
	actions, err := s.pipedreamActionsFor(ctx, toolkit)
	if err != nil {
		return nil, true, err
	}
	search := strings.ToLower(strings.TrimSpace(query))
	views := make([]ToolView, 0, min(len(actions), limit))
	for _, action := range actions {
		if len(views) == limit {
			break
		}
		if search != "" && !strings.Contains(strings.ToLower(action.name), search) &&
			!strings.Contains(strings.ToLower(action.description), search) {
			continue
		}
		views = append(views, ToolView{
			Tool: action.slug, Name: action.name, Description: action.description, Toolkit: toolkit,
			ReadOnly: action.readOnly, Destructive: action.destructive, InputSchema: action.schema,
		})
	}
	return views, true, nil
}

// pipedreamUserID is the user Pipedream files a member's accounts under: the
// same per-member, per-organization user Composio's is, so one rule keeps
// every member's accounts apart in both.
func pipedreamUserID(connection domain.Connection) string {
	if connection.ComposioUserID != "" {
		return connection.ComposioUserID
	}
	return domain.ComposioUserID(connection.OrganizationID, connection.MemberID)
}

// runPipedreamTool runs one Pipedream action as the member, and records it
// exactly as a Composio run is recorded: the row in apps_actions, whose broker
// the connection fills, the idempotency key a write carries, the replay that
// key answers, and the apps.action.* events a write announces.
func (s *Service) runPipedreamTool(
	ctx context.Context,
	caller domain.OrgMembership,
	connection domain.Connection,
	action pipedreamAction,
	key string,
	arguments map[string]json.RawMessage,
) (ToolRun, error) {
	if connection.Broker != domain.BrokerPipedream {
		return ToolRun{}, fmt.Errorf("%s is a Pipedream action, and the %s connection is %s's: %w",
			action.slug, connection.Toolkit, connection.Broker, domain.ErrNotConnectable)
	}
	props := make(map[string]any, len(arguments)+1)
	for name, value := range arguments {
		// The app prop is refused like any unknown one: the account a run
		// uses is always the member's own, never one an agent names.
		if !action.props[name] {
			return ToolRun{}, fmt.Errorf("%s takes no argument %q: %w", action.slug, name, domain.ErrInvalid)
		}
		props[name] = value
	}
	if action.appProp != "" {
		if connection.PipedreamAccountID == "" {
			return ToolRun{}, fmt.Errorf("the %s connection has no account yet: %w", connection.Toolkit, domain.ErrNotConnected)
		}
		props[action.appProp] = map[string]any{"authProvisionId": connection.PipedreamAccountID}
	}

	id, err := newID("apa")
	if err != nil {
		return ToolRun{}, err
	}
	started, created, err := s.store.StartAction(ctx, domain.Action{
		ID: id, OrganizationID: connection.OrganizationID, MemberID: caller.UserID, ConnectionID: connection.ID,
		Toolkit: connection.Toolkit, Tool: action.slug, ReadOnly: action.readOnly, Agent: caller.Agent,
		IdempotencyKey: key, Status: domain.ActionRunning, CreatedAt: s.now().UTC(),
	})
	if err != nil {
		return ToolRun{}, err
	}
	if !created {
		return replay(started, action.slug)
	}

	result, runErr := s.pipedream.RunAction(ctx, PipedreamRunRequest{
		ExternalUserID: pipedreamUserID(connection), ComponentKey: action.key, ConfiguredProps: props,
	})
	var data json.RawMessage
	if runErr == nil {
		data, runErr = json.Marshal(struct {
			Return  json.RawMessage `json:"ret,omitempty"`
			Exports json.RawMessage `json:"exports,omitempty"`
		}{result.Return, result.Exports})
	}
	callErr := pipedreamError("run "+action.slug, runErr)
	finished := started
	now := s.now().UTC()
	finished.FinishedAt = &now
	var refused *PipedreamActionError
	switch {
	case runErr == nil:
		finished.Status = domain.ActionSucceeded
	case callErr == nil && errors.As(runErr, &refused):
		// The app's own reason is kept for the caller, bounded as Composio's
		// is; the event it announces says only app_error.
		finished.Status = domain.ActionFailed
		finished.Error = firstNonEmpty(truncate(refused.Message, maxComposioMessage), "the app refused the tool")
	default:
		finished.Status = domain.ActionFailed
		finished.Error = actionReason(callErr)
	}
	if !finished.ReadOnly && len(data) <= domain.MaxResultBytes {
		// A write keeps what the app answered, so asking again under the same
		// key answers it; a read is simply run again.
		finished.Result = data
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
	return ToolRun{Action: finished, Data: data}, nil
}

// pipedreamError is a Pipedream failure in apps' terms. It answers nil for an
// action the app refused, which is not a failure of the call but the app's
// own answer, given to the caller as a failed run, as Composio's is. An app
// answering 429 is rate limiting, not refusing, so the caller waits instead.
func pipedreamError(doing string, err error) error {
	if err == nil {
		return nil
	}
	var refused *PipedreamActionError
	switch {
	case errors.As(err, &refused) && refused.UpstreamStatus == http.StatusTooManyRequests:
		return fmt.Errorf("%s: the app is rate limiting: %w", doing, domain.ErrRateLimited)
	case errors.As(err, &refused):
		return nil
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, domain.ErrRateLimited):
		return fmt.Errorf("%s: Pipedream is rate limiting: %w", doing, domain.ErrRateLimited)
	default:
		return fmt.Errorf("%s: Pipedream did not answer: %w: %w", doing, domain.ErrUnavailable, err)
	}
}
