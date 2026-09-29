package guardpolicy

import (
	"context"
	"embed"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrationsAdvisoryLock keeps replicas that start together from installing
// the tables at once. Every agents/primary package takes its own number from
// the 72635107xx range; the highest taken before this one was agentteamrooms'
// 7263510717.
const migrationsAdvisoryLock int64 = 7263510718

// store keeps what members chose. postgresStore is the one that is shipped;
// tests of the rule stand in for it.
type store interface {
	migrate(ctx context.Context) error
	// get reads what a member chose; nothing chosen is the zero Choices.
	get(ctx context.Context, organizationID, userID string) (Choices, error)
	// set changes what a member chose and answers with what they have chosen
	// now. A nil level leaves that origin as it is; a level that is "" clears
	// it. Every level it changes is logged in the same transaction.
	set(ctx context.Context, organizationID, userID string, owner, peer *Level) (Choices, error)
	// forget removes what is kept for a member of organizationID, or for the
	// whole organization when memberID is "".
	forget(ctx context.Context, organizationID, memberID string) error
}

// postgresStore is the PostgreSQL-backed store. Its tables are named with
// their schema, so the pool's search_path does not matter.
type postgresStore struct {
	database *postgreskit.Database
}

// migrate installs the tables. Running it twice is harmless.
func (store *postgresStore) migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "guard-policy", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

func (store *postgresStore) get(ctx context.Context, organizationID, userID string) (Choices, error) {
	var owner, peer *string
	err := store.database.Pool.QueryRow(ctx, `
		SELECT owner_level, peer_level FROM agents_primary.guard_preferences
		WHERE organization_id = $1 AND user_id = $2`, organizationID, userID).Scan(&owner, &peer)
	if errors.Is(err, pgx.ErrNoRows) {
		return Choices{}, nil
	}
	if err != nil {
		return Choices{}, fmt.Errorf("read guard preferences: %w", err)
	}
	return Choices{Owner: levelOf(owner), Peer: levelOf(peer)}, nil
}

func (store *postgresStore) set(ctx context.Context, organizationID, userID string, owner, peer *Level) (Choices, error) {
	var after Choices
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		// The member's row is made if it is not there, and locked either way,
		// so that two changes of one member's choices take their turns and
		// each logs the levels it really replaced: the second statement blocks,
		// resolving the conflict, until the first commits, as rooms' sequence
		// counter relies on. The update changes nothing; it is what takes the
		// lock, and what lets RETURNING read the levels as the last change to
		// commit left them.
		var storedOwner, storedPeer *string
		if err := tx.QueryRow(ctx, `
			INSERT INTO agents_primary.guard_preferences (organization_id, user_id, updated_at)
			VALUES ($1, $2, now())
			ON CONFLICT (organization_id, user_id) DO UPDATE SET updated_at = guard_preferences.updated_at
			RETURNING owner_level, peer_level`,
			organizationID, userID).Scan(&storedOwner, &storedPeer); err != nil {
			return fmt.Errorf("lock guard preferences: %w", err)
		}
		before := Choices{Owner: levelOf(storedOwner), Peer: levelOf(storedPeer)}
		after = before
		if owner != nil {
			after.Owner = *owner
		}
		if peer != nil {
			after.Peer = *peer
		}
		if after == before {
			return nil
		}
		if _, err := tx.Exec(ctx, `
			UPDATE agents_primary.guard_preferences
			SET owner_level = $3, peer_level = $4, updated_at = now()
			WHERE organization_id = $1 AND user_id = $2`,
			organizationID, userID, storedLevel(after.Owner), storedLevel(after.Peer)); err != nil {
			return fmt.Errorf("record guard preferences: %w", err)
		}
		for _, change := range []struct {
			origin   Origin
			from, to Level
		}{
			{OriginOwner, before.Owner, after.Owner},
			{OriginPeer, before.Peer, after.Peer},
		} {
			if change.from == change.to {
				continue
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO agents_primary.guard_preference_changes (organization_id, user_id, field, "old", "new")
				VALUES ($1, $2, $3, $4, $5)`,
				organizationID, userID, string(change.origin), storedLevel(change.from), storedLevel(change.to)); err != nil {
				return fmt.Errorf("log guard preference change: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return Choices{}, err
	}
	return after, nil
}

func (store *postgresStore) forget(ctx context.Context, organizationID, memberID string) error {
	return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			DELETE FROM agents_primary.guard_preferences
			WHERE organization_id = $1 AND ($2 = '' OR user_id = $2)`, organizationID, memberID); err != nil {
			return fmt.Errorf("forget guard preferences: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM agents_primary.guard_preference_changes
			WHERE organization_id = $1 AND ($2 = '' OR user_id = $2)`, organizationID, memberID); err != nil {
			return fmt.Errorf("forget guard preference changes: %w", err)
		}
		return nil
	})
}

// levelOf is a stored level: null is no choice.
func levelOf(stored *string) Level {
	if stored == nil {
		return ""
	}
	return Level(*stored)
}

// storedLevel is a level as it is stored: no choice is null.
func storedLevel(level Level) *string {
	if level == "" {
		return nil
	}
	text := string(level)
	return &text
}
