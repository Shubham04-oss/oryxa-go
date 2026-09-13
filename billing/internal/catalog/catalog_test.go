package catalog

import (
	"errors"
	"strconv"
	"testing"
	"time"

	billingeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/billing/v1"

	"github.com/Shubham04-oss/oryxa-go/billing/internal/domain"
)

const sampleCatalog = `{
	"unpaid": {"member_limit": 3},
	"plans": [
		{"key": "team", "product_id": "pdt_team", "member_limit": 20},
		{"key": "seats", "product_id": "pdt_seats", "member_limit_per_unit": 1, "min_quantity": 5},
		{"key": "enterprise", "product_id": "pdt_enterprise"}
	]
}`

func TestEntitlements(t *testing.T) {
	catalog := mustParse(t, sampleCatalog)
	graceEnds := time.Date(2026, time.September, 16, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		subscription *domain.Subscription
		want         domain.Entitlements
		wantKnown    bool
	}{
		{
			name:      "no subscription",
			want:      domain.Entitlements{Status: billingeventsv1.EntitlementStatusUnpaid, MemberLimit: intPointer(3)},
			wantKnown: true,
		},
		{
			name:         "fixed plan",
			subscription: &domain.Subscription{ProductID: "pdt_team", Quantity: 1, Status: domain.SubscriptionActive},
			want:         domain.Entitlements{Plan: "team", Status: billingeventsv1.EntitlementStatusActive, MemberLimit: intPointer(20)},
			wantKnown:    true,
		},
		{
			name:         "per-unit plan scales with quantity",
			subscription: &domain.Subscription{ProductID: "pdt_seats", Quantity: 12, Status: domain.SubscriptionActive},
			want:         domain.Entitlements{Plan: "seats", Status: billingeventsv1.EntitlementStatusActive, MemberLimit: intPointer(12)},
			wantKnown:    true,
		},
		{
			name:         "plan without a limit",
			subscription: &domain.Subscription{ProductID: "pdt_enterprise", Quantity: 1, Status: domain.SubscriptionActive},
			want:         domain.Entitlements{Plan: "enterprise", Status: billingeventsv1.EntitlementStatusActive},
			wantKnown:    true,
		},
		{
			name:         "past due keeps the plan until the grace period ends",
			subscription: &domain.Subscription{ProductID: "pdt_team", Quantity: 1, Status: domain.SubscriptionPastDue, PastDueEndsAt: &graceEnds},
			want:         domain.Entitlements{Plan: "team", Status: billingeventsv1.EntitlementStatusPastDue, MemberLimit: intPointer(20), GraceEndsAt: &graceEnds},
			wantKnown:    true,
		},
		{
			name:         "on hold falls back to unpaid",
			subscription: &domain.Subscription{ProductID: "pdt_team", Quantity: 1, Status: domain.SubscriptionOnHold},
			want:         domain.Entitlements{Status: billingeventsv1.EntitlementStatusUnpaid, MemberLimit: intPointer(3)},
			wantKnown:    true,
		},
		{
			name:         "cancelled falls back to unpaid",
			subscription: &domain.Subscription{ProductID: "pdt_team", Quantity: 1, Status: domain.SubscriptionCancelled},
			want:         domain.Entitlements{Status: billingeventsv1.EntitlementStatusUnpaid, MemberLimit: intPointer(3)},
			wantKnown:    true,
		},
		{
			name:         "product missing from the catalog is not cut off",
			subscription: &domain.Subscription{ProductID: "pdt_retired", Quantity: 1, Status: domain.SubscriptionActive},
			want:         domain.Entitlements{Status: billingeventsv1.EntitlementStatusActive},
			wantKnown:    false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, known := catalog.Entitlements(test.subscription)
			if known != test.wantKnown || !got.Equal(test.want) {
				t.Fatalf("Entitlements() = %+v (known %v), want %+v (known %v)", describe(got), known, describe(test.want), test.wantKnown)
			}
		})
	}
}

func TestEntitlementsDoNotShareTheCatalogsLimits(t *testing.T) {
	catalog := mustParse(t, sampleCatalog)

	first, _ := catalog.Entitlements(nil)
	*first.MemberLimit = 99
	second, _ := catalog.Entitlements(nil)
	if *second.MemberLimit != 3 {
		t.Fatalf("changing returned entitlements changed the catalog: limit = %d", *second.MemberLimit)
	}
}

