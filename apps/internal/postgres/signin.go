package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/signin"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// Store keeps signin's state too: its flows, credentials and OAuth clients.
var _ signin.Store = (*Store)(nil)

// flowRetention is how long a spent or expired flow is kept after it
// expired, before a later flow's insert forgets it.
const flowRetention = 24 * time.Hour

const flowColumns = `state_hash, organization_id, member_id, connection_id, toolkit, oauth_client_id,
	code_verifier, redirect_uri, return_to, settings, sealed, expires_at`

// credentialColumns are read from apps_credentials c joined to its
// connection n.
const credentialColumns = `c.connection_id, n.organization_id, n.member_id, n.toolkit, n.status, c.kind,
	COALESCE(c.oauth_client_id, '') AS oauth_client_id, c.sealed, c.settings, c.scopes, c.expires_at, c.version,
	c.next_check_at, c.check_failures, c.updated_at`

// credentialNames are credentialColumns' names, to read them back out of a
// CTE.
const credentialNames = `connection_id, organization_id, member_id, toolkit, status, kind, oauth_client_id, sealed,
	settings, scopes, expires_at, version, next_check_at, check_failures, updated_at`

const oauthClientColumns = `id, organization_id, client, client_id, sealed_secret, settings, sealed_fields,
	status, created_by, created_at, updated_at`

