package application

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"time"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	billingeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/billing/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"
	"github.com/Shubham04-oss/oryxa-go/email/internal/delivery"
	"github.com/Shubham04-oss/oryxa-go/email/internal/mail"
)

type renderer interface {
	Render(string, any) (mail.Message, error)
}

type deliverer interface {
	Deliver(context.Context, delivery.Request) (delivery.Outcome, error)
	RecordFeedback(context.Context, delivery.Feedback) error
}

type Service struct {
	templates  renderer
	deliveries deliverer
	now        func() time.Time
}

func New(templates renderer, deliveries deliverer) *Service {
	return &Service{templates: templates, deliveries: deliveries, now: time.Now}
}

// UserRegistered welcomes a user the first time they sign in: a Google
// sign-up, or a password account's first login after verifying its email.
func (s *Service) UserRegistered(ctx context.Context, event delivery.EventRef, data autheventsv1.UserRegistered) error {
	if data.Email == "" {
		return errors.New("event has no email")
	}
	message, err := s.templates.Render("welcome", welcome{Name: data.DisplayName})
	if err != nil {
		return err
	}
	_, err = s.deliveries.Deliver(ctx, delivery.Request{Event: event, Recipient: data.Email, Message: message})
	return err
}

type welcome struct {
	Name string
}

// action is what every email carrying one of auth's links renders with.
type action struct {
	Email            string
	ActionURL        string
	ActionLabel      string
	ExpiresIn        string
	OrganizationName string
	Role             string
}

func (s *Service) EmailVerificationRequested(ctx context.Context, event delivery.EventRef, data autheventsv1.EmailVerificationDeliveryRequested) error {
	return s.deliverAction(ctx, event, "verify-email", data.Email, data.ExpiresAt, action{
		Email:       data.Email,
		ActionURL:   data.VerificationURL,
		ActionLabel: "Confirm email address",
	})
}

func (s *Service) PasswordResetRequested(ctx context.Context, event delivery.EventRef, data autheventsv1.PasswordResetDeliveryRequested) error {
	return s.deliverAction(ctx, event, "password-reset", data.Email, data.ExpiresAt, action{
		Email:       data.Email,
		ActionURL:   data.ResetURL,
		ActionLabel: "Choose a new password",
	})
}

func (s *Service) MemberInvited(ctx context.Context, event delivery.EventRef, data autheventsv1.MemberInvited) error {
	if data.Quiet {
		return nil
	}
	if data.OrganizationName == "" || data.Role == "" {
		return errors.New("invitation has no organization name or role")
	}
	return s.deliverAction(ctx, event, "invitation", data.Email, data.ExpiresAt, action{
		Email:            data.Email,
		ActionURL:        data.AcceptURL,
		ActionLabel:      "Accept invitation",
		OrganizationName: data.OrganizationName,
		Role:             data.Role,
	})
}

// MemberRoleChanged tells a member that an owner changed their role. A member
// who changed their own role already knows.
func (s *Service) MemberRoleChanged(ctx context.Context, event delivery.EventRef, data autheventsv1.MemberRoleChanged) error {
	if data.ChangedBy == data.UserID {
		return nil
	}
	if data.PreviousRole == "" || data.Role == "" {
		return errors.New("role change has no roles")
	}
	return s.deliverNotice(ctx, event, "role-changed", data.Email, notice{
		OrganizationName: data.OrganizationName,
		Role:             data.Role,
		PreviousRole:     data.PreviousRole,
	})
}

// MemberRemoved tells a member they lost access to an organization because
// someone removed them or deleted it. Nobody is emailed for leaving, or for
// deleting an organization themselves.
func (s *Service) MemberRemoved(ctx context.Context, event delivery.EventRef, data autheventsv1.MemberRemoved) error {
	if data.RemovedBy == data.UserID || data.Reason == autheventsv1.MemberRemovedReasonLeft {
		return nil
	}
	template := "member-removed"
	if data.Reason == autheventsv1.MemberRemovedReasonOrganizationDeleted {
		template = "organization-deleted"
	}
	return s.deliverNotice(ctx, event, template, data.Email, notice{OrganizationName: data.OrganizationName})
}

