package application

import (
	"context"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/teams/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
)

type fakeStore struct {
	organizations map[string]domain.Organization
	teams         map[string]domain.Team
	memberships   map[string]domain.Membership
	events        []domain.Event
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		organizations: map[string]domain.Organization{},
		teams:         map[string]domain.Team{},
		memberships:   map[string]domain.Membership{},
	}
}

func membershipKey(teamID, userID string) string {
	return teamID + "/" + userID
}

func (store *fakeStore) UpsertOrganization(_ context.Context, organizationID, name string, _ time.Time) error {
	organization := store.organizations[organizationID]
	organization.ID = organizationID
	if !organization.Deleted {
		organization.Name = name
	}
	store.organizations[organizationID] = organization
	return nil
}

func (store *fakeStore) Organization(_ context.Context, organizationID string) (domain.Organization, error) {
	organization, ok := store.organizations[organizationID]
	if !ok {
		return domain.Organization{}, domain.ErrNotFound
	}
	return organization, nil
}

func (store *fakeStore) CreateTeam(_ context.Context, organizationName string, team domain.Team, creator domain.Membership, events []domain.Event) error {
	if organization, ok := store.organizations[team.OrganizationID]; ok && organization.Deleted {
		return domain.ErrNotFound
	}
	if _, ok := store.organizations[team.OrganizationID]; !ok {
		store.organizations[team.OrganizationID] = domain.Organization{ID: team.OrganizationID, Name: organizationName}
	}
	for _, existing := range store.teams {
		if existing.OrganizationID == team.OrganizationID && strings.EqualFold(existing.Name, team.Name) {
			return domain.ErrConflict
		}
	}
	store.teams[team.ID] = team
	store.memberships[membershipKey(creator.TeamID, creator.UserID)] = creator
	store.events = append(store.events, events...)
	return nil
}

func (store *fakeStore) UpdateTeam(_ context.Context, team domain.Team, event domain.Event) error {
	if _, ok := store.teams[team.ID]; !ok {
		return domain.ErrNotFound
	}
	for id, existing := range store.teams {
		if id != team.ID && existing.OrganizationID == team.OrganizationID && strings.EqualFold(existing.Name, team.Name) {
			return domain.ErrConflict
		}
	}
	store.teams[team.ID] = team
	store.events = append(store.events, event)
	return nil
}

func (store *fakeStore) DeleteTeam(_ context.Context, organizationID, teamID string, events []domain.Event) error {
	team, ok := store.teams[teamID]
	if !ok || team.OrganizationID != organizationID {
		return domain.ErrNotFound
	}
	delete(store.teams, teamID)
	for key, membership := range store.memberships {
		if membership.TeamID == teamID {
			delete(store.memberships, key)
		}
	}
	store.events = append(store.events, events...)
	return nil
}

func (store *fakeStore) Team(_ context.Context, organizationID, teamID string) (domain.Team, error) {
	team, ok := store.teams[teamID]
	if !ok || team.OrganizationID != organizationID {
		return domain.Team{}, domain.ErrNotFound
	}
	return team, nil
}

func (store *fakeStore) Teams(_ context.Context, organizationID string) ([]domain.Team, error) {
	var teams []domain.Team
	for _, team := range store.teams {
		if team.OrganizationID == organizationID {
			teams = append(teams, team)
		}
	}
	return teams, nil
}

func (store *fakeStore) TeamsForUser(_ context.Context, organizationID, userID string) ([]domain.Team, error) {
	var teams []domain.Team
	for _, membership := range store.memberships {
		if membership.OrganizationID != organizationID || membership.UserID != userID {
			continue
		}
		if team, ok := store.teams[membership.TeamID]; ok {
			teams = append(teams, team)
		}
	}
	return teams, nil
}

func (store *fakeStore) Membership(_ context.Context, teamID, userID string) (domain.Membership, error) {
	membership, ok := store.memberships[membershipKey(teamID, userID)]
	if !ok {
		return domain.Membership{}, domain.ErrNotFound
	}
	return membership, nil
}

func (store *fakeStore) Members(_ context.Context, teamID string) ([]domain.Membership, error) {
	var members []domain.Membership
	for _, membership := range store.memberships {
		if membership.TeamID == teamID {
			members = append(members, membership)
		}
	}
	return members, nil
}

func (store *fakeStore) AddMember(_ context.Context, membership domain.Membership, event domain.Event) error {
	if _, ok := store.teams[membership.TeamID]; !ok {
		return domain.ErrNotFound
	}
	if _, ok := store.memberships[membershipKey(membership.TeamID, membership.UserID)]; ok {
		return domain.ErrConflict
	}
	store.memberships[membershipKey(membership.TeamID, membership.UserID)] = membership
	store.events = append(store.events, event)
	return nil
}

