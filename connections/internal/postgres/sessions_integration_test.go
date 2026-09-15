package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

func newSession(org, appID, stateHash, memberID string, expiresAt time.Time) domain.Session {
	return domain.Session{
		StateHash: stateHash, OrganizationID: org, MemberID: memberID, AppID: appID, Intent: domain.IntentConnect,
		Scope: domain.ScopeMember, ReturnURI: "https://app.example.test/done",
		RedirectURI: "https://connect.example.test/v1/oauth/callback/google",
		CreatedAt:   expiresAt.Add(-domain.SessionTTL), ExpiresAt: expiresAt,
	}
}

func TestCountOpenSessions(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	org := "org_open_sessions"
	app := createApp(t, store, org, "app_google", domain.CredentialOAuth2, now)

	open := newSession(org, app.ID, sha("open"), "user-1", now.Add(5*time.Minute))
	if err := store.CreateSession(ctx, open); err != nil {
		t.Fatal(err)
	}
	expired := newSession(org, app.ID, sha("expired"), "user-1", now.Add(-time.Minute))
	expired.CreatedAt = now.Add(-20 * time.Minute)
	if err := store.CreateSession(ctx, expired); err != nil {
		t.Fatal(err)
	}
	completed := newSession(org, app.ID, sha("completed"), "user-1", now.Add(5*time.Minute))
	if err := store.CreateSession(ctx, completed); err != nil {
		t.Fatal(err)
	}
	if err := store.BurnSession(ctx, completed.StateHash, now); err != nil {
		t.Fatal(err)
	}
	otherMember := newSession(org, app.ID, sha("other-member"), "user-2", now.Add(5*time.Minute))
	if err := store.CreateSession(ctx, otherMember); err != nil {
		t.Fatal(err)
	}

	count, err := store.CountOpenSessions(ctx, org, "user-1", now)
	if err != nil || count != 1 {
		t.Fatalf("CountOpenSessions() = %d, %v, want 1", count, err)
	}
	if count, err := store.CountOpenSessions(ctx, org, "user-2", now); err != nil || count != 1 {
		t.Fatalf("CountOpenSessions(user-2) = %d, %v, want 1", count, err)
	}
	if count, err := store.CountOpenSessions(ctx, org, "user-3", now); err != nil || count != 0 {
		t.Fatalf("CountOpenSessions(user-3) = %d, %v, want 0", count, err)
	}
}

func TestDeleteExpiredSessions(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	org := "org_expired_sessions"
	app := createApp(t, store, org, "app_google", domain.CredentialOAuth2, now)
	cutoff := now

	expiredHashes := []string{sha("expired-1"), sha("expired-2"), sha("expired-3")}
	for _, hash := range expiredHashes {
		session := newSession(org, app.ID, hash, "user-1", cutoff.Add(-time.Minute))
		session.CreatedAt = cutoff.Add(-20 * time.Minute)
		if err := store.CreateSession(ctx, session); err != nil {
			t.Fatal(err)
		}
	}
	keep := newSession(org, app.ID, sha("keep"), "user-1", cutoff.Add(time.Hour))
	if err := store.CreateSession(ctx, keep); err != nil {
		t.Fatal(err)
	}

	deleted, err := store.DeleteExpiredSessions(ctx, cutoff, 2)
	if err != nil || deleted != 2 {
		t.Fatalf("DeleteExpiredSessions(limit 2) = %d, %v, want 2", deleted, err)
	}
	deleted, err = store.DeleteExpiredSessions(ctx, cutoff, 2)
	if err != nil || deleted != 1 {
		t.Fatalf("second DeleteExpiredSessions() = %d, %v, want 1", deleted, err)
	}
	deleted, err = store.DeleteExpiredSessions(ctx, cutoff, 2)
	if err != nil || deleted != 0 {
		t.Fatalf("third DeleteExpiredSessions() = %d, %v, want 0", deleted, err)
	}

	remaining := 0
	for _, hash := range expiredHashes {
		if _, err := store.SessionByState(ctx, hash); errors.Is(err, domain.ErrNotFound) {
			continue
		} else if err != nil {
			t.Fatal(err)
		}
		remaining++
	}
	if remaining != 0 {
		t.Fatalf("%d expired sessions were not deleted", remaining)
	}
	if _, err := store.SessionByState(ctx, keep.StateHash); err != nil {
		t.Fatalf("unexpired session was deleted: %v", err)
	}
}
