package application

import (
	"context"
	"errors"
	"testing"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"
	teamsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/sources"
)

// registrar builds a service whose caller a test can shape.
func registrar(t *testing.T, store *fakeStore, caller domain.OrgMembership) *Service {
	t.Helper()
	extractor := &fakeExtractor{provider: "files"}
	return New(Dependencies{
		Store:      store,
		Directory:  &fakeDirectory{membership: caller},
		Extractors: sources.Registry{"files": extractor},
		Clock:      func() time.Time { return testClock },
	}, Config{})
}

func member() domain.OrgMembership {
	return domain.OrgMembership{OrganizationID: "org_1", UserID: "user-1", Role: domain.OrgRoleMember}
}

func organizationDeleted(organizationID string) authv1.OrganizationDeleted {
	return authv1.OrganizationDeleted{OrganizationID: organizationID, DeletedAt: testClock}
}

func registration() RegisterInput {
	return RegisterInput{Provider: "files", Target: map[string]string{"team_id": "team_1"}}
}

// githubRegistrar builds a service whose caller and apps connection a test
// can shape, for the provider-source registration path files never takes.
func githubRegistrar(t *testing.T, store *fakeStore, caller domain.OrgMembership, apps *fakeApps) *Service {
	t.Helper()
	extractor := &fakeExtractor{provider: "github"}
	return New(Dependencies{
		Store:      store,
		Directory:  &fakeDirectory{membership: caller},
		Apps:       apps,
		Extractors: sources.Registry{"github": extractor},
		Clock:      func() time.Time { return testClock },
	}, Config{})
}

// usableConnection is a connection registration accepts: active, for
// github, and the caller's own.
func usableConnection() domain.AppConnection {
	return domain.AppConnection{
		ID: "apc_1", Toolkit: "github", Status: "active", MemberID: "user-1", ComposioAccountID: "ca_1",
	}
}

func githubRegistration() RegisterInput {
	return RegisterInput{ConnectionID: "apc_1", Provider: "github", Target: map[string]string{"repository": "acme/api"}}
}

func TestRegisterSourceStoresTheConnectionAndComposioAccount(t *testing.T) {
	store := newFakeStore()
	apps := &fakeApps{connection: usableConnection()}
	service := githubRegistrar(t, store, member(), apps)

	source, err := service.RegisterSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", githubRegistration())
	if err != nil {
		t.Fatalf("RegisterSource() error = %v", err)
	}
	if source.ConnectionID != "apc_1" || source.ComposioAccountID != "ca_1" {
		t.Fatalf("source = %+v, want the connection and its Composio account", source)
	}
	if source.Scope != domain.ScopeMember || source.MemberID != "user-1" {
		t.Fatalf("source placement = %s %q, want it indexed as the caller's own", source.Scope, source.MemberID)
	}
	if len(apps.calls) != 1 || apps.calls[0] != "apc_1" {
		t.Fatalf("apps was asked for %v, want one call for apc_1", apps.calls)
	}
}

func TestRegisterSourceRefusesAProviderSourceWithNoConnectionID(t *testing.T) {
	store := newFakeStore()
	service := githubRegistrar(t, store, member(), &fakeApps{connection: usableConnection()})

	input := githubRegistration()
	input.ConnectionID = ""
	if _, err := service.RegisterSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", input); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RegisterSource() error = %v, want ErrInvalid", err)
	}
}

func TestRegisterSourceRefusesFilesWithAConnectionID(t *testing.T) {
	// files is the organization's own storage; a connection_id on it can only
	// be a mistake worth catching now rather than silently ignoring.
	store := newFakeStore()
	service := registrar(t, store, member())

	input := registration()
	input.ConnectionID = "apc_1"
	if _, err := service.RegisterSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", input); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RegisterSource() error = %v, want ErrInvalid", err)
	}
}

func TestRegisterSourceRefusesAConnectionForAnotherToolkit(t *testing.T) {
	store := newFakeStore()
	elsewhere := usableConnection()
	elsewhere.Toolkit = "slack"
	service := githubRegistrar(t, store, member(), &fakeApps{connection: elsewhere})

	if _, err := service.RegisterSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", githubRegistration()); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RegisterSource() error = %v, want ErrInvalid", err)
	}
}

