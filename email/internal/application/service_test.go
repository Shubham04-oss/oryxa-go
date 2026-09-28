package application

import (
	"context"
	"strings"
	"testing"
	"time"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	billingeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/billing/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"
	"github.com/Shubham04-oss/oryxa-go/email/internal/delivery"
	"github.com/Shubham04-oss/oryxa-go/email/internal/mail"
)

var testNow = time.Date(2026, time.September, 13, 12, 0, 0, 0, time.UTC)

type fakeDeliverer struct {
	requests []delivery.Request
	feedback []delivery.Feedback
}

func (f *fakeDeliverer) Deliver(_ context.Context, request delivery.Request) (delivery.Outcome, error) {
	f.requests = append(f.requests, request)
	return delivery.OutcomeSent, nil
}

func (f *fakeDeliverer) RecordFeedback(_ context.Context, feedback delivery.Feedback) error {
	f.feedback = append(f.feedback, feedback)
	return nil
}

func newTestService(t *testing.T) (*Service, *fakeDeliverer) {
	t.Helper()
	templates, err := mail.ParseTemplates()
	if err != nil {
		t.Fatalf("ParseTemplates() error = %v", err)
	}
	deliveries := &fakeDeliverer{}
	service := New(templates, deliveries)
	service.now = func() time.Time { return testNow }
	return service, deliveries
}

func ref(detailType string) delivery.EventRef {
	return delivery.EventRef{Source: autheventsv1.Source, DetailType: detailType, ID: "evt_1"}
}

func TestDeliveryRequestsSendTheirEmail(t *testing.T) {
	tests := []struct {
		name     string
		send     func(*Service) error
		template string
		link     string
		expires  string
		mentions string
	}{
		{
			name: "email verification",
			send: func(service *Service) error {
				return service.EmailVerificationRequested(context.Background(), ref(autheventsv1.EmailVerificationDeliveryRequestedDetailType), autheventsv1.EmailVerificationDeliveryRequested{
					RequestID: "act_1", UserID: "user-1", Email: "asha@example.com",
					VerificationURL: "https://app.example.com/auth/verify-email?token=verify", ExpiresAt: testNow.Add(24 * time.Hour),
				})
			},
			template: "verify-email",
			link:     "https://app.example.com/auth/verify-email?token=verify",
			expires:  "24 hours",
			mentions: "asha@example.com",
		},
		{
			name: "password reset",
			send: func(service *Service) error {
				return service.PasswordResetRequested(context.Background(), ref(autheventsv1.PasswordResetDeliveryRequestedDetailType), autheventsv1.PasswordResetDeliveryRequested{
					RequestID: "act_2", UserID: "user-1", Email: "asha@example.com",
					ResetURL: "https://app.example.com/auth/reset-password?token=reset", ExpiresAt: testNow.Add(30 * time.Minute),
				})
			},
			template: "password-reset",
			link:     "https://app.example.com/auth/reset-password?token=reset",
			expires:  "30 minutes",
			mentions: "asha@example.com",
		},
		{
			name: "member invitation",
			send: func(service *Service) error {
				return service.MemberInvited(context.Background(), ref(autheventsv1.MemberInvitedDetailType), autheventsv1.MemberInvited{
					InvitationID: "inv_1", OrganizationID: "org_1", OrganizationName: "Acme", Email: "ravi@example.com",
					Role: "admin", InvitedBy: "user-1", AcceptURL: "https://app.example.com/invitations/accept?token=invite",
					ExpiresAt: testNow.Add(7 * 24 * time.Hour),
				})
			},
			template: "invitation",
			link:     "https://app.example.com/invitations/accept?token=invite",
			expires:  "7 days",
			mentions: "Acme",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, deliveries := newTestService(t)
			if err := test.send(service); err != nil {
				t.Fatalf("error = %v", err)
			}
			if len(deliveries.requests) != 1 {
				t.Fatalf("deliveries = %d, want 1", len(deliveries.requests))
			}
			request := deliveries.requests[0]
			if request.Message.Template != test.template || request.Event.ID != "evt_1" || request.ExpiresAt.IsZero() {
				t.Fatalf("delivery request = %+v", request)
			}
			text := request.Message.Text
			if !strings.Contains(text, test.link) || !strings.Contains(text, "expires in "+test.expires) || !strings.Contains(text, test.mentions) {
				t.Fatalf("email lacks the link, the expiry (%s) or %q:\n%s", test.expires, test.mentions, text)
			}
		})
	}
}

