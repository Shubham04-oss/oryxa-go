package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/nativeclient"
)

// Native is the integrations service's internal API, as apps uses it.
// nativeclient.Client satisfies it.
type Native interface {
	Connect(ctx context.Context, request nativeclient.ConnectRequest) (string, error)
	CallTool(ctx context.Context, request nativeclient.CallRequest) (nativeclient.CallResult, error)
	Remove(ctx context.Context, org, connection string) error
	ListConnections(ctx context.Context, org, member string) ([]integrationsv1.Connection, error)
	ListTools(ctx context.Context, org, member, app string) ([]nativeclient.Tool, error)
}

// nativeLevel is the level of every connect made here: the org-level
// connection arrives with the admin screens.
const nativeLevel = "personal"

// nativeServes says a new connect to toolkit goes to the integrations
// service. A toolkit pinned by APPS_TOOLKIT_BROKERS stays where it is pinned.
func (s *Service) nativeServes(toolkit string) bool {
	if _, pinned := s.toolkitBrokers[toolkit]; pinned {
		return false
	}
	return slices.Contains(s.nativeToolkits, toolkit)
}

// connectNative starts a member's sign-in at the integrations service and
// answers the URL their browser goes to. It checks what every connect checks,
// and records nothing: the connection itself is the integrations service's.
func (s *Service) connectNative(ctx context.Context, credentials authclient.Credentials, organizationID string, input ConnectInput) (string, error) {
	start, err := s.startConnect(ctx, credentials, organizationID, input)
	if err != nil {
		return "", err
	}
	url, err := s.native.Connect(ctx, nativeclient.ConnectRequest{
		Org: organizationID, Member: start.memberID, App: start.toolkit, Level: nativeLevel, ReturnTo: start.returnTo,
	})
	switch {
	case err == nil:
		s.logger.InfoContext(ctx, "native app connection started",
			"organization_id", organizationID, "member_id", start.memberID, "toolkit", start.toolkit)
		return url, nil
	case errors.Is(err, nativeclient.ErrNotSetUp):
		return "", fmt.Errorf("an admin needs to set up %s first: %w", start.toolkit, domain.ErrNotConnectable)
	case errors.Is(err, nativeclient.ErrBroken):
		return "", fmt.Errorf("the organization's %s app is not working; an admin needs to fix it: %w", start.toolkit, domain.ErrNotConnectable)
	case errors.Is(err, nativeclient.ErrNotFound):
		return "", fmt.Errorf("%s: %w", start.toolkit, domain.ErrNotConnectable)
	case errors.Is(err, nativeclient.ErrInvalid):
		return "", fmt.Errorf("start the connection: %w", domain.ErrInvalid)
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return "", err
	}
	s.logger.ErrorContext(ctx, "start a native app connection", "toolkit", start.toolkit, "error", err)
	return "", fmt.Errorf("start the connection: the integrations service did not answer: %w", domain.ErrUnavailable)
}

// checkNativeToolkits is what New requires of the settings: toolkits are
// slugs, and with any named there is a service to send them to.
func checkNativeToolkits(toolkits []string, native Native) ([]string, error) {
	checked := make([]string, 0, len(toolkits))
	for _, toolkit := range toolkits {
		value, err := domain.CheckToolkit(strings.TrimSpace(toolkit))
		if err != nil {
			return nil, fmt.Errorf("native toolkit %q: %w", toolkit, err)
		}
		checked = append(checked, value)
	}
	if len(checked) > 0 && native == nil {
		return nil, errors.New("native toolkits are named, and the integrations service is not configured")
	}
	return checked, nil
}

// NativeConnectionChanged applies a connection.created or connection.updated
// event: the apps row for the connection is made if it is new, and its status
// follows the event's, unless a newer event was already applied.
func (s *Service) NativeConnectionChanged(ctx context.Context, event integrationsv1.Connection) error {
	status, reason := nativeStatus(event.Status)
	return s.applyNative(ctx, event, status, reason)
}

