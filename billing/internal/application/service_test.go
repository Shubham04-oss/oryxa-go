package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	billingeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/billing/v1"

	"github.com/Shubham04-oss/oryxa-go/billing/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/catalog"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/dodo"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/domain"
)

var testNow = time.Date(2026, time.September, 13, 12, 0, 0, 0, time.UTC)

const testCatalog = `{
	"unpaid": {"member_limit": 3},
	"plans": [
		{"key": "team", "product_id": "pdt_team", "member_limit": 20},
		{"key": "seats", "product_id": "pdt_seats", "member_limit_per_unit": 1, "min_quantity": 5}
	]
}`

var (
	owner  = authclient.Credentials{Authorization: "owner-1"}
	member = authclient.Credentials{Authorization: "member-1"}
)

type harness struct {
	service  *Service
	store    *fakeStore
	payments *fakePayments
}

func newHarness(t *testing.T) harness {
	t.Helper()
	plans, err := catalog.Parse([]byte(testCatalog))
	if err != nil {
		t.Fatalf("catalog.Parse() error = %v", err)
	}
	store, payments := newFakeStore(), &fakePayments{}
	memberships := fakeMemberships{members: map[string]domain.Membership{
		"org_1 owner-1":  {OrganizationID: "org_1", OrganizationName: "Acme", UserID: "owner-1", Email: "asha@example.com", DisplayName: "Asha", Role: domain.RoleOwner},
		"org_1 member-1": {OrganizationID: "org_1", OrganizationName: "Acme", UserID: "member-1", Email: "ravi@example.com", Role: "member"},
	}}
	service := New(store, payments, memberships, plans, Config{
		BillingURL:            "https://app.example.com/settings/billing?tab=plan",
		RenewalReminderWindow: 7 * 24 * time.Hour,
	}, slog.New(slog.DiscardHandler))
	service.now = func() time.Time { return testNow }
	return harness{service: service, store: store, payments: payments}
}

func TestStartCheckoutCreatesTheOrganizationsCustomerOnce(t *testing.T) {
	h := newHarness(t)

	url, err := h.service.StartCheckout(context.Background(), owner, "org_1", "seats", 12)
	if err != nil {
		t.Fatalf("StartCheckout() error = %v", err)
	}
	if url != "https://checkout.test/pdt_seats" {
		t.Fatalf("checkout url = %q", url)
	}
	if len(h.payments.customers) != 1 {
		t.Fatalf("customers created = %d, want 1", len(h.payments.customers))
	}
	if got := h.payments.customers[0]; got != (dodo.CustomerRequest{OrganizationID: "org_1", Email: "asha@example.com", Name: "Asha"}) {
		t.Fatalf("customer request = %+v", got)
	}
	checkout := h.payments.checkouts[0]
	if checkout.CustomerID != "cus_1" || checkout.ProductID != "pdt_seats" || checkout.Quantity != 12 || checkout.OrganizationID != "org_1" {
		t.Fatalf("checkout request = %+v", checkout)
	}
	if checkout.ReturnURL != "https://app.example.com/settings/billing?organization_id=org_1&tab=plan" {
		t.Fatalf("return url = %q", checkout.ReturnURL)
	}
	organization, _, _ := h.store.Organization(context.Background(), "org_1")
	if organization.CustomerID != "cus_1" || organization.Name != "Acme" {
		t.Fatalf("organization = %+v", organization)
	}

	if _, err := h.service.StartCheckout(context.Background(), owner, "org_1", "team", 0); err != nil {
		t.Fatalf("second StartCheckout() error = %v", err)
	}
	if len(h.payments.customers) != 1 || h.payments.checkouts[1].CustomerID != "cus_1" || h.payments.checkouts[1].Quantity != 1 {
		t.Fatalf("second checkout created a customer or lost the first: customers=%d checkout=%+v", len(h.payments.customers), h.payments.checkouts[1])
	}
}

