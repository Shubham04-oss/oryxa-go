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

// TestClaimedSessionIsSingleUseUntilItExpires pins a deliberate trade-off. A
// completion claims its session before exchanging the code, so a repeated
// request can never exchange a code twice. The claim is not a lease: if the
// process dies between claiming and consuming, that session cannot be claimed
// again, and the member starts over. Preferring a failed connection attempt
// over a double exchange is the point, so this test exists to make a change to
// it deliberate rather than accidental.
func TestClaimedSessionIsSingleUseUntilItExpires(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	org := "org_session_claim"
	createApp(t, store, org, "app_google", domain.CredentialOAuth2, now)

	expires := now.Add(10 * time.Minute)
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO connections_sessions (
			state_hash, handle_hash, organization_id, member_id, app_id, intent, scope, usage,
			return_uri, redirect_uri, requested_scopes, created_at, expires_at, callback_at)
		VALUES ($1,$2,$3,'user-1','app_google','connect','member','members',
			'https://app.example.com/cb','https://conn.example.com/v1/oauth/callback/google','{}',$4,$5,$4)`,
		sha("state-claim"), sha("handle-claim"), org, now, expires); err != nil {
		t.Fatal(err)
	}

	if err := store.ClaimSession(ctx, sha("state-claim"), now); err != nil {
		t.Fatalf("first ClaimSession() error = %v", err)
	}
	// Long after, but before the session expires, the claim still stands.
	if err := store.ClaimSession(ctx, sha("state-claim"), now.Add(9*time.Minute)); !errors.Is(err, domain.ErrSessionExpired) {
		t.Fatalf("second ClaimSession() error = %v, want ErrSessionExpired", err)
	}
	// Consuming it in the connect transaction is what completes the session,
	// and the completion is single-use too.
	if _, err := store.CompleteConnect(ctx, sha("state-claim"), domain.Connection{
		ID: "con_claim", OrganizationID: org, AppID: "app_google", Provider: "google",
		CredentialType: domain.CredentialOAuth2, Scope: domain.ScopeMember, MemberID: "user-1",
		ProviderAccount: domain.ProviderAccount{ID: "acct-1"}, Scopes: []string{"email"},
		Status: domain.StatusActive, StatusChangedAt: now, EncryptedCredentials: sealed("con_claim"),
		CreatedBy: "user-1", CreatedAt: now, UpdatedAt: now,
	}, testEvent("connections.connection.connected.v1", now), now.Add(24*time.Hour)); err != nil {
		t.Fatalf("CompleteConnect() error = %v", err)
	}
	// Once completed, the session is spent even though it has not expired.
	if err := store.ClaimSession(ctx, sha("state-claim"), now.Add(time.Minute)); !errors.Is(err, domain.ErrSessionExpired) {
		t.Fatalf("ClaimSession() after completion error = %v, want ErrSessionExpired", err)
	}
	// Expiry is what finally reclaims the row.
	deleted, err := store.DeleteExpiredSessions(ctx, expires.Add(time.Minute), 100)
	if err != nil || deleted != 1 {
		t.Fatalf("DeleteExpiredSessions() = %d, %v, want 1", deleted, err)
	}
}
