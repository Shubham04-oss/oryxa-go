package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// TestAcquireLeaseExactlyOneWins fires many concurrent AcquireLease calls at
// the same version and confirms exactly one takes the lease, none error, and
// a later caller cannot take it until it expires.
func TestAcquireLeaseExactlyOneWins(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	org := "org_lease_race"
	app := createApp(t, store, org, "app_google", domain.CredentialOAuth2, now)
	connection := createConnection(t, store, app, "con_a", "user-1", "account-1", now)

	const attempts = 8
	var wins int32
	var wg sync.WaitGroup
	errs := make([]error, attempts)
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, err := store.AcquireLease(ctx, org, connection.ID, connection.Version, fmt.Sprintf("token-%d", i), now, now.Add(time.Minute))
			errs[i] = err
			if ok {
				atomic.AddInt32(&wins, 1)
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("AcquireLease() goroutine %d error = %v", i, err)
		}
	}
	if wins != 1 {
		t.Fatalf("expected exactly one AcquireLease() to win, got %d", wins)
	}

	// While it is held, a later caller at the same version cannot take it.
	if ok, err := store.AcquireLease(ctx, org, connection.ID, connection.Version, "token-late", now, now.Add(time.Minute)); err != nil || ok {
		t.Fatalf("AcquireLease() while held = %v, %v, want false", ok, err)
	}

	// Once it expires, another caller may take it.
	if ok, err := store.AcquireLease(ctx, org, connection.ID, connection.Version, "token-after-expiry", now.Add(2*time.Minute), now.Add(3*time.Minute)); err != nil || !ok {
		t.Fatalf("AcquireLease() after expiry = %v, %v, want true", ok, err)
	}

	// The version guard rejects a caller working off a stale version.
	if ok, err := store.AcquireLease(ctx, org, connection.ID, connection.Version-1, "token-stale-version", now.Add(4*time.Minute), now.Add(5*time.Minute)); err != nil || ok {
		t.Fatalf("AcquireLease(stale version) = %v, %v, want false", ok, err)
	}
}

