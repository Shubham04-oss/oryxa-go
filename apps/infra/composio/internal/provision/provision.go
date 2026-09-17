// Package provision points a Composio project at this deployment: one
// webhook subscription, for the three event types apps acts on, delivering to
// apps' own webhook path.
//
// The signing secret is shown once, when the subscription is created, and
// Composio does not show it again. So this prints it for an operator to put
// in COMPOSIO_WEBHOOK_SECRET, and refuses to replace a subscription unless
// asked: replacing one mints a new secret and breaks the running deployment
// until the new one is set.
package provision

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/apps"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// Events are the deliveries apps handles. Anything else Composio might send
// is acknowledged and dropped, so there is no reason to subscribe to it.
var Events = []string{
	composio.EventTriggerMessage,
	composio.EventConnectedAccountExpired,
	composio.EventTriggerDisabled,
}

type Config struct {
	APIKey  string
	BaseURL string
	// WebhookURL is where Composio delivers. Built from the public base URL
	// unless given outright, as a tunnel's URL is while developing.
	WebhookURL string
	// Replace deletes a subscription that already delivers to another URL and
	// makes a new one, which mints a new secret.
	Replace bool
}

func FromEnv() Config {
	webhookURL := strings.TrimSpace(os.Getenv("APPS_WEBHOOK_URL"))
	if webhookURL == "" {
		if base := strings.TrimRight(strings.TrimSpace(os.Getenv("APPS_PUBLIC_BASE_URL")), "/"); base != "" {
			webhookURL = base + apps.WebhookPath
		}
	}
	return Config{
		APIKey:     strings.TrimSpace(os.Getenv("COMPOSIO_API_KEY")),
		BaseURL:    strings.TrimSpace(os.Getenv("COMPOSIO_BASE_URL")),
		WebhookURL: webhookURL,
		Replace:    strings.EqualFold(strings.TrimSpace(os.Getenv("APPS_REPLACE_WEBHOOK")), "true"),
	}
}

type Output struct {
	SubscriptionID string   `json:"subscription_id"`
	WebhookURL     string   `json:"webhook_url"`
	Version        string   `json:"version"`
	Events         []string `json:"enabled_events"`
	// Secret is Composio's signing secret, shown only when this run created
	// the subscription. Put it in COMPOSIO_WEBHOOK_SECRET.
	Secret string `json:"secret,omitempty"`
	// Created says whether this run made the subscription.
	Created bool `json:"created"`
	// Note is what an operator has to do next, if anything.
	Note string `json:"note,omitempty"`
}

// Client is the part of Composio this provisioner uses.
type Client interface {
	WebhookSubscriptions(ctx context.Context) ([]composio.WebhookSubscription, error)
	CreateWebhookSubscription(ctx context.Context, webhookURL string, events []string) (composio.WebhookSubscription, error)
	DeleteWebhookSubscription(ctx context.Context, id string) error
}

// Run is repeatable: it creates the subscription if the project has none, and
// otherwise leaves the one that exists alone.
func Run(ctx context.Context, client Client, config Config) (Output, error) {
	if err := check(config); err != nil {
		return Output{}, err
	}
	existing, err := client.WebhookSubscriptions(ctx)
	if err != nil {
		return Output{}, fmt.Errorf("list webhook subscriptions: %w", err)
	}
	for _, subscription := range existing {
		if !sameURL(subscription.URL, config.WebhookURL) {
			continue
		}
		output := Output{
			SubscriptionID: subscription.ID, WebhookURL: subscription.URL,
			Version: subscription.Version, Events: subscription.EnabledEvents,
			Note: "this subscription already delivers here; its secret is shown only once, at creation, so keep the COMPOSIO_WEBHOOK_SECRET you have",
		}
		if subscription.Version != composio.WebhookVersion {
			output.Note = fmt.Sprintf("this subscription delivers payload version %s, and apps reads %s: replace it with APPS_REPLACE_WEBHOOK=true, which mints a new secret",
				subscription.Version, composio.WebhookVersion)
		} else if missing := missingEvents(subscription.EnabledEvents); len(missing) > 0 {
			output.Note = "this subscription does not carry " + strings.Join(missing, ", ") +
				": replace it with APPS_REPLACE_WEBHOOK=true, which mints a new secret"
		}
		return output, nil
	}

	// A project may have one subscription only, so an existing one elsewhere
	// is in the way; deleting it is the operator's call, not this program's.
	if len(existing) > 0 && !config.Replace {
		return Output{}, fmt.Errorf("the project already delivers to %s: run again with APPS_REPLACE_WEBHOOK=true to point it here, which mints a new secret you must put in COMPOSIO_WEBHOOK_SECRET",
			strings.Join(urls(existing), ", "))
	}
	for _, subscription := range existing {
		if err := client.DeleteWebhookSubscription(ctx, subscription.ID); err != nil && !errors.Is(err, composio.ErrNotFound) {
			return Output{}, fmt.Errorf("delete webhook subscription %s: %w", subscription.ID, err)
		}
	}
	created, err := client.CreateWebhookSubscription(ctx, config.WebhookURL, Events)
	if err != nil {
		return Output{}, fmt.Errorf("create webhook subscription: %w", err)
	}
	output := Output{
		SubscriptionID: created.ID, WebhookURL: config.WebhookURL, Version: created.Version,
		Events: created.EnabledEvents, Secret: created.Secret, Created: true,
		Note: "put this secret in COMPOSIO_WEBHOOK_SECRET; Composio does not show it again",
	}
	if output.Version == "" {
		output.Version = composio.WebhookVersion
	}
	if created.Secret == "" {
		output.Note = "Composio did not return a signing secret; read it from the project's webhook settings and put it in COMPOSIO_WEBHOOK_SECRET"
	}
	return output, nil
}

func check(config Config) error {
	if config.APIKey == "" {
		return errors.New("COMPOSIO_API_KEY is required")
	}
	if config.WebhookURL == "" {
		return errors.New("APPS_WEBHOOK_URL or APPS_PUBLIC_BASE_URL is required")
	}
	parsed, err := url.Parse(config.WebhookURL)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("the webhook URL %q is not absolute", config.WebhookURL)
	}
	if parsed.Scheme != "https" {
		// Composio delivers a signed payload over the public internet; plain
		// HTTP would put a member's app data on the wire in the clear.
		return fmt.Errorf("the webhook URL %q must be https", config.WebhookURL)
	}
	return nil
}

// sameURL compares delivery URLs the way Composio treats them: the host
// case-insensitively, the path exactly.
func sameURL(left, right string) bool {
	first, err := url.Parse(left)
	if err != nil {
		return false
	}
	second, err := url.Parse(right)
	if err != nil {
		return false
	}
	return strings.EqualFold(first.Scheme, second.Scheme) && strings.EqualFold(first.Host, second.Host) &&
		strings.TrimRight(first.Path, "/") == strings.TrimRight(second.Path, "/")
}

func missingEvents(enabled []string) []string {
	var missing []string
	for _, event := range Events {
		found := false
		for _, candidate := range enabled {
			if candidate == event || candidate == "*" {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, event)
		}
	}
	return missing
}

func urls(subscriptions []composio.WebhookSubscription) []string {
	var listed []string
	for _, subscription := range subscriptions {
		listed = append(listed, subscription.URL)
	}
	return listed
}