func TestUserRegisteredSendsWelcome(t *testing.T) {
	service, deliveries := newTestService(t)

	err := service.UserRegistered(context.Background(), ref(autheventsv1.UserRegisteredDetailType), autheventsv1.UserRegistered{
		UserID: "user-1", Email: "asha@example.com", DisplayName: "Asha", RegisteredAt: testNow,
	})
	if err != nil {
		t.Fatalf("UserRegistered() error = %v", err)
	}
	if len(deliveries.requests) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(deliveries.requests))
	}
	request := deliveries.requests[0]
	if request.Message.Template != "welcome" || request.Recipient != "asha@example.com" || !request.ExpiresAt.IsZero() {
		t.Fatalf("delivery request = %+v, want a welcome to asha@example.com that never expires", request)
	}
	if !strings.Contains(request.Message.Text, "Hi Asha,") {
		t.Fatalf("welcome does not greet the user:\n%s", request.Message.Text)
	}
}

func TestUserRegisteredRequiresEmail(t *testing.T) {
	service, deliveries := newTestService(t)

	if err := service.UserRegistered(context.Background(), ref(autheventsv1.UserRegisteredDetailType), autheventsv1.UserRegistered{UserID: "user-1"}); err == nil {
		t.Fatal("UserRegistered() error = nil, want missing email error")
	}
	if len(deliveries.requests) != 0 {
		t.Fatalf("deliveries = %d, want 0", len(deliveries.requests))
	}
}

func TestOrganizationNoticesSendTheirEmail(t *testing.T) {
	tests := []struct {
		name      string
		send      func(*Service) error
		template  string
		recipient string
		mentions  string
	}{
		{
			name: "role changed by an owner",
			send: func(service *Service) error {
				return service.MemberRoleChanged(context.Background(), ref(autheventsv1.MemberRoleChangedDetailType), autheventsv1.MemberRoleChanged{
					OrganizationID: "org_1", OrganizationName: "Acme", UserID: "user-2", Email: "ravi@example.com",
					PreviousRole: "member", Role: "admin", ChangedBy: "user-1", ChangedAt: testNow,
				})
			},
			template: "role-changed", recipient: "ravi@example.com", mentions: "from member to admin",
		},
		{
			name: "removed by an admin",
			send: func(service *Service) error {
				return service.MemberRemoved(context.Background(), ref(autheventsv1.MemberRemovedDetailType), autheventsv1.MemberRemoved{
					OrganizationID: "org_1", OrganizationName: "Acme", UserID: "user-2", Email: "ravi@example.com",
					PreviousRole: "member", Reason: autheventsv1.MemberRemovedReasonRemoved, RemovedBy: "user-1", RemovedAt: testNow,
				})
			},
			template: "member-removed", recipient: "ravi@example.com", mentions: "removed you",
		},
		{
			name: "organization deleted by another owner",
			send: func(service *Service) error {
				return service.MemberRemoved(context.Background(), ref(autheventsv1.MemberRemovedDetailType), autheventsv1.MemberRemoved{
					OrganizationID: "org_1", OrganizationName: "Acme", UserID: "user-2", Email: "ravi@example.com",
					PreviousRole: "admin", Reason: autheventsv1.MemberRemovedReasonOrganizationDeleted, RemovedBy: "user-1", RemovedAt: testNow,
				})
			},
			template: "organization-deleted", recipient: "ravi@example.com", mentions: "deleted it",
		},
		{
			name: "removal from an event without a reason",
			send: func(service *Service) error {
				return service.MemberRemoved(context.Background(), ref(autheventsv1.MemberRemovedDetailType), autheventsv1.MemberRemoved{
					OrganizationID: "org_1", OrganizationName: "Acme", UserID: "user-2", Email: "ravi@example.com",
					PreviousRole: "member", RemovedBy: "user-1", RemovedAt: testNow,
				})
			},
			template: "member-removed", recipient: "ravi@example.com", mentions: "removed you",
		},
		{
			name: "invitation revoked",
			send: func(service *Service) error {
				return service.MemberInvitationRevoked(context.Background(), ref(autheventsv1.MemberInvitationRevokedDetailType), autheventsv1.MemberInvitationRevoked{
					InvitationID: "inv_1", OrganizationID: "org_1", OrganizationName: "Acme", Email: "sam@example.com",
					Role: "member", RevokedBy: "user-1", RevokedAt: testNow,
				})
			},
			template: "invitation-revoked", recipient: "sam@example.com", mentions: "no longer works",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, deliveries := newTestService(t)
			if err := test.send(service); err != nil {
				t.Fatalf("error = %v", err)
			}
			if len(deliveries.requests) != 1 {
				t.Fatalf("deliveries = %d, want 1", len(deliveries.requests))
			}
			request := deliveries.requests[0]
			if request.Message.Template != test.template || request.Recipient != test.recipient || !request.ExpiresAt.IsZero() {
				t.Fatalf("delivery request = %+v, want %s to %s", request, test.template, test.recipient)
			}
			if !strings.Contains(request.Message.Text, "Acme") || !strings.Contains(request.Message.Text, test.mentions) {
				t.Fatalf("email lacks the organization or %q:\n%s", test.mentions, request.Message.Text)
			}
		})
	}
}

