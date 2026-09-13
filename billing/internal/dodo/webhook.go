package dodo

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	standardwebhooks "github.com/standard-webhooks/standard-webhooks/libraries/go"

	"github.com/Shubham04-oss/oryxa-go/billing/internal/domain"
)

// ErrInvalidWebhook means the webhook's signature doesn't verify: it is not
// from Dodo, was altered, or is older than the replay window.
var ErrInvalidWebhook = errors.New("webhook signature is invalid")

// Webhook is a verified delivery from Dodo.
type Webhook struct {
	// ID is the delivery ID, the same for every retry of one event.
	ID        string
	Type      string
	Timestamp time.Time
	// Subscription is set for subscription.* events.
	Subscription *domain.Subscription
}

type webhookPayload struct {
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

type subscriptionPayload struct {
	SubscriptionID           string         `json:"subscription_id"`
	Status                   string         `json:"status"`
	ProductID                string         `json:"product_id"`
	Quantity                 int            `json:"quantity"`
	CancelAtNextBillingDate  bool           `json:"cancel_at_next_billing_date"`
	NextBillingDate          time.Time      `json:"next_billing_date"`
	PastDueEndsAt            *time.Time     `json:"past_due_ends_at"`
	PaymentFrequencyInterval string         `json:"payment_frequency_interval"`
	PaymentFrequencyCount    int            `json:"payment_frequency_count"`
	Metadata                 map[string]any `json:"metadata"`
	Customer                 struct {
		CustomerID string         `json:"customer_id"`
		Email      string         `json:"email"`
		Metadata   map[string]any `json:"metadata"`
	} `json:"customer"`
}

// ParseWebhook verifies a delivery's signature and decodes it. It reads only
// what billing uses, so event types and fields Dodo adds later don't break it.
func (client *Client) ParseWebhook(payload []byte, headers http.Header) (Webhook, error) {
	if client == nil || client.webhook == nil {
		return Webhook{}, domain.ErrNotConfigured
	}
	if err := client.webhook.Verify(payload, headers); err != nil {
		return Webhook{}, fmt.Errorf("%w: %v", ErrInvalidWebhook, err)
	}

	var decoded webhookPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return Webhook{}, fmt.Errorf("decode webhook: %w", err)
	}
	webhook := Webhook{
		ID:        headers.Get(standardwebhooks.HeaderWebhookID),
		Type:      decoded.Type,
		Timestamp: decoded.Timestamp,
	}
	if webhook.Type == "" || webhook.Timestamp.IsZero() {
		return Webhook{}, errors.New("webhook has no type or timestamp")
	}
	if !strings.HasPrefix(webhook.Type, "subscription.") {
		return webhook, nil
	}

	var data subscriptionPayload
	if err := json.Unmarshal(decoded.Data, &data); err != nil {
		return Webhook{}, fmt.Errorf("decode %s webhook: %w", webhook.Type, err)
	}
	if data.SubscriptionID == "" || data.Status == "" || data.Customer.CustomerID == "" {
		return Webhook{}, fmt.Errorf("%s webhook has no subscription, status or customer", webhook.Type)
	}
	organizationID := stringMetadata(data.Metadata, MetadataOrganizationID)
	if organizationID == "" {
		organizationID = stringMetadata(data.Customer.Metadata, MetadataOrganizationID)
	}
	webhook.Subscription = &domain.Subscription{
		ID:                      data.SubscriptionID,
		OrganizationID:          organizationID,
		CustomerID:              data.Customer.CustomerID,
		CustomerEmail:           data.Customer.Email,
		ProductID:               data.ProductID,
		Quantity:                data.Quantity,
		Status:                  domain.SubscriptionStatus(data.Status),
		CancelAtNextBillingDate: data.CancelAtNextBillingDate,
		NextBillingDate:         data.NextBillingDate,
		PastDueEndsAt:           data.PastDueEndsAt,
		BillingInterval:         data.PaymentFrequencyInterval,
		BillingIntervalCount:    data.PaymentFrequencyCount,
		EventAt:                 webhook.Timestamp,
	}
	return webhook, nil
}

func stringMetadata(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}