func TestStartCheckoutRefusals(t *testing.T) {
	tests := []struct {
		name        string
		prepare     func(*testing.T, harness)
		credentials authclient.Credentials
		plan        string
		quantity    int
		want        error
	}{
		{name: "member", credentials: member, plan: "team", want: domain.ErrForbidden},
		{name: "outsider", credentials: authclient.Credentials{Authorization: "stranger"}, plan: "team", want: domain.ErrNotFound},
		{name: "signed out", plan: "team", want: domain.ErrUnauthorized},
		{name: "unknown plan", credentials: owner, plan: "platinum", want: domain.ErrInvalid},
		{name: "quantity below minimum", credentials: owner, plan: "seats", quantity: 2, want: domain.ErrInvalid},
		{
			name:        "open subscription",
			credentials: owner,
			plan:        "team",
			want:        domain.ErrConflict,
			prepare: func(t *testing.T, h harness) {
				h.apply(t, webhook("msg_1", "sub_1", testNow, domain.SubscriptionOnHold))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			if test.prepare != nil {
				test.prepare(t, h)
			}
			_, err := h.service.StartCheckout(context.Background(), test.credentials, "org_1", test.plan, test.quantity)
			if !errors.Is(err, test.want) {
				t.Fatalf("StartCheckout() error = %v, want %v", err, test.want)
			}
			if len(h.payments.checkouts) != 0 {
				t.Fatalf("checkouts = %d, want none", len(h.payments.checkouts))
			}
		})
	}
}

func TestCheckoutAndPortalNeedDodo(t *testing.T) {
	h := newHarness(t)
	h.service.payments = nil

	if _, err := h.service.StartCheckout(context.Background(), owner, "org_1", "team", 0); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("StartCheckout() error = %v, want ErrNotConfigured", err)
	}
	if _, err := h.service.OpenPortal(context.Background(), owner, "org_1"); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("OpenPortal() error = %v, want ErrNotConfigured", err)
	}
}

func TestOpenPortal(t *testing.T) {
	h := newHarness(t)

	if _, err := h.service.OpenPortal(context.Background(), owner, "org_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("OpenPortal() before any checkout error = %v, want ErrNotFound", err)
	}
	if _, err := h.service.StartCheckout(context.Background(), owner, "org_1", "team", 0); err != nil {
		t.Fatalf("StartCheckout() error = %v", err)
	}
	if _, err := h.service.OpenPortal(context.Background(), member, "org_1"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("OpenPortal() for a member error = %v, want ErrForbidden", err)
	}

	link, err := h.service.OpenPortal(context.Background(), owner, "org_1")
	if err != nil {
		t.Fatalf("OpenPortal() error = %v", err)
	}
	if link != "https://portal.test/cus_1" || h.payments.portals[0] != "cus_1 https://app.example.com/settings/billing?organization_id=org_1&tab=plan" {
		t.Fatalf("link = %q, portal requests = %v", link, h.payments.portals)
	}
}

