package memberauth

import (
	"context"
	"errors"
	"iter"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runprofile"
)

// fakeState is a minimal session.ReadonlyState, the same shape agents_test.go
// uses for its own mapState.
type fakeState map[string]any

func (s fakeState) Get(key string) (any, error) {
	value, ok := s[key]
	if !ok {
		return nil, session.ErrStateKeyNotExist
	}
	return value, nil
}

func (s fakeState) All() iter.Seq2[string, any] {
	return func(yield func(string, any) bool) {
		for key, value := range s {
			if !yield(key, value) {
				return
			}
		}
	}
}

// fakeReadonlyContext is a direct ReadonlyContext double for unit-testing
// MemberFromSession itself, independent of a real session or invocation
// context: agent.StrictContextMock (an ADK-exported test double) panics on
// anything not overridden, so a test that reaches further than UserID and
// ReadonlyState fails loudly rather than reading a silent zero value.
type fakeReadonlyContext struct {
	agent.StrictContextMock
	userID string
	state  session.ReadonlyState
}

func (c *fakeReadonlyContext) UserID() string                       { return c.userID }
func (c *fakeReadonlyContext) ReadonlyState() session.ReadonlyState { return c.state }

var _ agent.ReadonlyContext = (*fakeReadonlyContext)(nil)

func TestMemberFromSessionNoUser(t *testing.T) {
	ctx := &fakeReadonlyContext{
		StrictContextMock: agent.NewStrictContextMock(context.Background()),
		userID:            "",
		state:             fakeState{"organization_id": "org-1"},
	}
	if _, err := MemberFromSession(ctx); !errors.Is(err, ErrNoMember) {
		t.Fatalf("MemberFromSession() error = %v, want ErrNoMember", err)
	}
}

func TestMemberFromSessionNoOrganization(t *testing.T) {
	ctx := &fakeReadonlyContext{
		StrictContextMock: agent.NewStrictContextMock(context.Background()),
		userID:            "user-1",
		state:             fakeState{},
	}
	if _, err := MemberFromSession(ctx); !errors.Is(err, ErrNoMember) {
		t.Fatalf("MemberFromSession() error = %v, want ErrNoMember", err)
	}
}

func TestMemberFromSessionResolvesTheMember(t *testing.T) {
	ctx := &fakeReadonlyContext{
		StrictContextMock: agent.NewStrictContextMock(context.Background()),
		userID:            "user-1",
		state:             fakeState{"organization_id": "org-1"},
	}
	member, err := MemberFromSession(ctx)
	if err != nil {
		t.Fatalf("MemberFromSession() error = %v", err)
	}
	if member != (Member{UserID: "user-1", OrganizationID: "org-1"}) {
		t.Fatalf("member = %+v, want user-1/org-1", member)
	}
}

func TestAccessTokenRoundTrip(t *testing.T) {
	if got := AccessToken(context.Background()); got != "" {
		t.Fatalf("AccessToken() on a plain context = %q, want empty", got)
	}
	ctx := WithAccessToken(context.Background(), "token-abc")
	if got := AccessToken(ctx); got != "token-abc" {
		t.Fatalf("AccessToken() = %q, want token-abc", got)
	}
}

func TestBearerPrefersTheMembersOwnToken(t *testing.T) {
	state := fakeState{"organization_id": "org-1"}
	present := &fakeReadonlyContext{
		StrictContextMock: agent.NewStrictContextMock(WithAccessToken(context.Background(), "own-token")),
		userID:            "user-1",
		state:             state,
	}
	member, token, err := Bearer(present, nil)
	if err != nil || token != "own-token" || member.OrganizationID != "org-1" {
		t.Fatalf("Bearer() = %+v, %q, %v", member, token, err)
	}

	absent := &fakeReadonlyContext{
		StrictContextMock: agent.NewStrictContextMock(context.Background()),
		userID:            "user-1",
		state:             state,
	}
	if _, _, err := Bearer(absent, nil); err == nil {
		t.Fatal("Bearer() with neither a member token nor a token source = nil error")
	}
	_, token, err = Bearer(absent, staticTokens("agent-token"))
	if err != nil || token != "agent-token" {
		t.Fatalf("Bearer() for an absent member = %q, %v", token, err)
	}
}

// TestASpecialistsRunAsksForTheSpecialistsToken: a session running a
// specialist resolves to a member whose agent is that specialist, and the
// token minted for its run names it; Krude's own session, with no kind,
// asks as the primary.
func TestASpecialistsRunAsksForTheSpecialistsToken(t *testing.T) {
	for state, want := range map[*fakeState]Agent{
		{"organization_id": "org-1", "managed_agent_id": "agt_writer", "managed_agent_kind": "specialist"}: {ID: "agt_writer", Role: runprofile.Specialist},
		{"organization_id": "org-1"}: {},
	} {
		ctx := &fakeReadonlyContext{
			StrictContextMock: agent.NewStrictContextMock(context.Background()),
			userID:            "user-1",
			state:             *state,
		}
		tokens := &fakeTokenSource{token: "agent-token"}
		if _, _, err := Bearer(ctx, tokens); err != nil {
			t.Fatal(err)
		}
		if len(tokens.agents) != 1 || tokens.agents[0] != want {
			t.Fatalf("token asked for %+v, want %+v", tokens.agents, want)
		}
	}
}

type staticTokens string

func (tokens staticTokens) AgentToken(context.Context, string, Agent) (string, time.Time, error) {
	return string(tokens), time.Now().Add(time.Hour), nil
}