func TestOrganizationNoticesSkipWhatMembersDidThemselves(t *testing.T) {
	tests := map[string]func(*Service) error{
		"changed their own role": func(service *Service) error {
			return service.MemberRoleChanged(context.Background(), ref(autheventsv1.MemberRoleChangedDetailType), autheventsv1.MemberRoleChanged{
				OrganizationName: "Acme", UserID: "user-1", Email: "asha@example.com", PreviousRole: "owner", Role: "admin", ChangedBy: "user-1",
			})
		},
		"left": func(service *Service) error {
			return service.MemberRemoved(context.Background(), ref(autheventsv1.MemberRemovedDetailType), autheventsv1.MemberRemoved{
				OrganizationName: "Acme", UserID: "user-1", Email: "asha@example.com", Reason: autheventsv1.MemberRemovedReasonLeft, RemovedBy: "user-1",
			})
		},
		"deleted the organization": func(service *Service) error {
			return service.MemberRemoved(context.Background(), ref(autheventsv1.MemberRemovedDetailType), autheventsv1.MemberRemoved{
				OrganizationName: "Acme", UserID: "user-1", Email: "asha@example.com", Reason: autheventsv1.MemberRemovedReasonOrganizationDeleted, RemovedBy: "user-1",
			})
		},
	}

	for name, send := range tests {
		t.Run(name, func(t *testing.T) {
			service, deliveries := newTestService(t)
			if err := send(service); err != nil {
				t.Fatalf("error = %v, want the event skipped", err)
			}
			if len(deliveries.requests) != 0 {
				t.Fatalf("deliveries = %d, want 0", len(deliveries.requests))
			}
		})
	}
}

func TestTeamCalledSendsWhenTheMemberWantsEmail(t *testing.T) {
	service, deliveries := newTestService(t)
	err := service.MemberPinged(context.Background(), ref(teamseventsv1.MemberPingedDetailType), teamseventsv1.MemberPinged{
		TeamID: "team_1", TeamName: "Engineering", OrganizationID: "org_1", OrganizationName: "Acme",
		UserID: "user-2", Email: "ravi@example.com", Role: "member", Tool: teamseventsv1.ToolCallTeam,
		NotifyEmail: true, CalledBy: "user-1", CalledAt: testNow,
	})
	if err != nil {
		t.Fatalf("MemberPinged() error = %v", err)
	}
	if len(deliveries.requests) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(deliveries.requests))
	}
	request := deliveries.requests[0]
	if request.Message.Template != "team-called" || request.Recipient != "ravi@example.com" {
		t.Fatalf("delivery request = %+v", request)
	}
	if !strings.Contains(request.Message.Text, "Engineering") || !strings.Contains(request.Message.Text, "Acme") {
		t.Fatalf("email lacks the team or organization:\n%s", request.Message.Text)
	}
}

