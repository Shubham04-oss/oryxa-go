package adksession

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/adk/v2/session"
)

// The session state keys Krude's instruction reads, and the organization the
// session belongs to. Keep them aligned with the keys in internal/agents:
// UserNameKey, UserAboutKey, UserEmailKey, UserRoleKey, OrgNameKey and
// OrganizationIDKey.
const (
	StateUserName       = "user_name"
	StateUserAbout      = "user_about"
	StateUserEmail      = "user_email"
	StateUserRole       = "user_role"
	StateOrgName        = "org_name"
	StateOrganizationID = "organization_id"
)

// memberFinder reads the member a session is created for: in the named
// organization, or in their first when none is named.
type memberFinder interface {
	Member(ctx context.Context, userID, organizationID string) (Member, error)
}

// SessionEvents records a session's lifecycle for the events outbox. The
// session runtime calls it best-effort after the session service has done the
// work: a recorder failure is logged, never fatal, because the session itself
// succeeded and a retry would create a second one. Record what was lost and
// the dispatcher publishes whatever the outbox already holds.
type SessionEvents interface {
	RecordSessionCreated(ctx context.Context, appName, sessionID, userID, organizationID string) error
	RecordSessionDeleted(ctx context.Context, appName, sessionID, userID, organizationID string) error
}

// seedingSessionService wraps a session.Service so a session created for a
// member starts with the state Krude's instruction reads: their name, a few
// words about them, their organization's name and its ID. The wrapped service
// remains the only storage; this only fills state keys the caller did not set.
// It also reports session creation and deletion to SessionEvents when one is
// configured.
type seedingSessionService struct {
	session.Service
	members memberFinder
	events  SessionEvents
}

// Create seeds member state into the new session when the request's user is a
// member. A caller that sets organization_id in the request's state is seeded
// from that organization; one that does not, from the user's first. A user
// who is not a member there keeps the state the caller sent, so the
// development UI can still create bare sessions. Seeding decides nothing
// about access: whoever creates a session for a member checks that they
// belong to the organization first, as the run API does with auth.
func (s seedingSessionService) Create(ctx context.Context, request *session.CreateRequest) (*session.CreateResponse, error) {
	organizationID, _ := request.State[StateOrganizationID].(string)
	member, err := s.members.Member(ctx, request.UserID, organizationID)
	if errors.Is(err, ErrMemberNotFound) {
		created, err := s.Service.Create(ctx, request)
		s.recordCreated(ctx, request.AppName, request.UserID, created, err)
		return created, err
	}
	if err != nil {
		return nil, err
	}

	seeded := make(map[string]any, len(request.State)+4)
	for key, value := range request.State {
		seeded[key] = value
	}
	if member.DisplayName != "" {
		setDefault(seeded, StateUserName, member.DisplayName)
	}
	setDefault(seeded, StateUserAbout, memberAbout(member.Role))
	if member.Email != "" {
		setDefault(seeded, StateUserEmail, member.Email)
	}
	if member.Role != "" {
		setDefault(seeded, StateUserRole, member.Role)
	}
	setDefault(seeded, StateOrgName, member.OrganizationName)
	setDefault(seeded, StateOrganizationID, member.OrganizationID)
	request.State = seeded

	created, err := s.Service.Create(ctx, request)
	s.recordCreated(ctx, request.AppName, request.UserID, created, err)
	return created, err
}

// Delete reports the session's deletion after the service deleted it. The
// organization is read before the delete, because the state goes with the
// session.
func (s seedingSessionService) Delete(ctx context.Context, request *session.DeleteRequest) error {
	var organizationID string
	if s.events != nil {
		if current, err := s.Service.Get(ctx, &session.GetRequest{
			AppName: request.AppName, UserID: request.UserID, SessionID: request.SessionID,
		}); err == nil && current != nil {
			organizationID = stateString(current.Session.State(), StateOrganizationID)
		}
	}
	if err := s.Service.Delete(ctx, request); err != nil {
		return err
	}
	if s.events != nil {
		if err := s.events.RecordSessionDeleted(ctx, request.AppName, request.SessionID, request.UserID, organizationID); err != nil {
			slog.Default().WarnContext(ctx, "record session deleted event", "session_id", request.SessionID, "error", err)
		}
	}
	return nil
}

func (s seedingSessionService) recordCreated(ctx context.Context, appName, userID string, created *session.CreateResponse, createErr error) {
	if s.events == nil || createErr != nil || created == nil || created.Session == nil {
		return
	}
	organizationID := stateString(created.Session.State(), StateOrganizationID)
	if err := s.events.RecordSessionCreated(ctx, appName, created.Session.ID(), userID, organizationID); err != nil {
		slog.Default().WarnContext(ctx, "record session created event", "session_id", created.Session.ID(), "error", err)
	}
}

func stateString(state session.ReadonlyState, key string) string {
	value, err := state.Get(key)
	if err != nil {
		return ""
	}
	text, _ := value.(string)
	return text
}

func setDefault(state map[string]any, key, value string) {
	if _, set := state[key]; !set {
		state[key] = value
	}
}

// memberAbout renders the member's role as the few words about them Krude's
// instruction reads after their name.
func memberAbout(role string) string {
	switch role {
	case "owner":
		return "the owner of their organization"
	case "admin":
		return "an admin of their organization"
	default:
		return "a member of their organization"
	}
}
