package domain

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	connectionseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/connections/v1"
)

func TestPlace(t *testing.T) {
	placement, err := Place(ScopeMember, "", "user-1")
	if err != nil || placement != (Placement{Scope: ScopeMember, MemberID: "user-1"}) {
		t.Fatalf("member placement = %+v %v", placement, err)
	}
	placement, err = Place(ScopeTeam, " team_1 ", "user-1")
	if err != nil || placement != (Placement{Scope: ScopeTeam, TeamID: "team_1"}) {
		t.Fatalf("team placement = %+v %v", placement, err)
	}
	placement, err = Place(ScopeOrganization, "", "user-1")
	if err != nil || placement != (Placement{Scope: ScopeOrganization}) {
		t.Fatalf("organization placement = %+v %v", placement, err)
	}
	for _, invalid := range []struct {
		scope  Scope
		teamID string
		caller string
	}{
		{ScopeMember, "team_1", "user-1"},
		{ScopeMember, "", ""},
		{ScopeTeam, " ", "user-1"},
		{ScopeOrganization, "team_1", "user-1"},
		{Scope("everyone"), "", "user-1"},
	} {
		if _, err := Place(invalid.scope, invalid.teamID, invalid.caller); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Place(%q, %q, %q) error = %v", invalid.scope, invalid.teamID, invalid.caller, err)
		}
	}
	if scope, err := ParseScope(""); err != nil || scope != ScopeMember {
		t.Fatalf("ParseScope(\"\") = %q %v", scope, err)
	}
	if _, err := ParseScope("everyone"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ParseScope(everyone) error = %v", err)
	}
}

func TestParseUsage(t *testing.T) {
	if usage, err := ParseUsage("", ScopeTeam); err != nil || usage != UsageMembers {
		t.Fatalf("default usage = %q %v", usage, err)
	}
	if usage, err := ParseUsage("admins", ScopeOrganization); err != nil || usage != UsageAdmins {
		t.Fatalf("organization admins usage = %q %v", usage, err)
	}
	if _, err := ParseUsage("admins", ScopeTeam); !errors.Is(err, ErrInvalid) {
		t.Fatalf("team admins usage error = %v", err)
	}
	if _, err := ParseUsage("owners", ScopeOrganization); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown usage error = %v", err)
	}
}

func TestCredentialTypes(t *testing.T) {
	if err := CredentialOAuth2ClientCredentials.Fits(ScopeMember); !errors.Is(err, ErrInvalid) {
		t.Fatalf("member client credentials error = %v", err)
	}
	for _, scope := range []Scope{ScopeTeam, ScopeOrganization} {
		if err := CredentialOAuth2ClientCredentials.Fits(scope); err != nil {
			t.Fatalf("client credentials on %s error = %v", scope, err)
		}
	}
	if err := CredentialType("password").Fits(ScopeTeam); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown credential type error = %v", err)
	}
	if !CredentialOAuth2.Authorizes() || !CredentialInstallation.Authorizes() ||
		CredentialAPIKey.Authorizes() || CredentialOAuth2ClientCredentials.Authorizes() {
		t.Fatal("Authorizes() is wrong")
	}
	if !CredentialAPIKey.KeyBased() || !CredentialBearer.KeyBased() || !CredentialBasic.KeyBased() || CredentialOAuth2.KeyBased() {
		t.Fatal("KeyBased() is wrong")
	}
}

