package guardpolicy

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
	workspacestore "github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

type workspaceProviderFunc func(context.Context, string) (policy.Settings, error)

func (provider workspaceProviderFunc) Get(ctx context.Context, organizationID string) (policy.Settings, error) {
	return provider(ctx, organizationID)
}

type workspaceRepositoryFunc func(context.Context, string) (int64, []byte, error)

func (repository workspaceRepositoryFunc) Policy(ctx context.Context, organizationID string) (int64, []byte, error) {
	return repository(ctx, organizationID)
}

func TestWorkspacePoliciesMapOriginsRelevanceAndRegulatedAndPreserveRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	providerErr := errors.New("workspace unavailable")
	for _, failure := range []error{nil, providerErr} {
		source := NewWorkspacePolicies(workspaceProviderFunc(func(got context.Context, organizationID string) (policy.Settings, error) {
			if got != ctx || organizationID != "org_1" {
				t.Fatalf("provider received context %v and organization %q, want the request's", got, organizationID)
			}
			return policy.Settings{PIIMinimumOwner: "strict", PIIMinimumPeer: "secrets", GuardRelevance: "off", Regulated: true}, failure
		}))
		got, err := source.Get(ctx, "org_1")
		if failure != nil {
			if !errors.Is(err, failure) || got != (OrgPolicy{}) {
				t.Fatalf("failed provider = %+v, %v; want its error without partial settings", got, err)
			}
			continue
		}
		if want := (OrgPolicy{MinimumOwner: LevelStrict, MinimumPeer: LevelSecrets, Relevance: RelevanceOff, Regulated: true}); err != nil || got != want {
			t.Fatalf("workspace policy = %+v, %v; want %+v", got, err, want)
		}
	}
}

func TestWorkspacePoliciesApplyCurrentVersionFloorsWithoutChangingSavedChoices(t *testing.T) {
	ctx := context.Background()
	version, data := int64(1), []byte(`{"PIIMinimumOwner":"strict","PIIMinimumPeer":"strict","GuardRelevance":"shadow","Regulated":true}`)
	loader := policy.New(workspaceRepositoryFunc(func(_ context.Context, organizationID string) (int64, []byte, error) {
		if organizationID == "org_2" {
			return 1, []byte(`{}`), nil
		}
		if organizationID != "org_1" {
			t.Fatalf("unexpected organization %q", organizationID)
		}
		return version, data, nil
	}))
	service, _, log := testService(NewWorkspacePolicies(loader), Cap{})
	view, err := service.SetPreferences(ctx, "org_1", "user_1", levelPointer(LevelSecrets), levelPointer(LevelSecrets))
	if want := (Effective{Owner: LevelStrict, Peer: LevelStrict, Relevance: RelevanceShadow, Regulated: true}); err != nil || view.Effective() != want || view.Owner.Chosen != LevelSecrets || view.Peer.Chosen != LevelSecrets || view.Owner.Floor != LevelStrict || view.Peer.Floor != LevelStrict {
		t.Fatalf("strict workspace floor = %+v, %v; want %+v with both lower choices retained", view, err, want)
	}
	other, err := service.SetPreferences(ctx, "org_2", "user_1", nil, levelPointer(LevelSecrets))
	if err != nil || other.Peer.Effective != LevelSecrets || other.Regulated {
		t.Fatalf("another organization's empty policy = %+v, %v; want its own floors", other, err)
	}
	version, data = 2, []byte(`{"PIIMinimumOwner":"secrets","PIIMinimumPeer":"secrets","GuardRelevance":"off"}`)
	view, err = service.Preferences(ctx, "org_1", "user_1")
	if want := (Effective{Owner: LevelSecrets, Peer: LevelSecrets, Relevance: RelevanceOff}); err != nil || view.Effective() != want || view.Owner.Chosen != LevelSecrets || view.Peer.Chosen != LevelSecrets {
		t.Fatalf("new workspace version = %+v, %v; want retained choices to resume %+v", view, err, want)
	}
	if log.count() != 0 {
		t.Fatalf("successful policy reads logged %d records, want none", log.count())
	}
}

func TestWorkspaceProviderReadAndDecodeFailuresAreConservativeAndRecover(t *testing.T) {
	ctx := context.Background()
	version, data := int64(1), []byte(`{}`)
	var repositoryErr error
	loader := policy.New(workspaceRepositoryFunc(func(context.Context, string) (int64, []byte, error) {
		return version, data, repositoryErr
	}))
	service, _, log := testService(NewWorkspacePolicies(loader), Cap{Relevance: RelevanceShadow})
	if _, err := service.SetPreferences(ctx, "org_1", "user_1", levelPointer(LevelSecrets), levelPointer(LevelSecrets)); err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"read", "decode"} {
		if failure == "read" {
			repositoryErr = errors.New("workspace database unavailable")
		} else {
			version++
			data = []byte(`{"PIIMinimumPeer":`)
		}
		for range 2 {
			effective, err := service.Effective(ctx, "org_1", "user_1")
			if want := (Effective{Owner: LevelStrict, Peer: LevelStrict, Relevance: RelevanceShadow, Regulated: true}); err != nil || effective != want {
				t.Fatalf("%s failure = %+v, %v; want %+v", failure, effective, err, want)
			}
		}
		repositoryErr = nil
		version++
		data = []byte(`{}`)
		effective, err := service.Effective(ctx, "org_1", "user_1")
		if want := (Effective{Owner: LevelSecrets, Peer: LevelSecrets, Relevance: RelevanceShadow}); err != nil || effective != want {
			t.Fatalf("recovered from %s failure = %+v, %v; want saved choices %+v", failure, effective, err, want)
		}
	}
	if len(log.warnings()) != 2 {
		t.Fatalf("logged %d warnings, want one for each read/decode outage", len(log.warnings()))
	}
}

func TestWorkspaceProviderSuccessfulAbsenceSuppliesDefaultsWithoutWarnings(t *testing.T) {
	for name, provider := range map[string]policy.Provider{
		"noop":         policy.Noop{},
		"nil":          nil,
		"typed nil":    workspaceProviderFunc(nil),
		"empty loader": policy.New(nil),
		"unapplied org": policy.New(workspaceRepositoryFunc(func(context.Context, string) (int64, []byte, error) {
			return 0, nil, fmt.Errorf("read workspace: %w", workspacestore.ErrVersionNotFound)
		})),
	} {
		t.Run(name, func(t *testing.T) {
			service, _, log := testService(NewWorkspacePolicies(provider), Cap{Relevance: RelevanceShadow})
			effective, err := service.Effective(context.Background(), "org_1", "user_1")
			if want := (Effective{Owner: LevelSecrets, Peer: LevelStrict, Relevance: RelevanceShadow}); err != nil || effective != want {
				t.Fatalf("empty policy = %+v, %v; want %+v", effective, err, want)
			}
			view, err := service.SetPreferences(context.Background(), "org_1", "user_1", levelPointer(LevelSecrets), levelPointer(LevelSecrets))
			if err != nil || view.Owner.Floor != LevelSecrets || view.Peer.Floor != LevelSecrets || view.Peer.Effective != LevelSecrets || view.Regulated {
				t.Fatalf("choice under an empty policy = %+v, %v; want no extra floor or regulated status", view, err)
			}
			if log.count() != 0 {
				t.Fatalf("successful empty policy logged %d records, want none", log.count())
			}
		})
	}
}