func TestSubscriptionLifecycle(t *testing.T) {
	h := newHarness(t)
	h.store.lock("org_1").organization.Name = "Acme"
	graceEnds := testNow.Add(3 * 24 * time.Hour)

	type step struct {
		name  string
		hook  dodo.Webhook
		check func(t *testing.T, events []domain.Event)
	}
	steps := []step{
		{
			name: "activated",
			hook: webhook("msg_1", "sub_1", testNow, domain.SubscriptionActive),
			check: func(t *testing.T, events []domain.Event) {
				expectDetailTypes(t, events, billingeventsv1.EntitlementsChangedDetailType)
				expectEntitlements(t, events[0], 1, "team", billingeventsv1.EntitlementStatusActive, intPointer(20))
			},
		},
		{
			name: "renewed without change",
			hook: webhook("msg_2", "sub_1", testNow.Add(time.Hour), domain.SubscriptionActive),
			check: func(t *testing.T, events []domain.Event) {
				expectDetailTypes(t, events)
			},
		},
		{
			name: "renewal failed with a grace period",
			hook: webhook("msg_3", "sub_1", testNow.Add(2*time.Hour), domain.SubscriptionPastDue, func(subscription *domain.Subscription) {
				subscription.PastDueEndsAt = &graceEnds
			}),
			check: func(t *testing.T, events []domain.Event) {
				expectDetailTypes(t, events, billingeventsv1.EntitlementsChangedDetailType, billingeventsv1.SubscriptionPaymentFailedDetailType)
				entitlements := expectEntitlements(t, events[0], 2, "team", billingeventsv1.EntitlementStatusPastDue, intPointer(20))
				if entitlements.GraceEndsAt == nil || !entitlements.GraceEndsAt.Equal(graceEnds) {
					t.Fatalf("grace ends at = %v, want %v", entitlements.GraceEndsAt, graceEnds)
				}
				var failed billingeventsv1.SubscriptionPaymentFailed
				decode(t, events[1], &failed)
				if failed.GraceEndsAt == nil || !failed.GraceEndsAt.Equal(graceEnds) || failed.OrganizationName != "Acme" ||
					failed.BillingEmail != "billing@example.com" || failed.BillingURL != "https://app.example.com/settings/billing?organization_id=org_1&tab=plan" {
					t.Fatalf("payment failed = %+v", failed)
				}
			},
		},
		{
			name: "still past due, deadline not repeated",
			hook: webhook("msg_4", "sub_1", testNow.Add(3*time.Hour), domain.SubscriptionPastDue),
			check: func(t *testing.T, events []domain.Event) {
				expectDetailTypes(t, events)
				if stored := h.store.subscriptions["sub_1"]; stored.PastDueEndsAt == nil || !stored.PastDueEndsAt.Equal(graceEnds) {
					t.Fatalf("stored grace deadline = %v, want it kept", stored.PastDueEndsAt)
				}
			},
		},
		{
			name: "grace period over, put on hold",
			hook: webhook("msg_5", "sub_1", testNow.Add(4*time.Hour), domain.SubscriptionOnHold),
			check: func(t *testing.T, events []domain.Event) {
				expectDetailTypes(t, events, billingeventsv1.EntitlementsChangedDetailType, billingeventsv1.SubscriptionPaymentFailedDetailType)
				expectEntitlements(t, events[0], 3, "", billingeventsv1.EntitlementStatusUnpaid, intPointer(3))
				var failed billingeventsv1.SubscriptionPaymentFailed
				decode(t, events[1], &failed)
				if failed.GraceEndsAt != nil {
					t.Fatalf("on hold payment failure has a grace period: %+v", failed)
				}
			},
		},
		{
			name: "payment method fixed",
			hook: webhook("msg_6", "sub_1", testNow.Add(5*time.Hour), domain.SubscriptionActive),
			check: func(t *testing.T, events []domain.Event) {
				expectDetailTypes(t, events, billingeventsv1.EntitlementsChangedDetailType)
				expectEntitlements(t, events[0], 4, "team", billingeventsv1.EntitlementStatusActive, intPointer(20))
			},
		},
		{
			name: "duplicate delivery",
			hook: webhook("msg_6", "sub_1", testNow.Add(5*time.Hour), domain.SubscriptionActive),
			check: func(t *testing.T, events []domain.Event) {
				expectDetailTypes(t, events)
			},
		},
		{
			name: "cancelled",
			hook: webhook("msg_7", "sub_1", testNow.Add(6*time.Hour), domain.SubscriptionCancelled),
			check: func(t *testing.T, events []domain.Event) {
				expectDetailTypes(t, events, billingeventsv1.EntitlementsChangedDetailType, billingeventsv1.SubscriptionEndedDetailType)
				expectEntitlements(t, events[0], 5, "", billingeventsv1.EntitlementStatusUnpaid, intPointer(3))
				var ended billingeventsv1.SubscriptionEnded
				decode(t, events[1], &ended)
				if ended.Reason != billingeventsv1.SubscriptionEndedReasonCancelled || !ended.EndedAt.Equal(testNow.Add(6*time.Hour)) {
					t.Fatalf("ended = %+v", ended)
				}
			},
		},
		{
			name: "late delivery of an older event",
			hook: webhook("msg_8", "sub_1", testNow.Add(90*time.Minute), domain.SubscriptionActive),
			check: func(t *testing.T, events []domain.Event) {
				expectDetailTypes(t, events)
				if stored := h.store.subscriptions["sub_1"]; stored.Status != domain.SubscriptionCancelled {
					t.Fatalf("stored status = %s, want cancelled to stand", stored.Status)
				}
			},
		},
	}

	for _, step := range steps {
		h.apply(t, step.hook)
		step.check(t, h.store.takeEvents())
		if t.Failed() {
			t.Fatalf("failed at step %q", step.name)
		}
	}
}

