package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

func TestSlackStatusReadsOnlyTheOrganizationsInstallMetadata(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if _, err := store.InstallStatusForOrganization(ctx, "org_none"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("absent status=%v", err)
	}
	if err := store.SaveInstall(ctx, slackInstall("org_1", "T0OLD01"), slackEvent(t, "apps.slack.install.changed.v1")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.EndInstall(ctx, "T0OLD01", "app_uninstalled", testClock.Add(time.Hour), slackEnded(t)); err != nil {
		t.Fatal(err)
	}
	if status, err := store.InstallStatusForOrganization(ctx, "org_1"); err != nil || status != "ended" {
		t.Fatalf("ended status=%q err=%v", status, err)
	}
	if err := store.SaveInstall(ctx, slackInstall("org_1", "T0NEW01"), slackEvent(t, "apps.slack.install.changed.v1")); err != nil {
		t.Fatal(err)
	}
	// A later ended install must not hide the active one. A deliberately
	// undecodable sealed envelope proves this read never loads token data.
	if _, err := store.pool.Exec(ctx, `UPDATE apps_slack_installs SET sealed_bot_token='"unreadable envelope"'::jsonb WHERE team_id='T0NEW01'`); err != nil {
		t.Fatal(err)
	}
	before := countRows(t, store, `SELECT count(*) FROM apps_outbox`)
	for range 2 {
		if status, err := store.InstallStatusForOrganization(ctx, "org_1"); err != nil || status != "active" {
			t.Fatalf("active metadata=%q err=%v", status, err)
		}
	}
	if _, err := store.InstallForOrganization(ctx, "org_1"); err == nil {
		t.Fatal("the token envelope fixture unexpectedly decoded")
	}
	if _, err := store.InstallStatusForOrganization(ctx, "org_2"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("another organization's status=%v", err)
	}
	if countRows(t, store, `SELECT count(*) FROM apps_outbox`) != before || countRows(t, store, `SELECT count(*) FROM apps_slack_install_flows`) != 0 {
		t.Fatal("metadata reads mutated OAuth or events")
	}
}
