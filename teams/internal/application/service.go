package application

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/teams/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
)

type Store interface {
	UpsertOrganization(context.Context, string, string, time.Time) error
	Organization(context.Context, string) (domain.Organization, error)
	CreateTeam(context.Context, string, domain.Team, domain.Membership, []domain.Event) error
	UpdateTeam(context.Context, domain.Team, domain.Event) error
	DeleteTeam(context.Context, string, string, []domain.Event) error
	Team(context.Context, string, string) (domain.Team, error)
	Teams(context.Context, string) ([]domain.Team, error)
	TeamsForUser(context.Context, string, string) ([]domain.Team, error)
	Membership(context.Context, string, string) (domain.Membership, error)
	Members(context.Context, string) ([]domain.Membership, error)
	AddMember(context.Context, domain.Membership, domain.Event) error
	ChangeMemberRole(context.Context, string, string, domain.Role, domain.Role, domain.Event) error
	RemoveMember(context.Context, string, string, domain.Role, domain.Event) error
	RemoveUserFromOrganization(context.Context, string, string, func(domain.Team, domain.Membership) (domain.Event, error)) error
	DeleteOrganizationTeams(context.Context, string, func(domain.Team, []domain.Membership) ([]domain.Event, error)) error
	RecordEvents(context.Context, []domain.Event) error
	SetNotifyEmail(context.Context, string, string, bool) error
}

type Directory interface {
	Membership(context.Context, string, authclient.Credentials) (domain.OrgMembership, error)
	Members(context.Context, string, authclient.Credentials) ([]domain.OrgMembership, error)
}

type CreateTeamInput struct {
	Name string `json:"name"`
}

type UpdateTeamInput struct {
	Name string `json:"name"`
}

type AddMemberInput struct {
	UserID string      `json:"user_id"`
	Role   domain.Role `json:"role"`
}

type TeamSummary struct {
	domain.Team
	Role      domain.Role `json:"role,omitempty"`
	CanManage bool        `json:"can_manage"`
}

type TeamDetail struct {
	Team       domain.Team        `json:"team"`
	Membership *domain.Membership `json:"membership"`
	CanManage  bool               `json:"can_manage"`
}

type Service struct {
	store     Store
	directory Directory
	logger    *slog.Logger
	now       func() time.Time
}

func New(store Store, directory Directory, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{store: store, directory: directory, logger: logger, now: time.Now}
}

func (s *Service) CreateTeam(ctx context.Context, credentials authclient.Credentials, organizationID string, input CreateTeamInput) (domain.Team, error) {
	org, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.Team{}, err
	}
	if !domain.CanCreateTeam(org.Role) {
		return domain.Team{}, domain.ErrForbidden
	}
	name, err := teamName(input.Name)
	if err != nil {
		return domain.Team{}, err
	}
	teamID, err := randomID("team_", 12)
	if err != nil {
		return domain.Team{}, err
	}
	now := s.now().UTC()
	team := domain.Team{
		ID:             teamID,
		OrganizationID: organizationID,
		Name:           name,
		Path:           domain.TeamPath(organizationID, teamID),
		CreatedBy:      org.UserID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	creator := domain.Membership{
		TeamID:         team.ID,
		OrganizationID: organizationID,
		UserID:         org.UserID,
		Email:          org.Email,
		DisplayName:    org.DisplayName,
		Role:           domain.RoleAdmin,
		NotifyEmail:    true,
		JoinedAt:       now,
	}
	created, err := s.event(teamseventsv1.TeamCreatedDetailType, teamseventsv1.TeamCreated{
		TeamID:           team.ID,
		OrganizationID:   organizationID,
		OrganizationName: org.OrganizationName,
		Name:             team.Name,
		CreatedBy:        org.UserID,
		CreatedAt:        now,
	}, now)
	if err != nil {
		return domain.Team{}, err
	}
	added, err := s.memberAddedEvent(team, org.OrganizationName, creator, org.UserID, now)
	if err != nil {
		return domain.Team{}, err
	}
	if err := s.store.CreateTeam(ctx, org.OrganizationName, team, creator, []domain.Event{created, added}); err != nil {
		return domain.Team{}, err
	}
	return team, nil
}

