package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/slackbot"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// Store keeps the Slack surface: installs, the inbound records, the posts and
// the rest. See migration 008.
var _ slackbot.Store = (*Store)(nil)

const slackInstallColumns = `team_id, organization_id, status, end_reason, team_name, bot_user_id, app_id, scopes,
	sealed_bot_token, installed_by, installed_at, updated_at, ended_at`

func scanSlackInstall(row pgx.Row) (slackbot.Install, error) {
	var (
		install slackbot.Install
		sealed  []byte
	)
	if err := row.Scan(&install.TeamID, &install.OrganizationID, &install.Status, &install.EndReason, &install.TeamName,
		&install.BotUserID, &install.AppID, &install.Scopes, &sealed, &install.InstalledBy, &install.InstalledAt,
		&install.UpdatedAt, &install.EndedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return slackbot.Install{}, err
		}
		return slackbot.Install{}, fmt.Errorf("read a Slack install: %w", err)
	}
	if sealed != nil {
		var data secrets.SealedData
		if err := json.Unmarshal(sealed, &data); err != nil {
			return slackbot.Install{}, fmt.Errorf("read a sealed Slack bot token: %w", err)
		}
		install.Sealed = &data
	}
	return install, nil
}

// notFound turns a missing row into domain.ErrNotFound and leaves any other
// error as it is.
func notFound(what string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", what, domain.ErrNotFound)
	}
	return err
}

// InstallByTeam reads a workspace's install, active or ended.
func (store *Store) InstallByTeam(ctx context.Context, teamID string) (slackbot.Install, error) {
	install, err := scanSlackInstall(store.pool.QueryRow(ctx,
		`SELECT `+slackInstallColumns+` FROM apps_slack_installs WHERE team_id = $1`, teamID))
	return install, notFound("slack install", err)
}

// InstallForOrganization reads an organization's active install, or when it
// has none the one that ended last.
func (store *Store) InstallForOrganization(ctx context.Context, organizationID string) (slackbot.Install, error) {
	install, err := scanSlackInstall(store.pool.QueryRow(ctx, `
		SELECT `+slackInstallColumns+`
		FROM apps_slack_installs
		WHERE organization_id = $1
		ORDER BY (status = 'active') DESC, updated_at DESC
		LIMIT 1`, organizationID))
	return install, notFound("slack install", err)
}

// InstallStatusForOrganization reads the same active/latest install as the
// admin view, without selecting or decoding its sealed bot token.
func (store *Store) InstallStatusForOrganization(ctx context.Context, organizationID string) (string, error) {
	var status string
	err := store.pool.QueryRow(ctx, `SELECT status FROM apps_slack_installs
		WHERE organization_id=$1 ORDER BY (status='active') DESC, updated_at DESC LIMIT 1`, organizationID).Scan(&status)
	return status, notFound("slack install status", err)
}

