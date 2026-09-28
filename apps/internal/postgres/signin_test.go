package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/signin"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// sealedValue stands in for an envelope: the store keeps it as it is.
func sealedValue(marker string) secrets.SealedData {
	return secrets.SealedData{Version: 1, Nonce: []byte("nonce-" + marker), Ciphertext: []byte("ciphertext-" + marker),
		EncryptedDataKey: []byte("key-" + marker)}
}

func testOAuthClient(t *testing.T, store *Store, organizationID, client, clientID string) signin.OAuthClient {
	t.Helper()
	secret := sealedValue(clientID)
	row := signin.OAuthClient{
		ID: "aoc_" + randomHex(8), OrganizationID: organizationID, Client: client, ClientID: clientID,
		SealedSecret: &secret, Settings: map[string]string{"audience": "https://api.example"},
		SealedFields: map[string]secrets.SealedData{"developer_key": sealedValue("dev")},
		Status:       signin.ClientActive, CreatedBy: "admin_1", CreatedAt: testClock, UpdatedAt: testClock,
	}
	if err := store.InsertOAuthClient(context.Background(), row); err != nil {
		t.Fatalf("InsertOAuthClient() = %v", err)
	}
	return row
}

func testFlow(connection domain.Connection, client signin.OAuthClient, expiresAt time.Time) signin.Flow {
	return signin.Flow{
		StateHash: "hash_" + randomHex(16), OrganizationID: connection.OrganizationID, MemberID: connection.MemberID,
		ConnectionID: connection.ID, Toolkit: connection.Toolkit, OAuthClientID: client.ID, CodeVerifier: "verifier",
		RedirectURI: "https://api.example/v1/apps/oauth/callback", ReturnTo: "https://app.example/tools",
		Settings: map[string]string{"subdomain": "acme"}, Sealed: map[string]secrets.SealedData{"signing_key": sealedValue("flow")},
		ExpiresAt: expiresAt,
	}
}

func testCredential(connection domain.Connection, kind, clientID string) signin.Credential {
	expires := testClock.Add(time.Hour)
	return signin.Credential{
		ConnectionID: connection.ID, OrganizationID: connection.OrganizationID, Kind: kind, OAuthClientID: clientID,
		Sealed:   map[string]secrets.SealedData{"oauth.access_token": sealedValue("access"), "oauth.refresh_token": sealedValue("refresh")},
		Settings: map[string]string{"account.label": "octocat", "account.ref": "42"}, Scopes: []string{"repo", "read:org"},
		ExpiresAt: &expires, NextCheckAt: testClock.Add(24 * time.Hour), UpdatedAt: testClock,
	}
}