func TestLabel(t *testing.T) {
	if label, err := Label("  Work Gmail "); err != nil || label != "Work Gmail" {
		t.Fatalf("Label() = %q %v", label, err)
	}
	if _, err := Label(strings.Repeat("a", MaxLabelLength+1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("long label error = %v", err)
	}
	if _, err := Label("line\nbreak"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("multi-line label error = %v", err)
	}
}

func membership(userID string, role OrgRole) OrgMembership {
	return OrgMembership{OrganizationID: "org_1", UserID: userID, Role: role}
}

func TestMayCreate(t *testing.T) {
	member := Caller{Membership: membership("user-1", OrgRoleMember)}
	admin := Caller{Membership: membership("user-2", OrgRoleAdmin)}
	teamMember := Caller{Membership: membership("user-3", OrgRoleMember), Team: TeamAccess{TeamID: "team_1", Member: true}}
	teamManager := Caller{Membership: membership("user-4", OrgRoleMember), Team: TeamAccess{TeamID: "team_1", Member: true, CanManage: true}}
	adminWithTeam := Caller{Membership: membership("user-2", OrgRoleAdmin), Team: TeamAccess{TeamID: "team_1", CanManage: true}}
	team := Placement{Scope: ScopeTeam, TeamID: "team_1"}

	for _, tc := range []struct {
		name      string
		caller    Caller
		placement Placement
		want      error
	}{
		{"member connects for themselves", member, Placement{Scope: ScopeMember, MemberID: "user-1"}, nil},
		{"member cannot connect for someone else", member, Placement{Scope: ScopeMember, MemberID: "user-9"}, ErrForbidden},
		{"a team teams did not answer for is concealed", admin, team, ErrNotFound},
		{"team member cannot create", teamMember, team, ErrForbidden},
		{"team manager creates", teamManager, team, nil},
		{"organization admin creates on a visible team", adminWithTeam, team, nil},
		{"member cannot create an organization connection", member, Placement{Scope: ScopeOrganization}, ErrForbidden},
		{"admin creates an organization connection", admin, Placement{Scope: ScopeOrganization}, nil},
		{"unknown scope", admin, Placement{Scope: Scope("everyone")}, ErrInvalid},
	} {
		if err := MayCreate(tc.caller, tc.placement); !errors.Is(err, tc.want) {
			t.Fatalf("%s: MayCreate() error = %v, want %v", tc.name, err, tc.want)
		}
	}
	if err := MayManageApps(member); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member MayManageApps() error = %v", err)
	}
	if err := MayManageApps(Caller{Membership: membership("user-5", OrgRoleOwner)}); err != nil {
		t.Fatalf("owner MayManageApps() error = %v", err)
	}
}

