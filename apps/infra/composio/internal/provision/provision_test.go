package provision

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// fakeClient answers what a test set and records what it was asked.
type fakeClient struct {
	existing []composio.WebhookSubscription
	created  composio.WebhookSubscription
	createds []string
	deleted  []string
}

func (fake *fakeClient) WebhookSubscriptions(context.Context) ([]composio.WebhookSubscription, error) {
	return fake.existing, nil
}

func (fake *fakeClient) CreateWebhookSubscription(_ context.Context, webhookURL string, events []string) (composio.WebhookSubscription, error) {
	fake.createds = append(fake.createds, webhookURL)
	created := fake.created
	if created.ID == "" {
		created = composio.WebhookSubscription{
			ID: "whs_1", URL: webhookURL, Version: composio.WebhookVersion, EnabledEvents: events, Secret: "whsec_new",
		}
	}
	return created, nil
}

func (fake *fakeClient) DeleteWebhookSubscription(_ context.Context, id string) error {
	fake.deleted = append(fake.deleted, id)
	return nil
}

func config() Config {
	return Config{APIKey: "ak_test", WebhookURL: "https://api.oryxa.test/v1/apps/webhooks/composio"}
}

func TestRunCreatesTheSubscriptionAndShowsItsSecret(t *testing.T) {
	fake := &fakeClient{}

	output, err := Run(context.Background(), fake, config())
	if err != nil {
		t.Fatal(err)
	}
	if !output.Created || output.Secret != "whsec_new" || output.SubscriptionID != "whs_1" {
		t.Fatalf("output = %+v", output)
	}
	if !strings.Contains(output.Note, "COMPOSIO_WEBHOOK_SECRET") {
		t.Fatalf("note = %q", output.Note)
	}
	if len(fake.createds) != 1 || fake.createds[0] != config().WebhookURL {
		t.Fatalf("created = %v", fake.createds)
	}
	// Exactly the three deliveries apps handles, in the version it reads.
	if !slices.Equal(output.Events, Events) || output.Version != composio.WebhookVersion {
		t.Fatalf("output = %+v", output)
	}
}

func TestRunLeavesASubscriptionThatAlreadyDeliversHere(t *testing.T) {
	fake := &fakeClient{existing: []composio.WebhookSubscription{{
		ID: "whs_old", URL: "https://API.oryxa.test/v1/apps/webhooks/composio/",
		Version: composio.WebhookVersion, EnabledEvents: Events,
	}}}

	output, err := Run(context.Background(), fake, config())
	if err != nil {
		t.Fatal(err)
	}
	if output.Created || output.SubscriptionID != "whs_old" {
		t.Fatalf("output = %+v", output)
	}
	// Replacing it would mint a new secret and break the running deployment,
	// so nothing is created or deleted, and no secret is invented.
	if output.Secret != "" || len(fake.createds) != 0 || len(fake.deleted) != 0 {
		t.Fatalf("output = %+v, created = %v, deleted = %v", output, fake.createds, fake.deleted)
	}
	if !strings.Contains(output.Note, "already delivers here") {
		t.Fatalf("note = %q", output.Note)
	}
}

func TestRunSaysWhenAnExistingSubscriptionIsNotEnough(t *testing.T) {
	for name, testCase := range map[string]struct {
		subscription composio.WebhookSubscription
		want         string
	}{
		"an older payload version": {
			subscription: composio.WebhookSubscription{ID: "whs_old", URL: config().WebhookURL, Version: "V1", EnabledEvents: Events},
			want:         "payload version V1",
		},
		"missing an event apps acts on": {
			subscription: composio.WebhookSubscription{
				ID: "whs_old", URL: config().WebhookURL, Version: composio.WebhookVersion,
				EnabledEvents: []string{composio.EventTriggerMessage},
			},
			want: composio.EventConnectedAccountExpired,
		},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeClient{existing: []composio.WebhookSubscription{testCase.subscription}}
			output, err := Run(context.Background(), fake, config())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.Note, testCase.want) || !strings.Contains(output.Note, "APPS_REPLACE_WEBHOOK") {
				t.Fatalf("note = %q", output.Note)
			}
			if len(fake.createds) != 0 {
				t.Fatal("it replaced the subscription without being asked")
			}
		})
	}
	t.Run("all events at once", func(t *testing.T) {
		fake := &fakeClient{existing: []composio.WebhookSubscription{{
			ID: "whs_old", URL: config().WebhookURL, Version: composio.WebhookVersion, EnabledEvents: []string{"*"},
		}}}
		output, err := Run(context.Background(), fake, config())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.Note, "already delivers here") {
			t.Fatalf("note = %q", output.Note)
		}
	})
}

