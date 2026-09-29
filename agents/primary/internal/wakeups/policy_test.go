package wakeups

import (
	"context"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
)

type fakeWorkspacePolicy struct{ proactive string }

func (provider fakeWorkspacePolicy) Get(context.Context, string) (policy.Settings, error) {
	return policy.Settings{Proactive: provider.proactive}, nil
}

func TestWorkspacePolicySetsDefaultForAppUpdates(t *testing.T) {
	for _, test := range []struct {
		proactive string
		want      bool
	}{
		{"", true}, {"on", true}, {"off", false},
	} {
		store := &Store{policy: fakeWorkspacePolicy{proactive: test.proactive}}
		got, err := store.defaultAppUpdates(context.Background(), "org_1")
		if err != nil || got != test.want {
			t.Fatalf("proactive=%q default=%t err=%v", test.proactive, got, err)
		}
	}
}
