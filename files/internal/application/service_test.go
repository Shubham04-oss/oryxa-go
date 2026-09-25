package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	fileseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/files/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/files/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/files/internal/blob"
	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
)

func testService(store Store, blobs blob.Store) *Service {
	service := New(store, blobs, fakeDirectory{}, newFakeTeams(), nil)
	service.now = func() time.Time { return time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC) }
	return service
}

// given is a create's input already in hand, as a caller that has nothing to
// decode would supply it.
func given(input CreateInput) InputReader {
	return func() (CreateInput, error) { return input, nil }
}

func TestCreateStoresObjectAndEvent(t *testing.T) {
	store := newFakeStore()
	blobs := blob.NewMemory()
	service := testService(store, blobs)

	object, err := service.Create(context.Background(), creds("user-owner"), "org_1", given(CreateInput{
		Filename: "notes.txt", ContentType: "text/plain", Content: []byte("hello"),
	}))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !strings.HasPrefix(object.ID, "file_") || object.SizeBytes != 5 || object.Purpose != domain.PurposeUpload || object.CreatedBy != "user-owner" {
		t.Fatalf("object = %+v", object)
	}
	if object.Scope != domain.ScopeMember || object.MemberID != "user-owner" || object.TeamID != "" {
		t.Fatalf("placement = %+v", object)
	}
	if object.ExpiresAt.Sub(object.CreatedAt) != 30*24*time.Hour {
		t.Fatalf("expires_at = %s", object.ExpiresAt)
	}
	if object.Key() != "org/org_1/member/user-owner/2026/09/14/obj/"+object.ID {
		t.Fatalf("key = %q", object.Key())
	}
	got, body, err := service.Content(context.Background(), creds("user-owner"), "org_1", object.ID)
	if err != nil || string(body) != "hello" || got.SHA256 != object.SHA256 {
		t.Fatalf("Content() = %+v %q %v", got, body, err)
	}
	if len(store.events) != 1 || store.events[0].DetailType != fileseventsv1.ObjectCreatedDetailType {
		t.Fatalf("events = %+v", store.events)
	}
}

