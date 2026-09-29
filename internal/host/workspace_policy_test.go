package host

import (
	"context"
	"log/slog"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
)

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