func TestRegisterSourceRefusesAConnectionThatIsNotActive(t *testing.T) {
	store := newFakeStore()
	pending := usableConnection()
	pending.Status = "pending"
	service := githubRegistrar(t, store, member(), &fakeApps{connection: pending})

	if _, err := service.RegisterSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", githubRegistration()); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("RegisterSource() error = %v, want ErrConflict", err)
	}
}

func TestRegisterSourceRefusesAConnectionThatIsNotTheCallers(t *testing.T) {
	// A member indexes only through their own connection: apps answering
	// about someone else's is a caller trying to read as them, not a
	// mistake in what was asked for.
	store := newFakeStore()
	someoneElses := usableConnection()
	someoneElses.MemberID = "user-2"
	service := githubRegistrar(t, store, member(), &fakeApps{connection: someoneElses})

	if _, err := service.RegisterSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", githubRegistration()); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("RegisterSource() error = %v, want ErrForbidden", err)
	}
}

func TestRegisterSourceRefusesAProviderSourceWhenAppsIsNotConfigured(t *testing.T) {
	// Without COMPOSIO_API_KEY, mount builds no extractor for github, notion
	// or slack, so this path is defensive: it must still refuse cleanly
	// rather than reach for a nil Apps.
	store := newFakeStore()
	service := New(Dependencies{
		Store:      store,
		Directory:  &fakeDirectory{membership: member()},
		Extractors: sources.Registry{"github": &fakeExtractor{provider: "github"}},
		Clock:      func() time.Time { return testClock },
	}, Config{})

	if _, err := service.RegisterSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", githubRegistration()); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("RegisterSource() error = %v, want ErrProviderUnavailable", err)
	}
}

func TestRegisterSourceIsDueAtOnce(t *testing.T) {
	store := newFakeStore()
	service := registrar(t, store, member())

	source, err := service.RegisterSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", registration())
	if err != nil {
		t.Fatalf("RegisterSource() error = %v", err)
	}
	if source.SyncAt == nil || !source.SyncAt.Equal(testClock) {
		t.Fatalf("SyncAt = %v, want now: a member who just asked should not wait an interval", source.SyncAt)
	}
	if source.Interval != domain.DefaultInterval {
		t.Fatalf("Interval = %s, want the default %s", source.Interval, domain.DefaultInterval)
	}
	if source.Scope != domain.ScopeOrganization {
		t.Fatalf("source = %+v, want it placed on the organization", source)
	}
}

func TestRegisterSourceRefusesAProviderNothingReads(t *testing.T) {
	store := newFakeStore()
	service := registrar(t, store, member())

	input := registration()
	input.Provider = "dropbox"
	_, err := service.RegisterSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", input)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RegisterSource() error = %v, want ErrInvalid", err)
	}
}

func TestRegisterSourceRefusesATargetTheExtractorWouldNotUnderstand(t *testing.T) {
	store := newFakeStore()
	extractor := &fakeExtractor{provider: "files", targetErr: sources.MissingTarget("a team")}
	service := New(Dependencies{
		Store:      store,
		Directory:  &fakeDirectory{membership: member()},
		Extractors: sources.Registry{"files": extractor},
		Clock:      func() time.Time { return testClock },
	}, Config{})

	input := registration()
	input.Target = map[string]string{"branch": "main"}
	_, err := service.RegisterSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", input)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RegisterSource() error = %v, want ErrInvalid", err)
	}
}

func TestRegisterSourceRefusesAnAgent(t *testing.T) {
	// An agent may read what an organization indexed, but choosing what to
	// index is the member's own decision.
	store := newFakeStore()
	agent := member()
	agent.Agent = true
	service := registrar(t, store, agent)

	_, err := service.RegisterSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", registration())
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("RegisterSource() error = %v, want ErrForbidden", err)
	}
}

func TestRegisterSourceRefusesAnIntervalThatWouldHammerAProvider(t *testing.T) {
	store := newFakeStore()
	service := registrar(t, store, member())

	input := registration()
	input.Interval = time.Second
	_, err := service.RegisterSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", input)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RegisterSource() error = %v, want ErrInvalid", err)
	}
}