func TestEmptyCatalogLimitsNothing(t *testing.T) {
	entitlements, known := Empty().Entitlements(nil)
	if !known || entitlements.MemberLimit != nil || entitlements.Status != billingeventsv1.EntitlementStatusUnpaid {
		t.Fatalf("Empty().Entitlements(nil) = %+v (known %v)", describe(entitlements), known)
	}
	if loaded, err := Load(""); err != nil || len(loaded.Plans()) != 0 {
		t.Fatalf("Load(\"\") = %v plans, %v", loaded.Plans(), err)
	}
}

func TestCheckoutQuantity(t *testing.T) {
	catalog := mustParse(t, sampleCatalog)
	team, _ := catalog.Plan("team")
	seats, _ := catalog.Plan("seats")

	tests := []struct {
		name      string
		plan      Plan
		requested int
		want      int
		wantErr   bool
	}{
		{name: "fixed plan defaults to one", plan: team, requested: 0, want: 1},
		{name: "fixed plan accepts one", plan: team, requested: 1, want: 1},
		{name: "fixed plan refuses more", plan: team, requested: 2, wantErr: true},
		{name: "per-unit plan defaults to its minimum", plan: seats, requested: 0, want: 5},
		{name: "per-unit plan accepts a quantity in range", plan: seats, requested: 40, want: 40},
		{name: "per-unit plan refuses fewer than its minimum", plan: seats, requested: 4, wantErr: true},
		{name: "per-unit plan refuses more than its maximum", plan: seats, requested: defaultMaxQuantity + 1, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.plan.CheckoutQuantity(test.requested)
			if test.wantErr {
				if !errors.Is(err, domain.ErrInvalid) {
					t.Fatalf("CheckoutQuantity(%d) error = %v, want invalid", test.requested, err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("CheckoutQuantity(%d) = %d, %v; want %d", test.requested, got, err, test.want)
			}
		})
	}
}

func TestParseRejectsInvalidCatalogs(t *testing.T) {
	tests := map[string]string{
		"unknown field":            `{"plans": [{"key": "team", "product_id": "pdt_team", "price": 10}]}`,
		"bad key":                  `{"plans": [{"key": "Team Plan", "product_id": "pdt_team"}]}`,
		"no product":               `{"plans": [{"key": "team"}]}`,
		"duplicate key":            `{"plans": [{"key": "team", "product_id": "pdt_a"}, {"key": "team", "product_id": "pdt_b"}]}`,
		"duplicate product":        `{"plans": [{"key": "team", "product_id": "pdt_a"}, {"key": "pro", "product_id": "pdt_a"}]}`,
		"both limits":              `{"plans": [{"key": "team", "product_id": "pdt_a", "member_limit": 5, "member_limit_per_unit": 1}]}`,
		"zero limit":               `{"plans": [{"key": "team", "product_id": "pdt_a", "member_limit": 0}]}`,
		"zero unpaid limit":        `{"unpaid": {"member_limit": 0}}`,
		"quantity on a fixed plan": `{"plans": [{"key": "team", "product_id": "pdt_a", "member_limit": 5, "max_quantity": 10}]}`,
		"inverted quantity range":  `{"plans": [{"key": "seats", "product_id": "pdt_a", "member_limit_per_unit": 1, "min_quantity": 10, "max_quantity": 5}]}`,
		"trailing data":            `{"plans": []} {}`,
	}

	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(data)); err == nil {
				t.Fatalf("Parse() error = nil for %s", data)
			}
		})
	}
}

func TestLoadExampleCatalog(t *testing.T) {
	catalog, err := Load("../../plans.example.json")
	if err != nil {
		t.Fatalf("Load(plans.example.json) error = %v", err)
	}
	if len(catalog.Plans()) == 0 {
		t.Fatal("example catalog has no plans")
	}
}

func mustParse(t *testing.T, data string) *Catalog {
	t.Helper()
	catalog, err := Parse([]byte(data))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	return catalog
}

func intPointer(value int) *int {
	return &value
}

func describe(entitlements domain.Entitlements) string {
	limit, grace := "none", "none"
	if entitlements.MemberLimit != nil {
		limit = strconv.Itoa(*entitlements.MemberLimit)
	}
	if entitlements.GraceEndsAt != nil {
		grace = entitlements.GraceEndsAt.Format(time.RFC3339)
	}
	return "{plan:" + entitlements.Plan + " status:" + entitlements.Status + " limit:" + limit + " grace:" + grace + "}"
}