func TestConnectionAccess(t *testing.T) {
	owner := Caller{Membership: membership("user-1", OrgRoleMember)}
	other := Caller{Membership: membership("user-2", OrgRoleMember)}
	admin := Caller{Membership: membership("user-3", OrgRoleAdmin)}
	adminWithTeam := Caller{Membership: membership("user-3", OrgRoleAdmin), Team: TeamAccess{TeamID: "team_1", CanManage: true}}
	teamMember := Caller{Membership: membership("user-4", OrgRoleMember), Team: TeamAccess{TeamID: "team_1", Member: true}}
	teamManager := Caller{Membership: membership("user-5", OrgRoleMember), Team: TeamAccess{TeamID: "team_1", Member: true, CanManage: true}}
	otherTeam := Caller{Membership: membership("user-6", OrgRoleMember), Team: TeamAccess{TeamID: "team_2", Member: true, CanManage: true}}
	outsider := Caller{Membership: OrgMembership{OrganizationID: "org_2", UserID: "user-1", Role: OrgRoleOwner}}

	personal := Connection{OrganizationID: "org_1", Scope: ScopeMember, MemberID: "user-1"}
	shared := Connection{OrganizationID: "org_1", Scope: ScopeTeam, TeamID: "team_1"}
	organization := Connection{OrganizationID: "org_1", Scope: ScopeOrganization, Usage: UsageMembers}
	adminsOnly := Connection{OrganizationID: "org_1", Scope: ScopeOrganization, Usage: UsageAdmins}

	for _, tc := range []struct {
		name       string
		check      func(Caller, Connection) error
		caller     Caller
		connection Connection
		want       error
	}{
		{"owner uses their connection", MayUse, owner, personal, nil},
		{"owner manages their connection", MayManage, owner, personal, nil},
		{"another member cannot see it", MaySee, other, personal, ErrNotFound},
		{"admin sees it", MaySee, admin, personal, nil},
		{"admin cannot use it", MayUse, admin, personal, ErrForbidden},
		{"admin cannot reconnect it", MayManage, admin, personal, ErrForbidden},
		{"admin may disconnect it", MayDisconnect, admin, personal, nil},
		{"another member cannot disconnect it", MayDisconnect, other, personal, ErrNotFound},
		{"team member uses a team connection", MayUse, teamMember, shared, nil},
		{"team member cannot manage it", MayManage, teamMember, shared, ErrForbidden},
		{"team manager manages it", MayManage, teamManager, shared, nil},
		{"team manager disconnects it", MayDisconnect, teamManager, shared, nil},
		{"a manager of another team cannot see it", MaySee, otherTeam, shared, ErrNotFound},
		{"member off the team cannot see it", MaySee, other, shared, ErrNotFound},
		{"admin needs teams to answer", MaySee, admin, shared, ErrNotFound},
		{"admin with teams' answer manages it", MayManage, adminWithTeam, shared, nil},
		{"member uses an organization connection", MayUse, other, organization, nil},
		{"member cannot manage it", MayManage, other, organization, ErrForbidden},
		{"member cannot use an admins-only connection", MayUse, other, adminsOnly, ErrForbidden},
		{"member still sees an admins-only connection", MaySee, other, adminsOnly, nil},
		{"admin uses an admins-only connection", MayUse, admin, adminsOnly, nil},
		{"another organization cannot see anything", MaySee, outsider, organization, ErrNotFound},
	} {
		if err := tc.check(tc.caller, tc.connection); !errors.Is(err, tc.want) {
			t.Fatalf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestMissingScopes(t *testing.T) {
	const (
		drive         = "https://www.googleapis.com/auth/drive"
		driveReadonly = "https://www.googleapis.com/auth/drive.readonly"
	)
	implications := Implications{driveReadonly: {drive}}
	if missing := MissingScopes([]string{driveReadonly}, []string{drive}, []string{drive}, implications); len(missing) != 0 {
		t.Fatalf("implied scope missing = %v", missing)
	}
	// The admin narrowed the app to read-only after the member granted full access.
	if missing := MissingScopes([]string{driveReadonly}, []string{drive}, []string{driveReadonly}, implications); len(missing) != 0 {
		t.Fatalf("read-only on a narrowed app missing = %v", missing)
	}
	if missing := MissingScopes([]string{drive}, []string{drive}, []string{driveReadonly}, implications); !slices.Equal(missing, []string{drive}) {
		t.Fatalf("full access on a narrowed app missing = %v", missing)
	}
	if missing := MissingScopes([]string{" b ", "a", "a"}, nil, []string{"a", "b"}, nil); !slices.Equal(missing, []string{"a", "b"}) {
		t.Fatalf("ungranted scopes missing = %v", missing)
	}
	err := error(ScopeRequiredError{Missing: []string{"a", "b"}})
	if !errors.Is(err, ErrScopeRequired) || err.Error() != "scope required: a b" {
		t.Fatalf("ScopeRequiredError = %v", err)
	}
}

func TestAppScopes(t *testing.T) {
	ceiling := []string{"read", "write", "comments:create"}
	required := []string{"read"}
	if scopes, err := AppScopes(nil, ceiling, required); err != nil || !slices.Equal(scopes, []string{"read"}) {
		t.Fatalf("default scopes = %v %v", scopes, err)
	}
	if scopes, err := AppScopes([]string{"write", " read ", "read"}, ceiling, required); err != nil || !slices.Equal(scopes, []string{"read", "write"}) {
		t.Fatalf("requested scopes = %v %v", scopes, err)
	}
	if _, err := AppScopes([]string{"write"}, ceiling, required); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing required scope error = %v", err)
	}
	if _, err := AppScopes([]string{"read", "admin"}, ceiling, required); !errors.Is(err, ErrInvalid) {
		t.Fatalf("scope beyond the ceiling error = %v", err)
	}
}

func TestConnectionLifecycle(t *testing.T) {
	at := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	later := at.Add(time.Hour)
	active := Connection{ID: "con_1", Status: StatusActive, Default: true,
		ProviderAccount: ProviderAccount{ID: "account-1", Name: "Asha"}, Scopes: []string{"read"}, StatusChangedAt: at}

	broken, changed, err := active.MarkNeedsReconnect(ReconnectRefreshRejected, later)
	if err != nil || !changed || broken.Status != StatusNeedsReconnect || broken.Reason != "refresh_rejected" || !broken.StatusChangedAt.Equal(later) {
		t.Fatalf("MarkNeedsReconnect() = %+v %v %v", broken, changed, err)
	}
	if _, changed, err := broken.MarkNeedsReconnect(ReconnectUnauthorized, later); err != nil || changed {
		t.Fatalf("repeated MarkNeedsReconnect() = %v %v", changed, err)
	}
	if _, _, err := active.MarkNeedsReconnect(ReconnectReason("expired"), later); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown reconnect reason error = %v", err)
	}

	if _, err := broken.Reconnect(ProviderAccount{ID: "account-2"}, nil, later); !errors.Is(err, ErrAccountChanged) {
		t.Fatalf("different account error = %v", err)
	}
	restored, err := broken.Reconnect(ProviderAccount{ID: "account-1"}, []string{"write", "read"}, later)
	if err != nil || restored.Status != StatusActive || restored.Reason != "" ||
		!slices.Equal(restored.Scopes, []string{"read", "write"}) || restored.ProviderAccount.Name != "Asha" {
		t.Fatalf("Reconnect() = %+v %v", restored, err)
	}

	gone, changed, err := restored.Disconnect(DisconnectMemberLeft, later)
	if err != nil || !changed || gone.Status != StatusDisconnected || gone.Reason != "member_left" || gone.Default {
		t.Fatalf("Disconnect() = %+v %v %v", gone, changed, err)
	}
	if _, changed, err := gone.Disconnect(DisconnectOrganizationDeleted, later); err != nil || changed {
		t.Fatalf("repeated Disconnect() = %v %v", changed, err)
	}
	if _, err := gone.Reconnect(ProviderAccount{ID: "account-1"}, nil, later); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Reconnect() after disconnect error = %v", err)
	}
	if _, _, err := gone.MarkNeedsReconnect(ReconnectUnauthorized, later); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkNeedsReconnect() after disconnect error = %v", err)
	}
	if _, _, err := active.Disconnect(DisconnectReason("bored"), later); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown disconnect reason error = %v", err)
	}
}

