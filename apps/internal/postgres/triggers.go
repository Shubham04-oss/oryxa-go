package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/application"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/hooks"
)

const triggerColumns = `id, organization_id, member_id, connection_id, toolkit, slug, config,
	composio_trigger_id, COALESCE(pipedream_trigger_id, ''), status, status_reason, kind, manual_setup, delivery_count, last_delivery_at,
	created_at, updated_at`

// CreateTrigger records a trigger, unless a live one already answers the
// same Composio instance: Composio hands back the same trigger id for the
// same account, trigger and config, so a retry must not create a second row
// for it. The insert carries its own conflict check, targeting the same
// partial unique index the migration declares, so two callers racing to
// create the same instance cannot both believe they created it - the loser
// reads back what the winner inserted, inside the same transaction.
//
// It also refuses to insert at all once the connection is no longer active:
// without that, an auto-subscribe pass racing a disconnect could insert a
// trigger after RemoveConnection has already committed, leaving a row that
// looks live but that removal's own cleanup will never see again.
func (store *Store) CreateTrigger(ctx context.Context, trigger domain.Trigger, event domain.Event) (domain.Trigger, bool, error) {
	var result domain.Trigger
	created := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		// FOR SHARE conflicts with RemoveConnection's UPDATE (which takes an
		// exclusive row lock to change status), so the two are serialised:
		// this either runs before that commits, or sees 'removed' here and
		// refuses - never a trigger inserted after the connection is gone.
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM apps_connections WHERE id = $1 FOR SHARE`,
			trigger.ConnectionID).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("create trigger: the connection does not exist: %w", domain.ErrNotConnected)
			}
			return fmt.Errorf("create trigger: read the connection: %w", err)
		}
		if status != string(domain.ConnectionActive) {
			return fmt.Errorf("create trigger: the connection is %s: %w", status, domain.ErrNotConnected)
		}

		triggers, err := queryTriggers(ctx, tx, `
			INSERT INTO apps_triggers (
				id, organization_id, member_id, connection_id, toolkit, slug, config,
				composio_trigger_id, status, status_reason, kind, manual_setup, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13)
			ON CONFLICT (composio_trigger_id) WHERE status <> 'removed' AND composio_trigger_id <> '' DO NOTHING
			RETURNING `+triggerColumns,
			trigger.ID, trigger.OrganizationID, trigger.MemberID, trigger.ConnectionID, trigger.Toolkit,
			trigger.Slug, []byte(trigger.Config), trigger.ComposioTriggerID, string(trigger.Status),
			trigger.StatusReason, trigger.Kind, trigger.ManualSetup, trigger.CreatedAt)
		if err != nil {
			return mapWriteError("create trigger", err)
		}
		if len(triggers) == 1 {
			result = triggers[0]
			created = true
			// Only a subscription that is new announces one; the loser of a
			// race reads back the winner's and says nothing.
			return insertEvents(ctx, tx, event)
		}
		existing, err := queryTriggers(ctx, tx, `
			SELECT `+triggerColumns+`
			FROM apps_triggers
			WHERE composio_trigger_id = $1 AND status <> 'removed'`, trigger.ComposioTriggerID)
		if err != nil {
			return err
		}
		if len(existing) == 0 {
			return fmt.Errorf("create trigger: the conflicting instance vanished mid-transaction: %w", domain.ErrConflict)
		}
		result = existing[0]
		return nil
	})
	return result, created, err
}

// CreatePipedreamTrigger records a trigger deployed at Pipedream with its
// sealed signing key. It is keyed, as an Oryxa subscription is, on own_key:
// one live trigger per connection, event and config, so two requests racing
// to subscribe the same thing record one and the loser reads it back. Like
// CreateTrigger it refuses once the connection is no longer active.
func (store *Store) CreatePipedreamTrigger(ctx context.Context, trigger domain.Trigger, sealedKey []byte, event domain.Event) (domain.Trigger, bool, error) {
	var result domain.Trigger
	created := false
	key := application.OwnKey(trigger.ConnectionID, trigger.Slug, trigger.Config)
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM apps_connections WHERE id = $1 FOR SHARE`,
			trigger.ConnectionID).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("create trigger: the connection does not exist: %w", domain.ErrNotConnected)
			}
			return fmt.Errorf("create trigger: read the connection: %w", err)
		}
		if status != string(domain.ConnectionActive) {
			return fmt.Errorf("create trigger: the connection is %s: %w", status, domain.ErrNotConnected)
		}
		triggers, err := queryTriggers(ctx, tx, `
			INSERT INTO apps_triggers (
				id, organization_id, member_id, connection_id, toolkit, slug, config,
				composio_trigger_id, own_key, broker, pipedream_trigger_id, pipedream_signing_key,
				status, status_reason, kind, manual_setup, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, '', $8, 'pipedream', $9, $10, $11, '', $12, false, $13, $13)
			ON CONFLICT (own_key) WHERE status <> 'removed' AND own_key <> '' DO NOTHING
			RETURNING `+triggerColumns,
			trigger.ID, trigger.OrganizationID, trigger.MemberID, trigger.ConnectionID, trigger.Toolkit,
			trigger.Slug, []byte(trigger.Config), key, trigger.PipedreamTriggerID, sealedKey,
			string(trigger.Status), trigger.Kind, trigger.CreatedAt)
		if err != nil {
			return mapWriteError("create trigger", err)
		}
		if len(triggers) == 1 {
			result, created = triggers[0], true
			return insertEvents(ctx, tx, event)
		}
		existing, err := queryTriggers(ctx, tx, `
			SELECT `+triggerColumns+`
			FROM apps_triggers
			WHERE own_key = $1 AND status <> 'removed'`, key)
		if err != nil {
			return err
		}
		if len(existing) == 0 {
			return fmt.Errorf("create trigger: the conflicting subscription vanished mid-transaction: %w", domain.ErrConflict)
		}
		result = existing[0]
		return nil
	})
	return result, created, err
}