func TestNewSubscriptionReplacesTheEndedOne(t *testing.T) {
	h := newHarness(t)
	h.store.lock("org_1").organization.Name = "Acme"
	h.apply(t, webhook("msg_1", "sub_1", testNow, domain.SubscriptionActive))
	h.apply(t, webhook("msg_2", "sub_1", testNow.Add(time.Hour), domain.SubscriptionCancelled))
	h.store.takeEvents()

	h.apply(t, webhook("msg_3", "sub_2", testNow.Add(2*time.Hour), domain.SubscriptionActive, func(subscription *domain.Subscription) {
		subscription.ProductID = "pdt_seats"
		subscription.Quantity = 9
	}))
	events := h.store.takeEvents()
	expectDetailTypes(t, events, billingeventsv1.EntitlementsChangedDetailType)
	expectEntitlements(t, events[0], 3, "seats", billingeventsv1.EntitlementStatusActive, intPointer(9))

	h.apply(t, webhook("msg_4", "sub_1", testNow.Add(3*time.Hour), domain.SubscriptionExpired))
	expectDetailTypes(t, h.store.takeEvents())
	if organization := h.store.organizations["org_1"].organization; organization.SubscriptionID != "sub_2" {
		t.Fatalf("current subscription = %s, want sub_2", organization.SubscriptionID)
	}
}

func TestWebhookFindsOrganizationByCustomer(t *testing.T) {
	h := newHarness(t)
	if _, err := h.service.StartCheckout(context.Background(), owner, "org_1", "team", 0); err != nil {
		t.Fatalf("StartCheckout() error = %v", err)
	}

	h.apply(t, webhook("msg_1", "sub_1", testNow, domain.SubscriptionActive, func(subscription *domain.Subscription) {
		subscription.OrganizationID = ""
		subscription.CustomerID = "cus_1"
	}))
	events := h.store.takeEvents()
	expectDetailTypes(t, events, billingeventsv1.EntitlementsChangedDetailType)
	if h.store.subscriptions["sub_1"].OrganizationID != "org_1" {
		t.Fatalf("subscription recorded under %q, want org_1", h.store.subscriptions["sub_1"].OrganizationID)
	}
}

func TestWebhookForAnUnknownOrganizationChangesNothing(t *testing.T) {
	h := newHarness(t)

	h.apply(t, webhook("msg_1", "sub_1", testNow, domain.SubscriptionActive, func(subscription *domain.Subscription) {
		subscription.OrganizationID = ""
		subscription.CustomerID = "cus_elsewhere"
	}))
	if len(h.store.organizations) != 0 || len(h.store.takeEvents()) != 0 {
		t.Fatalf("unmatched webhook changed state: organizations=%v", h.store.organizations)
	}
}

func TestProductMissingFromTheCatalogIsLeftWithoutLimits(t *testing.T) {
	h := newHarness(t)

	h.apply(t, webhook("msg_1", "sub_1", testNow, domain.SubscriptionActive, func(subscription *domain.Subscription) {
		subscription.ProductID = "pdt_legacy"
	}))
	events := h.store.takeEvents()
	expectDetailTypes(t, events, billingeventsv1.EntitlementsChangedDetailType)
	expectEntitlements(t, events[0], 1, "", billingeventsv1.EntitlementStatusActive, nil)
}

func TestNoticesNeedANameAndABillingEmail(t *testing.T) {
	h := newHarness(t)
	h.apply(t, webhook("msg_1", "sub_1", testNow, domain.SubscriptionActive))
	h.store.takeEvents()

	h.apply(t, webhook("msg_2", "sub_1", testNow.Add(time.Hour), domain.SubscriptionOnHold))
	expectDetailTypes(t, h.store.takeEvents(), billingeventsv1.EntitlementsChangedDetailType)
}