func TestSaveRenewedCredentialsAndReleaseLease(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	org := "org_renew_credentials"
	app := createApp(t, store, org, "app_google", domain.CredentialOAuth2, now)
	connection := createConnection(t, store, app, "con_a", "user-1", "account-1", now)

	if ok, err := store.AcquireLease(ctx, org, connection.ID, connection.Version, "lease-1", now, now.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("AcquireLease() = %v, %v", ok, err)
	}

	if _, err := store.SaveRenewedCredentials(ctx, org, connection.ID, "wrong-token", connection.Version, sealed("renewed"), nil, now.Add(30*time.Second)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("SaveRenewedCredentials(wrong token) error = %v, want ErrConflict", err)
	}

	// A save at a version that moved while the lease was held is refused, and
	// keeps the lease for its holder to release.
	if _, err := store.SaveRenewedCredentials(ctx, org, connection.ID, "lease-1", connection.Version+1, sealed("renewed"), nil, now.Add(30*time.Second)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("SaveRenewedCredentials(stale version) error = %v, want ErrConflict", err)
	}
	version, err := store.SaveRenewedCredentials(ctx, org, connection.ID, "lease-1", connection.Version, sealed("renewed"), nil, now.Add(time.Minute))
	if err != nil || version != connection.Version+1 {
		t.Fatalf("SaveRenewedCredentials(nil scopes) = %d, %v", version, err)
	}
	reloaded, err := store.Connection(ctx, org, connection.ID)
	if err != nil || !slices.Equal(reloaded.Scopes, connection.Scopes) ||
		reloaded.EncryptedCredentials == nil || string(reloaded.EncryptedCredentials.Ciphertext) != "ciphertext-renewed" {
		t.Fatalf("connection after SaveRenewedCredentials(nil scopes) = %+v %v, want scopes unchanged", reloaded, err)
	}

	// The lease was released by the save: a fresh acquire at the new version
	// succeeds immediately rather than waiting for expiry.
	if ok, err := store.AcquireLease(ctx, org, connection.ID, version, "lease-2", now.Add(2*time.Minute), now.Add(3*time.Minute)); err != nil || !ok {
		t.Fatalf("AcquireLease() after save = %v, %v, want true", ok, err)
	}

	version, err = store.SaveRenewedCredentials(ctx, org, connection.ID, "lease-2", version, sealed("renewed-2"), []string{}, now.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if reloaded, err := store.Connection(ctx, org, connection.ID); err != nil || len(reloaded.Scopes) != 0 {
		t.Fatalf("connection after SaveRenewedCredentials(empty scopes) = %+v %v, want empty scopes", reloaded, err)
	}

	if ok, err := store.AcquireLease(ctx, org, connection.ID, version, "lease-3", now.Add(5*time.Minute), now.Add(time.Hour)); err != nil || !ok {
		t.Fatal(err)
	}
	if err := store.ReleaseLease(ctx, org, connection.ID, "lease-3"); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.AcquireLease(ctx, org, connection.ID, version, "lease-4", now.Add(6*time.Minute), now.Add(7*time.Minute)); err != nil || !ok {
		t.Fatalf("AcquireLease() after release = %v, %v, want true", ok, err)
	}

	// Releasing with a stale token changes nothing: lease-4 is still held.
	if err := store.ReleaseLease(ctx, org, connection.ID, "lease-3"); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.AcquireLease(ctx, org, connection.ID, version, "lease-5", now.Add(6*time.Minute), now.Add(8*time.Minute)); err != nil || ok {
		t.Fatalf("AcquireLease() after stale release = %v, %v, want false", ok, err)
	}

	// Releasing an unrelated connection's lease is harmless.
	if err := store.ReleaseLease(ctx, org, "con_missing", "token"); err != nil {
		t.Fatalf("ReleaseLease(missing connection) error = %v, want nil", err)
	}
}

// TestRenewalLosesToAReconnect replays the race where a reconnect lands while
// a renewal is calling the provider: the reconnect must win, and the stale
// renewal must neither overwrite its credentials nor mark it needs_reconnect.
func TestRenewalLosesToAReconnect(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	org := "org_renewal_race"
	app := createApp(t, store, org, "app_google", domain.CredentialOAuth2, now)
	connection := createConnection(t, store, app, "con_a", "user-1", "account-1", now)

	if ok, err := store.AcquireLease(ctx, org, connection.ID, connection.Version, "renewal", now, now.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("AcquireLease() = %v, %v", ok, err)
	}
	reconnected := connection
	reconnected.EncryptedCredentials, reconnected.UpdatedAt = sealed("reconnected"), now.Add(time.Second)
	saved, err := store.ReplaceCredentials(ctx, reconnected, connection.Version, testEvent("connections.connection.reconnected.v1", now))
	if err != nil {
		t.Fatalf("ReplaceCredentials() during a renewal error = %v", err)
	}
	if _, err := store.SaveRenewedCredentials(ctx, org, connection.ID, "renewal", connection.Version, sealed("stale"), nil, now.Add(2*time.Second)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale SaveRenewedCredentials() error = %v, want ErrConflict", err)
	}
	marked, err := store.MarkNeedsReconnect(ctx, org, connection.ID, connection.Version, domain.ReconnectRefreshRejected, now.Add(2*time.Second),
		func(domain.Connection) (domain.Event, error) {
			return testEvent("connections.connection.needs-reconnect.v1", now), nil
		})
	if err != nil || marked {
		t.Fatalf("stale MarkNeedsReconnect() = %v, %v, want unchanged", marked, err)
	}
	if err := store.ReleaseLease(ctx, org, connection.ID, "renewal"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.Connection(ctx, org, connection.ID)
	if err != nil || reloaded.Status != domain.StatusActive || reloaded.Version != saved.Version ||
		string(reloaded.EncryptedCredentials.Ciphertext) != "ciphertext-reconnected" {
		t.Fatalf("connection after the race = %+v %v", reloaded, err)
	}
	if ok, err := store.AcquireLease(ctx, org, connection.ID, saved.Version, "next", now.Add(3*time.Second), now.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("AcquireLease() after the stale renewal released = %v, %v", ok, err)
	}
}

// TestProviderGrantLockSerializes confirms a second holder of the same app
// and account waits for the first, while another account does not.
func TestProviderGrantLockSerializes(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	entered := make(chan string, 3)
	release := make(chan struct{})
	var wait sync.WaitGroup
	hold := func(name, account string) {
		defer wait.Done()
		if err := store.WithProviderGrantLock(ctx, "org_lock", "app_1", account, func(context.Context) error {
			entered <- name
			<-release
			return nil
		}); err != nil {
			t.Errorf("WithProviderGrantLock(%s) error = %v", name, err)
		}
	}
	wait.Add(1)
	go hold("first", "account-1")
	if got := <-entered; got != "first" {
		t.Fatalf("entered %s first", got)
	}
	wait.Add(2)
	go hold("same account", "account-1")
	go hold("other account", "account-2")
	select {
	case got := <-entered:
		if got != "other account" {
			t.Fatalf("%s entered while the same account was locked", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("another account's lock waited")
	}
	select {
	case got := <-entered:
		t.Fatalf("%s entered while the same account was locked", got)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	wait.Wait()
	if got := <-entered; got != "same account" {
		t.Fatalf("entered %s after release", got)
	}
}
