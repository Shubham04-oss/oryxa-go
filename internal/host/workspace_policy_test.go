package host

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
)

type fakeWorkspacePolicies struct {
	available bool
	err       error
}

func (repo fakeWorkspacePolicies) Available(context.Context) (bool, error) {
	return repo.available, repo.err
}
func (fakeWorkspacePolicies) Policy(context.Context, string) (int64, []byte, error) {
	return 1, []byte(`{"HoldForReview":true,"Offer":[]}`), nil
}

func TestWorkspacePolicyUsesSchemaWithoutAServiceURL(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	provider, err := workspacePolicyProvider(context.Background(), fakeWorkspacePolicies{available: true}, logger)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := provider.Get(context.Background(), "org_1")
	if err != nil || !settings.HoldForReview || settings.Offer == nil {
		t.Fatalf("schema policy ignored: %+v %v", settings, err)
	}
	_, err = workspacePolicyProvider(context.Background(), fakeWorkspacePolicies{}, logger)
	if err != nil || !strings.Contains(logs.String(), "enforcement is disabled") {
		t.Fatalf("disabled policy went unnoticed: %s %v", logs.String(), err)
	}
	_, err = workspacePolicyProvider(context.Background(), fakeWorkspacePolicies{err: errors.New("database down")}, logger)
	if err == nil {
		t.Fatal("database failure silently disabled policy")
	}
}

func TestMountedServicesShareWorkspacePolicy(t *testing.T) {
	provider := policy.New(nil)
	platform := Platform{Logger: slog.New(slog.DiscardHandler), WorkspacePolicy: provider}
	definitions := []Definition{}
	for _, name := range []string{"apps", "agents", "knowledge", "batching"} {
		definitions = append(definitions, Definition{Name: name, Mount: func(_ context.Context, got Platform) (Service, error) {
			if got.WorkspaceSettings() != provider {
				t.Fatal("service received a separate policy reader")
			}
			return Service{}, nil
		}})
	}
	if _, err := mount(context.Background(), platform, definitions); err != nil {
		t.Fatal(err)
	}
}