// NativeConnectionRemoved applies a connection.removed event: the row is
// retired for good.
func (s *Service) NativeConnectionRemoved(ctx context.Context, event integrationsv1.Connection) error {
	return s.applyNative(ctx, event, domain.ConnectionRemoved, "")
}

// nativeStatus is the apps status of an integrations status. Degraded stays
// usable, as the service itself decides per call whether it is.
func nativeStatus(status string) (domain.ConnectionStatus, string) {
	switch status {
	case integrationsv1.StatusNeedsReauth:
		return domain.ConnectionExpired, status
	case integrationsv1.StatusError:
		return domain.ConnectionFailed, status
	case integrationsv1.StatusRevoked:
		return domain.ConnectionRemoved, ""
	case integrationsv1.StatusDegraded:
		return domain.ConnectionActive, status
	}
	return domain.ConnectionActive, ""
}

func (s *Service) applyNative(ctx context.Context, event integrationsv1.Connection, status domain.ConnectionStatus, reason string) error {
	toolkit, err := domain.CheckToolkit(event.App)
	// Retrying cannot fix an event that names nobody, so it is logged and
	// dropped. An org-level connection has no member to attach a row to; the
	// admin screens that use them are not built.
	if err != nil || event.ID == "" || event.OrganizationID == "" || event.MemberID == "" || event.Level != integrationsv1.LevelPersonal {
		s.logger.WarnContext(ctx, "native connection event not applied",
			"native_connection_id", event.ID, "organization_id", event.OrganizationID, "level", event.Level, "app", event.App)
		return nil
	}
	id, err := newID("apc")
	if err != nil {
		return err
	}
	row := domain.Connection{
		ID: id, OrganizationID: event.OrganizationID, MemberID: event.MemberID, Toolkit: toolkit,
		Broker: domain.BrokerNative, NativeConnectionID: event.ID, Status: status, StatusReason: reason,
	}
	at := event.UpdatedAt.UTC()
	err = s.store.ApplyNativeConnection(ctx, row, at)
	if errors.Is(err, domain.ErrConflict) && status == domain.ConnectionActive {
		return s.settleNativeRace(ctx, row, at)
	}
	return err
}

// RemovedReplaced is why a connection was retired for a newer one to the
// same app: apps keeps one current connection per member and app.
const RemovedReplaced = "replaced_by_newer_connection"

// settleNativeRace decides between a native connection turning active and
// the member's other current connection to the same app, which the
// one-current-connection index keeps from both being active. It happens when
// a member signs in through Composio or Pipedream while a native sign-in is
// under way, or reconnects one way while the other was expired.
//
// The rule: a finished connection beats one still pending, and between two
// finished ones the newer sign-in wins: the native connection's UpdatedAt
// against the other's ConnectedAt, a tie going to the native one. The loser
// is revoked at its broker and its row retired as RemovedReplaced. The inputs
// are the stored rows and the event, so a retry or a second delivery
// decides the same way, and both steps are idempotent: a broker that no
// longer knows the connection has already revoked it, and a retired row is
// never changed again.
func (s *Service) settleNativeRace(ctx context.Context, row domain.Connection, at time.Time) error {
	current, err := s.store.Connections(ctx, row.OrganizationID, domain.ConnectionQuery{
		MemberID: row.MemberID, Toolkit: row.Toolkit,
		Statuses: []domain.ConnectionStatus{domain.ConnectionPending, domain.ConnectionActive},
	})
	if err != nil {
		return err
	}
	var other *domain.Connection
	for i := range current {
		if !(current[i].Native() && current[i].NativeConnectionID == row.NativeConnectionID) {
			other = &current[i]
			break
		}
	}
	if other == nil {
		// The other one went in the meantime; trying again applies the event.
		return s.store.ApplyNativeConnection(ctx, row, at)
	}
	if nativeWins(*other, at) {
		s.logger.InfoContext(ctx, "a newer native connection replaces the member's other connection",
			"organization_id", row.OrganizationID, "member_id", row.MemberID, "toolkit", row.Toolkit,
			"native_connection_id", row.NativeConnectionID, "replaced_connection_id", other.ID, "replaced_broker", other.Broker)
		if err := s.remove(ctx, *other, RemovedReplaced); err != nil {
			return err
		}
		return s.store.ApplyNativeConnection(ctx, row, at)
	}
	s.logger.InfoContext(ctx, "a native connection loses to the member's newer connection",
		"organization_id", row.OrganizationID, "member_id", row.MemberID, "toolkit", row.Toolkit,
		"native_connection_id", row.NativeConnectionID, "kept_connection_id", other.ID, "kept_broker", other.Broker)
	if s.native == nil {
		return fmt.Errorf("revoke a replaced native connection: the integrations service is not configured: %w", domain.ErrUnavailable)
	}
	if err := s.native.Remove(ctx, row.OrganizationID, row.NativeConnectionID); err != nil && !errors.Is(err, nativeclient.ErrNotFound) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("revoke a replaced native connection: %w", domain.ErrUnavailable)
	}
	row.Status, row.StatusReason = domain.ConnectionRemoved, RemovedReplaced
	return s.store.ApplyNativeConnection(ctx, row, at)
}

