package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	billingeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/billing/v1"

	"github.com/Shubham04-oss/oryxa-go/billing/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/catalog"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/dodo"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/domain"
)

type Store interface {
	Organization(context.Context, string) (domain.Organization, *domain.Subscription, error)
	UpdateOrganization(context.Context, string, func(domain.OrganizationState) (domain.Change, error)) error
	ApplyWebhook(context.Context, domain.WebhookDelivery, domain.Subscription, func(domain.WebhookState) (domain.Change, error)) (domain.WebhookOutcome, error)
	ClaimRenewals(context.Context, time.Time, time.Duration, []string, int, func(domain.Renewal) (*domain.Event, error)) (int, error)
	OrganizationIDs(context.Context) ([]string, error)
}

type Payments interface {
	CreateCustomer(context.Context, dodo.CustomerRequest) (string, error)
	CreateCheckout(context.Context, dodo.CheckoutRequest) (string, error)
	CreatePortalSession(ctx context.Context, customerID, returnURL string) (string, error)
	CancelSubscription(ctx context.Context, subscriptionID string) error
}

type Memberships interface {
	Membership(context.Context, string, authclient.Credentials) (domain.Membership, error)
}

type Config struct {
	// BillingURL is the app's billing page. Checkout and the customer portal
	// return there and billing emails link to it, with organization_id added.
	BillingURL string
	// RenewalReminderWindow is how long before a renewal its reminder is
	// sent. Zero sends none.
	RenewalReminderWindow time.Duration
}

// renewalReminderIntervals are the billing periods long enough for a reminder
// to help; weekly and daily plans renew too often for one.
var renewalReminderIntervals = []string{"Month", "Year"}

const renewalClaimBatch = 100

type Service struct {
	store       Store
	payments    Payments
	memberships Memberships
	catalog     *catalog.Catalog
	config      Config
	logger      *slog.Logger
	now         func() time.Time
}

// New builds the service. payments is nil when Dodo Payments isn't configured,
// which leaves checkout and the customer portal unavailable.
func New(store Store, payments Payments, memberships Memberships, plans *catalog.Catalog, config Config, logger *slog.Logger) *Service {
	return &Service{
		store:       store,
		payments:    payments,
		memberships: memberships,
		catalog:     plans,
		config:      config,
		logger:      logger,
		now:         time.Now,
	}
}

type Summary struct {
	OrganizationID string
	// CanManage reports whether the caller may start checkout or open the
	// customer portal.
	CanManage    bool
	Entitlements domain.Entitlements
	Subscription *SubscriptionSummary
}

type SubscriptionSummary struct {
	Plan     string
	Status   domain.SubscriptionStatus
	Quantity int
	// RenewsAt is set while the subscription will renew, EndsAt once it is
	// set to end at its next billing date.
	RenewsAt    *time.Time
	EndsAt      *time.Time
	GraceEndsAt *time.Time
}

func (s *Service) Plans() ([]catalog.Plan, catalog.Allowance) {
	return s.catalog.Plans(), s.catalog.Unpaid()
}

// Summary is an organization's billing as any of its members may see it.
func (s *Service) Summary(ctx context.Context, credentials authclient.Credentials, organizationID string) (Summary, error) {
	membership, err := s.memberships.Membership(ctx, organizationID, credentials)
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{OrganizationID: organizationID, CanManage: membership.Role == domain.RoleOwner}

	organization, subscription, err := s.store.Organization(ctx, organizationID)
	if errors.Is(err, domain.ErrNotFound) {
		summary.Entitlements, _ = s.catalog.Entitlements(nil)
		return summary, nil
	}
	if err != nil {
		return Summary{}, err
	}
	summary.Entitlements = organization.Entitlements
	if organization.Entitlements.Revision == 0 {
		summary.Entitlements, _ = s.catalog.Entitlements(subscription)
	}
	if subscription != nil {
		summary.Subscription = s.describeSubscription(*subscription)
	}
	return summary, nil
}

func (s *Service) describeSubscription(subscription domain.Subscription) *SubscriptionSummary {
	summary := &SubscriptionSummary{Status: subscription.Status, Quantity: subscription.Quantity}
	if plan, ok := s.catalog.PlanForProduct(subscription.ProductID); ok {
		summary.Plan = plan.Key
	}
	if subscription.Status == domain.SubscriptionActive && !subscription.NextBillingDate.IsZero() {
		nextBillingDate := subscription.NextBillingDate
		if subscription.CancelAtNextBillingDate {
			summary.EndsAt = &nextBillingDate
		} else {
			summary.RenewsAt = &nextBillingDate
		}
	}
	if subscription.Status == domain.SubscriptionPastDue {
		summary.GraceEndsAt = subscription.PastDueEndsAt
	}
	return summary
}

