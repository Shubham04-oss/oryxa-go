// Package dodo is billing's adapter to Dodo Payments: customers, checkout
// sessions, the customer portal, cancellations, and the webhooks Dodo sends.
package dodo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	dodopayments "github.com/dodopayments/dodopayments-go"
	"github.com/dodopayments/dodopayments-go/option"
	"github.com/dodopayments/dodopayments-go/shared"
	standardwebhooks "github.com/standard-webhooks/standard-webhooks/libraries/go"

	"github.com/Shubham04-oss/oryxa-go/billing/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

// MetadataOrganizationID is the metadata key naming the organization a Dodo
// customer or checkout belongs to.
const MetadataOrganizationID = "organization_id"

type Config struct {
	APIKey string
	// WebhookKey is the endpoint's signing secret (whsec_...). Without it no
	// webhook is accepted.
	WebhookKey string
	LiveMode   bool
	// BaseURL overrides the test or live API host, for fakes.
	BaseURL string
}

type Client struct {
	api     *dodopayments.Client
	webhook *standardwebhooks.Webhook
}

func New(config Config) (*Client, error) {
	if config.APIKey == "" {
		return nil, errors.New("Dodo Payments API key is required")
	}
	options := []option.RequestOption{
		option.WithBearerToken(config.APIKey),
		option.WithMaxRetries(2),
		option.WithRequestTimeout(15 * time.Second),
		// The SDK otherwise calls through http.DefaultClient, which carries no
		// span and propagates nothing: this is Dodo, a third party, so a
		// client span and traceparent matter here as much as for a call to
		// another Oryxa service.
		option.WithHTTPClient(&http.Client{Transport: observability.Transport(nil)}),
	}
	if config.LiveMode {
		options = append(options, option.WithEnvironmentLiveMode())
	} else {
		options = append(options, option.WithEnvironmentTestMode())
	}
	if config.BaseURL != "" {
		options = append(options, option.WithBaseURL(config.BaseURL))
	}

	client := &Client{api: dodopayments.NewClient(options...)}
	if config.WebhookKey != "" {
		webhook, err := standardwebhooks.NewWebhook(config.WebhookKey)
		if err != nil {
			return nil, fmt.Errorf("Dodo Payments webhook key: %w", err)
		}
		client.webhook = webhook
	}
	return client, nil
}

type CustomerRequest struct {
	OrganizationID string
	Email          string
	Name           string
}

// CreateCustomer creates the Dodo customer an organization's subscriptions
// are billed to, and returns its ID.
func (client *Client) CreateCustomer(ctx context.Context, request CustomerRequest) (string, error) {
	customer, err := client.api.Customers.New(ctx, dodopayments.CustomerNewParams{
		Email:    dodopayments.F(request.Email),
		Name:     dodopayments.F(request.Name),
		Metadata: dodopayments.F(organizationMetadata(request.OrganizationID)),
	})
	if err != nil {
		return "", fmt.Errorf("create Dodo customer: %w", err)
	}
	if customer.CustomerID == "" {
		return "", errors.New("Dodo created a customer without an ID")
	}
	return customer.CustomerID, nil
}

type CheckoutRequest struct {
	OrganizationID string
	CustomerID     string
	ProductID      string
	Quantity       int
	ReturnURL      string
}

// CreateCheckout starts a hosted checkout for a subscription and returns the
// URL to send the customer to.
func (client *Client) CreateCheckout(ctx context.Context, request CheckoutRequest) (string, error) {
	session, err := client.api.CheckoutSessions.New(ctx, dodopayments.CheckoutSessionNewParams{
		CheckoutSessionRequest: dodopayments.CheckoutSessionRequestParam{
			ProductCart: dodopayments.F([]dodopayments.ProductItemReqParam{{
				ProductID: dodopayments.F(request.ProductID),
				Quantity:  dodopayments.F(int64(request.Quantity)),
			}}),
			Customer: dodopayments.F[dodopayments.CustomerRequestUnionParam](dodopayments.AttachExistingCustomerParam{
				CustomerID: dodopayments.F(request.CustomerID),
			}),
			Metadata:  dodopayments.F(organizationMetadata(request.OrganizationID)),
			ReturnURL: dodopayments.F(request.ReturnURL),
		},
	})
	if err != nil {
		return "", fmt.Errorf("create Dodo checkout session: %w", err)
	}
	if session.CheckoutURL == "" {
		return "", errors.New("Dodo returned a checkout session without a URL")
	}
	return session.CheckoutURL, nil
}

// CreatePortalSession returns a link to the customer portal, where the
// customer updates payment methods, changes or cancels plans, and downloads
// invoices.
func (client *Client) CreatePortalSession(ctx context.Context, customerID, returnURL string) (string, error) {
	session, err := client.api.Customers.CustomerPortal.New(ctx, customerID, dodopayments.CustomerCustomerPortalNewParams{
		ReturnURL: dodopayments.F(returnURL),
	})
	if err != nil {
		return "", fmt.Errorf("create Dodo customer portal session: %w", err)
	}
	if session.Link == "" {
		return "", errors.New("Dodo returned a customer portal session without a link")
	}
	return session.Link, nil
}

// CancelSubscription cancels immediately. A subscription that has already
// ended counts as cancelled, so retrying is safe.
func (client *Client) CancelSubscription(ctx context.Context, subscriptionID string) error {
	_, err := client.api.Subscriptions.Update(ctx, subscriptionID, dodopayments.SubscriptionUpdateParams{
		Status: dodopayments.F(dodopayments.SubscriptionStatusCancelled),
	})
	if err == nil {
		return nil
	}
	var apiErr *dodopayments.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode >= http.StatusInternalServerError {
		return fmt.Errorf("cancel Dodo subscription %s: %w", subscriptionID, err)
	}
	subscription, getErr := client.api.Subscriptions.Get(ctx, subscriptionID)
	if getErr != nil {
		return fmt.Errorf("cancel Dodo subscription %s: %w (and reading it back: %v)", subscriptionID, err, getErr)
	}
	if domain.SubscriptionStatus(subscription.Status).Ended() {
		return nil
	}
	return fmt.Errorf("cancel Dodo subscription %s: %w", subscriptionID, err)
}

func organizationMetadata(organizationID string) dodopayments.MetadataParam {
	return dodopayments.MetadataParam{MetadataOrganizationID: shared.UnionString(organizationID)}
}
