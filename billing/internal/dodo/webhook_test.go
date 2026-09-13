package dodo

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	standardwebhooks "github.com/standard-webhooks/standard-webhooks/libraries/go"

	"github.com/Shubham04-oss/oryxa-go/billing/internal/domain"
)

// testWebhookKey is a throwaway Standard Webhooks secret: "whsec_" and 24
// base64-encoded bytes.
const testWebhookKey = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"

const subscriptionPastDue = `{
	"business_id": "bus_1",
	"type": "subscription.past_due",
	"timestamp": "2026-09-13T12:00:00Z",
	"data": {
		"payload_type": "Subscription",
		"subscription_id": "sub_1",
		"status": "past_due",
		"product_id": "pdt_team",
		"quantity": 4,
		"cancel_at_next_billing_date": false,
		"next_billing_date": "2026-10-13T12:00:00Z",
		"past_due_ends_at": "2026-09-16T12:00:00Z",
		"payment_frequency_interval": "Month",
		"payment_frequency_count": 1,
		"metadata": {"organization_id": "org_1"},
		"customer": {"customer_id": "cus_1", "email": "billing@example.com", "name": "Asha", "metadata": {}},
		"addons": [],
		"recurring_pre_tax_amount": 4000,
		"currency": "USD"
	}
}`

func signedHeaders(t *testing.T, id string, sentAt time.Time, payload string) http.Header {
	t.Helper()
	webhook, err := standardwebhooks.NewWebhook(testWebhookKey)
	if err != nil {
		t.Fatalf("NewWebhook() error = %v", err)
	}
	signature, err := webhook.Sign(id, sentAt, []byte(payload))
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	headers := http.Header{}
	headers.Set(standardwebhooks.HeaderWebhookID, id)
	headers.Set(standardwebhooks.HeaderWebhookTimestamp, strconv.FormatInt(sentAt.Unix(), 10))
	headers.Set(standardwebhooks.HeaderWebhookSignature, signature)
	return headers
}

func webhookClient(t *testing.T) *Client {
	t.Helper()
	client, err := New(Config{APIKey: "test-key", WebhookKey: testWebhookKey})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

func TestParseWebhookDecodesSubscription(t *testing.T) {
	client := webhookClient(t)

	webhook, err := client.ParseWebhook([]byte(subscriptionPastDue), signedHeaders(t, "msg_1", time.Now(), subscriptionPastDue))
	if err != nil {
		t.Fatalf("ParseWebhook() error = %v", err)
	}
	if webhook.ID != "msg_1" || webhook.Type != "subscription.past_due" || !webhook.Timestamp.Equal(time.Date(2026, time.September, 13, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("webhook = %+v", webhook)
	}
	subscription := webhook.Subscription
	graceEnds := time.Date(2026, time.September, 16, 12, 0, 0, 0, time.UTC)
	if subscription == nil || subscription.ID != "sub_1" || subscription.OrganizationID != "org_1" ||
		subscription.CustomerID != "cus_1" || subscription.CustomerEmail != "billing@example.com" ||
		subscription.ProductID != "pdt_team" || subscription.Quantity != 4 || subscription.Status != domain.SubscriptionPastDue ||
		subscription.PastDueEndsAt == nil || !subscription.PastDueEndsAt.Equal(graceEnds) ||
		subscription.BillingInterval != "Month" || subscription.BillingIntervalCount != 1 ||
		!subscription.EventAt.Equal(webhook.Timestamp) {
		t.Fatalf("subscription = %+v", subscription)
	}
}

func TestParseWebhookFallsBackToCustomerMetadata(t *testing.T) {
	client := webhookClient(t)
	payload := `{"type":"subscription.active","timestamp":"2026-09-13T12:00:00Z","data":{"subscription_id":"sub_1","status":"active","product_id":"pdt_team","quantity":1,"next_billing_date":"2026-10-13T12:00:00Z","past_due_ends_at":null,"metadata":{},"customer":{"customer_id":"cus_1","email":"billing@example.com","metadata":{"organization_id":"org_9"}}}}`

	webhook, err := client.ParseWebhook([]byte(payload), signedHeaders(t, "msg_2", time.Now(), payload))
	if err != nil {
		t.Fatalf("ParseWebhook() error = %v", err)
	}
	if webhook.Subscription.OrganizationID != "org_9" || webhook.Subscription.PastDueEndsAt != nil {
		t.Fatalf("subscription = %+v", webhook.Subscription)
	}
}

func TestParseWebhookLeavesOtherEventsUndecoded(t *testing.T) {
	client := webhookClient(t)
	payload := `{"type":"payment.succeeded","timestamp":"2026-09-13T12:00:00Z","data":{"payment_id":"pay_1"}}`

	webhook, err := client.ParseWebhook([]byte(payload), signedHeaders(t, "msg_3", time.Now(), payload))
	if err != nil {
		t.Fatalf("ParseWebhook() error = %v", err)
	}
	if webhook.Type != "payment.succeeded" || webhook.Subscription != nil {
		t.Fatalf("webhook = %+v", webhook)
	}
}

func TestParseWebhookRejectsUnverifiedDeliveries(t *testing.T) {
	client := webhookClient(t)
	valid := signedHeaders(t, "msg_1", time.Now(), subscriptionPastDue)

	tests := map[string]struct {
		payload string
		headers http.Header
	}{
		"altered payload": {payload: subscriptionPastDue + " ", headers: valid},
		"no signature":    {payload: subscriptionPastDue, headers: http.Header{}},
		"replayed later":  {payload: subscriptionPastDue, headers: signedHeaders(t, "msg_1", time.Now().Add(-time.Hour), subscriptionPastDue)},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := client.ParseWebhook([]byte(test.payload), test.headers); !errors.Is(err, ErrInvalidWebhook) {
				t.Fatalf("ParseWebhook() error = %v, want ErrInvalidWebhook", err)
			}
		})
	}
}

func TestParseWebhookRejectsIncompleteSubscriptions(t *testing.T) {
	client := webhookClient(t)
	payload := `{"type":"subscription.active","timestamp":"2026-09-13T12:00:00Z","data":{"subscription_id":"sub_1","status":"active"}}`

	if _, err := client.ParseWebhook([]byte(payload), signedHeaders(t, "msg_4", time.Now(), payload)); err == nil {
		t.Fatal("ParseWebhook() error = nil for a subscription without a customer")
	}
}

func TestParseWebhookWithoutKey(t *testing.T) {
	client, err := New(Config{APIKey: "test-key"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := client.ParseWebhook([]byte(subscriptionPastDue), http.Header{}); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("ParseWebhook() error = %v, want ErrNotConfigured", err)
	}
	var missing *Client
	if _, err := missing.ParseWebhook([]byte(subscriptionPastDue), http.Header{}); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("nil client ParseWebhook() error = %v, want ErrNotConfigured", err)
	}
}