// PipedreamTrigger reads a Pipedream trigger by apps' own id, in whichever
// organization it is, with its sealed signing key: a delivery names only the
// trigger, and its signature is what proves it.
func (store *Store) PipedreamTrigger(ctx context.Context, id string) (domain.Trigger, []byte, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT `+triggerColumns+`, pipedream_signing_key
		FROM apps_triggers
		WHERE id = $1 AND pipedream_trigger_id IS NOT NULL`, id)
	if err != nil {
		return domain.Trigger{}, nil, mapWriteError("read a Pipedream trigger", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return domain.Trigger{}, nil, fmt.Errorf("read a Pipedream trigger: %w", err)
		}
		return domain.Trigger{}, nil, domain.ErrNotFound
	}
	var sealed []byte
	trigger, err := scanTrigger(rows, &sealed)
	if err != nil {
		return domain.Trigger{}, nil, err
	}
	return trigger, sealed, nil
}

// Trigger reads one of an organization's triggers, whatever its status.
func (store *Store) Trigger(ctx context.Context, organizationID, id string) (domain.Trigger, error) {
	triggers, err := queryTriggers(ctx, store.pool, `
		SELECT `+triggerColumns+`
		FROM apps_triggers
		WHERE organization_id = $1 AND id = $2`, organizationID, id)
	if err != nil {
		return domain.Trigger{}, err
	}
	if len(triggers) == 0 {
		return domain.Trigger{}, domain.ErrNotFound
	}
	return triggers[0], nil
}

// TriggerByComposioID reads the live trigger a Composio instance backs, for
// a webhook delivery that names the instance rather than apps' own id.
func (store *Store) TriggerByComposioID(ctx context.Context, composioTriggerID string) (domain.Trigger, error) {
	triggers, err := queryTriggers(ctx, store.pool, `
		SELECT `+triggerColumns+`
		FROM apps_triggers
		WHERE composio_trigger_id = $1 AND status <> 'removed'`, composioTriggerID)
	if err != nil {
		return domain.Trigger{}, err
	}
	if len(triggers) == 0 {
		return domain.Trigger{}, domain.ErrNotFound
	}
	return triggers[0], nil
}

// Triggers lists an organization's live triggers, newest first, optionally
// narrowed to one member's or one connection's. IncludeRemoved also lists
// what was removed, for a caller that needs a connection's whole history -
// auto-subscribe reads it this way so it can tell a slug already live from
// one the member took away.
func (store *Store) Triggers(ctx context.Context, organizationID string, query application.TriggerQuery) ([]domain.Trigger, error) {
	sql := `SELECT ` + triggerColumns + ` FROM apps_triggers WHERE organization_id = $1`
	args := []any{organizationID}
	bind := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if !query.IncludeRemoved {
		sql += ` AND status <> 'removed'`
	}
	if query.MemberID != "" {
		sql += ` AND member_id = ` + bind(query.MemberID)
	}
	if query.ConnectionID != "" {
		sql += ` AND connection_id = ` + bind(query.ConnectionID)
	}
	sql += ` ORDER BY created_at DESC, id`
	return queryTriggers(ctx, store.pool, sql, args...)
}

// RemoveTrigger marks a trigger removed by itself, without touching the
// connection it is on. It answers false if it already was.
func (store *Store) RemoveTrigger(ctx context.Context, id string, now time.Time, event domain.Event) (bool, error) {
	removed := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE apps_triggers SET status = 'removed', updated_at = $2
			WHERE id = $1 AND status <> 'removed'`, id, now)
		if err != nil {
			return mapWriteError("remove trigger", err)
		}
		if tag.RowsAffected() != 1 {
			return nil
		}
		removed = true
		return insertEvents(ctx, tx, event)
	})
	return removed, err
}

