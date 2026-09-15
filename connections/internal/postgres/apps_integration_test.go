package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

func TestAnyAppAndDeleteOrganizationApps(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	org, otherOrg := "org_delete_apps", "org_untouched"

	app1 := createApp(t, store, org, "app_google", domain.CredentialOAuth2, now)
	app2 := createApp(t, store, org, "app_slack", domain.CredentialOAuth2, now)
	otherApp := createApp(t, store, otherOrg, "app_github", domain.CredentialOAuth2, now)

	// AnyApp behaves like App for a live app.
	if got, err := store.AnyApp(ctx, org, app1.ID); err != nil || got.ID != app1.ID || got.Status != domain.AppStatusActive {
		t.Fatalf("AnyApp(live) = %+v, %v", got, err)
	}
	if _, err := store.AnyApp(ctx, org, "app_missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("AnyApp(missing) error = %v", err)
	}

	var mu []domain.App
	appEvent := func(app domain.App) (domain.Event, error) {
		mu = append(mu, app)
		return testEvent("connections.app.deleted.v1", now), nil
	}
	at := now.Add(time.Minute)
	count, err := store.DeleteOrganizationApps(ctx, org, at, appEvent)
	if err != nil || count != 2 {
		t.Fatalf("DeleteOrganizationApps() = %d, %v", count, err)
	}
	if len(mu) != 2 {
		t.Fatalf("DeleteOrganizationApps() called appEvent %d times", len(mu))
	}
	for _, app := range mu {
		if app.Status != domain.AppStatusDisabled || app.Revision != 2 || !app.UpdatedAt.Equal(at) {
			t.Fatalf("app passed to appEvent = %+v", app)
		}
	}

	// App() no longer sees them; AnyApp() still does, disabled.
	if _, err := store.App(ctx, org, app1.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("App() after DeleteOrganizationApps error = %v", err)
	}
	if got, err := store.AnyApp(ctx, org, app1.ID); err != nil || got.Status != domain.AppStatusDisabled || got.Revision != 2 {
		t.Fatalf("AnyApp() after DeleteOrganizationApps = %+v, %v", got, err)
	}
	if got, err := store.AnyApp(ctx, org, app2.ID); err != nil || got.Status != domain.AppStatusDisabled {
		t.Fatalf("AnyApp(app2) after DeleteOrganizationApps = %+v, %v", got, err)
	}

	// Another organization's app is untouched.
	if got, err := store.App(ctx, otherOrg, otherApp.ID); err != nil || got.Status != domain.AppStatusActive {
		t.Fatalf("other organization's app = %+v, %v", got, err)
	}

	// A second call is idempotent: deleted_at IS NULL matches nothing more.
	mu = nil
	count, err = store.DeleteOrganizationApps(ctx, org, at.Add(time.Minute), appEvent)
	if err != nil || count != 0 || len(mu) != 0 {
		t.Fatalf("second DeleteOrganizationApps() = %d, %v (events=%d)", count, err, len(mu))
	}

	claimed, err := store.ClaimOutbox(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	deletedEvents := 0
	for _, event := range claimed {
		if event.DetailType == "connections.app.deleted.v1" {
			deletedEvents++
		}
	}
	if deletedEvents != 2 {
		t.Fatalf("outbox has %d connections.app.deleted.v1 events, want 2", deletedEvents)
	}
}

func TestAppSettingsRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	org := "org_app_settings"

	app := domain.App{
		ID: "app_github", OrganizationID: org, Provider: "github", CredentialType: domain.CredentialInstallation,
		ClientID: "client-1", EncryptedSecret: sealed("secret"),
		Settings: domain.AppSettings{GitHubAppID: "12345", GitHubAppSlug: "oryxa-bot"},
		Scopes:   []string{"repo"}, Status: domain.AppStatusActive, Revision: 1, CreatedBy: "user-admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateApp(ctx, app, testEvent("connections.app.created.v1", now)); err != nil {
		t.Fatalf("CreateApp() error = %v", err)
	}
	got, err := store.App(ctx, org, app.ID)
	if err != nil || got.Settings != app.Settings {
		t.Fatalf("App() Settings = %+v, %v, want %+v", got.Settings, err, app.Settings)
	}

	// An app created with no settings round-trips to the zero value, not nil
	// vs empty ambiguity.
	plain := domain.App{
		ID: "app_google", OrganizationID: org, Provider: "google", CredentialType: domain.CredentialOAuth2,
		ClientID: "client-2", EncryptedSecret: sealed("secret-2"), Scopes: []string{"email"},
		Status: domain.AppStatusActive, Revision: 1, CreatedBy: "user-admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateApp(ctx, plain, testEvent("connections.app.created.v1", now)); err != nil {
		t.Fatalf("CreateApp(plain) error = %v", err)
	}
	if got, err := store.App(ctx, org, plain.ID); err != nil || got.Settings != (domain.AppSettings{}) {
		t.Fatalf("App(plain) Settings = %+v, %v", got.Settings, err)
	}

	updated := app
	updated.Settings = domain.AppSettings{GitHubAppID: "67890", GitHubAppSlug: "oryxa-bot-2"}
	updated.Revision, updated.UpdatedAt = 2, now.Add(time.Minute)
	if err := store.UpdateApp(ctx, updated, 1, "", testEvent("connections.app.updated.v1", now), nil); err != nil {
		t.Fatalf("UpdateApp() error = %v", err)
	}
	if got, err := store.App(ctx, org, app.ID); err != nil || got.Settings != updated.Settings {
		t.Fatalf("App() after UpdateApp Settings = %+v, %v, want %+v", got.Settings, err, updated.Settings)
	}
}
