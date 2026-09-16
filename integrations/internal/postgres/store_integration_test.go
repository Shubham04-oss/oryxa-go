package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// openTestStore migrates a fresh schema and drops it afterwards, so the test
// never leaves tables in a shared development database.
// INTEGRATIONS_TEST_DATABASE_URL must be a postgres:// URL.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	databaseURL := os.Getenv("INTEGRATIONS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("INTEGRATIONS_TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	schemaName := "integrations_test_" + randomHex(6)
	schema := pgx.Identifier{schemaName}.Sanitize()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		_ = admin.Close(cleanup)
	})

	scoped, err := url.Parse(databaseURL)
	if err != nil || scoped.Scheme == "" {
		t.Fatalf("INTEGRATIONS_TEST_DATABASE_URL must be a postgres:// URL")
	}
	query := scoped.Query()
	query.Set("search_path", schemaName+",public")
	scoped.RawQuery = query.Encode()

	store, err := Open(ctx, scoped.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	for range 2 {
		if err := store.Migrate(ctx); err != nil {
			t.Fatalf("Migrate() error = %v", err)
		}
	}
	return store
}

// TestMigrateIsRepeatable asserts a third application on top of the two
// openTestStore already runs, so a non-idempotent migration statement fails
// loudly here rather than only showing up as a flaky schema elsewhere.
func TestMigrateIsRepeatable(t *testing.T) {
	store := openTestStore(t)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate() third call error = %v", err)
	}
}

func randomHex(size int) string {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value)
}

// routeFor turns a short label into a unique value that satisfies the
// route column's 32-64 character check.
func routeFor(label string) string {
	route := "route_" + label + "_" + randomHex(4)
	if len(route) < 32 {
		route += strings.Repeat("0", 32-len(route))
	}
	if len(route) > 64 {
		route = route[:64]
	}
	return route
}

// sealedData is a non-empty envelope-encrypted value, as CreateSubscription
// and PutAppWebhook expect.
func sealedData(label string) *domain.SealedData {
	return &domain.SealedData{
		Version:          1,
		Nonce:            []byte("nonce-" + label),
		Ciphertext:       []byte("ciphertext-" + label),
		EncryptedDataKey: []byte("key-" + label),
	}
}

func testEvent(detailType string, at time.Time) domain.Event {
	return domain.Event{ID: "evt_" + randomHex(8), DetailType: detailType, OccurredAt: at, Data: json.RawMessage(`{}`)}
}

// eventFor is an eventFor callback that ignores the subscription it is given;
// use it where the test does not need to inspect what the callback received.
func eventFor(detailType string, at time.Time) func(domain.Subscription) (domain.Event, error) {
	return func(domain.Subscription) (domain.Event, error) {
		return testEvent(detailType, at), nil
	}
}

// neverCalled fails the test if the store calls it, for the branches of
// Activate/Stop/FailSubscription that must not write an event.
func neverCalled(t *testing.T) func(domain.Subscription) (domain.Event, error) {
	t.Helper()
	return func(subscription domain.Subscription) (domain.Event, error) {
		t.Fatalf("eventFor called unexpectedly for subscription %s", subscription.ID)
		return domain.Event{}, nil
	}
}

// newSubscription is a valid subscription ready for CreateSubscription, with
// organization scope and every optional field populated so read-back tests
// have something to check; callers override whatever the case needs.
func newSubscription(organizationID, id, connectionID, appID, createdBy string, status domain.SubscriptionStatus, at time.Time) domain.Subscription {
	return domain.Subscription{
		ID:              id,
		OrganizationID:  organizationID,
		ConnectionID:    connectionID,
		AppID:           appID,
		Provider:        "github",
		Scope:           "organization",
		ProviderAccount: domain.ProviderAccount{ID: "acct_" + id, Name: "Account " + id},
		Target:          map[string]string{"repo": "octo/hello", "branch": "main"},
		Events:          []string{"issue.created", "issue.updated"},
		Route:           routeFor(id),
		AccountKey:      "account-key-" + id,
		EncryptedSecret: sealedData(id),
		Status:          status,
		CreatedBy:       createdBy,
		CreatedAt:       at,
		UpdatedAt:       at,
	}
}

func idsOf(subscriptions []domain.Subscription) []string {
	ids := make([]string, len(subscriptions))
	for i, subscription := range subscriptions {
		ids[i] = subscription.ID
	}
	return ids
}

func timePtrEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func sealedEqual(a, b *domain.SealedData) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Version == b.Version && string(a.Nonce) == string(b.Nonce) &&
		string(a.Ciphertext) == string(b.Ciphertext) && string(a.EncryptedDataKey) == string(b.EncryptedDataKey)
}
