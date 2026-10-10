package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/slackbot"
)

// InsertLinkFlow keeps a member's link in progress.
func (store *Store) InsertLinkFlow(ctx context.Context, flow slackbot.LinkFlow) error {
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO apps_slack_link_flows (state_hash, organization_id, member_id, team_id, return_to, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		flow.StateHash, flow.OrganizationID, flow.MemberID, flow.TeamID, flow.ReturnTo, flow.CreatedAt, flow.ExpiresAt); err != nil {
		return mapWriteError("start a Slack link", err)
	}
	return nil
}

// ConsumeLinkFlow spends a link in progress, once, while it is unexpired.
func (store *Store) ConsumeLinkFlow(ctx context.Context, stateHash string, now time.Time) (slackbot.LinkFlow, error) {
	flow := slackbot.LinkFlow{StateHash: stateHash, ConsumedAt: &now}
	err := store.pool.QueryRow(ctx, `
		UPDATE apps_slack_link_flows
		SET consumed_at = $2
		WHERE state_hash = $1 AND consumed_at IS NULL AND expires_at > $2
		RETURNING organization_id, member_id, team_id, return_to, created_at, expires_at`, stateHash, now,
	).Scan(&flow.OrganizationID, &flow.MemberID, &flow.TeamID, &flow.ReturnTo, &flow.CreatedAt, &flow.ExpiresAt)
	if err != nil {
		return slackbot.LinkFlow{}, notFound("slack link flow", err)
	}
	return flow, nil
}

// SaveLink keeps a member's link in one transaction: a Slack user linked to
// another member refuses it, and the member's link to another user of the
// workspace is replaced.
func (store *Store) SaveLink(ctx context.Context, link slackbot.Link) error {
	return store.inTx(ctx, func(tx pgx.Tx) error {
		var holder string
		err := tx.QueryRow(ctx, `
			SELECT member_id FROM apps_slack_links WHERE team_id = $1 AND slack_user_id = $2 FOR UPDATE`,
			link.TeamID, link.SlackUserID).Scan(&holder)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return fmt.Errorf("read a Slack link: %w", err)
		case holder != link.MemberID:
			return slackbot.ErrLinkTaken
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM apps_slack_links WHERE team_id = $1 AND member_id = $2 AND slack_user_id <> $3`,
			link.TeamID, link.MemberID, link.SlackUserID); err != nil {
			return fmt.Errorf("replace a member's Slack link: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO apps_slack_links (team_id, slack_user_id, organization_id, member_id, linked_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (team_id, slack_user_id) DO UPDATE SET organization_id = EXCLUDED.organization_id, linked_at = EXCLUDED.linked_at`,
			link.TeamID, link.SlackUserID, link.OrganizationID, link.MemberID, link.LinkedAt); err != nil {
			// A link another transaction made meanwhile, for this user or this
			// member, is taken.
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return slackbot.ErrLinkTaken
			}
			return mapWriteError("save a Slack link", err)
		}
		return nil
	})
}

const slackLinkColumns = `team_id, slack_user_id, organization_id, member_id, linked_at`

func scanSlackLink(row pgx.Row) (slackbot.Link, error) {
	var link slackbot.Link
	err := row.Scan(&link.TeamID, &link.SlackUserID, &link.OrganizationID, &link.MemberID, &link.LinkedAt)
	return link, err
}

// LinkForMember reads a member's link in their organization.
func (store *Store) LinkForMember(ctx context.Context, organizationID, memberID string) (slackbot.Link, error) {
	link, err := scanSlackLink(store.pool.QueryRow(ctx, `
		SELECT `+slackLinkColumns+` FROM apps_slack_links
		WHERE organization_id = $1 AND member_id = $2
		ORDER BY linked_at DESC LIMIT 1`, organizationID, memberID))
	if err != nil {
		return slackbot.Link{}, notFound("slack link", err)
	}
	return link, nil
}

// LinksForSlackUsers reads the links of a workspace's users.
func (store *Store) LinksForSlackUsers(ctx context.Context, teamID string, userIDs []string) ([]slackbot.Link, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT `+slackLinkColumns+` FROM apps_slack_links
		WHERE team_id = $1 AND slack_user_id = ANY($2)`, teamID, userIDs)
	if err != nil {
		return nil, fmt.Errorf("read Slack links: %w", err)
	}
	links, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (slackbot.Link, error) { return scanSlackLink(row) })
	if err != nil {
		return nil, fmt.Errorf("read Slack links: %w", err)
	}
	return links, nil
}

// DeleteLink forgets a member's link, answering whether there was one.
func (store *Store) DeleteLink(ctx context.Context, organizationID, memberID string) (bool, error) {
	tag, err := store.pool.Exec(ctx, `DELETE FROM apps_slack_links WHERE organization_id = $1 AND member_id = $2`, organizationID, memberID)
	if err != nil {
		return false, fmt.Errorf("delete a Slack link: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