func TestOrganizationCreatedAnnouncesEntitlementsOnce(t *testing.T) {
	h := newHarness(t)
	created := autheventsv1.OrganizationCreated{OrganizationID: "org_1", Name: "Acme", CreatedAt: testNow}

	if err := h.service.OrganizationCreated(context.Background(), created); err != nil {
		t.Fatalf("OrganizationCreated() error = %v", err)
	}
	events := h.store.takeEvents()
	expectDetailTypes(t, events, billingeventsv1.EntitlementsChangedDetailType)
	expectEntitlements(t, events[0], 1, "", billingeventsv1.EntitlementStatusUnpaid, intPointer(3))

	if err := h.service.OrganizationCreated(context.Background(), created); err != nil {
		t.Fatalf("redelivered OrganizationCreated() error = %v", err)
	}
	expectDetailTypes(t, h.store.takeEvents())
}

func TestOrganizationRenamesOnlyMoveForward(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	must(t, h.service.OrganizationCreated(ctx, autheventsv1.OrganizationCreated{OrganizationID: "org_1", Name: "Acme", CreatedAt: testNow}))
	must(t, h.service.OrganizationUpdated(ctx, autheventsv1.OrganizationUpdated{OrganizationID: "org_1", Name: "Acme Two", UpdatedAt: testNow.Add(2 * time.Hour)}))
	must(t, h.service.OrganizationUpdated(ctx, autheventsv1.OrganizationUpdated{OrganizationID: "org_1", Name: "Acme One", UpdatedAt: testNow.Add(time.Hour)}))

	if name := h.store.organizations["org_1"].organization.Name; name != "Acme Two" {
		t.Fatalf("name = %q, want the newest rename", name)
	}
}

func TestOrganizationDeletedCancelsItsOpenSubscription(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.store.lock("org_1").organization.Name = "Acme"
	h.apply(t, webhook("msg_1", "sub_1", testNow, domain.SubscriptionPastDue))
	deleted := autheventsv1.OrganizationDeleted{OrganizationID: "org_1", Name: "Acme", DeletedAt: testNow}

	h.payments.cancelErr = errors.New("Dodo unavailable")
	if err := h.service.OrganizationDeleted(ctx, deleted); err == nil {
		t.Fatal("OrganizationDeleted() error = nil while cancelling failed")
	}
	if h.store.organizations["org_1"].organization.Deleted {
		t.Fatal("organization marked deleted before its subscription was cancelled")
	}

	h.payments.cancelErr = nil
	must(t, h.service.OrganizationDeleted(ctx, deleted))
	if len(h.payments.cancelled) != 1 || h.payments.cancelled[0] != "sub_1" || !h.store.organizations["org_1"].organization.Deleted {
		t.Fatalf("cancelled = %v, deleted = %v", h.payments.cancelled, h.store.organizations["org_1"].organization.Deleted)
	}

	// The cancellation Dodo confirms still tells the billing contact the
	// subscription ended, but a deleted organization has no entitlements left
	// to announce.
	h.store.takeEvents()
	h.apply(t, webhook("msg_2", "sub_1", testNow.Add(time.Hour), domain.SubscriptionCancelled))
	expectDetailTypes(t, h.store.takeEvents(), billingeventsv1.SubscriptionEndedDetailType)
}

func TestOrganizationDeletedWithoutASubscription(t *testing.T) {
	h := newHarness(t)

	must(t, h.service.OrganizationDeleted(context.Background(), autheventsv1.OrganizationDeleted{OrganizationID: "org_1", DeletedAt: testNow}))
	if len(h.payments.cancelled) != 0 || !h.store.organizations["org_1"].organization.Deleted {
		t.Fatalf("cancelled = %v, organization = %+v", h.payments.cancelled, h.store.organizations["org_1"])
	}
	must(t, h.service.OrganizationCreated(context.Background(), autheventsv1.OrganizationCreated{OrganizationID: "org_1", Name: "Acme", CreatedAt: testNow}))
	expectDetailTypes(t, h.store.takeEvents())
}