// InsertSigninFlow records an OAuth sign-in in flight, and forgets the flows
// that expired a day or more before this one began.
func (store *Store) InsertSigninFlow(ctx context.Context, flow signin.Flow) error {
	settings, err := jsonObject(flow.Settings)
	if err != nil {
		return err
	}
	sealed, err := jsonObject(flow.Sealed)
	if err != nil {
		return err
	}
	cutoff := flow.ExpiresAt.Add(-signin.FlowLifetime - flowRetention)
	if _, err := store.pool.Exec(ctx, `
		WITH forgotten AS (
			DELETE FROM apps_signin_flows WHERE expires_at < $13
		)
		INSERT INTO apps_signin_flows (
			state_hash, organization_id, member_id, connection_id, toolkit, oauth_client_id,
			code_verifier, redirect_uri, return_to, settings, sealed, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		flow.StateHash, flow.OrganizationID, flow.MemberID, flow.ConnectionID, flow.Toolkit, flow.OAuthClientID,
		flow.CodeVerifier, flow.RedirectURI, flow.ReturnTo, settings, sealed, flow.ExpiresAt, cutoff,
	); err != nil {
		return mapWriteError("insert sign-in flow", err)
	}
	return nil
}

// ConsumeSigninFlow spends a flow and answers it in one statement: of two
// callbacks racing on one state, only one finds it.
func (store *Store) ConsumeSigninFlow(ctx context.Context, stateHash string, now time.Time) (signin.Flow, error) {
	flow, err := scanFlow(store.pool.QueryRow(ctx, `
		UPDATE apps_signin_flows SET consumed_at = $2
		WHERE state_hash = $1 AND consumed_at IS NULL AND expires_at > $2
		RETURNING `+flowColumns, stateHash, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return signin.Flow{}, signin.ErrFlowNotFound
	}
	if err != nil {
		return signin.Flow{}, fmt.Errorf("consume sign-in flow: %w", err)
	}
	return flow, nil
}

// Credential reads a connection's credential with its connection.
func (store *Store) Credential(ctx context.Context, connectionID string) (signin.Credential, error) {
	credentials, err := queryCredentials(ctx, store.pool, `
		SELECT `+credentialColumns+`
		FROM apps_credentials c JOIN apps_connections n ON n.id = c.connection_id
		WHERE c.connection_id = $1`, connectionID)
	if err != nil {
		return signin.Credential{}, err
	}
	if len(credentials) == 0 {
		return signin.Credential{}, signin.ErrNoCredential
	}
	return credentials[0], nil
}

// InsertCredential keeps a finished OAuth sign-in's credential at version 1,
// only for a pending connection of its organization that holds none.
func (store *Store) InsertCredential(ctx context.Context, credential signin.Credential) error {
	arguments, err := credentialArguments(credential)
	if err != nil {
		return err
	}
	tag, err := store.pool.Exec(ctx, `
		INSERT INTO apps_credentials (
			connection_id, kind, oauth_client_id, sealed, settings, scopes, expires_at,
			version, next_check_at, check_failures, updated_at
		)
		SELECT $1::text, $2::text, $3::text, $4::jsonb, $5::jsonb, $6::text[], $7::timestamptz, 1, $8::timestamptz, 0, $9::timestamptz
		WHERE EXISTS (
			SELECT 1 FROM apps_connections WHERE id = $1 AND organization_id = $10 AND status = 'pending'
		)
		ON CONFLICT (connection_id) DO NOTHING`, arguments...)
	if err != nil {
		return mapWriteError("insert credential", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("insert credential: the connection is not pending, or already holds one: %w", domain.ErrConflict)
	}
	return nil
}

// ReplaceCredential keeps a key, a password or a client's credentials for a
// pending or active connection of its organization: as its first, or over
// one of the same kind, whose version it moves on.
func (store *Store) ReplaceCredential(ctx context.Context, credential signin.Credential) (int64, error) {
	arguments, err := credentialArguments(credential)
	if err != nil {
		return 0, err
	}
	var version int64
	err = store.pool.QueryRow(ctx, `
		INSERT INTO apps_credentials (
			connection_id, kind, oauth_client_id, sealed, settings, scopes, expires_at,
			version, next_check_at, check_failures, updated_at
		)
		SELECT $1::text, $2::text, $3::text, $4::jsonb, $5::jsonb, $6::text[], $7::timestamptz, 1, $8::timestamptz, 0, $9::timestamptz
		WHERE EXISTS (
			SELECT 1 FROM apps_connections
			WHERE id = $1 AND organization_id = $10 AND status IN ('pending', 'active')
		)
		ON CONFLICT (connection_id) DO UPDATE SET
			oauth_client_id = EXCLUDED.oauth_client_id, sealed = EXCLUDED.sealed, settings = EXCLUDED.settings,
			scopes = EXCLUDED.scopes, expires_at = EXCLUDED.expires_at, version = apps_credentials.version + 1,
			lease_token = NULL, lease_until = NULL, next_check_at = EXCLUDED.next_check_at, check_failures = 0,
			updated_at = EXCLUDED.updated_at
		WHERE apps_credentials.kind = EXCLUDED.kind
		RETURNING version`, arguments...).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("replace credential: the connection is not pending or active, or holds another kind: %w", domain.ErrConflict)
	}
	if err != nil {
		return 0, mapWriteError("replace credential", err)
	}
	return version, nil
}

// credentialArguments are InsertCredential's and ReplaceCredential's
// arguments, in their order.
func credentialArguments(credential signin.Credential) ([]any, error) {
	sealed, err := jsonObject(credential.Sealed)
	if err != nil {
		return nil, err
	}
	settings, err := jsonObject(credential.Settings)
	if err != nil {
		return nil, err
	}
	return []any{
		credential.ConnectionID, credential.Kind, nullable(credential.OAuthClientID), sealed, settings,
		nonNil(credential.Scopes), credential.ExpiresAt, credential.NextCheckAt, credential.UpdatedAt,
		credential.OrganizationID,
	}, nil
}

// AcquireCredentialLease takes the refresh lease on a credential still at
// version, unless another holds it past now.
func (store *Store) AcquireCredentialLease(ctx context.Context, connectionID string, version int64, token string, now, until time.Time) (bool, error) {
	tag, err := store.pool.Exec(ctx, `
		UPDATE apps_credentials SET lease_token = $3, lease_until = $5
		WHERE connection_id = $1 AND version = $2 AND (lease_until IS NULL OR lease_until <= $4)`,
		connectionID, version, token, now, until)
	if err != nil {
		return false, fmt.Errorf("acquire credential lease: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// SaveRefreshedCredential saves a refresh by compare-and-swap on the
// version, under the lease, and releases the lease.
func (store *Store) SaveRefreshedCredential(ctx context.Context, connectionID, token string, version int64, refreshed signin.Refreshed) (int64, error) {
	sealed, err := jsonObject(refreshed.Sealed)
	if err != nil {
		return 0, err
	}
	settings, err := jsonObject(refreshed.Settings)
	if err != nil {
		return 0, err
	}
	var saved int64
	err = store.pool.QueryRow(ctx, `
		UPDATE apps_credentials
		SET sealed = $4, settings = $5, scopes = $6, expires_at = $7, version = version + 1,
			lease_token = NULL, lease_until = NULL, updated_at = $8
		WHERE connection_id = $1 AND lease_token = $2 AND version = $3
		RETURNING version`,
		connectionID, token, version, sealed, settings, nonNil(refreshed.Scopes), refreshed.ExpiresAt, refreshed.UpdatedAt,
	).Scan(&saved)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("save refreshed credential: it moved on: %w", domain.ErrConflict)
	}
	if err != nil {
		return 0, mapWriteError("save refreshed credential", err)
	}
	return saved, nil
}

// ReleaseCredentialLease clears the lease if token still holds it.
func (store *Store) ReleaseCredentialLease(ctx context.Context, connectionID, token string) error {
	if _, err := store.pool.Exec(ctx, `
		UPDATE apps_credentials SET lease_token = NULL, lease_until = NULL
		WHERE connection_id = $1 AND lease_token = $2`, connectionID, token); err != nil {
		return fmt.Errorf("release credential lease: %w", err)
	}
	return nil
}

// DeleteCredential forgets a connection's credential.
func (store *Store) DeleteCredential(ctx context.Context, connectionID string) (bool, error) {
	tag, err := store.pool.Exec(ctx, `DELETE FROM apps_credentials WHERE connection_id = $1`, connectionID)
	if err != nil {
		return false, fmt.Errorf("delete credential: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// MarkCredentialUsed puts off an active connection's next check to at least
// notBefore, and forgets its failed checks.
func (store *Store) MarkCredentialUsed(ctx context.Context, connectionID string, notBefore time.Time) error {
	if _, err := store.pool.Exec(ctx, `
		UPDATE apps_credentials c
		SET next_check_at = GREATEST(c.next_check_at, $2), check_failures = 0
		FROM apps_connections n
		WHERE c.connection_id = $1 AND n.id = c.connection_id AND n.status = 'active'`, connectionID, notBefore); err != nil {
		return fmt.Errorf("mark credential used: %w", err)
	}
	return nil
}

// ClaimCredentialChecks claims active connections' due checks.
func (store *Store) ClaimCredentialChecks(ctx context.Context, now, claimUntil time.Time, limit int) ([]signin.Credential, error) {
	return store.claimCredentials(ctx, now, claimUntil, limit, []string{string(domain.ConnectionActive)})
}

// ClaimRevocations claims ended connections' due revocations.
func (store *Store) ClaimRevocations(ctx context.Context, now, claimUntil time.Time, limit int) ([]signin.Credential, error) {
	return store.claimCredentials(ctx, now, claimUntil, limit,
		[]string{string(domain.ConnectionRemoved), string(domain.ConnectionFailed)})
}

// claimCredentials claims, oldest first, credentials of connections in one
// of statuses whose next check is due at now, by moving it to claimUntil.
// FOR UPDATE SKIP LOCKED keeps two replicas claiming at once apart, and the
// moved time keeps them apart afterwards.
func (store *Store) claimCredentials(ctx context.Context, now, claimUntil time.Time, limit int, statuses []string) ([]signin.Credential, error) {
	return queryCredentials(ctx, store.pool, `
		WITH due AS (
			SELECT waiting.connection_id, waiting.next_check_at AS due_at
			FROM apps_credentials waiting JOIN apps_connections owner ON owner.id = waiting.connection_id
			WHERE owner.status = ANY($4) AND waiting.next_check_at <= $1
			ORDER BY waiting.next_check_at, waiting.connection_id
			LIMIT $3
			FOR UPDATE OF waiting SKIP LOCKED
		), claimed AS (
			UPDATE apps_credentials c SET next_check_at = $2
			FROM due, apps_connections n
			WHERE c.connection_id = due.connection_id AND n.id = c.connection_id
			RETURNING due.due_at, `+credentialColumns+`
		)
		SELECT `+credentialNames+` FROM claimed ORDER BY due_at, connection_id`, now, claimUntil, limit, statuses)
}

// RecordCredentialCheck schedules a credential's next check or revocation
// attempt.
func (store *Store) RecordCredentialCheck(ctx context.Context, connectionID string, next time.Time, failures int) error {
	if _, err := store.pool.Exec(ctx, `
		UPDATE apps_credentials SET next_check_at = $2, check_failures = $3
		WHERE connection_id = $1`, connectionID, next, failures); err != nil {
		return fmt.Errorf("record credential check: %w", err)
	}
	return nil
}

// GrantShared says whether another live connection, in any organization,
// holds a credential from a client with clientID for the account labelled
// label: an app that revokes a whole grant would take its token too.
func (store *Store) GrantShared(ctx context.Context, connectionID, clientID, label string) (bool, error) {
	var shared bool
	if err := store.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM apps_credentials c
			JOIN apps_connections n ON n.id = c.connection_id
			JOIN apps_oauth_clients o ON o.id = c.oauth_client_id
			WHERE c.connection_id <> $1 AND n.status IN ('pending', 'active')
				AND o.client_id = $2 AND c.settings ->> 'account.label' = $3
		)`, connectionID, clientID, label).Scan(&shared); err != nil {
		return false, fmt.Errorf("look for a shared grant: %w", err)
	}
	return shared, nil
}