func (store *fakeStore) ChangeMemberRole(_ context.Context, teamID, userID string, expected, replacement domain.Role, event domain.Event) error {
	membership, ok := store.memberships[membershipKey(teamID, userID)]
	if !ok || membership.Role != expected {
		return domain.ErrConflict
	}
	if err := store.protectLastAdmin(teamID, expected, replacement); err != nil {
		return err
	}
	membership.Role = replacement
	store.memberships[membershipKey(teamID, userID)] = membership
	store.events = append(store.events, event)
	return nil
}

func (store *fakeStore) RemoveMember(_ context.Context, teamID, userID string, expected domain.Role, event domain.Event) error {
	membership, ok := store.memberships[membershipKey(teamID, userID)]
	if !ok || membership.Role != expected {
		return domain.ErrConflict
	}
	if err := store.protectLastAdmin(teamID, expected, ""); err != nil {
		return err
	}
	delete(store.memberships, membershipKey(teamID, userID))
	store.events = append(store.events, event)
	return nil
}

func (store *fakeStore) RemoveUserFromOrganization(_ context.Context, organizationID, userID string, removed func(domain.Team, domain.Membership) (domain.Event, error)) error {
	for key, membership := range store.memberships {
		if membership.OrganizationID != organizationID || membership.UserID != userID {
			continue
		}
		team := store.teams[membership.TeamID]
		event, err := removed(team, membership)
		if err != nil {
			return err
		}
		delete(store.memberships, key)
		store.events = append(store.events, event)
	}
	return nil
}

func (store *fakeStore) DeleteOrganizationTeams(_ context.Context, organizationID string, emit func(domain.Team, []domain.Membership) ([]domain.Event, error)) error {
	organization := store.organizations[organizationID]
	if organization.Deleted {
		return nil
	}
	for _, team := range store.teams {
		if team.OrganizationID != organizationID {
			continue
		}
		members, _ := store.Members(context.Background(), team.ID)
		events, err := emit(team, members)
		if err != nil {
			return err
		}
		store.events = append(store.events, events...)
		delete(store.teams, team.ID)
		for key, membership := range store.memberships {
			if membership.TeamID == team.ID {
				delete(store.memberships, key)
			}
		}
	}
	organization.ID = organizationID
	organization.Deleted = true
	store.organizations[organizationID] = organization
	return nil
}

func (store *fakeStore) protectLastAdmin(teamID string, current, next domain.Role) error {
	if current != domain.RoleAdmin || next == domain.RoleAdmin {
		return nil
	}
	admins := 0
	for _, membership := range store.memberships {
		if membership.TeamID == teamID && membership.Role == domain.RoleAdmin {
			admins++
		}
	}
	if admins <= 1 {
		return domain.ErrConflict
	}
	return nil
}

func (store *fakeStore) SetNotifyEmail(_ context.Context, teamID, userID string, notifyEmail bool) error {
	membership, ok := store.memberships[membershipKey(teamID, userID)]
	if !ok {
		return domain.ErrNotFound
	}
	membership.NotifyEmail = notifyEmail
	store.memberships[membershipKey(teamID, userID)] = membership
	return nil
}

func (store *fakeStore) RecordEvents(_ context.Context, events []domain.Event) error {
	store.events = append(store.events, events...)
	return nil
}

func (store *fakeStore) detailTypes() []string {
	types := make([]string, 0, len(store.events))
	for _, event := range store.events {
		types = append(types, event.DetailType)
	}
	return types
}

type fakeDirectory struct {
	callers map[string]domain.OrgMembership
	members []domain.OrgMembership
}

func (directory *fakeDirectory) Membership(_ context.Context, organizationID string, credentials authclient.Credentials) (domain.OrgMembership, error) {
	key := credentials.Authorization + credentials.AccessCookie
	caller, ok := directory.callers[key]
	if !ok {
		return domain.OrgMembership{}, domain.ErrUnauthorized
	}
	if caller.OrganizationID != organizationID {
		return domain.OrgMembership{}, domain.ErrNotFound
	}
	return caller, nil
}

func (directory *fakeDirectory) Members(_ context.Context, organizationID string, _ authclient.Credentials) ([]domain.OrgMembership, error) {
	var members []domain.OrgMembership
	for _, member := range directory.members {
		if member.OrganizationID == organizationID {
			members = append(members, member)
		}
	}
	return members, nil
}

func creds(userID string) authclient.Credentials {
	return authclient.Credentials{Authorization: "Bearer " + userID}
}
