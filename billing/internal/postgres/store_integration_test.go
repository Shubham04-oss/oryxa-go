package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/billing/internal/domain"
)

// TestStoreLifecycle runs the store against a real PostgreSQL, such as the one
// in docker-compose.yml:
//
//	BILLING_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./billing/internal/postgres
func TestStoreLifecycle(t *testing.T) {
	databaseURL := os.Getenv("BILLING_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("BILLING_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	organizationID := "it-org-" + suffix
	customerID := "it-cus-" + suffix
	subscriptionID := "it-sub-" + suffix
	eventPrefix := "it-evt-" + suffix
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM billing_subscriptions WHERE organization_id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM billing_organizations WHERE organization_id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM billing_webhook_deliveries WHERE webhook_id LIKE $1`, "it-msg-"+suffix+"%")
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM billing_outbox WHERE id LIKE $1`, eventPrefix+"%")
	}()
	now := time.Now().UTC().Truncate(time.Microsecond)
	limit := 20

	if _, _, err := store.Organization(ctx, organizationID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Organization() before any update error = %v, want not found", err)
	}

	// A first update creates the organization and writes it back with its
	// customer, name, entitlements and an event.
	err = store.UpdateOrganization(ctx, organizationID, func(state domain.OrganizationState) (domain.Change, error) {
		if state.Organization.ID != organizationID || state.Organization.Entitlements.Revision != 0 || state.Subscription != nil {
			t.Errorf("fresh organization state = %+v", state)
		}
		organization := state.Organization
		organization.Name = "Acme"
		organization.CustomerID = customerID
		organization.Entitlements = domain.Entitlements{Status: "unpaid", MemberLimit: &limit, Revision: 1}
		return domain.Change{Organization: &organization, NameUpdatedAt: now, Events: []domain.Event{testEvent(eventPrefix+"-1", now)}}, nil
	})
	if err != nil {
		t.Fatalf("UpdateOrganization() error = %v", err)
	}

	// Renames only move forward.
	for _, rename := range []struct {
		name string
		at   time.Time
	}{{"Acme Old", now.Add(-time.Hour)}, {"Acme New", now.Add(time.Hour)}} {
		err := store.UpdateOrganization(ctx, organizationID, func(state domain.OrganizationState) (domain.Change, error) {
			organization := state.Organization
			organization.Name = rename.name
			return domain.Change{Organization: &organization, NameUpdatedAt: rename.at}, nil
		})
		if err != nil {
			t.Fatalf("rename to %s error = %v", rename.name, err)
		}
	}
	organization, _, err := store.Organization(ctx, organizationID)
	if err != nil || organization.Name != "Acme New" || organization.CustomerID != customerID ||
		organization.Entitlements.MemberLimit == nil || *organization.Entitlements.MemberLimit != 20 || organization.Entitlements.Revision != 1 {
		t.Fatalf("Organization() = %+v, %v", organization, err)
	}

	// An update that fails writes nothing.
	failure := errors.New("refused")
	err = store.UpdateOrganization(ctx, organizationID, func(state domain.OrganizationState) (domain.Change, error) {
		return domain.Change{Events: []domain.Event{testEvent(eventPrefix+"-never", now)}}, failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("failing UpdateOrganization() error = %v", err)
	}

	// A webhook without organization metadata is matched through the customer,
	// applied once, and stores the subscription as current.
	grace := now.Add(72 * time.Hour)
	incoming := domain.Subscription{
		ID: subscriptionID, CustomerID: customerID, CustomerEmail: "billing@example.com", ProductID: "pdt_team", Quantity: 3,
		Status: domain.SubscriptionPastDue, NextBillingDate: now.Add(24 * time.Hour), PastDueEndsAt: &grace,
		BillingInterval: "Month", BillingIntervalCount: 1, EventAt: now,
	}
	apply := func(deliveryID string, subscription domain.Subscription) domain.WebhookOutcome {
		t.Helper()
		outcome, err := store.ApplyWebhook(ctx, domain.WebhookDelivery{ID: deliveryID, Type: "subscription." + string(subscription.Status)}, subscription,
			func(state domain.WebhookState) (domain.Change, error) {
				next := subscription
				next.OrganizationID = state.Organization.ID
				organization := state.Organization
				organization.SubscriptionID = next.ID
				return domain.Change{Subscription: &next, Organization: &organization, Events: []domain.Event{testEvent(eventPrefix+"-"+deliveryID, now)}}, nil
			})
		if err != nil {
			t.Fatalf("ApplyWebhook(%s) error = %v", deliveryID, err)
		}
		return outcome
	}
	if outcome := apply("it-msg-"+suffix+"-1", incoming); outcome != domain.WebhookApplied {
		t.Fatalf("outcome = %s, want applied", outcome)
	}
	if outcome := apply("it-msg-"+suffix+"-1", incoming); outcome != domain.WebhookDuplicate {
		t.Fatalf("redelivery outcome = %s, want duplicate", outcome)
	}
	stranger := incoming
	stranger.ID, stranger.CustomerID = "it-sub-stranger-"+suffix, "it-cus-stranger-"+suffix
	if outcome := apply("it-msg-"+suffix+"-2", stranger); outcome != domain.WebhookUnmatched {
		t.Fatalf("unknown customer outcome = %s, want unmatched", outcome)
	}

	organization, current, err := store.Organization(ctx, organizationID)
	if err != nil || current == nil || organization.SubscriptionID != subscriptionID {
		t.Fatalf("Organization() = %+v, current %+v, %v", organization, current, err)
	}
	if current.OrganizationID != organizationID || current.Status != domain.SubscriptionPastDue || current.Quantity != 3 ||
		current.PastDueEndsAt == nil || !current.PastDueEndsAt.Equal(grace) || !current.NextBillingDate.Equal(incoming.NextBillingDate) ||
		current.BillingInterval != "Month" || !current.EventAt.Equal(now) {
		t.Fatalf("current subscription = %+v", current)
	}

	// The webhook sees what it is updating: the stored subscription as previous.
	active := incoming
	active.Status, active.PastDueEndsAt, active.EventAt = domain.SubscriptionActive, nil, now.Add(time.Minute)
	active.NextBillingDate = now.Add(3 * 24 * time.Hour)
	_, err = store.ApplyWebhook(ctx, domain.WebhookDelivery{ID: "it-msg-" + suffix + "-3", Type: "subscription.active"}, active,
		func(state domain.WebhookState) (domain.Change, error) {
			if state.Previous == nil || state.Previous.Status != domain.SubscriptionPastDue || state.Current != nil {
				t.Errorf("webhook state = previous %+v, current %+v", state.Previous, state.Current)
			}
			next := active
			next.OrganizationID = state.Organization.ID
			return domain.Change{Subscription: &next}, nil
		})
	if err != nil {
		t.Fatalf("ApplyWebhook(active) error = %v", err)
	}

	// Renewals are claimed once per billing date.
	var reminded []string
	remind := func(renewal domain.Renewal) (*domain.Event, error) {
		reminded = append(reminded, renewal.Subscription.ID+" "+renewal.OrganizationName)
		event := testEvent(eventPrefix+"-renewal-"+renewal.Subscription.ID, now)
		return &event, nil
	}
	claimed, err := store.ClaimRenewals(ctx, now, 7*24*time.Hour, []string{"Month", "Year"}, 100, remind)
	if err != nil {
		t.Fatalf("ClaimRenewals() error = %v", err)
	}
	if claimed < 1 || !contains(reminded, subscriptionID+" Acme New") {
		t.Fatalf("claimed %d, reminded %v; want this subscription", claimed, reminded)
	}
	reminded = nil
	if _, err := store.ClaimRenewals(ctx, now, 7*24*time.Hour, []string{"Month", "Year"}, 100, remind); err != nil || contains(reminded, subscriptionID+" Acme New") {
		t.Fatalf("second ClaimRenewals() reminded %v, %v; want this subscription skipped", reminded, err)
	}

	ids, err := store.OrganizationIDs(ctx)
	if err != nil || !contains(ids, organizationID) {
		t.Fatalf("OrganizationIDs() = %v, %v", ids, err)
	}

	// Deleting marks the organization, which drops it from OrganizationIDs.
	err = store.UpdateOrganization(ctx, organizationID, func(state domain.OrganizationState) (domain.Change, error) {
		if state.Subscription == nil || state.Subscription.ID != subscriptionID {
			t.Errorf("organization state subscription = %+v", state.Subscription)
		}
		organization := state.Organization
		organization.Deleted = true
		return domain.Change{Organization: &organization}, nil
	})
	if err != nil {
		t.Fatalf("delete UpdateOrganization() error = %v", err)
	}
	if ids, _ := store.OrganizationIDs(ctx); contains(ids, organizationID) {
		t.Fatal("deleted organization still listed")
	}

	// The outbox holds exactly the events of the transactions that committed.
	var outbox int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM billing_outbox WHERE id LIKE $1`, eventPrefix+"%").Scan(&outbox); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if outbox != 3 {
		t.Fatalf("outbox events = %d, want 3 (organization, first webhook, renewal)", outbox)
	}
	// Claiming, marking and retrying are internal/outbox's own concern now:
	// see its table_integration_test.go.

	if err := store.PruneWebhookDeliveries(ctx, now.Add(-time.Hour)); err != nil {
		t.Fatalf("PruneWebhookDeliveries() error = %v", err)
	}
}

func TestIndexes(t *testing.T) {
	databaseURL := os.Getenv("BILLING_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("BILLING_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	var names []string
	if err := store.db.SelectContext(ctx, &names, `
		SELECT indexname FROM pg_indexes
		WHERE schemaname = current_schema()
		  AND indexname = ANY($1)
		ORDER BY indexname`, []string{
		"billing_organizations_active_idx",
		"billing_subscriptions_renewal_idx",
		"billing_outbox_pending_idx",
	}); err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	want := "billing_organizations_active_idx billing_outbox_pending_idx billing_subscriptions_renewal_idx"
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("indexes = %q, want %q", got, want)
	}
	var renewalDef, outboxDef string
	if err := store.db.GetContext(ctx, &renewalDef, `
		SELECT indexdef FROM pg_indexes
		WHERE schemaname = current_schema() AND indexname = 'billing_subscriptions_renewal_idx'`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(renewalDef, "billing_interval") {
		t.Fatalf("renewal indexdef = %s", renewalDef)
	}
	if err := store.db.GetContext(ctx, &outboxDef, `
		SELECT indexdef FROM pg_indexes
		WHERE schemaname = current_schema() AND indexname = 'billing_outbox_pending_idx'`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(outboxDef, "available_at, occurred_at, id") {
		t.Fatalf("outbox indexdef = %s", outboxDef)
	}
}

func testEvent(id string, at time.Time) domain.Event {
	data, _ := json.Marshal(map[string]string{"test": id})
	return domain.Event{ID: id, DetailType: "billing.integration-tested.v1", OccurredAt: at, Data: data}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