// LiveOAuthClient reads an organization's live client for a registration.
func (store *Store) LiveOAuthClient(ctx context.Context, organizationID, client string) (signin.OAuthClient, error) {
	return store.oauthClient(ctx, `
		SELECT `+oauthClientColumns+` FROM apps_oauth_clients
		WHERE organization_id = $1 AND client = $2 AND status = 'active'`, organizationID, client)
}

// OAuthClient reads a client by id, live or removed.
func (store *Store) OAuthClient(ctx context.Context, id string) (signin.OAuthClient, error) {
	return store.oauthClient(ctx, `SELECT `+oauthClientColumns+` FROM apps_oauth_clients WHERE id = $1`, id)
}

func (store *Store) oauthClient(ctx context.Context, sql string, args ...any) (signin.OAuthClient, error) {
	client, err := scanOAuthClient(store.pool.QueryRow(ctx, sql, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return signin.OAuthClient{}, signin.ErrNoClient
	}
	if err != nil {
		return signin.OAuthClient{}, fmt.Errorf("read OAuth client: %w", err)
	}
	return client, nil
}

// InsertOAuthClient makes a client its organization's live one for its
// registration, retiring the one before it in the same transaction. Two
// racing writes cannot both make one live: the unique index refuses the
// second.
func (store *Store) InsertOAuthClient(ctx context.Context, client signin.OAuthClient) error {
	secret, err := jsonNullable(client.SealedSecret)
	if err != nil {
		return err
	}
	settings, err := jsonObject(client.Settings)
	if err != nil {
		return err
	}
	fields, err := jsonObject(client.SealedFields)
	if err != nil {
		return err
	}
	return store.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE apps_oauth_clients SET status = 'removed', updated_at = $3
			WHERE organization_id = $1 AND client = $2 AND status = 'active'`,
			client.OrganizationID, client.Client, client.UpdatedAt); err != nil {
			return mapWriteError("retire OAuth client", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO apps_oauth_clients (
				id, organization_id, client, client_id, sealed_secret, settings, sealed_fields,
				status, created_by, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, 'active', $8, $9, $10)`,
			client.ID, client.OrganizationID, client.Client, client.ClientID, secret, settings, fields,
			client.CreatedBy, client.CreatedAt, client.UpdatedAt); err != nil {
			return mapWriteError("insert OAuth client", err)
		}
		return nil
	})
}