// SaveInstall makes install the workspace's active install and writes its
// event, in one transaction.
func (store *Store) SaveInstall(ctx context.Context, install slackbot.Install, event func(reinstalled bool) (domain.Event, error)) error {
	sealed, err := json.Marshal(install.Sealed)
	if err != nil || install.Sealed == nil {
		return errors.New("save a Slack install: it has no sealed bot token")
	}
	err = store.inTx(ctx, func(tx pgx.Tx) error {
		var existingOrganization, existingStatus string
		err := tx.QueryRow(ctx, `SELECT organization_id, status FROM apps_slack_installs WHERE team_id = $1 FOR UPDATE`,
			install.TeamID).Scan(&existingOrganization, &existingStatus)
		existing := err == nil
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return fmt.Errorf("read the workspace's Slack install: %w", err)
		case existingStatus == "active" && existingOrganization != install.OrganizationID:
			return slackbot.ErrTeamBound
		}
		var other string
		err = tx.QueryRow(ctx, `
			SELECT team_id FROM apps_slack_installs
			WHERE organization_id = $1 AND status = 'active' AND team_id <> $2
			FOR UPDATE`, install.OrganizationID, install.TeamID).Scan(&other)
		switch {
		case err == nil:
			return slackbot.ErrOrganizationBound
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("read the organization's Slack install: %w", err)
		}
		if existing {
			// An ended install taken up again is installed afresh, so its
			// installed_at is now; an active one installed over keeps it.
			if _, err := tx.Exec(ctx, `
				UPDATE apps_slack_installs
				SET organization_id = $2, status = 'active', end_reason = '', team_name = $3, bot_user_id = $4, app_id = $5,
				    scopes = $6, sealed_bot_token = $7, installed_by = $8,
				    installed_at = CASE WHEN status = 'active' THEN installed_at ELSE $9 END,
				    updated_at = $9, ended_at = NULL
				WHERE team_id = $1`, install.TeamID, install.OrganizationID, install.TeamName, install.BotUserID,
				install.AppID, nonNil(install.Scopes), sealed, install.InstalledBy, install.InstalledAt); err != nil {
				return mapSlackInstallError(err)
			}
		} else if _, err := tx.Exec(ctx, `
			INSERT INTO apps_slack_installs (`+slackInstallColumns+`)
			VALUES ($1, $2, 'active', '', $3, $4, $5, $6, $7, $8, $9, $9, NULL)`,
			install.TeamID, install.OrganizationID, install.TeamName, install.BotUserID, install.AppID,
			nonNil(install.Scopes), sealed, install.InstalledBy, install.InstalledAt); err != nil {
			return mapSlackInstallError(err)
		}
		notice, err := event(existing && existingStatus == "active")
		if err != nil {
			return err
		}
		return insertEvents(ctx, tx, notice)
	})
	return err
}

// mapSlackInstallError turns the two uniqueness rules an install can break,
// when two installs race, into the errors that name them.
func mapSlackInstallError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case "apps_slack_installs_pkey":
			return slackbot.ErrTeamBound
		case "apps_slack_installs_organization_idx":
			return slackbot.ErrOrganizationBound
		}
	}
	return mapWriteError("save a Slack install", err)
}

// endInstall ends a workspace's active install inside tx and answers it as it
// was, before its token went. It answers false when there was none.
func endInstall(ctx context.Context, tx pgx.Tx, teamID, reason string, now time.Time) (slackbot.Install, bool, error) {
	install, err := scanSlackInstall(tx.QueryRow(ctx, `
		UPDATE apps_slack_installs
		SET status = 'ended', end_reason = $2, sealed_bot_token = NULL, ended_at = $3, updated_at = $3
		WHERE team_id = $1 AND status = 'active'
		RETURNING `+slackInstallColumns, teamID, reason, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return slackbot.Install{}, false, nil
	}
	if err != nil {
		return slackbot.Install{}, false, err
	}
	return install, true, nil
}

// EndInstall ends a workspace's active install and writes its event.
func (store *Store) EndInstall(ctx context.Context, teamID, reason string, now time.Time, event func(slackbot.Install) (domain.Event, error)) (slackbot.Install, bool, error) {
	var (
		ended slackbot.Install
		found bool
	)
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		if ended, found, err = endInstall(ctx, tx, teamID, reason, now); err != nil || !found {
			return err
		}
		notice, err := event(ended)
		if err != nil {
			return err
		}
		return insertEvents(ctx, tx, notice)
	})
	return ended, found, err
}

// RecordInstallEnd records a delivery that ends an install: its dedupe mark,
// the install ended, and the event, together.
func (store *Store) RecordInstallEnd(ctx context.Context, mark string, at time.Time, teamID, reason string, event func(slackbot.Install) (domain.Event, error)) (bool, error) {
	recorded := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		first, err := markDelivery(ctx, tx, mark, at)
		if err != nil || !first {
			return err
		}
		ended, found, err := endInstall(ctx, tx, teamID, reason, at)
		if err != nil || !found {
			return err
		}
		notice, err := event(ended)
		if err != nil {
			return err
		}
		recorded = true
		return insertEvents(ctx, tx, notice)
	})
	return recorded, err
}