// DisableTrigger marks an active trigger disabled and writes event. It
// answers false if the trigger was not active.
func (store *Store) DisableTrigger(ctx context.Context, id, reason string, now time.Time, event domain.Event) (bool, error) {
	disabled := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE apps_triggers
			SET status = 'disabled', status_reason = $2, updated_at = $3
			WHERE id = $1 AND status = 'active'`, id, reason, now)
		if err != nil {
			return mapWriteError("disable trigger", err)
		}
		if tag.RowsAffected() != 1 {
			return nil
		}
		disabled = true
		return insertEvents(ctx, tx, event)
	})
	return disabled, err
}

func queryTriggers(ctx context.Context, db querier, sql string, args ...any) ([]domain.Trigger, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapWriteError("query triggers", err)
	}
	defer rows.Close()
	triggers := []domain.Trigger{}
	for rows.Next() {
		trigger, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		triggers = append(triggers, trigger)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query triggers: %w", err)
	}
	return triggers, nil
}

// scanTrigger reads triggerColumns, then into extra whatever a query selects
// after them.
func scanTrigger(rows pgx.Rows, extra ...any) (domain.Trigger, error) {
	var trigger domain.Trigger
	var config []byte
	var status string
	var lastDelivery *time.Time
	if err := rows.Scan(append([]any{&trigger.ID, &trigger.OrganizationID, &trigger.MemberID, &trigger.ConnectionID,
		&trigger.Toolkit, &trigger.Slug, &config, &trigger.ComposioTriggerID, &trigger.PipedreamTriggerID, &status, &trigger.StatusReason,
		&trigger.Kind, &trigger.ManualSetup, &trigger.DeliveryCount, &lastDelivery,
		&trigger.CreatedAt, &trigger.UpdatedAt}, extra...)...); err != nil {
		return domain.Trigger{}, fmt.Errorf("scan trigger: %w", err)
	}
	trigger.Config = json.RawMessage(config)
	trigger.Status = domain.TriggerStatus(status)
	if lastDelivery != nil {
		delivered := lastDelivery.UTC()
		trigger.LastDeliveryAt = &delivered
	}
	trigger.CreatedAt = trigger.CreatedAt.UTC()
	trigger.UpdatedAt = trigger.UpdatedAt.UTC()
	return trigger, nil
}

// ownColumns is what a subscription through an integration carries: the same
// row as a Composio trigger, plus its broker, the cursor a poll got to and
// when it is next due. The columns are read here rather than through
// domain.Trigger, which is Composio's shape and has nowhere to put them.
const ownColumns = `id, organization_id, member_id, connection_id, toolkit, slug, config,
	status, status_reason, broker, cursor, next_poll_at, kind, created_at, updated_at`

// ownNames is the same list qualified with the table it is read from, which a
// claiming statement needs: its UPDATE joins another table that has an id of
// its own, so an unqualified name would be ambiguous.
const ownNames = `t.id, t.organization_id, t.member_id, t.connection_id, t.toolkit, t.slug, t.config,
	t.status, t.status_reason, t.broker, t.cursor, t.next_poll_at, t.kind, t.created_at, t.updated_at`

// CreateOwnTrigger records a subscription made through an integration, and
// writes event.
//
// It is CreateTrigger without a Composio instance: the instance id is empty,
// so the row is keyed on own_key instead, which is one per connection, event
// and config. The insert carries its own conflict check against that partial
// unique index, so two callers asking for the same subscription at once cannot
// both believe they made it: the loser reads back what the winner inserted,
// inside the same transaction.
//
// It also refuses to insert at all once the connection is no longer active,
// for the reason CreateTrigger does: an auto-subscribe pass racing a
// disconnect must not leave a row that looks live and that removal's own
// cleanup will never see again.
func (store *Store) CreateOwnTrigger(ctx context.Context, subscription application.OwnSubscription, event domain.Event) (application.OwnSubscription, bool, error) {
	var result application.OwnSubscription
	created := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM apps_connections WHERE id = $1 FOR SHARE`,
			subscription.ConnectionID).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("create trigger: the connection does not exist: %w", domain.ErrNotConnected)
			}
			return fmt.Errorf("create trigger: read the connection: %w", err)
		}
		if status != string(domain.ConnectionActive) {
			return fmt.Errorf("create trigger: the connection is %s: %w", status, domain.ErrNotConnected)
		}
		key := ownKeyOf(subscription)
		rows, err := queryOwnTriggers(ctx, tx, `
			INSERT INTO apps_triggers (
				id, organization_id, member_id, connection_id, toolkit, slug, config,
				composio_trigger_id, own_key, broker, cursor, status, status_reason, kind, manual_setup,
				next_poll_at, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, '', $8, 'oryxa', '', $9, '', $10, false, $11, $12, $12)
			ON CONFLICT (own_key) WHERE status <> 'removed' AND own_key <> '' DO NOTHING
			RETURNING `+ownColumns,
			subscription.ID, subscription.OrganizationID, subscription.MemberID, subscription.ConnectionID,
			subscription.Toolkit, subscription.Slug, []byte(subscription.Config), key,
			string(subscription.Status), subscription.Kind, subscription.NextPollAt, subscription.CreatedAt)
		if err != nil {
			return mapWriteError("create trigger", err)
		}
		if len(rows) == 1 {
			result, created = rows[0], true
			return insertEvents(ctx, tx, event)
		}
		existing, err := queryOwnTriggers(ctx, tx, `
			SELECT `+ownColumns+`
			FROM apps_triggers
			WHERE own_key = $1 AND status <> 'removed' AND own_key <> ''`, key)
		if err != nil {
			return err
		}
		if len(existing) == 0 {
			return fmt.Errorf("create trigger: the conflicting subscription vanished mid-transaction: %w", domain.ErrConflict)
		}
		result = existing[0]
		return nil
	})
	return result, created, err
}