func (s *Service) Teams(ctx context.Context, credentials authclient.Credentials, organizationID string) ([]TeamSummary, error) {
	teams, org, err := s.visibleTeams(ctx, credentials, organizationID)
	if err != nil {
		return nil, err
	}
	summaries := make([]TeamSummary, 0, len(teams))
	for _, team := range teams {
		membership, membershipErr := s.store.Membership(ctx, team.ID, org.UserID)
		onTeam := membershipErr == nil
		if membershipErr != nil && !errors.Is(membershipErr, domain.ErrNotFound) {
			return nil, membershipErr
		}
		summary := TeamSummary{Team: team, CanManage: domain.CanManageTeam(org.Role, membership.Role)}
		if onTeam {
			summary.Role = membership.Role
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

func (s *Service) Team(ctx context.Context, credentials authclient.Credentials, organizationID, teamID string) (TeamDetail, error) {
	actor, err := s.teamActor(ctx, credentials, organizationID, teamID)
	if err != nil {
		return TeamDetail{}, err
	}
	if !domain.CanSeeTeam(actor.org.Role, actor.onTeam()) {
		return TeamDetail{}, domain.ErrNotFound
	}
	return TeamDetail{Team: actor.team, Membership: actor.membership, CanManage: actor.canManage()}, nil
}

func (s *Service) UpdateTeam(ctx context.Context, credentials authclient.Credentials, organizationID, teamID string, input UpdateTeamInput) (domain.Team, error) {
	actor, err := s.teamActor(ctx, credentials, organizationID, teamID)
	if err != nil {
		return domain.Team{}, err
	}
	if !actor.canSee() {
		return domain.Team{}, domain.ErrNotFound
	}
	if !actor.canManage() {
		return domain.Team{}, domain.ErrForbidden
	}
	name, err := teamName(input.Name)
	if err != nil {
		return domain.Team{}, err
	}
	if actor.team.Name == name {
		return actor.team, nil
	}
	now := s.now().UTC()
	actor.team.Name = name
	actor.team.UpdatedAt = now
	event, err := s.event(teamseventsv1.TeamUpdatedDetailType, teamseventsv1.TeamUpdated{
		TeamID:           actor.team.ID,
		OrganizationID:   organizationID,
		OrganizationName: actor.org.OrganizationName,
		Name:             name,
		UpdatedBy:        actor.org.UserID,
		UpdatedAt:        now,
	}, now)
	if err != nil {
		return domain.Team{}, err
	}
	if err := s.store.UpdateTeam(ctx, actor.team, event); err != nil {
		return domain.Team{}, err
	}
	return actor.team, nil
}

func (s *Service) DeleteTeam(ctx context.Context, credentials authclient.Credentials, organizationID, teamID string) error {
	actor, err := s.teamActor(ctx, credentials, organizationID, teamID)
	if err != nil {
		return err
	}
	if !actor.canSee() {
		return domain.ErrNotFound
	}
	if !actor.canManage() {
		return domain.ErrForbidden
	}
	members, err := s.store.Members(ctx, teamID)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	events, err := s.teamDeletedEvents(actor.team, actor.org.OrganizationName, members, actor.org.UserID, teamseventsv1.MemberRemovedReasonTeamDeleted, now)
	if err != nil {
		return err
	}
	return s.store.DeleteTeam(ctx, organizationID, teamID, events)
}

func (s *Service) Members(ctx context.Context, credentials authclient.Credentials, organizationID, teamID string) ([]domain.Membership, error) {
	actor, err := s.teamActor(ctx, credentials, organizationID, teamID)
	if err != nil {
		return nil, err
	}
	if !actor.canSee() {
		return nil, domain.ErrNotFound
	}
	return s.store.Members(ctx, teamID)
}

func (s *Service) AddMember(ctx context.Context, credentials authclient.Credentials, organizationID, teamID string, input AddMemberInput) (domain.Membership, error) {
	actor, err := s.teamActor(ctx, credentials, organizationID, teamID)
	if err != nil {
		return domain.Membership{}, err
	}
	if !actor.canSee() {
		return domain.Membership{}, domain.ErrNotFound
	}
	if !actor.canManage() {
		return domain.Membership{}, domain.ErrForbidden
	}
	if strings.TrimSpace(input.UserID) == "" {
		return domain.Membership{}, fmt.Errorf("user_id is required: %w", domain.ErrInvalid)
	}
	if !input.Role.Valid() {
		return domain.Membership{}, fmt.Errorf("role must be admin or member: %w", domain.ErrInvalid)
	}
	orgMember, err := s.organizationMember(ctx, credentials, organizationID, input.UserID)
	if err != nil {
		return domain.Membership{}, err
	}
	if _, err := s.store.Membership(ctx, teamID, orgMember.UserID); err == nil {
		return domain.Membership{}, domain.ErrConflict
	} else if !errors.Is(err, domain.ErrNotFound) {
		return domain.Membership{}, err
	}
	now := s.now().UTC()
	membership := domain.Membership{
		TeamID:         teamID,
		OrganizationID: organizationID,
		UserID:         orgMember.UserID,
		Email:          orgMember.Email,
		DisplayName:    orgMember.DisplayName,
		Role:           input.Role,
		NotifyEmail:    true,
		JoinedAt:       now,
	}
	event, err := s.memberAddedEvent(actor.team, actor.org.OrganizationName, membership, actor.org.UserID, now)
	if err != nil {
		return domain.Membership{}, err
	}
	if err := s.store.AddMember(ctx, membership, event); err != nil {
		return domain.Membership{}, err
	}
	return membership, nil
}

func (s *Service) ChangeMemberRole(ctx context.Context, credentials authclient.Credentials, organizationID, teamID, userID string, replacement domain.Role) (domain.Membership, error) {
	actor, err := s.teamActor(ctx, credentials, organizationID, teamID)
	if err != nil {
		return domain.Membership{}, err
	}
	if !actor.canSee() {
		return domain.Membership{}, domain.ErrNotFound
	}
	if !actor.canManage() {
		return domain.Membership{}, domain.ErrForbidden
	}
	if !replacement.Valid() {
		return domain.Membership{}, fmt.Errorf("role must be admin or member: %w", domain.ErrInvalid)
	}
	target, err := s.store.Membership(ctx, teamID, userID)
	if err != nil {
		return domain.Membership{}, err
	}
	if target.Role == replacement {
		return target, nil
	}
	now := s.now().UTC()
	event, err := s.event(teamseventsv1.MemberRoleChangedDetailType, teamseventsv1.MemberRoleChanged{
		TeamID:           teamID,
		TeamName:         actor.team.Name,
		OrganizationID:   organizationID,
		OrganizationName: actor.org.OrganizationName,
		UserID:           userID,
		Email:            target.Email,
		PreviousRole:     string(target.Role),
		Role:             string(replacement),
		ChangedBy:        actor.org.UserID,
		ChangedAt:        now,
	}, now)
	if err != nil {
		return domain.Membership{}, err
	}
	if err := s.store.ChangeMemberRole(ctx, teamID, userID, target.Role, replacement, event); err != nil {
		return domain.Membership{}, err
	}
	target.Role = replacement
	return target, nil
}

func (s *Service) RemoveMember(ctx context.Context, credentials authclient.Credentials, organizationID, teamID, userID string) error {
	actor, err := s.teamActor(ctx, credentials, organizationID, teamID)
	if err != nil {
		return err
	}
	if !actor.canSee() {
		return domain.ErrNotFound
	}
	target, err := s.store.Membership(ctx, teamID, userID)
	if err != nil {
		return err
	}
	reason := teamseventsv1.MemberRemovedReasonLeft
	if actor.org.UserID != userID {
		if !actor.canManage() {
			return domain.ErrForbidden
		}
		reason = teamseventsv1.MemberRemovedReasonRemoved
	} else if !domain.CanLeaveTeam(true) {
		return domain.ErrForbidden
	}
	now := s.now().UTC()
	event, err := s.memberRemovedEvent(actor.team, actor.org.OrganizationName, target, reason, actor.org.UserID, now)
	if err != nil {
		return err
	}
	return s.store.RemoveMember(ctx, teamID, userID, target.Role, event)
}

func (s *Service) SetNotification(ctx context.Context, credentials authclient.Credentials, organizationID, teamID string, notifyEmail bool) (domain.Membership, error) {
	actor, err := s.teamActor(ctx, credentials, organizationID, teamID)
	if err != nil {
		return domain.Membership{}, err
	}
	if !actor.onTeam() {
		return domain.Membership{}, domain.ErrNotFound
	}
	if actor.membership.NotifyEmail == notifyEmail {
		return *actor.membership, nil
	}
	if err := s.store.SetNotifyEmail(ctx, teamID, actor.org.UserID, notifyEmail); err != nil {
		return domain.Membership{}, err
	}
	actor.membership.NotifyEmail = notifyEmail
	return *actor.membership, nil
}

func (s *Service) OrganizationCreated(ctx context.Context, data autheventsv1.OrganizationCreated) error {
	if data.OrganizationID == "" {
		return errors.New("organization event has no organization_id")
	}
	return s.store.UpsertOrganization(ctx, data.OrganizationID, data.Name, data.CreatedAt)
}

func (s *Service) OrganizationUpdated(ctx context.Context, data autheventsv1.OrganizationUpdated) error {
	if data.OrganizationID == "" || data.Name == "" {
		return errors.New("organization event has no organization_id or name")
	}
	return s.store.UpsertOrganization(ctx, data.OrganizationID, data.Name, data.UpdatedAt)
}

func (s *Service) OrganizationDeleted(ctx context.Context, data autheventsv1.OrganizationDeleted) error {
	if data.OrganizationID == "" {
		return errors.New("organization event has no organization_id")
	}
	now := data.DeletedAt.UTC()
	if now.IsZero() {
		now = s.now().UTC()
	}
	return s.store.DeleteOrganizationTeams(ctx, data.OrganizationID, func(team domain.Team, members []domain.Membership) ([]domain.Event, error) {
		return s.teamDeletedEvents(team, data.Name, members, data.DeletedBy, teamseventsv1.MemberRemovedReasonOrganizationDeleted, now)
	})
}

func (s *Service) MemberRemoved(ctx context.Context, data autheventsv1.MemberRemoved) error {
	if data.OrganizationID == "" || data.UserID == "" {
		return errors.New("member event has no organization_id or user_id")
	}
	if data.Reason == autheventsv1.MemberRemovedReasonOrganizationDeleted {
		return nil
	}
	now := data.RemovedAt.UTC()
	if now.IsZero() {
		now = s.now().UTC()
	}
	return s.store.RemoveUserFromOrganization(ctx, data.OrganizationID, data.UserID, func(team domain.Team, membership domain.Membership) (domain.Event, error) {
		return s.memberRemovedEvent(team, data.OrganizationName, membership, teamseventsv1.MemberRemovedReasonOrganizationMembershipEnded, data.RemovedBy, now)
	})
}

type teamActor struct {
	org        domain.OrgMembership
	team       domain.Team
	membership *domain.Membership
}

func (actor teamActor) onTeam() bool {
	return actor.membership != nil
}

func (actor teamActor) teamRole() domain.Role {
	if actor.membership == nil {
		return ""
	}
	return actor.membership.Role
}

func (actor teamActor) canSee() bool {
	return domain.CanSeeTeam(actor.org.Role, actor.onTeam())
}

func (actor teamActor) canManage() bool {
	return domain.CanManageTeam(actor.org.Role, actor.teamRole())
}

func (s *Service) caller(ctx context.Context, credentials authclient.Credentials, organizationID string) (domain.OrgMembership, error) {
	if strings.TrimSpace(organizationID) == "" {
		return domain.OrgMembership{}, domain.ErrNotFound
	}
	org, err := s.directory.Membership(ctx, organizationID, credentials)
	if err != nil {
		return domain.OrgMembership{}, conceal(err)
	}
	return org, nil
}

func (s *Service) teamActor(ctx context.Context, credentials authclient.Credentials, organizationID, teamID string) (teamActor, error) {
	org, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return teamActor{}, err
	}
	if strings.TrimSpace(teamID) == "" {
		return teamActor{}, domain.ErrNotFound
	}
	team, err := s.store.Team(ctx, organizationID, teamID)
	if err != nil {
		return teamActor{}, conceal(err)
	}
	membership, err := s.store.Membership(ctx, teamID, org.UserID)
	if errors.Is(err, domain.ErrNotFound) {
		return teamActor{org: org, team: team}, nil
	}
	if err != nil {
		return teamActor{}, err
	}
	return teamActor{org: org, team: team, membership: &membership}, nil
}

func (s *Service) organizationMember(ctx context.Context, credentials authclient.Credentials, organizationID, userID string) (domain.OrgMembership, error) {
	members, err := s.directory.Members(ctx, organizationID, credentials)
	if err != nil {
		return domain.OrgMembership{}, conceal(err)
	}
	for _, member := range members {
		if member.UserID == userID {
			return member, nil
		}
	}
	return domain.OrgMembership{}, fmt.Errorf("user is not a member of the organization: %w", domain.ErrInvalid)
}

func (s *Service) memberAddedEvent(team domain.Team, organizationName string, membership domain.Membership, addedBy string, at time.Time) (domain.Event, error) {
	return s.event(teamseventsv1.MemberAddedDetailType, teamseventsv1.MemberAdded{
		TeamID:           team.ID,
		TeamName:         team.Name,
		OrganizationID:   team.OrganizationID,
		OrganizationName: organizationName,
		UserID:           membership.UserID,
		Email:            membership.Email,
		Role:             string(membership.Role),
		AddedBy:          addedBy,
		AddedAt:          at,
	}, at)
}

func (s *Service) memberRemovedEvent(team domain.Team, organizationName string, membership domain.Membership, reason, removedBy string, at time.Time) (domain.Event, error) {
	return s.event(teamseventsv1.MemberRemovedDetailType, teamseventsv1.MemberRemoved{
		TeamID:           team.ID,
		TeamName:         team.Name,
		OrganizationID:   team.OrganizationID,
		OrganizationName: organizationName,
		UserID:           membership.UserID,
		Email:            membership.Email,
		PreviousRole:     string(membership.Role),
		Reason:           reason,
		RemovedBy:        removedBy,
		RemovedAt:        at,
	}, at)
}

func (s *Service) teamDeletedEvents(team domain.Team, organizationName string, members []domain.Membership, deletedBy, memberReason string, at time.Time) ([]domain.Event, error) {
	deleted, err := s.event(teamseventsv1.TeamDeletedDetailType, teamseventsv1.TeamDeleted{
		TeamID:           team.ID,
		OrganizationID:   team.OrganizationID,
		OrganizationName: organizationName,
		Name:             team.Name,
		DeletedBy:        deletedBy,
		DeletedAt:        at,
	}, at)
	if err != nil {
		return nil, err
	}
	events := []domain.Event{deleted}
	for _, member := range members {
		event, err := s.memberRemovedEvent(team, organizationName, member, memberReason, deletedBy, at)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func (s *Service) event(detailType string, data any, occurredAt time.Time) (domain.Event, error) {
	id, err := randomID("evt_", 16)
	if err != nil {
		return domain.Event{}, fmt.Errorf("create event id: %w", err)
	}
	detail, err := json.Marshal(data)
	if err != nil {
		return domain.Event{}, fmt.Errorf("encode %s: %w", detailType, err)
	}
	return domain.Event{ID: id, DetailType: detailType, OccurredAt: occurredAt, Data: detail}, nil
}

func conceal(err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrNotFound
	}
	return err
}

func teamName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || len(name) > 120 {
		return "", fmt.Errorf("team name must be between 1 and 120 characters: %w", domain.ErrInvalid)
	}
	return name, nil
}

func randomID(prefix string, size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value), nil
}
