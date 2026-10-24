package application

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/access"

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
	MembersUnder(context.Context, string, string) ([]domain.Membership, error)
	OrganizationMemberships(context.Context, string) ([]domain.OrganizationMembership, error)
	AddMember(context.Context, domain.Membership, domain.Event) error
	ChangeMemberRole(context.Context, string, string, domain.Role, domain.Role, domain.Event) error
	RemoveMember(context.Context, string, string, domain.Role, domain.Event) error
	RemoveUserFromOrganization(context.Context, string, string, func(domain.Team, domain.Membership) (domain.Event, error)) error
	DeleteOrganizationTeams(context.Context, string, func(domain.Team, []domain.Membership) ([]domain.Event, error)) error
	RecordEvents(context.Context, []domain.Event) error
	SetNotifyEmail(context.Context, string, string, bool) error
	SearchTeams(context.Context, string, string, int, []string) ([]domain.Team, error)
	TeamDescendants(context.Context, string, string) ([]domain.Team, error)
	SetTeamEmbedding(context.Context, string, string, []float32) error
	TeamsNearest(context.Context, string, []float32, int, []string) ([]domain.Team, error)
}

type Directory interface {
	Membership(context.Context, string, authclient.Credentials) (domain.OrgMembership, error)
	Members(context.Context, string, authclient.Credentials) ([]domain.OrgMembership, error)
}

type CreateTeamInput struct {
	Name        string `json:"name"`
	ParentID    string `json:"parent_id,omitempty"`
	SkipCreator bool   `json:"skip_creator,omitempty"`
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

type MemberTeamRef struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Path       string      `json:"path,omitempty"`
	MemberPath string      `json:"member_path,omitempty"`
	Role       domain.Role `json:"role"`
}

type MemberSearchHit struct {
	UserID      string          `json:"user_id"`
	Email       string          `json:"email"`
	DisplayName string          `json:"display_name,omitempty"`
	OrgRole     domain.OrgRole  `json:"org_role"`
	Path        string          `json:"path,omitempty"`
	Teams       []MemberTeamRef `json:"teams"`
}

// OrganizationDirectory is the caller-visible organization tree. Teams and
// team references are visibility-filtered; people come from auth's current
// organization roster, which remains the source of membership identity.
type OrganizationDirectory struct {
	Organization DirectoryOrganization `json:"organization"`
	Teams        []TeamSummary         `json:"teams"`
	Members      []MemberSearchHit     `json:"members"`
}

type DirectoryOrganization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Service struct {
	store     Store
	directory Directory
	shadow    access.Comparer
	// engine, when set, decides who sees and manages a team from SpiceDB
	// (ACCESS_ENGINE_TEAMS=spicedb), and relationships writes teams and
	// their members to it as they change (engine.go).
	engine        access.Decider
	relationships Relationships
	logger        *slog.Logger
	now           func() time.Time
	// toolCalls bounds the ping-bearing tools per member per organization.
	toolCalls *toolRateLimiter
}

func New(store Store, directory Directory, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	service := &Service{store: store, directory: directory, logger: logger, now: time.Now}
	// The limiter reads the service's clock through the field rather than
	// capturing the function value, so a test that moves the clock moves it.
	service.toolCalls = newToolRateLimiter(func() time.Time { return service.now() })
	return service
}

// WithShadow sets where the service's decisions are compared with SpiceDB.
// Without one it compares nothing.
func (s *Service) WithShadow(shadow access.Comparer) *Service {
	s.shadow = shadow
	return s
}