// StartCheckout returns a Dodo checkout URL for the organization to subscribe
// to the plan. Only an owner may start one, and not while the organization
// has a subscription it can still pay, resume or fix in the customer portal.
func (s *Service) StartCheckout(ctx context.Context, credentials authclient.Credentials, organizationID, planKey string, quantity int) (string, error) {
	if s.payments == nil {
		return "", domain.ErrNotConfigured
	}
	membership, err := s.owner(ctx, organizationID, credentials)
	if err != nil {
		return "", err
	}
	plan, ok := s.catalog.Plan(planKey)
	if !ok {
		return "", fmt.Errorf("unknown plan %q: %w", planKey, domain.ErrInvalid)
	}
	units, err := plan.CheckoutQuantity(quantity)
	if err != nil {
		return "", err
	}

	var customerID string
	err = s.store.UpdateOrganization(ctx, organizationID, func(state domain.OrganizationState) (domain.Change, error) {
		organization := state.Organization
		if organization.Deleted {
			return domain.Change{}, domain.ErrNotFound
		}
		if state.Subscription != nil && state.Subscription.Status.Open() {
			return domain.Change{}, fmt.Errorf("organization already has a subscription (%s); manage it in the customer portal: %w", state.Subscription.Status, domain.ErrConflict)
		}
		customerID = organization.CustomerID
		organization.Name = membership.OrganizationName
		return domain.Change{Organization: &organization, NameUpdatedAt: s.now()}, nil
	})
	if err != nil {
		return "", err
	}
	if customerID == "" {
		if customerID, err = s.createCustomer(ctx, membership); err != nil {
			return "", err
		}
	}

	return s.payments.CreateCheckout(ctx, dodo.CheckoutRequest{
		OrganizationID: organizationID,
		CustomerID:     customerID,
		ProductID:      plan.ProductID,
		Quantity:       units,
		ReturnURL:      s.billingURL(organizationID),
	})
}

// createCustomer creates the organization's Dodo customer, keeping the one a
// concurrent checkout recorded first if it lost that race.
func (s *Service) createCustomer(ctx context.Context, membership domain.Membership) (string, error) {
	name := membership.DisplayName
	if name == "" {
		name = membership.OrganizationName
	}
	if name == "" {
		name = membership.Email
	}
	created, err := s.payments.CreateCustomer(ctx, dodo.CustomerRequest{
		OrganizationID: membership.OrganizationID,
		Email:          membership.Email,
		Name:           name,
	})
	if err != nil {
		return "", err
	}

	customerID := created
	err = s.store.UpdateOrganization(ctx, membership.OrganizationID, func(state domain.OrganizationState) (domain.Change, error) {
		organization := state.Organization
		if organization.CustomerID != "" {
			customerID = organization.CustomerID
			return domain.Change{}, nil
		}
		organization.CustomerID = created
		return domain.Change{Organization: &organization}, nil
	})
	if err != nil {
		return "", err
	}
	if customerID != created {
		s.logger.WarnContext(ctx, "another checkout created this organization's Dodo customer first; the extra customer is unused",
			"organization_id", membership.OrganizationID, "unused_customer_id", created)
	}
	return customerID, nil
}

// OpenPortal returns a link to the Dodo customer portal for the organization.
func (s *Service) OpenPortal(ctx context.Context, credentials authclient.Credentials, organizationID string) (string, error) {
	if s.payments == nil {
		return "", domain.ErrNotConfigured
	}
	if _, err := s.owner(ctx, organizationID, credentials); err != nil {
		return "", err
	}
	organization, _, err := s.store.Organization(ctx, organizationID)
	if err != nil {
		return "", err
	}
	if organization.CustomerID == "" {
		return "", fmt.Errorf("organization has no billing account yet: %w", domain.ErrNotFound)
	}
	return s.payments.CreatePortalSession(ctx, organization.CustomerID, s.billingURL(organizationID))
}

func (s *Service) owner(ctx context.Context, organizationID string, credentials authclient.Credentials) (domain.Membership, error) {
	membership, err := s.memberships.Membership(ctx, organizationID, credentials)
	if err != nil {
		return domain.Membership{}, err
	}
	if membership.Role != domain.RoleOwner {
		return domain.Membership{}, domain.ErrForbidden
	}
	return membership, nil
}