// UpdateOAuthClient rewrites a live client's secret, settings and sealed
// fields in place.
func (store *Store) UpdateOAuthClient(ctx context.Context, client signin.OAuthClient) (bool, error) {
	secret, err := jsonNullable(client.SealedSecret)
	if err != nil {
		return false, err
	}
	settings, err := jsonObject(client.Settings)
	if err != nil {
		return false, err
	}
	fields, err := jsonObject(client.SealedFields)
	if err != nil {
		return false, err
	}
	tag, err := store.pool.Exec(ctx, `
		UPDATE apps_oauth_clients SET sealed_secret = $2, settings = $3, sealed_fields = $4, updated_at = $5
		WHERE id = $1 AND status = 'active'`, client.ID, secret, settings, fields, client.UpdatedAt)
	if err != nil {
		return false, mapWriteError("update OAuth client", err)
	}
	return tag.RowsAffected() == 1, nil
}

// RemoveOAuthClient retires an organization's live client for a
// registration, keeping the row.
func (store *Store) RemoveOAuthClient(ctx context.Context, organizationID, client string, now time.Time) (bool, error) {
	tag, err := store.pool.Exec(ctx, `
		UPDATE apps_oauth_clients SET status = 'removed', updated_at = $3
		WHERE organization_id = $1 AND client = $2 AND status = 'active'`, organizationID, client, now)
	if err != nil {
		return false, mapWriteError("remove OAuth client", err)
	}
	return tag.RowsAffected() == 1, nil
}

