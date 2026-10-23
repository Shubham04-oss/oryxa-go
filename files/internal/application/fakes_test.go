package application

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/files/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
)

// agentTokenPrefix marks a credential as auth would answer for a token it
// minted for an agent acting for the named member, rather than the member's
// own session.
const agentTokenPrefix = "agent:"

// agentCreds is a token auth minted for an agent acting for userID. The fake
// directory answers it as that member, marked as an agent.
func agentCreds(userID string) authclient.Credentials {
	return authclient.Credentials{Authorization: "Bearer " + agentTokenPrefix + userID}
}

// grantedCreds is a specialist's token whose grant names exactly these file
// scopes; none at all is a grant of nothing.
func grantedCreds(userID string, files ...string) authclient.Credentials {
	return authclient.Credentials{Authorization: "Bearer " + agentTokenPrefix + userID + grantMark + strings.Join(files, ",")}
}

const grantMark = ";files="

// roleCreds is a token auth minted for an agent of the named role (primary,
// organization or specialist) acting for userID, with no files grant.
func roleCreds(userID, role string) authclient.Credentials {
	return authclient.Credentials{Authorization: "Bearer " + agentTokenPrefix + userID + roleMark + role}
}

const roleMark = ";role="

type fakeDirectory struct{}

func (fakeDirectory) Membership(_ context.Context, organizationID string, credentials authclient.Credentials) (domain.OrgMembership, error) {
	token := credentials.Authorization
	if len(token) > 7 {
		token = token[7:]
	}
	if credentials.AccessCookie != "" && token == "" {
		token = credentials.AccessCookie
	}
	token, agent := strings.CutPrefix(token, agentTokenPrefix)
	token, granted, limited := strings.Cut(token, grantMark)
	token, role, _ := strings.Cut(token, roleMark)
	people := map[string]domain.OrgMembership{
		"user-owner":    {OrganizationID: "org_1", OrganizationName: "Acme", UserID: "user-owner", Email: "owner@example.com", Role: domain.OrgRoleOwner},
		"user-admin":    {OrganizationID: "org_1", OrganizationName: "Acme", UserID: "user-admin", Email: "admin@example.com", Role: domain.OrgRoleAdmin},
		"user-member":   {OrganizationID: "org_1", OrganizationName: "Acme", UserID: "user-member", Email: "member@example.com", Role: domain.OrgRoleMember},
		"user-outsider": {OrganizationID: "org_1", OrganizationName: "Acme", UserID: "user-outsider", Email: "outsider@example.com", Role: domain.OrgRoleMember},
	}
	membership, ok := people[token]
	if !ok || organizationID != "org_1" {
		return domain.OrgMembership{}, domain.ErrNotFound
	}
	membership.Agent = agent
	if agent {
		membership.AgentRole = "specialist"
		if role != "" {
			membership.AgentRole = role
		}
		if limited {
			membership.AgentFiles = []string{}
			if granted != "" {
				membership.AgentFiles = strings.Split(granted, ",")
			}
		}
	}
	return membership, nil
}

type fakeTeams struct {
	mu     sync.Mutex
	access map[string]domain.TeamAccess
	calls  int
}

func newFakeTeams() *fakeTeams {
	return &fakeTeams{access: map[string]domain.TeamAccess{
		"user-owner/team_eng":     {TeamID: "team_eng", CanManage: true, Member: true},
		"user-admin/team_eng":     {TeamID: "team_eng", CanManage: true, Member: false},
		"user-member/team_eng":    {TeamID: "team_eng", CanManage: false, Member: true},
		"user-owner/team_design":  {TeamID: "team_design", CanManage: true, Member: false},
		"user-admin/team_design":  {TeamID: "team_design", CanManage: true, Member: true},
		"user-member/team_design": {},
		// Only org admins see team_ops, through teams' admin view; nobody is on it.
		"user-owner/team_ops":       {TeamID: "team_ops", CanManage: true, Member: false},
		"user-admin/team_ops":       {TeamID: "team_ops", CanManage: true, Member: false},
		"user-outsider/team_design": {TeamID: "team_design", CanManage: false, Member: true},
	}}
}

func (fake *fakeTeams) Team(_ context.Context, organizationID, teamID string, credentials authclient.Credentials) (domain.TeamAccess, error) {
	if organizationID != "org_1" {
		return domain.TeamAccess{}, domain.ErrNotFound
	}
	token := credentials.Authorization
	if len(token) > 7 {
		token = token[7:]
	}
	// Team membership is about which member is calling, agent or not: the
	// real teams service resolves an agent's token to its member's own
	// identity just as auth does, and decides nothing from the agent flag.
	token = strings.TrimPrefix(token, agentTokenPrefix)
	token, _, _ = strings.Cut(token, ";")
	fake.mu.Lock()
	fake.calls++
	fake.mu.Unlock()
	access, ok := fake.access[token+"/"+teamID]
	if !ok || access.TeamID == "" {
		return domain.TeamAccess{}, domain.ErrNotFound
	}
	return access, nil
}