// nativeWins says a native connection that became active at at replaces
// other; see settleNativeRace.
func nativeWins(other domain.Connection, at time.Time) bool {
	if other.Status != domain.ConnectionActive || other.ConnectedAt == nil {
		return true
	}
	return !at.Before(*other.ConnectedAt)
}

// nativeReconcileEvery is how often the maintenance sweep reconciles.
const nativeReconcileEvery = time.Hour

// reconcileNativeOrganizations runs ReconcileNative, once an hour, for every
// organization with a native row. It rides the maintenance sweep rather
// than a worker of its own: it is the same kind of slow repair, every
// replica may run it (each step is the idempotent upsert events use), and an
// organization that fails is tried again next hour.
func (s *Service) reconcileNativeOrganizations(ctx context.Context) {
	if s.native == nil {
		return
	}
	if started := s.now(); s.nativeReconciledAt.IsZero() || started.Sub(s.nativeReconciledAt) >= nativeReconcileEvery {
		s.nativeReconciledAt = started
	} else {
		return
	}
	organizations, err := s.store.NativeOrganizations(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "list organizations with native connections", "error", err)
		}
		return
	}
	for _, organizationID := range organizations {
		if err := s.ReconcileNative(ctx, organizationID); err != nil {
			if ctx.Err() != nil {
				return
			}
			s.logger.ErrorContext(ctx, "reconcile native connections", "organization_id", organizationID, "error", err)
		}
	}
}

// ReconcileNative repairs an organization's native rows from the integrations
// service's own list, for events apps missed: each listed personal
// connection is applied through the same upsert its events go through, so a
// row is made, or its status brought up to date, unless a newer event was
// applied already; and a row whose connection the list no longer has is
// retired, as its removed event would have.
//
// The rows are read before the list is, so a connection made after the list
// was read cannot be retired for being missing from it.
func (s *Service) ReconcileNative(ctx context.Context, organizationID string) error {
	if s.native == nil {
		return nil
	}
	rows, err := s.store.Connections(ctx, organizationID, domain.ConnectionQuery{})
	if err != nil {
		return err
	}
	listed, err := s.native.ListConnections(ctx, organizationID, "")
	if err != nil {
		return fmt.Errorf("list the organization's native connections: %w", err)
	}
	live := make(map[string]bool, len(listed))
	var failures []error
	for _, connection := range listed {
		live[connection.ID] = true
		if connection.OrganizationID != organizationID || connection.Level != integrationsv1.LevelPersonal {
			continue // org-level connections have no rows yet
		}
		status, reason := nativeStatus(connection.Status)
		if err := s.applyNative(ctx, connection, status, reason); err != nil {
			failures = append(failures, fmt.Errorf("apply native connection %s: %w", connection.ID, err))
		}
	}
	at := s.now().UTC()
	for _, row := range rows {
		if !row.Native() || live[row.NativeConnectionID] {
			continue
		}
		s.logger.InfoContext(ctx, "a native connection the integrations service no longer has is retired",
			"connection_id", row.ID, "organization_id", organizationID, "native_connection_id", row.NativeConnectionID)
		row.Status, row.StatusReason = domain.ConnectionRemoved, ""
		if err := s.store.ApplyNativeConnection(ctx, row, at); err != nil {
			failures = append(failures, fmt.Errorf("retire native connection %s: %w", row.NativeConnectionID, err))
		}
	}
	return errors.Join(failures...)
}

