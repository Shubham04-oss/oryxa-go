package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/slackbot"
)

// TestAReplyIsAThirdKindOfDelivery: an owner's reply from a Slack modal is kept
// as a record of its own kind, its words and the ticket beside the message the
// button was on, and is read back whole; the kind check knows it, and still
// refuses a kind nobody makes, however often its migration runs.
func TestAReplyIsAThirdKindOfDelivery(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	reply := slackbot.Inbound{
		ID: "asi_reply", OrganizationID: "org_1", TeamID: "T0ACME01", Kind: slackbot.InboundReply,
		ChannelID: "D0RAYA", UserID: "U0RAYA", MessageTS: "1780000600.000100", ThreadTS: "1780000600.000100",
		ActionID: slackbot.ActionReply, ActionValue: "t1.reply.sig", Text: "We ship on the 14th.", ReceivedAt: testClock,
	}
	if recorded, err := store.RecordInbound(ctx, "hook:aph_x:sha256:reply", testClock, reply, testEvent(t, "apps.slack.action.received.v1")); err != nil || !recorded {
		t.Fatalf("RecordInbound = %v, %v", recorded, err)
	}
	got, err := store.Inbound(ctx, "asi_reply")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != slackbot.InboundReply || got.Text != reply.Text || got.ActionValue != reply.ActionValue || got.ActionID != slackbot.ActionReply ||
		got.ChannelID != "D0RAYA" || got.MessageTS != reply.MessageTS || got.UserID != "U0RAYA" {
		t.Fatalf("Inbound = %+v", got)
	}
	for range 2 {
		if _, err := store.pool.Exec(ctx, `UPDATE oryxa_migrations SET checksum = 'stale' WHERE service = 'apps' AND name = '009_slack_reply.sql'`); err != nil {
			t.Fatal(err)
		}
		if err := store.Migrate(ctx); err != nil {
			t.Fatalf("migrating again: %v", err)
		}
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO apps_slack_inbound (id, organization_id, team_id, kind, channel_id, user_id, message_ts, thread_ts, received_at)
		VALUES ('asi_bad', 'org_1', 'T0ACME01', 'reaction', 'C0', 'U0', '1.1', '1.1', now())`); err == nil {
		t.Fatal("a kind nobody makes was accepted")
	} else if refused := (*pgconn.PgError)(nil); !errors.As(err, &refused) || refused.Code != "23514" || refused.ConstraintName != "apps_slack_inbound_kind_check" {
		t.Fatalf("a kind nobody makes was refused with %v, want the kind check", err)
	}
	var validated bool
	if err := store.pool.QueryRow(ctx, `SELECT convalidated FROM pg_constraint
		WHERE conname = 'apps_slack_inbound_kind_check' AND conrelid = 'apps_slack_inbound'::regclass`).Scan(&validated); err != nil || !validated {
		t.Fatalf("the kind check is validated=%v (%v)", validated, err)
	}
}
