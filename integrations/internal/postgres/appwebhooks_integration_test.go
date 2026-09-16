package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

func appWebhook(organizationID, appID, provider string, at time.Time) domain.AppWebhook {
	return domain.AppWebhook{
		AppID: appID, OrganizationID: organizationID, Provider: provider, Route: routeFor(appID),
		UpdatedBy: "user-admin", CreatedAt: at, UpdatedAt: at, EncryptedSecret: *sealedData(appID),
	}
}

func TestPutAppWebhookKeepsTheRouteAndReplacesTheSecret(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	webhook := appWebhook("org_1", "app_1", "slack", at)

	saved, err := store.PutAppWebhook(ctx, webhook)
	if err != nil {
		t.Fatalf("PutAppWebhook() error = %v", err)
	}
	if saved.Route != webhook.Route || saved.UpdatedBy != "user-admin" || !saved.CreatedAt.Equal(at) || !saved.UpdatedAt.Equal(at) {
		t.Fatalf("PutAppWebhook() = %+v, want it as given", saved)
	}
	if !sealedEqual(&saved.EncryptedSecret, &webhook.EncryptedSecret) {
		t.Fatalf("PutAppWebhook().EncryptedSecret = %+v, want %+v", saved.EncryptedSecret, webhook.EncryptedSecret)
	}

	// Rotating the secret keeps the route and when it was first set.
	later := at.Add(time.Hour)
	rotated := webhook
	rotated.Route, rotated.EncryptedSecret, rotated.UpdatedBy, rotated.UpdatedAt = routeFor("ignored"), *sealedData("rotated"), "user-other", later
	replaced, err := store.PutAppWebhook(ctx, rotated)
	if err != nil {
		t.Fatalf("PutAppWebhook() again error = %v", err)
	}
	if replaced.Route != webhook.Route || !replaced.CreatedAt.Equal(at) || !replaced.UpdatedAt.Equal(later) || replaced.UpdatedBy != "user-other" {
		t.Fatalf("PutAppWebhook() again = %+v, want the first route and creation time", replaced)
	}
	if !sealedEqual(&replaced.EncryptedSecret, &rotated.EncryptedSecret) {
		t.Fatalf("PutAppWebhook() again kept the old secret")
	}
	if byRoute, err := store.AppWebhookByRoute(ctx, webhook.Route); err != nil || byRoute.AppID != "app_1" {
		t.Fatalf("AppWebhookByRoute(the first route) = %+v, %v", byRoute, err)
	}
	if _, err := store.AppWebhookByRoute(ctx, rotated.Route); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("AppWebhookByRoute(the route that was ignored) error = %v, want domain.ErrNotFound", err)
	}
}

func TestPutAppWebhookRefusesAnAppOfAnotherOrganizationOrProvider(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	first, err := store.PutAppWebhook(ctx, appWebhook("org_1", "app_1", "slack", at))
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name    string
		webhook domain.AppWebhook
	}{
		{name: "another organization", webhook: appWebhook("org_2", "app_1", "slack", at.Add(time.Hour))},
		{name: "another provider", webhook: appWebhook("org_1", "app_1", "github", at.Add(time.Hour))},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := store.PutAppWebhook(ctx, test.webhook); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("PutAppWebhook(%s) error = %v, want domain.ErrNotFound", test.name, err)
			}
			kept, err := store.AppWebhook(ctx, "org_1", "app_1")
			if err != nil || kept.Route != first.Route || !sealedEqual(&kept.EncryptedSecret, &first.EncryptedSecret) || !kept.UpdatedAt.Equal(at) {
				t.Fatalf("app webhook after a refused put = %+v, %v", kept, err)
			}
		})
	}
}

func TestAppWebhookReadsAndDeletes(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	for _, webhook := range []domain.AppWebhook{
		appWebhook("org_1", "app_1", "slack", at),
		appWebhook("org_1", "app_2", "slack", at),
		appWebhook("org_2", "app_3", "slack", at),
	} {
		if _, err := store.PutAppWebhook(ctx, webhook); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := store.AppWebhook(ctx, "org_2", "app_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("AppWebhook(another organization) error = %v, want domain.ErrNotFound", err)
	}
	if _, err := store.AppWebhook(ctx, "org_1", "app_missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("AppWebhook(unknown app) error = %v, want domain.ErrNotFound", err)
	}
	if _, err := store.AppWebhookByRoute(ctx, routeFor("nothing")); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("AppWebhookByRoute(unknown route) error = %v, want domain.ErrNotFound", err)
	}

	// One app's webhook goes, the organization's others stay.
	if err := store.DeleteAppWebhooks(ctx, "org_1", "app_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppWebhook(ctx, "org_1", "app_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("AppWebhook(deleted) error = %v, want domain.ErrNotFound", err)
	}
	if _, err := store.AppWebhook(ctx, "org_1", "app_2"); err != nil {
		t.Fatalf("AppWebhook(app_2) error = %v, want it kept", err)
	}

	// An organization's webhooks go, another organization's stay.
	if err := store.DeleteAppWebhooks(ctx, "org_1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppWebhook(ctx, "org_1", "app_2"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("AppWebhook(app_2) after the organization's deletion error = %v, want domain.ErrNotFound", err)
	}
	if _, err := store.AppWebhook(ctx, "org_2", "app_3"); err != nil {
		t.Fatalf("AppWebhook(another organization's) error = %v, want it kept", err)
	}
	// Deleting again changes nothing.
	if err := store.DeleteAppWebhooks(ctx, "org_1", ""); err != nil {
		t.Fatalf("DeleteAppWebhooks() again error = %v", err)
	}
}