// OwnSubscription reads one of an organization's subscriptions to an
// integration's event. It answers false, rather than ErrNotFound, when the
// trigger is Composio's: the caller is deciding which route a trigger is on,
// and ErrNotFound would be the wrong word for a trigger that is there.
func (store *Store) OwnSubscription(ctx context.Context, organizationID, id string) (application.OwnSubscription, bool, error) {
	rows, err := queryOwnTriggers(ctx, store.pool, `
		SELECT `+ownColumns+`
		FROM apps_triggers
		WHERE organization_id = $1 AND id = $2 AND broker = 'oryxa'`, organizationID, id)
	if err != nil {
		return application.OwnSubscription{}, false, err
	}
	if len(rows) == 0 {
		return application.OwnSubscription{}, false, nil
	}
	return rows[0], true, nil
}

// OwnSubscriptionsForEndpoint is every live subscription to trigger on one
// connection. It is a connection endpoint's whole reach: the endpoint belongs
// to that connection, so nothing else can be reached through it.
func (store *Store) OwnSubscriptionsForEndpoint(ctx context.Context, connectionID, organizationID, trigger string) ([]hooks.Subscription, error) {
	return store.ownSubscriptions(ctx, `
		SELECT t.id, t.connection_id, t.organization_id, t.member_id, t.toolkit, t.slug
		FROM apps_triggers t
		WHERE t.connection_id = $1 AND t.slug = $2 AND t.status = 'active' AND t.broker = 'oryxa'
		  AND ($3 = '' OR t.organization_id = $3)
		ORDER BY t.id`, connectionID, trigger, organizationID)
}

