package application

import (
	"context"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/files/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/files/internal/blob"
	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/access"
)

// recordingService is testService with a recorder to read back.
func recordingService(t *testing.T) (*Service, *fakeRecorder) {
	t.Helper()
	recorder := &fakeRecorder{}
	service := New(newFakeStore(), blob.NewMemory(), fakeDirectory{}, newFakeTeams(), recorder, nil)
	service.now = func() time.Time { return time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC) }
	return service, recorder
}

// want is the part of a decision that says who reached what, and why.
type want struct {
	action, owner, actor, agent, agentRole, reason string
}

func expectOne(t *testing.T, recorder *fakeRecorder, fileID string, expected want) {
	t.Helper()
	got := recorder.take()
	if len(got) != 1 {
		t.Fatalf("recorded %d decisions, want 1: %+v", len(got), got)
	}
	decision := got[0]
	if decision.OrganizationID != "org_1" || decision.Service != "files" || decision.ResourceKind != "file" || decision.ResourceID != fileID {
		t.Fatalf("decision = %+v, want files' file %s in org_1", decision, fileID)
	}
	actual := want{decision.Action, decision.OwnerID, decision.ActorUserID, decision.ActorAgentID, decision.ActorAgentRole, decision.Reason}
	if actual != expected {
		t.Fatalf("decision = %+v, want %+v", actual, expected)
	}
}

func TestEachSingleDecisionIsRecordedOnceWithItsRule(t *testing.T) {
	ctx := context.Background()
	service, recorder := recordingService(t)
	create := func(credentials authclient.Credentials, input CreateInput) domain.Object {
		t.Helper()
		input.Filename, input.ContentType, input.Content = "a.txt", "text/plain", []byte("a")
		object, err := service.Create(ctx, credentials, "org_1", given(input))
		if err != nil {
			t.Fatalf("Create(%+v) = %v", input, err)
		}
		return object
	}
	read := func(credentials authclient.Credentials, fileID string) {
		t.Helper()
		if _, err := service.Get(ctx, credentials, "org_1", fileID); err != nil {
			t.Fatalf("Get(%s) = %v", fileID, err)
		}
	}
	remove := func(credentials authclient.Credentials, fileID string) {
		t.Helper()
		if err := service.Delete(ctx, credentials, "org_1", fileID); err != nil {
			t.Fatalf("Delete(%s) = %v", fileID, err)
		}
	}

	t.Run("member's own file", func(t *testing.T) {
		mine := create(creds("user-member"), CreateInput{})
		expectOne(t, recorder, mine.ID, want{access.ActionWrite, "user-member", "user-member", "", "", access.ReasonOwner})
		read(creds("user-member"), mine.ID)
		expectOne(t, recorder, mine.ID, want{access.ActionRead, "user-member", "user-member", "", "", access.ReasonOwner})
		if _, _, err := service.Content(ctx, creds("user-member"), "org_1", mine.ID); err != nil {
			t.Fatal(err)
		}
		expectOne(t, recorder, mine.ID, want{access.ActionRead, "user-member", "user-member", "", "", access.ReasonOwner})
		if _, err := service.DownloadURL(ctx, creds("user-member"), "org_1", mine.ID); err != nil {
			t.Fatal(err)
		}
		expectOne(t, recorder, mine.ID, want{access.ActionRead, "user-member", "user-member", "", "", access.ReasonOwner})
		remove(creds("user-member"), mine.ID)
		expectOne(t, recorder, mine.ID, want{access.ActionDelete, "user-member", "user-member", "", "", access.ReasonOwner})
	})

	t.Run("admin reaching a member's file", func(t *testing.T) {
		theirs := create(creds("user-member"), CreateInput{})
		recorder.take()
		read(creds("user-admin"), theirs.ID)
		expectOne(t, recorder, theirs.ID, want{access.ActionRead, "user-member", "user-admin", "", "", access.ReasonOrgAdmin})
		remove(creds("user-admin"), theirs.ID)
		expectOne(t, recorder, theirs.ID, want{access.ActionDelete, "user-member", "user-admin", "", "", access.ReasonOrgAdmin})
	})

	t.Run("teammate and team admin", func(t *testing.T) {
		team := create(creds("user-member"), CreateInput{Scope: domain.ScopeTeam, TeamID: "team_eng"})
		expectOne(t, recorder, team.ID, want{access.ActionWrite, "", "user-member", "", "", access.ReasonTeamMember})
		// Reading needs only a place on the team, even for its admin.
		read(creds("user-lead"), team.ID)
		expectOne(t, recorder, team.ID, want{access.ActionRead, "", "user-lead", "", "", access.ReasonTeamMember})
		// An org admin's own session is never asked about teams.
		read(creds("user-admin"), team.ID)
		expectOne(t, recorder, team.ID, want{access.ActionRead, "", "user-admin", "", "", access.ReasonOrgAdmin})
		// Deleting a teammate's file takes managing the team.
		remove(creds("user-lead"), team.ID)
		expectOne(t, recorder, team.ID, want{access.ActionDelete, "", "user-lead", "", "", access.ReasonTeamAdmin})

		own := create(creds("user-member"), CreateInput{Scope: domain.ScopeTeam, TeamID: "team_eng"})
		recorder.take()
		remove(creds("user-member"), own.ID)
		expectOne(t, recorder, own.ID, want{access.ActionDelete, "", "user-member", "", "", access.ReasonOwner})
	})

	t.Run("organization file", func(t *testing.T) {
		shared := create(creds("user-admin"), CreateInput{Scope: domain.ScopeOrganization})
		expectOne(t, recorder, shared.ID, want{access.ActionWrite, "", "user-admin", "", "", access.ReasonOrgAdmin})
		read(creds("user-member"), shared.ID)
		expectOne(t, recorder, shared.ID, want{access.ActionRead, "", "user-member", "", "", access.ReasonOrganization})
		read(creds("user-admin"), shared.ID)
		expectOne(t, recorder, shared.ID, want{access.ActionRead, "", "user-admin", "", "", access.ReasonOrganization})

		adminsOnly := create(creds("user-admin"), CreateInput{Scope: domain.ScopeOrganization, Access: domain.AccessAdmins})
		recorder.take()
		read(creds("user-owner"), adminsOnly.ID)
		expectOne(t, recorder, adminsOnly.ID, want{access.ActionRead, "", "user-owner", "", "", access.ReasonOrgAdmin})
		remove(creds("user-owner"), adminsOnly.ID)
		expectOne(t, recorder, adminsOnly.ID, want{access.ActionDelete, "", "user-owner", "", "", access.ReasonOrgAdmin})
	})

	t.Run("agents", func(t *testing.T) {
		// A specialist whose grant names member files: the grant let it in.
		specialist := grantedCreds("user-member", domain.FilesMember)
		stored := create(specialist, CreateInput{})
		expectOne(t, recorder, stored.ID, want{access.ActionWrite, "user-member", "user-member", "agt_user-member", "specialist", access.ReasonGrant})
		read(specialist, stored.ID)
		expectOne(t, recorder, stored.ID, want{access.ActionRead, "user-member", "user-member", "agt_user-member", "specialist", access.ReasonGrant})
		remove(specialist, stored.ID)
		expectOne(t, recorder, stored.ID, want{access.ActionDelete, "user-member", "user-member", "agt_user-member", "specialist", access.ReasonGrant})

		// A primary has no grant to narrow it: its member's own rule.
		primary := roleCreds("user-member", "primary")
		mine := create(creds("user-member"), CreateInput{})
		recorder.take()
		read(primary, mine.ID)
		expectOne(t, recorder, mine.ID, want{access.ActionRead, "user-member", "user-member", "agt_user-member", "primary", access.ReasonOwner})
	})

	t.Run("refusals record nothing", func(t *testing.T) {
		theirs := create(creds("user-owner"), CreateInput{})
		recorder.take()
		if _, err := service.Get(ctx, creds("user-member"), "org_1", theirs.ID); err == nil {
			t.Fatal("member read another member's file")
		}
		if err := service.Delete(ctx, creds("user-member"), "org_1", theirs.ID); err == nil {
			t.Fatal("member deleted another member's file")
		}
		if got := recorder.take(); len(got) != 0 {
			t.Fatalf("refusals recorded %+v", got)
		}
	})
}

