package domain

import (
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrInvalid       = errors.New("invalid input")
	ErrNotFound      = errors.New("not found")
	ErrConflict      = errors.New("conflict")
	ErrForbidden     = errors.New("forbidden")
	ErrUnauthorized  = errors.New("unauthorized")
	ErrNotConfigured = errors.New("billing is not configured")
)

// SubscriptionStatus is a Dodo Payments subscription status.
type SubscriptionStatus string

const (
	SubscriptionPending   SubscriptionStatus = "pending"
	SubscriptionActive    SubscriptionStatus = "active"
	SubscriptionPastDue   SubscriptionStatus = "past_due"
	SubscriptionOnHold    SubscriptionStatus = "on_hold"
	SubscriptionPaused    SubscriptionStatus = "paused"
	SubscriptionCancelled SubscriptionStatus = "cancelled"
	SubscriptionFailed    SubscriptionStatus = "failed"
	SubscriptionExpired   SubscriptionStatus = "expired"
)

// Grants reports whether the subscription entitles the organization to its
// plan. Dodo keeps access open while past due, and revokes it on hold and
// while paused.
func (status SubscriptionStatus) Grants() bool {
	return status == SubscriptionActive || status == SubscriptionPastDue
}

// Open reports whether the subscription still belongs to the organization: it
// can be paid, resumed or fixed in the customer portal, so a second one must
// not be started beside it.
func (status SubscriptionStatus) Open() bool {
	switch status {
	case SubscriptionActive, SubscriptionPastDue, SubscriptionOnHold, SubscriptionPaused:
		return true
	default:
		return false
	}
}

// Ended reports whether the subscription is over for good.
func (status SubscriptionStatus) Ended() bool {
	return status == SubscriptionCancelled || status == SubscriptionExpired
}

type Role string

const RoleOwner Role = "owner"

// Membership is the caller's place in an organization, as auth reports it.
type Membership struct {
	OrganizationID   string
	OrganizationName string
	UserID           string
	Email            string
	DisplayName      string
	Role             Role
}

type Organization struct {
	ID             string
	Name           string
	CustomerID     string
	SubscriptionID string
	Entitlements   Entitlements
	Deleted        bool
}

type Entitlements struct {
	Plan        string
	Status      string
	MemberLimit *int
	GraceEndsAt *time.Time
	Revision    int64
}

// Equal compares everything but the revision.
func (entitlements Entitlements) Equal(other Entitlements) bool {
	return entitlements.Plan == other.Plan &&
		entitlements.Status == other.Status &&
		equalInt(entitlements.MemberLimit, other.MemberLimit) &&
		equalTime(entitlements.GraceEndsAt, other.GraceEndsAt)
}

type Subscription struct {
	ID                      string
	OrganizationID          string
	CustomerID              string
	CustomerEmail           string
	ProductID               string
	Quantity                int
	Status                  SubscriptionStatus
	CancelAtNextBillingDate bool
	NextBillingDate         time.Time
	PastDueEndsAt           *time.Time
	BillingInterval         string
	BillingIntervalCount    int
	// EventAt is when the webhook this state came from was sent, which orders
	// deliveries that arrive out of order.
	EventAt time.Time
}

// Event is a domain event recorded in the outbox.
type Event struct {
	ID         string
	DetailType string
	OccurredAt time.Time
	Data       json.RawMessage
}

// Renewal is an active subscription whose next billing date is close.
type Renewal struct {
	Subscription     Subscription
	OrganizationName string
}

// OrganizationState is an organization and its current subscription, read
// inside the transaction that will write back their Change.
type OrganizationState struct {
	Organization Organization
	// Subscription is the organization's current subscription, if it has one.
	Subscription *Subscription
}

// WebhookState is what a subscription webhook is applied to.
type WebhookState struct {
	Organization Organization
	// Previous is the stored state of the subscription the webhook is about.
	Previous *Subscription
	// Current is the organization's current subscription when that is a
	// different one.
	Current *Subscription
}

// Change is what an update writes in one transaction.
type Change struct {
	// Organization replaces the stored organization when set.
	Organization *Organization
	// NameUpdatedAt orders renames: a name older than the stored one is kept
	// out. Zero leaves the name as Organization has it.
	NameUpdatedAt time.Time
	// Subscription is inserted or replaces the stored one when set.
	Subscription *Subscription
	Events       []Event
}

type WebhookDelivery struct {
	ID   string
	Type string
}

type WebhookOutcome string

const (
	WebhookApplied   WebhookOutcome = "applied"
	WebhookDuplicate WebhookOutcome = "duplicate"
	// WebhookUnmatched: the subscription belongs to no organization billing
	// knows, as when it was created outside Oryxa's checkout.
	WebhookUnmatched WebhookOutcome = "unmatched"
)

func equalInt(left, right *int) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func equalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Equal(*right)
}
