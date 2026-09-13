package events

import (
	"slices"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/email/internal/delivery"
)

// The SES records below follow the examples in the SES developer guide
// ("Examples of event data that Amazon SES publishes"), with the delivery ID
// tag this service adds to every message.
const sesMail = `"mail":{"timestamp":"2026-09-13T12:00:00.000Z","source":"no-reply@example.com","messageId":"ses-message-1","destination":["asha@example.com"],"headersTruncated":false,"tags":{"ses:configuration-set":["oryxa-email"],"delivery_id":["delivery-1"]}}`

func TestFeedbackFromSES(t *testing.T) {
	tests := []struct {
		name       string
		detailType string
		record     string
		want       delivery.Feedback
	}{
		{
			name:       "permanent bounce",
			detailType: "Email Bounced",
			record:     `{"eventType":"Bounce","bounce":{"bounceType":"Permanent","bounceSubType":"General","bouncedRecipients":[{"emailAddress":"asha@example.com","action":"failed","status":"5.1.1","diagnosticCode":"smtp; 550 5.1.1 user unknown"}],"timestamp":"2026-09-13T12:00:05.000Z","feedbackId":"feedback-1"},` + sesMail + `}`,
			want: delivery.Feedback{
				Status: delivery.StatusBounced, Recipients: []string{"asha@example.com"},
				Detail: "Permanent/General: smtp; 550 5.1.1 user unknown", Suppress: true, Reason: delivery.ReasonBounce,
			},
		},
		{
			name:       "transient bounce",
			detailType: "Email Bounced",
			record:     `{"eventType":"Bounce","bounce":{"bounceType":"Transient","bounceSubType":"MailboxFull","bouncedRecipients":[{"emailAddress":"asha@example.com"}],"timestamp":"2026-09-13T12:00:05.000Z"},` + sesMail + `}`,
			want: delivery.Feedback{
				Status: delivery.StatusBounced, Recipients: []string{"asha@example.com"},
				Detail: "Transient/MailboxFull", Reason: delivery.ReasonBounce,
			},
		},
		{
			name:       "complaint",
			detailType: "Email Complaint Received",
			record:     `{"eventType":"Complaint","complaint":{"complainedRecipients":[{"emailAddress":"asha@example.com"}],"timestamp":"2026-09-13T12:00:05.000Z","feedbackId":"feedback-1","complaintFeedbackType":"abuse"},` + sesMail + `}`,
			want: delivery.Feedback{
				Status: delivery.StatusComplained, Recipients: []string{"asha@example.com"},
				Detail: "abuse", Suppress: true, Reason: delivery.ReasonComplaint,
			},
		},
		{
			name:       "not-spam complaint",
			detailType: "Email Complaint Received",
			record:     `{"eventType":"Complaint","complaint":{"complainedRecipients":[{"emailAddress":"asha@example.com"}],"timestamp":"2026-09-13T12:00:05.000Z","complaintFeedbackType":"not-spam"},` + sesMail + `}`,
			want: delivery.Feedback{
				Status: delivery.StatusComplained, Recipients: []string{"asha@example.com"},
				Detail: "not-spam", Reason: delivery.ReasonComplaint,
			},
		},
		{
			name:       "delivery",
			detailType: "Email Delivered",
			record:     `{"eventType":"Delivery",` + sesMail + `,"delivery":{"timestamp":"2026-09-13T12:00:05.000Z","processingTimeMillis":546,"recipients":["asha@example.com"],"smtpResponse":"250 2.6.0 Message received","reportingMTA":"mta.example.com"}}`,
			want: delivery.Feedback{
				Status: delivery.StatusDelivered, Recipients: []string{"asha@example.com"}, Detail: "250 2.6.0 Message received",
			},
		},
		{
			name:       "delivery delay",
			detailType: "Email Delivery Delayed",
			record:     `{"eventType":"DeliveryDelay",` + sesMail + `,"deliveryDelay":{"timestamp":"2026-09-13T12:00:05.000Z","delayType":"TransientCommunicationFailure","expirationTime":"2026-09-13T13:00:00.000Z","delayedRecipients":[{"emailAddress":"asha@example.com","status":"4.4.1","diagnosticCode":"smtp; 421 4.4.1 Unable to connect to remote host"}]}}`,
			want: delivery.Feedback{
				Status: delivery.StatusDelayed, Recipients: []string{"asha@example.com"}, Detail: "TransientCommunicationFailure",
			},
		},
		{
			name:       "reject",
			detailType: "Email Rejected",
			record:     `{"eventType":"Reject",` + sesMail + `,"reject":{"reason":"Bad content"}}`,
			want: delivery.Feedback{
				Status: delivery.StatusRejected, Recipients: []string{"asha@example.com"}, Detail: "Bad content",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := feedbackFromSES(test.detailType, []byte(test.record))
			if err != nil {
				t.Fatalf("feedbackFromSES() error = %v", err)
			}
			if got.DeliveryID != "delivery-1" || got.SESMessageID != "ses-message-1" {
				t.Fatalf("delivery id %q, SES message id %q; want delivery-1 and ses-message-1", got.DeliveryID, got.SESMessageID)
			}
			if got.Status != test.want.Status || got.Detail != test.want.Detail || got.Suppress != test.want.Suppress ||
				got.Reason != test.want.Reason || !slices.Equal(got.Recipients, test.want.Recipients) {
				t.Fatalf("feedbackFromSES() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestFeedbackFromSESRejectsIncompleteRecords(t *testing.T) {
	if _, err := feedbackFromSES("Email Bounced", []byte(`{"eventType":"Bounce",`+sesMail+`}`)); err == nil || !strings.Contains(err.Error(), "bounce") {
		t.Fatalf("feedbackFromSES() error = %v, want a missing bounce object error", err)
	}
	if _, err := feedbackFromSES("Email Opened", []byte(`{}`)); err == nil {
		t.Fatal("feedbackFromSES() error = nil for an event the service does not consume")
	}
}