func TestSourceIsHiddenFromAnotherMember(t *testing.T) {
	store := newFakeStore()
	service := registrar(t, store, member())
	source, err := service.RegisterSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", registration())
	if err != nil {
		t.Fatalf("RegisterSource() error = %v", err)
	}

	stranger := registrar(t, store, domain.OrgMembership{OrganizationID: "org_1", UserID: "user-2", Role: domain.OrgRoleMember})
	if _, err := stranger.Source(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", source.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Source() error = %v, want ErrNotFound: a stranger learns nothing, not even that it exists", err)
	}

	admin := registrar(t, store, domain.OrgMembership{OrganizationID: "org_1", UserID: "user-3", Role: domain.OrgRoleAdmin})
	if _, err := admin.Source(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", source.ID); err != nil {
		t.Fatalf("Source() as an org admin error = %v", err)
	}
}

func TestRemoveSourceRetractsWhatItHeld(t *testing.T) {
	store := newFakeStore()
	service := registrar(t, store, member())
	source, err := service.RegisterSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", registration())
	if err != nil {
		t.Fatalf("RegisterSource() error = %v", err)
	}
	store.documents["k"] = domain.Document{
		ID: "doc_1", OrganizationID: "org_1", SourceID: source.ID, ExternalID: "1",
	}

	if err := service.RemoveSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", source.ID); err != nil {
		t.Fatalf("RemoveSource() error = %v", err)
	}
	if got := store.published(knowledgev1.DocumentRemovedDetailType); got != 1 {
		t.Fatalf("published %d retractions, want 1 so a retrieval pipeline forgets it too", got)
	}
	if store.sources[source.ID].Status != domain.StatusRemoved {
		t.Fatalf("source status = %s, want removed", store.sources[source.ID].Status)
	}
}

func TestRemoveSourceAsksAuthOnce(t *testing.T) {
	// s.Source resolves the caller and s.caller resolved it again was one
	// round trip too many on a path that runs once per removal, not once per
	// page; the caller is now resolved a single time.
	store := newFakeStore()
	directory := &fakeDirectory{membership: member()}
	service := New(Dependencies{
		Store:      store,
		Directory:  directory,
		Extractors: sources.Registry{"files": &fakeExtractor{provider: "files"}},
		Clock:      func() time.Time { return testClock },
	}, Config{})
	source, err := service.RegisterSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", registration())
	if err != nil {
		t.Fatalf("RegisterSource() error = %v", err)
	}

	directory.calls = 0
	if err := service.RemoveSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", source.ID); err != nil {
		t.Fatalf("RemoveSource() error = %v", err)
	}
	if directory.calls != 1 {
		t.Fatalf("RemoveSource() asked auth %d times, want 1", directory.calls)
	}
}

func TestOrganizationDeletedRetiresEverythingItIndexed(t *testing.T) {
	store := newFakeStore()
	service := registrar(t, store, member())
	source, err := service.RegisterSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", registration())
	if err != nil {
		t.Fatalf("RegisterSource() error = %v", err)
	}
	store.documents["k"] = domain.Document{ID: "doc_1", OrganizationID: "org_1", SourceID: source.ID, ExternalID: "1"}

	if err := service.OrganizationDeleted(context.Background(), organizationDeleted("org_1")); err != nil {
		t.Fatalf("OrganizationDeleted() error = %v", err)
	}
	if store.sources[source.ID].Status != domain.StatusRemoved {
		t.Fatalf("source status = %s, want removed", store.sources[source.ID].Status)
	}
	if got := store.published(knowledgev1.DocumentRemovedDetailType); got != 1 {
		t.Fatalf("published %d retractions, want 1", got)
	}
}

func TestConnectionExpiredPausesEveryLiveSourceOnThatConnection(t *testing.T) {
	store := newFakeStore()
	apps := &fakeApps{connection: usableConnection()}
	service := githubRegistrar(t, store, member(), apps)
	source, err := service.RegisterSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", githubRegistration())
	if err != nil {
		t.Fatalf("RegisterSource() error = %v", err)
	}

	if err := service.ConnectionExpired(context.Background(), appsv1.ConnectionExpired{
		ConnectionID: source.ConnectionID, OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github",
	}); err != nil {
		t.Fatalf("ConnectionExpired() error = %v", err)
	}
	read := store.sources[source.ID]
	if read.Status != domain.StatusPaused || read.Reason != knowledgev1.PausedReasonNeedsReconnect {
		t.Fatalf("source after ConnectionExpired = %+v", read)
	}
	if got := store.published(knowledgev1.SourcePausedDetailType); got != 1 {
		t.Fatalf("published %d pause events, want 1", got)
	}
}

