package policy

import (
	"context"
	"encoding/json"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/projection"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

type fakeRepository struct {
	document []byte
	err      error
}

type projectedRepository struct {
	version int64
	data    []byte
}

func (repo *projectedRepository) Policy(context.Context, string) (int64, []byte, error) {
	return repo.version, repo.data, nil
}

func TestProjectionReadsFutureKeysAndTracksVersionsWithoutLeakingMutableState(t *testing.T) {
	repo := &projectedRepository{version: 1, data: []byte(`{"Offer":["github"],"HoldForReview":true,"NewSetting":{"mode":"future"}}`)}
	loader := New(repo)
	first, err := loader.Get(context.Background(), "org_1")
	if err != nil || !first.HoldForReview || first.Offer == nil {
		t.Fatalf("future projection failed: %+v %v", first, err)
	}
	(*first.Offer)[0] = "slack"
	again, err := loader.Get(context.Background(), "org_1")
	if err != nil || (*again.Offer)[0] != "github" {
		t.Fatalf("caller mutated cached policy: %+v %v", again, err)
	}
	repo.version, repo.data = 2, []byte(`{"Offer":[],"HoldForReview":false}`)
	latest, err := loader.Get(context.Background(), "org_1")
	if err != nil || latest.HoldForReview || latest.Offer == nil || len(*latest.Offer) != 0 {
		t.Fatalf("stale cached policy: %+v %v", latest, err)
	}
}

func (repo fakeRepository) Policy(context.Context, string) (int64, []byte, error) {
	if repo.err != nil {
		return 0, nil, repo.err
	}
	settings, err := projection.FromYAML(repo.document)
	if err != nil {
		return 0, nil, err
	}
	encoded, err := json.Marshal(settings)
	return 1, encoded, err
}

func TestLoaderReadsOnlyAppliedPolicies(t *testing.T) {
	loader := New(fakeRepository{document: []byte("apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\nrules: {consent: {expireAfter: 6h}}\nsafety: {guard: {holdForReview: true}}\nagents: {personal: {proactive: off}}\ntools: {provider: composio, offer: [github], employeeConnections: false, autoSubscribe: false, wakeUpWithin: 30m}\nsearch: {mode: none}\n")})
	settings, err := loader.Get(context.Background(), "org_1")
	if err != nil || settings.ExpireAfter != 6*time.Hour || !settings.HoldForReview || settings.Proactive != "off" || settings.SearchMode != "none" || settings.WakeUpWithin != 30*time.Minute || settings.Offer == nil || len(*settings.Offer) != 1 || (*settings.Offer)[0] != "github" || settings.EmployeeConnections == nil || *settings.EmployeeConnections || settings.AutoSubscribe == nil || *settings.AutoSubscribe {
		t.Fatalf("settings = %+v, %v", settings, err)
	}
	settings, err = New(fakeRepository{err: store.ErrVersionNotFound}).Get(context.Background(), "org_1")
	if err != nil || settings != (Settings{}) {
		t.Fatalf("defaults = %+v, %v", settings, err)
	}
}

func TestNilLoaderAndNoopAreSafeDefaults(t *testing.T) {
	var nilLoader *Loader
	for _, provider := range []Provider{nilLoader, New(nil), Noop{}} {
		value, err := provider.Get(context.Background(), "org_1")
		if err != nil || value != (Settings{}) {
			t.Fatalf("default provider = %+v %v", value, err)
		}
	}
}

func TestLoaderReadsTheAnswerGuardPolicies(t *testing.T) {
	loader := New(fakeRepository{document: []byte("apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\npreset: regulated\nsafety: {guard: {holdForReview: true, pii: {minimum: {owner: secrets, peer: strict}}, relevance: shadow}}\n")})
	settings, err := loader.Get(context.Background(), "org_1")
	if err != nil || settings.PIIMinimumOwner != "secrets" || settings.PIIMinimumPeer != "strict" || settings.GuardRelevance != "shadow" || !settings.Regulated || !settings.HoldForReview {
		t.Fatalf("settings = %+v, %v", settings, err)
	}
	// Only what is named is set, and a preset that is not regulated says so.
	loader = New(fakeRepository{document: []byte("apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\npreset: team\nsafety: {guard: {pii: {minimum: {peer: strict}}}}\n")})
	settings, err = loader.Get(context.Background(), "org_1")
	if err != nil || settings.PIIMinimumOwner != "" || settings.PIIMinimumPeer != "strict" || settings.GuardRelevance != "" || settings.Regulated || settings.HoldForReview {
		t.Fatalf("settings = %+v, %v", settings, err)
	}
}