// nativeToolViews answers the tools of a member's active native connection
// to toolkit, as the integrations service's manifests declare them, under the
// slugs nativeRoute runs them by. ok is false when the member has no such
// connection, so the toolkit's tools are someone else's to answer.
func (s *Service) nativeToolViews(ctx context.Context, organizationID, memberID, toolkit, query string, limit int) ([]ToolView, bool, error) {
	if s.native == nil {
		return nil, false, nil
	}
	connections, err := s.store.Connections(ctx, organizationID, domain.ConnectionQuery{
		MemberID: memberID, Toolkit: toolkit, Statuses: []domain.ConnectionStatus{domain.ConnectionActive},
	})
	if err != nil {
		return nil, false, err
	}
	if !slices.ContainsFunc(connections, domain.Connection.Native) {
		return nil, false, nil
	}
	tools, err := s.native.ListTools(ctx, organizationID, memberID, toolkit)
	switch {
	case err == nil:
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return nil, true, err
	default:
		s.logger.ErrorContext(ctx, "list native tools", "toolkit", toolkit, "error", err)
		return nil, true, fmt.Errorf("list the app's tools: the integrations service did not answer: %w", domain.ErrUnavailable)
	}
	search := strings.ToLower(strings.TrimSpace(query))
	views := make([]ToolView, 0, min(len(tools), limit))
	for _, tool := range tools {
		if len(views) == limit {
			break
		}
		slug, ok := nativeSlug(toolkit, tool)
		if !ok {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(tool.Name), search) &&
			!strings.Contains(strings.ToLower(tool.Description), search) {
			continue
		}
		views = append(views, ToolView{
			Tool: slug, Name: tool.Name, Description: tool.Description, Toolkit: toolkit,
			ReadOnly: tool.Effect == "read", Destructive: tool.Effect == "delete", InputSchema: tool.InputSchema,
		})
	}
	return views, true, nil
}

// nativeSlug is the slug a native tool runs under: github.list_issues is
// GITHUB_LIST_ISSUES, the inverse of what runNativeTool sends. A tool of
// another app, or whose name makes no slug, is not offered.
func nativeSlug(toolkit string, tool nativeclient.Tool) (string, bool) {
	name, ok := strings.CutPrefix(tool.Name, toolkit+".")
	if !ok || tool.App != toolkit || name == "" {
		return "", false
	}
	slug, err := domain.CheckTool(strings.ToUpper(toolkit) + "_" + strings.ToUpper(name))
	if err != nil {
		return "", false
	}
	return slug, true
}

