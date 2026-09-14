package domain

import (
	"errors"
	"testing"
	"time"
)

func TestSessionPlacement(t *testing.T) {
	member := Session{MemberID: "user-1", Scope: ScopeMember}
	if got := member.Placement(); got != (Placement{Scope: ScopeMember, MemberID: "user-1"}) {
		t.Fatalf("member session placement = %+v", got)
	}
	team := Session{MemberID: "user-1", Scope: ScopeTeam, TeamID: "team_1"}
	if got := team.Placement(); got != (Placement{Scope: ScopeTeam, TeamID: "team_1"}) {
		t.Fatalf("team session placement = %+v", got)
	}
}

func TestSessionCompletable(t *testing.T) {
	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	callback := now.Add(-time.Minute)
	ready := Session{MemberID: "user-1", HandleHash: "handle", CallbackAt: &callback, ExpiresAt: now.Add(time.Minute)}
	if err := ready.Completable("user-1", now); err != nil {
		t.Fatalf("ready session error = %v", err)
	}
	completed := ready
	completed.CompletedAt = &callback
	denied := ready
	denied.ProviderError = "provider_denied"
	expired := ready
	expired.ExpiresAt = now
	waiting := ready
	waiting.CallbackAt, waiting.HandleHash = nil, ""
	claimed := ready
	claimed.ClaimedAt = &callback

	for _, tc := range []struct {
		name    string
		session Session
		member  string
		want    error
	}{
		{"another member", ready, "user-2", ErrNotFound},
		{"no callback yet", waiting, "user-1", ErrNotFound},
		{"claimed by another completion", claimed, "user-1", ErrSessionExpired},
		{"another member's claimed session", claimed, "user-2", ErrNotFound},
		{"already completed", completed, "user-1", ErrSessionExpired},
		{"provider refused", denied, "user-1", ErrSessionExpired},
		{"expired", expired, "user-1", ErrSessionExpired},
	} {
		if err := tc.session.Completable(tc.member, now); !errors.Is(err, tc.want) {
			t.Fatalf("%s: Completable() error = %v, want %v", tc.name, err, tc.want)
		}
	}
}
