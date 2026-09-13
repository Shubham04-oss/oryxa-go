// Package catalog is the plan catalog: which Dodo Payments products Oryxa sells
// and what each entitles an organization to. It is configuration rather than
// code, so changing pricing means editing the catalog file, not a release.
package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"

	billingeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/billing/v1"

	"github.com/Shubham04-oss/oryxa-go/billing/internal/domain"
)

const defaultMaxQuantity = 10000

var planKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type Catalog struct {
	unpaid    Allowance
	plans     []Plan
	byKey     map[string]Plan
	byProduct map[string]Plan
}

// Allowance is what an organization without a paid subscription gets.
type Allowance struct {
	// MemberLimit caps members, pending invitations included. Unset means no
	// limit.
	MemberLimit *int `json:"member_limit,omitempty"`
}

type Plan struct {
	Key       string `json:"key"`
	ProductID string `json:"product_id"`
	// MemberLimit is a fixed member cap. Leave it and MemberLimitPerUnit unset
	// for no limit.
	MemberLimit *int `json:"member_limit,omitempty"`
	// MemberLimitPerUnit scales the cap with the subscription's quantity, for
	// per-seat pricing: each unit bought allows this many members.
	MemberLimitPerUnit *int `json:"member_limit_per_unit,omitempty"`
	// MinQuantity and MaxQuantity bound the units a checkout may buy. They
	// apply only to per-unit plans; every other plan is bought once.
	MinQuantity int `json:"min_quantity,omitempty"`
	MaxQuantity int `json:"max_quantity,omitempty"`
}

type file struct {
	Unpaid Allowance `json:"unpaid"`
	Plans  []Plan    `json:"plans"`
}

// Empty is a catalog with no plans and no limits, which leaves every
// organization unrestricted.
func Empty() *Catalog {
	return &Catalog{byKey: map[string]Plan{}, byProduct: map[string]Plan{}}
}

// Load reads the catalog at path; an empty path is the empty catalog.
func Load(path string) (*Catalog, error) {
	if path == "" {
		return Empty(), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read plan catalog: %w", err)
	}
	catalog, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("plan catalog %s: %w", path, err)
	}
	return catalog, nil
}

func Parse(data []byte) (*Catalog, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var parsed file
	if err := decoder.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("must contain one JSON object")
	}
	if err := validLimit("unpaid member_limit", parsed.Unpaid.MemberLimit); err != nil {
		return nil, err
	}

	catalog := Empty()
	catalog.unpaid = parsed.Unpaid
	for _, plan := range parsed.Plans {
		if !planKeyPattern.MatchString(plan.Key) {
			return nil, fmt.Errorf("plan key %q must be lowercase letters, digits and hyphens", plan.Key)
		}
		if plan.ProductID == "" {
			return nil, fmt.Errorf("plan %q has no product_id", plan.Key)
		}
		if _, ok := catalog.byKey[plan.Key]; ok {
			return nil, fmt.Errorf("plan key %q appears twice", plan.Key)
		}
		if other, ok := catalog.byProduct[plan.ProductID]; ok {
			return nil, fmt.Errorf("plans %q and %q share product %s", other.Key, plan.Key, plan.ProductID)
		}
		if plan.MemberLimit != nil && plan.MemberLimitPerUnit != nil {
			return nil, fmt.Errorf("plan %q sets both member_limit and member_limit_per_unit", plan.Key)
		}
		if err := validLimit("plan "+plan.Key+" member_limit", plan.MemberLimit); err != nil {
			return nil, err
		}
		if err := validLimit("plan "+plan.Key+" member_limit_per_unit", plan.MemberLimitPerUnit); err != nil {
			return nil, err
		}
		if plan.MemberLimitPerUnit == nil {
			if plan.MinQuantity != 0 || plan.MaxQuantity != 0 {
				return nil, fmt.Errorf("plan %q sets a quantity range but is not priced per unit", plan.Key)
			}
		} else {
			if plan.MinQuantity == 0 {
				plan.MinQuantity = 1
			}
			if plan.MaxQuantity == 0 {
				plan.MaxQuantity = defaultMaxQuantity
			}
			if plan.MinQuantity < 1 || plan.MaxQuantity < plan.MinQuantity {
				return nil, fmt.Errorf("plan %q needs 1 <= min_quantity <= max_quantity", plan.Key)
			}
		}
		catalog.plans = append(catalog.plans, plan)
		catalog.byKey[plan.Key] = plan
		catalog.byProduct[plan.ProductID] = plan
	}
	return catalog, nil
}

func (catalog *Catalog) Plans() []Plan {
	return append([]Plan(nil), catalog.plans...)
}

func (catalog *Catalog) Unpaid() Allowance {
	return Allowance{MemberLimit: copyInt(catalog.unpaid.MemberLimit)}
}

func (catalog *Catalog) Plan(key string) (Plan, bool) {
	plan, ok := catalog.byKey[key]
	return plan, ok
}

func (catalog *Catalog) PlanForProduct(productID string) (Plan, bool) {
	plan, ok := catalog.byProduct[productID]
	return plan, ok
}

// CheckoutQuantity is the quantity a checkout for the plan buys, given the
// quantity requested; zero asks for the plan's minimum.
func (plan Plan) CheckoutQuantity(requested int) (int, error) {
	if plan.MemberLimitPerUnit == nil {
		if requested > 1 {
			return 0, fmt.Errorf("plan %s is bought once: %w", plan.Key, domain.ErrInvalid)
		}
		return 1, nil
	}
	if requested == 0 {
		return plan.MinQuantity, nil
	}
	if requested < plan.MinQuantity || requested > plan.MaxQuantity {
		return 0, fmt.Errorf("plan %s quantity must be between %d and %d: %w", plan.Key, plan.MinQuantity, plan.MaxQuantity, domain.ErrInvalid)
	}
	return requested, nil
}

// Entitlements is what the subscription entitles its organization to; a nil
// subscription is no subscription. It reports false for a paid subscription to
// a product the catalog doesn't list, which is left unlimited rather than cut
// off until the catalog catches up.
func (catalog *Catalog) Entitlements(subscription *domain.Subscription) (domain.Entitlements, bool) {
	if subscription == nil || !subscription.Status.Grants() {
		return domain.Entitlements{
			Status:      billingeventsv1.EntitlementStatusUnpaid,
			MemberLimit: copyInt(catalog.unpaid.MemberLimit),
		}, true
	}

	entitlements := domain.Entitlements{Status: billingeventsv1.EntitlementStatusActive}
	if subscription.Status == domain.SubscriptionPastDue {
		entitlements.Status = billingeventsv1.EntitlementStatusPastDue
		entitlements.GraceEndsAt = subscription.PastDueEndsAt
	}
	plan, ok := catalog.byProduct[subscription.ProductID]
	if !ok {
		return entitlements, false
	}
	entitlements.Plan = plan.Key
	switch {
	case plan.MemberLimitPerUnit != nil:
		limit := *plan.MemberLimitPerUnit * max(subscription.Quantity, 1)
		entitlements.MemberLimit = &limit
	case plan.MemberLimit != nil:
		entitlements.MemberLimit = copyInt(plan.MemberLimit)
	}
	return entitlements, true
}

func validLimit(name string, limit *int) error {
	if limit != nil && *limit < 1 {
		return fmt.Errorf("%s must be at least 1", name)
	}
	return nil
}

func copyInt(value *int) *int {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