// MemberInvitationRevoked tells an invitee that their invitation's link no
// longer works.
// MemberPinged emails a team member that their team was called, when they
// asked for those notices. Teams owns the notify_email switch. Listing is
// not mailed — it is too noisy — and the person who ran the tool already
// knows.
func (s *Service) MemberPinged(ctx context.Context, event delivery.EventRef, data teamseventsv1.MemberPinged) error {
	if !data.NotifyEmail || data.CalledBy == data.UserID {
		return nil
	}
	if data.Tool != teamseventsv1.ToolCallTeam && data.Tool != teamseventsv1.ToolCallTeamMembers {
		return nil
	}
	if data.TeamName == "" {
		return errors.New("event has no team_name")
	}
	return s.deliverNotice(ctx, event, "team-called", data.Email, notice{
		OrganizationName: data.OrganizationName,
		TeamName:         data.TeamName,
	})
}

func (s *Service) MemberInvitationRevoked(ctx context.Context, event delivery.EventRef, data autheventsv1.MemberInvitationRevoked) error {
	return s.deliverNotice(ctx, event, "invitation-revoked", data.Email, notice{OrganizationName: data.OrganizationName})
}

// notice is what the organization emails render with.
type notice struct {
	OrganizationName string
	TeamName         string
	Role             string
	PreviousRole     string
}

func (s *Service) deliverNotice(ctx context.Context, event delivery.EventRef, template, recipient string, data notice) error {
	if recipient == "" {
		return errors.New("event has no email")
	}
	if data.OrganizationName == "" {
		return errors.New("event has no organization_name")
	}
	message, err := s.templates.Render(template, data)
	if err != nil {
		return err
	}
	_, err = s.deliveries.Deliver(ctx, delivery.Request{Event: event, Recipient: recipient, Message: message})
	return err
}

// SubscriptionPaymentFailed tells the billing contact a renewal payment
// failed, and by when to fix it if a grace period is open.
func (s *Service) SubscriptionPaymentFailed(ctx context.Context, event delivery.EventRef, data billingeventsv1.SubscriptionPaymentFailed) error {
	billing := billingNotice{
		OrganizationName: data.OrganizationName,
		ActionURL:        data.BillingURL,
		ActionLabel:      "Update payment method",
	}
	if data.GraceEndsAt != nil {
		billing.GraceEndsAt = formatDeadline(*data.GraceEndsAt)
	}
	return s.deliverBillingNotice(ctx, event, "payment-failed", data.BillingEmail, time.Time{}, billing)
}

// SubscriptionEnded tells the billing contact a subscription was cancelled or
// reached the end of its term.
func (s *Service) SubscriptionEnded(ctx context.Context, event delivery.EventRef, data billingeventsv1.SubscriptionEnded) error {
	if data.Reason != billingeventsv1.SubscriptionEndedReasonCancelled && data.Reason != billingeventsv1.SubscriptionEndedReasonExpired {
		return fmt.Errorf("unknown subscription end reason %q", data.Reason)
	}
	return s.deliverBillingNotice(ctx, event, "subscription-ended", data.BillingEmail, time.Time{}, billingNotice{
		OrganizationName: data.OrganizationName,
		Expired:          data.Reason == billingeventsv1.SubscriptionEndedReasonExpired,
		ActionURL:        data.BillingURL,
		ActionLabel:      "View billing",
	})
}

