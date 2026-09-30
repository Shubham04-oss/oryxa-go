// Package secretshares keeps the one-time secrets an owner shares with the
// member who asked, and is where they are opened once.
//
// When the answer to a Slack request is a credential, it never goes through
// Slack, an event, the ledger or a model. Its owner types it into Oryxa; the
// member who asked opens it once, in their own session; and it is deleted when
// they do, when its owner revokes it, or when its hour is up. Store implements
// httpserver.SecretShares for that, and trusts the share it is handed: the
// ledger (interactions.Service's AuthorizeSecret* methods) has already said who
// may do what to the secret of which request, and the store checks nothing more
// of who.
//
// What the store guarantees is what the ledger cannot:
//
//   - A value is at rest only sealed. Each secret is envelope-encrypted with a
//     fresh KMS data key from the agents-secrets key, bound to its request
//     (encryption context service=oryxa-agents, and the request). The table
//     holds ciphertext, the wrapped data key and who and when, never the value,
//     and nothing here logs, formats or returns a value but View, to the member
//     who asked, once.
//   - A secret is opened once. View deletes the row and records the view on the
//     request (interactions.RecordSecretViewed) in one transaction, so of two
//     views at once one wins, a secret is never opened without its record, nor
//     recorded without having been opened, and a view that fails, for a KMS
//     error or a record that could not be made, leaves the secret to be opened
//     again.
//   - A secret lives an hour at most, whatever the caller asks for: Put refuses
//     a longer lifetime and the table's own check refuses it too. One that has
//     expired is refused, and deleted, by whichever call meets it, and Run's
//     sweeper deletes the rest.
package secretshares

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrationsAdvisoryLock is this store's own, from the 72635107xx range the
// agents' stores share; the highest taken before it was surfaces' 7263510720.
const migrationsAdvisoryLock int64 = 7263510721

// Store keeps the sealed secrets in PostgreSQL.
type Store struct {
	database *postgreskit.Database
	sealer   Sealer
	logger   *slog.Logger

	// now is the clock, and recordViewed what records a view in the
	// transaction that deletes the secret: fields, so a test can move the one
	// and make the other fail.
	now          func() time.Time
	recordViewed func(ctx context.Context, tx pgx.Tx, requestID, viewerUserID string, at time.Time) error
}

// New returns a store that keeps its secrets in database, sealed with sealer.
func New(database *postgreskit.Database, sealer Sealer, logger *slog.Logger) (*Store, error) {
	if database == nil || sealer == nil {
		return nil, errors.New("one-time secrets need a database and a sealer")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Store{
		database: database, sealer: sealer, logger: logger,
		now: time.Now, recordViewed: interactions.RecordSecretViewed,
	}, nil
}

// Migrate creates the table. It is safe to repeat, and to run from replicas
// that start together.
func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "agents-secret-shares", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

// Put seals value for the request the share names and keeps it, replacing any
// secret already shared for that request, until ttl from now. A lifetime over
// interactions.MaxSecretTTL, or none, is refused, as is a value that is empty
// or too long: the ledger has bounded them already, and this does not rely on
// it.
func (store *Store) Put(ctx context.Context, share interactions.SecretShare, value string, ttl time.Duration) (interactions.SecretMeta, error) {
	if err := checkShare(share); err != nil {
		return interactions.SecretMeta{}, err
	}
	if strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > interactions.MaxSecretRunes {
		return interactions.SecretMeta{}, fmt.Errorf("%w: a secret's value is required and holds at most %d characters", interactions.ErrInvalid, interactions.MaxSecretRunes)
	}
	if ttl <= 0 || ttl > interactions.MaxSecretTTL {
		return interactions.SecretMeta{}, fmt.Errorf("%w: a secret lives for %s at most", interactions.ErrInvalid, interactions.MaxSecretTTL)
	}

	plaintext := []byte(value)
	sealed, err := store.sealer.Seal(ctx, bindingOf(share.RequestID), plaintext)
	secrets.Wipe(plaintext)
	if err != nil {
		return interactions.SecretMeta{}, fmt.Errorf("seal a secret: %w", err)
	}
	encoded, err := json.Marshal(sealed)
	if err != nil {
		return interactions.SecretMeta{}, fmt.Errorf("encode a sealed secret: %w", err)
	}

	createdAt := store.now().UTC().Truncate(time.Microsecond)
	expiresAt := createdAt.Add(ttl)
	if _, err := store.database.Pool.Exec(ctx, `
		INSERT INTO agents_primary.secret_shares
			(request_id, organization_id, owner_user_id, asker_user_id, sealed, created_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (request_id) DO UPDATE SET
			organization_id=EXCLUDED.organization_id, owner_user_id=EXCLUDED.owner_user_id,
			asker_user_id=EXCLUDED.asker_user_id, sealed=EXCLUDED.sealed,
			created_at=EXCLUDED.created_at, expires_at=EXCLUDED.expires_at`,
		share.RequestID, share.OrganizationID, share.OwnerUserID, share.AskerUserID, encoded, createdAt, expiresAt); err != nil {
		return interactions.SecretMeta{}, fmt.Errorf("keep a secret: %w", err)
	}
	return interactions.SecretMeta{ExpiresAt: expiresAt}, nil
}

// View opens the secret once: it deletes it and records the view on the
// request, in one transaction, and returns the value only when that commits.
// interactions.ErrSecretGone is a secret that was never shared, or was opened,
// revoked or has expired.
//
// The row is deleted first, so a second view at the same moment waits for the
// first and then finds nothing. The value is opened with KMS after that, inside
// the transaction: if KMS fails, or the view cannot be recorded, the
// transaction rolls back and the secret is still there to be opened again.
func (store *Store) View(ctx context.Context, share interactions.SecretShare) (interactions.SecretValue, error) {
	if err := checkShare(share); err != nil {
		return interactions.SecretValue{}, err
	}
	var (
		opened  interactions.SecretValue
		expired bool
	)
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		at := store.now().UTC().Truncate(time.Microsecond)
		var (
			encoded   []byte
			expiresAt time.Time
		)
		err := tx.QueryRow(ctx, `
			DELETE FROM agents_primary.secret_shares
			WHERE request_id=$1 AND organization_id=$2 AND asker_user_id=$3
			RETURNING sealed, expires_at`,
			share.RequestID, share.OrganizationID, share.AskerUserID).Scan(&encoded, &expiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return interactions.ErrSecretGone
		}
		if err != nil {
			return fmt.Errorf("take a secret: %w", err)
		}
		if !expiresAt.After(at) {
			// Its time is up. It is not opened, and the delete stands, so
			// it is not kept a moment longer.
			expired = true
			return nil
		}
		var sealed secrets.SealedData
		if err := json.Unmarshal(encoded, &sealed); err != nil {
			return fmt.Errorf("read a sealed secret: %w", err)
		}
		plaintext, err := store.sealer.Open(ctx, bindingOf(share.RequestID), sealed)
		if err != nil {
			return fmt.Errorf("open a sealed secret: %w", err)
		}
		value := string(plaintext)
		secrets.Wipe(plaintext)
		if err := store.recordViewed(ctx, tx, share.RequestID, share.AskerUserID, at); err != nil {
			return fmt.Errorf("record that a secret was opened: %w", err)
		}
		opened = interactions.SecretValue{Value: value, ExpiresAt: expiresAt.UTC()}
		return nil
	})
	switch {
	case err != nil:
		return interactions.SecretValue{}, err
	case expired:
		return interactions.SecretValue{}, interactions.ErrSecretGone
	}
	return opened, nil
}