func TestIssuable(t *testing.T) {
	app := App{ID: "app_1", OrganizationID: "org_1", Status: AppStatusActive}
	disabled := app
	disabled.Status = AppStatusDisabled
	active := Connection{AppID: "app_1", OrganizationID: "org_1", Status: StatusActive}
	broken := active
	broken.Status = StatusNeedsReconnect
	gone := active
	gone.Status = StatusDisconnected
	deletedAt := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	shredded := gone
	shredded.CredentialsDeletedAt = &deletedAt

	for _, tc := range []struct {
		name       string
		connection Connection
		app        App
		purpose    Purpose
		holdOpen   bool
		want       error
	}{
		{"active action", active, app, PurposeAction, false, nil},
		{"disabled app blocks an action", active, disabled, PurposeAction, false, ErrAppDisabled},
		{"disabled app still allows cleanup", active, disabled, PurposeCleanup, false, nil},
		{"needs reconnect blocks a trigger", broken, app, PurposeTrigger, false, ErrNeedsReconnect},
		{"needs reconnect still allows cleanup", broken, app, PurposeCleanup, false, nil},
		{"disconnected blocks a heartbeat", gone, app, PurposeHeartbeat, true, ErrNotFound},
		{"disconnected with an open hold allows cleanup", gone, app, PurposeCleanup, true, nil},
		{"disconnected without a hold blocks cleanup", gone, app, PurposeCleanup, false, ErrNotFound},
		{"deleted credentials block cleanup", shredded, app, PurposeCleanup, true, ErrNotFound},
		{"another app", active, App{ID: "app_2", OrganizationID: "org_1", Status: AppStatusActive}, PurposeAction, false, ErrNotFound},
		{"unknown purpose", active, app, Purpose("export"), false, ErrInvalid},
	} {
		if err := Issuable(tc.connection, tc.app, tc.purpose, tc.holdOpen); !errors.Is(err, tc.want) {
			t.Fatalf("%s: Issuable() error = %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestHoldOpen(t *testing.T) {
	at := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	gone := Connection{Status: StatusDisconnected, StatusChangedAt: at}
	if !HoldOpen(gone, 1, at.Add(6*24*time.Hour)) {
		t.Fatal("a hold within MaxHold is closed")
	}
	if HoldOpen(gone, 1, at.Add(MaxHold)) {
		t.Fatal("a hold past MaxHold is open")
	}
	if HoldOpen(gone, 0, at) {
		t.Fatal("a connection nobody holds is open")
	}
	if HoldOpen(Connection{Status: StatusActive, StatusChangedAt: at}, 1, at) {
		t.Fatal("an active connection counts as held")
	}
}

func TestValuesMatchEventContracts(t *testing.T) {
	for got, want := range map[string]string{
		string(ReconnectRefreshRejected):      connectionseventsv1.NeedsReconnectReasonRefreshRejected,
		string(ReconnectUnauthorized):         connectionseventsv1.NeedsReconnectReasonUnauthorized,
		string(ReconnectAccountChanged):       connectionseventsv1.NeedsReconnectReasonAccountChanged,
		string(ReconnectClientChanged):        connectionseventsv1.NeedsReconnectReasonClientChanged,
		string(ReconnectInstallationRemoved):  connectionseventsv1.NeedsReconnectReasonInstallationRemoved,
		string(ReconnectCredentialRejected):   connectionseventsv1.NeedsReconnectReasonCredentialRejected,
		string(DisconnectRequested):           connectionseventsv1.DisconnectedReasonDisconnected,
		string(DisconnectMemberLeft):          connectionseventsv1.DisconnectedReasonMemberLeft,
		string(DisconnectTeamDeleted):         connectionseventsv1.DisconnectedReasonTeamDeleted,
		string(DisconnectOrganizationDeleted): connectionseventsv1.DisconnectedReasonOrganizationDeleted,
		string(DisconnectAppDeleted):          connectionseventsv1.DisconnectedReasonAppDeleted,
		string(HealthUnknown):                 connectionseventsv1.HealthUnknown,
		string(HealthHealthy):                 connectionseventsv1.HealthHealthy,
		string(HealthDegraded):                connectionseventsv1.HealthDegraded,
	} {
		if got != want {
			t.Fatalf("domain value %q does not match contract value %q", got, want)
		}
	}
}
