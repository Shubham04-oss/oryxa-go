package memberauth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"google.golang.org/adk/v2/auth"
)

// fakeTokenSource records every call it gets and answers with a fixed token,
// or a fixed error when set.
type fakeTokenSource struct {
	calls  int
	users  []string
	agents []Agent
	token  string
	err    error
}

func (f *fakeTokenSource) AgentToken(_ context.Context, userID string, agent Agent) (string, time.Time, error) {
	f.calls++
	f.users = append(f.users, userID)
	f.agents = append(f.agents, agent)
	if f.err != nil {
		return "", time.Time{}, f.err
	}
	return f.token, time.Now().Add(time.Hour), nil
}

func TestCredentialAppliesTheCtxTokenAndNeverCallsTheSource(t *testing.T) {
	tokens := &fakeTokenSource{token: "should-not-be-used"}
	provider := NewCredentialProvider(tokens)

	ctx := withMember(context.Background(), Member{UserID: "user-1", OrganizationID: "org-1"})
	ctx = WithAccessToken(ctx, "member-own-token")

	credential, err := provider.Credential(ctx)
	if err != nil {
		t.Fatalf("Credential() error = %v", err)
	}
	header := applyHeader(t, credential)
	if got := header.Get("Authorization"); got != "Bearer member-own-token" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer member-own-token")
	}
	if got := header.Get("X-Oryxa-Organization"); got != "org-1" {
		t.Errorf("X-Oryxa-Organization = %q, want %q", got, "org-1")
	}
	if len(header) != 2 {
		t.Errorf("header = %v, want exactly Authorization and X-Oryxa-Organization", header)
	}
	if tokens.calls != 0 {
		t.Errorf("token source was called %d times, want 0: a member's own token must never ask for an agent token", tokens.calls)
	}
}

func TestCredentialAsksTheSourceWithNoCtxToken(t *testing.T) {
	tokens := &fakeTokenSource{token: "agent-token-xyz"}
	provider := NewCredentialProvider(tokens)

	ctx := withMember(context.Background(), Member{UserID: "user-1", OrganizationID: "org-1"})

	credential, err := provider.Credential(ctx)
	if err != nil {
		t.Fatalf("Credential() error = %v", err)
	}
	header := applyHeader(t, credential)
	if got := header.Get("Authorization"); got != "Bearer agent-token-xyz" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer agent-token-xyz")
	}
	if got := header.Get("X-Oryxa-Organization"); got != "org-1" {
		t.Errorf("X-Oryxa-Organization = %q, want %q", got, "org-1")
	}
	if len(header) != 2 {
		t.Errorf("header = %v, want exactly Authorization and X-Oryxa-Organization", header)
	}
	if tokens.calls != 1 || tokens.users[0] != "user-1" {
		t.Errorf("token source calls = %d, users = %v, want one call for user-1", tokens.calls, tokens.users)
	}
}

func TestCredentialNoMemberInContext(t *testing.T) {
	provider := NewCredentialProvider(&fakeTokenSource{})
	if _, err := provider.Credential(context.Background()); !errors.Is(err, ErrNoMember) {
		t.Fatalf("Credential() error = %v, want ErrNoMember", err)
	}
}

func TestCredentialGrantRequiredPropagates(t *testing.T) {
	tokens := &fakeTokenSource{err: ErrGrantRequired}
	provider := NewCredentialProvider(tokens)
	ctx := withMember(context.Background(), Member{UserID: "user-1", OrganizationID: "org-1"})

	if _, err := provider.Credential(ctx); !errors.Is(err, ErrGrantRequired) {
		t.Fatalf("Credential() error = %v, want ErrGrantRequired", err)
	}
}

// applyHeader runs credential.Apply into a fresh header and returns it.
func applyHeader(t *testing.T, credential auth.Credential) http.Header {
	t.Helper()
	header := make(http.Header)
	if err := credential.Apply(header); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	return header
}