func TestSendRenewalReminders(t *testing.T) {
	h := newHarness(t)
	h.store.lock("org_1").organization.Name = "Acme"
	renewsAt := testNow.Add(5 * 24 * time.Hour)
	h.apply(t, webhook("msg_1", "sub_1", testNow, domain.SubscriptionActive, func(subscription *domain.Subscription) {
		subscription.NextBillingDate = renewsAt
	}))
	h.store.takeEvents()

	sent, err := h.service.SendRenewalReminders(context.Background())
	if err != nil || sent != 1 {
		t.Fatalf("SendRenewalReminders() = %d, %v; want 1", sent, err)
	}
	events := h.store.takeEvents()
	expectDetailTypes(t, events, billingeventsv1.SubscriptionRenewalUpcomingDetailType)
	var upcoming billingeventsv1.SubscriptionRenewalUpcoming
	decode(t, events[0], &upcoming)
	if upcoming.Plan != "team" || !upcoming.RenewsAt.Equal(renewsAt) || upcoming.BillingEmail != "billing@example.com" || upcoming.OrganizationName != "Acme" {
		t.Fatalf("renewal upcoming = %+v", upcoming)
	}

	if sent, err := h.service.SendRenewalReminders(context.Background()); err != nil || sent != 0 {
		t.Fatalf("second SendRenewalReminders() = %d, %v; want nothing new", sent, err)
	}
}

func TestRenewalRemindersSkipSubscriptionsThatWillNotRenew(t *testing.T) {
	tests := map[string]func(*domain.Subscription){
		"cancelling at period end": func(subscription *domain.Subscription) { subscription.CancelAtNextBillingDate = true },
		"weekly billing":           func(subscription *domain.Subscription) { subscription.BillingInterval = "Week" },
		"outside the window": func(subscription *domain.Subscription) {
			subscription.NextBillingDate = testNow.Add(20 * 24 * time.Hour)
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.store.lock("org_1").organization.Name = "Acme"
			h.apply(t, webhook("msg_1", "sub_1", testNow, domain.SubscriptionActive, func(subscription *domain.Subscription) {
				subscription.NextBillingDate = testNow.Add(2 * 24 * time.Hour)
				change(subscription)
			}))
			if sent, err := h.service.SendRenewalReminders(context.Background()); err != nil || sent != 0 {
				t.Fatalf("SendRenewalReminders() = %d, %v; want 0", sent, err)
			}
		})
	}

	h := newHarness(t)
	h.service.config.RenewalReminderWindow = 0
	if sent, err := h.service.SendRenewalReminders(context.Background()); err != nil || sent != 0 {
		t.Fatalf("SendRenewalReminders() with reminders off = %d, %v", sent, err)
	}
}

func TestReconcileEntitlementsAppliesACatalogChange(t *testing.T) {
	h := newHarness(t)
	h.apply(t, webhook("msg_1", "sub_1", testNow, domain.SubscriptionActive))
	must(t, h.service.OrganizationCreated(context.Background(), autheventsv1.OrganizationCreated{OrganizationID: "org_2", Name: "Beta", CreatedAt: testNow}))
	h.store.takeEvents()

	if changed, err := h.service.ReconcileEntitlements(context.Background()); err != nil || changed != 0 {
		t.Fatalf("ReconcileEntitlements() with an unchanged catalog = %d, %v", changed, err)
	}

	edited, err := catalog.Parse([]byte(`{"unpaid": {"member_limit": 5}, "plans": [{"key": "team", "product_id": "pdt_team", "member_limit": 50}]}`))
	must(t, err)
	h.service.catalog = edited
	changed, err := h.service.ReconcileEntitlements(context.Background())
	if err != nil || changed != 2 {
		t.Fatalf("ReconcileEntitlements() = %d, %v; want both organizations changed", changed, err)
	}
	events := h.store.takeEvents()
	expectDetailTypes(t, events, billingeventsv1.EntitlementsChangedDetailType, billingeventsv1.EntitlementsChangedDetailType)
	expectEntitlements(t, events[0], 2, "team", billingeventsv1.EntitlementStatusActive, intPointer(50))
	expectEntitlements(t, events[1], 2, "", billingeventsv1.EntitlementStatusUnpaid, intPointer(5))
}