type fakeStore struct {
	mu      sync.Mutex
	orgs    map[string]domain.Organization
	objects map[string]domain.Object
	events  []domain.Event
}

func newFakeStore() *fakeStore {
	return &fakeStore{orgs: map[string]domain.Organization{}, objects: map[string]domain.Object{}}
}

func (store *fakeStore) key(organizationID, fileID string) string {
	return organizationID + "/" + fileID
}

func (store *fakeStore) UpsertOrganization(_ context.Context, organizationID, name string, _ time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.orgs[organizationID] = domain.Organization{ID: organizationID, Name: name}
	return nil
}

func (store *fakeStore) CreateObject(_ context.Context, organizationName string, object domain.Object, event domain.Event) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.orgs[object.OrganizationID] = domain.Organization{ID: object.OrganizationID, Name: organizationName}
	store.objects[store.key(object.OrganizationID, object.ID)] = object
	store.events = append(store.events, event)
	return nil
}

func (store *fakeStore) Object(_ context.Context, organizationID, fileID string, now time.Time) (domain.Object, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	object, ok := store.objects[store.key(organizationID, fileID)]
	if !ok || !object.ExpiresAt.After(now) {
		return domain.Object{}, domain.ErrNotFound
	}
	return object, nil
}

func (store *fakeStore) Objects(_ context.Context, organizationID string, now time.Time, query domain.ObjectQuery) ([]domain.Object, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var result []domain.Object
	for _, object := range store.objects {
		if object.OrganizationID != organizationID || !object.ExpiresAt.After(now) {
			continue
		}
		if query.Purpose != "" && object.Purpose != query.Purpose {
			continue
		}
		if !query.CreatedFrom.IsZero() && object.CreatedAt.Before(query.CreatedFrom) {
			continue
		}
		if !query.CreatedTo.IsZero() && !object.CreatedAt.Before(query.CreatedTo) {
			continue
		}
		if !matchesQuery(object, query) {
			continue
		}
		result = append(result, object)
	}
	return result, nil
}

func matchesQuery(object domain.Object, query domain.ObjectQuery) bool {
	switch query.Scope {
	case domain.ScopeTeam:
		return object.Scope == domain.ScopeTeam && object.TeamID == query.TeamID
	case domain.ScopeMember:
		if object.Scope != domain.ScopeMember {
			return false
		}
		return query.Admin || object.MemberID == query.MemberID
	case domain.ScopeOrganization:
		if object.Scope != domain.ScopeOrganization {
			return false
		}
		return query.Admin || object.EffectiveAccess() == domain.AccessMembers
	default:
		switch object.Scope {
		case domain.ScopeMember:
			return query.Admin || object.MemberID == query.MemberID
		case domain.ScopeOrganization:
			return query.Admin || object.EffectiveAccess() == domain.AccessMembers
		default:
			return false
		}
	}
}

func (store *fakeStore) DeleteObject(_ context.Context, organizationID, fileID string, event domain.Event) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := store.key(organizationID, fileID)
	if _, ok := store.objects[key]; !ok {
		return domain.ErrNotFound
	}
	delete(store.objects, key)
	store.events = append(store.events, event)
	return nil
}

func (store *fakeStore) DeleteMemberScope(_ context.Context, organizationID, memberID string, emit func(domain.Object) (domain.Event, error)) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for key, object := range store.objects {
		if object.OrganizationID != organizationID || object.Scope != domain.ScopeMember || object.MemberID != memberID {
			continue
		}
		event, err := emit(object)
		if err != nil {
			return err
		}
		store.events = append(store.events, event)
		delete(store.objects, key)
	}
	return nil
}

func (store *fakeStore) DeleteTeamFiles(_ context.Context, organizationID, teamID string, emit func(domain.Object) (domain.Event, error)) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for key, object := range store.objects {
		if object.OrganizationID != organizationID || object.Scope != domain.ScopeTeam || object.TeamID != teamID {
			continue
		}
		event, err := emit(object)
		if err != nil {
			return err
		}
		store.events = append(store.events, event)
		delete(store.objects, key)
	}
	return nil
}

func (store *fakeStore) DeleteOrganizationFiles(_ context.Context, organizationID string, emit func(domain.Object) (domain.Event, error)) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for key, object := range store.objects {
		if object.OrganizationID != organizationID {
			continue
		}
		event, err := emit(object)
		if err != nil {
			return err
		}
		store.events = append(store.events, event)
		delete(store.objects, key)
	}
	org := store.orgs[organizationID]
	org.Deleted = true
	store.orgs[organizationID] = org
	return nil
}