// SubscriptionRenewalUpcoming reminds the billing contact of a renewal. A
// reminder handled after the renewal is no longer worth sending.
func (s *Service) SubscriptionRenewalUpcoming(ctx context.Context, event delivery.EventRef, data billingeventsv1.SubscriptionRenewalUpcoming) error {
	if data.RenewsAt.IsZero() {
		return errors.New("event has no renews_at")
	}
	return s.deliverBillingNotice(ctx, event, "renewal-upcoming", data.BillingEmail, data.RenewsAt, billingNotice{
		OrganizationName: data.OrganizationName,
		Plan:             data.Plan,
		RenewsOn:         formatDate(data.RenewsAt),
		ActionURL:        data.BillingURL,
		ActionLabel:      "Manage billing",
	})
}

// billingNotice is what the billing emails render with.
type billingNotice struct {
	OrganizationName string
	Plan             string
	GraceEndsAt      string
	RenewsOn         string
	Expired          bool
	ActionURL        string
	ActionLabel      string
}

func (s *Service) deliverBillingNotice(ctx context.Context, event delivery.EventRef, template, recipient string, expiresAt time.Time, data billingNotice) error {
	if recipient == "" {
		return errors.New("event has no billing_email")
	}
	if data.OrganizationName == "" {
		return errors.New("event has no organization_name")
	}
	if err := validateActionURL(data.ActionURL); err != nil {
		return err
	}
	message, err := s.templates.Render(template, data)
	if err != nil {
		return err
	}
	_, err = s.deliveries.Deliver(ctx, delivery.Request{Event: event, Recipient: recipient, Message: message, ExpiresAt: expiresAt})
	return err
}

// formatDate and formatDeadline write times as the emails put them, in UTC
// so every reader sees the same moment.
func formatDate(value time.Time) string {
	return value.UTC().Format("2 January 2006")
}

func formatDeadline(value time.Time) string {
	return value.UTC().Format("2 January 2006, 15:04 MST")
}

// SESFeedback records what SES reported about an email this service sent.
func (s *Service) SESFeedback(ctx context.Context, feedback delivery.Feedback) error {
	return s.deliveries.RecordFeedback(ctx, feedback)
}

// deliverAction sends an email built around one of auth's single-use links.
// Auth generated and owns the link; this service only carries it, and never
// lets it into an error or a log line.
func (s *Service) deliverAction(ctx context.Context, event delivery.EventRef, template, recipient string, expiresAt time.Time, data action) error {
	if recipient == "" {
		return errors.New("event has no email")
	}
	if expiresAt.IsZero() {
		return errors.New("event has no expires_at")
	}
	if err := validateActionURL(data.ActionURL); err != nil {
		return err
	}

	data.ExpiresIn = expiresIn(expiresAt.Sub(s.now()))
	message, err := s.templates.Render(template, data)
	if err != nil {
		return err
	}
	_, err = s.deliveries.Deliver(ctx, delivery.Request{
		Event:     event,
		Recipient: recipient,
		Message:   message,
		ExpiresAt: expiresAt,
	})
	return err
}

// validateActionURL keeps a malformed link out of an email. Auth only issues
// HTTPS links outside local development.
func validateActionURL(value string) error {
	if value == "" {
		return errors.New("event has no action URL")
	}
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return errors.New("action URL is not an absolute URL")
	}
	switch hostname := parsed.Hostname(); {
	case parsed.Scheme == "https":
		return nil
	case parsed.Scheme == "http" && (hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1"):
		return nil
	default:
		return errors.New("action URL must use HTTPS, except on localhost")
	}
}

// expiresIn describes how long a link has left, as the email will put it.
func expiresIn(remaining time.Duration) string {
	switch {
	case remaining < 90*time.Minute:
		return plural(max(1, int(math.Ceil(remaining.Minutes()))), "minute")
	case remaining < 48*time.Hour:
		return plural(int(math.Round(remaining.Hours())), "hour")
	default:
		return plural(int(math.Round(remaining.Hours()/24)), "day")
	}
}

func plural(count int, unit string) string {
	if count == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", count, unit)
}
