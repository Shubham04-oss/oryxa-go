package provision

import (
	"encoding/json"
	"reflect"
	"testing"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	billingeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/billing/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"
	"github.com/Shubham04-oss/oryxa-go/email/internal/events"
)

func TestEventPatternMatchesSubscribedAuthEvents(t *testing.T) {
	pattern, err := EventPattern(events.Subscriptions())
	if err != nil {
		t.Fatalf("EventPattern() error = %v", err)
	}

	// Auth's registration, delivery requests and membership changes, and
	// billing's notices, each under its own source, in sorted order.
	assertJSONEqual(t, pattern, `{"$or":[
		{"source":["`+autheventsv1.Source+`"],"detail-type":[
			"`+autheventsv1.MemberInvitationRevokedDetailType+`",
			"`+autheventsv1.MemberInvitedDetailType+`",
			"`+autheventsv1.MemberRemovedDetailType+`",
			"`+autheventsv1.MemberRoleChangedDetailType+`",
			"`+autheventsv1.EmailVerificationDeliveryRequestedDetailType+`",
			"`+autheventsv1.PasswordResetDeliveryRequestedDetailType+`",
			"`+autheventsv1.UserRegisteredDetailType+`"
		]},
		{"source":["`+billingeventsv1.Source+`"],"detail-type":[
			"`+billingeventsv1.SubscriptionEndedDetailType+`",
			"`+billingeventsv1.SubscriptionPaymentFailedDetailType+`",
			"`+billingeventsv1.SubscriptionRenewalUpcomingDetailType+`"
		]},
		{"source":["`+teamseventsv1.Source+`"],"detail-type":[
			"`+teamseventsv1.MemberPingedDetailType+`"
		]}
	]}`)
}

func TestEventPatternKeepsSourcesApart(t *testing.T) {
	pattern, err := EventPattern([]events.Subscription{
		{Source: "oryxa.billing", DetailType: "billing.invoice.issued.v1"},
		{Source: "oryxa.auth", DetailType: "auth.user.registered.v1"},
		{Source: "oryxa.auth", DetailType: "auth.user.deleted.v1"},
	})
	if err != nil {
		t.Fatalf("EventPattern() error = %v", err)
	}

	assertJSONEqual(t, pattern, `{"$or":[
		{"source":["oryxa.auth"],"detail-type":["auth.user.deleted.v1","auth.user.registered.v1"]},
		{"source":["oryxa.billing"],"detail-type":["billing.invoice.issued.v1"]}
	]}`)
}

func TestEventPatternRequiresSubscriptions(t *testing.T) {
	if _, err := EventPattern(nil); err == nil {
		t.Fatal("EventPattern() error = nil, want error for no subscriptions")
	}
}

func TestFeedbackPatternMatchesOwnConfigurationSet(t *testing.T) {
	pattern, err := FeedbackPattern("oryxa-email")
	if err != nil {
		t.Fatalf("FeedbackPattern() error = %v", err)
	}

	assertJSONEqual(t, pattern, `{
		"source": ["aws.ses"],
		"detail-type": ["Email Bounced", "Email Complaint Received", "Email Delivered", "Email Delivery Delayed", "Email Rejected"],
		"detail": {"mail": {"tags": {"ses:configuration-set": ["oryxa-email"]}}}
	}`)
}

func TestSESPublishesEveryConsumedFeedbackEvent(t *testing.T) {
	eventTypes, err := feedbackEventTypes()
	if err != nil {
		t.Fatalf("feedbackEventTypes() error = %v", err)
	}
	if len(eventTypes) != len(sesEventTypes) {
		t.Fatalf("SES publishes %d event types, but %d are mapped; keep sesEventTypes in step with the consumer", len(eventTypes), len(sesEventTypes))
	}
}

func TestQueuePolicyAllowsOnlyTheRules(t *testing.T) {
	policy, err := queuePolicy("arn:aws:sqs:ap-south-1:123456789012:email-events", []string{
		"arn:aws:events:ap-south-1:123456789012:rule/email-subscriptions",
		"arn:aws:events:ap-south-1:123456789012:rule/email-ses-feedback",
	})
	if err != nil {
		t.Fatalf("queuePolicy() error = %v", err)
	}

	assertJSONEqual(t, policy, `{
		"Version": "2012-10-17",
		"Statement": [{
			"Sid": "AllowEmailServiceRules",
			"Effect": "Allow",
			"Principal": {"Service": "events.amazonaws.com"},
			"Action": "sqs:SendMessage",
			"Resource": "arn:aws:sqs:ap-south-1:123456789012:email-events",
			"Condition": {"ArnEquals": {"aws:SourceArn": [
				"arn:aws:events:ap-south-1:123456789012:rule/email-subscriptions",
				"arn:aws:events:ap-south-1:123456789012:rule/email-ses-feedback"
			]}}
		}]
	}`)
}

func TestSendingDomain(t *testing.T) {
	tests := []struct {
		name    string
		address string
		want    string
		wantErr bool
	}{
		{name: "address", address: "no-reply@Mail.Example.com", want: "mail.example.com"},
		{name: "missing", address: "", wantErr: true},
		{name: "not an address", address: "no-reply", wantErr: true},
		{name: "display name", address: "Oryxa <no-reply@example.com>", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := sendingDomain(test.address)
			if (err != nil) != test.wantErr {
				t.Fatalf("sendingDomain() error = %v, wantErr %v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("sendingDomain() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFromEnvSESEventPublishing(t *testing.T) {
	t.Setenv("EMAIL_FROM_ADDRESS", "no-reply@example.com")

	for value, want := range map[string]bool{"": true, "true": true, "false": false} {
		t.Setenv("SES_EVENT_PUBLISHING", value)
		config, err := FromEnv()
		if err != nil {
			t.Fatalf("FromEnv() with SES_EVENT_PUBLISHING=%q error = %v", value, err)
		}
		if config.SESEventPublishing != want {
			t.Fatalf("SES_EVENT_PUBLISHING=%q gave %v, want %v", value, config.SESEventPublishing, want)
		}
	}

	t.Setenv("SES_EVENT_PUBLISHING", "sometimes")
	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() error = nil for SES_EVENT_PUBLISHING=sometimes")
	}
}

func assertJSONEqual(t *testing.T, got, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal([]byte(got), &gotValue); err != nil {
		t.Fatalf("decode %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decode %s: %v", want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
}
