package application

import (
	"context"
	"errors"
	"testing"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/sources"
)

// connectedService is knowledge as it runs when an app is connected: able to
// act as the member, able to ask apps what their connection holds, and able
// to read github.
func connectedService(t *testing.T, store *fakeStore, apps *fakeApps) (*Service, *fakeGrants) {
	t.Helper()
	grants := &fakeGrants{token: "member-token"}
	service := New(Dependencies{
		Store:      store,
		Directory:  &fakeDirectory{membership: member()},
		Apps:       apps,
		Grants:     grants,
		Extractors: sources.Registry{"github": &fakeExtractor{provider: "github"}},
		Clock:      func() time.Time { return testClock },
	}, Config{})
	return service, grants
}

func connected() appsv1.ConnectionConnected {
	return appsv1.ConnectionConnected{
		ConnectionID: "apc_1", OrganizationID: "org_1", MemberID: "user-1",
		Toolkit: "github", ConnectedAt: testClock,
	}
}

func repositories(names ...string) []domain.AppTarget {
	targets := make([]domain.AppTarget, 0, len(names))
	for _, name := range names {
		targets = append(targets, domain.AppTarget{
			Kind: "repository", ID: name, Name: name, Target: map[string]string{"repository": name},
		})
	}
	return targets
}

// TestConnectingAnAppIndexesWhatItHolds is the point: a member connects
// GitHub and their repositories become searchable, without them naming one.
func TestConnectingAnAppIndexesWhatItHolds(t *testing.T) {
	store := newFakeStore()
	apps := &fakeApps{connection: usableConnection(), targets: repositories("acme/api", "acme/web")}
	service, grants := connectedService(t, store, apps)

	if err := service.ConnectionConnected(context.Background(), connected()); err != nil {
		t.Fatal(err)
	}
	if len(store.sources) != 2 {
		t.Fatalf("sources = %d, want one per repository", len(store.sources))
	}
	indexed := map[string]bool{}
	for _, source := range store.sources {
		indexed[source.Target["repository"]] = true
		if source.ConnectionID != "apc_1" || source.Provider != "github" {
			t.Fatalf("source = %+v", source)
		}
		// Registered as the member, so it is theirs and reads as them.
		if source.CreatedBy != "user-1" {
			t.Fatalf("source was created by %q, want the member who connected", source.CreatedBy)
		}
	}
	if !indexed["acme/api"] || !indexed["acme/web"] {
		t.Fatalf("indexed = %v", indexed)
	}
	// It acted as the member rather than as itself.
	if len(grants.members) == 0 || grants.members[0] != "org_1/user-1" {
		t.Fatalf("minted tokens for %v, want the member who connected", grants.members)
	}
}

// TestIndexingOnConnectIsBounded keeps connecting an app from committing a
// member to a scheduled pass against four hundred repositories.
func TestIndexingOnConnectIsBounded(t *testing.T) {
	store := newFakeStore()
	var many []string
	for index := range autoRegisterLimit + 5 {
		many = append(many, "acme/repo-"+string(rune('a'+index)))
	}
	apps := &fakeApps{connection: usableConnection(), targets: repositories(many...)}
	service, _ := connectedService(t, store, apps)

	if err := service.ConnectionConnected(context.Background(), connected()); err != nil {
		t.Fatal(err)
	}
	if len(store.sources) != autoRegisterLimit {
		t.Fatalf("sources = %d, want the ceiling of %d", len(store.sources), autoRegisterLimit)
	}
}

// TestAMemberWhoNeverGrantedGetsNothingIndexed covers the one thing this
// cannot do without: acting as them. It is not an error — they connected an
// app, which worked — so the event is done rather than retried.
func TestAMemberWhoNeverGrantedGetsNothingIndexed(t *testing.T) {
	store := newFakeStore()
	apps := &fakeApps{connection: usableConnection(), targets: repositories("acme/api")}
	service, grants := connectedService(t, store, apps)
	grants.err = errors.New("no grant")

	if err := service.ConnectionConnected(context.Background(), connected()); err != nil {
		t.Fatalf("a missing grant was reported as a failure: %v", err)
	}
	if len(store.sources) != 0 {
		t.Fatalf("sources = %d, want none", len(store.sources))
	}
	// It did not go on to ask apps anything as nobody.
	for _, call := range apps.calls {
		if call == "targets:apc_1" {
			t.Fatal("asked apps for targets without a token")
		}
	}
}

// TestAnAppBeingUnreachableIsWorthAnotherDelivery separates what a retry may
// fix from what it cannot, since the bus redelivers what is returned as an
// error.
func TestAnAppBeingUnreachableIsWorthAnotherDelivery(t *testing.T) {
	store := newFakeStore()
	apps := &fakeApps{connection: usableConnection(), targetsErr: domain.ErrProviderUnavailable}
	service, _ := connectedService(t, store, apps)

	if err := service.ConnectionConnected(context.Background(), connected()); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want it reported so the bus redelivers", err)
	}

	// A refusal that will not change is not worth redelivering.
	apps.targetsErr = domain.ErrNotFound
	if err := service.ConnectionConnected(context.Background(), connected()); err != nil {
		t.Fatalf("a refusal no retry fixes was reported as a failure: %v", err)
	}
}

// TestIndexingOnConnectRunsOnceEvenIfTheEventArrivesTwice covers the bus
// delivering at least once. The unique index refuses the second, which is
// the same answer as having done it.
func TestIndexingOnConnectRunsOnceEvenIfTheEventArrivesTwice(t *testing.T) {
	store := newFakeStore()
	apps := &fakeApps{connection: usableConnection(), targets: repositories("acme/api")}
	service, _ := connectedService(t, store, apps)

	for range 3 {
		if err := service.ConnectionConnected(context.Background(), connected()); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.sources) != 1 {
		t.Fatalf("sources = %d, want one however often the event arrives", len(store.sources))
	}
}

// TestAnAppWithNothingToIndexIsNotAProblem covers an app that delivers events
// and holds nothing a pass could read.
func TestAnAppWithNothingToIndexIsNotAProblem(t *testing.T) {
	store := newFakeStore()
	apps := &fakeApps{connection: usableConnection()}
	service, _ := connectedService(t, store, apps)

	event := connected()
	event.Toolkit = "linear"
	if err := service.ConnectionConnected(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(store.sources) != 0 {
		t.Fatalf("sources = %d, want none", len(store.sources))
	}
}

// TestIndexingOnConnectNeedsBothHalves keeps a deployment without a way to
// act as a member from failing on every connection.
func TestIndexingOnConnectNeedsBothHalves(t *testing.T) {
	store := newFakeStore()
	service := New(Dependencies{
		Store:      store,
		Directory:  &fakeDirectory{membership: member()},
		Apps:       &fakeApps{connection: usableConnection(), targets: repositories("acme/api")},
		Extractors: sources.Registry{"github": &fakeExtractor{provider: "github"}},
		Clock:      func() time.Time { return testClock },
	}, Config{})

	if err := service.ConnectionConnected(context.Background(), connected()); err != nil {
		t.Fatalf("a deployment that cannot act as a member reported a failure: %v", err)
	}
	if len(store.sources) != 0 {
		t.Fatalf("sources = %d, want none", len(store.sources))
	}
}

func TestAnIncompleteConnectedEventIsRefused(t *testing.T) {
	store := newFakeStore()
	service, _ := connectedService(t, store, &fakeApps{connection: usableConnection()})

	event := connected()
	event.MemberID = ""
	if err := service.ConnectionConnected(context.Background(), event); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("error = %v, want invalid", err)
	}
}
