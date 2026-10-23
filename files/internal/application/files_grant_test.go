package application

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/files/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/files/internal/blob"
	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
)

// TestASpecialistReachesOnlyTheFilesItsGrantNames: with a grant naming some
// file scopes, every read and delete of a file outside them is the same not
// found a missing file gets, the list leaves it out, and a team outside it is
// as missing as one its member is not on. All ("*"), no files field, a
// member and a primary are not limited; an empty grant reaches nothing.
func TestASpecialistReachesOnlyTheFilesItsGrantNames(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	service := testService(store, blob.NewMemory())
	make := func(scope domain.Scope, teamID string) domain.Object {
		object, err := service.Create(ctx, creds("user-owner"), "org_1", given(CreateInput{
			Filename: "a.txt", ContentType: "text/plain", Scope: scope, TeamID: teamID, Content: []byte("a"),
		}))
		if err != nil {
			t.Fatal(err)
		}
		return object
	}
	own, org, team := make(domain.ScopeMember, ""), make(domain.ScopeOrganization, ""), make(domain.ScopeTeam, "team_eng")
	const missing = "file_doesnotexist"

	reads := func(who authclient.Credentials, fileID string) []error {
		_, getErr := service.Get(ctx, who, "org_1", fileID)
		_, _, contentErr := service.Content(ctx, who, "org_1", fileID)
		_, urlErr := service.DownloadURL(ctx, who, "org_1", fileID)
		return []error{getErr, contentErr, urlErr}
	}
	listed := func(who authclient.Credentials, filter ListFilter) []string {
		objects, err := service.List(ctx, who, "org_1", filter)
		if err != nil {
			t.Fatalf("list %+v: %v", filter, err)
		}
		ids := []string{}
		for _, object := range objects {
			ids = append(ids, object.ID)
		}
		slices.Sort(ids)
		return ids
	}

	// Outside the grant, each read is exactly what a missing file gives.
	for i, err := range reads(creds("user-owner"), missing) {
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("read %d of a missing file: %v", i, err)
		}
	}
	mine := grantedCreds("user-owner", "member")
	for i, err := range reads(mine, own.ID) {
		if err != nil {
			t.Errorf("granted read %d of its member's file: %v", i, err)
		}
	}
	for _, outside := range []domain.Object{org, team} {
		for i, err := range reads(mine, outside.ID) {
			if !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("read %d of an ungranted %s file: %v, want not found", i, outside.Scope, err)
			}
		}
		if err := service.Delete(ctx, mine, "org_1", outside.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("delete of an ungranted %s file: %v, want not found", outside.Scope, err)
		}
	}
	if _, err := service.Get(ctx, creds("user-owner"), "org_1", team.ID); err != nil {
		t.Fatalf("the refused delete removed the file: %v", err)
	}
	if got := listed(mine, ListFilter{}); !slices.Equal(got, []string{own.ID}) {
		t.Errorf("a specialist granted its member's files lists %v", got)
	}
	if got := listed(mine, ListFilter{Scope: domain.ScopeOrganization}); len(got) != 0 {
		t.Errorf("a specialist not granted the organization's files lists %v of them", got)
	}
	if _, err := service.List(ctx, mine, "org_1", ListFilter{TeamID: "team_eng"}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("listing an ungranted team: %v, want not found", err)
	}
	if _, err := service.List(ctx, mine, "org_1", ListFilter{TeamID: "team_nope"}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("listing a missing team: %v, want not found", err)
	}
	// It writes only where it may read: an ungranted team is as missing as
	// a team it is not on.
	write := func(who authclient.Credentials, scope domain.Scope, teamID string) error {
		_, err := service.Create(ctx, who, "org_1", given(CreateInput{
			Filename: "b.txt", ContentType: "text/plain", Scope: scope, TeamID: teamID, Content: []byte("b"),
		}))
		return err
	}
	if err := write(mine, domain.ScopeTeam, "team_eng"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("create in an ungranted team: %v, want not found", err)
	}
	if err := write(mine, domain.ScopeMember, ""); err != nil {
		t.Errorf("create in its member's granted files: %v", err)
	}

	// A grant of a team and the organization reaches those, not its
	// member's own files.
	teamAndOrg := grantedCreds("user-owner", "team_eng", "org")
	for _, inside := range []domain.Object{org, team} {
		for i, err := range reads(teamAndOrg, inside.ID) {
			if err != nil {
				t.Errorf("granted read %d of a %s file: %v", i, inside.Scope, err)
			}
		}
	}
	for i, err := range reads(teamAndOrg, own.ID) {
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("read %d of its member's ungranted file: %v, want not found", i, err)
		}
	}
	if got := listed(teamAndOrg, ListFilter{TeamID: "team_eng"}); !slices.Equal(got, []string{team.ID}) {
		t.Errorf("a specialist granted the team lists %v", got)
	}
	if got := listed(teamAndOrg, ListFilter{}); !slices.Equal(got, []string{org.ID}) {
		t.Errorf("a specialist granted a team and the organization lists %v", got)
	}
	if err := write(teamAndOrg, domain.ScopeMember, ""); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("create in its member's ungranted files: %v, want forbidden", err)
	}
	if err := write(teamAndOrg, domain.ScopeTeam, "team_eng"); err != nil {
		t.Errorf("create in a granted team: %v", err)
	}

	// A grant of nothing reaches nothing.
	nothing := grantedCreds("user-owner")
	for _, outside := range []domain.Object{own, org, team} {
		for i, err := range reads(nothing, outside.ID) {
			if !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("read %d of a %s file by a specialist granted nothing: %v", i, outside.Scope, err)
			}
		}
	}
	if got := listed(nothing, ListFilter{}); len(got) != 0 {
		t.Errorf("a specialist granted nothing lists %v", got)
	}
	if err := write(nothing, domain.ScopeMember, ""); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("create by a specialist granted nothing: %v, want forbidden", err)
	}

	// All, a grant without files, and the member themselves are not limited.
	all := listed(creds("user-owner"), ListFilter{})
	for name, who := range map[string]authclient.Credentials{
		"a specialist granted all":   grantedCreds("user-owner", "*"),
		"a specialist with no files": agentCreds("user-owner"),
		"the member's own session":   creds("user-owner"),
	} {
		for _, inside := range []domain.Object{own, org, team} {
			for i, err := range reads(who, inside.ID) {
				if err != nil {
					t.Errorf("%s read %d of a %s file: %v", name, i, inside.Scope, err)
				}
			}
		}
		if got := listed(who, ListFilter{}); !slices.Equal(got, all) {
			t.Errorf("%s lists %v, want %v", name, got, all)
		}
		if got := listed(who, ListFilter{TeamID: "team_eng"}); len(got) != 2 {
			t.Errorf("%s lists %d of the team's files, want 2", name, len(got))
		}
	}
	if !slices.Contains(all, own.ID) || !slices.Contains(all, org.ID) {
		t.Fatalf("the member lists %v", all)
	}

	// A primary or organization agent is not limited even if a grant
	// somehow rode along.
	for _, role := range []string{"primary", "organization"} {
		agent := domain.OrgMembership{Agent: true, AgentRole: role, AgentFiles: []string{}}
		if err := agent.MayReachFiles(domain.ScopeTeam, "team_eng"); err != nil {
			t.Errorf("%s: %v", role, err)
		}
	}
}