// markDelivery records a delivery's dedupe mark, and says whether it was the
// first: hooks' own mark, so a delivery is one whichever path recorded it.
func markDelivery(ctx context.Context, tx pgx.Tx, mark string, at time.Time) (bool, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO apps_handled_events (event_id, detail_type, handled_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (event_id) DO NOTHING`, mark, hookDeliveryMark, at)
	if err != nil {
		return false, fmt.Errorf("record a Slack delivery: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// InsertInstallFlow keeps an install in progress.
func (store *Store) InsertInstallFlow(ctx context.Context, flow slackbot.InstallFlow) error {
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO apps_slack_install_flows (state_hash, organization_id, member_id, return_to, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		flow.StateHash, flow.OrganizationID, flow.MemberID, flow.ReturnTo, flow.CreatedAt, flow.ExpiresAt); err != nil {
		return mapWriteError("start a Slack install", err)
	}
	return nil
}

// ConsumeInstallFlow spends an install in progress, once, while it is
// unexpired.
func (store *Store) ConsumeInstallFlow(ctx context.Context, stateHash string, now time.Time) (slackbot.InstallFlow, error) {
	flow := slackbot.InstallFlow{StateHash: stateHash, ConsumedAt: &now}
	err := store.pool.QueryRow(ctx, `
		UPDATE apps_slack_install_flows
		SET consumed_at = $2
		WHERE state_hash = $1 AND consumed_at IS NULL AND expires_at > $2
		RETURNING organization_id, member_id, return_to, created_at, expires_at`, stateHash, now,
	).Scan(&flow.OrganizationID, &flow.MemberID, &flow.ReturnTo, &flow.CreatedAt, &flow.ExpiresAt)
	if err != nil {
		return slackbot.InstallFlow{}, notFound("slack install flow", err)
	}
	return flow, nil
}

// RecordInbound records a verified delivery: its dedupe mark, the inbound
// record and the event that names it, in one transaction. A delivery already
// marked writes nothing.
func (store *Store) RecordInbound(ctx context.Context, mark string, at time.Time, inbound slackbot.Inbound, event domain.Event) (bool, error) {
	recorded := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		first, err := markDelivery(ctx, tx, mark, at)
		if err != nil || !first {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO apps_slack_inbound (id, organization_id, team_id, kind, event_id, channel_id, user_id, message_ts,
			                                thread_ts, channel_type, ext_shared, mentions, body, action_id, action_value, received_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
			inbound.ID, inbound.OrganizationID, inbound.TeamID, inbound.Kind, inbound.EventID, inbound.ChannelID, inbound.UserID,
			inbound.MessageTS, inbound.ThreadTS, inbound.ChannelType, inbound.ExtShared, nonNil(inbound.Mentions), inbound.Text,
			inbound.ActionID, inbound.ActionValue, inbound.ReceivedAt); err != nil {
			return mapWriteError("record a Slack delivery", err)
		}
		recorded = true
		return insertEvents(ctx, tx, event)
	})
	return recorded, err
}

// Inbound reads an inbound record.
func (store *Store) Inbound(ctx context.Context, id string) (slackbot.Inbound, error) {
	var inbound slackbot.Inbound
	err := store.pool.QueryRow(ctx, `
		SELECT id, organization_id, team_id, kind, event_id, channel_id, user_id, message_ts, thread_ts, channel_type,
		       ext_shared, mentions, body, action_id, action_value, received_at
		FROM apps_slack_inbound WHERE id = $1`, id,
	).Scan(&inbound.ID, &inbound.OrganizationID, &inbound.TeamID, &inbound.Kind, &inbound.EventID, &inbound.ChannelID,
		&inbound.UserID, &inbound.MessageTS, &inbound.ThreadTS, &inbound.ChannelType, &inbound.ExtShared, &inbound.Mentions,
		&inbound.Text, &inbound.ActionID, &inbound.ActionValue, &inbound.ReceivedAt)
	if err != nil {
		return slackbot.Inbound{}, notFound("slack inbound record", err)
	}
	return inbound, nil
}

// ThreadSeen finds a thread the workspace has sent a verified message or
// click in, by its root or by a message in it, and answers its root.
func (store *Store) ThreadSeen(ctx context.Context, organizationID, teamID, channelID, ts string) (string, bool, error) {
	var root string
	err := store.pool.QueryRow(ctx, `
		SELECT thread_ts FROM apps_slack_inbound
		WHERE organization_id = $1 AND team_id = $2 AND channel_id = $3 AND (thread_ts = $4 OR message_ts = $4)
		ORDER BY received_at DESC
		LIMIT 1`, organizationID, teamID, channelID, ts).Scan(&root)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("look for a Slack thread: %w", err)
	}
	return root, true, nil
}

// UserSeen says a person wrote, or was named in, a verified inbound record
// received since.
func (store *Store) UserSeen(ctx context.Context, organizationID, teamID, userID string, since time.Time) (bool, error) {
	var seen bool
	if err := store.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM apps_slack_inbound
			WHERE organization_id = $1 AND team_id = $2 AND received_at >= $4 AND (user_id = $3 OR $3 = ANY(mentions))
		)`, organizationID, teamID, userID, since).Scan(&seen); err != nil {
		return false, fmt.Errorf("look for a Slack user: %w", err)
	}
	return seen, nil
}