func (s *Service) CreateTeam(ctx context.Context, credentials authclient.Credentials, organizationID string, input CreateTeamInput) (domain.Team, error) {
	org, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.Team{}, err
	}
	if input.SkipCreator && !org.ActsAsAdmin() {
		return domain.Team{}, domain.ErrForbidden
	}
	name, err := teamName(input.Name)
	if err != nil {
		return domain.Team{}, err
	}
	parentPath, parentID := "", strings.TrimSpace(input.ParentID)
	if parentID != "" {
		parent, err := s.teamActor(ctx, credentials, organizationID, parentID)
		if err != nil {
			return domain.Team{}, err
		}
		if !parent.canSee() {
			return domain.Team{}, domain.ErrNotFound
		}
		if !parent.canManage() {
			return domain.Team{}, domain.ErrForbidden
		}
		parentPath = parent.team.Path
	} else if !domain.CanCreateTeam(org) {
		return domain.Team{}, domain.ErrForbidden
	}
	teamID, err := randomID("team_", 12)
	if err != nil {
		return domain.Team{}, err
	}
	now := s.now().UTC()
	path := domain.TeamPath(organizationID, teamID)
	if parentPath != "" {
		path = domain.ChildPath(parentPath, teamID)
	}
	team := domain.Team{
		ID:             teamID,
		OrganizationID: organizationID,
		Name:           name,
		Path:           path,
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
		Path:           domain.MemberPath(path, org.UserID),
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
	events := []domain.Event{created}
	if input.SkipCreator {
		creator = domain.Membership{}
	} else {
		added, err := s.memberAddedEvent(team, org.OrganizationName, creator, org.UserID, now)
		if err != nil {
			return domain.Team{}, err
		}
		events = append(events, added)
	}
	if err := s.store.CreateTeam(ctx, org.OrganizationName, team, creator, events); err != nil {
		return domain.Team{}, err
	}
	if s.relationships != nil {
		s.wrote(ctx, "team", s.relationships.TeamCreated(ctx, team, parentID))
		if creator.UserID != "" {
			s.wrote(ctx, "team creator", s.relationships.MemberSet(ctx, organizationID, team.ID, creator.UserID, creator.Role))
		}
	}
	return team, nil
}

func (s *Service) Teams(ctx context.Context, credentials authclient.Credentials, organizationID string) ([]TeamSummary, error) {
	teams, org, err := s.visibleTeams(ctx, credentials, organizationID)
	if err != nil {
		return nil, err
	}
	return s.summarizeTeams(ctx, org, teams)
}

func (s *Service) SearchTeams(ctx context.Context, credentials authclient.Credentials, organizationID, query string, limit int) ([]TeamSummary, error) {
	query, err := domain.SearchQuery(query)
	if err != nil {
		return nil, err
	}
	org, teamIDs, err := s.searchScope(ctx, credentials, organizationID)
	if err != nil {
		return nil, err
	}
	teams, err := s.store.SearchTeams(ctx, organizationID, query, limit, teamIDs)
	if err != nil {
		return nil, err
	}
	return s.summarizeTeams(ctx, org, teams)
}

func (s *Service) TeamDescendants(ctx context.Context, credentials authclient.Credentials, organizationID, teamID string) ([]TeamSummary, error) {
	actor, err := s.teamActor(ctx, credentials, organizationID, teamID)
	if err != nil {
		return nil, err
	}
	if !actor.canSee() {
		return nil, domain.ErrNotFound
	}
	subtree, err := s.store.TeamDescendants(ctx, organizationID, actor.team.Path)
	if err != nil {
		return nil, err
	}
	visible := make([]domain.Team, 0, len(subtree))
	if actor.org.ActsAsAdmin() {
		visible = subtree
	} else {
		for _, team := range subtree {
			_, membershipErr := s.store.Membership(ctx, team.ID, actor.org.UserID)
			onTeam := membershipErr == nil
			if membershipErr != nil && !errors.Is(membershipErr, domain.ErrNotFound) {
				return nil, membershipErr
			}
			if domain.CanSeeTeam(actor.org, onTeam) {
				visible = append(visible, team)
			}
		}
	}
	return s.summarizeTeams(ctx, actor.org, visible)
}

func (s *Service) SetTeamEmbedding(ctx context.Context, credentials authclient.Credentials, organizationID, teamID string, values []float32) error {
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
	return s.store.SetTeamEmbedding(ctx, organizationID, teamID, values)
}