func TestTeamCalledSkipsWhenTheMemberDoesNotWantEmail(t *testing.T) {
	tests := map[string]teamseventsv1.MemberPinged{
		"switch off": {
			TeamName: "Engineering", OrganizationName: "Acme", UserID: "user-2", Email: "ravi@example.com",
			Tool: teamseventsv1.ToolCallTeam, NotifyEmail: false, CalledBy: "user-1",
		},
		"they ran the tool": {
			TeamName: "Engineering", OrganizationName: "Acme", UserID: "user-1", Email: "asha@example.com",
			Tool: teamseventsv1.ToolCallTeam, NotifyEmail: true, CalledBy: "user-1",
		},
		"listing": {
			TeamName: "Engineering", OrganizationName: "Acme", UserID: "user-2", Email: "ravi@example.com",
			Tool: teamseventsv1.ToolListTeams, NotifyEmail: true, CalledBy: "user-1",
		},
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			service, deliveries := newTestService(t)
			if err := service.MemberPinged(context.Background(), ref(teamseventsv1.MemberPingedDetailType), data); err != nil {
				t.Fatalf("MemberPinged() error = %v", err)
			}
			if len(deliveries.requests) != 0 {
				t.Fatalf("deliveries = %d, want 0", len(deliveries.requests))
			}
		})
	}
}

func TestOrganizationNoticesRejectEventsWithoutRecipientOrOrganization(t *testing.T) {
	tests := map[string]func(*Service) error{
		"no email": func(service *Service) error {
			return service.MemberInvitationRevoked(context.Background(), ref(autheventsv1.MemberInvitationRevokedDetailType), autheventsv1.MemberInvitationRevoked{OrganizationName: "Acme"})
		},
		"no organization name": func(service *Service) error {
			return service.MemberRemoved(context.Background(), ref(autheventsv1.MemberRemovedDetailType), autheventsv1.MemberRemoved{
				UserID: "user-2", Email: "ravi@example.com", Reason: autheventsv1.MemberRemovedReasonRemoved, RemovedBy: "user-1",
			})
		},
		"no roles": func(service *Service) error {
			return service.MemberRoleChanged(context.Background(), ref(autheventsv1.MemberRoleChangedDetailType), autheventsv1.MemberRoleChanged{
				OrganizationName: "Acme", UserID: "user-2", Email: "ravi@example.com", ChangedBy: "user-1",
			})
		},
	}

	for name, send := range tests {
		t.Run(name, func(t *testing.T) {
			service, deliveries := newTestService(t)
			if err := send(service); err == nil {
				t.Fatal("error = nil, want the event rejected")
			}
			if len(deliveries.requests) != 0 {
				t.Fatalf("deliveries = %d, want 0", len(deliveries.requests))
			}
		})
	}
}

func TestBillingNoticesSendTheirEmail(t *testing.T) {
	graceEnds := time.Date(2026, time.September, 16, 12, 30, 0, 0, time.UTC)
	renewsAt := time.Date(2026, time.September, 20, 9, 0, 0, 0, time.UTC)
	billingURL := "https://app.example.com/settings/billing?organization_id=org_1"

	tests := []struct {
		name      string
		send      func(*Service) error
		template  string
		mentions  []string
		expiresAt time.Time
	}{
		{
			name: "payment failed with a grace period",
			send: func(service *Service) error {
				return service.SubscriptionPaymentFailed(context.Background(), billingRef(billingeventsv1.SubscriptionPaymentFailedDetailType), billingeventsv1.SubscriptionPaymentFailed{
					OrganizationID: "org_1", OrganizationName: "Acme", SubscriptionID: "sub_1", BillingEmail: "billing@example.com",
					GraceEndsAt: &graceEnds, BillingURL: billingURL, FailedAt: testNow,
				})
			},
			template: "payment-failed",
			mentions: []string{"by 16 September 2026, 12:30 UTC", billingURL},
		},
		{
			name: "payment failed without a grace period",
			send: func(service *Service) error {
				return service.SubscriptionPaymentFailed(context.Background(), billingRef(billingeventsv1.SubscriptionPaymentFailedDetailType), billingeventsv1.SubscriptionPaymentFailed{
					OrganizationID: "org_1", OrganizationName: "Acme", SubscriptionID: "sub_1", BillingEmail: "billing@example.com",
					BillingURL: billingURL, FailedAt: testNow,
				})
			},
			template: "payment-failed",
			mentions: []string{"Acme has lost its plan"},
		},
		{
			name: "subscription expired",
			send: func(service *Service) error {
				return service.SubscriptionEnded(context.Background(), billingRef(billingeventsv1.SubscriptionEndedDetailType), billingeventsv1.SubscriptionEnded{
					OrganizationID: "org_1", OrganizationName: "Acme", SubscriptionID: "sub_1", BillingEmail: "billing@example.com",
					Reason: billingeventsv1.SubscriptionEndedReasonExpired, BillingURL: billingURL, EndedAt: testNow,
				})
			},
			template: "subscription-ended",
			mentions: []string{"reached the end of its term"},
		},
		{
			name: "renewal upcoming",
			send: func(service *Service) error {
				return service.SubscriptionRenewalUpcoming(context.Background(), billingRef(billingeventsv1.SubscriptionRenewalUpcomingDetailType), billingeventsv1.SubscriptionRenewalUpcoming{
					OrganizationID: "org_1", OrganizationName: "Acme", SubscriptionID: "sub_1", BillingEmail: "billing@example.com",
					Plan: "team", RenewsAt: renewsAt, BillingURL: billingURL,
				})
			},
			template:  "renewal-upcoming",
			mentions:  []string{"team plan renews on 20 September 2026"},
			expiresAt: renewsAt,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, deliveries := newTestService(t)
			if err := test.send(service); err != nil {
				t.Fatalf("error = %v", err)
			}
			if len(deliveries.requests) != 1 {
				t.Fatalf("deliveries = %d, want 1", len(deliveries.requests))
			}
			request := deliveries.requests[0]
			if request.Message.Template != test.template || request.Recipient != "billing@example.com" || !request.ExpiresAt.Equal(test.expiresAt) {
				t.Fatalf("delivery request = %+v", request)
			}
			for _, mention := range test.mentions {
				if !strings.Contains(request.Message.Text, mention) {
					t.Fatalf("email lacks %q:\n%s", mention, request.Message.Text)
				}
			}
		})
	}
}