const slackPostColumns = `organization_id, idempotency_key, request_hash, team_id, template, kind, channel_id, ts, claimed_at, completed_at`

func scanSlackPost(row pgx.Row) (slackbot.Post, error) {
	var post slackbot.Post
	err := row.Scan(&post.OrganizationID, &post.Key, &post.RequestHash, &post.TeamID, &post.Template, &post.Kind,
		&post.ChannelID, &post.TS, &post.ClaimedAt, &post.CompletedAt)
	return post, err
}

// ClaimPost records that a post is being made, or answers who has its key. A
// claim never completed and older than staleBefore is taken over, for a post
// whose maker died, as long as it is the same request.
func (store *Store) ClaimPost(ctx context.Context, post slackbot.Post, staleBefore time.Time) (slackbot.Post, bool, error) {
	claimed, err := scanSlackPost(store.pool.QueryRow(ctx, `
		INSERT INTO apps_slack_posts (organization_id, idempotency_key, request_hash, team_id, template, kind, claimed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (organization_id, idempotency_key) DO UPDATE
		SET claimed_at = EXCLUDED.claimed_at
		WHERE apps_slack_posts.completed_at IS NULL
		  AND apps_slack_posts.claimed_at < $8
		  AND apps_slack_posts.request_hash = EXCLUDED.request_hash
		RETURNING `+slackPostColumns,
		post.OrganizationID, post.Key, post.RequestHash, post.TeamID, post.Template, post.Kind, post.ClaimedAt, staleBefore))
	if err == nil {
		return claimed, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return slackbot.Post{}, false, mapWriteError("claim a Slack post", err)
	}
	held, err := scanSlackPost(store.pool.QueryRow(ctx, `
		SELECT `+slackPostColumns+` FROM apps_slack_posts WHERE organization_id = $1 AND idempotency_key = $2`,
		post.OrganizationID, post.Key))
	if err != nil {
		return slackbot.Post{}, false, notFound("slack post", err)
	}
	return held, false, nil
}

