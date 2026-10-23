package application

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/files/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/files/internal/blob"
	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
)

// TestListAllowsExactlyWhatGetAllows is permissions step 2 for files: for
// every caller and every filter, each file the list returns is one Get lets
// the caller read, and each file Get lets them read that the filter names is
// listed. A list with no scope names member and organization files; a team's
// files are named by its team_id.
//
// It runs over the store as it is, and over one whose query narrows only by
// placement, purpose and date, ignoring who is asking: the store's query is
// a pre-filter, and the list must not lean on it to decide access.
func TestListAllowsExactlyWhatGetAllows(t *testing.T) {
	t.Run("store", func(t *testing.T) { testListAllowsExactlyWhatGetAllows(t, newFakeStore()) })
	t.Run("placement-only store", func(t *testing.T) {
		testListAllowsExactlyWhatGetAllows(t, placementOnlyStore{newFakeStore()})
	})
}

// placementOnlyStore lists as an org admin would whoever asks: every member's
// files and the organization's admin-only ones.
type placementOnlyStore struct{ *fakeStore }

func (store placementOnlyStore) Objects(ctx context.Context, organizationID string, now time.Time, query domain.ObjectQuery) ([]domain.Object, error) {
	query.Admin = true
	return store.fakeStore.Objects(ctx, organizationID, now, query)
}

func testListAllowsExactlyWhatGetAllows(t *testing.T, store Store) {
	ctx := context.Background()
	service := testService(store, blob.NewMemory())
	now := service.now().UTC()
	lastYear := time.Date(2025, time.March, 1, 9, 0, 0, 0, time.UTC)

	objects := []domain.Object{
		{ID: "m_owner", Scope: domain.ScopeMember, MemberID: "user-owner", Purpose: domain.PurposeUpload},
		{ID: "m_admin", Scope: domain.ScopeMember, MemberID: "user-admin", Purpose: domain.PurposePipelineArtifact},
		{ID: "m_member", Scope: domain.ScopeMember, MemberID: "user-member", Purpose: domain.PurposeUpload, CreatedAt: lastYear},
		{ID: "m_outsider", Scope: domain.ScopeMember, MemberID: "user-outsider", Purpose: domain.PurposePipelineArtifact},
		{ID: "o_members", Scope: domain.ScopeOrganization, Access: domain.AccessMembers, Purpose: domain.PurposeUpload},
		{ID: "o_admins", Scope: domain.ScopeOrganization, Access: domain.AccessAdmins, Purpose: domain.PurposeUpload},
		{ID: "o_billing", Scope: domain.ScopeOrganization, Access: domain.AccessAdmins, Purpose: domain.PurposeBillingAttachment},
		{ID: "o_pipeline", Scope: domain.ScopeOrganization, Access: domain.AccessMembers, Purpose: domain.PurposePipelineArtifact, CreatedAt: lastYear},
		{ID: "t_eng_upload", Scope: domain.ScopeTeam, TeamID: "team_eng", Purpose: domain.PurposeUpload},
		{ID: "t_eng_pipeline", Scope: domain.ScopeTeam, TeamID: "team_eng", Purpose: domain.PurposePipelineArtifact, CreatedAt: lastYear},
		{ID: "t_design", Scope: domain.ScopeTeam, TeamID: "team_design", Purpose: domain.PurposeUpload},
		{ID: "t_ops", Scope: domain.ScopeTeam, TeamID: "team_ops", Purpose: domain.PurposeUpload},
		{ID: "m_member_expired", Scope: domain.ScopeMember, MemberID: "user-member", Purpose: domain.PurposeUpload, ExpiresAt: now.Add(-time.Hour)},
	}
	for _, object := range objects {
		object.OrganizationID = "org_1"
		object.Filename = object.ID + ".txt"
		object.ContentType = "text/plain"
		object.CreatedBy = "user-owner"
		if object.MemberID != "" {
			object.CreatedBy = object.MemberID
		}
		if object.CreatedAt.IsZero() {
			object.CreatedAt = now.Add(-time.Hour)
		}
		if object.ExpiresAt.IsZero() {
			object.ExpiresAt = now.Add(24 * time.Hour)
		}
		if err := store.CreateObject(ctx, "Acme", object, domain.Event{}); err != nil {
			t.Fatal(err)
		}
	}

	callers := map[string]authclient.Credentials{
		"owner":                        creds("user-owner"),
		"admin":                        creds("user-admin"),
		"member":                       creds("user-member"),
		"outsider":                     creds("user-outsider"),
		"admin's primary agent":        roleCreds("user-admin", "primary"),
		"admin's organization agent":   roleCreds("user-admin", "organization"),
		"owner's primary agent":        roleCreds("user-owner", "primary"),
		"member's primary agent":       roleCreds("user-member", "primary"),
		"admin's specialist, no grant": agentCreds("user-admin"),
		"admin's specialist, *":        grantedCreds("user-admin", "*"),
		"admin's specialist, member":   grantedCreds("user-admin", "member"),
		"admin's specialist, org":      grantedCreds("user-admin", "org"),
		"admin's specialist, team":     grantedCreds("user-admin", "team_design"),
		"admin's specialist, none":     grantedCreds("user-admin"),
		"owner's specialist, team+org": grantedCreds("user-owner", "team_eng", "org"),
		"member's specialist, team":    grantedCreds("user-member", "team_eng"),
		"member's specialist, ops":     grantedCreds("user-member", "team_ops"),
	}

	purposes := []domain.Purpose{"", domain.PurposeUpload, domain.PurposePipelineArtifact, domain.PurposeBillingAttachment}
	var filters []ListFilter
	for _, scope := range []domain.Scope{"", domain.ScopeMember, domain.ScopeOrganization} {
		for _, purpose := range purposes {
			filters = append(filters, ListFilter{Scope: scope, Purpose: purpose})
		}
		filters = append(filters, ListFilter{Scope: scope, Date: domain.DateFilter{Year: 2025}})
	}
	for _, team := range []string{"team_eng", "team_design", "team_ops", "team_nope"} {
		for _, purpose := range purposes {
			filters = append(filters, ListFilter{TeamID: team, Purpose: purpose})
		}
		filters = append(filters, ListFilter{Scope: domain.ScopeTeam, TeamID: team, Date: domain.DateFilter{Year: 2025}})
	}

	for name, who := range callers {
		readable := map[string]bool{}
		for _, object := range objects {
			_, err := service.Get(ctx, who, "org_1", object.ID)
			switch {
			case err == nil:
				readable[object.ID] = true
			case !errors.Is(err, domain.ErrNotFound):
				t.Fatalf("%s: get %s: %v", name, object.ID, err)
			}
		}
		for _, filter := range filters {
			label := fmt.Sprintf("%s, filter %+v", name, filter)
			listed, err := service.List(ctx, who, "org_1", filter)
			if err != nil {
				// Only a team the caller may not reach is refused, and then
				// none of its files may be readable either.
				if filter.TeamID == "" || !errors.Is(err, domain.ErrNotFound) {
					t.Errorf("%s: list: %v", label, err)
					continue
				}
				for _, object := range objects {
					if object.Scope == domain.ScopeTeam && object.TeamID == filter.TeamID && readable[object.ID] {
						t.Errorf("%s: list refuses the team, but get allows %s", label, object.ID)
					}
				}
				continue
			}
			got := map[string]bool{}
			for _, object := range listed {
				got[object.ID] = true
				if !readable[object.ID] {
					t.Errorf("%s: lists %s, which get refuses", label, object.ID)
				}
				if !named(filter, object) {
					t.Errorf("%s: lists %s, which the filter does not name", label, object.ID)
				}
			}
			for _, object := range objects {
				if readable[object.ID] && named(filter, object) && !got[object.ID] {
					t.Errorf("%s: get allows %s, but the list leaves it out", label, object.ID)
				}
			}
		}
	}
}