func TestBillingNoticesRejectUnusableEvents(t *testing.T) {
	valid := billingeventsv1.SubscriptionEnded{
		OrganizationID: "org_1", OrganizationName: "Acme", BillingEmail: "billing@example.com",
		Reason: billingeventsv1.SubscriptionEndedReasonCancelled, BillingURL: "https://app.example.com/billing",
	}
	tests := map[string]func(*billingeventsv1.SubscriptionEnded){
		"no billing email":     func(data *billingeventsv1.SubscriptionEnded) { data.BillingEmail = "" },
		"no organization name": func(data *billingeventsv1.SubscriptionEnded) { data.OrganizationName = "" },
		"insecure link":        func(data *billingeventsv1.SubscriptionEnded) { data.BillingURL = "http://app.example.com/billing" },
		"unknown reason":       func(data *billingeventsv1.SubscriptionEnded) { data.Reason = "paused" },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			service, deliveries := newTestService(t)
			data := valid
			change(&data)
			if err := service.SubscriptionEnded(context.Background(), billingRef(billingeventsv1.SubscriptionEndedDetailType), data); err == nil {
				t.Fatal("error = nil, want the event rejected")
			}
			if len(deliveries.requests) != 0 {
				t.Fatalf("deliveries = %d, want 0", len(deliveries.requests))
			}
		})
	}

	service, _ := newTestService(t)
	if err := service.SubscriptionRenewalUpcoming(context.Background(), billingRef(billingeventsv1.SubscriptionRenewalUpcomingDetailType), billingeventsv1.SubscriptionRenewalUpcoming{
		OrganizationName: "Acme", BillingEmail: "billing@example.com", BillingURL: "https://app.example.com/billing",
	}); err == nil {
		t.Fatal("renewal without renews_at accepted")
	}
}

func billingRef(detailType string) delivery.EventRef {
	return delivery.EventRef{Source: billingeventsv1.Source, DetailType: detailType, ID: "evt_billing_1"}
}