func TestRunWillNotStealASubscriptionPointingElsewhere(t *testing.T) {
	elsewhere := composio.WebhookSubscription{ID: "whs_other", URL: "https://staging.oryxa.test/v1/apps/webhooks/composio", Version: composio.WebhookVersion}
	fake := &fakeClient{existing: []composio.WebhookSubscription{elsewhere}}

	_, err := Run(context.Background(), fake, config())
	if err == nil {
		t.Fatal("it took over another deployment's subscription")
	}
	if !strings.Contains(err.Error(), "staging.oryxa.test") || !strings.Contains(err.Error(), "APPS_REPLACE_WEBHOOK") {
		t.Fatalf("error = %v", err)
	}
	if len(fake.deleted) != 0 || len(fake.createds) != 0 {
		t.Fatalf("deleted = %v, created = %v", fake.deleted, fake.createds)
	}

	// Asked outright, it deletes the old one and makes ours.
	replacing := config()
	replacing.Replace = true
	output, err := Run(context.Background(), fake, replacing)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fake.deleted, []string{"whs_other"}) || !output.Created || output.Secret == "" {
		t.Fatalf("deleted = %v, output = %+v", fake.deleted, output)
	}
}

func TestRunRefusesAWebhookURLItCannotUse(t *testing.T) {
	for name, adjust := range map[string]func(*Config){
		"no API key":     func(config *Config) { config.APIKey = "" },
		"no URL":         func(config *Config) { config.WebhookURL = "" },
		"not absolute":   func(config *Config) { config.WebhookURL = "/v1/apps/webhooks/composio" },
		"not encrypted":  func(config *Config) { config.WebhookURL = "http://api.oryxa.test/v1/apps/webhooks/composio" },
		"not even a URL": func(config *Config) { config.WebhookURL = "https://:not a url" },
	} {
		t.Run(name, func(t *testing.T) {
			broken := config()
			adjust(&broken)
			fake := &fakeClient{}
			if _, err := Run(context.Background(), fake, broken); err == nil {
				t.Fatal("accepted it")
			}
			if len(fake.createds) != 0 {
				t.Fatal("it subscribed anyway")
			}
		})
	}
}

func TestFromEnvBuildsTheWebhookURLFromThePublicBaseURL(t *testing.T) {
	t.Setenv("COMPOSIO_API_KEY", "ak_test")
	t.Setenv("APPS_PUBLIC_BASE_URL", "https://api.oryxa.test/")
	t.Setenv("APPS_WEBHOOK_URL", "")
	t.Setenv("APPS_REPLACE_WEBHOOK", "")

	config := FromEnv()
	if config.WebhookURL != "https://api.oryxa.test/v1/apps/webhooks/composio" || config.Replace {
		t.Fatalf("config = %+v", config)
	}

	// A tunnel's URL while developing wins over the base URL.
	t.Setenv("APPS_WEBHOOK_URL", "https://apps.trycloudflare.com/v1/apps/webhooks/composio")
	t.Setenv("APPS_REPLACE_WEBHOOK", "TRUE")
	if config = FromEnv(); config.WebhookURL != "https://apps.trycloudflare.com/v1/apps/webhooks/composio" || !config.Replace {
		t.Fatalf("config = %+v", config)
	}
}
