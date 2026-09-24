package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	filesv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/files/v1"
	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"
	teamsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
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
		Store: store,
		// Auth answers every token a standing grant redeems to as an
		// agent's, so the fake does too.
		Directory:  &fakeDirectory{membership: member(), agentToken: "member-token"},
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
	grants.err = domain.ErrGrantRequired

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

// TestAuthNotAnsweringIsWorthAnotherDelivery keeps a moment's outage at auth
// from costing a member their indexing for good: only a member who has not
// granted, or a deployment that may not ask, ends the event.
func TestAuthNotAnsweringIsWorthAnotherDelivery(t *testing.T) {
	store := newFakeStore()
	apps := &fakeApps{connection: usableConnection(), targets: repositories("acme/api")}
	service, grants := connectedService(t, store, apps)
	grants.err = fmt.Errorf("mint a token: %w", domain.ErrProviderUnavailable)

	if err := service.ConnectionConnected(context.Background(), connected()); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want it reported so the bus redelivers", err)
	}
	files, fileGrants := filesService(t, store)
	fileGrants.err = grants.err
	if err := files.FileStored(context.Background(), storedFile("organization")); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("FileStored error = %v, want it reported so the bus redelivers", err)
	}

	// A deployment that may not act as members is configuration, not an
	// outage: reported, and done.
	grants.err = fmt.Errorf("auth refused this service: %w", domain.ErrForbidden)
	if err := service.ConnectionConnected(context.Background(), connected()); err != nil {
		t.Fatalf("error = %v, want none", err)
	}
	if len(store.sources) != 0 {
		t.Fatalf("sources = %d, want none", len(store.sources))
	}
}