func TestDeliveryRequestsRejectUnusableEvents(t *testing.T) {
	valid := autheventsv1.PasswordResetDeliveryRequested{
		RequestID: "act_1", UserID: "user-1", Email: "asha@example.com",
		ResetURL: "https://app.example.com/reset?token=secret-token", ExpiresAt: testNow.Add(30 * time.Minute),
	}
	tests := map[string]func(*autheventsv1.PasswordResetDeliveryRequested){
		"no email":      func(data *autheventsv1.PasswordResetDeliveryRequested) { data.Email = "" },
		"no expiry":     func(data *autheventsv1.PasswordResetDeliveryRequested) { data.ExpiresAt = time.Time{} },
		"no link":       func(data *autheventsv1.PasswordResetDeliveryRequested) { data.ResetURL = "" },
		"relative link": func(data *autheventsv1.PasswordResetDeliveryRequested) { data.ResetURL = "/reset?token=secret-token" },
		"insecure link": func(data *autheventsv1.PasswordResetDeliveryRequested) {
			data.ResetURL = "http://app.example.com/reset?token=secret-token"
		},
		"javascript link": func(data *autheventsv1.PasswordResetDeliveryRequested) {
			data.ResetURL = "javascript:alert('secret-token')"
		},
		"unparseable link": func(data *autheventsv1.PasswordResetDeliveryRequested) {
			data.ResetURL = "https://app.example.com/%zz?token=secret-token"
		},
		"control characters": func(data *autheventsv1.PasswordResetDeliveryRequested) {
			data.ResetURL = "https://app.example.com/\x7f?token=secret-token"
		},
	}

	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			service, deliveries := newTestService(t)
			data := valid
			change(&data)

			err := service.PasswordResetRequested(context.Background(), ref(autheventsv1.PasswordResetDeliveryRequestedDetailType), data)
			if err == nil {
				t.Fatal("error = nil, want the event rejected")
			}
			if strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("error leaks the link's token: %v", err)
			}
			if len(deliveries.requests) != 0 {
				t.Fatalf("deliveries = %d, want 0", len(deliveries.requests))
			}
		})
	}
}

func TestLocalhostLinksAreAllowed(t *testing.T) {
	for _, link := range []string{"http://localhost:3000/auth/verify-email?token=t", "http://127.0.0.1:3000/x", "http://[::1]:3000/x"} {
		if err := validateActionURL(link); err != nil {
			t.Fatalf("validateActionURL(%q) error = %v", link, err)
		}
	}
}

func TestInvitationRequiresOrganizationAndRole(t *testing.T) {
	service, deliveries := newTestService(t)

	err := service.MemberInvited(context.Background(), ref(autheventsv1.MemberInvitedDetailType), autheventsv1.MemberInvited{
		Email: "ravi@example.com", AcceptURL: "https://app.example.com/accept?token=t", ExpiresAt: testNow.Add(time.Hour),
	})
	if err == nil || len(deliveries.requests) != 0 {
		t.Fatalf("error = %v with %d deliveries, want rejection", err, len(deliveries.requests))
	}
}

func TestQuietInvitationSendsNoEmail(t *testing.T) {
	service, deliveries := newTestService(t)
	err := service.MemberInvited(context.Background(), ref(autheventsv1.MemberInvitedDetailType), autheventsv1.MemberInvited{
		Email: "ravi@example.com", Quiet: true,
	})
	if err != nil || len(deliveries.requests) != 0 {
		t.Fatalf("quiet invitation error = %v, deliveries = %d", err, len(deliveries.requests))
	}
}

func TestExpiresIn(t *testing.T) {
	tests := map[time.Duration]string{
		-time.Minute:                    "1 minute",
		30 * time.Second:                "1 minute",
		29*time.Minute + 50*time.Second: "30 minutes",
		60 * time.Minute:                "60 minutes",
		90 * time.Minute:                "2 hours",
		23*time.Hour + 59*time.Minute:   "24 hours",
		47 * time.Hour:                  "47 hours",
		48 * time.Hour:                  "2 days",
		7*24*time.Hour - time.Hour:      "7 days",
	}
	for remaining, want := range tests {
		if got := expiresIn(remaining); got != want {
			t.Fatalf("expiresIn(%s) = %q, want %q", remaining, got, want)
		}
	}
}

func TestSESFeedbackIsRecorded(t *testing.T) {
	service, deliveries := newTestService(t)

	feedback := delivery.Feedback{DeliveryID: "delivery-1", Status: delivery.StatusDelivered}
	if err := service.SESFeedback(context.Background(), feedback); err != nil {
		t.Fatalf("SESFeedback() error = %v", err)
	}
	if len(deliveries.feedback) != 1 || deliveries.feedback[0].DeliveryID != "delivery-1" {
		t.Fatalf("recorded feedback = %+v", deliveries.feedback)
	}
}
