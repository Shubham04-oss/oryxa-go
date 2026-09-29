package policy

import (
	"context"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

type fakeRepository struct {
	document []byte
	err      error
}

func (repo fakeRepository) Latest(context.Context, string) (store.Version, error) {
	if repo.err != nil {
		return store.Version{}, repo.err
	}
	return store.Version{Document: repo.document}, nil
}

func TestLoaderReadsOnlyAppliedPolicies(t *testing.T) {
	loader := New(fakeRepository{document: []byte("apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\nrules: {consent: {expireAfter: 6h}}\nsafety: {guard: {holdForReview: true}}\nagents: {personal: {proactive: off}}\ntools: {provider: composio, offer: [github], autoSubscribe: false}\n")})
	settings, err := loader.Get(context.Background(), "org_1")
	if err != nil || settings.ExpireAfter != 6*time.Hour || !settings.HoldForReview || settings.Proactive != "off" || settings.Offer == nil || len(*settings.Offer) != 1 || (*settings.Offer)[0] != "github" || settings.AutoSubscribe == nil || *settings.AutoSubscribe {
		t.Fatalf("settings = %+v, %v", settings, err)
	}
	settings, err = New(fakeRepository{err: store.ErrVersionNotFound}).Get(context.Background(), "org_1")
	if err != nil || settings != (Settings{}) {
		t.Fatalf("defaults = %+v, %v", settings, err)
	}
}