// HandleWebhook applies a verified Dodo webhook. Only subscription events
// change anything.
func (s *Service) HandleWebhook(ctx context.Context, webhook dodo.Webhook) error {
	if webhook.Subscription == nil {
		return nil
	}
	incoming := *webhook.Subscription
	outcome, err := s.store.ApplyWebhook(ctx, domain.WebhookDelivery{ID: webhook.ID, Type: webhook.Type}, incoming,
		func(state domain.WebhookState) (domain.Change, error) {
			return s.webhookChange(ctx, state, incoming)
		})
	if err != nil {
		return fmt.Errorf("apply %s webhook %s: %w", webhook.Type, webhook.ID, err)
	}
	switch outcome {
	case domain.WebhookUnmatched:
		s.logger.WarnContext(ctx, "subscription webhook matches no organization",
			"webhook_id", webhook.ID, "type", webhook.Type, "subscription_id", incoming.ID, "customer_id", incoming.CustomerID)
	case domain.WebhookDuplicate:
		s.logger.InfoContext(ctx, "subscription webhook already applied", "webhook_id", webhook.ID, "type", webhook.Type)
	default:
		s.logger.InfoContext(ctx, "subscription webhook applied",
			"webhook_id", webhook.ID, "type", webhook.Type, "subscription_id", incoming.ID, "status", incoming.Status)
	}
	return nil
}

func (s *Service) webhookChange(ctx context.Context, state domain.WebhookState, incoming domain.Subscription) (domain.Change, error) {
	previous := state.Previous
	if previous != nil && incoming.EventAt.Before(previous.EventAt) {
		s.logger.InfoContext(ctx, "subscription webhook older than the stored state; ignored",
			"subscription_id", incoming.ID, "status", incoming.Status)
		return domain.Change{}, nil
	}

	next := incoming
	organization := state.Organization
	next.OrganizationID = organization.ID
	switch {
	case next.Status != domain.SubscriptionPastDue:
		next.PastDueEndsAt = nil
	case next.PastDueEndsAt == nil && previous != nil && previous.Status == domain.SubscriptionPastDue:
		// Dodo sends the deadline with past_due events; keep it for later ones.
		next.PastDueEndsAt = previous.PastDueEndsAt
	}
	if organization.CustomerID == "" {
		organization.CustomerID = next.CustomerID
	}

	// An organization has one current subscription. An ended one gives way to
	// a new one, and events about a subscription that has been replaced only
	// update its own record.
	current := state.Current
	isCurrent := organization.SubscriptionID == "" || organization.SubscriptionID == next.ID || current == nil ||
		(next.Status.Open() && !current.Status.Open())
	if !isCurrent {
		return domain.Change{Subscription: &next, Organization: &organization}, nil
	}
	organization.SubscriptionID = next.ID

	var events []domain.Event
	if !organization.Deleted {
		event, err := s.refreshEntitlements(ctx, &organization, &next)
		if err != nil {
			return domain.Change{}, err
		}
		if event != nil {
			events = append(events, *event)
		}
	}

	var previousStatus domain.SubscriptionStatus
	if previous != nil {
		previousStatus = previous.Status
	}
	var notice *domain.Event
	var err error
	switch {
	case organization.Deleted && !next.Status.Ended():
	case next.Status == domain.SubscriptionPastDue && previousStatus != domain.SubscriptionPastDue:
		notice, err = s.paymentFailed(ctx, organization, next, next.PastDueEndsAt)
	case next.Status == domain.SubscriptionOnHold && previousStatus != domain.SubscriptionOnHold:
		notice, err = s.paymentFailed(ctx, organization, next, nil)
	case next.Status.Ended() && previousStatus.Open():
		notice, err = s.subscriptionEnded(ctx, organization, next)
	}
	if err != nil {
		return domain.Change{}, err
	}
	if notice != nil {
		events = append(events, *notice)
	}
	return domain.Change{Subscription: &next, Organization: &organization, Events: events}, nil
}