func TestConnectionRemovedRetiresSourcesOnThatConnectionAndRetractsTheirDocuments(t *testing.T) {
	store := newFakeStore()
	apps := &fakeApps{connection: usableConnection()}
	service := githubRegistrar(t, store, member(), apps)
	source, err := service.RegisterSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", githubRegistration())
	if err != nil {
		t.Fatalf("RegisterSource() error = %v", err)
	}
	store.documents["k"] = domain.Document{ID: "doc_1", OrganizationID: "org_1", SourceID: source.ID, ExternalID: "1"}

	if err := service.ConnectionRemoved(context.Background(), appsv1.ConnectionRemoved{
		ConnectionID: source.ConnectionID, OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github", Reason: "removed_by_member",
	}); err != nil {
		t.Fatalf("ConnectionRemoved() error = %v", err)
	}
	if store.sources[source.ID].Status != domain.StatusRemoved {
		t.Fatalf("source status = %s, want removed", store.sources[source.ID].Status)
	}
	if got := store.published(knowledgev1.DocumentRemovedDetailType); got != 1 {
		t.Fatalf("published %d retractions, want 1", got)
	}
	// A connection someone disconnected is not one whose credential expired,
	// and a member asking why their documents went deserves the difference.
	if reasons := store.publishedReasons(knowledgev1.DocumentRemovedDetailType); len(reasons) != 1 ||
		reasons[0] != knowledgev1.RemovedReasonConnectionRemoved {
		t.Fatalf("retraction reasons = %v, want %q", reasons, knowledgev1.RemovedReasonConnectionRemoved)
	}
}

func TestTeamDeletedRetiresThatTeamsSourcesAndRetractsTheirDocuments(t *testing.T) {
	store := newFakeStore()
	service := registrar(t, store, member())
	store.sources["src_gone"] = domain.Source{ID: "src_gone", OrganizationID: "org_1", Scope: domain.ScopeTeam, TeamID: "team_gone", Status: domain.StatusActive}
	store.sources["src_kept"] = domain.Source{ID: "src_kept", OrganizationID: "org_1", Scope: domain.ScopeTeam, TeamID: "team_kept", Status: domain.StatusActive}
	store.documents["k"] = domain.Document{ID: "doc_1", OrganizationID: "org_1", SourceID: "src_gone", ExternalID: "1"}

	if err := service.TeamDeleted(context.Background(), teamsv1.TeamDeleted{TeamID: "team_gone", OrganizationID: "org_1"}); err != nil {
		t.Fatalf("TeamDeleted() error = %v", err)
	}
	if store.sources["src_gone"].Status != domain.StatusRemoved || store.sources["src_kept"].Status != domain.StatusActive {
		t.Fatalf("statuses = %s and %s, want only the deleted team's source removed",
			store.sources["src_gone"].Status, store.sources["src_kept"].Status)
	}
	if reasons := store.publishedReasons(knowledgev1.DocumentRemovedDetailType); len(reasons) != 1 ||
		reasons[0] != knowledgev1.RemovedReasonTeamDeleted {
		t.Fatalf("retraction reasons = %v, want %q", reasons, knowledgev1.RemovedReasonTeamDeleted)
	}
}

func TestAlreadyHandledIsTrueOnlyAfterMarkHandled(t *testing.T) {
	// Retiring a source retracts its documents, which writes events a
	// retrieval pipeline acts on, so a redelivery must not do it twice.
	// AlreadyHandled must say so on its own, without recording anything,
	// since the caller decides when a handler has actually succeeded.
	store := newFakeStore()
	service := registrar(t, store, member())

	already, err := service.AlreadyHandled(context.Background(), "evt_1")
	if err != nil || already {
		t.Fatalf("AlreadyHandled(before) = %v, %v, want false", already, err)
	}
	if err := service.MarkHandled(context.Background(), "evt_1", "auth.organization.deleted.v1"); err != nil {
		t.Fatalf("MarkHandled() error = %v", err)
	}
	already, err = service.AlreadyHandled(context.Background(), "evt_1")
	if err != nil || !already {
		t.Fatalf("AlreadyHandled(after) = %v, %v, want true", already, err)
	}
}