func (s *Service) SearchMembers(ctx context.Context, credentials authclient.Credentials, organizationID, query string, limit int) ([]MemberSearchHit, error) {
	query, err := domain.SearchQuery(query)
	if err != nil {
		return nil, err
	}
	limit = domain.ClampSearchLimit(limit)
	directory, err := s.OrganizationDirectory(ctx, credentials, organizationID)
	if err != nil {
		return nil, err
	}
	hits := make([]MemberSearchHit, 0)
	for _, person := range directory.Members {
		if !domain.FuzzyMatch(query, person.Email, person.DisplayName, person.UserID) {
			continue
		}
		hits = append(hits, person)
	}
	slices.SortFunc(hits, func(a, b MemberSearchHit) int {
		as := max(domain.Similarity(query, a.Email), domain.Similarity(query, a.DisplayName))
		bs := max(domain.Similarity(query, b.Email), domain.Similarity(query, b.DisplayName))
		if as != bs {
			if as > bs {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Email), strings.ToLower(b.Email))
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// OrganizationDirectory returns every current organization member and the
// team tree the caller may see. A non-admin still sees people, as member
// search already permits, but team references outside their teams are absent.
func (s *Service) OrganizationDirectory(ctx context.Context, credentials authclient.Credentials, organizationID string) (OrganizationDirectory, error) {
	visible, org, err := s.visibleTeams(ctx, credentials, organizationID)
	if err != nil {
		return OrganizationDirectory{}, err
	}
	teams, err := s.summarizeTeams(ctx, org, visible)
	if err != nil {
		return OrganizationDirectory{}, err
	}
	people, err := s.directory.Members(ctx, organizationID, credentials)
	if err != nil {
		return OrganizationDirectory{}, conceal(err)
	}
	memberships, err := s.store.OrganizationMemberships(ctx, organizationID)
	if err != nil {
		return OrganizationDirectory{}, err
	}
	visibleTeamIDs := make(map[string]struct{}, len(visible))
	for _, team := range visible {
		visibleTeamIDs[team.ID] = struct{}{}
	}
	teamsByUser := map[string][]MemberTeamRef{}
	for _, membership := range memberships {
		if _, ok := visibleTeamIDs[membership.TeamID]; !ok {
			continue
		}
		teamsByUser[membership.UserID] = append(teamsByUser[membership.UserID], MemberTeamRef{
			ID: membership.TeamID, Name: membership.TeamName, Path: membership.Path,
			MemberPath: membership.MemberPath, Role: membership.Role,
		})
	}
	members := make([]MemberSearchHit, 0, len(people))
	for _, person := range people {
		teamRefs := teamsByUser[person.UserID]
		if teamRefs == nil {
			teamRefs = []MemberTeamRef{}
		}
		slices.SortFunc(teamRefs, func(a, b MemberTeamRef) int { return strings.Compare(a.Path, b.Path) })
		members = append(members, MemberSearchHit{
			UserID: person.UserID, Email: person.Email, DisplayName: person.DisplayName,
			OrgRole: person.Role, Path: domain.UserPath(organizationID, person.UserID), Teams: teamRefs,
		})
	}
	slices.SortFunc(members, func(a, b MemberSearchHit) int {
		if n := strings.Compare(strings.ToLower(a.DisplayName), strings.ToLower(b.DisplayName)); n != 0 {
			return n
		}
		return strings.Compare(strings.ToLower(a.Email), strings.ToLower(b.Email))
	})
	return OrganizationDirectory{
		Organization: DirectoryOrganization{ID: organizationID, Name: org.OrganizationName},
		Teams:        nonNilTeams(teams), Members: members,
	}, nil
}

func nonNilTeams(teams []TeamSummary) []TeamSummary {
	if teams == nil {
		return []TeamSummary{}
	}
	return teams
}

func (s *Service) TeamsNearest(ctx context.Context, credentials authclient.Credentials, organizationID string, values []float32, limit int) ([]TeamSummary, error) {
	org, teamIDs, err := s.searchScope(ctx, credentials, organizationID)
	if err != nil {
		return nil, err
	}
	teams, err := s.store.TeamsNearest(ctx, organizationID, values, limit, teamIDs)
	if err != nil {
		return nil, err
	}
	return s.summarizeTeams(ctx, org, teams)
}

func (s *Service) Team(ctx context.Context, credentials authclient.Credentials, organizationID, teamID string) (TeamDetail, error) {
	actor, err := s.teamActor(ctx, credentials, organizationID, teamID)
	if err != nil {
		return TeamDetail{}, err
	}
	if !actor.canSee() {
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
	subtree, err := s.store.TeamDescendants(ctx, organizationID, actor.team.Path)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	var events []domain.Event
	for i := len(subtree) - 1; i >= 0; i-- {
		team := subtree[i]
		members, err := s.store.Members(ctx, team.ID)
		if err != nil {
			return err
		}
		emitted, err := s.teamDeletedEvents(team, actor.org.OrganizationName, members, actor.org.UserID, teamseventsv1.MemberRemovedReasonTeamDeleted, now)
		if err != nil {
			return err
		}
		events = append(events, emitted...)
	}
	if err := s.store.DeleteTeam(ctx, organizationID, teamID, events); err != nil {
		return err
	}
	if s.relationships != nil {
		ids := make([]string, len(subtree))
		for i, team := range subtree {
			ids[i] = team.ID
		}
		s.wrote(ctx, "deleted teams", s.relationships.TeamsDeleted(ctx, organizationID, ids))
	}
	return nil
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

func (s *Service) MemberDescendants(ctx context.Context, credentials authclient.Credentials, organizationID, teamID string) ([]domain.Membership, error) {
	actor, err := s.teamActor(ctx, credentials, organizationID, teamID)
	if err != nil {
		return nil, err
	}
	if !actor.canSee() {
		return nil, domain.ErrNotFound
	}
	members, err := s.store.MembersUnder(ctx, organizationID, actor.team.Path)
	if err != nil {
		return nil, err
	}
	if actor.org.ActsAsAdmin() {
		return members, nil
	}
	teams, err := s.store.TeamsForUser(ctx, organizationID, actor.org.UserID)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]struct{}, len(teams))
	for _, team := range teams {
		allowed[team.ID] = struct{}{}
	}
	visible := make([]domain.Membership, 0, len(members))
	for _, member := range members {
		if _, ok := allowed[member.TeamID]; ok {
			visible = append(visible, member)
		}
	}
	return visible, nil
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
		Path:           domain.MemberPath(actor.team.Path, orgMember.UserID),
		JoinedAt:       now,
	}
	event, err := s.memberAddedEvent(actor.team, actor.org.OrganizationName, membership, actor.org.UserID, now)
	if err != nil {
		return domain.Membership{}, err
	}
	if err := s.store.AddMember(ctx, membership, event); err != nil {
		return domain.Membership{}, err
	}
	if s.relationships != nil {
		s.wrote(ctx, "team member", s.relationships.MemberSet(ctx, organizationID, teamID, membership.UserID, membership.Role))
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
	if s.relationships != nil {
		s.wrote(ctx, "team member's role", s.relationships.MemberSet(ctx, organizationID, teamID, userID, replacement))
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
	if err := s.store.RemoveMember(ctx, teamID, userID, target.Role, event); err != nil {
		return err
	}
	if s.relationships != nil {
		s.wrote(ctx, "removed team member", s.relationships.MemberRemoved(ctx, organizationID, teamID, userID))
	}
	return nil
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
	// see and manage are decided once, in teamActor, by the code and, with
	// an engine, narrowed by SpiceDB.
	see, manage bool
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
	return actor.see
}

func (actor teamActor) canManage() bool {
	return actor.manage
}

func (s *Service) summarizeTeams(ctx context.Context, org domain.OrgMembership, teams []domain.Team) ([]TeamSummary, error) {
	// The query already kept the caller to teams the code lets them see; the
	// engine may only narrow that. Seeing each team and managing it are
	// asked together, in one round trip for the whole list.
	memberships := make([]*domain.Membership, len(teams))
	asks, allowed := make([]access.Ask, 2*len(teams)), make([]bool, 2*len(teams))
	for i, team := range teams {
		membership, err := s.store.Membership(ctx, team.ID, org.UserID)
		switch {
		case err == nil:
			memberships[i] = &membership
		case !errors.Is(err, domain.ErrNotFound):
			return nil, err
		}
		asks[i], allowed[i] = teamAsk(org, team, access.ActionRead), true
		asks[len(teams)+i] = teamAsk(org, team, access.ActionWrite)
		allowed[len(teams)+i] = domain.CanManageTeam(org, membership.Role)
	}
	allowed, err := s.decideMany(ctx, asks, allowed)
	if err != nil {
		return nil, err
	}
	summaries := make([]TeamSummary, 0, len(teams))
	for i, team := range teams {
		if !allowed[i] {
			continue
		}
		summary := TeamSummary{Team: team, CanManage: allowed[len(teams)+i]}
		if memberships[i] != nil {
			summary.Role = memberships[i].Role
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

func (s *Service) searchScope(ctx context.Context, credentials authclient.Credentials, organizationID string) (domain.OrgMembership, []string, error) {
	org, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.OrgMembership{}, nil, err
	}
	if org.ActsAsAdmin() {
		return org, nil, nil
	}
	teams, err := s.store.TeamsForUser(ctx, organizationID, org.UserID)
	if err != nil {
		return domain.OrgMembership{}, nil, err
	}
	ids := make([]string, 0, len(teams))
	for _, team := range teams {
		ids = append(ids, team.ID)
	}
	return org, ids, nil
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
	actor := teamActor{org: org, team: team}
	switch {
	case err == nil:
		actor.membership = &membership
	case !errors.Is(err, domain.ErrNotFound):
		return teamActor{}, err
	}
	// Every route on one team decides from this actor, so deciding here
	// covers them all.
	if actor.see, err = s.decide(ctx, org, team, access.ActionRead, domain.CanSeeTeam(org, actor.onTeam())); err != nil {
		return teamActor{}, err
	}
	if actor.manage, err = s.decide(ctx, org, team, access.ActionWrite, domain.CanManageTeam(org, actor.teamRole())); err != nil {
		return teamActor{}, err
	}
	return actor, nil
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
	return domain.CleanTeamName(raw)
}

func randomID(prefix string, size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value), nil
}