// refreshEntitlements recomputes the organization's entitlements from its
// current subscription, and returns the event announcing them if they changed
// or were never announced.
func (s *Service) refreshEntitlements(ctx context.Context, organization *domain.Organization, subscription *domain.Subscription) (*domain.Event, error) {
	entitlements, known := s.catalog.Entitlements(subscription)
	if !known {
		s.logger.ErrorContext(ctx, "subscribed product is missing from the plan catalog; the organization is left without limits",
			"organization_id", organization.ID, "product_id", subscription.ProductID)
	}
	if organization.Entitlements.Revision > 0 && entitlements.Equal(organization.Entitlements) {
		return nil, nil
	}
	entitlements.Revision = organization.Entitlements.Revision + 1
	organization.Entitlements = entitlements

	now := s.now().UTC()
	event, err := newEvent(billingeventsv1.EntitlementsChangedDetailType, billingeventsv1.EntitlementsChanged{
		OrganizationID: organization.ID,
		Revision:       entitlements.Revision,
		Plan:           entitlements.Plan,
		Status:         entitlements.Status,
		MemberLimit:    entitlements.MemberLimit,
		GraceEndsAt:    entitlements.GraceEndsAt,
		ChangedAt:      now,
	}, now)
	if err != nil {
		return nil, err
	}
	return &event, nil
}

func (s *Service) paymentFailed(ctx context.Context, organization domain.Organization, subscription domain.Subscription, graceEndsAt *time.Time) (*domain.Event, error) {
	if !s.canNotify(ctx, organization, subscription) {
		return nil, nil
	}
	event, err := newEvent(billingeventsv1.SubscriptionPaymentFailedDetailType, billingeventsv1.SubscriptionPaymentFailed{
		OrganizationID:   organization.ID,
		OrganizationName: organization.Name,
		SubscriptionID:   subscription.ID,
		BillingEmail:     subscription.CustomerEmail,
		GraceEndsAt:      graceEndsAt,
		BillingURL:       s.billingURL(organization.ID),
		FailedAt:         subscription.EventAt.UTC(),
	}, s.now())
	if err != nil {
		return nil, err
	}
	return &event, nil
}

func (s *Service) subscriptionEnded(ctx context.Context, organization domain.Organization, subscription domain.Subscription) (*domain.Event, error) {
	if !s.canNotify(ctx, organization, subscription) {
		return nil, nil
	}
	reason := billingeventsv1.SubscriptionEndedReasonCancelled
	if subscription.Status == domain.SubscriptionExpired {
		reason = billingeventsv1.SubscriptionEndedReasonExpired
	}
	event, err := newEvent(billingeventsv1.SubscriptionEndedDetailType, billingeventsv1.SubscriptionEnded{
		OrganizationID:   organization.ID,
		OrganizationName: organization.Name,
		SubscriptionID:   subscription.ID,
		BillingEmail:     subscription.CustomerEmail,
		Reason:           reason,
		BillingURL:       s.billingURL(organization.ID),
		EndedAt:          subscription.EventAt.UTC(),
	}, s.now())
	if err != nil {
		return nil, err
	}
	return &event, nil
}

// canNotify reports whether billing knows enough to email about the
// subscription: whom to write to, and which organization to name.
func (s *Service) canNotify(ctx context.Context, organization domain.Organization, subscription domain.Subscription) bool {
	if organization.Name != "" && subscription.CustomerEmail != "" {
		return true
	}
	s.logger.WarnContext(ctx, "billing email skipped: organization name or billing email unknown",
		"organization_id", organization.ID, "subscription_id", subscription.ID, "status", subscription.Status)
	return false
}

// OrganizationCreated records a new organization and announces its first
// entitlements, so services that enforce them know its limits from the start.
func (s *Service) OrganizationCreated(ctx context.Context, data autheventsv1.OrganizationCreated) error {
	return s.recordOrganizationName(ctx, data.OrganizationID, data.Name, data.CreatedAt)
}

func (s *Service) OrganizationUpdated(ctx context.Context, data autheventsv1.OrganizationUpdated) error {
	return s.recordOrganizationName(ctx, data.OrganizationID, data.Name, data.UpdatedAt)
}

func (s *Service) recordOrganizationName(ctx context.Context, organizationID, name string, at time.Time) error {
	if organizationID == "" || name == "" {
		return errors.New("organization event has no organization_id or name")
	}
	return s.store.UpdateOrganization(ctx, organizationID, func(state domain.OrganizationState) (domain.Change, error) {
		organization := state.Organization
		if organization.Deleted {
			return domain.Change{}, nil
		}
		organization.Name = name
		change := domain.Change{Organization: &organization, NameUpdatedAt: at}
		if organization.Entitlements.Revision == 0 {
			event, err := s.refreshEntitlements(ctx, &organization, state.Subscription)
			if err != nil {
				return domain.Change{}, err
			}
			if event != nil {
				change.Events = append(change.Events, *event)
			}
		}
		return change, nil
	})
}