func TestListRecordsNothing(t *testing.T) {
	ctx := context.Background()
	service, recorder := recordingService(t)
	for _, input := range []CreateInput{
		{},
		{Scope: domain.ScopeTeam, TeamID: "team_eng"},
	} {
		input.Filename, input.ContentType, input.Content = "a.txt", "text/plain", []byte("a")
		if _, err := service.Create(ctx, creds("user-member"), "org_1", given(input)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.Create(ctx, creds("user-admin"), "org_1", given(CreateInput{
		Filename: "b.txt", ContentType: "text/plain", Content: []byte("b"), Scope: domain.ScopeOrganization,
	})); err != nil {
		t.Fatal(err)
	}
	recorder.take()

	for name, call := range map[string]struct {
		credentials authclient.Credentials
		filter      ListFilter
		atLeast     int
	}{
		"admin, everything":   {creds("user-admin"), ListFilter{}, 2},
		"member, everything":  {creds("user-member"), ListFilter{}, 2},
		"teammate, the team":  {creds("user-lead"), ListFilter{Scope: domain.ScopeTeam, TeamID: "team_eng"}, 1},
		"admin, the team":     {creds("user-admin"), ListFilter{Scope: domain.ScopeTeam, TeamID: "team_eng"}, 1},
		"specialist, its own": {grantedCreds("user-member", domain.FilesMember), ListFilter{}, 1},
		"outsider, the org's": {creds("user-outsider"), ListFilter{Scope: domain.ScopeOrganization}, 1},
	} {
		listed, err := service.List(ctx, call.credentials, "org_1", call.filter)
		if err != nil || len(listed) < call.atLeast {
			t.Fatalf("%s: List() = %d files, %v", name, len(listed), err)
		}
		if got := recorder.take(); len(got) != 0 {
			t.Fatalf("%s: List() recorded %+v", name, got)
		}
	}
}