// OwnSubscriptionsForAccount is every live subscription to trigger on a
// toolkit's connections whose own account ref is the one a delivery names.
//
// The account ref is what the manifest's test call read out of the token
// response when the credential was saved, so it is the account the app itself
// calls the member's own. Routing on it rather than on the workspace is what
// keeps an event about a private channel reaching the member it was delivered
// for and nobody who could also see it.
func (store *Store) OwnSubscriptionsForAccount(ctx context.Context, toolkit, organizationID, accountRef, trigger string) ([]hooks.Subscription, error) {
	return store.ownSubscriptions(ctx, `
		SELECT t.id, t.connection_id, t.organization_id, t.member_id, t.toolkit, t.slug
		FROM apps_triggers t
		JOIN apps_connections c ON c.id = t.connection_id
		JOIN apps_credentials k ON k.connection_id = t.connection_id
		WHERE t.toolkit = $1 AND t.slug = $2 AND t.status = 'active' AND t.broker = 'oryxa'
		  AND c.status = 'active' AND c.broker = 'oryxa'
		  AND k.settings ->> 'account.ref' = $3
		  AND ($4 = '' OR t.organization_id = $4)
		ORDER BY t.id`, toolkit, trigger, accountRef, organizationID)
}

func (store *Store) ownSubscriptions(ctx context.Context, sql string, args ...any) ([]hooks.Subscription, error) {
	rows, err := store.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapWriteError("query own subscriptions", err)
	}
	defer rows.Close()
	subscriptions := []hooks.Subscription{}
	for rows.Next() {
		var subscription hooks.Subscription
		if err := rows.Scan(&subscription.TriggerID, &subscription.ConnectionID, &subscription.OrganizationID,
			&subscription.MemberID, &subscription.Toolkit, &subscription.Trigger); err != nil {
			return nil, fmt.Errorf("scan own subscription: %w", err)
		}
		subscriptions = append(subscriptions, subscription)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query own subscriptions: %w", err)
	}
	return subscriptions, nil
}

// accountRefSetting is the setting signin kept the account's own id in, which
// is the JSON pointer the manifest's test call read out of its answer. It is
// how a delivery is routed to the connections of the account it is about.
const accountRefSetting = "account.ref"

