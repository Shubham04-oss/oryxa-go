package composio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// TriggerType is an event a toolkit can deliver, such as GITHUB_COMMIT_EVENT.
type TriggerType struct {
	Slug         string
	Name         string
	Description  string
	Instructions string
	Toolkit      string
	// Kind is "webhook" when the provider pushes the event, "poll" when
	// Composio polls for it.
	Kind    string
	Version string
	// Config is the JSON Schema of what a subscription to it takes; Payload,
	// of what an event carries.
	Config  json.RawMessage
	Payload json.RawMessage
	// RequiresWebhookEndpointSetup says a webhook URL Composio issues has to
	// be registered with the provider by hand, as Slack's event subscriptions
	// need.
	RequiresWebhookEndpointSetup bool
}

// TriggerTypeQuery narrows a trigger type listing.
type TriggerTypeQuery struct {
	Toolkits []string
	Cursor   string
	Limit    int
}

// TriggerTypes lists the events toolkits can deliver.
func (client *Client) TriggerTypes(ctx context.Context, query TriggerTypeQuery) (Page[TriggerType], error) {
	values := url.Values{}
	addAll(values, "toolkit_slugs", query.Toolkits)
	var answer struct {
		Items []struct {
			Slug         string `json:"slug"`
			Name         string `json:"name"`
			Description  string `json:"description"`
			Instructions string `json:"instructions"`
			Type         string `json:"type"`
			Toolkit      struct {
				Slug string `json:"slug"`
			} `json:"toolkit"`
			Config                       json.RawMessage `json:"config"`
			Payload                      json.RawMessage `json:"payload"`
			Version                      string          `json:"version"`
			RequiresWebhookEndpointSetup bool            `json:"requires_webhook_endpoint_setup"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := client.call(ctx, http.MethodGet, "/triggers_types", pageQuery(values, query.Cursor, query.Limit), nil, &answer); err != nil {
		return Page[TriggerType]{}, err
	}
	page := Page[TriggerType]{NextCursor: cursorOf(answer.NextCursor)}
	for _, item := range answer.Items {
		page.Items = append(page.Items, TriggerType{
			Slug: item.Slug, Name: item.Name, Description: item.Description, Instructions: item.Instructions,
			Toolkit: item.Toolkit.Slug, Kind: item.Type, Version: item.Version, Config: item.Config,
			Payload: item.Payload, RequiresWebhookEndpointSetup: item.RequiresWebhookEndpointSetup,
		})
	}
	return page, nil
}

// UpsertTrigger subscribes a connected account to a trigger type, or
// re-enables the matching subscription it already has, and answers the
// trigger instance's id.
func (client *Client) UpsertTrigger(ctx context.Context, slug, connectedAccountID string, config json.RawMessage) (string, error) {
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	body := map[string]any{"connected_account_id": connectedAccountID, "trigger_config": config}
	var answer struct {
		TriggerID string `json:"trigger_id"`
	}
	if err := client.call(ctx, http.MethodPost, "/trigger_instances/"+pathEscape(slug)+"/upsert", nil, body, &answer); err != nil {
		return "", err
	}
	return answer.TriggerID, nil
}

// TriggerInstance is one subscription of a connected account to a trigger.
type TriggerInstance struct {
	ID                 string
	TriggerName        string
	ConnectedAccountID string
	UserID             string
	Config             json.RawMessage
	Version            string
	UpdatedAt          time.Time
	// DisabledAt is set for a subscription that no longer delivers.
	DisabledAt *time.Time
}

// TriggerQuery narrows a trigger instance listing.
type TriggerQuery struct {
	ConnectedAccountIDs []string
	TriggerIDs          []string
	ShowDisabled        bool
	Cursor              string
	Limit               int
}

// Triggers lists trigger instances.
func (client *Client) Triggers(ctx context.Context, query TriggerQuery) (Page[TriggerInstance], error) {
	values := url.Values{}
	addAll(values, "connected_account_ids", query.ConnectedAccountIDs)
	addAll(values, "trigger_ids", query.TriggerIDs)
	if query.ShowDisabled {
		values.Set("show_disabled", "true")
	}
	var answer struct {
		Items []struct {
			ID                 string          `json:"id"`
			TriggerName        string          `json:"trigger_name"`
			ConnectedAccountID string          `json:"connected_account_id"`
			UserID             string          `json:"user_id"`
			TriggerConfig      json.RawMessage `json:"trigger_config"`
			Version            string          `json:"version"`
			UpdatedAt          string          `json:"updated_at"`
			DisabledAt         *string         `json:"disabled_at"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := client.call(ctx, http.MethodGet, "/trigger_instances/active", pageQuery(values, query.Cursor, query.Limit), nil, &answer); err != nil {
		return Page[TriggerInstance]{}, err
	}
	page := Page[TriggerInstance]{NextCursor: cursorOf(answer.NextCursor)}
	for _, item := range answer.Items {
		instance := TriggerInstance{
			ID: item.ID, TriggerName: item.TriggerName, ConnectedAccountID: item.ConnectedAccountID,
			UserID: item.UserID, Config: item.TriggerConfig, Version: item.Version, UpdatedAt: timeOf(item.UpdatedAt),
		}
		if item.DisabledAt != nil {
			if disabled := timeOf(*item.DisabledAt); !disabled.IsZero() {
				instance.DisabledAt = &disabled
			}
		}
		page.Items = append(page.Items, instance)
	}
	return page, nil
}

// SetTriggerEnabled enables or disables a trigger instance without deleting
// it.
func (client *Client) SetTriggerEnabled(ctx context.Context, id string, enabled bool) error {
	status := "disable"
	if enabled {
		status = "enable"
	}
	return client.call(ctx, http.MethodPatch, "/trigger_instances/manage/"+pathEscape(id), nil, map[string]string{"status": status}, nil)
}

// DeleteTrigger removes a trigger instance.
func (client *Client) DeleteTrigger(ctx context.Context, id string) error {
	return client.call(ctx, http.MethodDelete, "/trigger_instances/manage/"+pathEscape(id), nil, nil, nil)
}

// WebhookSubscription is where Composio delivers a project's events, and the
// secret it signs them with.
type WebhookSubscription struct {
	ID            string
	URL           string
	Version       string
	EnabledEvents []string
	Secret        string
}

type webhookSubscriptionWire struct {
	ID            string   `json:"id"`
	WebhookURL    string   `json:"webhook_url"`
	Version       string   `json:"version"`
	EnabledEvents []string `json:"enabled_events"`
	Secret        string   `json:"secret"`
}

func (wire webhookSubscriptionWire) subscription() WebhookSubscription {
	return WebhookSubscription{
		ID: wire.ID, URL: wire.WebhookURL, Version: wire.Version, EnabledEvents: wire.EnabledEvents, Secret: wire.Secret,
	}
}

// WebhookSubscriptions lists the project's webhook subscriptions.
func (client *Client) WebhookSubscriptions(ctx context.Context) ([]WebhookSubscription, error) {
	var subscriptions []WebhookSubscription
	cursor := ""
	for {
		var answer struct {
			Items      []webhookSubscriptionWire `json:"items"`
			NextCursor *string                   `json:"next_cursor"`
		}
		if err := client.call(ctx, http.MethodGet, "/webhook_subscriptions", pageQuery(nil, cursor, 50), nil, &answer); err != nil {
			return nil, err
		}
		for _, wire := range answer.Items {
			subscriptions = append(subscriptions, wire.subscription())
		}
		if cursor = cursorOf(answer.NextCursor); cursor == "" {
			return subscriptions, nil
		}
	}
}

// CreateWebhookSubscription has Composio deliver events of the given types to
// webhookURL, in the current payload version, and answers the signing secret.
func (client *Client) CreateWebhookSubscription(ctx context.Context, webhookURL string, events []string) (WebhookSubscription, error) {
	body := map[string]any{"webhook_url": webhookURL, "enabled_events": events, "version": WebhookVersion}
	var wire webhookSubscriptionWire
	if err := client.call(ctx, http.MethodPost, "/webhook_subscriptions", nil, body, &wire); err != nil {
		return WebhookSubscription{}, err
	}
	return wire.subscription(), nil
}

// DeleteWebhookSubscription stops a subscription's deliveries.
func (client *Client) DeleteWebhookSubscription(ctx context.Context, id string) error {
	return client.call(ctx, http.MethodDelete, "/webhook_subscriptions/"+pathEscape(id), nil, nil, nil)
}