func TestSummary(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	summary, err := h.service.Summary(ctx, member, "org_1")
	if err != nil {
		t.Fatalf("Summary() for an unknown organization error = %v", err)
	}
	if summary.CanManage || summary.Subscription != nil || summary.Entitlements.Status != billingeventsv1.EntitlementStatusUnpaid || *summary.Entitlements.MemberLimit != 3 {
		t.Fatalf("summary = %+v", summary)
	}

	renewsAt := testNow.Add(30 * 24 * time.Hour)
	h.apply(t, webhook("msg_1", "sub_1", testNow, domain.SubscriptionActive, func(subscription *domain.Subscription) {
		subscription.NextBillingDate = renewsAt
		subscription.CancelAtNextBillingDate = true
	}))
	summary, err = h.service.Summary(ctx, owner, "org_1")
	if err != nil {
		t.Fatalf("Summary() error = %v", err)
	}
	if !summary.CanManage || summary.Entitlements.Plan != "team" || summary.Subscription == nil || summary.Subscription.Plan != "team" ||
		summary.Subscription.RenewsAt != nil || summary.Subscription.EndsAt == nil || !summary.Subscription.EndsAt.Equal(renewsAt) {
		t.Fatalf("summary = %+v, subscription = %+v", summary, summary.Subscription)
	}

	if _, err := h.service.Summary(ctx, authclient.Credentials{Authorization: "stranger"}, "org_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Summary() for an outsider error = %v, want ErrNotFound", err)
	}
}

func (h harness) apply(t *testing.T, hook dodo.Webhook) {
	t.Helper()
	if err := h.service.HandleWebhook(context.Background(), hook); err != nil {
		t.Fatalf("HandleWebhook(%s) error = %v", hook.Type, err)
	}
}

func webhook(id, subscriptionID string, at time.Time, status domain.SubscriptionStatus, changes ...func(*domain.Subscription)) dodo.Webhook {
	subscription := domain.Subscription{
		ID:              subscriptionID,
		OrganizationID:  "org_1",
		CustomerID:      "cus_1",
		CustomerEmail:   "billing@example.com",
		ProductID:       "pdt_team",
		Quantity:        1,
		Status:          status,
		NextBillingDate: at.Add(30 * 24 * time.Hour),
		BillingInterval: "Month",
		EventAt:         at,
	}
	for _, change := range changes {
		change(&subscription)
	}
	return dodo.Webhook{ID: id, Type: "subscription." + string(status), Timestamp: at, Subscription: &subscription}
}

func expectDetailTypes(t *testing.T, events []domain.Event, want ...string) {
	t.Helper()
	var got []string
	for _, event := range events {
		got = append(got, event.DetailType)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func expectEntitlements(t *testing.T, event domain.Event, revision int64, plan, status string, limit *int) billingeventsv1.EntitlementsChanged {
	t.Helper()
	var entitlements billingeventsv1.EntitlementsChanged
	decode(t, event, &entitlements)
	limitMatches := (limit == nil && entitlements.MemberLimit == nil) ||
		(limit != nil && entitlements.MemberLimit != nil && *limit == *entitlements.MemberLimit)
	if entitlements.OrganizationID == "" || entitlements.Revision != revision || entitlements.Plan != plan || entitlements.Status != status || !limitMatches {
		t.Fatalf("entitlements = %+v (limit %v), want revision %d plan %q status %s limit %v", entitlements, deref(entitlements.MemberLimit), revision, plan, status, deref(limit))
	}
	return entitlements
}

func decode(t *testing.T, event domain.Event, target any) {
	t.Helper()
	if err := json.Unmarshal(event.Data, target); err != nil {
		t.Fatalf("decode %s: %v", event.DetailType, err)
	}
}

func intPointer(value int) *int {
	return &value
}

func deref(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