// named is whether a list filter names the object, leaving access aside.
func named(filter ListFilter, object domain.Object) bool {
	switch {
	case filter.TeamID != "":
		if object.Scope != domain.ScopeTeam || object.TeamID != filter.TeamID {
			return false
		}
	case filter.Scope != "":
		if object.Scope != filter.Scope {
			return false
		}
	default:
		if object.Scope == domain.ScopeTeam {
			return false
		}
	}
	if filter.Purpose != "" && object.Purpose != filter.Purpose {
		return false
	}
	from, to, _ := filter.Date.Range()
	if !from.IsZero() && (object.CreatedAt.Before(from) || !object.CreatedAt.Before(to)) {
		return false
	}
	return true
}

// TestListAsksTeamsOncePerTeam: a list decides every file with the check Get
// uses, but remembers what teams answered, so a team's many files cost one
// call to teams, not one each.
func TestListAsksTeamsOncePerTeam(t *testing.T) {
	ctx := context.Background()
	teams := newFakeTeams()
	service := New(newFakeStore(), blob.NewMemory(), fakeDirectory{}, teams, nil)
	for range 3 {
		if _, err := service.Create(ctx, creds("user-member"), "org_1", given(CreateInput{
			Filename: "a.txt", ContentType: "text/plain", Scope: domain.ScopeTeam, TeamID: "team_eng", Content: []byte("a"),
		})); err != nil {
			t.Fatal(err)
		}
	}
	for name, who := range map[string]authclient.Credentials{
		"member":        creds("user-member"),
		"owner's agent": roleCreds("user-owner", "primary"),
		"a specialist":  grantedCreds("user-member", "team_eng"),
	} {
		teams.calls = 0
		listed, err := service.List(ctx, who, "org_1", ListFilter{TeamID: "team_eng"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(listed) != 3 {
			t.Errorf("%s lists %d of the team's files, want 3", name, len(listed))
		}
		if teams.calls != 1 {
			t.Errorf("%s: the list asked teams %d times, want 1", name, teams.calls)
		}
	}
}