func TestMemberFileIsPrivateToOwner(t *testing.T) {
	service := testService(newFakeStore(), blob.NewMemory())
	created, err := service.Create(context.Background(), creds("user-owner"), "org_1", given(CreateInput{Filename: "a.txt", ContentType: "text/plain", Content: []byte("a")}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(context.Background(), creds("user-member"), "org_1", created.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("member get error = %v, want not found", err)
	}
	listed, err := service.List(context.Background(), creds("user-member"), "org_1", ListFilter{})
	if err != nil || len(listed) != 0 {
		t.Fatalf("member list = %+v, %v", listed, err)
	}
	adminList, err := service.List(context.Background(), creds("user-owner"), "org_1", ListFilter{})
	if err != nil || len(adminList) != 1 || adminList[0].ID != created.ID {
		t.Fatalf("admin list = %+v, %v", adminList, err)
	}
	dayList, err := service.List(context.Background(), creds("user-owner"), "org_1", ListFilter{Date: domain.DateFilter{Year: 2026, Month: 9, Day: 14}})
	if err != nil || len(dayList) != 1 || dayList[0].ID != created.ID {
		t.Fatalf("day list = %+v, %v", dayList, err)
	}
	otherYear, err := service.List(context.Background(), creds("user-owner"), "org_1", ListFilter{Date: domain.DateFilter{Year: 2025}})
	if err != nil || len(otherYear) != 0 {
		t.Fatalf("other year list = %+v, %v", otherYear, err)
	}
}

func TestOrgFileIsVisibleToMembers(t *testing.T) {
	service := testService(newFakeStore(), blob.NewMemory())
	created, err := service.Create(context.Background(), creds("user-owner"), "org_1", given(CreateInput{
		Filename: "handbook.pdf", ContentType: "application/pdf", Scope: domain.ScopeOrganization, Content: []byte("org"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if created.Scope != domain.ScopeOrganization || created.Key() != "org/org_1/organization/2026/09/14/obj/"+created.ID {
		t.Fatalf("org object = %+v key = %q", created, created.Key())
	}
	if _, err := service.Get(context.Background(), creds("user-member"), "org_1", created.ID); err != nil {
		t.Fatalf("member get org file error = %v", err)
	}
	listed, err := service.List(context.Background(), creds("user-member"), "org_1", ListFilter{})
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("member list org files = %+v, %v", listed, err)
	}
}

func TestCreateDoesNotReadTheInputOfACallerWhoIsNotAMember(t *testing.T) {
	service := testService(newFakeStore(), blob.NewMemory())
	read := func() (CreateInput, error) {
		t.Fatal("the input was read for a caller the directory does not know")
		return CreateInput{}, nil
	}
	// "user-stranger" belongs to no organization the fake directory knows.
	if _, err := service.Create(context.Background(), creds("user-stranger"), "org_1", read); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("stranger create error = %v, want not found", err)
	}
}

func TestMemberCannotCreateOrgFile(t *testing.T) {
	service := testService(newFakeStore(), blob.NewMemory())
	if _, err := service.Create(context.Background(), creds("user-member"), "org_1", given(CreateInput{
		Filename: "a.txt", ContentType: "text/plain", Scope: domain.ScopeOrganization, Content: []byte("a"),
	})); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member org create error = %v, want forbidden", err)
	}
}

func TestTeamFileFollowsTeamsACL(t *testing.T) {
	service := testService(newFakeStore(), blob.NewMemory())
	created, err := service.Create(context.Background(), creds("user-owner"), "org_1", given(CreateInput{
		Filename: "spec.md", ContentType: "text/markdown", Scope: domain.ScopeTeam, TeamID: "team_eng", Content: []byte("spec"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if created.Key() != "org/org_1/team/team_eng/2026/09/14/obj/"+created.ID {
		t.Fatalf("team key = %q", created.Key())
	}
	if _, err := service.Get(context.Background(), creds("user-member"), "org_1", created.ID); err != nil {
		t.Fatalf("engineering member get error = %v", err)
	}
	if _, err := service.Get(context.Background(), creds("user-outsider"), "org_1", created.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("outsider get error = %v, want not found", err)
	}
	if _, err := service.Create(context.Background(), creds("user-outsider"), "org_1", given(CreateInput{
		Filename: "nope.txt", ContentType: "text/plain", Scope: domain.ScopeTeam, TeamID: "team_eng", Content: []byte("nope"),
	})); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("outsider team create error = %v, want not found", err)
	}
	listed, err := service.List(context.Background(), creds("user-member"), "org_1", ListFilter{TeamID: "team_eng"})
	if err != nil || len(listed) != 1 {
		t.Fatalf("team list = %+v, %v", listed, err)
	}
	if _, err := service.List(context.Background(), creds("user-outsider"), "org_1", ListFilter{TeamID: "team_eng"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("outsider team list error = %v, want not found", err)
	}
}

func TestOrganizationAdminReadsTeamFilesWithoutTeamMembershipLookup(t *testing.T) {
	store := newFakeStore()
	service := testService(store, blob.NewMemory())
	created, err := service.Create(context.Background(), creds("user-owner"), "org_1", given(CreateInput{
		Filename: "team.md", ContentType: "text/markdown", Scope: domain.ScopeTeam,
		TeamID: "team_eng", Content: []byte("team notes"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	service.teams = nil
	if _, err := service.Get(context.Background(), creds("user-admin"), "org_1", created.ID); err != nil {
		t.Fatalf("org admin get team file = %v", err)
	}
	listed, err := service.List(context.Background(), creds("user-admin"), "org_1", ListFilter{Scope: domain.ScopeTeam, TeamID: "team_eng"})
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("org admin list team files = %+v, %v", listed, err)
	}
}

func TestBillingAttachmentIsAdminOnly(t *testing.T) {
	service := testService(newFakeStore(), blob.NewMemory())
	if _, err := service.Create(context.Background(), creds("user-owner"), "org_1", given(CreateInput{
		Filename: "invoice.pdf", ContentType: "application/pdf", Purpose: domain.PurposeBillingAttachment, Scope: domain.ScopeMember, Content: []byte("inv"),
	})); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("billing on member error = %v, want invalid", err)
	}
	created, err := service.Create(context.Background(), creds("user-owner"), "org_1", given(CreateInput{
		Filename: "invoice.pdf", ContentType: "application/pdf", Purpose: domain.PurposeBillingAttachment, Scope: domain.ScopeOrganization, Content: []byte("inv"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if created.ExpiresAt.Sub(created.CreatedAt) != 90*24*time.Hour {
		t.Fatalf("billing retention = %s", created.ExpiresAt)
	}
	if _, err := service.Get(context.Background(), creds("user-member"), "org_1", created.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("member billing get error = %v, want not found", err)
	}
	if _, err := service.Get(context.Background(), creds("user-admin"), "org_1", created.ID); err != nil {
		t.Fatalf("admin billing get error = %v", err)
	}
	memberList, err := service.List(context.Background(), creds("user-member"), "org_1", ListFilter{Scope: domain.ScopeOrganization})
	if err != nil || len(memberList) != 0 {
		t.Fatalf("member org list leaked billing = %+v, %v", memberList, err)
	}
}

func TestAdminOnlyOrganizationFileACL(t *testing.T) {
	service := testService(newFakeStore(), blob.NewMemory())
	if _, err := service.Create(context.Background(), creds("user-member"), "org_1", given(CreateInput{
		Filename: "secret.txt", ContentType: "text/plain", Access: domain.AccessAdmins, Content: []byte("secret"),
	})); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member admin-only create = %v", err)
	}
	if _, err := service.Create(context.Background(), creds("user-owner"), "org_1", given(CreateInput{
		Filename: "secret.txt", ContentType: "text/plain", Scope: domain.ScopeMember, Access: domain.AccessAdmins, Content: []byte("secret"),
	})); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("private admin-only create = %v", err)
	}
	restricted, err := service.Create(context.Background(), creds("user-owner"), "org_1", given(CreateInput{
		Filename: "secret.txt", ContentType: "text/plain", Scope: domain.ScopeOrganization, Access: domain.AccessAdmins, Content: []byte("secret"),
	}))
	if err != nil || restricted.Access != domain.AccessAdmins {
		t.Fatalf("restricted file = %+v, %v", restricted, err)
	}
	general, err := service.Create(context.Background(), creds("user-owner"), "org_1", given(CreateInput{
		Filename: "handbook.txt", ContentType: "text/plain", Scope: domain.ScopeOrganization, Content: []byte("handbook"),
	}))
	if err != nil || general.Access != domain.AccessMembers {
		t.Fatalf("general file = %+v, %v", general, err)
	}
	if _, err := service.Get(context.Background(), creds("user-member"), "org_1", restricted.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("member restricted GET = %v", err)
	}
	if _, _, err := service.Content(context.Background(), creds("user-member"), "org_1", restricted.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("member restricted content = %v", err)
	}
	if _, err := service.Get(context.Background(), creds("user-owner"), "org_1", restricted.ID); err != nil {
		t.Fatal(err)
	}
	listed, err := service.List(context.Background(), creds("user-member"), "org_1", ListFilter{Scope: domain.ScopeOrganization})
	if err != nil || len(listed) != 1 || listed[0].ID != general.ID {
		t.Fatalf("member list = %+v, %v", listed, err)
	}
}

// TestAdminsAgentGetsAPlainMembersReachToAdminOnlyFiles: RAG and Knowledge
// already give an agent's token a plain member's reach; Files must too. An
// agent acting for an org admin can be answering someone else entirely — a
// teammate's interaction request, a room it was mentioned in — so it may not
// list, read or download a file only admins may see, even though its member
// could. The admin's own session, and a plain member, are unaffected.
func TestAdminsAgentGetsAPlainMembersReachToAdminOnlyFiles(t *testing.T) {
	service := testService(newFakeStore(), blob.NewMemory())
	restricted, err := service.Create(context.Background(), creds("user-owner"), "org_1", given(CreateInput{
		Filename: "secret.txt", ContentType: "text/plain", Scope: domain.ScopeOrganization, Access: domain.AccessAdmins, Content: []byte("secret"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	general, err := service.Create(context.Background(), creds("user-owner"), "org_1", given(CreateInput{
		Filename: "handbook.txt", ContentType: "text/plain", Scope: domain.ScopeOrganization, Content: []byte("handbook"),
	}))
	if err != nil {
		t.Fatal(err)
	}

	for _, reader := range []struct {
		name           string
		credentials    authclient.Credentials
		seesRestricted bool
	}{
		{"the admin's agent", agentCreds("user-admin"), false},
		{"the admin's own session", creds("user-admin"), true},
		{"a plain member", creds("user-member"), false},
	} {
		t.Run(reader.name, func(t *testing.T) {
			wantListed := 1
			if reader.seesRestricted {
				wantListed = 2
			}
			listed, err := service.List(context.Background(), reader.credentials, "org_1", ListFilter{Scope: domain.ScopeOrganization})
			if err != nil || len(listed) != wantListed {
				t.Fatalf("List = %+v, %v; want %d files", listed, err, wantListed)
			}
			_, getErr := service.Get(context.Background(), reader.credentials, "org_1", restricted.ID)
			_, _, contentErr := service.Content(context.Background(), reader.credentials, "org_1", restricted.ID)
			_, downloadErr := service.DownloadURL(context.Background(), reader.credentials, "org_1", restricted.ID)
			if reader.seesRestricted {
				if getErr != nil || contentErr != nil || downloadErr != nil {
					t.Fatalf("reading the restricted file = %v, %v, %v; want it read", getErr, contentErr, downloadErr)
				}
			} else {
				if !errors.Is(getErr, domain.ErrNotFound) || !errors.Is(contentErr, domain.ErrNotFound) || !errors.Is(downloadErr, domain.ErrNotFound) {
					t.Fatalf("reading the restricted file = %v, %v, %v; want ErrNotFound throughout", getErr, contentErr, downloadErr)
				}
			}
			// The general file stays visible to everyone regardless.
			if _, err := service.Get(context.Background(), reader.credentials, "org_1", general.ID); err != nil {
				t.Fatalf("Get(general) = %v", err)
			}
		})
	}
}

// TestAdminsAgentCannotPerformAdminOnlyWrites: creating any organization
// file, creating an admin-only file, and deleting an organization file are
// an org admin's acts. An agent gets a plain member's reach and is refused
// each one even acting for an admin; the admin's own session and a plain
// member behave as before.
func TestAdminsAgentCannotPerformAdminOnlyWrites(t *testing.T) {
	service := testService(newFakeStore(), blob.NewMemory())
	agent := agentCreds("user-admin")

	if _, err := service.Create(context.Background(), agent, "org_1", given(CreateInput{
		Filename: "handbook.txt", ContentType: "text/plain", Scope: domain.ScopeOrganization, Content: []byte("handbook"),
	})); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("admin's agent creating an organization file = %v, want forbidden", err)
	}
	if _, err := service.Create(context.Background(), agent, "org_1", given(CreateInput{
		Filename: "secret.txt", ContentType: "text/plain", Scope: domain.ScopeOrganization, Access: domain.AccessAdmins, Content: []byte("secret"),
	})); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("admin's agent creating an admin-only file = %v, want forbidden", err)
	}

	orgFile, err := service.Create(context.Background(), creds("user-owner"), "org_1", given(CreateInput{
		Filename: "invoice.pdf", ContentType: "application/pdf", Scope: domain.ScopeOrganization, Content: []byte("inv"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(context.Background(), agent, "org_1", orgFile.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("admin's agent deleting an organization file = %v, want not found", err)
	}
	if err := service.Delete(context.Background(), creds("user-member"), "org_1", orgFile.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("member deleting an organization file = %v, want not found", err)
	}
	if err := service.Delete(context.Background(), creds("user-admin"), "org_1", orgFile.ID); err != nil {
		t.Fatalf("admin's own session deleting an organization file = %v", err)
	}
}

// TestAdminsAgentDoesNotReachEveryMembersFiles: an org admin's own session
// sees every member's files; an agent acting for that admin sees only its
// own member's, exactly as a plain member would.
func TestAdminsAgentDoesNotReachEveryMembersFiles(t *testing.T) {
	service := testService(newFakeStore(), blob.NewMemory())
	colleagueFile, err := service.Create(context.Background(), creds("user-member"), "org_1", given(CreateInput{
		Filename: "a.txt", ContentType: "text/plain", Content: []byte("a"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	agent := agentCreds("user-admin")
	if _, err := service.Get(context.Background(), agent, "org_1", colleagueFile.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("admin's agent reading a colleague's private file = %v, want not found", err)
	}
	if listed, err := service.List(context.Background(), agent, "org_1", ListFilter{}); err != nil || len(listed) != 0 {
		t.Fatalf("admin's agent listing member files = %+v, %v; want none", listed, err)
	}
	// The admin's own session is unaffected.
	if _, err := service.Get(context.Background(), creds("user-admin"), "org_1", colleagueFile.ID); err != nil {
		t.Fatalf("admin's own session reading a colleague's private file = %v", err)
	}
}

// TestAdminsAgentReachesATeamOnlyAsItsMemberActuallyCan: teams shows an org
// admin every team and says they can manage each one, whether or not they
// are really on it (see fakeTeams in fakes_test.go). An agent must not
// inherit that: on team_eng, where user-admin is not a member at all, the
// agent gets nothing; on team_design, where user-admin is a plain member
// only (teams' can_manage there is still just the org bypass), the agent
// reads the team's files but may not manage or delete them.
func TestAdminsAgentReachesATeamOnlyAsItsMemberActuallyCan(t *testing.T) {
	service := testService(newFakeStore(), blob.NewMemory())
	engFile, err := service.Create(context.Background(), creds("user-owner"), "org_1", given(CreateInput{
		Filename: "eng.md", ContentType: "text/markdown", Scope: domain.ScopeTeam, TeamID: "team_eng", Content: []byte("eng"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	designFile, err := service.Create(context.Background(), creds("user-owner"), "org_1", given(CreateInput{
		Filename: "design.md", ContentType: "text/markdown", Scope: domain.ScopeTeam, TeamID: "team_design", Content: []byte("design"),
	}))
	if err != nil {
		t.Fatal(err)
	}

	agent := agentCreds("user-admin")
	if _, err := service.Get(context.Background(), agent, "org_1", engFile.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("admin's agent reading a team it is not really on = %v, want not found", err)
	}
	if _, err := service.List(context.Background(), agent, "org_1", ListFilter{TeamID: "team_eng"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("admin's agent listing a team it is not really on = %v, want not found", err)
	}

	if _, err := service.Get(context.Background(), agent, "org_1", designFile.ID); err != nil {
		t.Fatalf("admin's agent reading its member's own team = %v", err)
	}
	listed, err := service.List(context.Background(), agent, "org_1", ListFilter{TeamID: "team_design"})
	if err != nil || len(listed) != 1 || listed[0].ID != designFile.ID {
		t.Fatalf("admin's agent listing its member's own team = %+v, %v", listed, err)
	}
	if err := service.Delete(context.Background(), agent, "org_1", designFile.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("admin's agent deleting a team file on the org bypass alone = %v, want not found", err)
	}

	// The admin's own session is unaffected either way.
	if _, err := service.Get(context.Background(), creds("user-admin"), "org_1", engFile.ID); err != nil {
		t.Fatalf("admin's own session reading team_eng = %v", err)
	}
	if err := service.Delete(context.Background(), creds("user-admin"), "org_1", designFile.ID); err != nil {
		t.Fatalf("admin's own session deleting a team_design file = %v", err)
	}
}

// TestAdminsAgentNeedsTeamsUnlikeTheAdminItself is the mirror image of
// TestOrganizationAdminReadsTeamFilesWithoutTeamMembershipLookup: the admin's
// own session never needs the teams client, since ActsAsAdmin short-circuits
// before it is asked. An agent acting for that admin gets no such shortcut
// and cannot be let through on role alone when there is no teams client to ask.
func TestAdminsAgentNeedsTeamsUnlikeTheAdminItself(t *testing.T) {
	service := testService(newFakeStore(), blob.NewMemory())
	teamFile, err := service.Create(context.Background(), creds("user-owner"), "org_1", given(CreateInput{
		Filename: "team.md", ContentType: "text/markdown", Scope: domain.ScopeTeam, TeamID: "team_eng", Content: []byte("team notes"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	service.teams = nil
	if _, err := service.Get(context.Background(), creds("user-admin"), "org_1", teamFile.ID); err != nil {
		t.Fatalf("admin's own session reading a team file with no teams client = %v", err)
	}
	if _, err := service.Get(context.Background(), agentCreds("user-admin"), "org_1", teamFile.ID); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("admin's agent reading a team file with no teams client = %v, want invalid", err)
	}
}

func TestDeleteIsCreatorOrAdmin(t *testing.T) {
	service := testService(newFakeStore(), blob.NewMemory())
	created, err := service.Create(context.Background(), creds("user-member"), "org_1", given(CreateInput{Filename: "a.txt", ContentType: "text/plain", Content: []byte("a")}))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(context.Background(), creds("user-outsider"), "org_1", created.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("outsider delete error = %v, want not found", err)
	}
	if err := service.Delete(context.Background(), creds("user-owner"), "org_1", created.ID); err != nil {
		t.Fatalf("admin delete error = %v", err)
	}
	if _, err := service.Get(context.Background(), creds("user-owner"), "org_1", created.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("get after delete = %v", err)
	}
}

func TestRejectsDisallowedPurposeAndType(t *testing.T) {
	service := testService(newFakeStore(), blob.NewMemory())
	if _, err := service.Create(context.Background(), creds("user-owner"), "org_1", given(CreateInput{Filename: "a.exe", ContentType: "application/x-msdownload", Content: []byte("a")})); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("exe error = %v", err)
	}
	if _, err := service.Create(context.Background(), creds("user-owner"), "org_1", given(CreateInput{Filename: "a.txt", ContentType: "text/plain", Purpose: "other", Content: []byte("a")})); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("purpose error = %v", err)
	}
}

func TestStrangerIsNotFound(t *testing.T) {
	service := testService(newFakeStore(), blob.NewMemory())
	if _, err := service.List(context.Background(), creds("user-stranger"), "org_1", ListFilter{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("stranger list error = %v, want not found", err)
	}
}

func TestOrganizationDeletedWipesPrefix(t *testing.T) {
	store := newFakeStore()
	blobs := blob.NewMemory()
	service := testService(store, blobs)
	created, err := service.Create(context.Background(), creds("user-owner"), "org_1", given(CreateInput{Filename: "a.txt", ContentType: "text/plain", Content: []byte("a")}))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.OrganizationDeleted(context.Background(), autheventsv1.OrganizationDeleted{OrganizationID: "org_1", DeletedBy: "user-owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := blobs.Get(context.Background(), created.Key()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("blob after org delete = %v", err)
	}
}

func TestMemberRemovedDeletesOnlyMemberFiles(t *testing.T) {
	store := newFakeStore()
	service := testService(store, blob.NewMemory())
	personal, err := service.Create(context.Background(), creds("user-member"), "org_1", given(CreateInput{Filename: "a.txt", ContentType: "text/plain", Content: []byte("a")}))
	if err != nil {
		t.Fatal(err)
	}
	teamFile, err := service.Create(context.Background(), creds("user-member"), "org_1", given(CreateInput{
		Filename: "team.txt", ContentType: "text/plain", Scope: domain.ScopeTeam, TeamID: "team_eng", Content: []byte("t"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.MemberRemoved(context.Background(), autheventsv1.MemberRemoved{OrganizationID: "org_1", UserID: "user-member", Reason: "left"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(context.Background(), creds("user-owner"), "org_1", personal.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("personal after member removed = %v", err)
	}
	if _, err := service.Get(context.Background(), creds("user-owner"), "org_1", teamFile.ID); err != nil {
		t.Fatalf("team file after member removed = %v", err)
	}
}

func TestTeamDeletedWipesTeamFiles(t *testing.T) {
	store := newFakeStore()
	blobs := blob.NewMemory()
	service := testService(store, blobs)
	teamFile, err := service.Create(context.Background(), creds("user-owner"), "org_1", given(CreateInput{
		Filename: "spec.md", ContentType: "text/markdown", Scope: domain.ScopeTeam, TeamID: "team_eng", Content: []byte("spec"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	personal, err := service.Create(context.Background(), creds("user-owner"), "org_1", given(CreateInput{Filename: "a.txt", ContentType: "text/plain", Content: []byte("a")}))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.TeamDeleted(context.Background(), teamseventsv1.TeamDeleted{OrganizationID: "org_1", TeamID: "team_eng", DeletedBy: "user-owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := blobs.Get(context.Background(), teamFile.Key()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("team blob after team delete = %v", err)
	}
	if _, err := service.Get(context.Background(), creds("user-owner"), "org_1", personal.ID); err != nil {
		t.Fatalf("personal after team delete = %v", err)
	}
}

func creds(userID string) authclient.Credentials {
	return authclient.Credentials{Authorization: "Bearer " + userID}
}
