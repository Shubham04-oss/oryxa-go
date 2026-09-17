package application

import (
	"context"
	"errors"
	"testing"
	"time"

	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

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