// TestAGrantThatActsAsSomeoneElseRegistersNothing: whatever the token turns
// out to be, a source is only ever registered as the member it was for.
func TestAGrantThatActsAsSomeoneElseRegistersNothing(t *testing.T) {
	store := newFakeStore()
	apps := &fakeApps{connection: usableConnection(), targets: repositories("acme/api")}
	service, _ := connectedService(t, store, apps)
	event := connected()
	event.MemberID = "user-2"

	if err := service.ConnectionConnected(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(store.sources) != 0 {
		t.Fatalf("sources = %d, want none registered as another member", len(store.sources))
	}
}

// TestATargetAMemberRemovedIsNotIndexedAgain: removing a repository's source
// is the member saying no, and the connection announcing itself again does
// not undo that.
func TestATargetAMemberRemovedIsNotIndexedAgain(t *testing.T) {
	store := newFakeStore()
	store.sources["src_removed"] = domain.Source{
		ID: "src_removed", OrganizationID: "org_1", Provider: "github", ConnectionID: "apc_1",
		Target: map[string]string{"repository": "acme/api"}, Scope: domain.ScopeMember, MemberID: "user-1",
		Status: domain.StatusRemoved, CreatedBy: "user-1",
	}
	apps := &fakeApps{connection: usableConnection(), targets: repositories("acme/api", "acme/web")}
	service, _ := connectedService(t, store, apps)

	if err := service.ConnectionConnected(context.Background(), connected()); err != nil {
		t.Fatal(err)
	}
	live := 0
	for _, source := range store.sources {
		if source.Status == domain.StatusRemoved {
			continue
		}
		live++
		if source.Target["repository"] != "acme/web" {
			t.Fatalf("registered %v again, which the member removed", source.Target)
		}
	}
	if live != 1 {
		t.Fatalf("live sources = %d, want acme/web alone", live)
	}
}

// TestTheCeilingCountsWhatIsAlreadyIndexed keeps a redelivery, or a
// connection whose targets came back in another order, from indexing a
// second ten.
func TestTheCeilingCountsWhatIsAlreadyIndexed(t *testing.T) {
	store := newFakeStore()
	for index := range autoRegisterLimit - 2 {
		id := fmt.Sprintf("src_%d", index)
		store.sources[id] = domain.Source{
			ID: id, OrganizationID: "org_1", Provider: "github", ConnectionID: "apc_1",
			Target: map[string]string{"repository": fmt.Sprintf("acme/old-%d", index)},
			Scope:  domain.ScopeMember, MemberID: "user-1", Status: domain.StatusActive, CreatedBy: "user-1",
		}
	}
	apps := &fakeApps{connection: usableConnection(), targets: repositories("acme/a", "acme/b", "acme/c", "acme/d")}
	service, _ := connectedService(t, store, apps)

	if err := service.ConnectionConnected(context.Background(), connected()); err != nil {
		t.Fatal(err)
	}
	if len(store.sources) != autoRegisterLimit {
		t.Fatalf("sources = %d, want the ceiling of %d counting what was there", len(store.sources), autoRegisterLimit)
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

// TestProviderForGoogleDriveAndGmail covers the two toolkits this pass adds:
// spelled the same as their knowledge provider, same as github, notion and
// slack already were, so connecting either indexes what it holds without a
// member naming anything.
func TestProviderForGoogleDriveAndGmail(t *testing.T) {
	for _, toolkit := range []string{"googledrive", "gmail"} {
		t.Run(toolkit, func(t *testing.T) {
			if got := providerFor(toolkit); got != toolkit {
				t.Fatalf("providerFor(%q) = %q, want %q", toolkit, got, toolkit)
			}
		})
	}
}

// TestConnectingGoogleDriveIndexesWhatItHolds mirrors
// TestConnectingAnAppIndexesWhatItHolds for the toolkit whose default target
// is the whole of the member's Drive: an empty target map, not a repository
// name, is what a connection's own targets listing hands back for it.
func TestConnectingGoogleDriveIndexesWhatItHolds(t *testing.T) {
	store := newFakeStore()
	apps := &fakeApps{
		connection: domain.AppConnection{ID: "apc_1", Toolkit: "googledrive", Status: "active", MemberID: "user-1", ComposioAccountID: "ca_1"},
		targets:    []domain.AppTarget{{Kind: "drive", ID: "root", Name: "My Drive", Target: map[string]string{}}},
	}
	grants := &fakeGrants{token: "member-token"}
	service := New(Dependencies{
		Store:      store,
		Directory:  &fakeDirectory{membership: member(), agentToken: "member-token"},
		Apps:       apps,
		Grants:     grants,
		Extractors: sources.Registry{"googledrive": &fakeExtractor{provider: "googledrive"}},
		Clock:      func() time.Time { return testClock },
	}, Config{})

	event := connected()
	event.Toolkit = "googledrive"
	if err := service.ConnectionConnected(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(store.sources) != 1 {
		t.Fatalf("sources = %d, want the one Drive source", len(store.sources))
	}
	for _, source := range store.sources {
		if source.Provider != "googledrive" || source.ConnectionID != "apc_1" {
			t.Fatalf("source = %+v", source)
		}
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

// A trigger says the app changed something. What it carries is the app's own
// payload, in a shape that differs per trigger; what it means is the same
// whatever the shape, so the sources on that connection are swept now rather
// than at their next hour.

// hastenable is a live source due in an hour, the way an automatically
// registered one is.
func (store *fakeStore) hastenable(t *testing.T, id, connectionID string, lastSynced *time.Time) domain.Source {
	t.Helper()
	due := testClock.Add(time.Hour)
	source := domain.Source{
		ID: id, OrganizationID: "org_1", ConnectionID: connectionID, Provider: "github",
		Target: map[string]string{"repository": "acme/" + id}, Status: domain.StatusActive,
		Interval: time.Hour, SyncAt: &due, LastSyncedAt: lastSynced, CreatedBy: "user-1",
	}
	store.sources[id] = source
	return source
}

func TestATriggerSweepsItsConnectionsSourcesNow(t *testing.T) {
	store := newFakeStore()
	store.hastenable(t, "src_1", "apc_1", nil)
	store.hastenable(t, "src_2", "apc_1", nil)
	// Another connection's source is not this trigger's business.
	store.hastenable(t, "src_3", "apc_2", nil)
	service, _ := connectedService(t, store, &fakeApps{connection: usableConnection()})

	err := service.TriggerReceived(context.Background(), appsv1.TriggerReceived{
		TriggerID: "apt_1", ConnectionID: "apc_1", OrganizationID: "org_1", MemberID: "user-1",
		Toolkit: "github", Trigger: "GITHUB_ISSUE_CREATED", ReceivedAt: testClock,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"src_1", "src_2"} {
		if due := store.sources[id].SyncAt; due == nil || !due.Equal(testClock) {
			t.Fatalf("%s is due at %v, want now", id, due)
		}
	}
	if due := store.sources["src_3"].SyncAt; due == nil || !due.Equal(testClock.Add(time.Hour)) {
		t.Fatalf("another connection's source was moved to %v", due)
	}
}

// TestABusyAppCannotSweepContinuously is the rail that makes this safe: a
// channel delivering every few seconds must not mean reading the provider
// every few seconds.
func TestABusyAppCannotSweepContinuously(t *testing.T) {
	store := newFakeStore()
	justSwept := testClock.Add(-30 * time.Second)
	store.hastenable(t, "src_1", "apc_1", &justSwept)
	service, _ := connectedService(t, store, &fakeApps{connection: usableConnection()})

	event := appsv1.TriggerReceived{ConnectionID: "apc_1", OrganizationID: "org_1", Toolkit: "slack"}
	for range 20 {
		if err := service.TriggerReceived(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	// Brought forward to the floor after its last sweep, however many
	// deliveries ask: not now, and not a whole interval away either.
	if due := store.sources["src_1"].SyncAt; due == nil || !due.Equal(justSwept.Add(triggerSweepFloor)) {
		t.Fatalf("a source swept 30s ago is due at %v, want the floor after that sweep", due)
	}

	// Once the floor has passed, the next delivery does move it.
	longAgo := testClock.Add(-10 * time.Minute)
	source := store.sources["src_1"]
	source.LastSyncedAt = &longAgo
	store.sources["src_1"] = source
	if err := service.TriggerReceived(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if due := store.sources["src_1"].SyncAt; due == nil || !due.Equal(testClock) {
		t.Fatalf("a source last swept 10m ago is due at %v, want now", due)
	}
}

// TestATriggerLeavesAFailingSourceToItsBackoff: a source a provider keeps
// refusing is waiting on purpose, and a busy connection must not turn every
// delivery into another refused read.
func TestATriggerLeavesAFailingSourceToItsBackoff(t *testing.T) {
	store := newFakeStore()
	longAgo := testClock.Add(-10 * time.Hour)
	store.hastenable(t, "src_1", "apc_1", &longAgo)
	source := store.sources["src_1"]
	source.Attempts = 3
	store.sources["src_1"] = source
	service, _ := connectedService(t, store, &fakeApps{connection: usableConnection()})

	event := appsv1.TriggerReceived{ConnectionID: "apc_1", OrganizationID: "org_1", Toolkit: "slack"}
	if err := service.TriggerReceived(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if due := store.sources["src_1"].SyncAt; due == nil || !due.Equal(testClock.Add(time.Hour)) {
		t.Fatalf("a failing source was brought forward to %v", due)
	}
}

// TestATriggerNeverDelaysASourceAlreadyDue keeps this from pushing back a
// sweep that was about to happen.
func TestATriggerNeverDelaysASourceAlreadyDue(t *testing.T) {
	store := newFakeStore()
	overdue := testClock.Add(-5 * time.Minute)
	store.sources["src_1"] = domain.Source{
		ID: "src_1", OrganizationID: "org_1", ConnectionID: "apc_1", Provider: "github",
		Status: domain.StatusActive, Interval: time.Hour, SyncAt: &overdue, CreatedBy: "user-1",
	}
	service, _ := connectedService(t, store, &fakeApps{connection: usableConnection()})

	if err := service.TriggerReceived(context.Background(), appsv1.TriggerReceived{
		ConnectionID: "apc_1", OrganizationID: "org_1", Toolkit: "github",
	}); err != nil {
		t.Fatal(err)
	}
	if due := store.sources["src_1"].SyncAt; due == nil || !due.Equal(overdue) {
		t.Fatalf("an overdue source was moved to %v, want it left alone", due)
	}
}

// TestATriggerOnAConnectionWithNothingIndexedIsQuiet covers the common case:
// an app delivering events that nothing is indexed for.
func TestATriggerOnAConnectionWithNothingIndexedIsQuiet(t *testing.T) {
	store := newFakeStore()
	service, _ := connectedService(t, store, &fakeApps{connection: usableConnection()})

	if err := service.TriggerReceived(context.Background(), appsv1.TriggerReceived{
		ConnectionID: "apc_1", OrganizationID: "org_1", Toolkit: "linear",
	}); err != nil {
		t.Fatal(err)
	}
}

// The organization's own files have no connection to announce them, so the
// first shared file is what starts indexing them.

func storedFile(scope string) filesv1.ObjectCreated {
	return filesv1.ObjectCreated{
		FileID: "fil_1", OrganizationID: "org_1", Scope: scope,
		Filename: "handbook.md", ContentType: "text/markdown", CreatedBy: "user-1", CreatedAt: testClock,
	}
}

// filesService is knowledge with the files extractor mounted, which is what
// a deployment that can read them looks like.
func filesService(t *testing.T, store *fakeStore) (*Service, *fakeGrants) {
	t.Helper()
	grants := &fakeGrants{token: "member-token"}
	service := New(Dependencies{
		Store:      store,
		Directory:  &fakeDirectory{membership: member(), agentToken: "member-token"},
		Grants:     grants,
		Extractors: sources.Registry{"files": &fakeExtractor{provider: "files"}},
		Clock:      func() time.Time { return testClock },
	}, Config{})
	return service, grants
}

func TestTheFirstSharedFileIndexesTheOrganizationsOwn(t *testing.T) {
	store := newFakeStore()
	service, grants := filesService(t, store)

	if err := service.FileStored(context.Background(), storedFile("organization")); err != nil {
		t.Fatal(err)
	}
	if len(store.sources) != 1 {
		t.Fatalf("sources = %d, want the organization's files", len(store.sources))
	}
	for _, source := range store.sources {
		if source.Provider != "files" || source.ConnectionID != "" {
			t.Fatalf("source = %+v, want a files source with no connection", source)
		}
		if source.Scope != domain.ScopeOrganization {
			t.Fatalf("scope = %q, want the organization's", source.Scope)
		}
	}
	// Read as whoever stored the file, not as an admin: an admin also sees
	// billing attachments.
	if len(grants.members) != 1 || grants.members[0] != "org_1/user-1" {
		t.Fatalf("minted tokens for %v", grants.members)
	}
}

// Unknown scopes start nothing.
func TestOnlySupportedFileScopesStartIndexing(t *testing.T) {
	for _, scope := range []string{"", "unknown"} {
		t.Run("scope "+scope, func(t *testing.T) {
			store := newFakeStore()
			service, grants := filesService(t, store)

			if err := service.FileStored(context.Background(), storedFile(scope)); err != nil {
				t.Fatal(err)
			}
			if len(store.sources) != 0 {
				t.Fatalf("sources = %d, want none", len(store.sources))
			}
			if len(grants.members) != 0 {
				t.Fatalf("minted a token for %v", grants.members)
			}
		})
	}
}

func TestFirstTeamFileStartsOnlyThatTeamsSource(t *testing.T) {
	store := newFakeStore()
	service, grants := filesService(t, store)
	event := storedFile("team")
	event.TeamID = "team_eng"
	for range 3 {
		if err := service.FileStored(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.sources) != 1 || len(grants.members) != 1 {
		t.Fatalf("team upload made %d sources and %d grants", len(store.sources), len(grants.members))
	}
	for _, source := range store.sources {
		if source.Scope != domain.ScopeTeam || source.TeamID != "team_eng" ||
			source.MemberID != "" || source.Target["team"] != "team_eng" {
			t.Fatalf("team source = %+v", source)
		}
	}
	other := event
	other.TeamID = "team_design"
	if err := service.FileStored(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if len(store.sources) != 2 {
		t.Fatalf("two teams got %d sources", len(store.sources))
	}
}

func TestTeamFileEventWithoutTeamIDStartsNothing(t *testing.T) {
	store := newFakeStore()
	service, grants := filesService(t, store)
	if err := service.FileStored(context.Background(), storedFile("team")); err != nil {
		t.Fatal(err)
	}
	if len(store.sources) != 0 || len(grants.members) != 0 {
		t.Fatal("team event without team id caused indexing")
	}
}

func TestTeamFilesSourceRetiredWithItsUploaderCanBeReplaced(t *testing.T) {
	store := newFakeStore()
	service, _ := filesService(t, store)
	store.sources["src_departed"] = domain.Source{
		ID: "src_departed", OrganizationID: "org_1", Provider: "files",
		Target: map[string]string{"team": "team_eng"}, Scope: domain.ScopeTeam,
		TeamID: "team_eng", CreatedBy: "user-departed",
		Status: domain.StatusRemoved, Reason: knowledgev1.RemovedReasonMemberLeft,
	}
	event := storedFile("team")
	event.TeamID = "team_eng"
	if err := service.FileStored(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(store.sources) != 2 {
		t.Fatalf("team source was not replaced after its uploader left: %+v", store.sources)
	}
}

// The first uploader owns what a placement's files source reads as. When they
// leave the organization their sources go with them, and the next member to
// upload there takes the placement over: a departure is not somebody saying
// the placement should stop being indexed.
func TestAPlacementWhoseUploaderLeftIsTakenOverByTheNextUploader(t *testing.T) {
	for _, scope := range []string{"organization", "team"} {
		t.Run(scope, func(t *testing.T) {
			store := newFakeStore()
			service, grants := filesService(t, store)
			first := storedFile(scope)
			first.TeamID = map[string]string{"team": "team_eng"}[scope]
			first.CreatedBy = "user-departed"
			service.directory.(*fakeDirectory).membership.UserID = "user-departed"
			if err := service.FileStored(context.Background(), first); err != nil {
				t.Fatal(err)
			}
			if len(store.sources) != 1 {
				t.Fatalf("the first upload registered %d sources, want 1", len(store.sources))
			}

			if err := service.MemberRemoved(context.Background(), authv1.MemberRemoved{OrganizationID: "org_1", UserID: "user-departed"}); err != nil {
				t.Fatal(err)
			}
			for _, source := range store.sources {
				if source.Status != domain.StatusRemoved || source.Reason != knowledgev1.RemovedReasonMemberLeft {
					t.Fatalf("the departed member's source = %s, reason %q; want it retired as member_left", source.Status, source.Reason)
				}
			}

			next := first
			next.FileID, next.CreatedBy = "fil_2", "user-1"
			service.directory.(*fakeDirectory).membership.UserID = "user-1"
			if err := service.FileStored(context.Background(), next); err != nil {
				t.Fatal(err)
			}
			live := 0
			for _, source := range store.sources {
				if source.Status == domain.StatusActive {
					live++
					if source.CreatedBy != "user-1" || string(source.Scope) != scope {
						t.Fatalf("replacement = %+v, want user-1's %s source", source, scope)
					}
				}
			}
			if live != 1 {
				t.Fatalf("live sources after the next upload = %d, want the next uploader's", live)
			}
			if got := grants.members[len(grants.members)-1]; got != "org_1/user-1" {
				t.Fatalf("the replacement reads as %s, want the next uploader", got)
			}
		})
	}
}

// Every way a source is retired records why, so what decides whether to
// register one again can tell a member saying no from anything else.
func TestEveryRetirementRecordsItsReason(t *testing.T) {
	store := newFakeStore()
	service, _ := filesService(t, store)
	seed := func(id string, change func(*domain.Source)) {
		source := domain.Source{
			ID: id, OrganizationID: "org_1", Provider: "github", ConnectionID: "apc_" + id,
			Scope: domain.ScopeMember, MemberID: "user-" + id, CreatedBy: "user-" + id, Status: domain.StatusActive,
			Reason: knowledgev1.SyncErrorProviderUnavailable,
		}
		if change != nil {
			change(&source)
		}
		store.sources[id] = source
	}
	seed("left", nil)
	seed("disconnected", nil)
	seed("team", func(source *domain.Source) {
		source.Provider, source.ConnectionID, source.Scope, source.MemberID, source.TeamID = "files", "", domain.ScopeTeam, "", "team_gone"
	})
	seed("removed", nil)
	ctx := context.Background()
	if err := service.MemberRemoved(ctx, authv1.MemberRemoved{OrganizationID: "org_1", UserID: "user-left"}); err != nil {
		t.Fatal(err)
	}
	if err := service.ConnectionRemoved(ctx, appsv1.ConnectionRemoved{ConnectionID: "apc_disconnected"}); err != nil {
		t.Fatal(err)
	}
	if err := service.TeamDeleted(ctx, teamsv1.TeamDeleted{OrganizationID: "org_1", TeamID: "team_gone"}); err != nil {
		t.Fatal(err)
	}
	service.directory.(*fakeDirectory).membership.UserID = "user-removed"
	if err := service.RemoveSource(ctx, authclient.Credentials{Authorization: "Bearer t"}, "org_1", "removed"); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{
		"left":         knowledgev1.RemovedReasonMemberLeft,
		"disconnected": knowledgev1.RemovedReasonConnectionRemoved,
		"team":         knowledgev1.RemovedReasonTeamDeleted,
		"removed":      knowledgev1.RemovedReasonSourceRemoved,
	} {
		if source := store.sources[id]; source.Status != domain.StatusRemoved || source.Reason != want {
			t.Errorf("%s: status %s, reason %q; want removed as %q", id, source.Status, source.Reason, want)
		}
	}
}

func TestFirstPrivateFileStartsOnlyItsOwnersSource(t *testing.T) {
	store := newFakeStore()
	service, grants := filesService(t, store)
	event := storedFile("member")
	event.MemberID = "user-1"
	for range 3 {
		if err := service.FileStored(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.sources) != 1 || len(grants.members) != 1 {
		t.Fatalf("private upload made %d sources and %d grants", len(store.sources), len(grants.members))
	}
	for _, source := range store.sources {
		if source.Scope != domain.ScopeMember || source.MemberID != "user-1" ||
			source.Target["member"] != "self" {
			t.Fatalf("private source = %+v", source)
		}
	}
	other := event
	other.MemberID, other.CreatedBy = "user-2", "user-2"
	service.directory.(*fakeDirectory).membership.UserID = "user-2"
	if err := service.FileStored(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if len(store.sources) != 2 {
		t.Fatalf("two members got %d sources", len(store.sources))
	}
	owners := map[string]bool{}
	for _, source := range store.sources {
		owners[source.MemberID] = true
	}
	if !owners["user-1"] || !owners["user-2"] {
		t.Fatalf("private source owners = %v", owners)
	}
}

func TestMemberFileEventRejectsMismatchedUploader(t *testing.T) {
	store := newFakeStore()
	service, grants := filesService(t, store)
	event := storedFile("member")
	event.MemberID = "user-2"
	if err := service.FileStored(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(store.sources) != 0 || len(grants.members) != 0 {
		t.Fatal("mismatched member event caused indexing")
	}
}

// TestEveryUploadAfterTheFirstCostsOneLookup keeps a busy organization from
// minting a token on every upload.
func TestEveryUploadAfterTheFirstCostsOneLookup(t *testing.T) {
	store := newFakeStore()
	service, grants := filesService(t, store)

	for range 5 {
		if err := service.FileStored(context.Background(), storedFile("organization")); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.sources) != 1 {
		t.Fatalf("sources = %d, want one", len(store.sources))
	}
	if len(grants.members) != 1 {
		t.Fatalf("minted %d tokens, want one: the rest found the source already there", len(grants.members))
	}
}

func TestNewPrivateFileSchedulesAnExistingMemberSourceNow(t *testing.T) {
	store := newFakeStore()
	service, _ := filesService(t, store)
	event := storedFile("member")
	event.MemberID = "user-1"
	if err := service.FileStored(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	var source domain.Source
	for _, found := range store.sources {
		source = found
	}
	later := testClock.Add(24 * time.Hour)
	source.SyncAt = &later
	store.sources[source.ID] = source
	if err := service.FileStored(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if got := store.sources[source.ID].SyncAt; got == nil || !got.Equal(testClock) {
		t.Fatalf("next private file sync at %v, want now", got)
	}
}

// TestASourceAMemberRemovedDoesNotComeBack covers someone deciding their
// organization's files should not be indexed.
func TestASourceAMemberRemovedDoesNotComeBack(t *testing.T) {
	store := newFakeStore()
	service, _ := filesService(t, store)
	store.sources["src_removed"] = domain.Source{
		ID: "src_removed", OrganizationID: "org_1", Provider: "files",
		Scope: domain.ScopeOrganization, Status: domain.StatusRemoved, CreatedBy: "user-1",
	}

	if err := service.FileStored(context.Background(), storedFile("organization")); err != nil {
		t.Fatal(err)
	}
	// A removed source leaves the name free, so one is registered again only
	// because nothing live answers for it — which is the behaviour a member
	// who removed it would not want. It stays removed.
	if len(store.sources) != 1 {
		t.Fatalf("sources = %d, want the removed one left alone", len(store.sources))
	}
}

// Any source already at a placement, live or removed, keeps another from
// being registered there automatically: a narrower one someone registered by
// hand would otherwise sit beside a new one reading everything, and the
// files both read would be indexed, and found, twice. Only a source retired
// because its member left gives the placement back.
func TestAnySourceAtThePlacementKeepsAnotherFromBeingRegistered(t *testing.T) {
	later := testClock.Add(time.Hour)
	narrower := func(id string, status domain.Status, reason string) domain.Source {
		return domain.Source{
			ID: id, OrganizationID: "org_1", Provider: "files", Target: map[string]string{"purpose": "report"},
			Scope: domain.ScopeOrganization, CreatedBy: "user-2", Status: status, Reason: reason, SyncAt: &later,
		}
	}
	for name, testCase := range map[string]struct {
		source     domain.Source
		purpose    string
		registered bool
		hastened   bool
	}{
		"a live narrower source, another kind uploaded": {source: narrower("src_narrow", domain.StatusActive, ""), purpose: "upload"},
		"a live narrower source, its kind uploaded":     {source: narrower("src_narrow", domain.StatusActive, ""), purpose: "report", hastened: true},
		"a narrower source a member removed": {
			source: narrower("src_narrow", domain.StatusRemoved, knowledgev1.RemovedReasonSourceRemoved), purpose: "upload",
		},
		"a narrower source whose member left": {
			source: narrower("src_narrow", domain.StatusRemoved, knowledgev1.RemovedReasonMemberLeft), purpose: "upload", registered: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore()
			service, _ := filesService(t, store)
			store.sources[testCase.source.ID] = testCase.source
			event := storedFile("organization")
			event.Purpose = testCase.purpose
			if err := service.FileStored(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			if registered := len(store.sources) == 2; registered != testCase.registered {
				t.Fatalf("registered another source = %t, want %t: %+v", registered, testCase.registered, store.sources)
			}
			due := store.sources[testCase.source.ID].SyncAt
			if hastened := due != nil && due.Equal(testClock); hastened != testCase.hastened {
				t.Fatalf("brought the narrower source forward = %t, want %t", hastened, testCase.hastened)
			}
		})
	}
}

// TestATeamsFilesSourceIsNotTheOrganizations: a team indexing its own files
// says nothing about the organization's, which the first shared upload
// still starts.
func TestATeamsFilesSourceIsNotTheOrganizations(t *testing.T) {
	store := newFakeStore()
	service, _ := filesService(t, store)
	store.sources["src_team"] = domain.Source{
		ID: "src_team", OrganizationID: "org_1", Provider: "files", Target: map[string]string{"team": "team_1"},
		Scope: domain.ScopeTeam, TeamID: "team_1", Status: domain.StatusActive, CreatedBy: "user-2",
	}

	if err := service.FileStored(context.Background(), storedFile("organization")); err != nil {
		t.Fatal(err)
	}
	if len(store.sources) != 2 {
		t.Fatalf("sources = %d, want the team's and now the organization's", len(store.sources))
	}
}

// TestFilesAreNotIndexedWhereNothingCanReadThem covers a deployment with the
// files extractor unmounted.
func TestFilesAreNotIndexedWhereNothingCanReadThem(t *testing.T) {
	store := newFakeStore()
	grants := &fakeGrants{token: "member-token"}
	service := New(Dependencies{
		Store: store, Directory: &fakeDirectory{membership: member()}, Grants: grants,
		Extractors: sources.Registry{"github": &fakeExtractor{provider: "github"}},
		Clock:      func() time.Time { return testClock },
	}, Config{})

	if err := service.FileStored(context.Background(), storedFile("organization")); err != nil {
		t.Fatal(err)
	}
	if len(store.sources) != 0 || len(grants.members) != 0 {
		t.Fatalf("sources = %d, tokens = %v", len(store.sources), grants.members)
	}
}

// A schedule cannot know that a member just fixed a permission, or just
// changed something at the provider and wants to see it indexed. Asking is
// how they say so.

func TestAMemberCanReadASourceNow(t *testing.T) {
	store := newFakeStore()
	source := store.hastenable(t, "src_1", "apc_1", nil)
	service, _ := connectedService(t, store, &fakeApps{connection: usableConnection()})

	read, err := service.SyncSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.SyncAt == nil || !read.SyncAt.Equal(testClock) {
		t.Fatalf("the source is due at %v, want now", read.SyncAt)
	}
	if store.sources["src_1"].SyncAt == nil || !store.sources["src_1"].SyncAt.Equal(testClock) {
		t.Fatal("the stored schedule was not moved")
	}
}

// TestAskingForASweptSourceIsNotRefused is the difference from a trigger: a
// person has a reason the schedule does not know, so the two-minute floor
// that keeps a busy app in check does not apply to them.
func TestAskingForASweptSourceIsNotRefused(t *testing.T) {
	store := newFakeStore()
	justSwept := testClock.Add(-10 * time.Second)
	source := store.hastenable(t, "src_1", "apc_1", &justSwept)
	service, _ := connectedService(t, store, &fakeApps{connection: usableConnection()})

	read, err := service.SyncSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.SyncAt == nil || !read.SyncAt.Equal(testClock) {
		t.Fatalf("a member asking for a source swept 10s ago was left at %v", read.SyncAt)
	}
}

// TestAskingNeverDelaysASourceAlreadyDue keeps the ask from pushing back a
// sweep that was about to happen.
func TestAskingNeverDelaysASourceAlreadyDue(t *testing.T) {
	store := newFakeStore()
	overdue := testClock.Add(-5 * time.Minute)
	store.sources["src_1"] = domain.Source{
		ID: "src_1", OrganizationID: "org_1", ConnectionID: "apc_1", Provider: "github",
		Status: domain.StatusActive, Interval: time.Hour, SyncAt: &overdue, CreatedBy: "user-1",
	}
	service, _ := connectedService(t, store, &fakeApps{connection: usableConnection()})

	read, err := service.SyncSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", "src_1")
	if err != nil {
		t.Fatal(err)
	}
	if read.SyncAt == nil || !read.SyncAt.Equal(overdue) {
		t.Fatalf("an overdue source was moved to %v, want it left alone", read.SyncAt)
	}
}

func TestSyncSourceChecksWhoIsAskingAndWhatTheSourceIs(t *testing.T) {
	store := newFakeStore()
	store.hastenable(t, "src_mine", "apc_1", nil)

	theirs := store.hastenable(t, "src_theirs", "apc_1", nil)
	theirs.CreatedBy = "user-2"
	store.sources[theirs.ID] = theirs

	paused := store.hastenable(t, "src_paused", "apc_1", nil)
	paused.Status = domain.StatusPaused
	store.sources[paused.ID] = paused

	removed := store.hastenable(t, "src_removed", "apc_1", nil)
	removed.Status = domain.StatusRemoved
	store.sources[removed.ID] = removed

	service, _ := connectedService(t, store, &fakeApps{connection: usableConnection()})
	for name, testCase := range map[string]struct {
		sourceID string
		want     error
	}{
		"another member's":                {sourceID: "src_theirs", want: domain.ErrNotFound},
		"one that does not exist":         {sourceID: "src_nope", want: domain.ErrNotFound},
		"one paused, needing a reconnect": {sourceID: "src_paused", want: domain.ErrNeedsReconnect},
		"one already removed":             {sourceID: "src_removed", want: domain.ErrConflict},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := service.SyncSource(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1", testCase.sourceID)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
		})
	}
}

// A deleted file stops being searchable the moment files says so, rather than
// when the next full sweep notices it missing, up to a week later — and its
// original goes with it.
func TestFileDeletedForgetsTheFileAtOnce(t *testing.T) {
	store := newFakeStore()
	fakeBucket := newFakeOriginals()
	key := "originals/org_1/src_1/doc_1/somehash"
	store.documents["src_1\x00file_1"] = domain.Document{
		ID: "doc_1", OrganizationID: "org_1", SourceID: "src_1", ExternalID: "file_1",
		ContentHash: "somehash", LastSeenAt: testClock,
		Original: &domain.Original{Bucket: "test-bucket", Key: key, SHA256: "somehash"},
	}
	store.documents["src_1\x00file_2"] = domain.Document{
		ID: "doc_2", OrganizationID: "org_1", SourceID: "src_1", ExternalID: "file_2",
		ContentHash: "otherhash", LastSeenAt: testClock,
	}
	fakeBucket.objects[key] = []byte("the pdf")
	service := testServiceWithOriginals(t, store, &fakeExtractor{provider: "files"}, fakeBucket)
	activeSource(store, "files")

	if err := service.FileDeleted(context.Background(), filesv1.ObjectDeleted{
		FileID: "file_1", OrganizationID: "org_1", Scope: "organization", Reason: filesv1.DeletedReasonDeleted,
	}); err != nil {
		t.Fatalf("FileDeleted() error = %v", err)
	}
	if store.documents["src_1\x00file_1"].RemovedAt == nil {
		t.Fatal("the deleted file's document is still searchable")
	}
	if store.documents["src_1\x00file_2"].RemovedAt != nil {
		t.Fatal("another file's document was removed too")
	}
	var removed []string
	for _, event := range store.events {
		if event.DetailType == knowledgev1.DocumentRemovedDetailType {
			removed = append(removed, string(event.Data))
		}
	}
	if len(removed) != 1 || !strings.Contains(removed[0], `"document_id":"doc_1"`) {
		t.Fatalf("removed events = %v, want one for doc_1", removed)
	}
	if fakeBucket.has(key) {
		t.Fatal("the deleted file's original was left in the bucket")
	}

	// Delivered again, as the bus may, it finds nothing left to do.
	if err := service.FileDeleted(context.Background(), filesv1.ObjectDeleted{FileID: "file_1", OrganizationID: "org_1"}); err != nil {
		t.Fatalf("FileDeleted() again error = %v", err)
	}
}

// A deleted file is forgotten through the source of the placement it lived
// in, which the event names, not by asking every files source the
// organization has: with a source per member and per team, that was one
// transaction for each of them on every deletion.
func TestFileDeletedAsksOnlyThePlacementsSource(t *testing.T) {
	store := newFakeStore()
	service, _ := filesService(t, store)
	placements := map[string]domain.Source{
		"src_team":   {Scope: domain.ScopeTeam, TeamID: "team_eng", Target: map[string]string{"team": "team_eng"}},
		"src_design": {Scope: domain.ScopeTeam, TeamID: "team_design", Target: map[string]string{"team": "team_design"}},
		"src_org":    {Scope: domain.ScopeOrganization},
		"src_mine":   {Scope: domain.ScopeMember, MemberID: "user-1", Target: map[string]string{"member": "self"}},
		"src_theirs": {Scope: domain.ScopeMember, MemberID: "user-2", Target: map[string]string{"member": "self"}},
	}
	for id, source := range placements {
		source.ID, source.OrganizationID, source.Provider = id, "org_1", "files"
		source.CreatedBy, source.Status = "user-1", domain.StatusActive
		store.sources[id] = source
	}
	store.documents["src_team\x00file_1"] = domain.Document{
		ID: "doc_1", OrganizationID: "org_1", SourceID: "src_team", ExternalID: "file_1", LastSeenAt: testClock,
	}

	if err := service.FileDeleted(context.Background(), filesv1.ObjectDeleted{
		FileID: "file_1", OrganizationID: "org_1", Scope: "team", TeamID: "team_eng", Reason: filesv1.DeletedReasonDeleted,
	}); err != nil {
		t.Fatal(err)
	}
	if store.documents["src_team\x00file_1"].RemovedAt == nil {
		t.Fatal("the team's deleted file is still searchable")
	}
	if store.retractCalls != 1 {
		t.Fatalf("asked %d sources to forget the file, want only the team's", store.retractCalls)
	}

	store.retractCalls = 0
	if err := service.FileDeleted(context.Background(), filesv1.ObjectDeleted{
		FileID: "file_2", OrganizationID: "org_1", Scope: "member", MemberID: "user-2",
	}); err != nil {
		t.Fatal(err)
	}
	if store.retractCalls != 1 {
		t.Fatalf("asked %d sources to forget a private file, want only its owner's", store.retractCalls)
	}
}