func TestSigninFlowIsSpentOnceAndNeverAfterItExpires(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	connection, _ := testConnection(t, store, "org_1", "mem_1")
	client := testOAuthClient(t, store, "", "github", "oryxa-github")

	flow := testFlow(connection, client, testClock.Add(10*time.Minute))
	if err := store.InsertSigninFlow(ctx, flow); err != nil {
		t.Fatalf("InsertSigninFlow() = %v", err)
	}
	if err := store.InsertSigninFlow(ctx, flow); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a repeated state = %v, want ErrConflict", err)
	}
	consumed, err := store.ConsumeSigninFlow(ctx, flow.StateHash, testClock)
	if err != nil {
		t.Fatalf("ConsumeSigninFlow() = %v", err)
	}
	if consumed.ConnectionID != connection.ID || consumed.OAuthClientID != client.ID || consumed.CodeVerifier != "verifier" ||
		consumed.Toolkit != "github" || !maps.Equal(consumed.Settings, flow.Settings) ||
		!slices.Equal(consumed.Sealed["signing_key"].Ciphertext, flow.Sealed["signing_key"].Ciphertext) ||
		!consumed.ExpiresAt.Equal(flow.ExpiresAt) || consumed.ReturnTo != flow.ReturnTo || consumed.RedirectURI != flow.RedirectURI {
		t.Fatalf("consumed = %+v, want %+v", consumed, flow)
	}
	if _, err := store.ConsumeSigninFlow(ctx, flow.StateHash, testClock); !errors.Is(err, signin.ErrFlowNotFound) {
		t.Fatalf("a spent flow = %v, want ErrFlowNotFound", err)
	}

	expired := testFlow(connection, client, testClock.Add(time.Minute))
	if err := store.InsertSigninFlow(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeSigninFlow(ctx, expired.StateHash, testClock.Add(time.Minute)); !errors.Is(err, signin.ErrFlowNotFound) {
		t.Fatalf("an expired flow = %v, want ErrFlowNotFound", err)
	}
	if _, err := store.ConsumeSigninFlow(ctx, "hash_never_recorded", testClock); !errors.Is(err, signin.ErrFlowNotFound) {
		t.Fatalf("an unknown flow = %v, want ErrFlowNotFound", err)
	}

	// Two callbacks racing on one state get one flow between them.
	racing := testFlow(connection, client, testClock.Add(10*time.Minute))
	if err := store.InsertSigninFlow(ctx, racing); err != nil {
		t.Fatal(err)
	}
	var wins sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wins.Go(func() {
			_, err := store.ConsumeSigninFlow(ctx, racing.StateHash, testClock)
			results <- err
		})
	}
	wins.Wait()
	close(results)
	won := 0
	for err := range results {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, signin.ErrFlowNotFound):
			t.Fatalf("a racing consume = %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d callbacks spent one flow, want 1", won)
	}

	// A flow for a connection that does not exist is refused.
	ghost := testFlow(domain.Connection{ID: "con_ghost", OrganizationID: "org_1", MemberID: "mem_1", Toolkit: "github"}, client, testClock.Add(time.Minute))
	if err := store.InsertSigninFlow(ctx, ghost); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a flow for no connection = %v, want ErrNotFound", err)
	}
	// A later insert forgets the flows that expired a day before it began.
	if err := store.InsertSigninFlow(ctx, testFlow(connection, client, testClock.Add(48*time.Hour))); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM apps_signin_flows WHERE expires_at < $1`, testClock.Add(time.Hour)).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d old flows were kept", left)
	}
}

func TestInsertCredentialOnlyForAPendingConnectionThatHoldsNone(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	client := testOAuthClient(t, store, "", "github", "oryxa-github")
	connection, _ := testConnection(t, store, "org_1", "mem_1")

	credential := testCredential(connection, "oauth2_code", client.ID)
	wrongOrganization := credential
	wrongOrganization.OrganizationID = "org_2"
	if err := store.InsertCredential(ctx, wrongOrganization); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("another organization's connection = %v, want ErrConflict", err)
	}
	if err := store.InsertCredential(ctx, credential); err != nil {
		t.Fatalf("InsertCredential() = %v", err)
	}
	if err := store.InsertCredential(ctx, credential); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a second credential = %v, want ErrConflict", err)
	}
	read, err := store.Credential(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.Version != 1 || read.OrganizationID != "org_1" || read.MemberID != "mem_1" || read.Toolkit != "github" ||
		read.Status != "pending" || read.Kind != "oauth2_code" || read.OAuthClientID != client.ID ||
		!slices.Equal(read.Scopes, credential.Scopes) || !read.ExpiresAt.Equal(*credential.ExpiresAt) ||
		!maps.Equal(read.Settings, credential.Settings) || read.CheckFailures != 0 ||
		!slices.Equal(read.Sealed["oauth.refresh_token"].Nonce, credential.Sealed["oauth.refresh_token"].Nonce) {
		t.Fatalf("read = %+v, want %+v", read, credential)
	}
	if _, err := store.Credential(ctx, "con_none"); !errors.Is(err, signin.ErrNoCredential) {
		t.Fatalf("no credential = %v, want ErrNoCredential", err)
	}

	active := activate(t, store, mustConnection(t, store, "org_1", "mem_2"))
	if err := store.InsertCredential(ctx, testCredential(active, "oauth2_code", client.ID)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("an active connection = %v, want ErrConflict", err)
	}
}

// mustConnection is a pending connection of its own for member.
func mustConnection(t *testing.T, store *Store, organizationID, memberID string) domain.Connection {
	t.Helper()
	connection, _ := testConnection(t, store, organizationID, memberID)
	return connection
}

func TestReplaceCredentialMovesTheVersionOn(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	connection, _ := testConnection(t, store, "org_1", "mem_1")
	key := testCredential(connection, "api_token", "")
	key.Sealed = map[string]secrets.SealedData{"token": sealedValue("token-1")}
	key.ExpiresAt, key.Scopes = nil, nil

	if version, err := store.ReplaceCredential(ctx, key); err != nil || version != 1 {
		t.Fatalf("the first key = %d, %v", version, err)
	}
	// A refresh lease and a failed check are both cleared by a new key.
	if ok, err := store.AcquireCredentialLease(ctx, connection.ID, 1, "lease", testClock, testClock.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("AcquireCredentialLease() = %v, %v", ok, err)
	}
	if err := store.RecordCredentialCheck(ctx, connection.ID, testClock, 3); err != nil {
		t.Fatal(err)
	}
	activate(t, store, connection)
	key.Sealed = map[string]secrets.SealedData{"token": sealedValue("token-2")}
	if version, err := store.ReplaceCredential(ctx, key); err != nil || version != 2 {
		t.Fatalf("a replaced key = %d, %v, want version 2", version, err)
	}
	read, _ := store.Credential(ctx, connection.ID)
	if read.CheckFailures != 0 || !slices.Equal(read.Sealed["token"].Ciphertext, []byte("ciphertext-token-2")) || read.ExpiresAt != nil ||
		len(read.Scopes) != 0 {
		t.Fatalf("read = %+v", read)
	}
	if ok, _ := store.AcquireCredentialLease(ctx, connection.ID, 2, "lease-2", testClock, testClock.Add(time.Minute)); !ok {
		t.Fatal("the replaced key's lease was not cleared")
	}

	other := key
	other.Kind = "basic"
	if _, err := store.ReplaceCredential(ctx, other); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("another kind = %v, want ErrConflict", err)
	}
	if _, err := store.ExpireConnection(ctx, connection.ID, "credential_rejected", testClock, testEvent(t, "apps.connection.expired.v1")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceCredential(ctx, key); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("an expired connection = %v, want ErrConflict", err)
	}
}

func TestCredentialLeaseAndCompareAndSwap(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	client := testOAuthClient(t, store, "", "github", "oryxa-github")
	connection, _ := testConnection(t, store, "org_1", "mem_1")
	if err := store.InsertCredential(ctx, testCredential(connection, "oauth2_code", client.ID)); err != nil {
		t.Fatal(err)
	}

	// Of many refreshes racing for one credential, one takes the lease.
	var racers sync.WaitGroup
	won := make(chan string, 16)
	for index := range 16 {
		racers.Go(func() {
			token := "lease-" + string(rune('a'+index))
			ok, err := store.AcquireCredentialLease(ctx, connection.ID, 1, token, testClock, testClock.Add(time.Minute))
			if err != nil {
				t.Errorf("AcquireCredentialLease() = %v", err)
			}
			if ok {
				won <- token
			}
		})
	}
	racers.Wait()
	close(won)
	var holder string
	for token := range won {
		if holder != "" {
			t.Fatal("two refreshes took one lease")
		}
		holder = token
	}
	if holder == "" {
		t.Fatal("no refresh took the lease")
	}
	// It holds until it expires, and then another may take it.
	if ok, _ := store.AcquireCredentialLease(ctx, connection.ID, 1, "late", testClock.Add(59*time.Second), testClock.Add(2*time.Minute)); ok {
		t.Fatal("a held lease was taken")
	}
	if ok, _ := store.AcquireCredentialLease(ctx, connection.ID, 1, "after", testClock.Add(time.Minute), testClock.Add(2*time.Minute)); !ok {
		t.Fatal("an expired lease could not be taken")
	}
	// The first holder's lease is gone: it cannot release or save.
	if err := store.ReleaseCredentialLease(ctx, connection.ID, holder); err != nil {
		t.Fatal(err)
	}
	expires := testClock.Add(2 * time.Hour)
	refreshed := signin.Refreshed{
		Sealed:   map[string]secrets.SealedData{"oauth.access_token": sealedValue("access-2"), "oauth.refresh_token": sealedValue("refresh-2")},
		Settings: map[string]string{"account.label": "octocat"}, Scopes: []string{"repo"}, ExpiresAt: &expires,
		UpdatedAt: testClock.Add(time.Minute),
	}
	if _, err := store.SaveRefreshedCredential(ctx, connection.ID, holder, 1, refreshed); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a save under a lost lease = %v, want ErrConflict", err)
	}
	if _, err := store.SaveRefreshedCredential(ctx, connection.ID, "after", 7, refreshed); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a save at another version = %v, want ErrConflict", err)
	}
	version, err := store.SaveRefreshedCredential(ctx, connection.ID, "after", 1, refreshed)
	if err != nil || version != 2 {
		t.Fatalf("SaveRefreshedCredential() = %d, %v, want version 2", version, err)
	}
	read, _ := store.Credential(ctx, connection.ID)
	if read.Version != 2 || !read.ExpiresAt.Equal(expires) || !slices.Equal(read.Scopes, []string{"repo"}) ||
		!slices.Equal(read.Sealed["oauth.refresh_token"].Ciphertext, []byte("ciphertext-refresh-2")) {
		t.Fatalf("read = %+v", read)
	}
	// The save released the lease; the next refresh takes it at once.
	if ok, _ := store.AcquireCredentialLease(ctx, connection.ID, 2, "next", testClock.Add(time.Minute), testClock.Add(2*time.Minute)); !ok {
		t.Fatal("the save did not release the lease")
	}
	if err := store.ReleaseCredentialLease(ctx, connection.ID, "someone-else"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := store.AcquireCredentialLease(ctx, connection.ID, 2, "third", testClock.Add(time.Minute), testClock.Add(2*time.Minute)); ok {
		t.Fatal("a release by someone else freed the lease")
	}
	if err := store.ReleaseCredentialLease(ctx, connection.ID, "next"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := store.AcquireCredentialLease(ctx, connection.ID, 2, "third", testClock.Add(time.Minute), testClock.Add(2*time.Minute)); !ok {
		t.Fatal("the holder's release did not free the lease")
	}
	if deleted, err := store.DeleteCredential(ctx, connection.ID); err != nil || !deleted {
		t.Fatalf("DeleteCredential() = %v, %v", deleted, err)
	}
	if deleted, _ := store.DeleteCredential(ctx, connection.ID); deleted {
		t.Fatal("a credential was deleted twice")
	}
}

func TestClaimingChecksAndRevocations(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	credentialFor := func(member string, nextCheck time.Time, status string) domain.Connection {
		t.Helper()
		connection, _ := testConnection(t, store, "org_1", member)
		key := testCredential(connection, "api_token", "")
		key.NextCheckAt = nextCheck
		if _, err := store.ReplaceCredential(ctx, key); err != nil {
			t.Fatal(err)
		}
		switch status {
		case "active":
			connection = activate(t, store, connection)
		case "removed":
			if _, err := store.RemoveConnection(ctx, connection.ID, "removed_by_member", testClock, nil); err != nil {
				t.Fatal(err)
			}
		}
		return connection
	}
	older := credentialFor("mem_1", testClock.Add(-2*time.Hour), "active")
	newer := credentialFor("mem_2", testClock.Add(-time.Hour), "active")
	credentialFor("mem_3", testClock.Add(time.Hour), "active")
	credentialFor("mem_4", testClock.Add(-time.Hour), "pending")
	removed := credentialFor("mem_5", testClock.Add(-time.Hour), "removed")

	claimUntil := testClock.Add(10 * time.Minute)
	claimed, err := store.ClaimCredentialChecks(ctx, testClock, claimUntil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 2 || claimed[0].ConnectionID != older.ID || claimed[1].ConnectionID != newer.ID ||
		claimed[0].Status != "active" || !claimed[0].NextCheckAt.Equal(claimUntil) {
		t.Fatalf("claimed = %+v, want the two due active ones, oldest first", claimed)
	}
	if again, _ := store.ClaimCredentialChecks(ctx, testClock, claimUntil, 10); len(again) != 0 {
		t.Fatalf("claimed again = %+v, want none", again)
	}
	revocations, err := store.ClaimRevocations(ctx, testClock, claimUntil, 10)
	if err != nil || len(revocations) != 1 || revocations[0].ConnectionID != removed.ID {
		t.Fatalf("ClaimRevocations() = %+v, %v, want the removed connection's", revocations, err)
	}

	if err := store.RecordCredentialCheck(ctx, older.ID, testClock.Add(5*time.Minute), 1); err != nil {
		t.Fatal(err)
	}
	// A use puts the check off, never brings it forward, and clears the
	// failures, for an active connection only.
	if err := store.MarkCredentialUsed(ctx, older.ID, testClock.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	read, _ := store.Credential(ctx, older.ID)
	if !read.NextCheckAt.Equal(testClock.Add(12*time.Hour)) || read.CheckFailures != 0 {
		t.Fatalf("after a use: %+v", read)
	}
	if err := store.MarkCredentialUsed(ctx, older.ID, testClock.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if read, _ := store.Credential(ctx, older.ID); !read.NextCheckAt.Equal(testClock.Add(12 * time.Hour)) {
		t.Fatalf("a use brought the check forward to %v", read.NextCheckAt)
	}
	if err := store.RecordCredentialCheck(ctx, removed.ID, testClock, 2); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkCredentialUsed(ctx, removed.ID, testClock.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if read, _ := store.Credential(ctx, removed.ID); read.CheckFailures != 2 || !read.NextCheckAt.Equal(testClock) {
		t.Fatalf("a use of a removed connection's credential changed its retries: %+v", read)
	}

	// Two replicas claiming at once take different credentials.
	for member := range 12 {
		credentialFor("mem_race_"+string(rune('a'+member)), testClock.Add(-time.Minute), "active")
	}
	var replicas sync.WaitGroup
	var mu sync.Mutex
	seen := map[string]int{}
	for range 3 {
		replicas.Go(func() {
			batch, err := store.ClaimCredentialChecks(ctx, testClock, claimUntil, 5)
			if err != nil {
				t.Errorf("ClaimCredentialChecks() = %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			for _, credential := range batch {
				seen[credential.ConnectionID]++
			}
		})
	}
	replicas.Wait()
	for id, times := range seen {
		if times != 1 {
			t.Fatalf("%s was claimed %d times", id, times)
		}
	}
	if len(seen) != 12 {
		t.Fatalf("%d credentials were claimed, want 12", len(seen))
	}
}

func TestGrantSharedLooksAcrossOrganizationsAtLiveConnections(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	oryxas := testOAuthClient(t, store, "", "google", "oryxa-google")
	organizations := testOAuthClient(t, store, "org_2", "google", "org-google")
	mail, _ := testConnection(t, store, "org_1", "mem_1")
	drive, _ := testConnection(t, store, "org_9", "mem_1")
	for _, connection := range []domain.Connection{mail, drive} {
		credential := testCredential(connection, "oauth2_code", oryxas.ID)
		credential.Settings = map[string]string{"account.label": "ada@example.com"}
		if err := store.InsertCredential(ctx, credential); err != nil {
			t.Fatal(err)
		}
	}
	calendar, _ := testConnection(t, store, "org_2", "mem_1")
	credential := testCredential(calendar, "oauth2_code", organizations.ID)
	credential.Settings = map[string]string{"account.label": "ada@example.com"}
	if err := store.InsertCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}

	shared := func(connectionID, clientID, label string) bool {
		t.Helper()
		answer, err := store.GrantShared(ctx, connectionID, clientID, label)
		if err != nil {
			t.Fatal(err)
		}
		return answer
	}
	if !shared(mail.ID, "oryxa-google", "ada@example.com") {
		t.Fatal("the same account through the same client, in another organization, is not shared")
	}
	if shared(calendar.ID, "org-google", "ada@example.com") {
		t.Fatal("another client's grant counted as shared")
	}
	if shared(mail.ID, "oryxa-google", "grace@example.com") {
		t.Fatal("another account's grant counted as shared")
	}
	if _, err := store.RemoveConnection(ctx, drive.ID, "removed_by_member", testClock, nil); err != nil {
		t.Fatal(err)
	}
	if shared(mail.ID, "oryxa-google", "ada@example.com") {
		t.Fatal("a removed connection's grant counted as shared")
	}
}

func TestOAuthClientsKeepOneLiveRowAndEveryReplacedOne(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if _, err := store.LiveOAuthClient(ctx, "org_1", "github"); !errors.Is(err, signin.ErrNoClient) {
		t.Fatalf("no client = %v, want ErrNoClient", err)
	}
	first := testOAuthClient(t, store, "org_1", "github", "org-github")
	read, err := store.LiveOAuthClient(ctx, "org_1", "github")
	if err != nil || read.ID != first.ID || read.ClientID != "org-github" || read.CreatedBy != "admin_1" ||
		!slices.Equal(read.SealedSecret.Ciphertext, first.SealedSecret.Ciphertext) ||
		!maps.Equal(read.Settings, first.Settings) || len(read.SealedFields) != 1 {
		t.Fatalf("LiveOAuthClient() = %+v, %v", read, err)
	}
	// Oryxa's default is another organization's row, "".
	if _, err := store.LiveOAuthClient(ctx, "", "github"); !errors.Is(err, signin.ErrNoClient) {
		t.Fatalf("Oryxa's default = %v, want none yet", err)
	}

	second := testOAuthClient(t, store, "org_1", "github", "org-github-v2")
	if live, _ := store.LiveOAuthClient(ctx, "org_1", "github"); live.ID != second.ID {
		t.Fatalf("live = %s, want the new client %s", live.ID, second.ID)
	}
	replaced, err := store.OAuthClient(ctx, first.ID)
	if err != nil || replaced.Status != signin.ClientRemoved || replaced.SealedSecret == nil {
		t.Fatalf("the replaced client = %+v, %v, want it kept, removed, with its secret", replaced, err)
	}
	if updated, err := store.UpdateOAuthClient(ctx, replaced); err != nil || updated {
		t.Fatalf("updating a replaced client = %v, %v, want false", updated, err)
	}

	rotated := second
	secret := sealedValue("rotated")
	rotated.SealedSecret, rotated.Settings, rotated.UpdatedAt = &secret, map[string]string{}, testClock.Add(time.Hour)
	if updated, err := store.UpdateOAuthClient(ctx, rotated); err != nil || !updated {
		t.Fatalf("UpdateOAuthClient() = %v, %v", updated, err)
	}
	if live, _ := store.LiveOAuthClient(ctx, "org_1", "github"); !slices.Equal(live.SealedSecret.Ciphertext, secret.Ciphertext) ||
		len(live.Settings) != 0 || !live.UpdatedAt.Equal(testClock.Add(time.Hour)) {
		t.Fatalf("the rotated client = %+v", live)
	}

	// A row that only refuses Oryxa's default carries no secret.
	refusal := signin.OAuthClient{
		ID: "aoc_" + randomHex(8), OrganizationID: "org_2", Client: "github",
		Settings: map[string]string{signin.SettingRefuseDefault: "true"}, Status: signin.ClientActive,
		CreatedAt: testClock, UpdatedAt: testClock,
	}
	if err := store.InsertOAuthClient(ctx, refusal); err != nil {
		t.Fatal(err)
	}
	if live, err := store.LiveOAuthClient(ctx, "org_2", "github"); err != nil || live.SealedSecret != nil || live.ClientID != "" ||
		live.Settings[signin.SettingRefuseDefault] != "true" || len(live.SealedFields) != 0 {
		t.Fatalf("the refusal = %+v, %v", live, err)
	}

	if removed, err := store.RemoveOAuthClient(ctx, "org_1", "github", testClock.Add(2*time.Hour)); err != nil || !removed {
		t.Fatalf("RemoveOAuthClient() = %v, %v", removed, err)
	}
	if removed, _ := store.RemoveOAuthClient(ctx, "org_1", "github", testClock.Add(2*time.Hour)); removed {
		t.Fatal("a client was removed twice")
	}
	if _, err := store.OAuthClient(ctx, second.ID); err != nil {
		t.Fatalf("a removed client could not be read by id: %v", err)
	}
	if _, err := store.OAuthClient(ctx, "aoc_none"); !errors.Is(err, signin.ErrNoClient) {
		t.Fatalf("no such client = %v, want ErrNoClient", err)
	}
	// Only one live row per organization and registration, whatever races.
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO apps_oauth_clients (id, organization_id, client, client_id, status, created_at, updated_at)
		VALUES ('aoc_dup_1', 'org_3', 'github', 'a', 'active', now(), now()),
		       ('aoc_dup_2', 'org_3', 'github', 'b', 'active', now(), now())`); err == nil {
		t.Fatal("two live clients for one organization and registration were stored")
	}
}