func queryCredentials(ctx context.Context, db querier, sql string, args ...any) ([]signin.Credential, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapWriteError("query credentials", err)
	}
	defer rows.Close()
	credentials := []signin.Credential{}
	for rows.Next() {
		credential, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		credentials = append(credentials, credential)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query credentials: %w", err)
	}
	return credentials, nil
}

func scanCredential(rows pgx.Rows) (signin.Credential, error) {
	var credential signin.Credential
	var sealed, settings []byte
	if err := rows.Scan(&credential.ConnectionID, &credential.OrganizationID, &credential.MemberID,
		&credential.Toolkit, &credential.Status, &credential.Kind, &credential.OAuthClientID, &sealed, &settings,
		&credential.Scopes, &credential.ExpiresAt, &credential.Version, &credential.NextCheckAt,
		&credential.CheckFailures, &credential.UpdatedAt); err != nil {
		return signin.Credential{}, fmt.Errorf("scan credential: %w", err)
	}
	if err := json.Unmarshal(sealed, &credential.Sealed); err != nil {
		return signin.Credential{}, fmt.Errorf("decode credential's sealed values: %w", err)
	}
	if err := json.Unmarshal(settings, &credential.Settings); err != nil {
		return signin.Credential{}, fmt.Errorf("decode credential's settings: %w", err)
	}
	credential.ExpiresAt = utcPointer(credential.ExpiresAt)
	credential.NextCheckAt = credential.NextCheckAt.UTC()
	credential.UpdatedAt = credential.UpdatedAt.UTC()
	return credential, nil
}

func scanFlow(row pgx.Row) (signin.Flow, error) {
	var flow signin.Flow
	var settings, sealed []byte
	if err := row.Scan(&flow.StateHash, &flow.OrganizationID, &flow.MemberID, &flow.ConnectionID, &flow.Toolkit,
		&flow.OAuthClientID, &flow.CodeVerifier, &flow.RedirectURI, &flow.ReturnTo, &settings, &sealed,
		&flow.ExpiresAt); err != nil {
		return signin.Flow{}, err
	}
	if err := json.Unmarshal(settings, &flow.Settings); err != nil {
		return signin.Flow{}, fmt.Errorf("decode sign-in flow's settings: %w", err)
	}
	if err := json.Unmarshal(sealed, &flow.Sealed); err != nil {
		return signin.Flow{}, fmt.Errorf("decode sign-in flow's sealed values: %w", err)
	}
	flow.ExpiresAt = flow.ExpiresAt.UTC()
	return flow, nil
}

func scanOAuthClient(row pgx.Row) (signin.OAuthClient, error) {
	var client signin.OAuthClient
	var secret, settings, fields []byte
	if err := row.Scan(&client.ID, &client.OrganizationID, &client.Client, &client.ClientID, &secret, &settings,
		&fields, &client.Status, &client.CreatedBy, &client.CreatedAt, &client.UpdatedAt); err != nil {
		return signin.OAuthClient{}, err
	}
	if secret != nil {
		var sealed secrets.SealedData
		if err := json.Unmarshal(secret, &sealed); err != nil {
			return signin.OAuthClient{}, fmt.Errorf("decode OAuth client's secret: %w", err)
		}
		client.SealedSecret = &sealed
	}
	if err := json.Unmarshal(settings, &client.Settings); err != nil {
		return signin.OAuthClient{}, fmt.Errorf("decode OAuth client's settings: %w", err)
	}
	if err := json.Unmarshal(fields, &client.SealedFields); err != nil {
		return signin.OAuthClient{}, fmt.Errorf("decode OAuth client's sealed fields: %w", err)
	}
	client.CreatedAt = client.CreatedAt.UTC()
	client.UpdatedAt = client.UpdatedAt.UTC()
	return client, nil
}

// jsonObject encodes a map for a jsonb column that holds an object, nil
// included.
func jsonObject[V any](values map[string]V) ([]byte, error) {
	if values == nil {
		return []byte(`{}`), nil
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("encode a jsonb object: %w", err)
	}
	return encoded, nil
}

// jsonNullable encodes a value for a nullable jsonb column.
func jsonNullable[V any](value *V) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode a jsonb value: %w", err)
	}
	return encoded, nil
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func utcPointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}