// OrganizationDeleted cancels the organization's subscription, so a deleted
// organization is never charged again, and marks it deleted.
func (s *Service) OrganizationDeleted(ctx context.Context, data autheventsv1.OrganizationDeleted) error {
	if data.OrganizationID == "" {
		return errors.New("organization event has no organization_id")
	}
	_, subscription, err := s.store.Organization(ctx, data.OrganizationID)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if subscription != nil && subscription.Status.Open() {
		if s.payments == nil {
			return fmt.Errorf("organization %s was deleted with an open subscription, and Dodo Payments is not configured to cancel it", data.OrganizationID)
		}
		if err := s.payments.CancelSubscription(ctx, subscription.ID); err != nil {
			return err
		}
		s.logger.InfoContext(ctx, "subscription cancelled because its organization was deleted",
			"organization_id", data.OrganizationID, "subscription_id", subscription.ID)
	}
	return s.store.UpdateOrganization(ctx, data.OrganizationID, func(state domain.OrganizationState) (domain.Change, error) {
		organization := state.Organization
		if organization.Deleted {
			return domain.Change{}, nil
		}
		organization.Deleted = true
		return domain.Change{Organization: &organization}, nil
	})
}

// SendRenewalReminders records a reminder for each subscription renewing
// within the reminder window that hasn't had one for this renewal, and
// returns how many it handled.
func (s *Service) SendRenewalReminders(ctx context.Context) (int, error) {
	if s.config.RenewalReminderWindow <= 0 {
		return 0, nil
	}
	now := s.now().UTC()
	return s.store.ClaimRenewals(ctx, now, s.config.RenewalReminderWindow, renewalReminderIntervals, renewalClaimBatch,
		func(renewal domain.Renewal) (*domain.Event, error) {
			subscription := renewal.Subscription
			organization := domain.Organization{ID: subscription.OrganizationID, Name: renewal.OrganizationName}
			if !s.canNotify(ctx, organization, subscription) {
				return nil, nil
			}
			data := billingeventsv1.SubscriptionRenewalUpcoming{
				OrganizationID:   subscription.OrganizationID,
				OrganizationName: renewal.OrganizationName,
				SubscriptionID:   subscription.ID,
				BillingEmail:     subscription.CustomerEmail,
				RenewsAt:         subscription.NextBillingDate.UTC(),
				BillingURL:       s.billingURL(subscription.OrganizationID),
			}
			if plan, ok := s.catalog.PlanForProduct(subscription.ProductID); ok {
				data.Plan = plan.Key
			}
			event, err := newEvent(billingeventsv1.SubscriptionRenewalUpcomingDetailType, data, now)
			if err != nil {
				return nil, err
			}
			return &event, nil
		})
}

// ReconcileEntitlements recomputes every organization's entitlements against
// the current plan catalog and announces those that changed, which is how an
// edited catalog reaches organizations that are already subscribed.
func (s *Service) ReconcileEntitlements(ctx context.Context) (int, error) {
	organizationIDs, err := s.store.OrganizationIDs(ctx)
	if err != nil {
		return 0, err
	}
	changed := 0
	for _, organizationID := range organizationIDs {
		err := s.store.UpdateOrganization(ctx, organizationID, func(state domain.OrganizationState) (domain.Change, error) {
			organization := state.Organization
			if organization.Deleted {
				return domain.Change{}, nil
			}
			event, err := s.refreshEntitlements(ctx, &organization, state.Subscription)
			if err != nil || event == nil {
				return domain.Change{}, err
			}
			changed++
			return domain.Change{Organization: &organization, Events: []domain.Event{*event}}, nil
		})
		if err != nil {
			return changed, fmt.Errorf("reconcile organization %s: %w", organizationID, err)
		}
	}
	return changed, nil
}

func (s *Service) billingURL(organizationID string) string {
	parsed, err := url.Parse(s.config.BillingURL)
	if err != nil {
		return s.config.BillingURL
	}
	query := parsed.Query()
	query.Set("organization_id", organizationID)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func newEvent(detailType string, data any, occurredAt time.Time) (domain.Event, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return domain.Event{}, fmt.Errorf("create event id: %w", err)
	}
	detail, err := json.Marshal(data)
	if err != nil {
		return domain.Event{}, fmt.Errorf("encode %s: %w", detailType, err)
	}
	return domain.Event{
		ID:         "evt_" + hex.EncodeToString(value),
		DetailType: detailType,
		OccurredAt: occurredAt.UTC(),
		Data:       detail,
	}, nil
}