func TestBrokerIsComposioUnlessSaidOtherwise(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	connection, _ := testConnection(t, store, "org_1", "mem_1")
	var broker string
	if err := store.pool.QueryRow(ctx, `SELECT broker FROM apps_connections WHERE id = $1`, connection.ID).Scan(&broker); err != nil {
		t.Fatal(err)
	}
	if broker != "composio" {
		t.Fatalf("broker = %q, want composio", broker)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE apps_connections SET broker = 'oryxa' WHERE id = $1`, connection.ID); err != nil {
		t.Fatalf("broker oryxa = %v", err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE apps_connections SET broker = 'nango' WHERE id = $1`, connection.ID); err == nil {
		t.Fatal("an unknown broker was stored")
	}
}

// plainKeys stands in for KMS in a test: a wrapped key unwraps only with
// the encryption context it was made for.
type plainKeys struct{}

func (plainKeys) GenerateDataKey(_ context.Context, _ string, encryptionContext map[string]string) ([]byte, []byte, error) {
	key := []byte(randomHex(16))
	wrapped, err := json.Marshal(map[string]any{"key": key, "context": encryptionContext})
	return key, wrapped, err
}

func (plainKeys) DecryptDataKey(_ context.Context, wrapped []byte, encryptionContext map[string]string) ([]byte, error) {
	var unwrapped struct {
		Key     []byte            `json:"key"`
		Context map[string]string `json:"context"`
	}
	if err := json.Unmarshal(wrapped, &unwrapped); err != nil || !maps.Equal(unwrapped.Context, encryptionContext) {
		return nil, errors.New("the encryption context does not match")
	}
	return unwrapped.Key, nil
}

