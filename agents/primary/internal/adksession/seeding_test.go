package adksession

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/adk/v2/session"
)

type fakeMembers struct {
	member Member
	err    error
}

func (f fakeMembers) Member(context.Context, string) (Member, error) {
	return f.member, f.err
}

type fakeEvents struct {
	created []string
	deleted []string
	err     error
}

func (f *fakeEvents) RecordSessionCreated(_ context.Context, appName, sessionID, userID, organizationID string) error {
	f.created = append(f.created, appName+"|"+sessionID+"|"+userID+"|"+organizationID)
	return f.err
}

func (f *fakeEvents) RecordSessionDeleted(_ context.Context, appName, sessionID, userID, organizationID string) error {
	f.deleted = append(f.deleted, appName+"|"+sessionID+"|"+userID+"|"+organizationID)
	return f.err
}

func TestSeedingRecordsSessionLifecycle(t *testing.T) {
	events := &fakeEvents{}
	service := seedingSessionService{
		Service: session.InMemoryService(),
		members: fakeMembers{member: Member{
			UserID: "user-owner", DisplayName: "Asha", Email: "asha@example.com",
			OrganizationID: "org_1", OrganizationName: "Acme", Role: "admin",
		}},
		events: events,
	}
	created, err := service.Create(context.Background(), &session.CreateRequest{AppName: "krude", UserID: "user-owner"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events.created) != 1 || !strings.Contains(events.created[0], "org_1") || !strings.Contains(events.created[0], created.Session.ID()) {
		t.Fatalf("created events = %v", events.created)
	}

	if err := service.Delete(context.Background(), &session.DeleteRequest{AppName: "krude", UserID: "user-owner", SessionID: created.Session.ID()}); err != nil {
		t.Fatal(err)
	}
	if len(events.deleted) != 1 || !strings.Contains(events.deleted[0], "org_1") || !strings.Contains(events.deleted[0], created.Session.ID()) {
		t.Fatalf("deleted events = %v", events.deleted)
	}
}

func TestSeedingSurvivesRecorderFailure(t *testing.T) {
	events := &fakeEvents{err: errors.New("outbox down")}
	service := seedingSessionService{
		Service: session.InMemoryService(),
		members: fakeMembers{member: Member{
			UserID: "user-owner", DisplayName: "Asha",
			OrganizationID: "org_1", OrganizationName: "Acme", Role: "member",
		}},
		events: events,
	}
	created, err := service.Create(context.Background(), &session.CreateRequest{AppName: "krude", UserID: "user-owner"})
	if err != nil {
		t.Fatalf("Create() failed because the recorder did: %v", err)
	}
	if created == nil || created.Session == nil {
		t.Fatal("Create() returned no session")
	}
	if err := service.Delete(context.Background(), &session.DeleteRequest{AppName: "krude", UserID: "user-owner", SessionID: created.Session.ID()}); err != nil {
		t.Fatalf("Delete() failed because the recorder did: %v", err)
	}
}

func TestSeedingCreatesSessionForMember(t *testing.T) {
	service := seedingSessionService{
		Service: session.InMemoryService(),
		members: fakeMembers{member: Member{
			UserID: "user-owner", DisplayName: "Asha", Email: "asha@example.com",
			OrganizationID: "org_1", OrganizationName: "Acme", Role: "owner",
		}},
	}
	created, err := service.Create(context.Background(), &session.CreateRequest{
		AppName: "krude", UserID: "user-owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	state := created.Session.State()
	for key, want := range map[string]string{
		StateUserName:       "Asha",
		StateUserAbout:      "the owner of their organization",
		StateUserEmail:      "asha@example.com",
		StateUserRole:       "owner",
		StateOrgName:        "Acme",
		StateOrganizationID: "org_1",
	} {
		got, err := state.Get(key)
		if err != nil || got != want {
			t.Errorf("state[%q] = %v, want %q (error %v)", key, got, want, err)
		}
	}
}

func TestSeedingLeavesCallerStateIntact(t *testing.T) {
	service := seedingSessionService{
		Service: session.InMemoryService(),
		members: fakeMembers{member: Member{
			UserID: "user-owner", DisplayName: "Asha",
			OrganizationID: "org_1", OrganizationName: "Acme", Role: "member",
		}},
	}
	created, err := service.Create(context.Background(), &session.CreateRequest{
		AppName: "krude", UserID: "user-owner",
		State: map[string]any{"user_name": "Ash", "custom_key": "custom"},
	})
	if err != nil {
		t.Fatal(err)
	}
	state := created.Session.State()
	for key, want := range map[string]string{
		"user_name":         "Ash",
		"user_about":        "a member of their organization",
		"user_role":         "member",
		"custom_key":        "custom",
		StateOrgName:        "Acme",
		StateOrganizationID: "org_1",
	} {
		got, err := state.Get(key)
		if err != nil || got != want {
			t.Errorf("state[%q] = %v, want %q (error %v)", key, got, want, err)
		}
	}
}

func TestSeedingSkipsUsersWhoAreNotMembers(t *testing.T) {
	service := seedingSessionService{
		Service: session.InMemoryService(),
		members: fakeMembers{err: ErrMemberNotFound},
	}
	created, err := service.Create(context.Background(), &session.CreateRequest{
		AppName: "krude", UserID: "anonymous",
		State: map[string]any{"user_name": "Test User"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := created.Session.State().Get("user_name")
	if err != nil || got != "Test User" {
		t.Fatalf("user_name = %v, want Test User (error %v)", got, err)
	}
	if _, err := created.Session.State().Get(StateOrgName); !errors.Is(err, session.ErrStateKeyNotExist) {
		t.Fatalf("seeding reached a non-member: org_name = %v, error %v", got, err)
	}
}

func TestSeedingFailsOnMemberLookupError(t *testing.T) {
	service := seedingSessionService{
		Service: session.InMemoryService(),
		members: fakeMembers{err: errors.New("database down")},
	}
	if _, err := service.Create(context.Background(), &session.CreateRequest{
		AppName: "krude", UserID: "user-owner",
	}); err == nil {
		t.Fatal("Create() error = nil, want member lookup failure to fail the call")
	}
}

func TestSeedingSkipsEmptyDisplayName(t *testing.T) {
	service := seedingSessionService{
		Service: session.InMemoryService(),
		members: fakeMembers{member: Member{
			UserID: "user-owner", DisplayName: "",
			OrganizationID: "org_1", OrganizationName: "Acme", Role: "member",
		}},
	}
	created, err := service.Create(context.Background(), &session.CreateRequest{
		AppName: "krude", UserID: "user-owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := created.Session.State().Get(StateUserName); !errors.Is(err, session.ErrStateKeyNotExist) {
		t.Fatalf("an empty display_name seeded user_name: error %v", err)
	}
}

func TestMemberAbout(t *testing.T) {
	cases := map[string]string{
		"owner":  "the owner of their organization",
		"admin":  "an admin of their organization",
		"member": "a member of their organization",
		"":       "a member of their organization",
	}
	for role, want := range cases {
		if got := memberAbout(role); got != want {
			t.Errorf("memberAbout(%q) = %q, want %q", role, got, want)
		}
	}
}

func TestSeedingPreservesAllCallerKeys(t *testing.T) {
	service := seedingSessionService{
		Service: session.InMemoryService(),
		members: fakeMembers{member: Member{
			UserID: "user-owner", DisplayName: "Asha",
			OrganizationID: "org_1", OrganizationName: "Acme", Role: "admin",
		}},
	}
	sent := map[string]any{"a": "1", "b": "2", "user_name": "Asha Verma"}
	created, err := service.Create(context.Background(), &session.CreateRequest{
		AppName: "krude", UserID: "user-owner", State: sent,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := created.Session.State()
	// The request's map must not be mutated: the caller may reuse it.
	if len(sent) != 3 {
		t.Errorf("the caller's state map was mutated: %v", sent)
	}
	for key, want := range map[string]string{"a": "1", "b": "2", "user_name": "Asha Verma", "user_about": "an admin of their organization"} {
		got, err := state.Get(key)
		if err != nil || got != want {
			t.Errorf("state[%q] = %v, want %q (error %v)", key, got, want, err)
		}
	}
}