// nativeRoute answers the route of a slug that names a tool of one of the
// member's active native connections: the toolkit's name in uppercase, an
// underscore, then the tool's name, so GITHUB_LIST_ISSUES is github.list_issues.
// Only the member's own connections say which slugs are theirs.
func (s *Service) nativeRoute(ctx context.Context, organizationID, memberID, slug string) (toolRoute, bool, error) {
	if s.native == nil {
		return toolRoute{}, false, nil
	}
	connections, err := s.store.Connections(ctx, organizationID, domain.ConnectionQuery{
		MemberID: memberID, Statuses: []domain.ConnectionStatus{domain.ConnectionActive},
	})
	if err != nil {
		return toolRoute{}, false, err
	}
	for _, connection := range connections {
		if connection.Native() && strings.HasPrefix(slug, strings.ToUpper(connection.Toolkit)+"_") {
			// readOnly stays false: apps cannot tell a read from a write here,
			// so a specialist needs a write grant to the app, and the
			// integrations service refuses an unconfirmed change.
			return toolRoute{slug: slug, toolkit: connection.Toolkit, native: true}, true, nil
		}
	}
	return toolRoute{}, false, nil
}

// runNativeTool runs one tool through the integrations service. It records no
// apps_actions row: the service keeps its own audit of every call.
func (s *Service) runNativeTool(
	ctx context.Context,
	caller domain.OrgMembership,
	connection domain.Connection,
	slug string,
	arguments json.RawMessage,
	onlyReads bool,
) (ToolRun, error) {
	tool := strings.ToLower(connection.Toolkit) + "." + strings.ToLower(strings.TrimPrefix(slug, strings.ToUpper(connection.Toolkit)+"_"))
	agent, role := caller.AgentID, caller.AgentRole
	if !caller.Agent {
		agent, role = "member", "member"
	}
	result, err := s.native.CallTool(ctx, nativeclient.CallRequest{
		Org: connection.OrganizationID, Member: caller.UserID, Agent: agent, Role: role,
		Connection: connection.NativeConnectionID, Tool: tool, Input: arguments, Confirmed: !onlyReads,
	})
	switch {
	case err == nil:
	case errors.Is(err, nativeclient.ErrNotFound):
		return ToolRun{}, fmt.Errorf("no tool is called %s: %w", slug, domain.ErrNotFound)
	case errors.Is(err, nativeclient.ErrConfirmation):
		return ToolRun{}, fmt.Errorf("%s changes things, so it runs only once the member confirms: %w", slug, domain.ErrNotReadOnly)
	case errors.Is(err, nativeclient.ErrNeedsReauth):
		return ToolRun{}, fmt.Errorf("the %s connection needs signing in again: %w", connection.Toolkit, domain.ErrNeedsReconnect)
	case errors.Is(err, nativeclient.ErrUnavailable):
		return ToolRun{}, fmt.Errorf("the %s connection is not usable right now: %w", connection.Toolkit, domain.ErrNotConnected)
	case errors.Is(err, nativeclient.ErrInvalid):
		return ToolRun{}, fmt.Errorf("%s does not take these arguments: %w", slug, domain.ErrInvalid)
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return ToolRun{}, err
	default:
		// Includes output_too_large: the caller can do nothing about it, and
		// the service's message is not repeated.
		s.logger.ErrorContext(ctx, "run a native tool", "toolkit", connection.Toolkit, "tool", tool, "error", err)
		return ToolRun{}, fmt.Errorf("run %s: the integrations service did not answer: %w", slug, domain.ErrUnavailable)
	}
	now := s.now().UTC()
	action := domain.Action{
		OrganizationID: connection.OrganizationID, MemberID: caller.UserID, ConnectionID: connection.ID,
		Toolkit: connection.Toolkit, Tool: slug, Agent: caller.Agent, AgentID: caller.AgentID, AgentRole: caller.AgentRole,
		Status: domain.ActionSucceeded, CreatedAt: now, FinishedAt: &now,
	}
	if result.ToolError != nil {
		// The app said no: that is the tool's answer, as at Composio.
		action.Status = domain.ActionFailed
		action.Error = truncate(result.ToolError.Message, maxComposioMessage)
		return ToolRun{Action: action}, nil
	}
	return ToolRun{Action: action, Data: result.Output}, nil
}