type signinIntegration struct{ manifest integration.Manifest }

func (i signinIntegration) Manifest() integration.Manifest         { return i.manifest }
func (i signinIntegration) Tools() map[string]integration.ToolFunc { return nil }

// TestSigninRunsOnThisStore signs a member in, refreshes, checks, saves a
// key and revokes, through signin itself, on this store.
func TestSigninRunsOnThisStore(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()

	var mu sync.Mutex
	live := map[string]bool{}
	serial, revoked := 0, 0
	issue := func(w http.ResponseWriter, refresh bool) {
		serial++
		answer := map[string]any{"access_token": fmt.Sprintf("at-%d", serial), "token_type": "bearer", "expires_in": 3600}
		live[answer["access_token"].(string)] = true
		if refresh {
			answer["refresh_token"] = fmt.Sprintf("rt-%d", serial)
			live[answer["refresh_token"].(string)] = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(answer)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = r.ParseForm()
		if r.PostForm.Get("client_secret") != "oryxa-github-client-secret" {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			issue(w, r.PostForm.Get("code") == "good" && r.PostForm.Get("code_verifier") != "")
		case "refresh_token":
			if !live[r.PostForm.Get("refresh_token")] {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			delete(live, r.PostForm.Get("refresh_token"))
			issue(w, true)
		}
	})
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !live[token] && token != "pat-0123456789abcdef" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"login": "octocat", "id": 7}`))
	})
	mux.HandleFunc("POST /revoke", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = r.ParseForm()
		delete(live, r.PostForm.Get("token"))
		revoked++
	})
	app := httptest.NewTLSServer(mux)
	t.Cleanup(app.Close)

	registry := integration.NewRegistry()
	if err := registry.Register(signinIntegration{integration.Manifest{
		Toolkit: "github", Version: 1, Name: "GitHub", APIBase: app.URL + "/api",
		Auth: integration.Auth{
			Methods: []integration.Method{
				{Kind: integration.KindOAuth2Code, OAuth2: &integration.OAuth2{
					Client: "github", AuthURL: app.URL + "/authorize", TokenURL: app.URL + "/token", RevokeURL: app.URL + "/revoke",
					Scopes: []string{"repo"}, PKCE: true, ClientAuth: integration.ClientAuthBody, Rotating: true,
				}},
				{Kind: integration.KindAPIToken, Place: &integration.Place{In: integration.InHeader, Name: "Authorization", Template: "Bearer {token}"}},
			},
			Test: integration.Test{Path: "/me", Label: "/login", AccountRef: "/id"},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	var clockMu sync.Mutex
	now := time.Now().UTC()
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	advance := func(d time.Duration) { clockMu.Lock(); defer clockMu.Unlock(); now = now.Add(d) }
	service, err := signin.New(signin.Dependencies{
		Store: store, Sealer: secrets.Envelope{Keys: plainKeys{}, KeyID: "alias/test", Service: signin.SealService},
		Integrations: registry, HTTP: app.Client(), Clock: clock,
	}, signin.Config{RedirectURI: "https://api.example/v1/apps/oauth/callback"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.PutClient(ctx, signin.ClientInput{Client: "github", ClientID: "oryxa-github", ClientSecret: "oryxa-github-client-secret"}); err != nil {
		t.Fatalf("PutClient() = %v", err)
	}

	connection, _ := testConnection(t, store, "org_1", "mem_1")
	started, err := service.StartOAuth(ctx, signin.StartRequest{
		OrganizationID: "org_1", MemberID: "mem_1", ConnectionID: connection.ID, Toolkit: "github",
	})
	if err != nil {
		t.Fatalf("StartOAuth() = %v", err)
	}
	completion, err := service.CompleteOAuth(ctx, signin.Callback{State: started.State, CookieState: started.State, Code: "good"})
	if err != nil || completion.Account != (signin.Account{Label: "octocat", Ref: "7"}) {
		t.Fatalf("CompleteOAuth() = %+v, %v", completion, err)
	}
	activate(t, store, connection)
	session, err := service.Session(ctx, connection.ID)
	if err != nil {
		t.Fatalf("Session() = %v", err)
	}
	if token, err := session.Token(ctx); err != nil || token != "at-1" {
		t.Fatalf("Token() = %q, %v", token, err)
	}
	advance(59 * time.Minute)
	if token, err := session.Token(ctx); err != nil || token != "at-2" {
		t.Fatalf("Token() after a refresh = %q, %v", token, err)
	}
	if credential, _ := store.Credential(ctx, connection.ID); credential.Version != 2 {
		t.Fatalf("version = %d, want 2", credential.Version)
	}
	if err := store.RecordCredentialCheck(ctx, connection.ID, clock(), 0); err != nil {
		t.Fatal(err)
	}
	checks, err := service.CheckDue(ctx, 10)
	if err != nil || len(checks) != 1 || checks[0].Outcome != signin.CheckPassed {
		t.Fatalf("CheckDue() = %+v, %v", checks, err)
	}

	keyed, _ := testConnection(t, store, "org_1", "mem_2")
	if saved, err := service.SaveCredential(ctx, signin.CredentialRequest{
		OrganizationID: "org_1", ConnectionID: keyed.ID, Toolkit: "github", Kind: integration.KindAPIToken,
		Fields: map[string]string{"token": "pat-0123456789abcdef"},
	}); err != nil || saved.Version != 1 {
		t.Fatalf("SaveCredential() = %+v, %v", saved, err)
	}

	revocation, err := service.Revoke(ctx, connection.ID)
	mu.Lock()
	revocations := revoked
	mu.Unlock()
	if err != nil || !revocation.ProviderRevoked || revocations != 1 {
		t.Fatalf("Revoke() = %+v, %v, with %d revocations", revocation, err, revocations)
	}
	if _, err := store.Credential(ctx, connection.ID); !errors.Is(err, signin.ErrNoCredential) {
		t.Fatalf("the revoked credential = %v, want it gone", err)
	}
}