// Revoke deletes the secret whether or not anyone opened it, for the member
// who shared it. There being none to revoke, because it was opened or revoked,
// or expired (which is deleted all the same), is interactions.ErrSecretGone.
func (store *Store) Revoke(ctx context.Context, share interactions.SecretShare) error {
	if err := checkShare(share); err != nil {
		return err
	}
	var expiresAt time.Time
	err := store.database.Pool.QueryRow(ctx, `
		DELETE FROM agents_primary.secret_shares
		WHERE request_id=$1 AND organization_id=$2 AND owner_user_id=$3
		RETURNING expires_at`,
		share.RequestID, share.OrganizationID, share.OwnerUserID).Scan(&expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return interactions.ErrSecretGone
	}
	if err != nil {
		return fmt.Errorf("revoke a secret: %w", err)
	}
	if !expiresAt.After(store.now()) {
		return interactions.ErrSecretGone
	}
	return nil
}

// Sweep deletes up to limit secrets whose time is up, oldest first, and says
// how many it deleted. A secret being opened at that moment is left to the view
// that has it.
func (store *Store) Sweep(ctx context.Context, limit int) (int, error) {
	if limit < 1 {
		return 0, errors.New("sweeping secrets needs a positive limit")
	}
	tag, err := store.database.Pool.Exec(ctx, `
		DELETE FROM agents_primary.secret_shares
		WHERE request_id IN (
			SELECT request_id FROM agents_primary.secret_shares
			WHERE expires_at <= $1
			ORDER BY expires_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED)`, store.now().UTC(), limit)
	if err != nil {
		return 0, fmt.Errorf("sweep expired secrets: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// Forget deletes the secrets kept for a member of organizationID, whether they
// shared them or were to open them, or for the whole organization when userID
// is "": for a member who left, or an organization that is gone. It has the
// shape of inbound.Forgetter, and needs no request to find what it removes.
func (store *Store) Forget(ctx context.Context, organizationID, userID string) error {
	if strings.TrimSpace(organizationID) == "" {
		return errors.New("forgetting secrets needs an organization")
	}
	statement := `DELETE FROM agents_primary.secret_shares WHERE organization_id=$1`
	arguments := []any{organizationID}
	if userID != "" {
		statement += ` AND (owner_user_id=$2 OR asker_user_id=$2)`
		arguments = append(arguments, userID)
	}
	if _, err := store.database.Pool.Exec(ctx, statement, arguments...); err != nil {
		return fmt.Errorf("forget secrets: %w", err)
	}
	return nil
}

// checkShare refuses a share that does not name what the store keeps a secret
// under. The ledger always builds a whole one, so this is a mistake in the
// caller, not in the request, and is no 400.
func checkShare(share interactions.SecretShare) error {
	if share.RequestID == "" || share.OrganizationID == "" || share.OwnerUserID == "" || share.AskerUserID == "" {
		return errors.New("a secret's share names its request, its organization, the member who shares it and the member who opens it")
	}
	return nil
}
