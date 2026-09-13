package application

import (
	"context"
	"strings"
	"testing"
	"time"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
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