// CompletePost records where a claimed post went.
func (store *Store) CompletePost(ctx context.Context, organizationID, key, channelID, ts string, at time.Time) error {
	tag, err := store.pool.Exec(ctx, `
		UPDATE apps_slack_posts SET channel_id = $3, ts = $4, completed_at = $5
		WHERE organization_id = $1 AND idempotency_key = $2`, organizationID, key, channelID, ts, at)
	if err != nil {
		return mapWriteError("complete a Slack post", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("slack post: %w", domain.ErrNotFound)
	}
	return nil
}

// ReleasePost forgets a claim whose post Slack refused.
func (store *Store) ReleasePost(ctx context.Context, organizationID, key string) error {
	if _, err := store.pool.Exec(ctx, `
		DELETE FROM apps_slack_posts
		WHERE organization_id = $1 AND idempotency_key = $2 AND completed_at IS NULL`, organizationID, key); err != nil {
		return fmt.Errorf("release a Slack post: %w", err)
	}
	return nil
}

// PostAt finds a completed post by where it went.
func (store *Store) PostAt(ctx context.Context, organizationID, channelID, ts string) (slackbot.Post, error) {
	post, err := scanSlackPost(store.pool.QueryRow(ctx, `
		SELECT `+slackPostColumns+`
		FROM apps_slack_posts
		WHERE organization_id = $1 AND channel_id = $2 AND ts = $3 AND completed_at IS NOT NULL`, organizationID, channelID, ts))
	return post, notFound("slack post", err)
}

// Profiles answers the profiles of users kept since fetchedAfter.
func (store *Store) Profiles(ctx context.Context, teamID string, userIDs []string, fetchedAfter time.Time) ([]slackbot.Profile, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT team_id, user_id, email, email_confirmed, name, title, is_guest, is_bot, is_deleted, is_stranger, fetched_at
		FROM apps_slack_profiles
		WHERE team_id = $1 AND user_id = ANY($2) AND fetched_at > $3`, teamID, nonNil(userIDs), fetchedAfter)
	if err != nil {
		return nil, fmt.Errorf("read Slack profiles: %w", err)
	}
	defer rows.Close()
	var profiles []slackbot.Profile
	for rows.Next() {
		var profile slackbot.Profile
		if err := rows.Scan(&profile.TeamID, &profile.UserID, &profile.Email, &profile.EmailConfirmed, &profile.Name, &profile.Title,
			&profile.IsGuest, &profile.IsBot, &profile.IsDeleted, &profile.IsStranger, &profile.FetchedAt); err != nil {
			return nil, fmt.Errorf("read a Slack profile: %w", err)
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read Slack profiles: %w", err)
	}
	return profiles, nil
}

// SaveProfile keeps what Slack said about a person.
func (store *Store) SaveProfile(ctx context.Context, profile slackbot.Profile) error {
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO apps_slack_profiles (team_id, user_id, email, email_confirmed, name, title, is_guest, is_bot, is_deleted, is_stranger, fetched_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (team_id, user_id) DO UPDATE
		SET email = EXCLUDED.email, email_confirmed = EXCLUDED.email_confirmed, name = EXCLUDED.name, title = EXCLUDED.title,
		    is_guest = EXCLUDED.is_guest, is_bot = EXCLUDED.is_bot, is_deleted = EXCLUDED.is_deleted,
		    is_stranger = EXCLUDED.is_stranger, fetched_at = EXCLUDED.fetched_at`,
		profile.TeamID, profile.UserID, profile.Email, profile.EmailConfirmed, profile.Name, profile.Title, profile.IsGuest,
		profile.IsBot, profile.IsDeleted, profile.IsStranger, profile.FetchedAt); err != nil {
		return mapWriteError("save a Slack profile", err)
	}
	return nil
}

