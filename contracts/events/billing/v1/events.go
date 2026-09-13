// Package v1 contains the stable version-one event contracts emitted by billing.
package v1

import "time"

const (
	Source = "oryxa.billing"

	EntitlementsChangedDetailType         = "billing.entitlements.changed.v1"
	SubscriptionPaymentFailedDetailType   = "billing.subscription.payment-failed.v1"
	SubscriptionEndedDetailType           = "billing.subscription.ended.v1"
	SubscriptionRenewalUpcomingDetailType = "billing.subscription.renewal-upcoming.v1"
)

// Entitlement statuses, as EntitlementsChanged.Status.
const (
	// EntitlementStatusUnpaid: no subscription grants anything, so the
	// organization has the catalog's unpaid allowance.
	EntitlementStatusUnpaid = "unpaid"
	// EntitlementStatusActive: a subscription is paid up.
	EntitlementStatusActive = "active"
	// EntitlementStatusPastDue: a renewal failed but its grace period is open,
	// so the organization keeps its plan until GraceEndsAt.
	EntitlementStatusPastDue = "past_due"
)

// Reasons a subscription ends, as SubscriptionEnded.Reason.
const (
	SubscriptionEndedReasonCancelled = "cancelled"
	SubscriptionEndedReasonExpired   = "expired"
)

// EntitlementsChanged carries an organization's complete entitlements, which
// replace any earlier ones. Revision increases with every change, so a consumer
// keeps only the highest revision it has seen.
type EntitlementsChanged struct {
	OrganizationID string `json:"organization_id"`
	Revision       int64  `json:"revision"`
	// Plan is the catalog key of the subscribed plan; empty when unpaid.
	Plan   string `json:"plan,omitempty"`
	Status string `json:"status"`
	// MemberLimit is the most members, pending invitations included, the
	// organization may have. Null means no limit.
	MemberLimit *int       `json:"member_limit"`
	GraceEndsAt *time.Time `json:"grace_ends_at,omitempty"`
	ChangedAt   time.Time  `json:"changed_at"`
}

// SubscriptionPaymentFailed is emitted when a renewal payment fails. With a
// grace period open, access continues until GraceEndsAt; without one, access
// to the plan stopped when the payment failed.
type SubscriptionPaymentFailed struct {
	OrganizationID   string     `json:"organization_id"`
	OrganizationName string     `json:"organization_name"`
	SubscriptionID   string     `json:"subscription_id"`
	BillingEmail     string     `json:"billing_email"`
	GraceEndsAt      *time.Time `json:"grace_ends_at,omitempty"`
	BillingURL       string     `json:"billing_url"`
	FailedAt         time.Time  `json:"failed_at"`
}

// SubscriptionEnded is emitted when a subscription that granted a plan is
// cancelled or reaches the end of its term.
type SubscriptionEnded struct {
	OrganizationID   string    `json:"organization_id"`
	OrganizationName string    `json:"organization_name"`
	SubscriptionID   string    `json:"subscription_id"`
	BillingEmail     string    `json:"billing_email"`
	Reason           string    `json:"reason"`
	BillingURL       string    `json:"billing_url"`
	EndedAt          time.Time `json:"ended_at"`
}

// SubscriptionRenewalUpcoming is emitted once per billing period, shortly
// before a subscription renews.
type SubscriptionRenewalUpcoming struct {
	OrganizationID   string    `json:"organization_id"`
	OrganizationName string    `json:"organization_name"`
	SubscriptionID   string    `json:"subscription_id"`
	BillingEmail     string    `json:"billing_email"`
	Plan             string    `json:"plan,omitempty"`
	RenewsAt         time.Time `json:"renews_at"`
	BillingURL       string    `json:"billing_url"`
}
