package shadow

import (
	"context"
	"errors"
	"testing"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
)

type fakeTokens map[string]*v1.ZedToken

func (tokens fakeTokens) Latest(_ context.Context, org string) (*v1.ZedToken, error) {
	if org == "broken" {
		return nil, errors.New("no table")
	}
	return tokens[org], nil
}

func TestConsistencyFollowsTheOrgsLatestToken(t *testing.T) {
	tokens := fakeTokens{"org_1": {Token: "zed_1"}}
	ctx := context.Background()

	fresh := Client{Tokens: tokens}.consistency(ctx, "org_1").GetAtLeastAsFresh()
	if fresh.GetToken() != "zed_1" {
		t.Fatalf("org with a token: want at_least_as_fresh zed_1, got %v", fresh)
	}
	for name, client := range map[string]struct {
		Client
		org string
	}{
		"no store":      {Client{}, "org_1"},
		"no token":      {Client{Tokens: tokens}, "org_2"},
		"store failing": {Client{Tokens: tokens}, "broken"},
		"no org":        {Client{Tokens: tokens}, ""},
	} {
		if !client.consistency(ctx, client.org).GetMinimizeLatency() {
			t.Errorf("%s: want minimize_latency", name)
		}
	}
}