// ClaimPolls claims the polled subscriptions of integrations' triggers that
// are due, oldest first, by moving their next poll to claimUntil. The lease
// is the same one the refresh lease and the auto-subscribe claim are: FOR
// UPDATE SKIP LOCKED keeps two replicas claiming at once apart, and the moved
// time keeps them apart once the claim has committed.
func (store *Store) ClaimPolls(ctx context.Context, now, claimUntil time.Time, limit int) ([]application.OwnSubscription, error) {
	return queryOwnTriggers(ctx, store.pool, `
		WITH due AS (
			SELECT waiting.id, waiting.next_poll_at AS due_at
			FROM apps_triggers waiting
			JOIN apps_connections owner ON owner.id = waiting.connection_id
			WHERE waiting.status = 'active' AND waiting.broker = 'oryxa' AND waiting.kind = 'poll'
			  AND waiting.next_poll_at <= $1 AND owner.status = 'active'
			ORDER BY waiting.next_poll_at, waiting.id
			LIMIT $3
			FOR UPDATE OF waiting SKIP LOCKED
		), claimed AS (
			UPDATE apps_triggers t
			SET next_poll_at = $2, poll_claimed_until = $2, updated_at = $2
			FROM due
			WHERE t.id = due.id
			RETURNING due.due_at, `+ownNames+`
		)
		SELECT `+ownColumns+` FROM claimed ORDER BY due_at, id`, now, claimUntil, limit)
}

// SavePoll records where a polled subscription's last poll got to, and when it
// is next due. It answers false when the subscription is no longer live, so a
// poll that raced an unsubscribe publishes nothing further.
func (store *Store) SavePoll(ctx context.Context, id, cursor string, next time.Time) error {
	if _, err := store.pool.Exec(ctx, `
		UPDATE apps_triggers
		SET cursor = $2, next_poll_at = $3, poll_claimed_until = NULL, updated_at = $4
		WHERE id = $1 AND status <> 'removed'`, id, cursor, next, time.Now().UTC()); err != nil {
		return mapWriteError("save a poll", err)
	}
	return nil
}

// ownKeyOf is the key one live subscription per connection, event and config
// is kept under. It is the same thing Composio keys its instances on, and the
// application builds it, so both sides agree on what "the same subscription"
// means.
func ownKeyOf(subscription application.OwnSubscription) string {
	return application.OwnKey(subscription.ConnectionID, subscription.Slug, subscription.Config)
}

func queryOwnTriggers(ctx context.Context, db querier, sql string, args ...any) ([]application.OwnSubscription, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapWriteError("query own triggers", err)
	}
	defer rows.Close()
	subscriptions := []application.OwnSubscription{}
	for rows.Next() {
		subscription, err := scanOwnTrigger(rows)
		if err != nil {
			return nil, err
		}
		subscriptions = append(subscriptions, subscription)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query own triggers: %w", err)
	}
	return subscriptions, nil
}

func scanOwnTrigger(rows pgx.Rows) (application.OwnSubscription, error) {
	var subscription application.OwnSubscription
	var config []byte
	var status string
	if err := rows.Scan(&subscription.ID, &subscription.OrganizationID, &subscription.MemberID,
		&subscription.ConnectionID, &subscription.Toolkit, &subscription.Slug, &config, &status,
		&subscription.StatusReason, &subscription.Broker, &subscription.Cursor, &subscription.NextPollAt,
		&subscription.Kind, &subscription.CreatedAt, &subscription.UpdatedAt); err != nil {
		return application.OwnSubscription{}, fmt.Errorf("scan own trigger: %w", err)
	}
	subscription.Config = json.RawMessage(config)
	subscription.Status = domain.TriggerStatus(status)
	subscription.CreatedAt = subscription.CreatedAt.UTC()
	subscription.UpdatedAt = subscription.UpdatedAt.UTC()
	if subscription.NextPollAt != nil {
		due := subscription.NextPollAt.UTC()
		subscription.NextPollAt = &due
	}
	return subscription, nil
}
