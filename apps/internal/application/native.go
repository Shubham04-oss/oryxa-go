package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

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
	return s.store.ApplyNativeConnection(ctx, domain.Connection{
		ID: id, OrganizationID: event.OrganizationID, MemberID: event.MemberID, Toolkit: toolkit,
		Broker: domain.BrokerNative, NativeConnectionID: event.ID, Status: status, StatusReason: reason,
	}, event.UpdatedAt.UTC())
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