// HighPriorityChannels lists the channels an organization marked.
func (store *Store) HighPriorityChannels(ctx context.Context, organizationID, teamID string) ([]slackbot.MarkedChannel, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT channel_id, name, marked_by, marked_at
		FROM apps_slack_channels
		WHERE organization_id = $1 AND team_id = $2 AND high_priority
		ORDER BY name, channel_id`, organizationID, teamID)
	if err != nil {
		return nil, fmt.Errorf("read the high priority channels: %w", err)
	}
	defer rows.Close()
	var channels []slackbot.MarkedChannel
	for rows.Next() {
		var channel slackbot.MarkedChannel
		if err := rows.Scan(&channel.ChannelID, &channel.Name, &channel.MarkedBy, &channel.MarkedAt); err != nil {
			return nil, fmt.Errorf("read a high priority channel: %w", err)
		}
		channels = append(channels, channel)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the high priority channels: %w", err)
	}
	return channels, nil
}

// IsHighPriority says a channel is marked.
func (store *Store) IsHighPriority(ctx context.Context, organizationID, teamID, channelID string) (bool, error) {
	var marked bool
	if err := store.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM apps_slack_channels
			WHERE organization_id = $1 AND team_id = $2 AND channel_id = $3 AND high_priority
		)`, organizationID, teamID, channelID).Scan(&marked); err != nil {
		return false, fmt.Errorf("read a channel's mark: %w", err)
	}
	return marked, nil
}

// ReplaceHighPriority makes exactly channels the organization's marked ones.
// A channel that stays marked keeps who marked it and when.
func (store *Store) ReplaceHighPriority(ctx context.Context, organizationID, teamID, actor string, channels []slackbot.MarkedChannel, now time.Time) error {
	return store.inTx(ctx, func(tx pgx.Tx) error {
		ids := make([]string, 0, len(channels))
		for _, channel := range channels {
			ids = append(ids, channel.ChannelID)
			if _, err := tx.Exec(ctx, `
				INSERT INTO apps_slack_channels (organization_id, team_id, channel_id, name, high_priority, marked_by, marked_at)
				VALUES ($1, $2, $3, $4, true, $5, $6)
				ON CONFLICT (organization_id, channel_id) DO UPDATE
				SET name = EXCLUDED.name,
				    marked_by = CASE WHEN apps_slack_channels.high_priority AND apps_slack_channels.team_id = EXCLUDED.team_id
				                     THEN apps_slack_channels.marked_by ELSE EXCLUDED.marked_by END,
				    marked_at = CASE WHEN apps_slack_channels.high_priority AND apps_slack_channels.team_id = EXCLUDED.team_id
				                     THEN apps_slack_channels.marked_at ELSE EXCLUDED.marked_at END,
				    team_id = EXCLUDED.team_id,
				    high_priority = true`,
				organizationID, teamID, channel.ChannelID, channel.Name, actor, now); err != nil {
				return mapWriteError("mark a Slack channel", err)
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE apps_slack_channels
			SET high_priority = false, marked_by = $3, marked_at = $4
			WHERE organization_id = $1 AND high_priority AND channel_id <> ALL($2)`,
			organizationID, ids, actor, now); err != nil {
			return mapWriteError("unmark Slack channels", err)
		}
		return nil
	})
}

// Purge forgets what is older than the cutoffs.
func (store *Store) Purge(ctx context.Context, cutoffs slackbot.Cutoffs) (slackbot.Purged, error) {
	var purged slackbot.Purged
	for _, step := range []struct {
		count *int64
		sql   string
		at    time.Time
	}{
		{&purged.Inbound, `DELETE FROM apps_slack_inbound WHERE received_at < $1`, cutoffs.Inbound},
		{&purged.Flows, `DELETE FROM apps_slack_install_flows WHERE expires_at < $1`, cutoffs.Flows},
		{&purged.Flows, `DELETE FROM apps_slack_link_flows WHERE expires_at < $1`, cutoffs.Flows},
		{&purged.Posts, `DELETE FROM apps_slack_posts WHERE claimed_at < $1`, cutoffs.Posts},
		{&purged.Profiles, `DELETE FROM apps_slack_profiles WHERE fetched_at < $1`, cutoffs.Profiles},
	} {
		tag, err := store.pool.Exec(ctx, step.sql, step.at)
		if err != nil {
			return purged, fmt.Errorf("purge Slack records: %w", err)
		}
		*step.count += tag.RowsAffected()
	}
	return purged, nil
}
