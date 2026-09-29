package application

import (
	"context"
	"errors"
	"testing"
	"time"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"
	workspacepolicy "github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/sources"
)

type fakeWorkspacePolicy struct {
	offer *[]string
	err   error
}

func (policy *fakeWorkspacePolicy) Get(context.Context, string) (workspacepolicy.Settings, error) {
	return workspacepolicy.Settings{Offer: policy.offer}, policy.err
}

func TestWorkspaceOfferStopsKnowledgeProviderAtRegistrationAndSync(t *testing.T) {
	store := newFakeStore()
	apps := &fakeApps{connection: usableConnection()}
	extractor := &fakeExtractor{provider: "github"}
	offer := []string{"gmail"}
	policy := &fakeWorkspacePolicy{offer: &offer}
	service := New(Dependencies{
		Store: store, Directory: &fakeDirectory{membership: member()}, Apps: apps, Policy: policy,
		Extractors: sources.Registry{"github": extractor}, Clock: func() time.Time { return testClock },
	}, Config{})
	credentials := authclient.Credentials{Authorization: "Bearer t"}
	if _, err := service.RegisterSource(context.Background(), credentials, "org_1", githubRegistration()); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("restricted registration = %v", err)
	}
	if len(apps.calls) != 0 || len(store.sources) != 0 {
		t.Fatalf("restricted registration used apps or stored a source: %v %+v", apps.calls, store.sources)
	}
	source := activeSource(store, "github")
	source.ConnectionID, source.ComposioAccountID = "apc_1", "ca_1"
	store.sources[source.ID] = source
	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatal(err)
	}
	if len(extractor.calls) != 0 || store.failedReason != knowledgev1.SyncErrorForbidden {
		t.Fatalf("restricted sync fetched %d pages; reason = %q", len(extractor.calls), store.failedReason)
	}
}

func TestWorkspaceOfferStopsQueuedConnectionIndexing(t *testing.T) {
	store := newFakeStore()
	apps := &fakeApps{connection: usableConnection(), targets: repositories("acme/api")}
	service, grants := connectedService(t, store, apps)
	offer := []string{}
	service.policy = &fakeWorkspacePolicy{offer: &offer}
	if err := service.ConnectionConnected(context.Background(), connected()); err != nil {
		t.Fatal(err)
	}
	if len(apps.calls) != 0 || len(grants.members) != 0 || len(store.sources) != 0 {
		t.Fatalf("restricted queued event used provider: apps=%v grants=%v sources=%v", apps.calls, grants.members, store.sources)
	}
}

func TestWorkspaceOfferLeavesInternalFilesAloneAndFailsClosed(t *testing.T) {
	offer := []string{}
	policy := &fakeWorkspacePolicy{offer: &offer, err: errors.New("database down")}
	service := New(Dependencies{Policy: policy}, Config{})
	if err := service.allowProvider(context.Background(), "org_1", "files"); err != nil {
		t.Fatalf("internal files = %v", err)
	}
	if err := service.allowProvider(context.Background(), "org_1", "github"); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("unavailable policy = %v", err)
	}
}
